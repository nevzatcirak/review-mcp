package issuectx

import (
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

func TestValidate(t *testing.T) {
	ok := func(mod func(*Input)) []Input {
		in := Input{Key: "PROJ-1", Title: "Retry sign-in"}
		if mod != nil {
			mod(&in)
		}
		return []Input{in}
	}
	const quoted = "VALUE-NEVER-QUOTED"
	bad := []struct {
		name string
		in   []Input
		want string
	}{
		{"four issues", append(append(ok(nil), ok(nil)...), append(ok(nil), ok(nil)...)...), "issue_context must hold 1 to 3 issues"},
		{"no key", ok(func(i *Input) { i.Key = " \x00 " }), "issue_context[1].key is required"},
		{"long key", ok(func(i *Input) { i.Key = quoted + strings.Repeat("k", 60) }), "issue_context[1].key must be at most 64 characters"},
		{"no title", ok(func(i *Input) { i.Title = "" }), "issue_context[1].title is required"},
		{"long title", ok(func(i *Input) { i.Title = quoted + strings.Repeat("é", 300) }), "issue_context[1].title must be at most 300 characters"},
		{"long description", ok(func(i *Input) { i.Description = quoted + strings.Repeat("d", 20000) }),
			"issue_context[1].description must be at most 20000 characters"},
		{"criteria of another type", ok(func(i *Input) { i.AcceptanceCriteria = 3 }),
			"issue_context[1].acceptance_criteria must be a string or a list of strings"},
		{"criteria list with a number", ok(func(i *Input) { i.AcceptanceCriteria = []any{"a", 1} }),
			"issue_context[1].acceptance_criteria must be a string or a list of strings"},
		{"long criteria in total", ok(func(i *Input) {
			i.AcceptanceCriteria = []any{quoted + strings.Repeat("a", 5000), strings.Repeat("b", 5000)}
		}), "issue_context[1].acceptance_criteria must be at most 10000 characters in total"},
		{"javascript url", ok(func(i *Input) { i.URL = "javascript:alert('" + quoted + "')" }), "issue_context[1].url must be an http or https URL"},
		{"url with credentials", ok(func(i *Input) { i.URL = "https://user:" + quoted + "@tracker.example/x" }),
			"issue_context[1].url must be an http or https URL"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			_, err := Validate(c.in)
			if err == nil || err.Error() != c.want {
				t.Fatalf("error %v, want %q", err, c.want)
			}
			if strings.Contains(err.Error(), quoted) {
				t.Error("the error quotes the value")
			}
		})
	}
	t.Run("second item", func(t *testing.T) {
		in := append(ok(nil), Input{Key: "PROJ-2"})
		if _, err := Validate(in); err == nil || err.Error() != "issue_context[2].title is required" {
			t.Errorf("error %v", err)
		}
	})

	got, err := Validate([]Input{{Key: " PROJ-7\x1b ", Title: "Retry\nsign-in\u202e", Description: "Line one\r\nLine\x07 two\n",
		AcceptanceCriteria: []any{" retries once ", "", "shows\nan error"}, URL: "https://tracker.example/browse/PROJ-7"},
		{Key: "OPS-1", Title: "One", AcceptanceCriteria: "a single criterion"}})
	if err != nil {
		t.Fatal(err)
	}
	if g := got[0]; g.Ref != "PROJ-7" || g.Title != "Retry sign-in" || g.Description != "Line one\nLine two" ||
		!slices.Equal(g.Criteria, []string{"retries once", "shows\nan error"}) || g.URL != "https://tracker.example/browse/PROJ-7" {
		t.Errorf("cleaned = %+v", g)
	}
	if !slices.Equal(got[1].Criteria, []string{"a single criterion"}) {
		t.Errorf("a string criterion: %q", got[1].Criteria)
	}
	for _, in := range [][]Input{nil, {}} {
		if got, err := Validate(in); got != nil || err != nil {
			t.Errorf("Validate(%v) = %v, %v; want not given", in, got, err)
		}
	}
	if got, err := Validate(ok(func(i *Input) { i.Key = strings.Repeat("k", 64) })); err != nil || len(got) != 1 {
		t.Errorf("a key of exactly 64 characters: %v", err)
	}
}

func TestRenderBudget(t *testing.T) {
	long := ""
	for i := range 200 {
		long += "Line " + strings.Repeat("x", i%7) + " of the description.\n"
	}
	c := &Context{Source: SourceArgument}
	issues, _ := Validate([]Input{
		{Key: "A-1", Title: "Short", Description: "Do one thing.", AcceptanceCriteria: []any{"one"}},
		{Key: "B-2", Title: "Long", Description: long, AcceptanceCriteria: []any{"two"}},
	})
	for i, is := range issues {
		is.ref = i
		c.Refs = append(c.Refs, Ref{Ref: is.Ref, Source: RefArgument, Outcome: OutcomeUsed})
		c.Issues = append(c.Issues, is)
	}
	block := c.Render(400, 1)
	if !strings.HasPrefix(block, Header+"\n```") {
		t.Fatalf("block does not start with the header and a fence:\n%s", block[:80])
	}
	if !strings.Contains(block, "Issue A-1: Short\nDescription:\nDo one thing.\nAcceptance criteria:\n- one") {
		t.Errorf("the short issue is not whole:\n%s", block)
	}
	if !strings.Contains(block, descriptionCutLine) || !strings.Contains(block, "Acceptance criteria:\n- two") {
		t.Errorf("the long issue's description is not cut before its criteria:\n%s", block)
	}
	if c.Refs[0].Outcome != OutcomeUsed || c.Refs[1].Outcome != OutcomeCut || !slices.Equal(c.Notes, []string{NoteCut}) {
		t.Errorf("refs %+v, notes %q", c.Refs, c.Notes)
	}
	for _, is := range c.Issues {
		if text, _ := is.fit(200, 1); tokens.Estimate(text, 1) > 200 {
			t.Errorf("issue %s takes %d tokens, over its share of 200", is.Ref, tokens.Estimate(text, 1))
		}
	}
	if None().Render(400, 1) != "" || c.Render(0, 1) != "" {
		t.Error("a block without issues or without a budget")
	}
}

func TestRenderCutsCriteriaLast(t *testing.T) {
	var crit []any
	for range 50 {
		crit = append(crit, "A criterion that takes some room in the prompt.")
	}
	issues, _ := Validate([]Input{{Key: "C-3", Title: "Criteria", Description: "Short.", AcceptanceCriteria: crit}})
	c := &Context{Refs: []Ref{{Ref: "C-3"}}, Issues: issues}
	block := c.Render(150, 1)
	if !strings.Contains(block, criteriaCutLine) || !strings.Contains(block, "Issue C-3: Criteria") ||
		!strings.Contains(block, descriptionCutLine) {
		t.Errorf("block:\n%s", block)
	}
}
