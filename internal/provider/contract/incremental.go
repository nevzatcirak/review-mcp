package contract

// The incremental-review scenario (X-36, WP-7d; carry-forward WP-7f). Like RunReviewDuplicates it
// is not a provider contract: it drives pr_review (review.Run with the real
// renderers and a canned model) through the real provider and the fixture's
// fake server, so that the files changed since the last review are found
// through every provider's own reads. Only _test.go files import it.

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/review/incremental"
	"github.com/nevzatcirak/review-mcp/internal/review/render"
)

// Paths, contents and commits of incrementalPR.
const (
	pathIncSame     = "src/same.go"
	pathIncEdited   = "src/edited.go"
	incSameHead     = "package src\n\nvar Same = 2\n"
	incEditedLast   = "package src\n\nvar Edited = 2\n"
	incEditedHead   = "package src\n\nvar Edited = 3\n"
	shaLastReview   = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
	idOwnOverview   = 31
	incSameMarker   = "var Same = 2"
	incEditedMarker = "var Edited = 3"
	// incHeldHeader is the header of the earlier review's finding on
	// pathIncSame that the overview listed only (its inline comment was
	// not posted): the overview records it in a held line.
	incHeldHeader  = "Earlier overview-only finding"
	incHeldContent = "The value was never checked."
)

// incHeld is the record of a held line (spec v2.1 §7.2), as the scenario
// writes and reads it.
type incHeld struct {
	V    int    `json:"v"`
	F    string `json:"f"`
	S    int    `json:"s"`
	E    int    `json:"e"`
	H    string `json:"h"`
	C    string `json:"c"`
	From string `json:"from"`
}

// incHeldPrefix opens a held line.
const incHeldPrefix = "[//]: # (review-mcp:held:"

// heldLine is the held line of r.
func heldLine(r incHeld) string {
	b, err := json.Marshal(r)
	if err != nil {
		panic(err)
	}
	return incHeldPrefix + base64.RawURLEncoding.EncodeToString(b) + ")"
}

// heldRecords decodes the held lines of an overview body; a line that does
// not decode is skipped.
func heldRecords(body string) []incHeld {
	var out []incHeld
	for line := range strings.SplitSeq(body, "\n") {
		p, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), incHeldPrefix)
		if !ok {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSuffix(p, ")"))
		var r incHeld
		if err != nil || json.Unmarshal(raw, &r) != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// incrementalPR changes two files. The last review recorded shaLastReview,
// at which pathIncSame already had its head content and pathIncEdited had
// another one: since that review, only pathIncEdited changed. The token
// user's overview of that review is on the pull request; it listed one
// finding on pathIncSame only, so it holds a held line for it.
func incrementalPR() Spec {
	pr := samplePR()
	pr.Title, pr.Description = "Bump values", "Synthetic pull request of the incremental scenario."
	pr.Files = []File{
		{Path: pathIncSame, Type: provider.ChangeModified, Base: "package src\n\nvar Same = 1\n", Head: incSameHead,
			Hunks: "@@ -1,3 +1,3 @@\n package src\n \n-var Same = 1\n+var Same = 2\n"},
		{Path: pathIncEdited, Type: provider.ChangeModified, Base: "package src\n\nvar Edited = 1\n", Head: incEditedHead,
			Hunks: "@@ -1,3 +1,3 @@\n package src\n \n-var Edited = 1\n+var Edited = 3\n"},
	}
	last := map[string]string{pathIncSame: incSameHead, pathIncEdited: incEditedLast}
	for _, f := range pr.Unchanged {
		last[f.Path] = f.Content
	}
	pr.Revisions = map[string]map[string]string{shaLastReview: last}
	pr.Threads = []Thread{{Kind: provider.ThreadGeneral, Comments: []Comment{{ID: idOwnOverview, Author: tokenUser,
		Body: "Earlier overview.\n\n" + heldLine(incHeld{V: 1, F: pathIncSame, S: 3, E: 3, H: incHeldHeader, C: incHeldContent,
			From: shaLastReview}) + "\n[//]: # (review-mcp:reviewed:" + shaLastReview + ")\n" + review.OverviewMarker,
		Created: at(1)}}}}
	pr.Reviewers, pr.Checks = nil, nil
	return pr
}

// incAnswer is a model answer with a finding on each file, on its changed
// line, and no security concern.
func incAnswer() string {
	var b strings.Builder
	b.WriteString("```yaml\nreview:\n  estimated_effort_to_review: 1\n  security_concerns: \"No\"\n  key_issues_to_review:\n")
	for _, p := range []string{pathIncSame, pathIncEdited} {
		b.WriteString("    - relevant_file: " + p + "\n      issue_header: Magic number\n" +
			"      issue_content: The constant should be named.\n      start_line: 3\n      end_line: 3\n")
	}
	b.WriteString("```\n")
	return b.String()
}

// RunReviewIncremental runs the incremental scenario of X-36 against f: a
// second review after a head change that touched one of the two files sends
// only that file to the model and posts only for it, edits the earlier
// overview with "changes since", carries the earlier overview-only finding
// of the unchanged file forward (spec v2.1 §7, WP-7f) with its suffix and
// held line, names the earlier inline comments and scopes its statements; a
// third run on the same head makes no model call and writes nothing.
func RunReviewIncremental(t *testing.T, f Fixture) {
	t.Helper()
	var tr Traits
	if dc, ok := f.(Declarer); ok {
		tr = dc.Traits()
	}
	s := &suite{f: f, tr: tr}
	pr := incrementalPR()
	p, ref, log := s.serve(t, pr)
	llm := &cannedLLM{answer: incAnswer()}
	cfg := config.Defaults()
	cfg.LLM.BaseURL, cfg.LLM.Model, cfg.LLM.ContextWindow = "https://llm.example.com/v1", "test-model", 32000
	on := true
	run := func(t *testing.T) *review.Result {
		t.Helper()
		res, err := review.Run(t.Context(), review.Deps{
			Config: cfg, Resolver: fixedResolver{ref, p}, LLM: llm,
			RenderProvider: render.Provider, RenderInline: render.Inline,
		}, review.Args{PRURL: "https://git.example.com/octo/demo/pulls/7", Publish: true, Incremental: &on})
		if err != nil {
			t.Fatalf("review.Run: %v", err)
		}
		return res
	}

	t.Run("one_file_changed", func(t *testing.T) {
		res := run(t)
		users := llm.prompts()
		if len(users) != 1 {
			t.Fatalf("%d model calls, want 1", len(users))
		}
		if !strings.Contains(users[0], incEditedMarker) || strings.Contains(users[0], incSameMarker) {
			t.Errorf("the model must see %s only; prompt:\n%s", pathIncEdited, users[0])
		}
		want := review.Incremental{Mode: review.IncrementalChanged, Since: shaLastReview,
			Strategy: incremental.StrategyAPI, ChangedFiles: 1, UnchangedFiles: 1, CarriedFindings: 1}
		if res.Incremental == nil || *res.Incremental != want {
			t.Errorf("incremental = %+v, want %+v", res.Incremental, want)
		}
		c := res.Coverage
		if c.Partial || c.ReviewedFiles != 1 || c.NotReviewedFiles != 0 || c.UnchangedFiles != 1 ||
			!slices.Contains(c.Skipped, llmrun.SkippedFile{Path: pathIncSame, Reason: llmrun.SkipUnchangedSinceReview}) {
			t.Errorf("coverage = %+v", c)
		}
		if res.Publish == nil || !res.Publish.Published || !res.Publish.Updated || res.Publish.Inline == nil ||
			*res.Publish.Inline != (review.InlineSummary{Posted: 1, Unanchorable: 1}) {
			t.Fatalf("publish = %+v", res.Publish)
		}
		threads, err := p.ListThreads(t.Context(), ref)
		if err != nil {
			t.Fatalf("ListThreads: %v", err)
		}
		var inline []string
		var overview string
		for _, th := range threads {
			if th.Kind == provider.ThreadInline {
				inline = append(inline, th.Path)
			}
			if th.Kind == provider.ThreadGeneral && len(th.Comments) > 0 && review.HasOverviewMarker(th.Comments[0].Body) {
				overview = strings.ReplaceAll(th.Comments[0].Body, "\r\n", "\n")
			}
		}
		if !slices.Equal(inline, []string{pathIncEdited}) {
			t.Errorf("inline comments on %q, want only on %s", inline, pathIncEdited)
		}
		if !strings.Contains(overview, "(changes since `"+shaLastReview[:7]+"`)") ||
			!strings.Contains(overview, "1 file reviewed, 1 unchanged since the last review") ||
			!strings.HasSuffix(strings.TrimRight(overview, " \t\n"), "[//]: # (review-mcp:reviewed:"+pr.HeadSHA+")\n"+review.OverviewMarker) {
			t.Errorf("the overview does not continue from the last review:\n%s", overview)
		}

		// The earlier overview-only finding of the unchanged file is carried
		// forward: last in the list, listed here only with the commit of its
		// review, recorded again with that commit; never inline (above).
		issues := res.Review.KeyIssuesToReview
		if n := len(issues); n == 0 || issues[n-1].CarriedFrom != shaLastReview || issues[n-1].RelevantFile != pathIncSame ||
			issues[n-1].IssueHeader != incHeldHeader || issues[n-1].InlineStatus != "" {
			t.Errorf("no carried finding last: %+v", issues)
		}
		short := shaLastReview[:7]
		if !strings.Contains(overview, "(from the review of <code>"+short+"</code>, listed here only)") &&
			!strings.Contains(overview, "(from the review of `"+short+"`, listed here only)") {
			t.Errorf("the overview does not list the carried finding:\n%s", overview)
		}
		carried := false
		for _, r := range heldRecords(overview) {
			if r.F == pathIncSame && r.H == incHeldHeader && r.From == shaLastReview {
				carried = true
			}
		}
		if !carried {
			t.Errorf("the overview does not hold the carried finding with its commit: %+v", heldRecords(overview))
		}
		for _, w := range []string{
			"1 finding of earlier reviews on unchanged files is carried forward; it was not reviewed again.",
			"- 1 file reviewed, 1 unchanged since the last review\n- Inline comments of earlier reviews stay on the pull request.\n",
			"No security concerns identified in the reviewed files",
		} {
			if !strings.Contains(overview, w) {
				t.Errorf("the overview lacks %q:\n%s", w, overview)
			}
		}
	})

	t.Run("same_head", func(t *testing.T) {
		_, before := log.count()
		res := run(t)
		if n := len(llm.prompts()); n != 1 {
			t.Errorf("%d model calls in all, want only the first run's", n)
		}
		if _, after := log.count(); after != before {
			t.Errorf("%d write requests on the same head, want none", after-before)
		}
		if res.Incremental == nil || res.Incremental.Mode != review.IncrementalUnchanged || res.Publish != nil ||
			res.Metadata.LLMCalls != 0 {
			t.Errorf("incremental = %+v, publish = %+v, llm_calls %d", res.Incremental, res.Publish, res.Metadata.LLMCalls)
		}
		note := "Nothing changed since the last review (commit `" + pr.HeadSHA[:7] + "`); nothing was reviewed."
		if !slices.Contains(res.Notes, note) {
			t.Errorf("notes = %q", res.Notes)
		}
	})
}
