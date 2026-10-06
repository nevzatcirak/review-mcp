package review

import (
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/prompt"
)

func sampleInput() PromptInput {
	return PromptInput{
		Toggles: allOn, MaxFindings: 3, Title: "T", Branch: "b", Description: "d", Date: "2026-10-06", Diff: "DIFF",
	}
}

// TestPromptTemplatesMissingKey is the [canary] of spec P4 §4.2 on the real
// templates: dropping any variable the templates use makes rendering fail.
func TestPromptTemplatesMissingKey(t *testing.T) {
	in := sampleInput()
	base := in.vars()
	if _, err := renderWith(templates, base); err != nil {
		t.Fatal(err)
	}
	for key := range base {
		vars := map[string]any{}
		for k, v := range base {
			if k != key {
				vars[k] = v
			}
		}
		if p, err := renderWith(templates, vars); err == nil {
			t.Errorf("rendering without %q succeeded (system has <no value>: %v, user: %v)", key,
				strings.Contains(p.System, "<no value>"), strings.Contains(p.User, "<no value>"))
		}
	}
}

func TestPromptShape(t *testing.T) {
	p, err := RenderPrompts(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.System, "Adapted from") || strings.Contains(p.User, "Adapted from") {
		t.Errorf("template header comment leaked into a prompt")
	}
	if !strings.HasPrefix(p.System, "You are PR-Reviewer") {
		t.Errorf("system prompt starts with %q", p.System[:min(40, len(p.System))])
	}
	// DQ-7: the user prompt ends with the response line and an open fence.
	if !strings.HasSuffix(p.User, "\n"+ResponseLine+"\n```yaml") {
		t.Errorf("user prompt tail = %q", p.User[max(0, len(p.User)-80):])
	}
	if strings.Count(p.User, ResponseLine) != 1 {
		t.Errorf("response line count = %d", strings.Count(p.User, ResponseLine))
	}
	if !strings.Contains(p.User, "Today's Date: 2026-10-06\n") {
		t.Errorf("date missing")
	}
	if !strings.Contains(p.User, "The PR code diff:\n======\nDIFF\n======") {
		t.Errorf("diff block missing")
	}
	if strings.Contains(p.System, "Extra instructions from the user") {
		t.Errorf("empty extra instructions rendered a block")
	}
}

// TestNonEnglishKeepsKeys: a non-English output language adds upstream's
// instruction to the extra instructions and leaves the schema and example
// keys in English.
func TestNonEnglishKeepsKeys(t *testing.T) {
	in := sampleInput()
	en, err := RenderPrompts(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Language = "tr-TR"
	tr, err := RenderPrompts(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.System, "Extra instructions from the user:\n======\n"+prompt.LanguageInstruction("tr-TR")+"\n======") {
		t.Errorf("language instruction block missing")
	}
	if strings.Replace(tr.System, "\n\n\nExtra instructions from the user:\n======\n"+prompt.LanguageInstruction("tr-TR")+"\n======\n", "", 1) != en.System {
		t.Errorf("a non-English language changed more than the extra instructions")
	}
	if tr.User != en.User {
		t.Errorf("the user prompt must not depend on the output language")
	}
}

func TestEmptyDescriptionOmitsBlock(t *testing.T) {
	in := sampleInput()
	in.Description = ""
	p, err := RenderPrompts(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.User, "PR Description:") || !strings.Contains(p.User, "Branch: 'b'\n\n\nThe PR code diff:") {
		t.Errorf("user prompt = %q", p.User)
	}
	in.Description = "  \n x \n "
	p, _ = RenderPrompts(in)
	if !strings.Contains(p.User, "PR Description:\n======\nx\n======") {
		t.Errorf("description not trimmed: %q", p.User)
	}
}
