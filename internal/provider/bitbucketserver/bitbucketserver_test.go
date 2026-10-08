package bitbucketserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
)

func TestKindAndCapabilities(t *testing.T) {
	f := newFake(t, "")
	p := f.provider(t, nil)
	if p.Kind() != provider.KindBitbucketServer || bitbucketserver.NewFactory().Kind() != provider.KindBitbucketServer {
		t.Fatal("wrong kind")
	}
	want := provider.Capabilities{
		GFM: false, MarkdownTables: true, Labels: false, InlineComments: true,
		ThreadResolution: true, DescriptionEdit: true,
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
		{"/projects/PROJ/repos/demo/pull-requests/7", "PROJ", "demo", 7, true},
		{"/projects/PROJ/repos/demo/pull-requests/7/overview", "PROJ", "demo", 7, true},
		{"/projects/PROJ/repos/demo/pull-requests/7/diff", "PROJ", "demo", 7, true},
		{"/projects/PROJ/repos/demo/pull-requests/7/", "PROJ", "demo", 7, true},
		{"/projects/PROJ/repos/demo/pull-requests/007", "PROJ", "demo", 7, true},
		{"/users/jdoe/repos/demo/pull-requests/12", "~jdoe", "demo", 12, true},
		{"/users/jdoe/repos/demo/pull-requests/12/overview", "~jdoe", "demo", 12, true},
		{"/projects/P%20X/repos/de%6Do/pull-requests/3", "P X", "demo", 3, true},
		{"/projects/PROJ/repos/demo/pull-requests/0", "", "", 0, false},
		{"/projects/PROJ/repos/demo/pull-requests/-1", "", "", 0, false},
		{"/projects/PROJ/repos/demo/pull-requests/+1", "", "", 0, false},
		{"/projects/PROJ/repos/demo/pull-requests/1x", "", "", 0, false},
		{"/projects/PROJ/repos/demo/pull-requests/", "", "", 0, false},
		{"/projects/PROJ/repos/demo/pull-requests", "", "", 0, false},
		{"/projects/PROJ/repos/demo/pull-requests/99999999999999999999", "", "", 0, false},
		{"/projects/PROJ/repos/demo/commits/7", "", "", 0, false},
		{"/projects/PROJ/browse/demo/pull-requests/7", "", "", 0, false},
		{"/teams/PROJ/repos/demo/pull-requests/7", "", "", 0, false},
		{"/projects//repos/demo/pull-requests/7", "", "", 0, false},
		{"/projects/PROJ/repos//pull-requests/7", "", "", 0, false},
		{"/projects/./repos/demo/pull-requests/7", "", "", 0, false},
		{"/projects/PROJ/repos/../pull-requests/7", "", "", 0, false},
		{"/projects/%2e%2e/repos/demo/pull-requests/7", "", "", 0, false},
		{"/projects/PROJ%2Fsub/repos/demo/pull-requests/7", "", "", 0, false},
		{"/users/jdoe%2fx/repos/demo/pull-requests/7", "", "", 0, false},
		{"/projects/PROJ/sub/repos/demo/pull-requests/7", "", "", 0, false},
		{"/users//repos/demo/pull-requests/7", "", "", 0, false},
		{"/projects/PR%zzOJ/repos/demo/pull-requests/7", "", "", 0, false},
		{"projects/PROJ/repos/demo/pull-requests/7", "", "", 0, false},
		{"/PROJ/demo/pull-requests/7", "", "", 0, false},
		{"", "", "", 0, false},
	}
	for _, c := range cases {
		ns, rep, n, err := bitbucketserver.NewFactory().ParsePRPath(c.in)
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
	_, err := bitbucketserver.NewFactory().New(config.Defaults(), nil)
	if !errors.Is(err, provider.ErrURLNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetPullRequestMergeBaseUnderContextPath(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.standard()
	var ua string
	f.handle("GET", prAPI, func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		_ = json.NewEncoder(w).Encode(prJSON())
	})
	p := f.provider(t, nil)
	pr, err := p.GetPullRequest(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	want := provider.PullRequest{
		Title: "Add feature", Description: "Description " + testMarker, Author: "jdoe",
		SourceBranch: "feature", TargetBranch: "main", HeadSHA: "headsha", BaseSHA: "mergesha",
		WebURL: webPR, State: "OPEN", BaseStrategy: provider.BaseBBSMergeBaseEP,
	}
	if *pr != want {
		t.Fatalf("pr = %+v", *pr)
	}
	if !strings.HasPrefix(ua, "review-mcp/") {
		t.Fatalf("User-Agent = %q", ua)
	}
	reqs := f.requests()
	var paths []string
	for _, r := range reqs {
		paths = append(paths, r.Method+" "+r.Path)
	}
	wantPaths := []string{"GET " + propsAPI, "GET " + prAPI, "GET " + mergeBase}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("requests = %v, want %v", paths, wantPaths)
	}
}

func TestAuthorFallsBackToDisplayName(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	pj := prJSON()
	pj["author"] = map[string]any{"user": map[string]any{"displayName": "J. Doe"}}
	pj["links"] = map[string]any{}
	f.handleJSON("GET", prAPI, pj)
	pr, err := f.provider(t, nil).GetPullRequest(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	if pr.Author != "J. Doe" || pr.WebURL != "" {
		t.Fatalf("pr = %+v", pr)
	}
}

func TestVersionProbe(t *testing.T) {
	ctx := context.Background()
	t.Run("below 7 is unsupported_version before any other call", func(t *testing.T) {
		for _, v := range []string{"6.10.2", "5.16.0", "0.9"} {
			f := newFake(t, "/bitbucket")
			f.standard()
			f.handleJSON("GET", propsAPI, map[string]any{"version": v})
			p := f.provider(t, nil)
			ops := map[string]func() error{
				"GetPullRequest":    func() error { _, err := p.GetPullRequest(ctx, ref()); return err },
				"GetCommitMessages": func() error { _, err := p.GetCommitMessages(ctx, ref()); return err },
				"GetDiff":           func() error { _, err := p.GetDiff(ctx, ref(), stdPR(), provider.DiffOptions{}); return err },
				"PostComment":       func() error { _, err := p.PostComment(ctx, ref(), "x"); return err },
			}
			for name, op := range ops {
				err := op()
				if !errors.Is(err, provider.ErrUnsupportedVersion) {
					t.Errorf("%s with version %s: err = %v", name, v, err)
				}
			}
			reqs := f.requests()
			if len(reqs) != 1 || reqs[0].Path != propsAPI {
				t.Errorf("version %s: requests = %+v, want only the probe", v, reqs)
			}
		}
	})
	t.Run("7 and later work", func(t *testing.T) {
		for _, v := range []string{"7.0.0", "7.21.3-SNAPSHOT", "8.9.0", "9.1.0", "10.0.0"} {
			f := newFake(t, "")
			f.standard()
			f.handleJSON("GET", propsAPI, map[string]any{"version": v})
			if _, err := f.provider(t, nil).GetPullRequest(ctx, ref()); err != nil {
				t.Errorf("version %s: %v", v, err)
			}
		}
	})
	t.Run("probe failure continues", func(t *testing.T) {
		for _, status := range []int{401, 403, 404, 429, 500} {
			f := newFake(t, "")
			f.standard()
			f.handleStatus("GET", propsAPI, status)
			if _, err := f.provider(t, nil).GetPullRequest(ctx, ref()); err != nil {
				t.Errorf("probe status %d: %v", status, err)
			}
		}
		for name, body := range map[string]string{"not json": "<html>" + testMarker, "no version": `{}`, "odd version": `{"version":"dev"}`} {
			f := newFake(t, "")
			f.standard()
			f.handle("GET", propsAPI, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
			if _, err := f.provider(t, nil).GetPullRequest(ctx, ref()); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
	t.Run("once per provider instance", func(t *testing.T) {
		f := newFake(t, "")
		f.standard()
		f.handle("GET", v1Repo+"/pull-requests/7/commits", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"values":[],"isLastPage":true}`)
		})
		p := f.provider(t, nil)
		for range 2 {
			if _, err := p.GetPullRequest(ctx, ref()); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := p.GetCommitMessages(ctx, ref()); err != nil {
			t.Fatal(err)
		}
		if n := f.count("GET", propsAPI); n != 1 {
			t.Fatalf("probe requests = %d, want 1", n)
		}
		// A failed probe is also not repeated.
		f2 := newFake(t, "")
		f2.standard()
		f2.handleStatus("GET", propsAPI, 500)
		p2 := f2.provider(t, nil)
		for range 2 {
			if _, err := p2.GetPullRequest(ctx, ref()); err != nil {
				t.Fatal(err)
			}
		}
		if n := f2.count("GET", propsAPI); n != 1 {
			t.Fatalf("failed probe repeated: %d requests", n)
		}
	})
}

// ---- base revision ----

func commitJSON(id, msg string, parents ...string) map[string]any {
	ps := []any{}
	for _, p := range parents {
		ps = append(ps, map[string]any{"id": p})
	}
	return map[string]any{"id": id, "message": msg, "parents": ps}
}

// ancestorHistory registers the synthetic history of the ancestor-walk
// canary: the PR branch merged the target branch after branching.
//
//	target: T0 - T1 - T2 (toRef.latestCommit = T2)
//	PR:     P1 (parent T0) - P2 (merge commit, parents [P1, T1]) - P3 (parent P2)
//
// PR commits in API order are [P3, P2, P1]; the destination commits since T0
// until T2 are [T2, T1]. Both lists are split over two pages. The true merge
// base is T1; a first-parent-only implementation would return T0.
func (f *fakeBBS) ancestorHistory() {
	f.handleJSON("GET", propsAPI, map[string]any{"version": "7.6.0"})
	pj := prJSON()
	pj["toRef"] = map[string]any{"displayId": "main", "latestCommit": "T2"}
	f.handleJSON("GET", prAPI, pj)
	f.handleStatus("GET", mergeBase, http.StatusNotFound)
	f.handlePaged(prAPI+"/commits", nil,
		[]any{commitJSON("P3", "third "+testMarker, "P2")},
		[]any{commitJSON("P2", "merge target into feature", "P1", "T1"), commitJSON("P1", "first", "T0")})
	f.handlePaged(v1Repo+"/commits", func(q url.Values) {
		if q["since"][0] != "T0" || q["until"][0] != "T2" {
			f.t.Errorf("destination history asked for since=%v until=%v, want T0..T2", q["since"], q["until"])
		}
	},
		[]any{commitJSON("T2", "t2", "T1")},
		[]any{commitJSON("T1", "t1", "T0")})
}

// [canary] The ancestor walk must return the true common ancestor (T1), not
// the first parent of the oldest PR commit (T0).
func TestAncestorWalkMergedTargetBranch(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.ancestorHistory()
	f.setRaw("headsha", "a.txt", "new\n")
	f.setRaw("T1", "a.txt", "old\n")
	f.handlePaged(prAPI+"/changes", nil, []any{change("MODIFY", "a.txt", "")})
	p := f.provider(t, nil)
	pr, err := p.GetPullRequest(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	if pr.BaseSHA != "T1" {
		t.Fatalf("BaseSHA = %q, want T1 (the merged target commit); T0 would include already-merged target commits", pr.BaseSHA)
	}
	d, err := p.GetDiff(context.Background(), ref(), pr, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pr.BaseStrategy != provider.BaseBBSAncestorWalk || d.BaseStrategy != provider.BaseBBSAncestorWalk || len(d.Files) != 1 {
		t.Fatalf("diff = %+v", d)
	}
	// Base content must have been requested at the walked base.
	raws := f.rawRequests()
	if len(raws) != 2 || raws[0].Query != "at=T1" && raws[1].Query != "at=T1" {
		t.Fatalf("raw requests = %+v", raws)
	}
	// Merge-base was probed first; the walk used two pages of each list.
	if f.count("GET", mergeBase) != 1 {
		t.Fatal("merge-base endpoint was not tried first")
	}
	var commitPages int
	for _, r := range f.requests() {
		if r.Path == v1Repo+"/commits" {
			commitPages++
		}
	}
	if commitPages != 2 {
		t.Fatalf("destination history pages = %d, want 2", commitPages)
	}
}

func TestAncestorWalkNoMergeFallsBackToGuaranteedAncestor(t *testing.T) {
	// Linear PR branch: the match for the oldest commit is G itself.
	f := newFake(t, "")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "7.6.0"})
	pj := prJSON()
	pj["toRef"] = map[string]any{"displayId": "main", "latestCommit": "T2"}
	f.handleJSON("GET", prAPI, pj)
	f.handleStatus("GET", mergeBase, http.StatusNotFound)
	f.handlePaged(prAPI+"/commits", nil, []any{commitJSON("P2", "b", "P1"), commitJSON("P1", "a", "T0")})
	f.handlePaged(v1Repo+"/commits", nil, []any{commitJSON("T2", "t2", "T1"), commitJSON("T1", "t1", "T0")})
	pr, err := f.provider(t, nil).GetPullRequest(context.Background(), ref())
	if err != nil || pr.BaseSHA != "T0" {
		t.Fatalf("pr %+v err %v", pr, err)
	}
}

func TestAncestorWalkProtocolErrors(t *testing.T) {
	cases := map[string][]any{
		"empty commit list": {},
		"oldest has no parents": {
			commitJSON("P1", "a"),
		},
	}
	for name, commits := range cases {
		f := newFake(t, "")
		f.handleJSON("GET", propsAPI, map[string]any{"version": "7.6.0"})
		f.handleJSON("GET", prAPI, prJSON())
		f.handleStatus("GET", mergeBase, http.StatusNotFound)
		f.handlePaged(prAPI+"/commits", nil, commits)
		p := f.provider(t, nil)
		for opName, op := range map[string]func() error{
			"GetPullRequest": func() error { _, err := p.GetPullRequest(context.Background(), ref()); return err },
		} {
			err := op()
			var perr *provider.Error
			if !errors.As(err, &perr) || perr.Class != provider.ClassProtocol || perr.Hint == "" {
				t.Errorf("%s/%s: err = %v, want protocol with hint", name, opName, err)
			}
		}
		for _, r := range f.requests() {
			if r.Path == v1Repo+"/commits" || strings.Contains(r.Path, "/raw/") || strings.HasSuffix(r.Path, "/changes") {
				t.Errorf("%s: unexpected request after the failure: %s", name, r.Path)
			}
		}
	}
}

func TestMergeBaseFailuresFailTheCall(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   *provider.Error
	}{
		{500, provider.ErrUpstream}, {502, provider.ErrUpstream}, {401, provider.ErrAuth},
		{403, provider.ErrAuth}, {429, provider.ErrRateLimited}, {400, provider.ErrProtocol},
	} {
		f := newFake(t, "")
		f.standard()
		f.handleStatus("GET", mergeBase, tc.status)
		p := f.provider(t, nil)
		_, err := p.GetPullRequest(context.Background(), ref())
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d GetPullRequest: err = %v, want %s", tc.status, err, tc.want.Class)
		}
		for _, r := range f.requests() {
			if strings.HasSuffix(r.Path, "/commits") || strings.Contains(r.Path, "/raw/") || strings.HasSuffix(r.Path, "/changes") {
				t.Errorf("status %d: fell back or continued: %s", tc.status, r.Path)
			}
		}
	}
	// A 200 without an id is a protocol error, not an empty base.
	f := newFake(t, "")
	f.standard()
	f.handleJSON("GET", mergeBase, map[string]any{})
	if _, err := f.provider(t, nil).GetPullRequest(context.Background(), ref()); !errors.Is(err, provider.ErrProtocol) {
		t.Fatalf("empty merge-base id: %v", err)
	}
}

// ---- diff assembly ----

type fileView struct {
	Path, OldPath          string
	Type                   provider.ChangeType
	Add, Del               int
	Base, Head             string
	BaseStatus, HeadStatus provider.ContentStatus
	Patch                  string
}

func view(d *provider.Diff) []fileView {
	var out []fileView
	for _, fp := range d.Files {
		out = append(out, fileView{fp.Path, fp.OldPath, fp.Type, fp.Additions, fp.Deletions,
			deref(fp.BaseContent), deref(fp.HeadContent), fp.BaseStatus, fp.HeadStatus, fp.Patch})
	}
	return out
}

// stdPR is a hand-built PR for GetDiff tests that do not exercise GetPullRequest.
func stdPR() *provider.PullRequest {
	return &provider.PullRequest{HeadSHA: "headsha", BaseSHA: "mergesha", BaseStrategy: provider.BaseBBSMergeBaseEP}
}

func TestGetDiffWithoutBaseSHAMakesNoRequest(t *testing.T) {
	for name, pr := range map[string]*provider.PullRequest{
		"empty BaseSHA": {HeadSHA: "h"},
		"empty HeadSHA": {BaseSHA: "b"},
		"nil":           nil,
	} {
		f := newFake(t, "")
		f.standard()
		d, err := f.provider(t, nil).GetDiff(context.Background(), ref(), pr, provider.DiffOptions{})
		if d != nil || !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%s: diff = %v, err = %v, want a protocol error", name, d, err)
		}
		if n := len(f.requests()); n != 0 {
			t.Errorf("%s: %d requests were made (version probe included), want 0", name, n)
		}
	}
}

func getDiff(t *testing.T, f *fakeBBS, mutate func(*config.Config), opts provider.DiffOptions) *provider.Diff {
	t.Helper()
	_, d := getPRAndDiff(t, f, mutate, opts)
	return d
}

func getPRAndDiff(t *testing.T, f *fakeBBS, mutate func(*config.Config), opts provider.DiffOptions) (*provider.PullRequest, *provider.Diff) {
	t.Helper()
	p := f.provider(t, mutate)
	pr, err := p.GetPullRequest(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.GetDiff(context.Background(), ref(), pr, opts)
	if err != nil {
		t.Fatal(err)
	}
	return pr, d
}

func TestGetDiffContentsPatchesAndRename(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.standard()
	d := getDiff(t, f, nil, provider.DiffOptions{})
	appHead := "package main\nvar a = 2 // " + testMarker + "\n"
	want := []fileView{
		{"src/renamed.go", "src/old_name.go", provider.ChangeRenamed, 1, 1,
			"package main\nvar r = 1\n", "package main\nvar r = 2\n", provider.ContentFull, provider.ContentFull,
			"@@ -1,2 +1,2 @@\n package main\n-var r = 1\n+var r = 2\n"},
		{"src/app.go", "", provider.ChangeModified, 1, 1,
			"package main\nvar a = 1\n", appHead, provider.ContentFull, provider.ContentFull,
			"@@ -1,2 +1,2 @@\n package main\n-var a = 1\n+var a = 2 // " + testMarker + "\n"},
		{"old/gone.go", "", provider.ChangeDeleted, 0, 2,
			"package old\nvar g = 1\n", "<nil>", provider.ContentFull, provider.ContentNotApplicable,
			"@@ -1,2 +0,0 @@\n-package old\n-var g = 1\n"},
		{"src/new.go", "", provider.ChangeAdded, 2, 0,
			"<nil>", "package main\nvar n = 1\n", provider.ContentNotApplicable, provider.ContentFull,
			"@@ -0,0 +1,2 @@\n+package main\n+var n = 1\n"},
	}
	if got := view(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("files:\n got %+v\nwant %+v", got, want)
	}
	if len(d.Skipped) != 0 || d.BaseStrategy != provider.BaseBBSMergeBaseEP {
		t.Fatalf("skipped %+v strategy %q", d.Skipped, d.BaseStrategy)
	}
	// The rename: base is fetched at the OLD path, head at the new path.
	var baseAtOld, headAtNew, baseAtNew int
	for _, r := range f.rawRequests() {
		switch {
		case r.Path == v1Repo+"/raw/src/old_name.go" && r.Query == "at=mergesha":
			baseAtOld++
		case r.Path == v1Repo+"/raw/src/renamed.go" && r.Query == "at=headsha":
			headAtNew++
		case r.Path == v1Repo+"/raw/src/renamed.go" && r.Query == "at=mergesha":
			baseAtNew++
		}
	}
	if baseAtOld != 1 || headAtNew != 1 || baseAtNew != 0 {
		t.Fatalf("rename fetches: baseAtOld=%d headAtNew=%d baseAtNew=%d", baseAtOld, headAtNew, baseAtNew)
	}
	// 4 files, 2 sides each, minus the N/A base of the add and head of the delete.
	if n := len(f.rawRequests()); n != 6 {
		t.Fatalf("raw requests = %d, want 6", n)
	}
	if n := f.count("GET", mergeBase); n != 1 {
		t.Fatalf("merge-base requests = %d, want 1 (GetDiff must not compute the base again)", n)
	}
}

func TestChangeTypeMapping(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	f.handlePaged(prAPI+"/changes", nil, []any{
		change("COPY", "copy.go", "orig.go"),
		change("RENAME", "r.go", "q.go"),
		change("MOVE", "same.go", "same.go"),  // srcPath == path: modified
		change("MOVE", "nosrc.go", ""),        // no srcPath: modified
		change("MODIFY", "m.go", ""),          // modify
		change("SOMETHING_NEW", "unk.go", ""), // unknown: modified
		change("modify", "lower.go", ""),      // case-insensitive
	})
	for _, n := range []string{"copy.go", "r.go", "same.go", "nosrc.go", "m.go", "unk.go", "lower.go"} {
		f.setRaw("headsha", n, "x\n")
	}
	for _, n := range []string{"q.go", "same.go", "nosrc.go", "m.go", "unk.go", "lower.go"} {
		f.setRaw("mergesha", n, "y\n")
	}
	d := getDiff(t, f, nil, provider.DiffOptions{})
	type tv struct {
		typ     provider.ChangeType
		old     string
		baseNil bool
	}
	got := map[string]tv{}
	for _, fp := range d.Files {
		got[fp.Path] = tv{fp.Type, fp.OldPath, fp.BaseContent == nil}
	}
	want := map[string]tv{
		"copy.go":  {provider.ChangeAdded, "", true},
		"r.go":     {provider.ChangeRenamed, "q.go", false},
		"same.go":  {provider.ChangeModified, "", false},
		"nosrc.go": {provider.ChangeModified, "", false},
		"m.go":     {provider.ChangeModified, "", false},
		"unk.go":   {provider.ChangeModified, "", false},
		"lower.go": {provider.ChangeModified, "", false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	for _, r := range f.rawRequests() {
		if strings.Contains(r.Path, "orig.go") {
			t.Fatal("the source of a COPY must not be fetched")
		}
	}
}

func TestGetDiffDeterministicUnderConcurrency(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	f.rawDelay = 20 * time.Millisecond
	p := f.provider(t, nil)
	var first []fileView
	for i := range 3 {
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

func TestGetDiffConcurrencyBoundAndOrder(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	var changes []any
	var order []string
	for i := range 20 {
		name := "src/f" + strconv.Itoa(i) + ".go"
		changes = append(changes, change("ADD", name, ""))
		order = append(order, name)
		f.setRaw("headsha", name, "x\n")
	}
	f.handlePaged(prAPI+"/changes", nil, changes)
	f.rawDelay = 10 * time.Millisecond
	d := getDiff(t, f, func(c *config.Config) { c.Diff.MaxFilesFullContent = 100 }, provider.DiffOptions{})
	if m := f.maxInflight.Load(); m > 4 {
		t.Fatalf("concurrency %d exceeds 4", m)
	}
	var got []string
	for _, fp := range d.Files {
		got = append(got, fp.Path)
	}
	if !reflect.DeepEqual(got, order) {
		t.Fatalf("output order is not API order: %v", got)
	}
}

func TestIncludeFilterFetchesNothingForFiltered(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	opts := provider.DiffOptions{Include: func(path string) bool { return path != "src/app.go" && path != "old/gone.go" }}
	d := getDiff(t, f, nil, opts)
	got := map[string]string{}
	for _, s := range d.Skipped {
		got[s.Path] = s.Reason
	}
	if got["src/app.go"] != provider.SkipFiltered || got["old/gone.go"] != provider.SkipFiltered || len(got) != 2 {
		t.Fatalf("skipped = %+v", d.Skipped)
	}
	if len(d.Files) != 2 {
		t.Fatalf("files = %d", len(d.Files))
	}
	for _, r := range f.rawRequests() {
		if strings.Contains(r.Path, "src/app.go") || strings.Contains(r.Path, "old/gone.go") {
			t.Fatalf("filtered file was fetched: %s", r.Path)
		}
	}
	// renamed (2) + added (1).
	if n := len(f.rawRequests()); n != 3 {
		t.Fatalf("raw requests = %d, want 3", n)
	}
}

func TestFileCountLimitCountsProcessedFilesAfterInclude(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	// Limit 2, with the first API-order file filtered: the next two are
	// processed, the last one is over the limit.
	opts := provider.DiffOptions{Include: func(path string) bool { return path != "src/renamed.go" }}
	d := getDiff(t, f, func(c *config.Config) { c.Diff.MaxFilesFullContent = 2 }, opts)
	var files []string
	for _, fp := range d.Files {
		files = append(files, fp.Path)
	}
	if !reflect.DeepEqual(files, []string{"src/app.go", "old/gone.go"}) {
		t.Fatalf("files = %v", files)
	}
	want := []provider.SkippedFile{
		{Path: "src/renamed.go", Reason: provider.SkipFiltered},
		{Path: "src/new.go", Reason: provider.SkipFileLimit},
	}
	if !reflect.DeepEqual(d.Skipped, want) {
		t.Fatalf("skipped = %+v", d.Skipped)
	}
	for _, r := range f.rawRequests() {
		if strings.Contains(r.Path, "new.go") {
			t.Fatal("file over the limit was fetched")
		}
	}
}

func TestFileSizeLimitBoundary(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	f.handlePaged(prAPI+"/changes", nil, []any{
		change("MODIFY", "exact.txt", ""), change("MODIFY", "baseover.txt", ""),
		change("MODIFY", "headover.txt", ""), change("ADD", "newover.txt", ""),
	})
	f.setRaw("mergesha", "exact.txt", "aaaa\n")     // 5 bytes: exactly the cap
	f.setRaw("headsha", "exact.txt", "bbbb\n")      // 5 bytes
	f.setRaw("mergesha", "baseover.txt", "aaaaa\n") // 6 bytes
	f.setRaw("headsha", "baseover.txt", "b\n")
	f.setRaw("mergesha", "headover.txt", "a\n")
	f.setRaw("headsha", "headover.txt", "bbbbb\n") // 6 bytes
	f.setRaw("headsha", "newover.txt", "ccccc\n")  // 6 bytes
	d := getDiff(t, f, func(c *config.Config) { c.Diff.MaxFileBytes = 5 }, provider.DiffOptions{})
	if len(d.Files) != 1 || d.Files[0].Path != "exact.txt" {
		t.Fatalf("files = %+v", view(d))
	}
	want := []provider.SkippedFile{
		{Path: "baseover.txt", Reason: provider.SkipSizeLimit},
		{Path: "headover.txt", Reason: provider.SkipSizeLimit},
		{Path: "newover.txt", Reason: provider.SkipSizeLimit},
	}
	if !reflect.DeepEqual(d.Skipped, want) {
		t.Fatalf("skipped = %+v", d.Skipped)
	}
}

func TestBinaryDetection(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	f.handlePaged(prAPI+"/changes", nil, []any{
		change("MODIFY", "head_nul.bin", ""), change("MODIFY", "base_nul.bin", ""),
		change("MODIFY", "late_nul.txt", ""), change("ADD", "edge_nul.bin", ""), change("ADD", "ok.txt", ""),
	})
	f.setRaw("mergesha", "head_nul.bin", "text\n")
	f.setRaw("headsha", "head_nul.bin", "te\x00xt\n")
	f.setRaw("mergesha", "base_nul.bin", "te\x00xt\n")
	f.setRaw("headsha", "base_nul.bin", "text\n")
	// A NUL after the first 8000 bytes is not considered.
	late := strings.Repeat("a", 8000) + "\x00\n"
	f.setRaw("mergesha", "late_nul.txt", "x\n")
	f.setRaw("headsha", "late_nul.txt", late)
	// A NUL at byte index 7999 is the last byte scanned.
	f.setRaw("headsha", "edge_nul.bin", strings.Repeat("a", 7999)+"\x00\n")
	f.setRaw("headsha", "ok.txt", "fine\n")
	d := getDiff(t, f, nil, provider.DiffOptions{})
	var files []string
	for _, fp := range d.Files {
		files = append(files, fp.Path)
	}
	if !reflect.DeepEqual(files, []string{"late_nul.txt", "ok.txt"}) {
		t.Fatalf("files = %v", files)
	}
	want := []provider.SkippedFile{
		{Path: "head_nul.bin", Reason: provider.SkipBinary},
		{Path: "base_nul.bin", Reason: provider.SkipBinary},
		{Path: "edge_nul.bin", Reason: provider.SkipBinary},
	}
	if !reflect.DeepEqual(d.Skipped, want) {
		t.Fatalf("skipped = %+v", d.Skipped)
	}
}

func TestIdenticalAndEmptyContentsAreDroppedSilently(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	f.handlePaged(prAPI+"/changes", nil, []any{
		change("MODIFY", "same.txt", ""),    // identical
		change("MODIFY", "newline.txt", ""), // identical after trailing-newline normalization
		change("ADD", "empty.txt", ""),      // new empty file: both sides empty
		change("MODIFY", "real.txt", ""),
	})
	f.setRaw("mergesha", "same.txt", "a\nb\n")
	f.setRaw("headsha", "same.txt", "a\nb\n")
	f.setRaw("mergesha", "newline.txt", "a\nb")
	f.setRaw("headsha", "newline.txt", "a\nb\n")
	f.setRaw("headsha", "empty.txt", "")
	f.setRaw("mergesha", "real.txt", "a\n")
	f.setRaw("headsha", "real.txt", "b\n")
	d := getDiff(t, f, nil, provider.DiffOptions{})
	if len(d.Files) != 1 || d.Files[0].Path != "real.txt" {
		t.Fatalf("files = %+v", view(d))
	}
	if len(d.Skipped) != 0 {
		t.Fatalf("a dropped file must not be a skip: %+v", d.Skipped)
	}
}

func TestFetchErrorIsSkipFetchFailed(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		f := newFake(t, "")
		f.standard()
		f.setRawStatus("headsha", "src/app.go", status, "nope "+testMarker+" "+testToken)
		d := getDiff(t, f, nil, provider.DiffOptions{})
		var reason string
		for _, s := range d.Skipped {
			if s.Path == "src/app.go" {
				reason = s.Reason
			}
		}
		if reason != provider.SkipFetchFailed {
			t.Errorf("status %d: reason = %q, skipped = %+v", status, reason, d.Skipped)
		}
		for _, fp := range d.Files {
			if fp.Path == "src/app.go" {
				t.Errorf("status %d: failed file is in Files", status)
			}
		}
		if len(d.Files) != 3 {
			t.Errorf("status %d: other files must survive, got %d", status, len(d.Files))
		}
	}
	// A failing base side also skips (a 404 is NOT treated as empty content).
	f := newFake(t, "")
	f.standard()
	f.setRawStatus("mergesha", "src/app.go", 404, "")
	d := getDiff(t, f, nil, provider.DiffOptions{})
	if len(d.Skipped) != 1 || d.Skipped[0] != (provider.SkippedFile{Path: "src/app.go", Reason: provider.SkipFetchFailed}) {
		t.Fatalf("skipped = %+v", d.Skipped)
	}
}

func TestRawPathTraversalMakesNoRequest(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	f.handlePaged(prAPI+"/changes", nil, []any{
		change("MODIFY", "a/../etc/passwd", ""), change("ADD", "../x", ""), change("ADD", "a//b", ""),
		change("ADD", "./y", ""), change("MOVE", "ok.go", "../old.go"), change("ADD", "", ""),
		change("DELETE", "dir/..", ""), change("ADD", "good.go", ""),
	})
	f.setRaw("headsha", "good.go", "x\n")
	d := getDiff(t, f, nil, provider.DiffOptions{})
	if len(d.Files) != 1 || d.Files[0].Path != "good.go" {
		t.Fatalf("files = %+v", view(d))
	}
	if len(d.Skipped) != 7 {
		t.Fatalf("skipped = %+v", d.Skipped)
	}
	for _, s := range d.Skipped {
		if s.Reason != provider.SkipFetchFailed {
			t.Errorf("skip %+v: want fetch_failed", s)
		}
	}
	if n := len(f.rawRequests()); n != 1 {
		t.Fatalf("raw requests = %d, want only the one for good.go", n)
	}
}

func TestRawPathEscaping(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	f.handlePaged(prAPI+"/changes", nil, []any{change("ADD", "dir with space/a#b?c%d.go", "")})
	f.setRaw("head+sha/x", "dir with space/a#b?c%d.go", "x\n")
	f.handleJSON("GET", prAPI, map[string]any{
		"title": "t", "fromRef": map[string]any{"displayId": "f", "latestCommit": "head+sha/x"},
		"toRef": map[string]any{"displayId": "main", "latestCommit": "targetsha"},
	})
	d := getDiff(t, f, nil, provider.DiffOptions{})
	if len(d.Files) != 1 {
		t.Fatalf("files = %+v skipped %+v", view(d), d.Skipped)
	}
	raws := f.rawRequests()
	if len(raws) != 1 || raws[0].Path != v1Repo+"/raw/dir%20with%20space/a%23b%3Fc%25d.go" || raws[0].Query != "at=head%2Bsha%2Fx" {
		t.Fatalf("raw request = %+v", raws)
	}
}

func TestChangesPagination(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	all := sampleChanges()
	f.handlePaged(prAPI+"/changes", nil, all[:1], all[1:3], all[3:])
	d := getDiff(t, f, nil, provider.DiffOptions{})
	var got []string
	for _, fp := range d.Files {
		got = append(got, fp.Path)
	}
	if !reflect.DeepEqual(got, []string{"src/renamed.go", "src/app.go", "old/gone.go", "src/new.go"}) {
		t.Fatalf("files = %v", got)
	}
	var starts []string
	for _, r := range f.requests() {
		if r.Path == prAPI+"/changes" {
			starts = append(starts, r.Query)
		}
	}
	if !reflect.DeepEqual(starts, []string{"start=0&limit=100", "start=1&limit=100", "start=3&limit=100"}) {
		t.Fatalf("changes requests = %v", starts)
	}
}

func TestBaseStrategyReported(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	pr, d := getPRAndDiff(t, f, nil, provider.DiffOptions{})
	if pr.BaseStrategy != provider.BaseBBSMergeBaseEP || d.BaseStrategy != provider.BaseBBSMergeBaseEP {
		t.Fatalf("strategy: PullRequest %q, Diff %q", pr.BaseStrategy, d.BaseStrategy)
	}
	f2 := newFake(t, "")
	f2.ancestorHistory()
	f2.handlePaged(prAPI+"/changes", nil, []any{})
	pr, d = getPRAndDiff(t, f2, nil, provider.DiffOptions{})
	if pr.BaseStrategy != provider.BaseBBSAncestorWalk || d.BaseStrategy != provider.BaseBBSAncestorWalk {
		t.Fatalf("strategy: PullRequest %q, Diff %q", pr.BaseStrategy, d.BaseStrategy)
	}
}

func TestPersonalRepositoryNamespace(t *testing.T) {
	f := newFake(t, "/bitbucket")
	pers := "/rest/api/1.0/projects/~jdoe/repos/demo"
	persLatest := "/rest/api/latest/projects/~jdoe/repos/demo"
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	f.handleJSON("GET", pers+"/pull-requests/7", prJSON())
	f.handleJSON("GET", persLatest+"/pull-requests/7/merge-base", map[string]any{"id": "mergesha"})
	f.handlePaged(pers+"/pull-requests/7/changes", nil, []any{change("MODIFY", "a.txt", "")})
	f.setRaw("mergesha", "a.txt", "1\n")
	f.setRaw("headsha", "a.txt", "2\n")
	f.handlePaged(pers+"/pull-requests/7/commits", nil, []any{commitJSON("c1", "msg")})
	f.handle("POST", pers+"/pull-requests/7/comments", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"id": 5}`)
	})
	r := provider.PRRef{Kind: provider.KindBitbucketServer, Namespace: "~jdoe", Repo: "demo", Number: 7}
	p := f.provider(t, nil)
	ctx := context.Background()
	pr, err := p.GetPullRequest(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := p.GetDiff(ctx, r, pr, provider.DiffOptions{}); err != nil || len(d.Files) != 1 {
		t.Fatalf("diff %+v err %v", d, err)
	}
	if m, err := p.GetCommitMessages(ctx, r); err != nil || len(m) != 1 {
		t.Fatalf("messages %v err %v", m, err)
	}
	if _, err := p.PostComment(ctx, r, "x"); err != nil {
		t.Fatal(err)
	}
}

func TestCommitsOrderReversalAcrossPages(t *testing.T) {
	f := newFake(t, "")
	f.standard()
	f.handlePaged(prAPI+"/commits", nil,
		[]any{commitJSON("c4", "fourth", "c3"), commitJSON("c3", "third\n\nbody", "c2")},
		[]any{commitJSON("c2", "second", "c1")},
		[]any{commitJSON("c1", "first", "c0")})
	got, err := f.provider(t, nil).GetCommitMessages(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"first", "second", "third\n\nbody", "fourth"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("messages = %q, want %q", got, want)
	}
}

func TestPostComment(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.standard()
	var ct string
	f.handle("POST", prAPI+"/comments", func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id": 4242, "version": 0, "text": "echo"}`)
	})
	pers := "/rest/api/1.0/projects/~jdoe/repos/demo/pull-requests/7/comments"
	f.handle("POST", pers, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"id": 5}`) })
	noID := "/rest/api/1.0/projects/PROJ/repos/demo/pull-requests/8/comments"
	f.handle("POST", noID, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{}`) })
	p := f.provider(t, nil)
	ctx := context.Background()
	// No GetPullRequest has run: the URL depends only on config and ref.
	c, err := p.PostComment(ctx, ref(), "hello \"world\"\n## h")
	if err != nil {
		t.Fatal(err)
	}
	base := f.baseURL()
	if c.ID != "4242" || c.URL != base+"/projects/PROJ/repos/demo/pull-requests/7/overview?commentId=4242" {
		t.Fatalf("comment = %+v", c)
	}
	var posts []recorded
	for _, r := range f.requests() {
		if r.Method == "POST" {
			posts = append(posts, r)
		}
	}
	if len(posts) != 1 || posts[0].Path != prAPI+"/comments" || posts[0].Body != `{"text":"hello \"world\"\n## h"}` {
		t.Fatalf("posts = %+v", posts)
	}
	if ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
	// Personal repository.
	r := ref()
	r.Namespace = "~jdoe"
	c, err = p.PostComment(ctx, r, "x")
	if err != nil || c.ID != "5" || c.URL != base+"/users/jdoe/repos/demo/pull-requests/7/overview?commentId=5" {
		t.Fatalf("personal comment = %+v err %v", c, err)
	}
	// Missing id: no commentId parameter, and no error.
	r = ref()
	r.Number = 8
	c, err = p.PostComment(ctx, r, "x")
	if err != nil || c.ID != "" || c.URL != base+"/projects/PROJ/repos/demo/pull-requests/8/overview" {
		t.Fatalf("missing-id comment = %+v err %v", c, err)
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
		f.standard()
		f.handleStatus("GET", prAPI, c.status)
		f.handleStatus("POST", prAPI+"/comments", c.status)
		f.handleStatus("GET", prAPI+"/changes", c.status)
		f.handleStatus("GET", prAPI+"/commits", c.status)
		p := f.provider(t, nil)
		ctx := context.Background()
		_, err1 := p.GetPullRequest(ctx, ref())
		_, err2 := p.PostComment(ctx, ref(), "x")
		_, err3 := p.GetCommitMessages(ctx, ref())
		// GetDiff with a PR value of ours reaches the changes endpoint.
		_, err4 := p.GetDiff(ctx, ref(), stdPR(), provider.DiffOptions{})
		for i, e := range []error{err1, err2, err3, err4} {
			if !errors.Is(e, c.want) {
				t.Errorf("status %d op %d: err = %v, want class %s", c.status, i, e, c.want.Class)
				continue
			}
			if s := e.Error(); strings.Contains(s, testMarker) || strings.Contains(s, testToken) {
				t.Errorf("status %d op %d: error leaks: %s", c.status, i, s)
			}
		}
	}
	// A failing changes endpoint with a known PR.
	f := newFake(t, "")
	f.standard()
	f.handleStatus("GET", prAPI+"/changes", 500)
	p := f.provider(t, nil)
	pr, err := p.GetPullRequest(context.Background(), ref())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.GetDiff(context.Background(), ref(), pr, provider.DiffOptions{}); !errors.Is(err, provider.ErrUpstream) {
		t.Fatalf("changes 500: %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	f := newFake(t, "")
	f.standard()
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
		{Namespace: "..", Repo: "demo", Number: 7}, {Namespace: "PROJ", Repo: "", Number: 7},
		{Namespace: "PROJ", Repo: "demo"}, {Namespace: "~", Repo: "demo", Number: 7}, {Namespace: "", Repo: "demo", Number: 7},
	} {
		ctx := context.Background()
		_, e1 := p.GetPullRequest(ctx, r)
		_, e2 := p.GetCommitMessages(ctx, r)
		_, e3 := p.GetDiff(ctx, r, nil, provider.DiffOptions{})
		_, e4 := p.PostComment(ctx, r, "x")
		for _, err := range []error{e1, e2, e3, e4} {
			if !errors.Is(err, provider.ErrProtocol) {
				t.Errorf("%+v: err = %v", r, err)
			}
		}
	}
	if len(f.requests()) != 0 {
		t.Fatal("requests were made")
	}
}

func TestFileLineURL(t *testing.T) {
	cfg := config.Defaults()
	cfg.BitbucketServer.BaseURL = "https://bitbucket.example.com/bitbucket/"
	p, err := bitbucketserver.NewFactory().New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := &provider.PullRequest{HeadSHA: "abc123"}
	got := p.FileLineURL(ref(), pr, "dir with space/a#b.go", 12)
	if want := "https://bitbucket.example.com/bitbucket/projects/PROJ/repos/demo/pull-requests/7/diff#dir%20with%20space/a%23b.go?t=12"; got != want {
		t.Fatalf("got %s", got)
	}
	personal := ref()
	personal.Namespace = "~jdoe"
	got = p.FileLineURL(personal, pr, "src/app.go", 3)
	if want := "https://bitbucket.example.com/bitbucket/users/jdoe/repos/demo/pull-requests/7/diff#src/app.go?t=3"; got != want {
		t.Fatalf("personal: got %s", got)
	}
	if got := p.FileLineURL(ref(), pr, "src/app.go", 0); strings.Contains(got, "?t=") {
		t.Fatalf("line 0 must not add a line anchor: %s", got)
	}
	if p.FileLineURL(ref(), nil, "x", 1) != "" {
		t.Fatal("nil PR must give empty URL")
	}
}

func TestResolverIntegration(t *testing.T) {
	f := newFake(t, "/bitbucket")
	f.standard()
	pers := "/rest/api/1.0/projects/~jdoe/repos/demo"
	f.handleJSON("GET", pers+"/pull-requests/7", prJSON())
	f.handleJSON("GET", "/rest/api/latest/projects/~jdoe/repos/demo/pull-requests/7/merge-base", map[string]any{"id": "mergesha"})
	r := provider.NewResolver(f.config(), nil, bitbucketserver.NewFactory())
	for _, tc := range []struct{ raw, ns string }{
		{f.baseURL() + "/projects/PROJ/repos/demo/pull-requests/7", "PROJ"},
		{f.baseURL() + "/projects/PROJ/repos/demo/pull-requests/7/overview?x=1#frag", "PROJ"},
		{f.baseURL() + "/users/jdoe/repos/demo/pull-requests/7/diff", "~jdoe"},
	} {
		pref, p, err := r.Resolve(tc.raw)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if pref.Kind != provider.KindBitbucketServer || pref.Namespace != tc.ns || pref.Repo != "demo" || pref.Number != 7 {
			t.Fatalf("ref = %+v", pref)
		}
		if _, err := p.GetPullRequest(context.Background(), pref); err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
	}
	if _, _, err := r.Resolve(f.baseURL() + "/projects/PROJ/repos/demo/commits/7"); !errors.Is(err, provider.ErrURLMalformed) {
		t.Fatalf("non-PR URL: %v", err)
	}
	before := len(f.requests())
	for _, raw := range []string{
		f.srv.URL + "/bitbucketx/projects/PROJ/repos/demo/pull-requests/7", // sibling prefix
		f.srv.URL + "/projects/PROJ/repos/demo/pull-requests/7",            // no context path
		"https://bitbucket.example.com.evil.example/bitbucket/projects/PROJ/repos/demo/pull-requests/7",
	} {
		if _, _, err := r.Resolve(raw); !errors.Is(err, provider.ErrURLNotConfigured) {
			t.Errorf("%s: err = %v", raw, err)
		}
	}
	if len(f.requests()) != before {
		t.Fatal("resolver performed I/O")
	}
}

// [canary] X-8: at debug level neither the token nor any response-body
// marker is logged, and neither the token nor the marker is in an error. The
// full flow is run on both base strategies.
func TestDebugLogsAndErrorsDoNotLeak(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		f := newFake(t, "/bitbucket")
		f.standard()
		if !ancestor {
			f.handlePaged(prAPI+"/commits", nil, []any{commitJSON("c2", "second "+testMarker, "c1")}, []any{commitJSON("c1", "first", "c0")})
		} else {
			f.ancestorHistory()
			f.setRaw("T1", "src/app.go", "package main\nvar a = 1\n")
			f.setRaw("T1", "old/gone.go", "package old\n")
			f.setRaw("T1", "src/old_name.go", "package main\n")
		}
		f.handle("POST", prAPI+"/comments", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"id": 9, "text": "`+testMarker+`"}`)
		})
		// A raw 500 whose body echoes both the marker and the token.
		f.setRawStatus("headsha", "src/new.go", 500, "oops "+testMarker+" "+testToken)
		// Error paths on other PRs.
		base := v1Repo + "/pull-requests/"
		f.handleStatus("GET", base+"8", http.StatusNotFound)
		f.handleStatus("GET", base+"9", http.StatusUnauthorized)
		f.handle("GET", base+"10", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"title": not json `+testMarker+testToken)
		})
		f.handleStatus("GET", base+"11", http.StatusInternalServerError)

		var logs bytes.Buffer
		p, err := bitbucketserver.NewFactory().New(f.config(), slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		pr, err := p.GetPullRequest(ctx, ref())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(pr.Description, testMarker) {
			t.Fatal("marker expected in returned data")
		}
		if msgs, err := p.GetCommitMessages(ctx, ref()); err != nil || len(msgs) != 2 && !ancestor || len(msgs) != 3 && ancestor {
			t.Fatalf("messages %v err %v", msgs, err)
		}
		d, err := p.GetDiff(ctx, ref(), pr, provider.DiffOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var sawFailed bool
		for _, s := range d.Skipped {
			if s.Reason == provider.SkipFetchFailed {
				sawFailed = true
			}
		}
		if !sawFailed {
			t.Fatal("expected a fetch_failed skip")
		}
		if _, err := p.PostComment(ctx, ref(), "body "+testMarker); err != nil {
			t.Fatal(err)
		}
		var errs []error
		for _, n := range []int64{8, 9, 10, 11} {
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
}
