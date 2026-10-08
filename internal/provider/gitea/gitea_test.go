package gitea_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
)

func TestKindAndCapabilities(t *testing.T) {
	f := newFake(t, "")
	p := f.provider(t, nil)
	if p.Kind() != provider.KindGitea || gitea.NewFactory().Kind() != provider.KindGitea {
		t.Fatal("wrong kind")
	}
	want := provider.Capabilities{
		GFM: true, MarkdownTables: true, Labels: true, InlineComments: true,
		InlineThreadResolution: true, DescriptionEdit: true,
	}
	if p.Capabilities() != want {
		t.Fatalf("capabilities = %+v", p.Capabilities())
	}
}

func TestParsePRPath(t *testing.T) {
	cases := []struct {
		in      string
		ns, rep string
		n       int64
		ok      bool
	}{
		{"/octo/demo/pulls/7", "octo", "demo", 7, true},
		{"/octo/demo/pulls/7/files", "octo", "demo", 7, true},
		{"/octo/demo/pulls/7/commits/abc", "octo", "demo", 7, true},
		{"/octo/demo/pulls/007", "octo", "demo", 7, true},
		{"/o%20x/de%6Do/pulls/12", "o x", "demo", 12, true},
		{"/octo/demo/pulls/0", "", "", 0, false},
		{"/octo/demo/pulls/-1", "", "", 0, false},
		{"/octo/demo/pulls/+1", "", "", 0, false},
		{"/octo/demo/pulls/1x", "", "", 0, false},
		{"/octo/demo/pulls/7.diff", "", "", 0, false},
		{"/octo/demo/pulls/", "", "", 0, false},
		{"/octo/demo/pulls", "", "", 0, false},
		{"/octo/demo/issues/7", "", "", 0, false},
		{"//demo/pulls/7", "", "", 0, false},
		{"/octo//pulls/7", "", "", 0, false},
		{"/../demo/pulls/7", "", "", 0, false},
		{"/octo/%2e%2e/pulls/7", "", "", 0, false},
		{"/octo%2Fsub/demo/pulls/7", "", "", 0, false},
		{"/octo/de%2Fmo/pulls/7", "", "", 0, false},
		{"/octo/de%2fmo/pulls/7", "", "", 0, false},
		{"/octo/sub/demo/pulls/7", "", "", 0, false},
		{"/octo/de%zzmo/pulls/7", "", "", 0, false},
		{"/octo/demo/pulls/99999999999999999999", "", "", 0, false},
		{"octo/demo/pulls/7", "", "", 0, false},
		{"", "", "", 0, false},
	}
	for _, c := range cases {
		ns, rep, n, err := gitea.NewFactory().ParsePRPath(c.in)
		if (err == nil) != c.ok {
			t.Errorf("%q: err = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && (ns != c.ns || rep != c.rep || n != c.n) {
			t.Errorf("%q: got %q %q %d", c.in, ns, rep, n)
		}
	}
}

func TestNewRequiresBaseURL(t *testing.T) {
	_, err := gitea.NewFactory().New(config.Defaults(), nil)
	if !errors.Is(err, provider.ErrURLNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetPullRequestUnderContextPath(t *testing.T) {
	f := newFake(t, "/gitea")
	f.standard("mergesha")
	var ua string
	f.handle("GET", prAPI, func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		_ = json.NewEncoder(w).Encode(prJSON("mergesha"))
	})
	p := f.provider(t, nil)
	pr, err := p.GetPullRequest(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	want := provider.PullRequest{
		Title: "Add feature", Description: "Description " + testMarker, Author: "alice",
		SourceBranch: "feature", TargetBranch: "main", HeadSHA: "headsha", BaseSHA: "mergesha",
		WebURL: "https://your-gitea.example/octo/demo/pulls/7", State: "open",
	}
	want.BaseStrategy = provider.BaseGiteaMergeBase
	if *pr != want {
		t.Fatalf("pr = %+v", *pr)
	}
	if !strings.HasPrefix(ua, "review-mcp/") {
		t.Fatalf("User-Agent = %q", ua)
	}
	if reqs := f.requests(); len(reqs) != 1 || reqs[0].Path != prAPI {
		t.Fatalf("requests = %+v", reqs)
	}
}

func TestBaseStrategy(t *testing.T) {
	for _, tc := range []struct{ mergeBase, wantSHA, wantStrategy string }{
		{"mergesha", "mergesha", provider.BaseGiteaMergeBase},
		{"", "basesha", provider.BaseGiteaBaseSHA},
	} {
		f := newFake(t, "")
		f.standard(tc.mergeBase)
		p := f.provider(t, nil)
		pr, err := p.GetPullRequest(context.Background(), ref())
		if err != nil {
			t.Fatal(err)
		}
		if pr.BaseSHA != tc.wantSHA {
			t.Errorf("BaseSHA = %q, want %q", pr.BaseSHA, tc.wantSHA)
		}
		if pr.BaseStrategy != tc.wantStrategy {
			t.Errorf("PullRequest.BaseStrategy = %q, want %q", pr.BaseStrategy, tc.wantStrategy)
		}
		d, err := p.GetDiff(context.Background(), ref(), pr, provider.DiffOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if d.BaseStrategy != tc.wantStrategy {
			t.Errorf("Diff.BaseStrategy = %q, want %q", d.BaseStrategy, tc.wantStrategy)
		}
		// Base content must have been requested at the chosen base revision.
		var sawBase bool
		for _, r := range f.rawRequests() {
			if strings.Contains(r.Query, "ref="+tc.wantSHA) {
				sawBase = true
			}
		}
		if !sawBase {
			t.Errorf("no raw request at ref=%s", tc.wantSHA)
		}
	}
}

// stdPR is a hand-built PR for GetDiff tests that do not exercise GetPullRequest.
func stdPR() *provider.PullRequest {
	return &provider.PullRequest{HeadSHA: "headsha", BaseSHA: "basesha", BaseStrategy: provider.BaseGiteaBaseSHA}
}

func TestGetDiffWithoutBaseSHAMakesNoRequest(t *testing.T) {
	for name, pr := range map[string]*provider.PullRequest{
		"empty BaseSHA": {HeadSHA: "h"},
		"empty HeadSHA": {BaseSHA: "b"},
		"nil":           nil,
	} {
		f := newFake(t, "")
		f.standard("mergesha")
		d, err := f.provider(t, nil).GetDiff(context.Background(), ref(), pr, provider.DiffOptions{})
		if d != nil || !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%s: diff = %v, err = %v, want a protocol error", name, d, err)
		}
		if n := len(f.requests()); n != 0 {
			t.Errorf("%s: %d requests were made, want 0", name, n)
		}
	}
}

type fileView struct {
	Path, OldPath          string
	Type                   provider.ChangeType
	Add, Del               int
	Base, Head             string
	BaseStatus, HeadStatus provider.ContentStatus
	HasPatch               bool
}

func view(d *provider.Diff) []fileView {
	var out []fileView
	for _, fp := range d.Files {
		out = append(out, fileView{fp.Path, fp.OldPath, fp.Type, fp.Additions, fp.Deletions,
			deref(fp.BaseContent), deref(fp.HeadContent), fp.BaseStatus, fp.HeadStatus, fp.Patch != ""})
	}
	return out
}

func TestGetDiffJoinContentsAndRename(t *testing.T) {
	f := newFake(t, "/gitea")
	f.standard("mergesha")
	p := f.provider(t, nil)
	ctx := context.Background()
	pr, err := p.GetPullRequest(ctx, ref())
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.GetDiff(ctx, ref(), pr, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := []fileView{
		// API order: renamed, app, gone, new (the binary is skipped).
		{"src/renamed.go", "src/old_name.go", provider.ChangeRenamed, 1, 1, "base old name", "head renamed", provider.ContentFull, provider.ContentFull, true},
		{"src/app.go", "", provider.ChangeModified, 1, 1, "base app", "head app " + testMarker, provider.ContentFull, provider.ContentFull, true},
		{"old/gone.go", "", provider.ChangeDeleted, 0, 2, "base gone", "<nil>", provider.ContentFull, provider.ContentNotApplicable, true},
		{"src/new.go", "", provider.ChangeAdded, 2, 0, "<nil>", "head new", provider.ContentNotApplicable, provider.ContentFull, true},
	}
	if got := view(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("files:\n got %+v\nwant %+v", got, want)
	}
	if len(d.Skipped) != 1 || d.Skipped[0] != (provider.SkippedFile{Path: "assets/logo.png", Reason: provider.SkipBinary}) {
		t.Fatalf("skipped = %+v", d.Skipped)
	}
	if !strings.HasPrefix(d.Files[0].Patch, "@@ -1,2 +1,2 @@\n") {
		t.Fatalf("patch is not hunk-only: %q", d.Files[0].Patch)
	}
	// The rename: base is fetched at the OLD path, head at the new path.
	var baseAtOld, headAtNew, baseAtNew bool
	for _, r := range f.rawRequests() {
		switch {
		case r.Path == repoAPI+"/raw/src/old_name.go" && r.Query == "ref=mergesha":
			baseAtOld = true
		case r.Path == repoAPI+"/raw/src/renamed.go" && r.Query == "ref=headsha":
			headAtNew = true
		case r.Path == repoAPI+"/raw/src/renamed.go" && r.Query == "ref=mergesha":
			baseAtNew = true
		case strings.Contains(r.Path, "logo.png"):
			t.Error("binary file content was requested")
		}
	}
	if !baseAtOld || !headAtNew || baseAtNew {
		t.Fatalf("rename fetches: baseAtOld=%v headAtNew=%v baseAtNew=%v", baseAtOld, headAtNew, baseAtNew)
	}
	// 4 files: 2 sides each, minus 1 N/A for added and 1 for deleted = 6.
	if n := len(f.rawRequests()); n != 6 {
		t.Fatalf("raw requests = %d, want 6", n)
	}
}

func TestGetDiffDeterministicUnderConcurrency(t *testing.T) {
	f := newFake(t, "")
	f.standard("")
	f.rawDelay = 20 * time.Millisecond
	p := f.provider(t, nil)
	var first []fileView
	for i := 0; i < 3; i++ {
		d, err := p.GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = view(d)
		} else if !reflect.DeepEqual(first, view(d)) {
			t.Fatalf("run %d differs", i)
		}
	}
	if m := f.maxInflight.Load(); m > 4 || m < 2 {
		t.Fatalf("max concurrent raw requests = %d, want 2..4", m)
	}
}

func TestGetDiffConcurrencyBound(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", prAPI, prJSON(""))
	var diff strings.Builder
	var metas []any
	for i := 0; i < 12; i++ {
		name := "src/f" + string(rune('a'+i)) + ".go"
		diff.WriteString("diff --git a/" + name + " b/" + name + "\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/" + name + "\n@@ -0,0 +1 @@\n+x\n")
		metas = append(metas, fileMeta(name, "", "added", 1, 0))
		f.setRaw("headsha", name, "x")
	}
	f.handle("GET", prAPI+".diff", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, diff.String()) })
	f.handlePages(prAPI+"/files", metas)
	f.rawDelay = 15 * time.Millisecond
	d, err := f.provider(t, nil).GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{})
	if err != nil || len(d.Files) != 12 {
		t.Fatalf("err %v files %d", err, len(d.Files))
	}
	if m := f.maxInflight.Load(); m > 4 {
		t.Fatalf("concurrency %d exceeds 4", m)
	}
}

func TestIncludeFilterFetchesNothingForFiltered(t *testing.T) {
	f := newFake(t, "")
	f.standard("")
	p := f.provider(t, nil)
	opts := provider.DiffOptions{Include: func(path string) bool { return path != "src/app.go" && path != "assets/logo.png" }}
	d, err := p.GetDiff(context.Background(), ref(), stdPR(), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, fp := range d.Files {
		if fp.Path == "src/app.go" {
			t.Fatal("filtered file is in Files")
		}
	}
	got := map[string]string{}
	for _, s := range d.Skipped {
		got[s.Path] = s.Reason
	}
	if got["src/app.go"] != provider.SkipFiltered || got["assets/logo.png"] != provider.SkipFiltered || len(got) != 2 {
		t.Fatalf("skipped = %+v", d.Skipped)
	}
	for _, r := range f.rawRequests() {
		if strings.Contains(r.Path, "src/app.go") || strings.Contains(r.Path, "logo.png") {
			t.Fatalf("filtered file was fetched: %s", r.Path)
		}
	}
	if n := len(f.rawRequests()); n != 6-2 {
		t.Fatalf("raw requests = %d, want 4", n)
	}
}

func TestFileCountLimit(t *testing.T) {
	f := newFake(t, "")
	f.standard("")
	p := f.provider(t, func(c *config.Config) { c.Diff.MaxFilesFullContent = 2 })
	d, err := p.GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := view(d)
	if got[0].BaseStatus != provider.ContentFull || got[0].HeadStatus != provider.ContentFull ||
		got[1].BaseStatus != provider.ContentFull || got[1].HeadStatus != provider.ContentFull {
		t.Fatalf("first two files must have contents: %+v", got[:2])
	}
	// gone.go: deleted, head side stays not_applicable; new.go: added, base N/A.
	if got[2].BaseStatus != provider.ContentNotFetchedFileCap || got[2].HeadStatus != provider.ContentNotApplicable {
		t.Fatalf("gone.go: %+v", got[2])
	}
	if got[3].BaseStatus != provider.ContentNotApplicable || got[3].HeadStatus != provider.ContentNotFetchedFileCap {
		t.Fatalf("new.go: %+v", got[3])
	}
	for _, v := range got[2:] {
		if !v.HasPatch || v.Base != "<nil>" || v.Head != "<nil>" {
			t.Fatalf("patch must be kept without contents: %+v", v)
		}
	}
	if n := len(f.rawRequests()); n != 4 {
		t.Fatalf("raw requests = %d, want 4 (two files, both sides)", n)
	}
}

func TestFileSizeLimitBoundary(t *testing.T) {
	f := newFake(t, "")
	f.standard("")
	f.setRaw("headsha", "src/app.go", "12345678")  // exactly the cap
	f.setRaw("basesha", "src/app.go", "123456789") // one byte over
	p := f.provider(t, func(c *config.Config) { c.Diff.MaxFileBytes = 8 })
	d, err := p.GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var app provider.FilePatch
	for _, fp := range d.Files {
		if fp.Path == "src/app.go" {
			app = fp
		}
	}
	if app.HeadStatus != provider.ContentFull || deref(app.HeadContent) != "12345678" {
		t.Fatalf("exact-cap head: %v %q", app.HeadStatus, deref(app.HeadContent))
	}
	if app.BaseStatus != provider.ContentNotFetchedSizeCap || app.BaseContent != nil || app.Patch == "" {
		t.Fatalf("over-cap base: %v content=%v patch kept=%v", app.BaseStatus, app.BaseContent, app.Patch != "")
	}
}

func TestPerFile404KeepsPatch(t *testing.T) {
	f := newFake(t, "")
	f.standard("")
	f.raw["headsha:src/app.go"] = rawEntry{status: 404, body: "no such file " + testMarker}
	f.raw["basesha:src/new.go"] = rawEntry{} // unused
	p := f.provider(t, nil)
	d, err := p.GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{})
	if err != nil {
		t.Fatalf("a per-file 404 must not fail GetDiff: %v", err)
	}
	var app provider.FilePatch
	for _, fp := range d.Files {
		if fp.Path == "src/app.go" {
			app = fp
		}
	}
	if app.HeadStatus != provider.ContentFetchFailed || app.HeadContent != nil || app.Patch == "" {
		t.Fatalf("app.go: %+v", app)
	}
	if app.BaseStatus != provider.ContentFull {
		t.Fatalf("the other side must still be fetched: %v", app.BaseStatus)
	}
	if len(d.Files) != 4 {
		t.Fatalf("files = %d", len(d.Files))
	}
}

func TestFilesOnlyAndDiffOnly(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", prAPI, prJSON(""))
	f.handle("GET", prAPI+".diff", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, sampleDiff) })
	// /files lacks src/new.go (diff-only) and lists ghost.go (files-only).
	f.handlePages(prAPI+"/files", []any{
		fileMeta("ghost.go", "", "changed", 1, 1),
		fileMeta("src/app.go", "", "changed", 1, 1),
	})
	f.setRaw("headsha", "src/app.go", "h")
	f.setRaw("basesha", "src/app.go", "b")
	f.setRaw("headsha", "src/new.go", "n")
	f.setRaw("basesha", "old/gone.go", "g")
	f.setRaw("headsha", "src/renamed.go", "r")
	f.setRaw("basesha", "src/old_name.go", "o")
	var logs bytes.Buffer
	cfg := f.config()
	p, err := gitea.NewFactory().New(cfg, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, fp := range d.Files {
		paths = append(paths, fp.Path)
	}
	// API order first (app.go), then diff-only files in diff order.
	if want := []string{"src/app.go", "src/new.go", "old/gone.go", "src/renamed.go"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	reasons := map[string]string{}
	for _, s := range d.Skipped {
		reasons[s.Path] = s.Reason
	}
	if reasons["ghost.go"] != provider.SkipFetchFailed || reasons["assets/logo.png"] != provider.SkipBinary {
		t.Fatalf("skipped = %+v", d.Skipped)
	}
	if !strings.Contains(logs.String(), "join mismatch") {
		t.Fatalf("mismatch not logged at debug:\n%s", logs.String())
	}
	for _, r := range f.rawRequests() {
		if strings.Contains(r.Path, "ghost.go") {
			t.Fatal("files-only entry must not be fetched")
		}
	}
}

// [canary] Gitea pagination: a short page is not the end.
func TestFilesPaginationShortPageFollowedByMore(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", prAPI, prJSON(""))
	var diff strings.Builder
	var metas []any
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		diff.WriteString("diff --git a/" + name + " b/" + name + "\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ b/" + name + "\n@@ -0,0 +1 @@\n+x\n")
		metas = append(metas, fileMeta(name, "", "added", 1, 0))
		f.setRaw("headsha", name, "x")
	}
	// Only the first file is in the diff, so /files is the sole source of the
	// other two: they must show up as Skipped (fetch_failed).
	f.handle("GET", prAPI+".diff", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.SplitAfter(diff.String(), "+x\n")[0])
	})
	// Page 1 is short (1 item, limit 50) yet page 2 still has items.
	f.handlePages(prAPI+"/files", metas[:1], metas[1:])
	d, err := f.provider(t, nil).GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	seen := len(d.Files) + len(d.Skipped)
	if seen != 3 {
		t.Fatalf("collected %d of 3 files from the paged listing: files=%d skipped=%+v", seen, len(d.Files), d.Skipped)
	}
}

func TestDiffOverCap(t *testing.T) {
	f := newFake(t, "")
	f.standard("")
	exact := len(sampleDiff)
	p := f.provider(t, func(c *config.Config) { c.Diff.MaxDiffBytes = exact })
	if _, err := p.GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{}); err != nil {
		t.Fatalf("a diff of exactly the cap must pass: %v", err)
	}
	p = f.provider(t, func(c *config.Config) { c.Diff.MaxDiffBytes = exact - 1 })
	_, err := p.GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{})
	var perr *provider.Error
	if !errors.As(err, &perr) || perr.Class != provider.ClassTooLarge || perr.Hint != "diff.max_diff_bytes" {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, provider.ErrTooLarge) {
		t.Fatal("errors.Is too_large failed")
	}
}

func TestDiffParseErrorIsProtocolWithFixedHint(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", prAPI, prJSON(""))
	f.handle("GET", prAPI+".diff", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "diff --git a/secret/"+testMarker+".go b/secret/"+testMarker+".go\n--- a/x\n+++ b/x\n@@ -1,5 +1,5 @@\n context\n")
	})
	_, err := f.provider(t, nil).GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{})
	var perr *provider.Error
	if !errors.As(err, &perr) || perr.Class != provider.ClassProtocol || perr.Hint != "the diff could not be parsed" {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testMarker) || strings.Contains(err.Error(), "secret/") {
		t.Fatalf("parser text leaked: %v", err)
	}
}

const traversalDiff = `diff --git a/a/../b.go b/a/../b.go
index 1111111..2222222 100644
--- a/a/../b.go
+++ b/a/../b.go
@@ -1 +1 @@
-x
+y
diff --git a/dot/./c.go b/dot/./c.go
index 1111111..2222222 100644
--- a/dot/./c.go
+++ b/dot/./c.go
@@ -1 +1 @@
-x
+y
diff --git a/empty//d.go b/empty//d.go
index 1111111..2222222 100644
--- a/empty//d.go
+++ b/empty//d.go
@@ -1 +1 @@
-x
+y
diff --git a/dir with space/a#b%.go b/dir with space/a#b%.go
index 1111111..2222222 100644
--- a/dir with space/a#b%.go
+++ b/dir with space/a#b%.go
@@ -1 +1 @@
-x
+y
`

func TestRawPathTraversalMakesNoRequest(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", prAPI, prJSON(""))
	f.handle("GET", prAPI+".diff", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, traversalDiff) })
	f.handlePages(prAPI+"/files", []any{
		fileMeta("a/../b.go", "", "changed", 1, 1), fileMeta("dot/./c.go", "", "changed", 1, 1),
		fileMeta("empty//d.go", "", "changed", 1, 1), fileMeta("dir with space/a#b%.go", "", "changed", 1, 1),
	})
	f.setRaw("headsha", "dir with space/a#b%.go", "h")
	f.setRaw("basesha", "dir with space/a#b%.go", "b")
	d, err := f.provider(t, nil).GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 4 {
		t.Fatalf("files = %d", len(d.Files))
	}
	for _, fp := range d.Files[:3] {
		if fp.BaseStatus != provider.ContentFetchFailed || fp.HeadStatus != provider.ContentFetchFailed || fp.Patch == "" {
			t.Errorf("%s: %v/%v", fp.Path, fp.BaseStatus, fp.HeadStatus)
		}
	}
	raws := f.rawRequests()
	if len(raws) != 2 {
		t.Fatalf("raw requests = %+v, want only the two for the safe path", raws)
	}
	for _, r := range raws {
		if r.Path != repoAPI+"/raw/dir%20with%20space/a%23b%25.go" {
			t.Errorf("raw path not escaped per segment: %s", r.Path)
		}
	}
}

func TestRefQueryEscaped(t *testing.T) {
	f := newFake(t, "")
	pr := prJSON("")
	pr["head"] = map[string]any{"ref": "feature", "sha": "a b&c"}
	f.handleJSON("GET", prAPI, pr)
	f.handle("GET", prAPI+".diff", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, sampleDiff) })
	f.handlePages(prAPI+"/files", sampleFiles())
	for _, p := range []string{"src/app.go", "src/new.go", "src/renamed.go"} {
		f.setRaw("a b&c", p, "h")
	}
	for _, p := range []string{"src/app.go", "old/gone.go", "src/old_name.go"} {
		f.setRaw("basesha", p, "b")
	}
	if _, err := f.provider(t, nil).GetDiff(context.Background(), ref(), &provider.PullRequest{HeadSHA: "a b&c", BaseSHA: "basesha"}, provider.DiffOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.rawRequests() {
		if strings.Contains(r.Query, "a+b%26c") {
			return
		}
	}
	t.Fatalf("ref was not query-escaped: %+v", f.rawRequests())
}

func TestCommitsOrderReversalAcrossPages(t *testing.T) {
	f := newFake(t, "/gitea")
	c := func(m string) any { return map[string]any{"commit": map[string]any{"message": m}} }
	// Newest first, with a short first page followed by more.
	f.handlePages(prAPI+"/commits", []any{c("third"), c("second")}, []any{c("first")})
	got, err := f.provider(t, nil).GetCommitMessages(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"first", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPostComment(t *testing.T) {
	f := newFake(t, "")
	var ct string
	f.handle("POST", repoAPI+"/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id": 4242, "html_url": "https://your-gitea.example/octo/demo/pulls/7#issuecomment-4242"}`)
	})
	c, err := f.provider(t, nil).PostComment(context.Background(), ref(), "hello \"world\"\n## h")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "4242" || c.URL != "https://your-gitea.example/octo/demo/pulls/7#issuecomment-4242" {
		t.Fatalf("comment = %+v", c)
	}
	reqs := f.requests()
	if len(reqs) != 1 || reqs[0].Body != `{"body":"hello \"world\"\n## h"}` {
		t.Fatalf("requests = %+v", reqs)
	}
	if ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
}

func TestErrorClassMapping(t *testing.T) {
	cases := []struct {
		status int
		want   *provider.Error
	}{
		{401, provider.ErrAuth}, {403, provider.ErrAuth}, {404, provider.ErrNotFound},
		{429, provider.ErrRateLimited}, {500, provider.ErrUpstream}, {502, provider.ErrUpstream},
	}
	for _, c := range cases {
		f := newFake(t, "")
		st := c.status
		f.handle("GET", prAPI, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom "+testMarker+" "+testToken, st)
		})
		f.handle("POST", repoAPI+"/issues/7/comments", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom "+testMarker, st)
		})
		p := f.provider(t, nil)
		_, err := p.GetPullRequest(context.Background(), ref())
		_, err2 := p.PostComment(context.Background(), ref(), "x")
		for _, e := range []error{err, err2} {
			if !errors.Is(e, c.want) {
				t.Errorf("status %d: err = %v, want class %s", c.status, e, c.want.Class)
				continue
			}
			if s := e.Error(); strings.Contains(s, testMarker) || strings.Contains(s, testToken) {
				t.Errorf("status %d: error leaks: %s", c.status, s)
			}
		}
	}
}

func TestContextCancellation(t *testing.T) {
	f := newFake(t, "")
	f.standard("")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.rawHook = func(*http.Request) { cancel() }
	f.rawDelay = 2 * time.Second
	_, err := f.provider(t, nil).GetDiff(ctx, ref(), stdPR(), provider.DiffOptions{})
	var perr *provider.Error
	if !errors.As(err, &perr) || perr.Class != provider.ClassTransport || perr.Hint != "canceled" {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidRefMakesNoRequest(t *testing.T) {
	f := newFake(t, "")
	p := f.provider(t, nil)
	for _, r := range []provider.PRRef{
		{Namespace: "..", Repo: "demo", Number: 7}, {Namespace: "octo", Repo: "", Number: 7}, {Namespace: "octo", Repo: "demo"},
	} {
		if _, err := p.GetPullRequest(context.Background(), r); !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%+v: err = %v", r, err)
		}
	}
	if len(f.requests()) != 0 {
		t.Fatal("requests were made")
	}
}

func TestFileLineURL(t *testing.T) {
	cfg := config.Defaults()
	cfg.Gitea.BaseURL = "https://your-gitea.example/gitea/"
	p, _ := gitea.NewFactory().New(cfg, nil)
	pr := &provider.PullRequest{HeadSHA: "abc123"}
	got := p.FileLineURL(ref(), pr, "dir with space/a#b.go", 12)
	if want := "https://your-gitea.example/gitea/octo/demo/src/commit/abc123/dir%20with%20space/a%23b.go#L12"; got != want {
		t.Fatalf("got %s", got)
	}
	cfg.Gitea.WebURL = "https://web.example.com/ui"
	p, _ = gitea.NewFactory().New(cfg, nil)
	if got := p.FileLineURL(ref(), pr, "src/app.go", 3); got != "https://web.example.com/ui/octo/demo/src/commit/abc123/src/app.go#L3" {
		t.Fatalf("web_url not used: %s", got)
	}
	if got := p.FileLineURL(ref(), pr, "src/app.go", 0); strings.Contains(got, "#") {
		t.Fatalf("line 0 must not add an anchor: %s", got)
	}
	if p.FileLineURL(ref(), nil, "x", 1) != "" {
		t.Fatal("nil PR must give empty URL")
	}
}

func TestResolverIntegration(t *testing.T) {
	f := newFake(t, "/gitea")
	f.standard("")
	cfg := f.config()
	cfg.Gitea.WebURL = f.srv.URL + "/ui"
	r := provider.NewResolver(cfg, nil, gitea.NewFactory())
	for _, raw := range []string{
		f.baseURL() + "/octo/demo/pulls/7",
		f.baseURL() + "/octo/demo/pulls/7/files?x=1#frag",
		f.srv.URL + "/ui/octo/demo/pulls/7", // web_url variant
	} {
		pref, p, err := r.Resolve(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if pref.Kind != provider.KindGitea || pref.Namespace != "octo" || pref.Repo != "demo" || pref.Number != 7 {
			t.Fatalf("ref = %+v", pref)
		}
		// API calls go to base_url even when the URL matched web_url.
		if _, err := p.GetPullRequest(context.Background(), pref); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, rq := range f.requests() {
		if !strings.HasPrefix(rq.Path, "/api/v1/") {
			t.Fatalf("unexpected path %s", rq.Path)
		}
	}
	if _, _, err := r.Resolve(f.baseURL() + "/octo/demo/issues/7"); !errors.Is(err, provider.ErrURLMalformed) {
		t.Fatalf("issue URL: %v", err)
	}
	before := len(f.requests())
	for _, raw := range []string{
		"https://your-gitea.example.evil.com/octo/demo/pulls/7",
		f.srv.URL + "/gitea-x/octo/demo/pulls/7",
		f.srv.URL + "/other/octo/demo/pulls/7",
	} {
		if _, _, err := r.Resolve(raw); !errors.Is(err, provider.ErrURLNotConfigured) {
			t.Errorf("%s: err = %v", raw, err)
		}
	}
	if len(f.requests()) != before {
		t.Fatal("resolver performed I/O")
	}
}

func TestMapStatusViaDiffTypes(t *testing.T) {
	// Unknown /files statuses must not break the join or fail GetDiff.
	f := newFake(t, "")
	f.standard("")
	f.handlePages(prAPI+"/files", []any{
		fileMeta("src/app.go", "", "weird-new-status", 1, 1),
		fileMeta("src/new.go", "", "copied", 2, 0),
	})
	d, err := f.provider(t, nil).GetDiff(context.Background(), ref(), stdPR(), provider.DiffOptions{})
	if err != nil || len(d.Files) < 2 || d.Files[0].Type != provider.ChangeModified {
		t.Fatalf("err %v files %+v", err, d)
	}
}

// [canary] X-8: at debug level neither the token nor any response-body
// marker is logged, and neither the token nor the marker is in an error.
func TestDebugLogsAndErrorsDoNotLeak(t *testing.T) {
	f := newFake(t, "/gitea")
	f.standard("mergesha")
	c := func(m string) any { return map[string]any{"commit": map[string]any{"message": m + testMarker}} }
	f.handlePages(prAPI+"/commits", []any{c("b")}, []any{c("a")})
	f.handle("POST", repoAPI+"/issues/7/comments", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id": 9, "html_url": "https://your-gitea.example/c/9", "body": "`+testMarker+`"}`)
	})
	// A raw 500 whose body echoes both the marker and the token.
	f.raw["headsha:src/new.go"] = rawEntry{status: 500, body: "oops " + testMarker + " " + testToken}
	// Error paths.
	f.handle("GET", repoAPI+"/pulls/8", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing "+testMarker+" "+testToken, http.StatusNotFound)
	})
	f.handle("GET", repoAPI+"/pulls/9", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "denied "+testMarker+" "+testToken, http.StatusUnauthorized)
	})
	f.handle("GET", repoAPI+"/pulls/10", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"title": not json `+testMarker+testToken)
	})

	var logs bytes.Buffer
	p, err := gitea.NewFactory().New(f.config(), slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var errs []error
	pr, err := p.GetPullRequest(ctx, ref())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pr.Description, testMarker) {
		t.Fatal("marker expected in returned data")
	}
	if _, err := p.GetCommitMessages(ctx, ref()); err != nil {
		t.Fatal(err)
	}
	d, err := p.GetDiff(ctx, ref(), pr, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var sawFailed bool
	for _, fp := range d.Files {
		if fp.HeadStatus == provider.ContentFetchFailed {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Fatal("expected a fetch_failed side")
	}
	if _, err := p.PostComment(ctx, ref(), "body "+testMarker); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int64{8, 9, 10} {
		r := ref()
		r.Number = n
		_, err := p.GetPullRequest(ctx, r)
		errs = append(errs, err)
	}
	for _, e := range errs {
		if e == nil {
			t.Fatal("expected error")
		}
		if s := e.Error(); strings.Contains(s, testToken) || strings.Contains(s, testMarker) {
			t.Errorf("error leaks: %s", s)
		}
	}
	out := logs.String()
	if !strings.Contains(out, "http request") {
		t.Fatalf("debug log is empty, test is not meaningful:\n%s", out)
	}
	if strings.Contains(out, testToken) {
		t.Error("token appears in logs")
	}
	if strings.Contains(out, testMarker) {
		t.Errorf("response body marker appears in logs:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "authorization") {
		t.Error("an Authorization header name appears in logs")
	}
}
