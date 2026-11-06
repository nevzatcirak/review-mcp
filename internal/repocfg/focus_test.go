package repocfg

import (
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

// focusOperator is operatorConfig with two operator focuses and one rule
// (X-51, v2.9 spec V-19 §1.2); names and texts are generic.
func focusOperator() *config.Config {
	c := operatorConfig()
	c.Review.CustomFocuses = []config.CustomFocus{
		{Name: "api-contract", Instructions: "OPERATOR-API-TEXT-QX4", Source: config.FocusSourceOperator},
		{Name: "messaging", Instructions: "OPERATOR-MSG-TEXT-QX4", Source: config.FocusSourceOperator},
	}
	c.Review.FocusRules = []config.FocusRule{{Paths: []string{"**/controller/**"}, Focus: "api-contract"}}
	return c
}

func applyFocusText(t *testing.T, content string) (*config.Config, *Outcome) {
	t.Helper()
	base := focusOperator()
	return base, Apply(base, readBase(t, content))
}

// TestApplyRepoFocusReplacesOperatorFocus (canary c): a repository
// definition with the name of an operator definition replaces it for the
// run, in place, with the note; the operator's configuration is unchanged.
func TestApplyRepoFocusReplacesOperatorFocus(t *testing.T) {
	base, o := applyFocusText(t, `
[[review.custom_focuses]]
name = "api-contract"
instructions = "REPO-API-TEXT-QX4"
docs_style = true

[[review.custom_focuses]]
name = "doc-standard"
instructions = "REPO-DOC-TEXT-QX4"
`)
	cf := o.Config.Review.CustomFocuses
	if len(cf) != 3 {
		t.Fatalf("custom focuses %+v", cf)
	}
	if cf[0].Name != "api-contract" || cf[0].Instructions != "REPO-API-TEXT-QX4" || !cf[0].DocsStyle ||
		cf[0].Source != config.FocusSourceRepoConfig {
		t.Errorf("the repository's definition does not win: %+v", cf[0])
	}
	if cf[1].Name != "messaging" || cf[1].Source != config.FocusSourceOperator ||
		cf[2].Name != "doc-standard" || cf[2].Source != config.FocusSourceRepoConfig {
		t.Errorf("order or sources: %+v", cf)
	}
	if !slices.Contains(o.Notes, "The repository config redefines the focus `api-contract`.") {
		t.Errorf("notes %q", o.Notes)
	}
	if base.Review.CustomFocuses[0].Instructions != "OPERATOR-API-TEXT-QX4" || len(base.Review.CustomFocuses) != 2 {
		t.Errorf("the operator's configuration changed: %+v", base.Review.CustomFocuses)
	}
	if !slices.Contains(o.Result.Applied, "review.custom_focuses") {
		t.Errorf("applied %q", o.Result.Applied)
	}
	if text := strings.Join(o.Notes, "\n"); strings.Contains(text, "TEXT-QX4") {
		t.Errorf("a note quotes instructions: %s", text)
	}
}

// TestApplyEmptyRulesTurnOperatorRulesOff (canary d): review.focus_rules =
// [] in the repository turns the operator's rules off; rules never merge
// across sources.
func TestApplyEmptyRulesTurnOperatorRulesOff(t *testing.T) {
	base, o := applyFocusText(t, "[review]\nfocus_rules = []\n")
	if o.Config.Review.FocusRules == nil || len(o.Config.Review.FocusRules) != 0 {
		t.Errorf("rules %+v: the operator's rules are still on", o.Config.Review.FocusRules)
	}
	if len(base.Review.FocusRules) != 1 || !slices.Equal(o.Result.Applied, []string{"review.focus_rules"}) {
		t.Errorf("base %+v, applied %q", base.Review.FocusRules, o.Result.Applied)
	}

	_, o = applyFocusText(t, "[[review.focus_rules]]\npaths = [\"**/*.md\"]\nfocus = \"docs\"\n")
	if want := []config.FocusRule{{Paths: []string{"**/*.md"}, Focus: "docs"}}; !slices.EqualFunc(o.Config.Review.FocusRules, want, ruleEqual) {
		t.Errorf("rules %+v: the repository's rules do not replace the operator's", o.Config.Review.FocusRules)
	}

	_, o = applyFocusText(t, "")
	if len(o.Config.Review.FocusRules) != 1 || o.Config.Review.FocusRules[0].Focus != "api-contract" {
		t.Errorf("without the key the operator's rules apply: %+v", o.Config.Review.FocusRules)
	}
}

func ruleEqual(a, b config.FocusRule) bool {
	return a.Focus == b.Focus && slices.Equal(a.Paths, b.Paths)
}

// TestApplyReservedRepoFocusIsIgnored (canary a, configuration half): a
// repository focus named after a built-in is ignored with the invalid-entry
// note naming its index; the valid entries are kept.
func TestApplyReservedRepoFocusIsIgnored(t *testing.T) {
	_, o := applyFocusText(t, `
[[review.custom_focuses]]
name = "security"
instructions = "REPO-SEC-TEXT-QX4"

[[review.custom_focuses]]
name = "doc-standard"
instructions = "REPO-DOC-TEXT-QX4"
`)
	for _, f := range o.Config.Review.CustomFocuses {
		if f.Name == "security" {
			t.Fatalf("a reserved name was accepted: %+v", o.Config.Review.CustomFocuses)
		}
	}
	if !slices.Contains(o.Notes, "The value of review.custom_focuses[0] in `.review-mcp.toml` is not valid and was ignored.") ||
		!slices.Contains(o.Result.Ignored, "review.custom_focuses[0]") {
		t.Errorf("notes %q, ignored %q", o.Notes, o.Result.Ignored)
	}
	if n := len(o.Config.Review.CustomFocuses); n != 3 {
		t.Errorf("%d focuses", n)
	}
}

// TestApplyFocusEntriesInvalid: each invalid entry is ignored on its own
// and named by its index, never quoted.
func TestApplyFocusEntriesInvalid(t *testing.T) {
	var many strings.Builder
	for i := range config.MaxCustomFocuses + 1 {
		many.WriteString("[[review.custom_focuses]]\nname = \"area-" + string(rune('a'+i)) + "\"\ninstructions = \"x\"\n")
	}
	var rules strings.Builder
	for range config.MaxFocusRules + 1 {
		rules.WriteString("[[review.focus_rules]]\npaths = [\"**\"]\nfocus = \"tests\"\n")
	}
	for _, tc := range []struct {
		name, file string
		ignored    []string
	}{
		{"name syntax", "[[review.custom_focuses]]\nname = \"Bad Name QX4\"\ninstructions = \"x\"\n", []string{"review.custom_focuses[0]"}},
		{"bidi", "[[review.custom_focuses]]\nname = \"area-qx\"\ninstructions = \"x\\u202eQX4\"\n", []string{"review.custom_focuses[0]"}},
		{"too long", "[[review.custom_focuses]]\nname = \"area-qx\"\ninstructions = \"" + strings.Repeat("y", 1001) + "\"\n", []string{"review.custom_focuses[0]"}},
		{"unknown field", "[[review.custom_focuses]]\nname = \"area-qx\"\ninstructions = \"x\"\nextra = \"QX4\"\n", []string{"review.custom_focuses[0]"}},
		{"wrong type", "[[review.custom_focuses]]\nname = \"area-qx\"\ninstructions = \"x\"\ndocs_style = \"QX4\"\n", []string{"review.custom_focuses[0]"}},
		{"duplicate", "[[review.custom_focuses]]\nname = \"area-qx\"\ninstructions = \"x\"\n[[review.custom_focuses]]\nname = \"area-qx\"\ninstructions = \"QX4\"\n", []string{"review.custom_focuses[1]"}},
		{"not tables", "[review]\ncustom_focuses = \"QX4\"\n", []string{"review.custom_focuses"}},
		{"eleventh", many.String(), []string{"review.custom_focuses[10]"}},
		{"rule unknown focus", "[[review.focus_rules]]\npaths = [\"**\"]\nfocus = \"no-such-qx4\"\n", []string{"review.focus_rules[0]"}},
		{"rule bad glob", "[[review.focus_rules]]\npaths = [\"a/[QX4\"]\nfocus = \"tests\"\n", []string{"review.focus_rules[0]"}},
		{"rule no paths", "[[review.focus_rules]]\npaths = []\nfocus = \"tests\"\n", []string{"review.focus_rules[0]"}},
		{"twenty-first rule", rules.String(), []string{"review.focus_rules[20]"}},
		{"style file", "[review]\ndocs_style_file = \"../QX4.md\"\n", []string{"review.docs_style_file"}},
		{"style file long", "[review]\ndocs_style_file = \"" + strings.Repeat("d", 201) + "\"\n", []string{"review.docs_style_file"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, o := applyFocusText(t, tc.file)
			if !slices.Equal(o.Result.Ignored, tc.ignored) {
				t.Errorf("ignored %q, want %q", o.Result.Ignored, tc.ignored)
			}
			if text := strings.Join(o.Notes, "\n"); strings.Contains(text, "QX4") {
				t.Errorf("a note quotes the file: %s", text)
			}
		})
	}
}

// TestApplyRepoRuleSeesOperatorAndRepoFocuses: a repository rule may name
// a built-in, its own custom focus or the operator's.
func TestApplyRepoRuleSeesOperatorAndRepoFocuses(t *testing.T) {
	_, o := applyFocusText(t, `
[[review.custom_focuses]]
name = "doc-standard"
instructions = "x"

[[review.focus_rules]]
paths = ["**/*.md"]
focus = "doc-standard"

[[review.focus_rules]]
paths = ["**/topic/**"]
focus = "Messaging"

[[review.focus_rules]]
paths = ["**/*_test.go"]
focus = "tests"
`)
	got := o.Config.Review.FocusRules
	if len(got) != 3 || got[0].Focus != "doc-standard" || got[1].Focus != "messaging" || got[2].Focus != "tests" || len(o.Result.Ignored) != 0 {
		t.Errorf("rules %+v, ignored %q", got, o.Result.Ignored)
	}
}

// TestApplyDocsStyleFile: a valid path is kept on the run's copy.
func TestApplyDocsStyleFile(t *testing.T) {
	base, o := applyFocusText(t, "[review]\ndocs_style_file = \"docs/style.md\"\n")
	if o.Config.Review.DocsStyleFile != "docs/style.md" || base.Review.DocsStyleFile != "" ||
		!slices.Equal(o.Result.Applied, []string{"review.docs_style_file"}) {
		t.Errorf("file %q, applied %q", o.Config.Review.DocsStyleFile, o.Result.Applied)
	}
}
