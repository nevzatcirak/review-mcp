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
	old := makeEntry(t, root, "your-gitea.example/_/owner/old", 100, now.Add(-8*24*time.Hour))
	keep := makeEntry(t, root, "bitbucket.example.com/bb/PROJ/keep", 100, now.Add(-6*24*time.Hour))

	list, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := repoNames(list); got != "bitbucket.example.com/bb/PROJ/keep,your-gitea.example/_/owner/old" {
		t.Fatalf("List = %s", got)
	}
	if list[1].IdleDays != 8 || list[1].SizeBytes != 100 || list[1].InUse {
		t.Errorf("old entry = %+v", list[1])
	}

	removed, err := r.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if got := repoNames(removed); got != "your-gitea.example/_/owner/old" {
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
	a := makeEntry(t, root, "h/_/ns/a", 4000, now.Add(-3*time.Hour))
	b := makeEntry(t, root, "h/_/ns/b", 4000, now.Add(-2*time.Hour))
	c := makeEntry(t, root, "h/_/ns/c", 4000, now.Add(-time.Hour))
	removed, err := r.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if got := repoNames(removed); got != "h/_/ns/a" {
		t.Errorf("removed = %s, want h/ns/a", got)
	}
	if exists(a) || !exists(b) || !exists(c) {
		t.Error("wrong entries removed")
	}

	r.opts.MaxCacheBytes = 3000
	removed, _ = r.Prune()
	if got := repoNames(removed); got != "h/_/ns/b,h/_/ns/c" {
		t.Errorf("removed = %s, want h/_/ns/b,h/_/ns/c", got)
	}
}

// TestSweepSparesInUse: neither sweep removes the caller's own entry, an
// entry with a fresh lock, or (LRU) an entry used within the last ten
// minutes; a stale lock does not protect an entry.
func TestSweepSparesInUse(t *testing.T) {
	r, root := cacheRunner(t, Options{IdleDays: 1, MaxCacheBytes: 1})
	now := time.Now()
	own := makeEntry(t, root, "h/_/ns/own", 100, now.Add(-48*time.Hour))
	locked := makeEntry(t, root, "h/_/ns/locked", 100, now.Add(-48*time.Hour))
	recent := makeEntry(t, root, "h/_/ns/recent", 100, now.Add(-time.Minute))
	stale := makeEntry(t, root, "h/_/ns/stale", 100, now.Add(-48*time.Hour))
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
		if want := e.Repo == "h/_/ns/locked"; e.InUse != want {
			t.Errorf("%s in_use = %v", e.Repo, e.InUse)
		}
	}

	removed := r.sweep(root, own)
	if got := repoNames(removed); got != "h/_/ns/stale" {
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
	e := makeEntry(t, root, "h/_/ns/repo", 10, old)
	if err := os.Symlink(outside, filepath.Join(e, gitDirName, "objects", "link")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	// A symlinked git directory and a symlinked entry.
	e2 := makeEntry(t, root, "h/_/ns/linkedgit", 10, old)
	if err := os.RemoveAll(filepath.Join(e2, gitDirName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e2, gitDirName)); err != nil {
		t.Fatal(err)
	}
	fakeEntry := t.TempDir()
	makeEntry(t, fakeEntry, "x", 10, old)
	if err := os.Symlink(filepath.Join(fakeEntry, "x"), filepath.Join(root, "h", "_", "ns", "linked")); err != nil {
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
	if got := repoNames(removed); got != "h/_/ns/linkedgit,h/_/ns/repo" {
		t.Errorf("removed = %s", got)
	}
	if !exists(victim) || !exists(filepath.Join(fakeEntry, "x", gitDirName)) {
		t.Fatal("deletion followed a symlink")
	}
	if !exists(filepath.Join(root, "h", "_", "ns", "linked")) {
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
			"your-gitea.example/_/owner/repo", schemeToken, false},
		{bbsRepo("https://Bitbucket.Example.com/bb"), "https://Bitbucket.Example.com/bb/scm/PROJ/repo.git", "refs/pull-requests/7/from",
			"bitbucket.example.com/bb/PROJ/repo", schemeBearer, false},
		{func() Repo { r := bbsRepo("http://127.0.0.1:7990"); r.Namespace = "~jdoe"; return r }(),
			"http://127.0.0.1:7990/scm/~jdoe/repo.git", "refs/pull-requests/7/from", "127.0.0.1_7990/_/~jdoe/repo", schemeBearer, true},
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

// TestChildEnvAllowlist checks childEnv directly: the configuration
// entries, the empty home instead of the user's, the system configuration
// switched off except on Windows, and the proxy variables in both cases.
func TestChildEnvAllowlist(t *testing.T) {
	realHome := t.TempDir()
	t.Setenv("HOME", realHome)
	t.Setenv("XDG_CONFIG_HOME", realHome)
	t.Setenv("REVIEW_MCP_GITEA_TOKEN", testToken)
	t.Setenv("GIT_SSH_COMMAND", "evil")
	proxies := map[string]string{
		"http_proxy": "http://p1.example:3128", "https_proxy": "http://p2.example:3128",
		"no_proxy": "localhost,.internal", "all_proxy": "socks5://p3.example:1080",
		"HTTP_PROXY": "http://P1.example:3128", "HTTPS_PROXY": "http://P2.example:3128",
		"NO_PROXY": "LOCALHOST", "ALL_PROXY": "socks5://P3.example:1080",
	}
	for k, v := range proxies {
		t.Setenv(k, v)
	}
	home := filepath.Join(t.TempDir(), homeName)
	cfg := [][2]string{{"http.extraHeader", ""}, {"http.extraHeader", "X: y"}}

	for _, goos := range []string{"linux", "darwin", "windows"} {
		env := childEnv(home, cfg, goos)
		joined := "\n" + strings.Join(env, "\n") + "\n"
		if strings.Contains(joined, testToken) || strings.Contains(joined, "GIT_SSH_COMMAND") {
			t.Errorf("%s: parent variables leaked: %v", goos, env)
		}
		if strings.Contains(joined, "="+realHome+"\n") {
			t.Errorf("%s: the user's real home was passed: %v", goos, env)
		}
		want := []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home, "LANG=C", "GIT_TERMINAL_PROMPT=0",
			"GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_1=http.extraHeader",
			"GIT_CONFIG_VALUE_1=X: y"}
		if goos == "windows" {
			want = append(want, "USERPROFILE="+home)
			if strings.Contains(joined, "GIT_CONFIG_NOSYSTEM") {
				t.Errorf("windows: GIT_CONFIG_NOSYSTEM set; Git for Windows keeps its TLS settings in the system config")
			}
		} else {
			want = append(want, "GIT_CONFIG_NOSYSTEM=1")
		}
		for _, w := range want {
			if !strings.Contains(joined, "\n"+w+"\n") {
				t.Errorf("%s: env lacks %q", goos, w)
			}
		}
		names := map[string]int{}
		for _, kv := range env {
			k, _, _ := strings.Cut(kv, "=")
			if goos == "windows" {
				k = strings.ToUpper(k)
			}
			names[k]++
		}
		for k, n := range names {
			if n > 1 {
				t.Errorf("%s: %s passed %d times", goos, k, n)
			}
		}
		if goos == "windows" {
			// Case-insensitive names: one of each pair, whichever case.
			for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY"} {
				if names[k] != 1 {
					t.Errorf("windows: %s passed %d times", k, names[k])
				}
			}
			continue
		}
		for k, v := range proxies {
			if !strings.Contains(joined, "\n"+k+"="+v+"\n") {
				t.Errorf("%s: proxy %s not passed", goos, k)
			}
		}
	}

	// The version probe runs before the cache is known: no home at all.
	if env := strings.Join(childEnv("", nil, "linux"), "\n"); strings.Contains(env, "HOME=") {
		t.Errorf("a home was passed without a cache: %s", env)
	}
}

// TestNetArgsMirrorProvider: the provider's ca_cert and
// insecure_skip_verify become git settings of network commands only, with
// the openssl backend on Windows so that the CA file is honoured.
func TestNetArgsMirrorProvider(t *testing.T) {
	has := func(a []string, kv string) bool {
		return strings.Contains(" "+strings.Join(a, " ")+" ", " -c "+kv+" ")
	}
	np := &netPolicy{caCert: "/etc/ssl/corp-ca.pem", insecureSkipVerify: true}
	for goos, backend := range map[string]bool{"linux": false, "darwin": false, "windows": true} {
		a := safetyArgs(np, goos)
		if !has(a, "http.sslCAInfo=/etc/ssl/corp-ca.pem") || !has(a, "http.sslVerify=false") {
			t.Errorf("%s: %v", goos, a)
		}
		if has(a, "http.sslBackend=openssl") != backend {
			t.Errorf("%s: sslBackend=openssl = %v, want %v", goos, !backend, backend)
		}
		if local := safetyArgs(nil, goos); has(local, "http.sslCAInfo=/etc/ssl/corp-ca.pem") || has(local, "http.sslVerify=false") {
			t.Errorf("%s: a local command carries network settings", goos)
		}
	}
	plain := safetyArgs(&netPolicy{}, "windows")
	for _, kv := range []string{"http.sslVerify=false", "http.sslBackend=openssl"} {
		if has(plain, kv) {
			t.Errorf("default policy has %s", kv)
		}
	}
	for _, a := range plain {
		if strings.HasPrefix(a, "http.sslCAInfo") {
			t.Errorf("default policy has %s", a)
		}
	}
}

// TestBaseSegment: the escaped base path is one safe, injective directory
// name; the cache key includes it, so two instances on one host never share
// an entry.
func TestBaseSegment(t *testing.T) {
	long := "/" + strings.Repeat("ctx/", 40)
	cases := map[string]string{
		"":              "_",
		"/":             "_",
		"/bb":           "bb",
		"/bitbucket":    "bitbucket",
		"/a/b":          "a_2Fb",
		"/a_b":          "a_5Fb",
		"/a%2Fb":        "a_252Fb",
		"/.hidden":      "_2Ehidden",
		"/..":           "_2E.",
		"/-x":           "_2Dx",
		"/v1.2-x":       "v1.2-x",
		"/~user":        "_7Euser",
		"/_h0123":       "_5Fh0123",
		"/git/":         "git_2F",
		long:            "",
		long + "x":      "",
		"//double":      "_2Fdouble",
		"/caf%C3%A9":    "caf_25C3_25A9",
		"/with%20space": "with_2520space",
	}
	seen := map[string]string{}
	for in, want := range cases {
		got := baseSegment(in)
		if want != "" && got != want {
			t.Errorf("baseSegment(%q) = %q, want %q", in, got, want)
		}
		if !safeSegment(got) {
			t.Errorf("baseSegment(%q) = %q is not a safe segment", in, got)
		}
		if strings.HasPrefix(in, "/") && len(in) > 1 && got == "_" {
			t.Errorf("baseSegment(%q) is the empty-path name", in)
		}
		if prev, ok := seen[got]; ok && strings.TrimPrefix(prev, "/") != strings.TrimPrefix(in, "/") {
			t.Errorf("%q and %q share %q", prev, in, got)
		}
		seen[got] = in
	}
	if got := baseSegment(long); !strings.HasPrefix(got, "_h") || len(got) != 42 {
		t.Errorf("long base path = %q, want _h + 40 hex", got)
	}

	// Two instances on one host, and one at the root, get three entries.
	rels := map[string]bool{}
	for _, base := range []string{"https://git.example.com", "https://git.example.com/gitea", "https://git.example.com/bitbucket"} {
		p, err := newPlan(giteaRepo(base), PR{Number: 7, HeadSHA: fakeSHA})
		if err != nil {
			t.Fatal(err)
		}
		rels[filepath.ToSlash(p.rel)] = true
	}
	for _, want := range []string{"git.example.com/_/owner/repo", "git.example.com/gitea/owner/repo", "git.example.com/bitbucket/owner/repo"} {
		if !rels[want] {
			t.Errorf("rel %s missing from %v", want, rels)
		}
	}
}

// TestHomeDir: the cache's .home is created 0700 with the cache, is never an
// entry (List, Prune and the sweeps leave it alone), and a symlinked .home
// makes the cache unusable rather than giving git someone else's home.
func TestHomeDir(t *testing.T) {
	r, root := cacheRunner(t, Options{IdleDays: 1, MaxCacheBytes: 1})
	h := homeDir(root)
	fi, err := os.Lstat(h)
	if err != nil || !fi.IsDir() {
		t.Fatalf(".home = %v, %v", fi, err)
	}
	if os.PathSeparator == '/' && fi.Mode().Perm() != 0o700 {
		t.Errorf(".home mode = %v, want 0700", fi.Mode().Perm())
	}
	// Even a .home that looks like an old entry is not one.
	old := time.Now().Add(-48 * time.Hour)
	makeEntry(t, h, "x/y/z", 10, old)
	makeEntry(t, root, "h/_/ns/old", 10, old)
	if list, _ := r.List(); repoNames(list) != "h/_/ns/old" {
		t.Errorf("List = %s", repoNames(list))
	}
	if removed, _ := r.Prune(); repoNames(removed) != "h/_/ns/old" {
		t.Errorf("Prune = %s", repoNames(removed))
	}
	if !exists(filepath.Join(h, "x", "y", "z", gitDirName)) {
		t.Error("the sweep entered .home")
	}

	// Mode is restored on the next use.
	if os.PathSeparator == '/' {
		if err := os.Chmod(h, 0o755); err != nil { //nolint:gosec // G302: testing the repair
			t.Fatal(err)
		}
		if _, err := r.openRoot(true); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Lstat(h); fi.Mode().Perm() != 0o700 {
			t.Errorf(".home mode after reuse = %v", fi.Mode().Perm())
		}
	}

	if err := os.RemoveAll(h); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), h); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	if _, err := r.openRoot(true); ReasonOf(err) != ReasonCache {
		t.Errorf("openRoot with a symlinked .home = %v, want cache_unusable", err)
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
