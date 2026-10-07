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
	dir string
}

// netPolicy are the non-secret settings of a command that talks to the
// provider.
type netPolicy struct {
	allowHTTP          bool
	caCert             string
	insecureSkipVerify bool
}

// safetyArgs are the "-c" settings of every git command (RC-3, RC-4). They
// are not secret, so they are arguments, where a test can see them.
func safetyArgs(np *netPolicy) []string {
	a := []string{
		"-c", "credential.helper=",
		"-c", "core.askPass=",
		"-c", "core.hooksPath=" + os.DevNull, // NUL on Windows
		"-c", "core.fsmonitor=false",
		"-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "http.followRedirects=false",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
	}
	if np == nil {
		return a
	}
	if np.allowHTTP {
		a = append(a, "-c", "protocol.http.allow=always")
	}
	if np.caCert != "" {
		a = append(a, "-c", "http.sslCAInfo="+np.caCert)
	}
	if np.insecureSkipVerify {
		a = append(a, "-c", "http.sslVerify=false")
	}
	return a
}

// childEnv builds the environment of a git process from an allowlist (§3
// WP-11a): PATH, HOME (USERPROFILE and SYSTEMROOT on Windows), LANG=C, the
// prompt guards and the configuration entries. The parent's environment is
// never passed on: it may hold other tokens.
func childEnv(config [][2]string) []string {
	var env []string
	pass := func(name string) {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	pass("PATH")
	pass("HOME")
	if runtime.GOOS == "windows" {
		pass("USERPROFILE")
		pass("SYSTEMROOT")
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
type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
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
}

// run executes git. It never uses a shell. A failure is reported in
// result.err as the raw *exec.ExitError (or the context error); the caller
// classifies it with failure.
func run(ctx context.Context, gitPath string, c gitCmd) result {
	argv := append(safetyArgs(c.net), c.args...)
	//nolint:gosec // G204: the binary is the configured or looked-up git, and every argument is built here; no shell is involved.
	cmd := exec.CommandContext(ctx, gitPath, argv...)
	cmd.Env = childEnv(c.config)
	cmd.Dir = c.dir
	cmd.Stdin = nil
	cmd.WaitDelay = waitDelay
	killGroup(cmd)
	stdout := &limitedBuffer{max: maxStdout}
	stderr := &limitedBuffer{max: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return result{stdout: stdout.buf.Bytes(), stderr: stderr.buf.String(), err: err}
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
