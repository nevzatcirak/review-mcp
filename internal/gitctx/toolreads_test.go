package gitctx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// The reads of the repository tools (X-43, V-8 spec §2.1): every entry of a
// directory, a blob the partial clone lacks, and the fixed-string search.

// TestDirEntriesAll: every entry, sorted, not capped at the provider's 500.
func TestDirEntriesAll(t *testing.T) {
	files := map[string]string{"README.md": "x\n", "sub/a.go": "package sub\n"}
	for i := range 520 {
		files[fmt.Sprintf("many/f%03d.txt", i)] = "x\n"
	}
	r, co := localCheckout(t, files, "")
	ctx := context.Background()
	root, err := r.DirEntries(ctx, co, co.HeadSHA, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []provider.DirEntry{{Name: "README.md"}, {Name: "many", Dir: true}, {Name: "sub", Dir: true}}; !slices.Equal(root, want) {
		t.Fatalf("root = %v", root)
	}
	many, err := r.DirEntries(ctx, co, co.HeadSHA, "many")
	if err != nil || len(many) != 520 || many[0].Name != "f000.txt" || many[519].Name != "f519.txt" {
		t.Fatalf("many: %d entries, %v", len(many), err)
	}
	if _, err := r.DirEntries(ctx, co, co.HeadSHA, "README.md"); !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("a file: %v", err)
	}
	for _, d := range []string{"/sub", "sub/", "a//b", "a\nb"} {
		if _, err := r.DirEntries(ctx, co, co.HeadSHA, d); ReasonOf(err) != ReasonUnsupported {
			t.Errorf("DirEntries(%q) = %v, want unsupported", d, err)
		}
	}
}

// TestFileAtBlobMissing: a blob the partial clone lacks is blob_missing,
// never fetched; one it holds is read.
func TestFileAtBlobMissing(t *testing.T) {
	r, co := localCheckout(t, map[string]string{"big.txt": strings.Repeat("x", 3000) + "\n", "small.txt": "s\n"}, "1k")
	ctx := context.Background()
	if _, err := r.FileAt(ctx, co, co.HeadSHA, "big.txt", 1<<20); ReasonOf(err) != ReasonBlobMissing {
		t.Fatalf("missing blob: %v", err)
	}
	if got, err := r.FileAt(ctx, co, co.HeadSHA, "small.txt", 1<<20); err != nil || string(got) != "s\n" {
		t.Fatalf("present blob: %q %v", got, err)
	}
}

// TestSearchTextRealRepository: a fixed string anywhere in a line (not a
// word search, not a pattern), case-sensitive; a directory prefix; the
// filter; binary files; a text that starts with "-"; one line of context
// from the blob; the hit cap.
func TestSearchTextRealRepository(t *testing.T) {
	files := map[string]string{
		"a.go":            numbered(5, 3, "x := MyHelper(a.b*)"),
		"lib/b.go":        "package lib\n\nfunc helper() {} // MyHelper(a.b*) lower\n",
		"lib/deep/c.go":   "-flag MyHelper(a.b*)\n",
		"libx/d.go":       "MyHelper(a.b*)\n",
		"docs/e.md":       "MyHelper(a.b*)\n",
		"bin.dat":         "\x00MyHelper(a.b*)\n",
		"lower.go":        "myhelper(a.b*)\n",
		"regexp-trap.txt": "MyHelperXaYb\n",
	}
	r, co := localCheckout(t, files, "")
	ctx := context.Background()
	res, err := r.SearchText(ctx, co, TextQuery{Text: "MyHelper(a.b*)", ContextLines: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pathsOf(res.Hits), []string{"a.go", "docs/e.md", "lib/b.go", "lib/deep/c.go", "libx/d.go"}; !slices.Equal(got, want) {
		t.Fatalf("paths %q, want %q", got, want)
	}
	if h := res.Hits[0]; h.Line != 3 || h.StartLine != 2 || h.Snippet != "line 2\nx := MyHelper(a.b*)\nline 4" || h.Symbol != "" {
		t.Fatalf("hit %+v", h)
	}
	if res.More || res.SkippedBlobs != 0 {
		t.Fatalf("%+v", res)
	}

	res, err = r.SearchText(ctx, co, TextQuery{Text: "MyHelper(a.b*)", Dir: "lib"})
	if err != nil || !slices.Equal(pathsOf(res.Hits), []string{"lib/b.go", "lib/deep/c.go"}) {
		t.Fatalf("dir lib: %q %v", pathsOf(res.Hits), err)
	}
	if res.Hits[0].Snippet != "func helper() {} // MyHelper(a.b*) lower" {
		t.Fatalf("no context asked: %q", res.Hits[0].Snippet)
	}
	res, err = r.SearchText(ctx, co, TextQuery{Text: "-flag"})
	if err != nil || !slices.Equal(pathsOf(res.Hits), []string{"lib/deep/c.go"}) {
		t.Fatalf("leading dash: %q %v", pathsOf(res.Hits), err)
	}
	res, err = r.SearchText(ctx, co, TextQuery{Text: "MyHelper(a.b*)", Filter: testFilter(t, []string{"docs/**"})})
	if err != nil || slices.Contains(pathsOf(res.Hits), "docs/e.md") || len(res.Hits) != 4 {
		t.Fatalf("filter: %q %v", pathsOf(res.Hits), err)
	}
	res, err = r.SearchText(ctx, co, TextQuery{Text: "MyHelper(a.b*)", MaxHits: 2})
	if err != nil || len(res.Hits) != 2 || !res.More {
		t.Fatalf("cap: %+v %v", res, err)
	}
	res, err = r.SearchText(ctx, co, TextQuery{Text: "nothing like this"})
	if err != nil || len(res.Hits) != 0 || res.More {
		t.Fatalf("no match: %+v %v", res, err)
	}
	for _, q := range []TextQuery{{Text: ""}, {Text: "a\nb"}, {Text: "x", Dir: "/lib"}, {Text: "x", Dir: "lib/"}, {Text: strings.Repeat("x", 801)}} {
		if _, err := r.SearchText(ctx, co, q); ReasonOf(err) != ReasonUnsupported {
			t.Errorf("SearchText(%+v) = %v, want unsupported", q, err)
		}
	}
}

// TestSearchTextSkipsMissingBlobs: a file whose blob the partial clone
// lacks is not searched and is counted; nothing is fetched.
func TestSearchTextSkipsMissingBlobs(t *testing.T) {
	r, co := localCheckout(t, map[string]string{
		"big.txt":   "needle\n" + strings.Repeat("x", 3000) + "\n",
		"small.txt": "needle\n",
	}, "1k")
	res, err := r.SearchText(context.Background(), co, TextQuery{Text: "needle"})
	if err != nil || !slices.Equal(pathsOf(res.Hits), []string{"small.txt"}) || res.SkippedBlobs != 1 {
		t.Fatalf("%+v %v", res, err)
	}
}

// TestSearchTextPaths: Paths confines a search to its directory prefixes
// (a related repository from the repository config, v2.5 spec V-13b §3.1):
// a sibling that only shares a name prefix is not searched; a prefix that
// cannot name a path is refused before git runs.
func TestSearchTextPaths(t *testing.T) {
	r, co := localCheckout(t, map[string]string{
		"src/api/a.ts":  "needle\n",
		"src/apix/b.ts": "needle\n",
		"web/c.ts":      "needle\n",
		"docs/d.md":     "needle\n",
	}, "")
	ctx := context.Background()
	res, err := r.SearchText(ctx, co, TextQuery{Text: "needle", Paths: []string{"src/api/", "web"}})
	if err != nil || !slices.Equal(pathsOf(res.Hits), []string{"src/api/a.ts", "web/c.ts"}) {
		t.Fatalf("paths: %q %v", pathsOf(res.Hits), err)
	}
	for _, bad := range []string{"", "/src", "a//b", "a\nb"} {
		if _, err := r.SearchText(ctx, co, TextQuery{Text: "needle", Paths: []string{bad}}); ReasonOf(err) != ReasonUnsupported {
			t.Errorf("prefix %q: %v, want unsupported", bad, err)
		}
	}
}

// TestToolReadsRunOffline [canary]: every git process of the tools' reads
// runs offline: protocol.allow=never with no https or http allowance,
// GIT_NO_LAZY_FETCH=1, no credential, the allowlisted environment.
func TestToolReadsRunOffline(t *testing.T) {
	resetSchemes()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_0", "Authorization: token "+testToken)
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA, GrepOut: fakeSHA + ":use.go\x003\x00needle\n"}, Options{})
	co := fakeCheckout(t, r)
	ctx := context.Background()
	if res, err := r.SearchText(ctx, co, TextQuery{Text: "needle", Dir: "src"}); err != nil || len(res.Hits) != 1 {
		t.Fatalf("SearchText = %+v, %v", res, err)
	}
	_, _ = r.DirEntries(ctx, co, co.HeadSHA, "src")
	_, _ = r.FileAt(ctx, co, co.HeadSHA, "src/a.go", 1<<20)
	seen := map[string]bool{}
	for _, c := range f.calls(t) {
		sub := c.subcommand()
		if sub == "--version" {
			continue
		}
		seen[sub] = true
		args := " " + strings.Join(c.Args, " ") + " "
		if !strings.Contains(args, " -c protocol.allow=never ") || strings.Contains(args, "protocol.https.allow") ||
			strings.Contains(args, "protocol.http.allow") || strings.Contains(args, testToken) {
			t.Errorf("git %s is not offline: %s", sub, args)
		}
		if v, ok := envValue(c.Env, "GIT_NO_LAZY_FETCH"); !ok || v != "1" {
			t.Errorf("git %s: GIT_NO_LAZY_FETCH = %q (set %v), want 1", sub, v, ok)
		}
		for _, kv := range c.Env {
			name, _, _ := strings.Cut(kv, "=")
			if strings.HasPrefix(name, "GIT_CONFIG_COUNT") || strings.HasPrefix(name, "GIT_CONFIG_KEY") || strings.HasPrefix(name, "GIT_CONFIG_VALUE") || strings.Contains(kv, testToken) {
				t.Errorf("git %s: environment carries %s", sub, name)
			}
			if !offlineEnv[name] && !allowedEnv.MatchString(name) {
				t.Errorf("git %s got %s, which is not on the allowlist", sub, name)
			}
		}
	}
	for _, sub := range []string{"grep", "rev-list", "cat-file"} {
		if !seen[sub] {
			t.Errorf("no git %s ran; seen %v", sub, seen)
		}
	}
	// The text is the value of -e and the directory a literal pathspec.
	for _, c := range f.calls(t) {
		if c.subcommand() != "grep" {
			continue
		}
		a := strings.Join(c.Args, "\x00")
		if !strings.Contains(a, "\x00-e\x00needle\x00"+fakeSHA+"\x00--\x00:(top,literal)src") || strings.Contains(a, "\x00-w\x00") {
			t.Errorf("grep arguments %q", c.Args)
		}
	}
}
