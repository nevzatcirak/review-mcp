package review

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
)

// noCallLLM fails the test on any model call: a merged publish (X-33)
// never calls the model.
type noCallLLM struct{ t *testing.T }

func (n noCallLLM) Complete(context.Context, string, string) (*llm.Response, error) {
	n.t.Error("the model was called for a publish of supplied findings")
	return &llm.Response{Content: goodAnswer}, nil
}

func newSuppliedHarness(t *testing.T) *harness {
	h := newHarness()
	h.deps.LLM = noCallLLM{t}
	return h
}

func supplied(file string, start, end int, header, content string) SuppliedFinding {
	return SuppliedFinding{RelevantFile: file, StartLine: start, EndLine: end, IssueHeader: header, IssueContent: content}
}

// TestRunSuppliedFindings: findings publish with no model call; invalid
// items are dropped and counted, exact repeats merged, and the publish runs
// as for a model's review (overview, inline comments, snippets, links).
func TestRunSuppliedFindings(t *testing.T) {
	h := newSuppliedHarness(t)
	findings := []SuppliedFinding{
		supplied("src/app.go", 11, 11, "Possible Bug", "Line 11 is wrong "+answerMarker+"."),
		// A repeat: same place, header equal after case and white space.
		supplied(" src/app.go ", 11, 11, "  possible   BUG ", "Another wording of the same issue."),
		// Same place, another header: kept.
		supplied("src/app.go", 11, 11, "Missing test", "Nothing tests line 11."),
		{RelevantFile: "src/util.go", StartLine: 2, EndLine: 2, IssueHeader: "Style", IssueContent: "Name it.", Focus: "Architecture"},
		// Invalid: not in the diff, filtered out of it, a line not positive,
		// the end before the start, an empty header or content, a header too
		// long, an invalid focus.
		supplied("src/missing.go", 1, 1, "Ghost", "Not a changed file."),
		supplied("vendor/lib.go", 1, 1, "Filtered", "Excluded by the ignore rules."),
		supplied("src/app.go", 0, 1, "Zero", "Line zero."),
		supplied("src/app.go", 12, 11, "Backwards", "Ends before it starts."),
		supplied("src/app.go", 11, 11, "  ", "No header."),
		supplied("src/app.go", 11, 11, "No content", " \n "),
		supplied("src/app.go", 11, 11, strings.Repeat("h", MaxSuppliedHeaderRunes+1), "Header too long."),
		{RelevantFile: "src/app.go", StartLine: 11, EndLine: 11, IssueHeader: "Bad focus", IssueContent: "x", Focus: "a\nb"},
	}
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true, Findings: findings,
		SecurityConcerns: " No ", PerformanceConcerns: "One query per item in src/app.go."})
	if err != nil {
		t.Fatal(err)
	}
	if res.Metadata.LLMCalls != 0 || res.Coverage.ModelCalls != 0 || res.Metadata.RequestTokens != 0 {
		t.Errorf("llm_calls %d, model_calls %d, request_tokens %d; want 0", res.Metadata.LLMCalls,
			res.Coverage.ModelCalls, res.Metadata.RequestTokens)
	}
	got := res.Review.KeyIssuesToReview
	if len(got) != 3 || got[0].IssueHeader != "Possible Bug" || got[1].IssueHeader != "Missing test" ||
		got[2].RelevantFile != "src/util.go" || got[2].Focus != FocusArchitecture {
		t.Fatalf("findings = %+v", got)
	}
	if got[0].Snippet == "" || got[0].Link == "" || got[0].InlineStatus != InlinePosted {
		t.Errorf("first finding: snippet %q, link %q, inline %q", got[0].Snippet, got[0].Link, got[0].InlineStatus)
	}
	r := res.Review
	if r.EstimatedEffortToReview != nil || r.RelevantTests != nil {
		t.Errorf("effort %v, tests %v; want absent", r.EstimatedEffortToReview, r.RelevantTests)
	}
	if r.SecurityConcerns == nil || *r.SecurityConcerns != SecurityNo || r.PerformanceConcerns == nil ||
		*r.PerformanceConcerns != "One query per item in src/app.go." {
		t.Errorf("concerns = %v, %v", r.SecurityConcerns, r.PerformanceConcerns)
	}
	wantNotes := []string{
		"These findings were supplied by the client (merged from focused reviews); no model was called for this publish.",
		"8 supplied findings were dropped: a finding needs a changed file of the diff, positive lines that do not end before they start, a header and a description that are neither empty nor too long, and a valid focus when it has one.",
		"1 supplied finding repeated an earlier one at the same place with the same header and was merged into it.",
	}
	if len(res.Notes) < 3 || !slices.Equal(res.Notes[:3], wantNotes) {
		t.Errorf("notes = %q", res.Notes)
	}
	if res.Publish == nil || !res.Publish.Published || res.Publish.Inline == nil ||
		*res.Publish.Inline != (InlineSummary{Posted: 3}) {
		t.Errorf("publish = %+v", res.Publish)
	}
	if !slices.Equal(h.prov.seq, []string{"post", "inline", "edit"}) {
		t.Errorf("write calls %v", h.prov.seq)
	}
	// The coverage comes from the diff; nothing that only feeds a prompt
	// was read.
	if len(res.Coverage.Included) != 2 || res.Coverage.Guidelines != nil || res.Coverage.FileTree != nil {
		t.Errorf("coverage = %+v", res.Coverage)
	}
	if len(h.prov.reads) != 0 {
		t.Errorf("project context read: %v", h.prov.reads)
	}
	if res.Metadata.AlreadyDiscussed != 0 || res.Focus != "" {
		t.Errorf("already_discussed %d, focus %q", res.Metadata.AlreadyDiscussed, res.Focus)
	}
	h.checkNoLeaks(t)
}

// TestRunSuppliedFindingsRepoContext: with repository context on, a merged
// publish fetches nothing for it and reports it as skipped.
func TestRunSuppliedFindingsRepoContext(t *testing.T) {
	h := newSuppliedHarness(t)
	h.deps.Config.Context.Repo.Enabled = true
	h.deps.RepoContext = panicBackend{}
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true,
		Findings: []SuppliedFinding{supplied("src/app.go", 11, 11, "H", "C")}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (llmrun.RepoContext{Status: llmrun.RepoSkipped, Reason: repoctx.ReasonClientFindings}); withoutTools(res.Coverage.RepoContext) != want {
		t.Errorf("repo_context = %+v, want %+v", res.Coverage.RepoContext, want)
	}
	if rt := res.Coverage.RepoContext.Tools; rt == nil || rt.Status != "off" {
		t.Errorf("repo_context.tools = %+v, want off", rt)
	}
}

// panicBackend is a repository-context backend that must not be used.
type panicBackend struct{ repoctx.Backend }

// TestRunSuppliedFindingsCap: the list is capped at the effective
// review.max_total_findings, with the cap note.
func TestRunSuppliedFindingsCap(t *testing.T) {
	h := newSuppliedHarness(t)
	h.deps.Config.Review.MaxFindings = 1
	h.deps.Config.Review.MaxTotalFindings = 2
	res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true, Findings: []SuppliedFinding{
		supplied("src/app.go", 10, 10, "One", "First."),
		supplied("src/app.go", 11, 11, "Two", "Second."),
		supplied("src/app.go", 12, 12, "Three", "Third."),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(res.Review.KeyIssuesToReview); n != 2 || res.Review.KeyIssuesToReview[1].IssueHeader != "Two" {
		t.Errorf("%d findings kept: %+v", n, res.Review.KeyIssuesToReview)
	}
	if !slices.Contains(res.Notes, "1 further finding was not shown because of review.max_total_findings.") {
		t.Errorf("notes = %q", res.Notes)
	}
}

// TestRunSuppliedFindingsTooMany: more items than twice the cap are
// refused before anything is sent.
func TestRunSuppliedFindingsTooMany(t *testing.T) {
	h := newSuppliedHarness(t)
	h.deps.Config.Review.MaxFindings = 1
	h.deps.Config.Review.MaxTotalFindings = 2
	_, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Publish: true, Findings: make([]SuppliedFinding, 5)})
	if err == nil || err.Error() != "findings must hold at most 4 items (twice review.max_total_findings); merge repeats before publishing" {
		t.Fatalf("err = %v", err)
	}
	if h.resolver.calls != 0 || h.prov.calls != 0 {
		t.Errorf("resolver %d, provider %d calls; want none", h.resolver.calls, h.prov.calls)
	}
}

// TestRunSuppliedFindingsDuplicates: a supplied finding on the place of
// review-mcp's own earlier finding comment is not posted again (X-29), as
// for a model's finding; a second merged publish of the same list adds
// nothing new.
func TestRunSuppliedFindingsDuplicates(t *testing.T) {
	h := newSuppliedHarness(t)
	h.prov.threads = []provider.Thread{{ID: "t1", Kind: provider.ThreadInline, Path: "src/app.go", Line: 11,
		Comments: []provider.CommentItem{{ID: "c1", Body: "Earlier.\n\n" + FingerprintMarker("0123456789ab"),
			AuthorID: h.prov.me.ID, AuthorLogin: h.prov.me.Name, URL: "https://your-gitea.example/c1"}}}}
	args := Args{PRURL: testPRURL, Publish: true, Findings: []SuppliedFinding{
		supplied("src/app.go", 11, 11, "Reworded", "Same place, new words."),
		supplied("src/util.go", 2, 2, "Style", "Name it."),
	}}
	first, err := Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	if in := first.Publish.Inline; in == nil || *in != (InlineSummary{Posted: 1, SkippedDuplicate: 1}) {
		t.Fatalf("first inline = %+v", first.Publish.Inline)
	}
	if ki := first.Review.KeyIssuesToReview[0]; ki.InlineStatus != InlineSkippedDuplicate || ki.InlineURL != "https://your-gitea.example/c1" {
		t.Errorf("duplicate finding: %q %q", ki.InlineStatus, ki.InlineURL)
	}
	second, err := Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	if in := second.Publish.Inline; in == nil || *in != (InlineSummary{SkippedDuplicate: 2}) || !second.Publish.Updated {
		t.Errorf("second publish = %+v, inline %+v", second.Publish, second.Publish.Inline)
	}
}
