package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// hookSample returns the sh block under the given heading of
// docs/local-review.md.
func hookSample(t *testing.T, heading string) string {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "local-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile("(?s)\n### " + regexp.QuoteMeta(heading) + "\n.*?```sh\n(.*?)```")
	m := re.FindSubmatch(doc)
	if m == nil {
		t.Fatalf("no sh block under %q", heading)
	}
	return string(m[1])
}

// TestLocalCLIHookSamples runs the pre-push hook sample of the docs for
// this OS (the POSIX one, or on Windows the Git for Windows one) in a
// throwaway repository with a bare remote: a finding blocks the push
// (exit 3 of review-mcp), and git push --no-verify skips the hook.
func TestLocalCLIHookSamples(t *testing.T) {
	posix, windows := hookSample(t, "POSIX shells (Linux, macOS)"), hookSample(t, "Windows (Git for Windows)")
	sample, exe := posix, "review-mcp"
	if runtime.GOOS == "windows" {
		sample, exe = windows, "review-mcp.exe"
	}
	g, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed; the hook test needs it")
	}
	// The binary is built under the name the sample calls, on the PATH of
	// the hook.
	binDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(binDir, exe), ".") //nolint:gosec // G204: fixed arguments; the output is under t.TempDir()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	l := newFakeLLM(t, 200, localAnswer(true, "No"))
	home, root := t.TempDir(), t.TempDir()
	env := []string{"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com"}
	if runtime.GOOS == "windows" {
		env = append(env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"), "TMP="+os.TempDir(), "TEMP="+os.TempDir(),
			"USERPROFILE="+home)
	}
	for k, v := range localLLMEnv(l) {
		env = append(env, k+"="+v)
	}
	work := filepath.Join(root, "work")
	git := func(dir string, args ...string) (string, error) {
		cmd := exec.Command(g, args...) //nolint:gosec // G204: the system git with fixed test arguments
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(dir string, args ...string) string {
		t.Helper()
		out, err := git(dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(out)
	}
	write := func(content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(work, "src"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, "src", "app.go"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must(root, "init", "-q", "--bare", "-b", "main", "remote.git")
	must(root, "clone", "-q", "remote.git", "work")
	must(work, "config", "core.autocrlf", "false")
	write("package main\nvar a = 1\nfunc main() {}\n")
	must(work, "add", "-A")
	must(work, "commit", "-q", "-m", "base")
	must(work, "push", "-q", "origin", "HEAD:main")
	must(work, "checkout", "-q", "-b", "feat")
	write("package main\nvar a = 2 // " + localCLIMarker + "\nfunc main() {}\n")
	must(work, "commit", "-q", "-am", "change a")
	head := must(work, "rev-parse", "HEAD")

	// The hook, byte for byte from the docs, with LF line endings.
	hook := filepath.Join(work, ".git", "hooks", "pre-push")
	if err := os.WriteFile(hook, []byte(sample), 0o700); err != nil { //nolint:gosec // G306: a git hook must be executable
		t.Fatal(err)
	}

	out, err := git(work, "push", "origin", "feat")
	t.Logf("git push:\n%s", out)
	if err == nil {
		t.Fatal("the push went ahead despite the finding")
	}
	for _, s := range []string{"local review: exit 3: --fail-on any is met (1 findings)",
		"pre-push: review-mcp reported findings; push with --no-verify to skip the review once"} {
		if !strings.Contains(out, s) {
			t.Errorf("push output lacks %q", s)
		}
	}
	if _, err := git(filepath.Join(root, "remote.git"), "rev-parse", "--verify", "-q", "refs/heads/feat"); err == nil {
		t.Error("the remote has the branch after a blocked push")
	}

	out, err = git(work, "push", "--no-verify", "origin", "feat")
	t.Logf("git push --no-verify:\n%s", out)
	if err != nil {
		t.Fatalf("push --no-verify failed: %v", err)
	}
	if got := must(filepath.Join(root, "remote.git"), "rev-parse", "refs/heads/feat"); got != head {
		t.Errorf("remote feat = %s, want %s", got, head)
	}
	if strings.Contains(out, "local review:") {
		t.Error("--no-verify ran the hook")
	}
}
