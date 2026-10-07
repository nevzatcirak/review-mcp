package gitctx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MinGitVersion is the oldest git that is used: GIT_CONFIG_COUNT, which
// carries the credentials, needs 2.31 (RC-2).
const MinGitVersion = "2.31"

const (
	minGitMajor, minGitMinor = 2, 31
	// probeTimeout bounds "git --version".
	probeTimeout = 10 * time.Second
	// waitDelay bounds the wait for git's output pipes after git was killed:
	// a helper it started (git-remote-https) may still hold them open.
	waitDelay = 2 * time.Second
	// maxStderr and maxStdout cap what is read from a git command. Stderr is
	// only classified and then dropped.
	maxStderr = 64 << 10
	maxStdout = 1 << 20
)

// gitCmd is one git invocation.
type gitCmd struct {
	args []string
	// config are extra configuration entries passed through the
	// environment (GIT_CONFIG_COUNT / GIT_CONFIG_KEY_n / GIT_CONFIG_VALUE_n).
	// This is the only way a credential reaches git.
	config [][2]string
	// net is the network policy of the command (fetch); nil for local
	// commands.
	net *netPolicy
	// offline marks a read of the cache (grep, cat-file, rev-list, ls-tree):
	// no protocol may be used at all (protocol.allow=never with no https or
	// http allow), and GIT_NO_LAZY_FETCH=1, so a blob the partial clone
	// lacks fails locally and at once instead of being fetched. It never
	// carries credentials (config is empty).
	offline bool
	// stdin is fed to git's standard input; nil means none.
	stdin []byte
	// maxStdout caps the output read; 0 means maxStdout. When the cap is
	// reached git is stopped and result.truncated is set: that is not an
	// error.
	maxStdout int
	dir       string
	// home is the cache's empty home directory (<cache_dir>/.home), passed
	// as HOME and XDG_CONFIG_HOME; "" passes neither (the version probe,
	// which runs before the cache is known).
	home string
}

// netPolicy are the non-secret settings of a command that talks to the
// provider. They mirror the provider's HTTP client: the same CA file and
// the same (explicit, warned) insecure_skip_verify opt-in, so git is never
// stricter or looser than it.
type netPolicy struct {
	allowHTTP          bool
	caCert             string
	insecureSkipVerify bool
}

// testExtraConfig are configuration entries a test adds to every git
// command, ahead of the command's own (the redirect-guard test injects a
// url.<other>.insteadOf with it). It is always empty outside tests.
var testExtraConfig [][2]string

// safetyArgs are the "-c" settings of every git command (RC-3, RC-4). They
// are not secret, so they are arguments, where a test can see them. goos is
// runtime.GOOS (a parameter so that the Windows settings can be tested
// anywhere).
func safetyArgs(np *netPolicy, goos string) []string {
	return safetyArgsFor(np, goos, false)
}

// safetyArgsFor is safetyArgs; offline drops the https allowance, so that
// protocol.allow=never leaves every transport forbidden (the lazy fetch of a
// missing blob needs one).
func safetyArgsFor(np *netPolicy, goos string, offline bool) []string {
	a := []string{
		"-c", "credential.helper=",
		"-c", "core.askPass=",
		"-c", "core.hooksPath=" + os.DevNull, // NUL on Windows
		"-c", "core.fsmonitor=false",
		"-c", "protocol.allow=never",
	}
	if !offline {
		a = append(a, "-c", "protocol.https.allow=always")
	}
	a = append(a,
		"-c", "http.followRedirects=false",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
	)
	if np == nil {
		return a
	}
	if np.allowHTTP {
		a = append(a, "-c", "protocol.http.allow=always")
	}
	if np.caCert != "" {
		a = append(a, "-c", "http.sslCAInfo="+np.caCert)
		if goos == "windows" {
			// Git for Windows defaults to schannel, which ignores
			// http.sslCAInfo; openssl honours the file.
			a = append(a, "-c", "http.sslBackend=openssl")
		}
	}
	if np.insecureSkipVerify {
		a = append(a, "-c", "http.sslVerify=false")
	}
	return a
}

// proxyVars are passed to git when set, in both cases, like the provider's
// HTTP client honours them. Their values go to the child only and are never
// logged.
var proxyVars = []string{
	"http_proxy", "https_proxy", "no_proxy", "all_proxy",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
}

// childEnv builds the environment of a git process from an allowlist (§3
// WP-11a and its review): PATH, the proxy variables, SYSTEMROOT on Windows,
// HOME and XDG_CONFIG_HOME pointing at the cache's empty home directory
// (USERPROFILE too on Windows), GIT_CONFIG_NOSYSTEM=1 except on Windows,
// LANG=C, the prompt guards and the configuration entries. The parent's
// environment is never passed on (it may hold other tokens), and neither is
// the user's real home: git must not read the user's global configuration
// (url.*.insteadOf, http.*, include.path and the like).
//
// Git for Windows keeps its TLS backend and CA settings in the system
// configuration, so it is read there; the ls-remote --get-url guard of
// Ensure catches a url.*.insteadOf in it.
func childEnv(home string, config [][2]string, goos string) []string {
	var env []string
	parent := os.Environ()
	pass := func(name string) {
		for _, kv := range parent {
			k, _, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				continue
			}
			// Windows names are case-insensitive: one entry per variable.
			if k == name || (goos == "windows" && strings.EqualFold(k, name)) {
				env = append(env, kv)
				return
			}
		}
	}
	pass("PATH")
	if goos == "windows" {
		pass("SYSTEMROOT")
	}
	seen := map[string]bool{}
	for _, name := range proxyVars {
		key := name
		if goos == "windows" {
			key = strings.ToUpper(name)
		}
		if !seen[key] {
			seen[key] = true
			pass(name)
		}
	}
	if home != "" {
		env = append(env, "HOME="+home, "XDG_CONFIG_HOME="+home)
		if goos == "windows" {
			env = append(env, "USERPROFILE="+home)
		}
	}
	if goos != "windows" {
		env = append(env, "GIT_CONFIG_NOSYSTEM=1")
	}
	env = append(env,
		"LANG=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
	)
	if len(config) > 0 {
		env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
		for i, kv := range config {
			n := strconv.Itoa(i)
			env = append(env, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
		}
	}
	return env
}

// limitedBuffer keeps the first max bytes written to it and drops the rest.
// onFull, when set, is called once when a write does not fit.
type limitedBuffer struct {
	buf    bytes.Buffer
	max    int
	full   bool
	onFull func()
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	room := b.max - b.buf.Len()
	if room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	if len(p) > room && !b.full {
		b.full = true
		if b.onFull != nil {
			b.onFull()
		}
	}
	return len(p), nil
}

// result is the outcome of one git command. stderr never leaves this
// package: it is classified by the caller and dropped.
type result struct {
	stdout []byte
	stderr string
	err    error
	// truncated: the output reached gitCmd.maxStdout and git was stopped; err
	// is nil then.
	truncated bool
}

// run executes git. It never uses a shell. A failure is reported in
// result.err as the raw *exec.ExitError (or the context error); the caller
// classifies it with failure.
func run(ctx context.Context, gitPath string, c gitCmd) result {
	argv := append(safetyArgsFor(c.net, runtime.GOOS, c.offline), c.args...)
	limit := c.maxStdout
	if limit <= 0 {
		limit = maxStdout
	}
	// A command with its own cap is stopped when the cap is reached.
	stopCtx, stop := context.WithCancel(ctx)
	defer stop()
	//nolint:gosec // G204: the binary is the configured or looked-up git, and every argument is built here; no shell is involved.
	cmd := exec.CommandContext(stopCtx, gitPath, argv...)
	cmd.Env = childEnv(c.home, append(append([][2]string(nil), testExtraConfig...), c.config...), runtime.GOOS)
	if c.offline {
		cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
	}
	cmd.Dir = c.dir
	if c.stdin != nil {
		cmd.Stdin = bytes.NewReader(c.stdin)
	}
	cmd.WaitDelay = waitDelay
	killGroup(cmd)
	stdout := &limitedBuffer{max: limit}
	if c.maxStdout > 0 {
		stdout.onFull = stop
	}
	stderr := &limitedBuffer{max: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	truncated := c.maxStdout > 0 && stdout.full
	if truncated && ctx.Err() == nil {
		err = nil // stopped by the cap, not a failure
	} else if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return result{stdout: stdout.buf.Bytes(), stderr: stderr.buf.String(), err: err, truncated: truncated}
}

// failure turns a failed result into an error with a fixed reason. The
// context's own error is returned when the caller's context was canceled.
func (r result) failure() (classified, error) {
	switch {
	case errors.Is(r.err, context.DeadlineExceeded):
		return classified{reason: ReasonTimeout}, fail(ReasonTimeout)
	case errors.Is(r.err, context.Canceled):
		return classified{reason: ReasonGitFailed}, context.Canceled
	}
	c := classifyStderr(r.stderr)
	return c, fail(c.reason)
}

// ---- version check (RC-2) ----

// probe is the once-per-process result of "git --version" for one binary.
type probe struct {
	once    sync.Once
	path    string // resolved binary
	version string // "2.45.1"; "" when unavailable
}

var (
	probesMu sync.Mutex
	probes   = map[string]*probe{}
)

// gitProbe returns the version check for gitPath ("" = "git" in PATH). The
// check runs once per process and binary.
func gitProbe(gitPath string) *probe {
	probesMu.Lock()
	p, ok := probes[gitPath]
	if !ok {
		p = &probe{}
		probes[gitPath] = p
	}
	probesMu.Unlock()
	p.once.Do(func() { p.path, p.version = checkGit(gitPath) })
	return p
}

func (p *probe) ok() bool { return p.version != "" }

var versionRE = regexp.MustCompile(`^git version (\d+)\.(\d+)(?:\.(\d+))?`)

// checkGit resolves and runs "git --version". It returns "" as the version
// when git is missing, does not answer, or is older than MinGitVersion.
func checkGit(gitPath string) (path, version string) {
	path = gitPath
	if path == "" {
		p, err := exec.LookPath("git")
		if err != nil {
			return "", ""
		}
		path = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	res := run(ctx, path, gitCmd{args: []string{"--version"}})
	if res.err != nil {
		return path, ""
	}
	v, ok := parseGitVersion(string(res.stdout))
	if !ok {
		return path, ""
	}
	return path, v
}

// parseGitVersion reads "git version 2.45.1" (also "2.39.3 (Apple Git-146)"
// and "2.45.1.windows.1") and reports whether it is at least MinGitVersion.
func parseGitVersion(out string) (string, bool) {
	m := versionRE.FindStringSubmatch(out)
	if m == nil {
		return "", false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
		return "", false
	}
	v := m[1] + "." + m[2]
	if m[3] != "" {
		v += "." + m[3]
	}
	return v, true
}

// GitStatus describes the git this package would use (gitPath "" = "git"
// in PATH): "git <version>", or "unavailable: <fixed reason>". The check
// runs once per process.
func GitStatus(gitPath string) string {
	p := gitProbe(gitPath)
	if p.ok() {
		return "git " + p.version
	}
	return "unavailable: git " + MinGitVersion + " or later is required"
}

// Status is the server_info value of context.repo when it is enabled (§3.0
// item 4): "enabled, git <version>" or "enabled, unavailable: <fixed
// reason>".
func Status() string { return "enabled, " + GitStatus("") }
