package contract

// The merged-publish scenario (X-33, WP-5d). Like RunReviewDuplicates it is
// not a provider contract: it drives pr_review's publish of supplied
// findings (review.Run with Args.Findings, the real renderers and a model
// that fails the test when called) through the real provider and the
// fixture's fake server, so that the X-29 duplicate rule holds for client
// findings on every provider that has a Fixture. Only _test.go files import
// it.

import (
	"context"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/review/render"
)

// noModel fails the test on any model call.
type noModel struct{ t *testing.T }

func (n noModel) Complete(context.Context, string, string) (*llm.Response, error) {
	n.t.Error("the model was called for a publish of supplied findings")
	return &llm.Response{}, nil
}

// RunReviewFindings runs the merged-publish scenario of X-33 against f: a
// list of supplied findings is published without a model call; a finding
// on the place of review-mcp's own resolved finding comment is not posted
// again (X-29), while an outdated own comment and anyone else's comment
// with a copied marker do not count; an invalid item is dropped and an
// exact repeat merged; a second publish of the same list adds nothing new.
func RunReviewFindings(t *testing.T, f Fixture) {
	t.Helper()
	var tr Traits
	if dc, ok := f.(Declarer); ok {
		tr = dc.Traits()
	}
	s := &suite{f: f, tr: tr}
	p, ref, _ := s.serve(t, dupPR(true))
	finding := func(line int, header, content string) review.SuppliedFinding {
		return review.SuppliedFinding{RelevantFile: pathDup, StartLine: line, EndLine: line, IssueHeader: header,
			IssueContent: content, Focus: review.FocusSecurity}
	}
	findings := []review.SuppliedFinding{
		finding(lineOutdated, "On the outdated comment", "Something new under an outdated thread."),
		finding(lineForeign, "On the copied marker", "A comment by someone else does not count."),
		finding(lineResolved, "On the resolved comment", "Raised again after it was resolved."),
		finding(2, "On a free line", "Nothing was said about this line."),
		finding(2, "on a FREE line", "The same finding, merged by a client that did not de-duplicate."),
		{RelevantFile: "src/not-in-the-diff.go", StartLine: 1, EndLine: 1, IssueHeader: "Ghost", IssueContent: "Dropped."},
	}
	run := func(t *testing.T) *review.Result {
		t.Helper()
		cfg := config.Defaults()
		cfg.LLM.BaseURL, cfg.LLM.Model, cfg.LLM.ContextWindow = "https://llm.example.com/v1", "test-model", 32000
		res, err := review.Run(t.Context(), review.Deps{
			Config: cfg, Resolver: fixedResolver{ref, p}, LLM: noModel{t},
			RenderProvider: render.Provider, RenderInline: render.Inline,
		}, review.Args{PRURL: "https://git.example.com/octo/demo/pulls/7", Publish: true, Findings: findings,
			SecurityConcerns: "No"})
		if err != nil {
			t.Fatalf("review.Run: %v", err)
		}
		if res.Publish == nil || !res.Publish.Published || res.Publish.Inline == nil || res.Metadata.LLMCalls != 0 {
			t.Fatalf("publish = %+v, llm_calls %d", res.Publish, res.Metadata.LLMCalls)
		}
		if len(res.Notes) < 3 || res.Notes[0] != review.NoteClientFindings {
			t.Errorf("notes = %q", res.Notes)
		}
		return res
	}
	d := &dupRunner{p: p, ref: ref}
	resolvedURL, _ := d.commentURL(t, idOwnResolved)

	first := run(t)
	checkSummary(t, first, review.InlineSummary{Posted: 3, SkippedDuplicate: 1})
	k := first.Review.KeyIssuesToReview
	if len(k) != 4 {
		t.Fatalf("%d findings kept, want 4 (one dropped, one merged)", len(k))
	}
	checkFinding(t, "outdated own thread", &k[0], review.InlinePosted, "")
	checkFinding(t, "foreign copied marker", &k[1], review.InlinePosted, "")
	checkFinding(t, "resolved own thread", &k[2], review.InlineSkippedDuplicate, resolvedURL)
	checkFinding(t, "free line", &k[3], review.InlinePosted, "")
	if k[0].Focus != review.FocusSecurity || k[0].Snippet == "" {
		t.Errorf("first finding: focus %q, snippet %q", k[0].Focus, k[0].Snippet)
	}

	second := run(t)
	checkSummary(t, second, review.InlineSummary{SkippedDuplicate: 4})
	if !second.Publish.Updated {
		t.Errorf("the second publish did not edit the overview in place: %+v", second.Publish)
	}
	d.overview(t)
}
