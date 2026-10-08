package gitctx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// ---- fixtures ----

// testFilter is the filter of the default configuration plus the given
// ignore globs and generated-code frameworks.
func testFilter(t *testing.T, globs []string, frameworks ...string) *filter.Filter {
	t.Helper()
	cfg := config.Defaults()
	cfg.Ignore.Glob = globs
	cfg.Diff.IgnoreGeneratedFrameworks = frameworks
	f, err := filter.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// localCheckout builds a cache entry that holds a bare repository with one
// commit of files, the way Ensure leaves it. With partialLimit set (a size
// such as "1k") the repository is a partial clone of a local source: blobs
// above the limit are genuinely missing, and its origin is a promisor remote
// that a lazy fetch would reach, so a search that fetched would succeed.
func localCheckout(t *testing.T, files map[string]string, partialLimit string) (*Runner, Checkout) {
	t.Helper()
	gitPath, _ := realGit(t)
	isolateHome(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	gitOut(t, src, gitPath, "init", "-q")
	writeFiles(t, src, files)
	gitOut(t, src, gitPath, "add", "-A")
	gitOut(t, src, gitPath, "commit", "-q", "-m", "first")
	sha := strings.TrimSpace(gitOut(t, src, gitPath, "rev-parse", "HEAD"))

	r, _ := realRunner(t, Options{})
	root, err := r.openRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "host.example", "_", "ns", "repo")
	if err := os.MkdirAll(entry, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := touch(filepath.Join(entry, markerName)); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(entry, gitDirName)
	if partialLimit == "" {
		gitOut(t, tmp, gitPath, "init", "-q", "--bare", gitDir)
		gitOut(t, src, gitPath, "push", "-q", gitDir, "HEAD:refs/review-mcp/pr/1")
	} else {
		gitOut(t, src, gitPath, "config", "uploadpack.allowFilter", "true")
		gitOut(t, tmp, gitPath, "clone", "-q", "--bare", "--filter=blob:limit="+partialLimit, "file://"+filepath.ToSlash(src), gitDir)
	}
	return r, Checkout{GitDir: gitDir, HeadSHA: sha, Ref: "refs/review-mcp/pr/1"}
}

// fakeCheckout is a cache entry for the fake git (which has no repository).
func fakeCheckout(t *testing.T, r *Runner) Checkout {
	t.Helper()
	root, err := r.openRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(root, "host.example", "_", "ns", "repo")
	gitDir := filepath.Join(entry, gitDirName)
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return Checkout{GitDir: gitDir, HeadSHA: fakeSHA}
}

func syms(names ...string) []Symbol {
	out := make([]Symbol, len(names))
	for i, n := range names {
		out[i] = Symbol{Name: n, Path: "pr.go"}
	}
	return out
}

func pathsOf(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Path
	}
	return out
}

// numbered returns n lines "line 1" ... "line n", with text at line at.
func numbered(n, at int, text string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if i == at {
			b.WriteString(text + "\n")
		} else {
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	return b.String()
}

// ---- the search against a real repository ----

// TestGrepRealRepository searches a real bare repository: a path with a
// space and a colon, a path with a newline, a binary file, the pull
// request's own file, an ignored file, a minified file and a file with an
// image extension are not reported; the context lines come from the blob.
func TestGrepRealRepository(t *testing.T) {
	files := map[string]string{
		"pr.go":                 "package lib\n\nfunc Helper() int { return 1 }\n\nvar _ = Helper()\n",
		"cmd/main.go":           numbered(9, 5, "x := lib.Helper()"),
		"cmd/second.go":         "package main\n\nfunc a() { Helper() }\nfunc b() { Helper() }\n",
		"scripts/run.py":        "Helper()\n",
		"docs/guide.md":         "Helper is documented here\n",
		"web/app.min.js":        "Helper();\n",
		"img/logo.png":          "Helper\n",
		"weird dir/co:lon.txt":  "call Helper now\n",
		"blob.dat":              "\x00Helper\n",
		"HelperLike.txt":        "Helpers and MyHelper are different words\n",
		"vendor/gen_gen.go":     "Helper()\n",
		"tab\tand\nnewline.txt": "Helper once\n",
	}
	r, co := localCheckout(t, files, "")
	q := Query{
		Symbols:          syms("Helper"),
		Exclude:          []string{"pr.go"},
		Filter:           testFilter(t, []string{"docs/**"}, "go_gen"),
		MaxHitsPerSymbol: 20,
	}
	res, err := r.Grep(context.Background(), co, q)
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	// Files of the definition's language group (Go) first, the others by path;
	// a second hit of a file only after every file has had one.
	want := []string{
		"cmd/main.go", "cmd/second.go",
		"scripts/run.py", "tab\tand\nnewline.txt", "weird dir/co:lon.txt",
		"cmd/second.go",
	}
	if got := pathsOf(res.Hits); !slices.Equal(got, want) {
		t.Fatalf("hit paths = %q\nwant       %q", got, want)
	}
	if res.Symbols != 1 || res.SymbolsWithHits != 1 || res.Files != len(want)-1 || res.References() != len(want) || res.SkippedBlobs != 0 || res.SkippedSymbols != 0 || res.Truncated {
		t.Errorf("counts = %+v", res)
	}
	h := res.Hits[0]
	if h.Symbol != "Helper" || h.Line != 5 || h.StartLine != 2 ||
		h.Snippet != "line 2\nline 3\nline 4\nx := lib.Helper()\nline 6\nline 7\nline 8" {
		t.Errorf("first hit = %+v", h)
	}
	second := res.Hits[1]
	if second.Line != 3 || second.StartLine != 1 || !strings.HasPrefix(second.Snippet, "package main\n\nfunc a()") {
		t.Errorf("hit near the start of a file = %+v", second)
	}

	// No context lines: the matching line alone.
	q.ContextLines = -1
	res, err = r.Grep(context.Background(), co, q)
	if err != nil || res.Hits[0].Snippet != "x := lib.Helper()" || res.Hits[0].StartLine != 5 {
		t.Errorf("without context: %+v, %v", res.Hits, err)
	}
}

// TestGrepSymbolsAreNotOptions: a symbol that starts with "-" is the value
// of -e, never an option; a symbol with a newline is not searched.
func TestGrepSymbolsAreNotOptions(t *testing.T) {
	r, co := localCheckout(t, map[string]string{
		"flags.txt": "run with -verbose now\nask for --help\n",
		"other.txt": "nothing\n",
	}, "")
	for _, tc := range []struct {
		symbol string
		hits   int
	}{
		{"-verbose", 1},
		{"--help", 1},
		{"--no-such-option-xyz", 0},
	} {
		res, err := r.Grep(context.Background(), co, Query{Symbols: syms(tc.symbol)})
		if err != nil || len(res.Hits) != tc.hits || res.Symbols != 1 {
			t.Errorf("%q: %d hits, %+v, %v; want %d hits", tc.symbol, len(res.Hits), res, err, tc.hits)
		}
	}
	res, err := r.Grep(context.Background(), co, Query{Symbols: syms("verbose\nnothing", "", "ok-name", "ok-name")})
	if err != nil || res.Symbols != 1 || res.SkippedSymbols != 2 || len(res.Hits) != 0 {
		t.Errorf("unusable symbols: %+v, %v", res, err)
	}
}

// TestGrepCapAndPreference: at most max hits per symbol, distinct files
// before a second hit of one file, the definition's language group first.
func TestGrepCapAndPreference(t *testing.T) {
	r, co := localCheckout(t, map[string]string{
		"a.py": "Widget()\nWidget()\nWidget()\n",
		"b.go": "Widget()\nWidget()\n",
		"c.go": "Widget()\n",
		"d.py": "Widget()\n",
		"e.go": "Widget()\nWidget()\n",
	}, "")
	sym := []Symbol{{Name: "Widget", Path: "defs/widget.go"}}
	for _, tc := range []struct {
		max  int
		want string
	}{
		{1, "b.go:1"},
		{3, "b.go:1 c.go:1 e.go:1"},
		{4, "b.go:1 c.go:1 e.go:1 a.py:1"},
		{6, "b.go:1 c.go:1 e.go:1 a.py:1 d.py:1 b.go:2"},
		{50, "b.go:1 c.go:1 e.go:1 a.py:1 d.py:1 b.go:2 e.go:2 a.py:2 a.py:3"},
	} {
		res, err := r.Grep(context.Background(), co, Query{Symbols: sym, MaxHitsPerSymbol: tc.max})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, h := range res.Hits {
			got = append(got, fmt.Sprintf("%s:%d", h.Path, h.Line))
		}
		if strings.Join(got, " ") != tc.want {
			t.Errorf("max %d: hits %v, want %s", tc.max, got, tc.want)
		}
	}
	// The default is five.
	res, err := r.Grep(context.Background(), co, Query{Symbols: sym})
	if err != nil || len(res.Hits) != DefaultMaxHitsPerSymbol {
		t.Errorf("default cap: %d hits, %v", len(res.Hits), err)
	}
}

// TestGrepMissingBlobIsSkipped: a blob the partial clone does not hold is
// counted as skipped, never an error; the other files are searched; a
// missing blob of the pull request's own or of an ignored file is not
// counted (it would not have been searched).
func TestGrepMissingBlobIsSkipped(t *testing.T) {
	big := "Helper " + strings.Repeat("x", 3000) + "\n"
	r, co := localCheckout(t, map[string]string{
		"small.go":      "Helper()\n",
		"big.go":        big,
		"big2.go":       "Helper " + strings.Repeat("y", 3000) + "\n",
		"pr_big.go":     "Helper " + strings.Repeat("z", 3000) + "\n",
		"docs/big.md":   "Helper " + strings.Repeat("w", 3000) + "\n",
		"sub/big3.go":   "Helper " + strings.Repeat("v", 3000) + "\n",
		"sub/other.txt": "Helper elsewhere\n",
	}, "1k")
	gitPath, _ := realGit(t)
	if out := gitOut(t, "", gitPath, "--git-dir="+co.GitDir, "rev-list", "--objects", "--missing=print", co.HeadSHA); strings.Count(out, "?") != 5 {
		t.Fatalf("fixture: %d blobs missing, want 5\n%s", strings.Count(out, "?"), out)
	}
	res, err := r.Grep(context.Background(), co, Query{
		Symbols:          syms("Helper"),
		Exclude:          []string{"pr_big.go"},
		Filter:           testFilter(t, []string{"docs/**"}),
		MaxHitsPerSymbol: 20,
	})
	if err != nil {
		t.Fatalf("a missing blob is an error: %v", err)
	}
	if res.SkippedBlobs != 3 {
		t.Errorf("SkippedBlobs = %d, want 3 (big.go, big2.go, sub/big3.go)", res.SkippedBlobs)
	}
	if got := pathsOf(res.Hits); !slices.Equal(got, []string{"small.go", "sub/other.txt"}) {
		t.Errorf("hits = %q", got)
	}
	if res.Symbols != 1 || res.SkippedSymbols != 0 {
		t.Errorf("counts = %+v", res)
	}
}

// TestExcludePathspecsAgreeWithInclude: git's exclude pathspecs, built from
// the filter, leave exactly the files Include accepts, for every rule that
// has a pathspec form. A rule that has none (a brace in a glob) is left to
// the post-filter, which is authoritative.
func TestExcludePathspecsAgreeWithInclude(t *testing.T) {
	paths := []string{
		"a.go", "deep/b.go", "package-lock.json", "sub/package-lock.json", "x.min.js", "dir/y.MIN.JS",
		"img/Logo.PNG", "img/ok.svgx", "docs/readme.txt", "docs/deep/more.txt", "notdocs/readme.txt",
		"root.snap", "deep/z.snap", "pkg/api_gen.go", "api_gen.go", "pkg/api.go", "app.js.map",
		"src/keep.txt", "br/a/q.tmp",
	}
	files := map[string]string{}
	for _, p := range paths {
		files[p] = "needle\n"
	}
	r, co := localCheckout(t, files, "")
	f := testFilter(t, []string{"docs/**", "*.snap", "br/{a,b}/*.tmp"}, "go_gen")
	specs := f.ExcludePathspecs()
	if len(specs) == 0 {
		t.Fatal("no pathspecs")
	}
	for _, s := range specs {
		if strings.Contains(s, "{") {
			t.Errorf("a brace glob became a pathspec: %s", s)
		}
	}
	probe, _ := r.openRoot(false)
	g := &gitRun{path: gitProbe("").path, gitDir: co.GitDir, dir: filepath.Dir(co.GitDir), home: homeDir(probe)}
	args := []string{"grep", "-l", "-z", "-F", "-e", "needle", co.HeadSHA, "--"}
	for _, s := range specs {
		args = append(args, strings.Replace(s, ":(exclude,", ":(top,exclude,", 1))
	}
	res := g.run(context.Background(), gitCmd{args: args, offline: true})
	if res.err != nil {
		t.Fatalf("git grep: %v", res.err)
	}
	var got []string
	for _, p := range bytes.Split(res.stdout, []byte{0}) {
		if len(p) > 0 {
			got = append(got, strings.TrimPrefix(string(p), co.HeadSHA+":"))
		}
	}
	slices.Sort(got)
	var want []string
	for _, p := range paths {
		if f.Include(p) || p == "br/a/q.tmp" { // the brace rule has no pathspec
			want = append(want, p)
		}
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("pathspecs leave %q\nInclude accepts %q", got, want)
	}

	// The post-filter removes what only Include knows about.
	out, err := r.Grep(context.Background(), co, Query{Symbols: syms("needle"), Filter: f, MaxHitsPerSymbol: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range out.Hits {
		if !f.Include(h.Path) {
			t.Errorf("hit in a path the filter excludes: %s", h.Path)
		}
	}
	if slices.Contains(pathsOf(out.Hits), "br/a/q.tmp") {
		t.Error("the brace-glob file survived the post-filter")
	}
}

// TestGrepNeverReportsOwnFiles [canary]: the pull request's own files never
// appear among the hits. Two layers keep them out, each enough alone: the
// exclude pathspec and the post-filter on the hits.
func TestGrepNeverReportsOwnFiles(t *testing.T) {
	r, co := localCheckout(t, map[string]string{
		"pr.go":          "func Helper() {}\nHelper()\n",
		"renamed_new.go": "Helper()\n",
		"pkg/changed.go": "Helper()\nHelper()\n",
		"caller.go":      "Helper()\n",
		"other/use.go":   "Helper()\n",
	}, "")
	files := []provider.FilePatch{
		{Path: "pr.go"},
		{Path: "renamed_new.go", OldPath: "renamed_old.go"},
		{Path: "pkg/changed.go"},
	}
	res, err := r.Grep(context.Background(), co, Query{
		Symbols: syms("Helper"), Exclude: ChangedPaths(files), MaxHitsPerSymbol: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := pathsOf(res.Hits); !slices.Equal(got, []string{"caller.go", "other/use.go"}) {
		t.Errorf("hits = %q, want only caller.go and other/use.go", got)
	}
	own := toSet(ChangedPaths(files))
	for _, h := range res.Hits {
		if own[h.Path] {
			t.Errorf("the pull request's own file %s is among the hits", h.Path)
		}
	}
}

// ---- offline, no credentials (fake git) ----

// TestGrepRunsOffline [canary]: every git process of the search runs without
// credentials, with protocol.allow=never and no https or http allowance, and
// with GIT_NO_LAZY_FETCH=1, from the allowlisted environment; the symbol is
// the value of -e, after the tree-ish comes "--" and the pathspecs.
func TestGrepRunsOffline(t *testing.T) {
	resetSchemes()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "Authorization: token "+testToken)
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA, GrepOut: fakeSHA + ":use.go\x003\x00Helper()\n"}, Options{})
	co := fakeCheckout(t, r)
	res, err := r.Grep(context.Background(), co, Query{
		Symbols: syms("-weird"), Exclude: []string{"pr.go"}, Filter: testFilter(t, []string{"docs/**"}),
	})
	if err != nil || len(res.Hits) != 1 || res.Hits[0].Path != "use.go" {
		t.Fatalf("Grep = %+v, %v", res, err)
	}
	root, _ := r.CacheDir()
	var searches int
	for _, c := range f.calls(t) {
		sub := c.subcommand()
		if sub == "--version" {
			continue
		}
		args := " " + strings.Join(c.Args, " ") + " "
		for _, bad := range []string{testToken, "extraheader", "protocol.https.allow", "protocol.http.allow", "sslverify", "sslcainfo"} {
			if strings.Contains(strings.ToLower(args), strings.ToLower(bad)) {
				t.Errorf("git %s: argument %q on an offline command", sub, bad)
			}
		}
		if !strings.Contains(args, " -c protocol.allow=never ") {
			t.Errorf("git %s lacks protocol.allow=never: %s", sub, args)
		}
		if v, ok := envValue(c.Env, "GIT_NO_LAZY_FETCH"); !ok || v != "1" {
			t.Errorf("git %s: GIT_NO_LAZY_FETCH = %q (set %v), want 1", sub, v, ok)
		}
		for _, kv := range c.Env {
			name, v, _ := strings.Cut(kv, "=")
			if strings.HasPrefix(name, "GIT_CONFIG_COUNT") || strings.HasPrefix(name, "GIT_CONFIG_KEY") || strings.HasPrefix(name, "GIT_CONFIG_VALUE") ||
				strings.Contains(kv, testToken) || strings.EqualFold(name, "HTTPS_PROXY") && v == "" {
				t.Errorf("git %s: environment carries %s", sub, name)
			}
			if name != "GIT_NO_LAZY_FETCH" && !allowedEnv.MatchString(name) {
				t.Errorf("git %s got %s, which is not on the allowlist", sub, name)
			}
		}
		if h, _ := envValue(c.Env, "HOME"); h != homeDir(root) {
			t.Errorf("git %s: HOME = %q, want the cache's empty home", sub, h)
		}
		if sub == "grep" {
			searches++
			i := slices.Index(c.Args, "-e")
			if i < 0 || c.Args[i+1] != "-weird" || c.Args[i+2] != fakeSHA || c.Args[i+3] != "--" {
				t.Errorf("grep arguments after -e: %v", c.Args[max(i, 0):])
			}
			for _, want := range []string{" -n ", " -z ", " -w ", " -F ", " -I "} {
				if !strings.Contains(args, want) {
					t.Errorf("grep lacks %s", strings.TrimSpace(want))
				}
			}
			if !strings.Contains(args, " :(top,exclude,literal)pr.go ") {
				t.Errorf("grep does not exclude the pull request's file pr.go by pathspec (%d arguments)", len(c.Args))
			}
		}
	}
	if searches != 1 {
		t.Errorf("%d git grep calls, want 1", searches)
	}
}

// TestGrepPostFilter: whatever git prints, a hit in the pull request's own
// files or in a file the filter excludes is dropped, and so is a record that
// does not belong to the head tree.
func TestGrepPostFilter(t *testing.T) {
	rec := func(path string, line int) string { return fmt.Sprintf("%s:%s\x00%d\x00text\n", fakeSHA, path, line) }
	out := rec("pr.go", 1) + rec("keep.go", 2) + rec("docs/x.md", 3) + rec("lib.min.js", 4) + rec("dir/pr.go", 5) +
		"other-tree:evil.go\x007\x00text\n"
	r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA, GrepOut: out}, Options{})
	co := fakeCheckout(t, r)
	res, err := r.Grep(context.Background(), co, Query{
		Symbols: syms("Helper"), Exclude: []string{"pr.go"}, Filter: testFilter(t, []string{"docs/**"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := pathsOf(res.Hits); !slices.Equal(got, []string{"dir/pr.go", "keep.go"}) {
		t.Errorf("hits = %q, want dir/pr.go (a different path than pr.go) and keep.go", got)
	}
}

// TestGrepFailures: a git failure is a fixed reason without git's text; no
// match is not a failure; a symbol that takes too long is skipped; a canceled
// context is reported as such; the output cap stops a runaway search.
func TestGrepFailures(t *testing.T) {
	const stderrText = "fatal: /home/someone/hidden-path is not a repository"
	t.Run("git fails", func(t *testing.T) {
		r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA, GrepExit: 128, GrepStderr: stderrText}, Options{})
		_, err := r.Grep(context.Background(), fakeCheckout(t, r), Query{Symbols: syms("Helper")})
		wantReason(t, err, ReasonGitFailed)
		if strings.Contains(err.Error(), "hidden-path") {
			t.Errorf("git's stderr reached the error: %v", err)
		}
	})
	t.Run("no match", func(t *testing.T) {
		r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA, GrepExit: 1, GrepStderr: stderrText}, Options{})
		res, err := r.Grep(context.Background(), fakeCheckout(t, r), Query{Symbols: syms("Helper", "Other")})
		if err != nil || res.Symbols != 2 || len(res.Hits) != 0 {
			t.Errorf("no match: %+v, %v", res, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		saved := grepTimeout
		grepTimeout = 200 * time.Millisecond
		t.Cleanup(func() { grepTimeout = saved })
		r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA, GrepSleepMS: 5000}, Options{})
		start := time.Now()
		_, err := r.Grep(context.Background(), fakeCheckout(t, r), Query{Symbols: syms("Helper", "Other")})
		wantReason(t, err, ReasonTimeout)
		if time.Since(start) > 4*time.Second {
			t.Errorf("the search waited %v for a hung git", time.Since(start))
		}
	})
	t.Run("canceled", func(t *testing.T) {
		r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
		co := fakeCheckout(t, r)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := r.Grep(ctx, co, Query{Symbols: syms("Helper")}); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("output cap", func(t *testing.T) {
		saved := maxGrepOutput
		maxGrepOutput = 200
		t.Cleanup(func() { maxGrepOutput = saved })
		var out strings.Builder
		for i := 1; i <= 50; i++ {
			fmt.Fprintf(&out, "%s:f%02d.go\x00%d\x00Helper()\n", fakeSHA, i, i)
		}
		r, _ := fakeRunner(t, fakeBehavior{SHA: fakeSHA, GrepOut: out.String()}, Options{})
		res, err := r.Grep(context.Background(), fakeCheckout(t, r), Query{Symbols: syms("Helper"), MaxHitsPerSymbol: 100})
		if err != nil || !res.Truncated || len(res.Hits) == 0 || len(res.Hits) >= 50 {
			t.Fatalf("capped search: %d hits, truncated %v, %v", len(res.Hits), res.Truncated, err)
		}
		for _, h := range res.Hits { // only whole records
			if !strings.HasPrefix(h.Path, "f") || !strings.HasSuffix(h.Path, ".go") || len(h.Path) != 6 {
				t.Errorf("a cut record became a hit: %+v", h)
			}
		}
	})
}

// TestGrepRejectsUnusableCheckout: a Checkout outside the cache, without a
// repository or with a bad SHA is refused before git runs.
func TestGrepRejectsUnusableCheckout(t *testing.T) {
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
	good := fakeCheckout(t, r)
	outside := filepath.Join(t.TempDir(), "x", gitDirName)
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		co     Checkout
		reason string
	}{
		"outside the cache": {Checkout{GitDir: outside, HeadSHA: fakeSHA}, ReasonCache},
		"missing directory": {Checkout{GitDir: filepath.Join(filepath.Dir(good.GitDir), "nope", gitDirName), HeadSHA: fakeSHA}, ReasonCache},
		"bad sha":           {Checkout{GitDir: good.GitDir, HeadSHA: "--output=x"}, ReasonUnsupported},
		"empty":             {Checkout{}, ReasonUnsupported},
	} {
		_, err := r.Grep(context.Background(), tc.co, Query{Symbols: syms("Helper")})
		t.Run(name, func(t *testing.T) { wantReason(t, err, tc.reason) })
	}
	if n := len(callsOf(f.calls(t), "grep")); n != 0 {
		t.Errorf("%d git grep calls for refused checkouts", n)
	}
}

// ---- no lazy fetch (real git over http) ----

// TestGrepAfterEnsureNeverFetches: against a repository fetched with the
// real 1 MiB filter, the blob above it is missing; the search skips it,
// counts it, and sends no request to the server.
func TestGrepAfterEnsureNeverFetches(t *testing.T) {
	resetSchemes()
	huge := "Helper " + strings.Repeat("x", 1<<20+1024) + "\n"
	s := newGitServerFiles(t, provider.KindGitea, map[string]string{
		"huge.txt": huge,
		"use.go":   "package a\n\nfunc f() { Helper() }\n",
	})
	r, _ := realRunner(t, Options{})
	co, err := r.Ensure(context.Background(), giteaRepo(s.base), PR{Number: 7, HeadSHA: s.head})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	gitPath, _ := realGit(t)
	if out := gitOut(t, "", gitPath, "--git-dir="+co.GitDir, "rev-list", "--objects", "--missing=print", co.HeadSHA); strings.Count(out, "?") != 1 {
		t.Fatalf("fixture: the 1 MiB filter left %d blobs out, want 1\n%s", strings.Count(out, "?"), out)
	}
	s.requests() // forget the fetch

	res, err := r.Grep(context.Background(), co, Query{Symbols: syms("Helper")})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if got := pathsOf(res.Hits); !slices.Equal(got, []string{"use.go"}) || res.SkippedBlobs != 1 {
		t.Errorf("hits %q, skipped %d; want use.go and 1", got, res.SkippedBlobs)
	}
	if reqs := s.requests(); len(reqs) != 0 {
		t.Errorf("the search sent %d requests to the git server", len(reqs))
	}
}

// TestOfflineBlocksLazyFetch [canary]: an offline command that reads a blob
// the partial clone lacks fails at once, locally; the same command with the
// network allowed fetches the blob from the server. This is what the
// protocol settings and GIT_NO_LAZY_FETCH are for.
func TestOfflineBlocksLazyFetch(t *testing.T) {
	resetSchemes()
	huge := "Helper " + strings.Repeat("x", 1<<20+1024) + "\n"
	s := newGitServerFiles(t, provider.KindGitea, map[string]string{"huge.txt": huge, "use.go": "Helper()\n"})
	r, _ := realRunner(t, Options{})
	co, err := r.Ensure(context.Background(), giteaRepo(s.base), PR{Number: 7, HeadSHA: s.head})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	root, _ := r.CacheDir()
	g := &gitRun{path: gitProbe(r.opts.GitPath).path, gitDir: co.GitDir, dir: filepath.Dir(co.GitDir), home: homeDir(root)}
	cat := []string{"cat-file", "-p", co.HeadSHA + ":huge.txt"}

	s.requests()
	res := g.run(context.Background(), gitCmd{args: cat, offline: true})
	if res.err == nil {
		t.Errorf("an offline read of a missing blob succeeded (%d bytes)", len(res.stdout))
	}
	if reqs := s.requests(); len(reqs) != 0 {
		t.Errorf("an offline read sent %d requests", len(reqs))
	}

	// The control: with the network allowed the blob is fetched, so the test
	// above does prove that the settings are what stops it.
	res = g.run(context.Background(), gitCmd{args: cat, net: &netPolicy{allowHTTP: true}})
	if res.err != nil || len(res.stdout) < 1<<20 {
		t.Fatalf("control: the lazy fetch did not happen (%v, %d bytes): the test proves nothing", res.err, len(res.stdout))
	}
	if reqs := s.requests(); len(reqs) == 0 {
		t.Error("control: no request reached the server")
	}
}

// TestNoLazyFetchEnvironmentAlone [canary]: with a transport that the
// protocol settings would allow (protocol.file.allow=always added after
// them), GIT_NO_LAZY_FETCH=1 alone still stops the fetch of a missing blob;
// without "offline" the same command fetches it.
func TestNoLazyFetchEnvironmentAlone(t *testing.T) {
	r, co := localCheckout(t, map[string]string{"big.txt": "Helper " + strings.Repeat("x", 3000) + "\n"}, "1k")
	p := gitProbe(r.opts.GitPath)
	var major, minor int
	if _, err := fmt.Sscanf(p.version, "%d.%d", &major, &minor); err != nil || major < 2 || major == 2 && minor < 44 {
		t.Skipf("git %s: this test is run only on a git that honours GIT_NO_LAZY_FETCH; on older git the protocol settings alone stop the fetch (TestOfflineBlocksLazyFetch)", p.version)
	}
	root, _ := r.CacheDir()
	g := &gitRun{path: p.path, gitDir: co.GitDir, dir: filepath.Dir(co.GitDir), home: homeDir(root)}
	cat := []string{"cat-file", "-p", co.HeadSHA + ":big.txt"}
	allow := [][2]string{{"protocol.file.allow", "always"}}
	if res := g.run(context.Background(), gitCmd{args: cat, offline: true, config: allow}); res.err == nil {
		t.Error("with the transport allowed, GIT_NO_LAZY_FETCH did not stop the fetch of a missing blob")
	}
	if res := g.run(context.Background(), gitCmd{args: cat, config: allow}); res.err != nil || len(res.stdout) < 3000 {
		t.Fatalf("control: the lazy fetch did not happen (%v, %d bytes): the test proves nothing", res.err, len(res.stdout))
	}
}

// ---- pure parts ----

func TestChooseHits(t *testing.T) {
	h := func(path string, line int) rawHit { return rawHit{symbol: "S", path: path, line: line} }
	raw := []rawHit{h("z.py", 1), h("b.go", 9), h("a.go", 3), h("b.go", 2), h("m.rs", 1), h("a.go", 1)}
	got := func(max int) string {
		var s []string
		for _, x := range chooseHits(Symbol{Name: "S", Path: "def.go"}, raw, max) {
			s = append(s, fmt.Sprintf("%s:%d", x.path, x.line))
		}
		return strings.Join(s, " ")
	}
	if g := got(3); g != "a.go:1 b.go:2 m.rs:1" {
		t.Errorf("max 3 = %s", g)
	}
	if g := got(5); g != "a.go:1 b.go:2 m.rs:1 z.py:1 a.go:3" {
		t.Errorf("max 5 = %s", g)
	}
	if g := got(0); g != "" {
		t.Errorf("max 0 = %s", g)
	}
	// Another language group first when the definition is in it.
	var first []string
	for _, x := range chooseHits(Symbol{Name: "S", Path: "def.py"}, raw, 2) {
		first = append(first, x.path)
	}
	if strings.Join(first, " ") != "z.py a.go" {
		t.Errorf("python definition: %v", first)
	}
}

func TestCutLine(t *testing.T) {
	if got := cutLine("abc\r"); got != "abc" {
		t.Errorf("cutLine = %q", got)
	}
	long := strings.Repeat("é", 300) // 600 bytes
	got := cutLine(long)
	if !strings.HasSuffix(got, "...") || len(got) > maxSnippetLine+3 || strings.ContainsRune(got, '�') {
		t.Errorf("cutLine cut inside a character: %q", got)
	}
}
