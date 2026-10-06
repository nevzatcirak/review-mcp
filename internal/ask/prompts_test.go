package ask

import (
	"os"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/prompt"
)

func sampleInput() PromptInput {
	return PromptInput{
		ExtraInstructions: "Be brief.",
		Language:          "en-US",
		MainLanguage:      "Go",
		Title:             "Retry requests",
		Branch:            "feature/retry",
		Description:       "  Adds a retry.  \n",
		Question:          "  Is it bounded?\n",
		Diff:              "## File: 'a.go'\n+x",
	}
}

// TestTemplatesFailOnMissingKey [canary]: the real ask templates run with
// missingkey=error, so a variable the caller forgets to set fails the
// render instead of printing "<no value>".
func TestTemplatesFailOnMissingKey(t *testing.T) {
	in := sampleInput()
	full := in.vars()
	for _, name := range []string{systemTemplate, userTemplate} {
		if _, err := prompt.Execute(templates, name, full); err != nil {
			t.Fatalf("%s with every key: %v", name, err)
		}
	}
	// Every key the templates use must be required: dropping it fails the
	// template that uses it.
	for key := range full {
		vars := map[string]any{}
		for k, v := range full {
			if k != key {
				vars[k] = v
			}
		}
		failed := 0
		for _, name := range []string{systemTemplate, userTemplate} {
			if _, err := prompt.Execute(templates, name, vars); err != nil {
				failed++
			}
		}
		if failed == 0 {
			t.Errorf("no template fails without the %q key", key)
		}
	}
	if _, err := prompt.Execute(templates, userTemplate, map[string]any{}); err == nil {
		t.Error("the user template rendered with no variables")
	}
}

func TestRenderPrompts(t *testing.T) {
	p, err := RenderPrompts(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.System+p.User, "<no value>") {
		t.Error("<no value> in the prompts")
	}
	for _, want := range []string{
		"Title: 'Retry requests'", "Branch: 'feature/retry'",
		"Description:\n======\nAdds a retry.\n======",
		"Main PR language: 'Go'",
		"The PR Git Diff:\n======\n## File: 'a.go'\n+x\n======\n",
		"'-' for deletions, '+' for additions, and ' ' (a space) for unchanged lines",
		"The PR Questions:\n======\nIs it bounded?\n======\n\nResponse to the PR Questions:",
	} {
		if !strings.Contains(p.User, want) {
			t.Errorf("user prompt lacks %q", want)
		}
	}
	if !strings.HasSuffix(p.User, "Response to the PR Questions:") {
		t.Error("the user prompt does not end with the closing line")
	}
	for _, want := range []string{
		"Answer only from the PR information and diff provided. If the answer cannot be determined from them, say so explicitly and state what information is missing. Do not guess.",
		"omitted because of its size; do not draw conclusions about the content of those files.",
		"Extra instructions from the user:\n======\nBe brief.\n======\nFollow the extra instructions above; they take precedence over any conflicting guidance in this prompt.",
	} {
		if !strings.Contains(p.System, want) {
			t.Errorf("system prompt lacks %q", want)
		}
	}
	// The upstream sentence that demanded an answer is gone, and so are the
	// removed blocks.
	for _, gone := range []string{"You must answer the questions", "skills", "Previous discussion", "conversation"} {
		if strings.Contains(p.System+p.User, gone) {
			t.Errorf("prompts still contain %q", gone)
		}
	}
}

func TestMainLanguageOmitted(t *testing.T) {
	for _, lang := range []string{"", "Other"} {
		in := sampleInput()
		in.MainLanguage = lang
		p, err := RenderPrompts(in)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.User, "Main PR language") {
			t.Errorf("main language %q not omitted", lang)
		}
	}
}

func TestOutputLanguageGoesThroughSharedHelper(t *testing.T) {
	in := sampleInput()
	in.Language = "tr-TR"
	p, err := RenderPrompts(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := prompt.LanguageInstruction("tr-TR"); !strings.Contains(p.System, want) {
		t.Error("the shared output-language instruction is missing")
	}
	in.ExtraInstructions = ""
	in.Language = "en-US"
	if p, _ = RenderPrompts(in); strings.Contains(p.System, "Extra instructions") {
		t.Error("an empty extra-instructions block was rendered")
	}
}

func TestScaffoldingIncludesQuestion(t *testing.T) {
	in := sampleInput()
	short, err := ScaffoldingTokens(in, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	in.Question = strings.Repeat("a long question ", 200)
	long, err := ScaffoldingTokens(in, 0.3)
	if err != nil {
		t.Fatal(err)
	}
	if long <= short {
		t.Errorf("the question is not part of the scaffolding: %d vs %d", long, short)
	}
}

// TestTemplateAttribution: both templates name the upstream file and
// point to NOTICE, and NOTICE lists them (X-7).
func TestTemplateAttribution(t *testing.T) {
	notice, err := os.ReadFile("../../NOTICE")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"system.tmpl", "user.tmpl"} {
		b, err := promptFiles.ReadFile("prompts/" + name)
		if err != nil {
			t.Fatal(err)
		}
		head := string(b[:min(len(b), 700)])
		for _, want := range []string{"pr_agent/settings/pr_questions_prompts.toml @ 8e5a929", "see NOTICE"} {
			if !strings.Contains(head, want) {
				t.Errorf("%s header lacks %q", name, want)
			}
		}
		if !strings.Contains(string(notice), "- internal/ask/prompts/"+name) {
			t.Errorf("NOTICE does not list %s", name)
		}
	}
	if !strings.Contains(string(notice), "internal/ask/testdata/prompts/") {
		t.Error("NOTICE does not list the ask prompt goldens")
	}
	sys, _ := promptFiles.ReadFile("prompts/system.tmpl")
	if !strings.Contains(string(sys), "HONESTY DEVIATION") {
		t.Error("the system template does not cite the honesty deviation")
	}
}

// TestHonestyInstructionReplacesMustAnswer [canary]: upstream's two-sentence
// must-answer instruction is gone, and the spec's grounding sentence is
// present verbatim, directly after the "Be informative" line.
func TestHonestyInstructionReplacesMustAnswer(t *testing.T) {
	p, err := RenderPrompts(sampleInput())
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"Don't avoid answering", "You must answer"} {
		if strings.Contains(p.System, gone) {
			t.Errorf("system prompt still contains %q", gone)
		}
	}
	const want = "Try to be as specific as possible.\n" +
		"Answer only from the PR information and diff provided. If the answer cannot be determined from them, " +
		"say so explicitly and state what information is missing. Do not guess.\n" +
		"The diff may list files that were omitted"
	if !strings.Contains(p.System, want) {
		t.Errorf("system prompt lacks the grounding sentence verbatim:\n%s", p.System)
	}
}
