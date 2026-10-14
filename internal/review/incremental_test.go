package review

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/review/incremental"
)

// The incremental review (X-36, WP-7d) on the fake provider. The pull
// request changes two files in two directories; the last review recorded
// shaOld, at which incPathSame already had its head content: since that
// review only incPathEdited changed.
const (
	incPathSame   = "src/same.go"
	incPathEdited = "pkg/edited.go"
	incMarkSame   = "INC-SAME-3e1a"
	incMarkEdited = "INC-EDITED-7c2b"
)

// incFile is a modified file whose third line changed from base to head
// (both "package x", a blank line and one more line).
func incFile(path, base, head string) provider.FilePatch {
	third := func(s string) string { return strings.SplitN(s, "\n", 4)[2] }
	return provider.FilePatch{Path: path, Type: provider.ChangeModified, Additions: 1, Deletions: 1,
		Patch:       "@@ -1,3 +1,3 @@\n package x\n \n-" + third(base) + "\n+" + third(head) + "\n",
		BaseContent: &base, HeadContent: &head, BaseStatus: provider.ContentFull, HeadStatus: provider.ContentFull}
}

const (
	incSameBase   = "package x\n\nfunc Old() {}\n"
	incSameHead   = "package x\n\nfunc Same() {} // " + incMarkSame + "\n"
	incEditedBase = "package x\n\nfunc Before() {}\n"
	incEditedLast = "package x\n\nfunc Between() {}\n"
	incEditedHead = "package x\n\nfunc Edited() {} // " + incMarkEdited + "\n"
)

// Findings on both files, on their changed line.
var (
	onSame   = testFinding{incPathSame, "Same finding", "On the unchanged file.", 3, 3}
	onEdited = testFinding{incPathEdited, "Edited finding", "On the changed file.", 3, 3}
)

// incHarness serves the two-file pull request at head shaHead with review-
// mcp's overview of the last review, which recorded last ("" plants none).
func incHarness(last string) *harness {
	h := newHarness(answerWith(onSame, onEdited))
	h.prov.pr.HeadSHA = shaHead
	h.prov.files = []provider.FilePatch{
		incFile(incPathSame, incSameBase, incSameHead),
		incFile(incPathEdited, incEditedBase, incEditedHead),
	}
	h.prov.revs = map[string]map[string]string{
		"base0": {incPathSame: incSameBase, incPathEdited: incEditedBase, "src/other.go": "package x\n", "pkg/other.go": "package x\n"},
		shaOld:  {incPathSame: incSameHead, incPathEdited: incEditedLast, "src/other.go": "package x\n", "pkg/other.go": "package x\n"},
	}
	if last != "" {
		h.prov.addComment(h.prov.me, "Earlier overview.\n\n"+reviewedLineOf(last)+"\n"+OverviewMarker)
	}
	return h
}

func incArgs(publish bool) Args {
	on := true
	return Args{PRURL: testPRURL, Publish: publish, Incremental: &on}
}

func runOK(t *testing.T, h *harness, args Args) *Result {
	t.Helper()
	res, err := Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// readsAt returns the provider reads made at revision rev.
func (f *fakeProvider) readsAt(rev string) []string {
	var out []string
	for _, r := range f.reads {
		if strings.HasSuffix(r, "@"+rev) {
			out = append(out, r)
		}
	}
	return out
}

// TestIncrementalOneFileChanged: a second review after a head change that
// touched one of two files sends only that file to the model, with the
// incremental sentence, posts only for it, keeps the other one out of the
// coverage's counts (not partial) and records the new head with the commit
// it continues from. The file tree lists the changed file's directory only.
func TestIncrementalOneFileChanged(t *testing.T) {
	h := incHarness(shaOld)
	res := runOK(t, h, incArgs(true))

	if len(h.llm.calls) != 1 {
		t.Fatalf("%d model calls, want 1", len(h.llm.calls))
	}
	call := h.llm.calls[0]
	if !strings.Contains(call.user, incMarkEdited) || strings.Contains(call.user, incMarkSame) {
		t.Errorf("the model must see %s only; user prompt:\n%s", incPathEdited, call.user)
	}
	if !strings.Contains(call.system, "\n- "+IncrementalSentence+"\n") {
		t.Errorf("the system prompt lacks the incremental sentence:\n%s", call.system)
	}
	want := Incremental{Mode: IncrementalChanged, Since: shaOld, Strategy: incremental.StrategyAPI,
		ChangedFiles: 1, UnchangedFiles: 1}
	if res.Incremental == nil || *res.Incremental != want {
		t.Errorf("incremental = %+v, want %+v", res.Incremental, want)
	}
	if res.Reviewed != (ReviewedCommit{SHA: shaHead, Since: shaOld}) {
		t.Errorf("reviewed = %+v", res.Reviewed)
	}

	c := res.Coverage
	if c.Partial || c.ReviewedFiles != 1 || c.TotalFiles != 1 || c.NotReviewedFiles != 0 || c.UnchangedFiles != 1 {
		t.Errorf("coverage counts: partial %v, reviewed %d, total %d, not reviewed %d, unchanged %d",
			c.Partial, c.ReviewedFiles, c.TotalFiles, c.NotReviewedFiles, c.UnchangedFiles)
	}
	if !slices.Equal(c.Included, []string{incPathEdited}) ||
		!slices.Contains(c.Skipped, SkippedFile{Path: incPathSame, Reason: llmrun.SkipUnchangedSinceReview}) {
		t.Errorf("coverage lists: included %v, skipped %v", c.Included, c.Skipped)
	}
	if c.FileTree == nil || c.FileTree.Directories != 1 {
		t.Errorf("file tree = %+v, want the directory of the changed file only", c.FileTree)
	}
	tree := call.user[strings.Index(call.user, "Files in the directories this change touches"):]
	if !strings.Contains(tree, "pkg/") || strings.Contains(tree, "src/") {
		t.Errorf("the tree block is not the changed file's directory:\n%s", tree)
	}

	// The finding on the unchanged file has no line in the reduced diff:
	// only the changed file gets an inline comment. The earlier overview
	// is edited in place.
	if len(h.prov.inline) != 1 || len(h.prov.inline[0]) != 1 || h.prov.inline[0][0].Path != incPathEdited {
		t.Fatalf("inline posts %+v", h.prov.inline)
	}
	if res.Publish == nil || !res.Publish.Updated || *res.Publish.Inline != (InlineSummary{Posted: 1, Unanchorable: 1}) {
		t.Errorf("publish = %+v", res.Publish)
	}
	if got := reviewedSHAOf(h.prov.comments[0].body); got != shaHead {
		t.Errorf("the overview records %q, want the head", got)
	}
	// Nothing was read at the last commit beyond the probe and the two
	// files of the comparison.
	if got := h.prov.readsAt(shaOld); len(got) != 3 {
		t.Errorf("reads at the last commit: %v", got)
	}
}

// TestIncrementalSameHead: the last review recorded the current head:
// no model call, the fixed note, nothing posted or edited although publish
// is set, and nothing read for the prompt.
func TestIncrementalSameHead(t *testing.T) {
	h := incHarness(shaHead)
	res := runOK(t, h, incArgs(true))
	if len(h.llm.calls) != 0 || res.Metadata.LLMCalls != 0 {
		t.Fatalf("the model was called %d times", len(h.llm.calls))
	}
	if len(h.prov.seq) != 0 || res.Publish != nil {
		t.Errorf("writes %v, publish %+v", h.prov.seq, res.Publish)
	}
	if len(h.prov.reads) != 0 {
		t.Errorf("reads %v, want none", h.prov.reads)
	}
	want := Incremental{Mode: IncrementalUnchanged, Since: shaHead, UnchangedFiles: 2}
	if res.Incremental == nil || *res.Incremental != want {
		t.Errorf("incremental = %+v, want %+v", res.Incremental, want)
	}
	note := "Nothing changed since the last review (commit `bbbbbbb`); nothing was reviewed."
	if !slices.Contains(res.Notes, note) {
		t.Errorf("notes = %q", res.Notes)
	}
	c := res.Coverage
	if c.Partial || c.UnchangedFiles != 2 || c.TotalFiles != 0 || len(res.EnabledFields) != 0 ||
		res.Review == nil || len(res.Review.KeyIssuesToReview) != 0 || c.Guidelines == nil || c.FileTree == nil {
		t.Errorf("coverage %+v, enabled fields %v, review %+v", c, res.EnabledFields, res.Review)
	}
}

// TestIncrementalNoFileChanged: every file has the content it had at the
// last review: no model call, the fixed note, nothing posted.
func TestIncrementalNoFileChanged(t *testing.T) {
	h := incHarness(shaOld)
	h.prov.revs[shaOld][incPathEdited] = incEditedHead
	res := runOK(t, h, incArgs(true))
	if len(h.llm.calls) != 0 || len(h.prov.seq) != 0 || res.Publish != nil {
		t.Fatalf("model calls %d, writes %v, publish %+v", len(h.llm.calls), h.prov.seq, res.Publish)
	}
	want := Incremental{Mode: IncrementalUnchanged, Since: shaOld, Strategy: incremental.StrategyAPI, UnchangedFiles: 2}
	if res.Incremental == nil || *res.Incremental != want {
		t.Errorf("incremental = %+v, want %+v", res.Incremental, want)
	}
	note := "No file of the pull request changed since the last review (commit `aaaaaaa`); nothing was reviewed."
	if !slices.Contains(res.Notes, note) {
		t.Errorf("notes = %q", res.Notes)
	}
}

// checkFull checks a full review of both files with the notes wantNotes
// among the result's and none of the incremental notes otherwise.
func checkFull(t *testing.T, h *harness, res *Result, wantNotes ...string) {
	t.Helper()
	if len(h.llm.calls) != 1 || !strings.Contains(h.llm.calls[0].user, incMarkSame) ||
		!strings.Contains(h.llm.calls[0].user, incMarkEdited) {
		t.Fatalf("a full review sends both files in one call (%d calls)", len(h.llm.calls))
	}
	if strings.Contains(h.llm.calls[0].system, IncrementalSentence) {
		t.Error("a full review carries the incremental sentence")
	}
	if want := (Incremental{Mode: IncrementalFull, ChangedFiles: 2}); res.Incremental == nil || *res.Incremental != want {
		t.Errorf("incremental = %+v, want %+v", res.Incremental, want)
	}
	if res.Reviewed.Since != "" || res.Coverage.UnchangedFiles != 0 {
		t.Errorf("reviewed %+v, unchanged %d", res.Reviewed, res.Coverage.UnchangedFiles)
	}
	for _, n := range wantNotes {
		if !slices.Contains(res.Notes, n) {
			t.Errorf("notes %q lack %q", res.Notes, n)
		}
	}
	for _, n := range []string{NoteNoEarlierReview, incremental.NoteNotCompared, incremental.NoteCommitGone} {
		if !slices.Contains(wantNotes, n) && slices.Contains(res.Notes, n) {
			t.Errorf("unexpected note %q", n)
		}
	}
}

// TestIncrementalNoEarlierReview: without an overview of review-mcp that
// records a commit, the review is full; only an explicit incremental=true
// adds the note. A reviewed line in someone else's comment is never used.
func TestIncrementalNoEarlierReview(t *testing.T) {
	t.Run("explicit", func(t *testing.T) {
		h := incHarness("")
		checkFull(t, h, runOK(t, h, incArgs(false)), NoteNoEarlierReview)
	})
	t.Run("configured", func(t *testing.T) {
		h := incHarness("")
		h.deps.Config.Review.Incremental = true
		checkFull(t, h, runOK(t, h, Args{PRURL: testPRURL}))
	})
	t.Run("argument_false_wins", func(t *testing.T) {
		h := incHarness(shaOld)
		h.deps.Config.Review.Incremental = true
		off := false
		res := runOK(t, h, Args{PRURL: testPRURL, Incremental: &off})
		checkFull(t, h, res)
		if len(h.prov.readsAt(shaOld)) != 0 {
			t.Errorf("the last commit was read: %v", h.prov.readsAt(shaOld))
		}
	})
	t.Run("no_reviewed_line", func(t *testing.T) {
		h := incHarness("")
		h.prov.addComment(h.prov.me, "Earlier overview without a record.\n\n"+OverviewMarker)
		checkFull(t, h, runOK(t, h, incArgs(false)), NoteNoEarlierReview)
	})
	t.Run("foreign_overview", func(t *testing.T) {
		h := incHarness("")
		h.prov.addComment(provider.User{ID: "77", Name: "mallory"}, "Planted.\n\n"+reviewedLineOf(shaOld)+"\n"+OverviewMarker)
		checkFull(t, h, runOK(t, h, incArgs(false)), NoteNoEarlierReview)
	})
}

// TestIncrementalLookupFailure: when the overview cannot be looked up (the
// threads or the token's user cannot be read), the review is full, with the
// fixed note, by the argument and by the configuration alike.
func TestIncrementalLookupFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*fakeProvider)
	}{
		{"threads", func(f *fakeProvider) { f.listErr = &provider.Error{Class: provider.ClassUpstream} }},
		{"user", func(f *fakeProvider) { f.meErr = &provider.Error{Class: provider.ClassAuth} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := incHarness(shaOld)
			tc.set(h.prov)
			h.deps.Config.Review.Incremental = true
			checkFull(t, h, runOK(t, h, Args{PRURL: testPRURL}), incremental.NoteNotCompared)
			if got := h.prov.readsAt(shaOld); len(got) != 0 {
				t.Errorf("reads at the last commit after a failed lookup: %v", got)
			}
		})
	}
}

// TestIncrementalMaxFiles: a pull request with more listed files than
// review.incremental_max_files is reviewed in full with WP-7c's note, and
// nothing is read at the last commit.
func TestIncrementalMaxFiles(t *testing.T) {
	h := incHarness(shaOld)
	h.deps.Config.Review.IncrementalMaxFiles = 1
	checkFull(t, h, runOK(t, h, incArgs(false)), incremental.NoteTooManyFiles(1))
	if got := h.prov.readsAt(shaOld); len(got) != 0 {
		t.Errorf("reads at the last commit: %v", got)
	}
}

// TestIncrementalCommitGone: a last commit the host no longer knows is a
// full review with WP-7c's note.
func TestIncrementalCommitGone(t *testing.T) {
	h := incHarness(shaOld)
	delete(h.prov.revs, shaOld)
	checkFull(t, h, runOK(t, h, incArgs(false)), incremental.NoteCommitGone)
}

// TestIncrementalReadErrorIsChanged: a read error at the last commit makes
// the file changed: never skipped on a doubt.
func TestIncrementalReadErrorIsChanged(t *testing.T) {
	h := incHarness(shaOld)
	h.prov.revs[shaOld][incPathEdited] = incEditedHead // nothing changed by content
	p := &failingReads{fakeProvider: h.prov, failPath: incPathSame, rev: shaOld}
	h.deps.Resolver = providerResolver{p}
	res := runOK(t, h, incArgs(false))
	if res.Incremental == nil || res.Incremental.Mode != IncrementalChanged || res.Incremental.ChangedFiles != 1 ||
		len(h.llm.calls) != 1 || !strings.Contains(h.llm.calls[0].user, incMarkSame) ||
		strings.Contains(h.llm.calls[0].user, incMarkEdited) {
		t.Errorf("incremental = %+v, calls %d", res.Incremental, len(h.llm.calls))
	}
}

// failingReads fails GetFileAt of one path at one revision.
type failingReads struct {
	*fakeProvider
	failPath, rev string
}

func (f *failingReads) GetFileAt(ctx context.Context, ref provider.PRRef, path, rev string) ([]byte, error) {
	if path == f.failPath && rev == f.rev {
		return nil, &provider.Error{Class: provider.ClassUpstream}
	}
	return f.fakeProvider.GetFileAt(ctx, ref, path, rev)
}

type providerResolver struct{ p provider.Provider }

func (r providerResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

// TestIncrementalFocus: a focused run may be incremental (it never
// publishes).
func TestIncrementalFocus(t *testing.T) {
	h := incHarness(shaOld)
	args := incArgs(false)
	args.Focus = "security"
	res := runOK(t, h, args)
	if res.Incremental == nil || res.Incremental.Mode != IncrementalChanged || len(h.llm.calls) != 1 ||
		strings.Contains(h.llm.calls[0].user, incMarkSame) || res.Focus != FocusSecurity || res.Publish != nil {
		t.Errorf("incremental %+v, calls %d, focus %q, publish %+v", res.Incremental, len(h.llm.calls), res.Focus, res.Publish)
	}
}

// TestIncrementalIgnoredByFindings: a publish of supplied findings never
// compares (no model call, nothing read at the last commit), reports a
// full run and carries the last commit over (WP-7b).
func TestIncrementalIgnoredByFindings(t *testing.T) {
	h := incHarness(shaOld)
	h.deps.LLM = noCallLLM{t}
	args := incArgs(true)
	args.Findings = []SuppliedFinding{supplied(incPathSame, 3, 3, "Supplied", "A finding on the unchanged file.")}
	res := runOK(t, h, args)
	if want := (Incremental{Mode: IncrementalFull, ChangedFiles: 2}); res.Incremental == nil || *res.Incremental != want {
		t.Errorf("incremental = %+v, want %+v", res.Incremental, want)
	}
	if got := h.prov.readsAt(shaOld); len(got) != 0 {
		t.Errorf("reads at the last commit: %v", got)
	}
	if res.Publish == nil || res.Publish.Inline == nil || res.Publish.Inline.Posted != 1 {
		t.Errorf("publish = %+v", res.Publish)
	}
	if got := reviewedSHAOf(h.prov.comments[0].body); got != shaOld {
		t.Errorf("the overview records %q, want the carried-over %q", got, shaOld)
	}
}

// TestPartialReviewRecordsNoCommit (WP-7d DQ-3): a partial review records no
// commit, so that the next incremental run reviews in full instead of
// skipping files no model saw.
func TestPartialReviewRecordsNoCommit(t *testing.T) {
	h := incHarness("")
	h.prov.skipped = []provider.SkippedFile{{Path: "data/big.json", Reason: provider.SkipSizeLimit}}
	res := runOK(t, h, Args{PRURL: testPRURL, Publish: true})
	if !res.Coverage.Partial || res.Reviewed.SHA != "" {
		t.Fatalf("partial %v, reviewed %+v", res.Coverage.Partial, res.Reviewed)
	}
	if strings.Contains(h.prov.comments[0].body, "review-mcp:reviewed:") || !HasOverviewMarker(h.prov.comments[0].body) {
		t.Errorf("a partial review recorded a commit:\n%s", h.prov.comments[0].body)
	}
	// The next incremental run finds no record: full.
	h.llm.calls = nil
	checkFull(t, h, runOK(t, h, incArgs(false)), NoteNoEarlierReview)
}

// gitRepo is a repository-context backend that can also compare commits
// (repoctx.ChangeReader), like *gitctx.Runner.
type gitRepo struct {
	fakeRepo
	commits map[string]bool
	changed []string
	paths   []string
	asked   int
}

func (g *gitRepo) HasCommit(_ context.Context, _ gitctx.Checkout, rev string) bool {
	return g.commits[rev]
}

func (g *gitRepo) ChangedPaths(context.Context, gitctx.Checkout, string, string) ([]string, error) {
	g.asked++
	return g.changed, nil
}

func (g *gitRepo) ListPaths(context.Context, gitctx.Checkout, string) ([]string, error) {
	return g.paths, nil
}

// TestIncrementalGitStrategy: with repository context on and the head
// fetched by this run, the cache compares the commits (git:diff) and the
// provider reads nothing at the last commit; a use in the unchanged file is
// shown to the model, marked as not reviewed. Without a fetch (no symbol to
// look for) nothing is fetched for the detection and the provider compares.
func TestIncrementalGitStrategy(t *testing.T) {
	t.Run("fetched", func(t *testing.T) {
		h := incHarness(shaOld)
		h.deps.Config.Context.Repo.Enabled = true
		g := &gitRepo{commits: map[string]bool{shaOld: true, shaHead: true}, changed: []string{incPathEdited},
			paths: []string{incPathSame, incPathEdited}}
		g.hits = map[string][]gitctx.Hit{"Edited": {useHit("Edited", incPathSame, 9)}}
		h.deps.RepoContext = g
		res := runOK(t, h, incArgs(false))
		want := Incremental{Mode: IncrementalChanged, Since: shaOld, Strategy: incremental.StrategyGit,
			ChangedFiles: 1, UnchangedFiles: 1}
		if res.Incremental == nil || *res.Incremental != want {
			t.Errorf("incremental = %+v, want %+v", res.Incremental, want)
		}
		if g.ensures != 1 || g.asked != 1 || len(h.prov.readsAt(shaOld)) != 0 {
			t.Errorf("fetches %d, git comparisons %d, provider reads at the last commit %v", g.ensures, g.asked,
				h.prov.readsAt(shaOld))
		}
		user := h.llm.calls[0].user
		if !strings.Contains(user, "["+incPathSame+":9] uses Edited ("+repoctx.MarkNotReviewed+")") {
			t.Errorf("the use in the unchanged file is not shown as not reviewed:\n%s", user)
		}
		if q := g.queries[0]; slices.Contains(q.Exclude, incPathSame) || !slices.Contains(q.Exclude, incPathEdited) {
			t.Errorf("search excludes %v", q.Exclude)
		}
	})
	t.Run("not_fetched", func(t *testing.T) {
		h := incHarness(shaOld)
		h.deps.Config.Context.Repo.Enabled = true
		for i := range h.prov.files {
			// No definition line: nothing to search, so nothing is fetched.
			h.prov.files[i].Patch = "@@ -1,3 +1,3 @@\n package x\n \n-// a\n+// b " + incMarkEdited + "\n"
		}
		g := &gitRepo{commits: map[string]bool{shaOld: true, shaHead: true}}
		h.deps.RepoContext = g
		res := runOK(t, h, incArgs(false))
		if g.ensures != 0 || g.asked != 0 || res.Incremental == nil || res.Incremental.Strategy != incremental.StrategyAPI {
			t.Errorf("fetches %d, git comparisons %d, incremental %+v", g.ensures, g.asked, res.Incremental)
		}
	})
}
