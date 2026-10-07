package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cacheTag is review-mcp's CACHEDIR.TAG, as gitctx writes it.
const cacheTag = "Signature: 8a477f597d28d172789f06886806bc55\n# This file is a cache directory tag created by review-mcp (repository context).\n"

// makeCacheEntry lays out one cached repository the way gitctx does.
func makeCacheEntry(t *testing.T, root, rel string, size int, lastUsed time.Time) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Join(p, "git", "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "git", "objects", "pack"), make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(p, "last-used")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(marker, lastUsed, lastUsed); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiagCache(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repos")
	env := diagEnv(newFakeGitea(t), nil)
	env["REVIEW_MCP_CONTEXT_REPO_CACHE_DIR"] = dir
	env["REVIEW_MCP_CONTEXT_REPO_IDLE_DAYS"] = "7"

	// A cache that does not exist yet is empty.
	code, out, errs := diag(env, "cache")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	m, _ := decodeReport(t, out)
	if m["cache_dir"] != dir || len(asMaps(t, m["repos"])) != 0 || m["enabled"] != false {
		t.Errorf("empty report = %v", m)
	}
	if _, ok := m["pruned"]; ok {
		t.Error("pruned is present without --prune")
	}
	if g, _ := m["git"].(string); !strings.HasPrefix(g, "git ") && !strings.HasPrefix(g, "unavailable: ") {
		t.Errorf("git = %q", g)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CACHEDIR.TAG"), []byte(cacheTag), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := makeCacheEntry(t, dir, "your-gitea.example/_/owner/old", 300, now.Add(-10*24*time.Hour))
	makeCacheEntry(t, dir, "your-gitea.example/_/owner/fresh", 200, now.Add(-time.Hour))
	// git's empty home (<cache_dir>/.home) is never a repository, even when
	// something in it looks like one.
	home := makeCacheEntry(t, dir, ".home/h/_/o/r", 100, now.Add(-30*24*time.Hour))

	code, out, errs = diag(env, "cache")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	t.Logf("sample diag cache output:\n%s", out)
	m, _ = decodeReport(t, out)
	repos := asMaps(t, m["repos"])
	if len(repos) != 2 || repos[1]["repo"] != "your-gitea.example/_/owner/old" || repos[1]["idle_days"] != float64(10) ||
		repos[1]["size_bytes"] != float64(300) || m["total_bytes"] != float64(500) {
		t.Errorf("report = %v", m)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("listing deleted something")
	}

	code, out, errs = diag(env, "cache", "--prune")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	m, _ = decodeReport(t, out)
	pruned, repos := asMaps(t, m["pruned"]), asMaps(t, m["repos"])
	if len(pruned) != 1 || pruned[0]["repo"] != "your-gitea.example/_/owner/old" ||
		len(repos) != 1 || repos[0]["repo"] != "your-gitea.example/_/owner/fresh" {
		t.Errorf("after --prune: %v", m)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the idle repository is still there")
	}
	if _, err := os.Stat(home); err != nil {
		t.Error("--prune entered .home")
	}

	code, out, _ = diag(env, "cache", "--prune")
	m, _ = decodeReport(t, out)
	if code != 0 || len(asMaps(t, m["pruned"])) != 0 {
		t.Errorf("second --prune: exit %d, %v", code, m)
	}
}

func TestDiagCacheRefusesForeignDir(t *testing.T) {
	dir := t.TempDir()
	victim := makeCacheEntry(t, dir, "projects/team/app", 10, time.Now().Add(-400*24*time.Hour))
	env := diagEnv(newFakeGitea(t), nil)
	env["REVIEW_MCP_CONTEXT_REPO_CACHE_DIR"] = dir
	code, out, errs := diag(env, "cache", "--prune")
	if code != 1 || out != "" || !strings.Contains(errs, diagCacheFailed) {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(victim, "git")); err != nil {
		t.Fatal("a foreign directory was pruned")
	}
}

func TestDiagCacheUsage(t *testing.T) {
	env := diagEnv(newFakeGitea(t), nil)
	if code, _, errs := diag(env, "cache", "extra"); code != 2 || !strings.Contains(errs, "takes no arguments") {
		t.Errorf("exit %d, stderr %q", code, errs)
	}
	if code, _, _ := diag(env, "cache", "--nope"); code != 2 {
		t.Errorf("unknown flag: exit %d", code)
	}
	if code, _, errs := diag(map[string]string{}, "cache"); code != 1 || errs == "" {
		t.Errorf("invalid configuration: exit %d, stderr %q", code, errs)
	}
}
