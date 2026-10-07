package gitctx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func asError(err error, target **Error) bool { return errors.As(err, target) }

// makeEntry creates a cache entry at rel ("host/ns/repo") whose bare
// repository holds size bytes and whose marker was touched at lastUsed. It
// returns the entry directory.
func makeEntry(t *testing.T, root, rel string, size int, lastUsed time.Time) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Join(p, gitDirName, "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, gitDirName, "objects", "pack"), make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(p, markerName)
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(marker, lastUsed, lastUsed); err != nil {
		t.Fatal(err)
	}
	return p
}

func cacheRunner(t *testing.T, opts Options) (*Runner, string) {
	t.Helper()
	opts.CacheDir = filepath.Join(t.TempDir(), "cache")
	r := New(opts)
	root, err := r.openRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	return r, root
}

func exists(p string) bool {
	_, err := os.Lstat(p) //nolint:gosec // G703: test paths under t.TempDir()
	return err == nil
}

func repoNames(es []Entry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.Repo)
	}
	return strings.Join(out, ",")
}

// TestIdleSweep: repositories idle longer than idle_days are deleted, with
// their empty parent directories; the others stay.
func TestIdleSweep(t *testing.T) {
	r, root := cacheRunner(t, Options{IdleDays: 7})
	now := time.Now()
	old := makeEntry(t, root, "your-gitea.example/owner/old", 100, now.Add(-8*24*time.Hour))
	keep := makeEntry(t, root, "bitbucket.example.com/PROJ/keep", 100, now.Add(-6*24*time.Hour))

	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := repoNames(list); got != "bitbucket.example.com/PROJ/keep,your-gitea.example/owner/old" {
		t.Fatalf("List = %s", got)
	}
	if list[1].IdleDays != 8 || list[1].SizeBytes != 100 || list[1].InUse {
		t.Errorf("old entry = %+v", list[1])
	}

	removed, err := r.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if got := repoNames(removed); got != "your-gitea.example/owner/old" {
		t.Errorf("removed = %s", got)
	}
	if exists(old) || exists(filepath.Join(root, "your-gitea.example")) {
		t.Error("the idle entry or its empty parents remain")
	}
	if !exists(filepath.Join(keep, gitDirName)) || !exists(filepath.Join(root, tagName)) {
		t.Error("a live entry or the cache tag was removed")
	}
}

// TestLRUSweep: above max_cache_mb, least-recently-used repositories go
// first until the cache fits.
func TestLRUSweep(t *testing.T) {
	r, root := cacheRunner(t, Options{IdleDays: 7, MaxCacheBytes: 9000})
	now := time.Now()
	a := makeEntry(t, root, "h/ns/a", 4000, now.Add(-3*time.Hour))
	b := makeEntry(t, root, "h/ns/b", 4000, now.Add(-2*time.Hour))
	c := makeEntry(t, root, "h/ns/c", 4000, now.Add(-time.Hour))
	removed, err := r.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if got := repoNames(removed); got != "h/ns/a" {
		t.Errorf("removed = %s, want h/ns/a", got)
	}
	if exists(a) || !exists(b) || !exists(c) {
		t.Error("wrong entries removed")
	}

	r.opts.MaxCacheBytes = 3000
	removed, _ = r.Prune()
	if got := repoNames(removed); got != "h/ns/b,h/ns/c" {
		t.Errorf("removed = %s, want h/ns/b,h/ns/c", got)
	}
}

// TestSweepSparesInUse: neither sweep removes the caller's own entry, an
// entry with a fresh lock, or (LRU) an entry used within the last ten
// minutes; a stale lock does not protect an entry.
func TestSweepSparesInUse(t *testing.T) {
	r, root := cacheRunner(t, Options{IdleDays: 1, MaxCacheBytes: 1})
	now := time.Now()
	own := makeEntry(t, root, "h/ns/own", 100, now.Add(-48*time.Hour))
	locked := makeEntry(t, root, "h/ns/locked", 100, now.Add(-48*time.Hour))
	recent := makeEntry(t, root, "h/ns/recent", 100, now.Add(-time.Minute))
	stale := makeEntry(t, root, "h/ns/stale", 100, now.Add(-48*time.Hour))
	if err := os.WriteFile(filepath.Join(locked, lockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	staleLock := filepath.Join(stale, lockName)
	if err := os.WriteFile(staleLock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-lockStaleAfter - time.Minute)
	if err := os.Chtimes(staleLock, old, old); err != nil {
		t.Fatal(err)
	}
	list, _ := r.List()
	for _, e := range list {
		if want := e.Repo == "h/ns/locked"; e.InUse != want {
			t.Errorf("%s in_use = %v", e.Repo, e.InUse)
		}
	}

	removed := r.sweep(root, own)
	if got := repoNames(removed); got != "h/ns/stale" {
		t.Errorf("removed = %s, want h/ns/stale", got)
	}
	if !exists(own) || !exists(locked) || !exists(recent) || exists(stale) {
		t.Error("an entry in use was removed, or the stale one stayed")
	}
}

// TestDeletionNeverFollowsSymlinks: a symlink inside a repository is removed
// as a link, never followed; a symlinked entry is not touched; files that
// are not the cache's own survive.
func TestDeletionNeverFollowsSymlinks(t *testing.T) {
	r, root := cacheRunner(t, Options{IdleDays: 1})
	outside := t.TempDir()
	victim := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	e := makeEntry(t, root, "h/ns/repo", 10, old)
	if err := os.Symlink(outside, filepath.Join(e, gitDirName, "objects", "link")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	// A symlinked git directory and a symlinked entry.
	e2 := makeEntry(t, root, "h/ns/linkedgit", 10, old)
	if err := os.RemoveAll(filepath.Join(e2, gitDirName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e2, gitDirName)); err != nil {
		t.Fatal(err)
	}
	fakeEntry := t.TempDir()
	makeEntry(t, fakeEntry, "x", 10, old)
	if err := os.Symlink(filepath.Join(fakeEntry, "x"), filepath.Join(root, "h", "ns", "linked")); err != nil {
		t.Fatal(err)
	}
	// A foreign file inside an entry.
	if err := os.WriteFile(filepath.Join(e, "notes.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := r.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if got := repoNames(removed); got != "h/ns/linkedgit,h/ns/repo" {
		t.Errorf("removed = %s", got)
	}
	if !exists(victim) || !exists(filepath.Join(fakeEntry, "x", gitDirName)) {
		t.Fatal("deletion followed a symlink")
	}
	if !exists(filepath.Join(root, "h", "ns", "linked")) {
		t.Error("a symlinked entry was touched")
	}
	if exists(filepath.Join(e, gitDirName)) || !exists(filepath.Join(e, "notes.txt")) {
		t.Error("the git directory stayed, or a foreign file was deleted")
	}
}

// TestForeignCacheDirRefused: a non-empty directory without review-mcp's
// cache tag is never used or swept.
func TestForeignCacheDirRefused(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-365 * 24 * time.Hour)
	victim := makeEntry(t, dir, "projects/team/app", 10, old)
	r := New(Options{CacheDir: dir, IdleDays: 1})
	if _, err := r.List(); ReasonOf(err) != ReasonCache {
		t.Errorf("List err = %v", err)
	}
	if _, err := r.Prune(); ReasonOf(err) != ReasonCache {
		t.Errorf("Prune err = %v", err)
	}
	f := newFakeGit(t, fakeBehavior{SHA: fakeSHA})
	r.opts.GitPath = f.path
	_, err := r.Ensure(context.Background(), giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA})
	wantReason(t, err, ReasonCache)
	if !exists(filepath.Join(victim, gitDirName)) {
		t.Fatal("a foreign directory was swept")
	}

	// A missing cache directory lists as empty; a new one gets the tag and
	// mode 0700.
	r = New(Options{CacheDir: filepath.Join(t.TempDir(), "a", "b")})
	if list, err := r.List(); err != nil || len(list) != 0 {
		t.Errorf("List of a missing dir = %v, %v", list, err)
	}
	root, err := r.openRoot(true)
	if err != nil || !hasTag(root) {
		t.Fatalf("openRoot = %v; tag %v", err, hasTag(root))
	}
	if fi, _ := os.Stat(root); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("cache dir mode = %v, want 0700", fi.Mode().Perm())
	}
}

// TestPathEscapeRejected: names that could leave the cache directory are
// refused, and within() is lexical and strict.
func TestPathEscapeRejected(t *testing.T) {
	for _, bad := range []struct{ ns, name string }{
		{"..", "repo"}, {"owner", ".."}, {".", "repo"}, {"a/b", "repo"}, {`a\b`, "repo"},
		{"owner", "re/po"}, {"", "repo"}, {"owner", ""}, {"own er", "repo"}, {"owner", "repo\x00"},
	} {
		repo := giteaRepo("https://your-gitea.example")
		repo.Namespace, repo.Name = bad.ns, bad.name
		if _, err := newPlan(repo, PR{Number: 7, HeadSHA: fakeSHA}); ReasonOf(err) != ReasonUnsupported {
			t.Errorf("%q/%q: err = %v, want unsupported", bad.ns, bad.name, err)
		}
	}
	root := filepath.Join(string(filepath.Separator), "cache")
	for p, want := range map[string]bool{
		filepath.Join(root, "h", "n", "r"): true,
		root:                               false,
		filepath.Join(root, "..", "etc"):   false,
		filepath.Join(root, "h", "..", "..", "etc"):         false,
		filepath.Join(string(filepath.Separator), "cachex"): false,
	} {
		if got := within(root, p); got != want {
			t.Errorf("within(%s) = %v, want %v", p, got, want)
		}
	}
}

// TestPlan: the clone URL comes from the base URL and the resolved names
// only (RC-4), with each provider's PR ref (RC-5) and first auth scheme.
func TestPlan(t *testing.T) {
	cases := []struct {
		repo          Repo
		url, ref, rel string
		first         scheme
		allowHTTP     bool
	}{
		{giteaRepo("https://your-gitea.example/"), "https://your-gitea.example/owner/repo.git", "refs/pull/7/head",
			"your-gitea.example/owner/repo", schemeToken, false},
		{bbsRepo("https://Bitbucket.Example.com/bb"), "https://Bitbucket.Example.com/bb/scm/PROJ/repo.git", "refs/pull-requests/7/from",
			"bitbucket.example.com/PROJ/repo", schemeBearer, false},
		{func() Repo { r := bbsRepo("http://127.0.0.1:7990"); r.Namespace = "~jdoe"; return r }(),
			"http://127.0.0.1:7990/scm/~jdoe/repo.git", "refs/pull-requests/7/from", "127.0.0.1_7990/~jdoe/repo", schemeBearer, true},
	}
	for _, tc := range cases {
		p, err := newPlan(tc.repo, PR{Number: 7, HeadSHA: strings.ToUpper(fakeSHA)})
		if err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		if p.cloneURL != tc.url || p.remoteRef != tc.ref || filepath.ToSlash(p.rel) != tc.rel ||
			p.schemes[0] != tc.first || p.schemes[1] != schemeBasic || p.net.allowHTTP != tc.allowHTTP || p.headSHA != fakeSHA {
			t.Errorf("plan = %+v, want url %s ref %s rel %s", p, tc.url, tc.ref, tc.rel)
		}
	}
}

func TestClassifyStderr(t *testing.T) {
	for stderr, want := range map[string]classified{
		"fatal: could not read Username for 'https://h': terminal prompts disabled": {ReasonAuth, true},
		"fatal: Authentication failed for 'https://h/o/r.git/'":                     {ReasonAuth, true},
		"fatal: unable to access 'x': The requested URL returned error: 401":        {ReasonAuth, true},
		"fatal: unable to access 'x': The requested URL returned error: 403":        {ReasonAuth, false},
		"fatal: unable to access 'x': The requested URL returned error: 301":        {ReasonRedirect, false},
		"fatal: repository 'https://h/o/r.git/' not found":                          {ReasonNotFound, false},
		"fatal: couldn't find remote ref refs/pull/9/head":                          {ReasonNotFound, false},
		"fatal: unable to access 'x': Operation timed out after 5000 milliseconds":  {ReasonTimeout, false},
		"fatal: the remote end hung up unexpectedly":                                {ReasonGitFailed, false},
		"": {ReasonGitFailed, false},
	} {
		if got := classifyStderr(stderr); got != want {
			t.Errorf("classify(%q) = %+v, want %+v", stderr, got, want)
		}
	}
	for r := range notes {
		if !strings.HasPrefix(Note(r), "repository context skipped: ") {
			t.Errorf("note for %s = %q", r, Note(r))
		}
	}
	if Note("nonsense") != notes[ReasonGitFailed] || ReasonOf(errors.New("x")) != ReasonGitFailed || ReasonOf(nil) != "" {
		t.Error("fallbacks wrong")
	}
}

// TestLockExclusive: a second taker fails while the lock is held and
// succeeds after release.
func TestLockExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), lockName)
	l, ok, err := tryLock(p)
	if err != nil || !ok {
		t.Fatalf("tryLock = %v, %v", ok, err)
	}
	if _, ok, _ := tryLock(p); ok {
		t.Fatal("the lock was taken twice")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := acquireLock(ctx, p); ReasonOf(err) != ReasonBusy {
		t.Errorf("acquireLock while held = %v, want busy", err)
	}
	l.release()
	l2, ok, err := tryLock(p)
	if err != nil || !ok {
		t.Fatalf("tryLock after release = %v, %v", ok, err)
	}
	l2.release()
}

// TestChildEnvAllowlist checks childEnv directly, including the git config
// entries.
func TestChildEnvAllowlist(t *testing.T) {
	t.Setenv("REVIEW_MCP_GITEA_TOKEN", testToken)
	t.Setenv("GIT_SSH_COMMAND", "evil")
	env := childEnv([][2]string{{"http.extraHeader", ""}, {"http.extraHeader", "X: y"}})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, testToken) || strings.Contains(joined, "GIT_SSH_COMMAND") {
		t.Errorf("parent variables leaked: %v", env)
	}
	for _, want := range []string{"LANG=C", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=",
		"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_1=http.extraHeader", "GIT_CONFIG_VALUE_1=X: y"} {
		if !strings.Contains("\n"+joined+"\n", "\n"+want+"\n") {
			t.Errorf("env lacks %q", want)
		}
	}
}

// TestRepoFor builds a Repo from the configuration and the PR reference.
func TestRepoFor(t *testing.T) {
	cfg := config.Defaults()
	cfg.Gitea.BaseURL = "https://your-gitea.example"
	cfg.Gitea.CACert = "/etc/ca.pem"
	cfg.Secrets.GiteaToken = config.NewSecret(testToken)
	repo, ok := RepoFor(cfg, provider.PRRef{Kind: provider.KindGitea, Namespace: "owner", Repo: "repo", Number: 7}, nil)
	if !ok || repo.BaseURL != cfg.Gitea.BaseURL || repo.Token.Reveal() != testToken || repo.CACert != "/etc/ca.pem" ||
		repo.Namespace != "owner" || repo.Name != "repo" || repo.Identity != nil {
		t.Errorf("RepoFor = %+v, %v", repo, ok)
	}
	if _, ok := RepoFor(cfg, provider.PRRef{Kind: provider.KindBitbucketServer, Namespace: "P", Repo: "r"}, nil); ok {
		t.Error("a disabled provider gave a Repo")
	}
	o := OptionsFromConfig(config.Defaults().Context.Repo)
	if o.MaxCacheBytes != 2048<<20 || o.MaxRepoBytes != 500<<20 || o.FetchTimeout != time.Minute || o.IdleDays != 7 {
		t.Errorf("OptionsFromConfig = %+v", o)
	}
}

// TestGrepStub: Grep is a stub until WP-11b.
func TestGrepStub(t *testing.T) {
	hits, err := New(Options{}).Grep(context.Background(), Checkout{}, Query{Symbols: []string{"Helper"}})
	if hits != nil || !errors.Is(err, ErrGrepNotImplemented) {
		t.Errorf("Grep = %v, %v", hits, err)
	}
}
