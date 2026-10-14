package contract

// The pr_review duplicate scenario (X-29, WP-4a). Unlike Run, it is not a
// provider contract: it drives pr_review's publish (review.Run with the
// real renderers and a canned model) through the real provider and the
// fixture's fake server, so the same end-to-end test runs on every
// provider that has a Fixture. Only _test.go files import it, as for Run.

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/review/render"
)

// pathDup is the one file of dupPR: 30 added lines, so every line from 1 to
// 30 can carry an inline comment on every host.
const pathDup = "src/dup.go"

// Comment IDs of dupPR's threads.
const (
	idOwnOutdated = 41 // the token user's finding comment, outdated, on line 24
	idForeignMark = 42 // alice's comment with a copied finding marker, on line 26
	idOwnResolved = 43 // the token user's finding comment, resolved, on line 28
	lineOutdated  = 24
	lineForeign   = 26
	lineResolved  = 28
)

// dupPR is a pull request that adds pathDup. With threads set it also has
// the planted threads of the "places" case.
func dupPR(threads bool) Spec {
	var head, hunks strings.Builder
	const lines = 30
	hunks.WriteString("@@ -0,0 +1," + strconv.Itoa(lines) + " @@\n")
	for i := 1; i <= lines; i++ {
		l := "var v" + strconv.Itoa(i) + " = " + strconv.Itoa(i) + "\n"
		head.WriteString(l)
		hunks.WriteString("+" + l)
	}
	pr := samplePR()
	pr.Title, pr.Description = "Add values", "Synthetic pull request of the duplicate scenario."
	pr.Files = []File{{Path: pathDup, Type: provider.ChangeAdded, Head: head.String(), Hunks: hunks.String()}}
	pr.Threads, pr.Reviewers, pr.Checks = nil, nil, nil
	if threads {
		marker := func(fp string) string { return "Earlier finding.\n\n" + review.FingerprintMarker(fp) }
		pr.Threads = []Thread{
			{Kind: provider.ThreadInline, Path: pathDup, Line: lineOutdated, Outdated: true, Comments: []Comment{
				{ID: idOwnOutdated, Author: tokenUser, Body: marker("0000000000aa"), Created: at(1)},
			}},
			{Kind: provider.ThreadInline, Path: pathDup, Line: lineForeign, Comments: []Comment{
				{ID: idForeignMark, Author: alice, Body: marker("0000000000bb"), Created: at(2)},
			}},
			{Kind: provider.ThreadInline, Path: pathDup, Line: lineResolved, Resolved: true, Comments: []Comment{
				{ID: idOwnResolved, Author: tokenUser, Body: marker("0000000000cc"), Created: at(3)},
			}},
		}
	}
	return pr
}

// dupFinding is one finding of a canned model answer.
type dupFinding struct {
	header, content string
	start, end      int
}

func dupAnswer(fs ...dupFinding) string {
	var b strings.Builder
	b.WriteString("```yaml\nreview:\n  estimated_effort_to_review: 1\n  key_issues_to_review:\n")
	for _, f := range fs {
		fmt.Fprintf(&b, "    - relevant_file: %s\n      issue_header: %s\n      issue_content: %s\n"+
			"      start_line: %d\n      end_line: %d\n", pathDup, f.header, f.content, f.start, f.end)
	}
	b.WriteString("```\n")
	return b.String()
}

// cannedLLM answers the model calls of one review with answer.
type cannedLLM struct {
	mu     sync.Mutex
	answer string
}

func (c *cannedLLM) Complete(context.Context, string, string) (*llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &llm.Response{Content: c.answer}, nil
}

type fixedResolver struct {
	ref provider.PRRef
	p   provider.Provider
}

func (r fixedResolver) Resolve(string) (provider.PRRef, provider.Provider, error) {
	return r.ref, r.p, nil
}

// dupRunner runs pr_review with publish on one served pull request.
type dupRunner struct {
	p   provider.Provider
	ref provider.PRRef
	llm *cannedLLM
}

func (d *dupRunner) run(t *testing.T, fs ...dupFinding) *review.Result {
	t.Helper()
	return d.runAnswer(t, dupAnswer(fs...))
}

// runAnswer runs pr_review with publish and the model answer answer.
func (d *dupRunner) runAnswer(t *testing.T, answer string) *review.Result {
	t.Helper()
	d.llm.mu.Lock()
	d.llm.answer = answer
	d.llm.mu.Unlock()
	cfg := config.Defaults()
	cfg.LLM.BaseURL, cfg.LLM.Model, cfg.LLM.ContextWindow = "https://llm.example.com/v1", "test-model", 32000
	res, err := review.Run(t.Context(), review.Deps{
		Config: cfg, Resolver: fixedResolver{d.ref, d.p}, LLM: d.llm,
		RenderProvider: render.Provider, RenderInline: render.Inline,
	}, review.Args{PRURL: "https://git.example.com/octo/demo/pulls/7", Publish: true, MaxFindings: 10})
	if err != nil {
		t.Fatalf("review.Run: %v", err)
	}
	if res.Publish == nil || !res.Publish.Published || res.Publish.Inline == nil || res.Review == nil {
		t.Fatalf("publish = %+v", res.Publish)
	}
	return res
}

// overview returns the body of the token user's overview on the PR.
func (d *dupRunner) overview(t *testing.T) string {
	t.Helper()
	threads, err := d.p.ListThreads(t.Context(), d.ref)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	var out []string
	for _, th := range threads {
		if th.Kind == provider.ThreadGeneral && len(th.Comments) > 0 && review.HasOverviewMarker(th.Comments[0].Body) {
			out = append(out, th.Comments[0].Body)
		}
	}
	if len(out) != 1 {
		t.Fatalf("%d overviews on the PR, want 1", len(out))
	}
	return out[0]
}

// commentURL returns the URL ListThreads reports for the comment id.
func (d *dupRunner) commentURL(t *testing.T, id int64) (string, provider.Thread) {
	t.Helper()
	threads, err := d.p.ListThreads(t.Context(), d.ref)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	for _, th := range threads {
		for _, c := range th.Comments {
			if c.ID == strconv.FormatInt(id, 10) {
				return c.URL, th
			}
		}
	}
	t.Fatalf("comment %d is not listed", id)
	return "", provider.Thread{}
}

func checkSummary(t *testing.T, res *review.Result, want review.InlineSummary) {
	t.Helper()
	if got := *res.Publish.Inline; got != want {
		t.Errorf("inline = %+v, want %+v", got, want)
	}
}

func checkFinding(t *testing.T, name string, ki *review.KeyIssue, status, url string) {
	t.Helper()
	if ki.InlineStatus != status || (url != "" && ki.InlineURL != url) {
		t.Errorf("%s: inline_status %q, inline_url %q; want %q, %q", name, ki.InlineStatus, ki.InlineURL, status, url)
	}
	if url == "" && status == review.InlinePosted && ki.InlineURL == "" {
		t.Errorf("%s: posted without an inline_url", name)
	}
}

// RunReviewDuplicates runs the duplicate scenario of X-29 against f: a
// finding on the place of review-mcp's own finding comment is not posted
// again and links to that comment; an outdated own comment and anyone
// else's comment with a copied marker do not count; a resolved own comment
// does. A finding's line that could run a quick action is published
// sanitised exactly when the provider has QuickActions (quick_action_line).
// Each case serves its own pull request.
func RunReviewDuplicates(t *testing.T, f Fixture) {
	t.Helper()
	var tr Traits
	if dc, ok := f.(Declarer); ok {
		tr = dc.Traits()
	}
	s := &suite{f: f, tr: tr}
	serve := func(t *testing.T, pr Spec) *dupRunner {
		p, ref, _ := s.serve(t, pr)
		return &dupRunner{p: p, ref: ref, llm: &cannedLLM{}}
	}

	// reworded is the #19 shape: the second answer rewords both findings
	// and widens one of them to a range over both earlier comments.
	t.Run("reworded", func(t *testing.T) {
		d := serve(t, dupPR(false))
		first := d.run(t,
			dupFinding{"Unchecked value", "The value on this line is never checked.", 14, 14},
			dupFinding{"Magic number", "The constant should be named.", 19, 19})
		checkSummary(t, first, review.InlineSummary{Posted: 2})
		k1 := first.Review.KeyIssuesToReview
		on14, on19 := k1[0].InlineURL, k1[1].InlineURL
		if on14 == "" || on19 == "" || on14 == on19 {
			t.Fatalf("first run inline URLs %q and %q", on14, on19)
		}
		second := d.run(t,
			dupFinding{"Unnamed constant", "Give this literal a name.", 19, 19},
			dupFinding{"Values not validated", "None of these values is validated before use.", 14, 19})
		checkSummary(t, second, review.InlineSummary{SkippedDuplicate: 2})
		k2 := second.Review.KeyIssuesToReview
		checkFinding(t, "single line 19", &k2[0], review.InlineSkippedDuplicate, on19)
		checkFinding(t, "range 14-19", &k2[1], review.InlineSkippedDuplicate, on14)
		if !strings.Contains(strings.Join(second.Notes, "\n"), "2 findings were already posted on this PR and were not repeated.") {
			t.Errorf("notes = %q", second.Notes)
		}
		ov := d.overview(t)
		for _, u := range []string{on14, on19} {
			if !strings.Contains(ov, u) && !strings.Contains(ov, strings.ReplaceAll(u, "&", "&amp;")) {
				t.Errorf("the overview does not link the earlier comment %q", u)
			}
		}
	})

	// places: one run against planted threads.
	t.Run("places", func(t *testing.T) {
		d := serve(t, dupPR(true))
		resolvedURL, resolvedThread := d.commentURL(t, idOwnResolved)
		if _, th := d.commentURL(t, idOwnOutdated); !th.Outdated {
			t.Fatalf("the planted outdated thread is listed as not outdated; the case proves nothing")
		}
		if resolvedURL == "" {
			t.Fatalf("the own resolved comment has no URL")
		}
		res := d.run(t,
			dupFinding{"On the outdated comment", "Something new under an outdated thread.", lineOutdated, lineOutdated},
			dupFinding{"On the copied marker", "A comment by someone else does not count.", lineForeign, lineForeign},
			dupFinding{"On the resolved comment", "Raised again after it was resolved.", lineResolved, lineResolved},
			dupFinding{"On a free line", "Nothing was said about this line.", 2, 2})
		checkSummary(t, res, review.InlineSummary{Posted: 3, SkippedDuplicate: 1})
		k := res.Review.KeyIssuesToReview
		checkFinding(t, "outdated own thread", &k[0], review.InlinePosted, "")
		checkFinding(t, "foreign copied marker", &k[1], review.InlinePosted, "")
		checkFinding(t, fmt.Sprintf("resolved own thread (Resolved %v)", fmtBoolPtr(resolvedThread.Resolved)), &k[2],
			review.InlineSkippedDuplicate, resolvedURL)
		checkFinding(t, "free line", &k[3], review.InlinePosted, "")
	})

	// quick_action_line (X-35, architect request DQ-b2): a finding whose
	// text has a line "/close" reaches the host through review.Run and the
	// real renderers as " /close" on a provider with QuickActions, and as
	// written ("/close") on any other: the sanitising follows the
	// capability end to end, and a provider without quick actions publishes
	// the text unchanged.
	t.Run("quick_action_line", func(t *testing.T) {
		d := serve(t, dupPR(false))
		res := d.runAnswer(t, "```yaml\nreview:\n  estimated_effort_to_review: 1\n  key_issues_to_review:\n"+
			"    - relevant_file: "+pathDup+"\n      issue_header: Not ready\n      issue_content: |\n"+
			"        Please do not merge this yet.\n        /close\n      start_line: 5\n      end_line: 5\n```\n")
		checkSummary(t, res, review.InlineSummary{Posted: 1})
		threads, err := d.p.ListThreads(t.Context(), d.ref)
		if err != nil {
			t.Fatalf("ListThreads: %v", err)
		}
		var body string
		for _, th := range threads {
			if th.Kind == provider.ThreadInline && len(th.Comments) > 0 && strings.Contains(th.Comments[0].Body, "Please do not merge this yet.") {
				body = th.Comments[0].Body
			}
		}
		if body == "" {
			t.Fatalf("the finding is not on the host")
		}
		lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
		want, not := "/close", " /close"
		if d.p.Capabilities().QuickActions {
			want, not = " /close", "/close"
		}
		if !slices.Contains(lines, want) || slices.Contains(lines, not) {
			t.Errorf("QuickActions %v: the finding's line reached the host as %q, want %q:\n%s",
				d.p.Capabilities().QuickActions, lines, want, body)
		}
	})

	// range_start: an own comment on a range covers every line of it where
	// the provider reports the range's start (Traits.InlineRanges); without
	// it the comment is known on its first line only.
	t.Run("range_start", func(t *testing.T) {
		d := serve(t, dupPR(false))
		first := d.run(t, dupFinding{"Range finding", "These lines repeat each other.", 6, 9})
		checkSummary(t, first, review.InlineSummary{Posted: 1})
		earlier := first.Review.KeyIssuesToReview[0].InlineURL
		res := d.run(t, dupFinding{"Inside the range", "This line repeats the one above.", 8, 8})
		k := &res.Review.KeyIssuesToReview[0]
		if tr.InlineRanges {
			checkSummary(t, res, review.InlineSummary{SkippedDuplicate: 1})
			checkFinding(t, "inside a range comment", k, review.InlineSkippedDuplicate, earlier)
		} else {
			checkSummary(t, res, review.InlineSummary{Posted: 1})
			checkFinding(t, "inside a range posted on its first line", k, review.InlinePosted, "")
		}
	})
}
