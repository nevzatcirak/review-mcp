package repoctx

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

var update = flag.Bool("update", false, "rewrite the block golden")

const factor = 0.3

func sampleHits() []gitctx.Hit {
	return []gitctx.Hit{
		{Symbol: "Alpha", Path: "pkg/use.go", Line: 12, StartLine: 10, Snippet: "func caller() {\n\tAlpha(1)\n}"},
		{Symbol: "Alpha", Path: "docs/notes.md", Line: 3, StartLine: 3, Snippet: "Call `Alpha` first.\n```go\nAlpha()\n```"},
		{Symbol: "Beta", Path: "pkg/other.go", Line: 40, StartLine: 38, Snippet: "x := Beta()\r\n\x00y := 2\xff"},
	}
}

// TestRenderGolden: the block with its fixed header, the entries
// "[path:line] uses symbol", the snippets, and an adaptive fence that the
// snippet's own ``` cannot close. Run with -update after checking a change on
// purpose.
func TestRenderGolden(t *testing.T) {
	b := Render(sampleHits(), 10000, factor)
	if b.Entries != 3 || b.Omitted != 0 || b.Files != 3 {
		t.Fatalf("block = %+v", b)
	}
	path := filepath.Join("testdata", "block.golden")
	if *update {
		if err := os.WriteFile(path, []byte(b.Text), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // G304: test fixture path
	if err != nil {
		t.Fatalf("%v (run go test ./internal/repoctx -run TestRenderGolden -update)", err)
	}
	if b.Text != string(want) {
		t.Errorf("block differs from %s (-update after checking)\n--- got\n%s\n--- want\n%s", path, b.Text, want)
	}
	if !strings.HasPrefix(b.Text, Header+"\n````\n") {
		t.Errorf("the fence is not one backtick longer than the snippet's ``` run:\n%s", b.Text)
	}
	if strings.ContainsAny(b.Text, "\x00\r") || !strings.Contains(b.Text, "�") {
		t.Errorf("control characters, carriage returns or invalid UTF-8 survived:\n%q", b.Text)
	}
}

// TestRenderClipsByWholeEntries: a budget for fewer entries keeps the first
// ones whole (rank order) and counts the rest as omitted; never half an
// entry.
func TestRenderClipsByWholeEntries(t *testing.T) {
	hits := sampleHits()
	full := Render(hits, 10000, factor)
	two := Render(hits[:2], 10000, factor)
	max := tokens.Estimate(two.Text, factor)
	b := Render(hits, max, factor)
	if b.Entries != 2 || b.Omitted != 1 || b.Text != two.Text {
		t.Errorf("clipped to %d tokens: %+v, want the first two entries whole", max, b)
	}
	if tokens.Estimate(b.Text, factor) > max {
		t.Errorf("the block exceeds its budget")
	}
	if strings.Contains(b.Text, "pkg/other.go") {
		t.Errorf("the third entry is in a two-entry block")
	}
	if tokens.Estimate(full.Text, factor) <= max {
		t.Fatal("test setup: the full block fits the clipped budget")
	}
	// Not even the header and one entry fit: the block is empty and every
	// use is omitted.
	if b := Render(hits, 10, factor); b.Text != "" || b.Entries != 0 || b.Omitted != 3 {
		t.Errorf("tiny budget: %+v", b)
	}
	if b := Render(hits, 0, factor); b.Text != "" || b.Omitted != 3 {
		t.Errorf("zero budget: %+v", b)
	}
	if b := Render(nil, 100, factor); b.Text != "" || b.Omitted != 0 {
		t.Errorf("no hits: %+v", b)
	}
}

// TestPlaceDiffWins: the block gets at most the room the diff leaves; a
// block that would make the request-size guard trim the diff is dropped.
func TestPlaceDiffWins(t *testing.T) {
	hits := sampleHits()
	tried := 0
	ok := func(string) (bool, error) { tried++; return true, nil }

	p, err := Place(hits, 5000, 5000, factor, ok)
	if err != nil || p.Reason != "" || p.Block.Entries != 3 || tried != 1 {
		t.Fatalf("room for all: %+v %v (tried %d)", p, err, tried)
	}
	// No room beside the diff: dropped, and the guard is not even asked.
	tried = 0
	if p, _ = Place(hits, 5000, 20, factor, ok); p.Reason != ReasonBudget || p.Block.Text != "" || tried != 0 {
		t.Errorf("no room: %+v (tried %d)", p, tried)
	}
	// Room for two entries only.
	two := Render(hits[:2], 10000, factor)
	if p, _ = Place(hits, 5000, tokens.Estimate(two.Text, factor), factor, ok); p.Block.Entries != 2 || p.Block.Omitted != 1 {
		t.Errorf("room for two: %+v", p)
	}
	// The guard would trim the diff: dropped whole.
	trims := func(string) (bool, error) { return false, nil }
	if p, _ = Place(hits, 5000, 5000, factor, trims); p.Reason != ReasonBudget || p.Block.Text != "" || p.Block.Omitted != 3 {
		t.Errorf("trimming block kept: %+v", p)
	}
	// max_tokens bounds the block even when the room is large.
	if p, _ = Place(hits, tokens.Estimate(two.Text, factor), 1<<20, factor, ok); p.Block.Entries != 2 {
		t.Errorf("max_tokens not applied: %+v", p)
	}
}

func TestSummarize(t *testing.T) {
	if rc, notes := Summarize(nil); rc.Status != llmrun.RepoOff || rc.Reason != "" || notes != nil {
		t.Errorf("off: %+v %v", rc, notes)
	}
	used := Outcome{Status: llmrun.RepoUsed, Symbols: 3, References: 4, Files: 2, Omitted: 1}
	rc, notes := Summarize([]Outcome{used})
	if rc != (llmrun.RepoContext{Status: llmrun.RepoUsed, Symbols: 3, References: 4, Files: 2}) || len(notes) != 1 ||
		notes[0] != NoteClipped(1) {
		t.Errorf("used: %+v %v", rc, notes)
	}
	skip := Outcome{Status: llmrun.RepoSkipped, Reason: gitctx.ReasonAuth}
	rc, notes = Summarize([]Outcome{skip})
	if rc != (llmrun.RepoContext{Status: llmrun.RepoSkipped, Reason: "auth"}) || len(notes) != 1 || notes[0] != gitctx.Note("auth") {
		t.Errorf("skipped: %+v %v", rc, notes)
	}
	// A review in parts: used when any part searched; the skipped parts are
	// named in one note; counts are summed.
	rc, notes = Summarize([]Outcome{used, skip, {Status: llmrun.RepoUsed, Symbols: 1}})
	if rc.Status != llmrun.RepoUsed || rc.Symbols != 4 || rc.References != 4 || rc.Files != 2 || rc.Reason != "" ||
		len(notes) != 2 || notes[0] != NotePartsSkipped(1, "auth") {
		t.Errorf("parts: %+v %v", rc, notes)
	}
	// Every part skipped: skipped, with the first reason.
	rc, _ = Summarize([]Outcome{skip, {Status: llmrun.RepoSkipped, Reason: ReasonBudget}})
	if rc.Status != llmrun.RepoSkipped || rc.Reason != "auth" {
		t.Errorf("all skipped: %+v", rc)
	}
}

func TestNoteFor(t *testing.T) {
	if NoteFor(ReasonBudget) != NoteBudget || NoteFor(gitctx.ReasonTimeout) != gitctx.Note(gitctx.ReasonTimeout) {
		t.Error("NoteFor does not map the reasons")
	}
}

type fakeBackend struct {
	ensureErr, grepErr error
	ensures            int
	queries            []gitctx.Query
	hits               map[string][]gitctx.Hit
}

func (f *fakeBackend) Ensure(context.Context, gitctx.Repo, gitctx.PR) (gitctx.Checkout, error) {
	f.ensures++
	return gitctx.Checkout{GitDir: "/cache/x.git", HeadSHA: strings.Repeat("a", 40)}, f.ensureErr
}

func (f *fakeBackend) Grep(_ context.Context, _ gitctx.Checkout, q gitctx.Query) (gitctx.Result, error) {
	f.queries = append(f.queries, q)
	if f.grepErr != nil {
		return gitctx.Result{}, f.grepErr
	}
	res := gitctx.Result{Symbols: len(q.Symbols)}
	seen := map[string]bool{}
	for _, s := range q.Symbols {
		for _, h := range f.hits[s.Name] {
			res.Hits = append(res.Hits, h)
			seen[h.Path] = true
		}
	}
	res.Files = len(seen)
	return res, nil
}

func goFile(path, def string) provider.FilePatch {
	return provider.FilePatch{Path: path, Type: provider.ChangeModified, Patch: "@@ -1,2 +1,3 @@\n a\n+" + def + "\n b\n"}
}

func sessionOf(b Backend) *Session {
	cfg := config.Defaults()
	cfg.Context.Repo.Enabled = true
	cfg.Gitea.BaseURL = "https://gitea.example"
	ref := provider.PRRef{Kind: provider.KindGitea, Namespace: "o", Repo: "r", Number: 7}
	files := []provider.FilePatch{goFile("a.go", "func Alpha() {}"), goFile("b.go", "func Beta() {}")}
	return Open(cfg, b, ref, nil, strings.Repeat("a", 40), files, nil, nil)
}

// TestSessionFetchesOnceAndOnlyWhenNeeded: a search with no symbol does no
// git work; the head is fetched once however many searches follow; the
// pull request's files are excluded; a failure is a fixed reason.
func TestSessionFetchesOnceAndOnlyWhenNeeded(t *testing.T) {
	fb := &fakeBackend{hits: map[string][]gitctx.Hit{"Alpha": {{Symbol: "Alpha", Path: "u.go", Line: 1, Snippet: "Alpha()"}}}}
	s := sessionOf(fb)

	if f := s.Find(context.Background(), []provider.FilePatch{{Path: "x.go", Patch: "@@ -1 +1 @@\n-a\n+b\n"}}); f.Symbols != 0 || f.Skipped != "" || fb.ensures != 0 {
		t.Errorf("no symbol: %+v, ensures %d", f, fb.ensures)
	}
	a := []provider.FilePatch{goFile("a.go", "func Alpha() {}")}
	f := s.Find(context.Background(), a)
	if f.Skipped != "" || f.Symbols != 1 || len(f.Hits) != 1 || len(f.Names) != 1 || f.Names[0] != "Alpha" {
		t.Fatalf("found = %+v", f)
	}
	s.Find(context.Background(), a)
	if fb.ensures != 1 || len(fb.queries) != 2 {
		t.Errorf("ensures %d, greps %d; want 1 and 2", fb.ensures, len(fb.queries))
	}
	if ex := fb.queries[0].Exclude; len(ex) != 2 || ex[0] != "a.go" || ex[1] != "b.go" || fb.queries[0].MaxHitsPerSymbol != 5 {
		t.Errorf("query = %+v", fb.queries[0])
	}

	// Failures are reasons, memoized for the review.
	bad := &fakeBackend{ensureErr: &gitctx.Error{Reason: gitctx.ReasonSHAMismatch}}
	s = sessionOf(bad)
	if f := s.Find(context.Background(), a); f.Skipped != gitctx.ReasonSHAMismatch || bad.ensures != 1 {
		t.Errorf("ensure failure: %+v", f)
	}
	s.Find(context.Background(), a)
	if bad.ensures != 1 {
		t.Errorf("a failed fetch was retried: %d", bad.ensures)
	}
	grepFails := &fakeBackend{grepErr: &gitctx.Error{Reason: gitctx.ReasonTimeout}}
	if f := sessionOf(grepFails).Find(context.Background(), a); f.Skipped != gitctx.ReasonTimeout {
		t.Errorf("grep failure: %+v", f)
	}
	// An unknown error is git_failed; its text is never kept.
	grepFails = &fakeBackend{grepErr: errors.New("fatal: secret stderr text")}
	if f := sessionOf(grepFails).Find(context.Background(), a); f.Skipped != gitctx.ReasonGitFailed {
		t.Errorf("unknown error: %+v", f)
	}
	// A provider the repository cannot be reached for.
	cfg := config.Defaults()
	ref := provider.PRRef{Kind: provider.KindGitea, Namespace: "o", Repo: "r", Number: 7}
	none := &fakeBackend{}
	if f := Open(cfg, none, ref, nil, "abc", nil, nil, nil).Find(context.Background(), a); f.Skipped != gitctx.ReasonUnsupported || none.ensures != 0 {
		t.Errorf("no base URL: %+v", f)
	}
}
