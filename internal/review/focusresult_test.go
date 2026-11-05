package review

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

// The focus of a result and of a merged publish (X-51, v2.9 spec V-19 §2.3).

// TestResultNamesTheFocusAndItsSource: focus holds the name, focus_source
// where it comes from; both are absent without a focus.
func TestResultNamesTheFocusAndItsSource(t *testing.T) {
	for _, tc := range []struct {
		name, focus, source, want string
		repoConfig                string
		operator                  bool
	}{
		{name: "builtin", focus: "Security", want: "security", source: "builtin"},
		{name: "docs", focus: "docs", want: "docs", source: "builtin"},
		{name: "operator custom", focus: "API-Contract", want: "api-contract", source: "operator", operator: true},
		{name: "repository custom", focus: "api-contract", want: "api-contract", source: "repo_config",
			repoConfig: "[[review.custom_focuses]]\nname = \"api-contract\"\ninstructions = \"Contracts.\"\n"},
		{name: "repository redefines operator", focus: "api-contract", want: "api-contract", source: "repo_config", operator: true,
			repoConfig: "[[review.custom_focuses]]\nname = \"api-contract\"\ninstructions = \"Contracts.\"\n"},
		{name: "free text", focus: "locking in the cache", want: "locking in the cache", source: "free_text"},
		{name: "undefined name", focus: "message-queue", want: "message-queue", source: "free_text"},
		{name: "no focus", focus: "", want: "", source: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(goodAnswer)
			if tc.operator {
				h.deps.Config.Review.CustomFocuses = []config.CustomFocus{apiContract()}
			}
			if tc.repoConfig != "" {
				withRepoConfig(h, tc.repoConfig)
			}
			res := prepareWith(t, h, Args{Focus: tc.focus}).Result
			if res.Focus != tc.want || res.FocusSource != tc.source {
				t.Errorf("focus %q, focus_source %q; want %q, %q", res.Focus, res.FocusSource, tc.want, tc.source)
			}
			b, _ := json.Marshal(res)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			_, hasFocus := m["focus"]
			_, hasSource := m["focus_source"]
			if hasFocus != (tc.focus != "") || hasSource != (tc.focus != "") {
				t.Errorf("JSON has focus %v, focus_source %v", hasFocus, hasSource)
			}
		})
	}
}

// TestFocusSourceValuesMatchTheSchema: the schema enum lists exactly the
// values the code can produce.
func TestFocusSourceValuesMatchTheSchema(t *testing.T) {
	props := ResultSchema()["properties"].(map[string]any)
	fs, ok := props["focus_source"].(map[string]any)
	if !ok {
		t.Fatal("focus_source is not declared")
	}
	var got []string
	for _, v := range fs["enum"].([]any) {
		got = append(got, v.(string))
	}
	if !slices.Equal(got, []string{"builtin", "operator", "repo_config", "free_text"}) {
		t.Errorf("enum %q", got)
	}
	if req, _ := ResultSchema()["required"].([]any); slices.Contains(req, any("focus_source")) {
		t.Error("focus_source is required; it is absent without a focus")
	}
}

// TestSuppliedFindingsAcceptCustomFocusNames: a merged publish never rejects
// a custom name of the repository; it is stored in lower case. Free text up
// to the limit passes as before, and an invalid focus is still dropped.
func TestSuppliedFindingsAcceptCustomFocusNames(t *testing.T) {
	h := newSuppliedHarness(t)
	withRepoConfig(h, "[[review.custom_focuses]]\nname = \"api-contract\"\ninstructions = \"Contracts.\"\n")
	h.deps.Config.Review.CustomFocuses = []config.CustomFocus{{Name: "msg-layer", Instructions: "Messaging.", Source: config.FocusSourceOperator}}
	f := func(line int, focus string) SuppliedFinding {
		s := supplied("src/app.go", line, line, "Header "+strings.Repeat("x", line), "Content.")
		s.Focus = focus
		return s
	}
	findings := []SuppliedFinding{
		f(11, "API-Contract"),
		f(12, "msg-layer"),
		f(13, "not defined anywhere"),
		f(14, strings.Repeat("a", MaxFocusRunes)),
		f(15, strings.Repeat("a", MaxFocusRunes+1)), // too long: dropped
		f(16, "bad\nfocus"),                         // dropped
		f(17, "Security"),
	}
	res, err := Run(t.Context(), h.deps, Args{PRURL: testPRURL, Publish: true, Findings: findings})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range res.Review.KeyIssuesToReview {
		got = append(got, k.Focus)
	}
	want := []string{"security", "msg-layer", "api-contract", "not defined anywhere", strings.Repeat("a", MaxFocusRunes)}
	if !slices.Equal(got, want) {
		t.Errorf("focuses in the overview's order = %q\nwant %q", got, want)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "2 supplied findings were dropped") {
		t.Errorf("notes %q", res.Notes)
	}
}

// TestSuppliedFindingsAreGroupedByFocus: the findings are ordered by group:
// no focus first, the built-in values in the order of the prompt's default
// list, the custom focuses in their merged order (the operator's first),
// then other texts as they first appear; the order inside a group is kept.
func TestSuppliedFindingsAreGroupedByFocus(t *testing.T) {
	customs := []config.CustomFocus{{Name: "api-contract"}, {Name: "doc-standard"}}
	mk := func(focus, header string) KeyIssue { return KeyIssue{Focus: focus, IssueHeader: header} }
	issues := []KeyIssue{
		mk("doc-standard", "1"), mk("tests", "2"), mk("", "3"), mk("zeta text", "4"), mk("api-contract", "5"),
		mk("security", "6"), mk("doc-standard", "7"), mk("alpha text", "8"), mk("security", "9"), mk("zeta text", "10"),
	}
	groupByFocus(issues, customs)
	var got []string
	for _, k := range issues {
		got = append(got, k.IssueHeader)
	}
	if want := []string{"3", "6", "9", "2", "5", "1", "7", "4", "10", "8"}; !slices.Equal(got, want) {
		t.Errorf("order %q, want %q", got, want)
	}
}
