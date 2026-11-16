package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// localCLIMarker is the changed line of the synthetic repository.
const localCLIMarker = "LOCAL-CLI-MARKER-5e2a"

// localCLIRepo builds a synthetic repository: main holds src/app.go,
// refs/remotes/origin/main is main, and the branch feat changes line 2 in
// one commit. HOME points at an empty directory for the test. It returns
// the work tree.
func localCLIRepo(t *testing.T) string {
	t.Helper()
	g, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed; the local review tests need it")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("USERPROFILE", home)
	dir := t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(g, args...) //nolint:gosec // G204: the system git with fixed test arguments
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com"}
		if runtime.GOOS == "windows" {
			cmd.Env = append(cmd.Env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"), "TMP="+os.TempDir(), "TEMP="+os.TempDir())
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(content string) {
		t.Helper()
		p := filepath.Join(dir, "src", "app.go")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "core.autocrlf", "false")
	write("package main\nvar a = 1\nfunc main() {}\n")
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("checkout", "-q", "-b", "feat")
	write("package main\nvar a = 2 // " + localCLIMarker + "\nfunc main() {}\n")
	run("commit", "-q", "-am", "change a")
	return dir
}

// localAnswer is a review answer with one finding on the changed line, or
// none, and the given security concerns.
func localAnswer(finding bool, security string) string {
	issues := "  key_issues_to_review: []\n"
	if finding {
		issues = "  key_issues_to_review:\n    - relevant_file: src/app.go\n      issue_header: Check the value\n" +
			"      issue_content: The new value needs a test.\n      start_line: 2\n      end_line: 2\n"
	}
	return "```yaml\nreview:\n  estimated_effort_to_review: 2\n  relevant_tests: \"No\"\n" + issues +
		"  security_concerns: \"" + security + "\"\n```\n"
}

// localLLMEnv is a local-only configuration (WP-24d) pointing at the fake
// LLM: no git provider, only llm.* is set.
func localLLMEnv(l *fakeLLM) map[string]string {
	env := diagEnv(nil, nil)
	env["REVIEW_MCP_LLM_BASE_URL"] = l.srv.URL + "/v1"
	return env
}

type localRunResult struct {
	code       int
	out, errs  string
	loads      int
	lastStderr string
}

// localCall runs "review-mcp local ..." in-process through runWith, with a
// standard input that fails the test when it is read (the command never
// asks a question on the terminal).
func localCall(t *testing.T, env map[string]string, args ...string) localRunResult {
	t.Helper()
	var out, errb bytes.Buffer
	loads := 0
	load := func() (*config.Config, *config.Report, error) {
		loads++
		return loaderFor(env)()
	}
	code := runWith(append([]string{"local"}, args...), noStdin{t}, &out, &errb, load)
	return localRunResult{code: code, out: out.String(), errs: errb.String(), loads: loads, lastStderr: lastLine(errb.String())}
}

// noStdin fails the test when it is read.
type noStdin struct{ t *testing.T }

func (n noStdin) Read([]byte) (int, error) {
	n.t.Error("local review read standard input")
	return 0, errors.New("no standard input")
}

// TestLocalCLIExitCodes covers canary (l): with --fail-on any, one finding
// exits 3 after the review was printed; the other levels and no finding
// exit 0, as in CI mode.
// Mutation: exit 0 when --fail-on is met.
func TestLocalCLIExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
		args   []string
		code   int
		reason string
	}{
		{"any with a finding", localAnswer(true, "No"), []string{"--fail-on", "any"}, 3, "--fail-on any is met (1 findings)"},
		{"finding without fail-on", localAnswer(true, "Yes, the token is logged"), nil, 0, ""},
		{"any without findings", localAnswer(false, "No"), []string{"--fail-on", "any"}, 0, ""},
		{"security concerns", localAnswer(false, "Yes, the token is logged"), []string{"--fail-on", "security"}, 3,
			"--fail-on security is met (security concerns)"},
		{"security no", localAnswer(true, "No"), []string{"--fail-on", "security"}, 0, ""},
		{"no issue checked", localAnswer(true, "No"), []string{"--fail-on", "issue-not-compliant"}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := localCLIRepo(t)
			l := newFakeLLM(t, 200, tc.answer)
			r := localCall(t, localLLMEnv(l), append([]string{"review", "--path", dir, "--json"}, tc.args...)...)
			if r.code != tc.code {
				t.Fatalf("exit %d, want %d; stderr:\n%s", r.code, tc.code, r.errs)
			}
			var res review.Result
			if err := json.Unmarshal([]byte(r.out), &res); err != nil {
				t.Fatalf("stdout is not the result: %v", err)
			}
			validateCIOutput(t, review.ResultSchema(), r.out)
			if res.PR.Kind != "local" || res.Local == nil || res.Local.BaseRef != "origin/main" || res.Local.RepoPath != dir {
				t.Errorf("pr %+v, local %+v", res.PR, res.Local)
			}
			if tc.reason != "" && r.lastStderr != "local review: exit 3: "+tc.reason {
				t.Errorf("final line = %q", r.lastStderr)
			}
			if tc.code == 0 && r.lastStderr != localDoneLine {
				t.Errorf("final line = %q", r.lastStderr)
			}
			for _, s := range diagSecrets {
				if strings.Contains(r.out, s) || strings.Contains(r.errs, s) {
					t.Errorf("output leaks %q", s)
				}
			}
		})
	}
}

// TestLocalCLIMarkdown: without --json the client markdown goes to stdout,
// and --path defaults to the current directory (a subdirectory of the work
// tree works too).
func TestLocalCLIMarkdown(t *testing.T) {
	dir := localCLIRepo(t)
	l := newFakeLLM(t, 200, localAnswer(true, "No"))
	t.Chdir(filepath.Join(dir, "src"))
	r := localCall(t, localLLMEnv(l), "review")
	if r.code != 0 || !strings.HasPrefix(r.out, "## ") || !strings.Contains(r.out, "Check the value") {
		t.Fatalf("exit %d, stdout:\n%s\nstderr:\n%s", r.code, r.out, r.errs)
	}
	if strings.Contains(r.out, dir) {
		t.Errorf("the markdown names the absolute path:\n%s", r.out)
	}
	if l.hits.Load() == 0 {
		t.Error("the model was not called")
	}

	// A relative --path is taken against the current directory.
	t.Chdir(filepath.Dir(dir))
	r = localCall(t, localLLMEnv(l), "review", "--path", filepath.Base(dir), "--json")
	var res review.Result
	if r.code != 0 || json.Unmarshal([]byte(r.out), &res) != nil || res.Local == nil || res.Local.RepoPath != dir {
		t.Fatalf("relative path: exit %d, stderr:\n%s", r.code, r.errs)
	}
}

// TestLocalCLIUsageErrors: a refused command line exits 2 before the
// configuration is loaded.
func TestLocalCLIUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		line string
	}{
		{"no command", nil, ""},
		{"unknown command", []string{"describe"}, ""},
		{"argument", []string{"review", "extra"}, "local review: unexpected argument; the repository is given with --path"},
		{"fail-on", []string{"review", "--fail-on", "all"}, "local review: --fail-on must be security, any or issue-not-compliant"},
		{"empty fail-on", []string{"review", "--fail-on", ""}, "local review: --fail-on must be security, any or issue-not-compliant"},
		{"timeout", []string{"review", "--timeout", "30s"}, "local review: --timeout must be a Go duration of at least 1m"},
		{"max findings", []string{"review", "--max-findings", "21"}, ""},
		{"empty path", []string{"review", "--path", ""}, "local review: --path is empty"},
		{"publish", []string{"review", "--publish"}, ""},
		{"issue file", []string{"review", "--issue-context-file", filepath.Join(t.TempDir(), "missing.json")},
			"local review: --issue-context-file: the file cannot be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := localCall(t, diagEnv(nil, nil), tc.args...)
			if r.code != 2 || r.loads != 0 || r.out != "" {
				t.Fatalf("exit %d, loads %d, stdout %q; stderr:\n%s", r.code, r.loads, r.out, r.errs)
			}
			if tc.line != "" && r.lastStderr != tc.line {
				t.Errorf("final line = %q", r.lastStderr)
			}
		})
	}
}

// TestLocalCLINoRepository: no repository or no base exits 2, and nothing
// is sent to the model.
func TestLocalCLINoRepository(t *testing.T) {
	dir := localCLIRepo(t)
	l := newFakeLLM(t, 200, localAnswer(true, "No"))
	for _, tc := range []struct {
		name string
		args []string
		line string
	}{
		{"not a repository", []string{"--path", t.TempDir()}, "local review: repo_path is not inside a git work tree"},
		{"missing", []string{"--path", filepath.Join(dir, "missing")}, "local review: repo_path does not exist or is not a directory"},
		{"base", []string{"--path", dir, "--base", "release"}, "local review: base names no commit in the repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := localCall(t, localLLMEnv(l), append([]string{"review"}, tc.args...)...)
			if r.code != 2 || r.out != "" || r.lastStderr != tc.line {
				t.Fatalf("exit %d, final line %q, stdout %q", r.code, r.lastStderr, r.out)
			}
		})
	}
	if n := l.hits.Load(); n != 0 {
		t.Errorf("the model was called %d times", n)
	}

	// local.allowed_roots refuses a path outside it with exit 2.
	env := localLLMEnv(l)
	env["REVIEW_MCP_LOCAL_ALLOWED_ROOTS"] = t.TempDir()
	r := localCall(t, env, "review", "--path", dir)
	if r.code != 2 || r.lastStderr != "local review: repo_path is not under a directory of local.allowed_roots" {
		t.Errorf("allowed roots: exit %d, final line %q", r.code, r.lastStderr)
	}
}

// TestLocalCLIFailures: a failed run exits 1 with the reason on stderr,
// local.enabled false included.
func TestLocalCLIFailures(t *testing.T) {
	dir := localCLIRepo(t)

	l := newFakeLLM(t, 500, "")
	r := localCall(t, localLLMEnv(l), "review", "--path", dir, "--fail-on", "any")
	if r.code != 1 || r.out != "" || !strings.HasPrefix(r.lastStderr, "local review: ") {
		t.Errorf("model failure: exit %d, final line %q", r.code, r.lastStderr)
	}

	env := localLLMEnv(l)
	env["REVIEW_MCP_LOCAL_ENABLED"] = "false"
	// With local.enabled = false a provider is required to load at all.
	env["REVIEW_MCP_GITEA_BASE_URL"] = "https://gitea.example.invalid"
	env["REVIEW_MCP_GITEA_TOKEN"] = diagGiteaToken
	r = localCall(t, env, "review", "--path", dir)
	if r.code != 1 || r.lastStderr != localOffLine {
		t.Errorf("local off: exit %d, final line %q", r.code, r.lastStderr)
	}

	env = localLLMEnv(l)
	env["REVIEW_MCP_LLM_BASE_URL"] = "not a url"
	r = localCall(t, env, "review", "--path", dir)
	if r.code != 1 || r.loads != 1 {
		t.Errorf("invalid configuration: exit %d, loads %d", r.code, r.loads)
	}
}

// TestLocalCLIUncommitted: --uncommitted=false leaves an unstaged edit out;
// by default it is reviewed.
func TestLocalCLIUncommitted(t *testing.T) {
	dir := localCLIRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "src", "app.go"), []byte("package main\nvar a = 3\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := newFakeLLM(t, 200, localAnswer(false, "No"))
	for _, tc := range []struct {
		flag string
		want bool
	}{{"--uncommitted=true", true}, {"--uncommitted=false", false}} {
		r := localCall(t, localLLMEnv(l), "review", "--path", dir, "--json", tc.flag)
		var res review.Result
		if r.code != 0 || json.Unmarshal([]byte(r.out), &res) != nil || res.Local == nil {
			t.Fatalf("%s: exit %d, stderr:\n%s", tc.flag, r.code, r.errs)
		}
		if res.Local.Uncommitted != tc.want {
			t.Errorf("%s: uncommitted %v", tc.flag, res.Local.Uncommitted)
		}
	}
}

// TestLocalCLIUsageText: "review-mcp local" prints the usage with the exit
// codes, and the top-level usage names the command.
func TestLocalCLIUsageText(t *testing.T) {
	r := localCall(t, diagEnv(nil, nil))
	for _, s := range []string{"review-mcp local review [--path DIR]", "--fail-on LEVEL", "exit codes:", "  3  a --fail-on condition is met"} {
		if !strings.Contains(r.errs, s) {
			t.Errorf("usage lacks %q", s)
		}
	}
	var out, errb bytes.Buffer
	if code := runWith([]string{"unknown"}, strings.NewReader(""), &out, &errb, loaderFor(nil)); code != 2 ||
		!strings.Contains(errb.String(), "local review [--path DIR]") {
		t.Errorf("top-level usage: exit %d\n%s", code, errb.String())
	}
}
