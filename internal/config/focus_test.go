package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// focusFile is an operator file with two custom focuses and two rules
// (X-51, v2.9 spec V-19 §1.1); the names and texts are generic.
const focusFile = `
[[review.custom_focuses]]
name = "api-contract"
instructions = """
Public contracts: request and response shapes.
DISTINCT-OPERATOR-INSTRUCTIONS-QX4"""

[[review.custom_focuses]]
name = "doc-standard"
instructions = "Documentation against the writing standard."
docs_style = true

[[review.focus_rules]]
paths = ["**/*.md", "**/docs/**"]
focus = "Doc-Standard"

[[review.focus_rules]]
paths = ["**/controller/**"]
focus = "security"
`

// TestFocusProfilesFromFile: both lists load from the file, the entries
// keep their order, the source is the operator, a rule's focus is lower
// case, and the load reports the file as their source.
func TestFocusProfilesFromFile(t *testing.T) {
	cfg, rep := mustLoad(t, withFile(minimalEnv(), focusFile))
	cf := cfg.Review.CustomFocuses
	if len(cf) != 2 || cf[0].Name != "api-contract" || cf[1].Name != "doc-standard" || cf[0].DocsStyle || !cf[1].DocsStyle {
		t.Fatalf("custom focuses %+v", cf)
	}
	if cf[0].Source != FocusSourceOperator || cf[1].Source != FocusSourceOperator {
		t.Errorf("sources %q %q", cf[0].Source, cf[1].Source)
	}
	if !strings.HasSuffix(cf[0].Instructions, "DISTINCT-OPERATOR-INSTRUCTIONS-QX4") || strings.HasPrefix(cf[0].Instructions, "\n") {
		t.Errorf("instructions %q", cf[0].Instructions)
	}
	fr := cfg.Review.FocusRules
	if len(fr) != 2 || fr[0].Focus != "doc-standard" || !slices.Equal(fr[0].Paths, []string{"**/*.md", "**/docs/**"}) || fr[1].Focus != "security" {
		t.Errorf("rules %+v", fr)
	}
	if rep.Sources[keyCustomFocuses] != OriginFile || rep.Sources[keyFocusRules] != OriginFile {
		t.Errorf("sources %v", rep.Sources)
	}
}

// TestFocusProfilesProblems: an invalid operator entry is a problem that
// names the key and the entry's index and never quotes the entry.
func TestFocusProfilesProblems(t *testing.T) {
	long := strings.Repeat("x", MaxCustomInstructionsBytes+1)
	cases := []struct{ name, file, want string }{
		{"reserved", "[[review.custom_focuses]]\nname = \"security\"\ninstructions = \"SECRET-TEXT-QX4\"\n",
			"review.custom_focuses[0]: name is reserved"},
		{"reserved impact", "[[review.custom_focuses]]\nname = \"impact\"\ninstructions = \"SECRET-TEXT-QX4\"\n",
			"review.custom_focuses[0]: name is reserved"},
		{"upper case", "[[review.custom_focuses]]\nname = \"Api-QX4\"\ninstructions = \"SECRET-TEXT-QX4\"\n",
			"review.custom_focuses[0]: name must be"},
		{"too short", "[[review.custom_focuses]]\nname = \"a\"\ninstructions = \"SECRET-TEXT-QX4\"\n",
			"review.custom_focuses[0]: name must be"},
		{"empty instructions", "[[review.custom_focuses]]\nname = \"area-qx4\"\ninstructions = \"  \"\n",
			"review.custom_focuses[0]: instructions must not be empty"},
		{"long instructions", "[[review.custom_focuses]]\nname = \"area-qx4\"\ninstructions = \"" + long + "\"\n",
			"review.custom_focuses[0]: instructions are longer than 1000 bytes"},
		{"control", "[[review.custom_focuses]]\nname = \"area-qx4\"\ninstructions = \"SECRET-TEXT-QX4\\u0007\"\n",
			"review.custom_focuses[0]: instructions must not contain control"},
		{"tab", "[[review.custom_focuses]]\nname = \"area-qx4\"\ninstructions = \"SECRET-TEXT-QX4\\tx\"\n",
			"review.custom_focuses[0]: instructions must not contain control"},
		{"bidi", "[[review.custom_focuses]]\nname = \"area-qx4\"\ninstructions = \"SECRET-TEXT-QX4\\u202e\"\n",
			"review.custom_focuses[0]: instructions must not contain control"},
		{"duplicate", "[[review.custom_focuses]]\nname = \"area-qx4\"\ninstructions = \"SECRET-TEXT-QX4\"\n" +
			"[[review.custom_focuses]]\nname = \"area-qx4\"\ninstructions = \"SECRET-TEXT-QX4\"\n",
			"review.custom_focuses[1]: name is already defined"},
		{"rule without paths", "[[review.focus_rules]]\npaths = []\nfocus = \"security\"\n",
			"review.focus_rules[0]: paths must hold 1 to 20 globs"},
		{"rule bad glob", "[[review.focus_rules]]\npaths = [\"[SECRET-TEXT-QX4\"]\nfocus = \"security\"\n",
			"review.focus_rules[0]: paths: not a valid glob"},
		{"rule unknown focus", "[[review.focus_rules]]\npaths = [\"**\"]\nfocus = \"area-qx4\"\n",
			"review.focus_rules[0]: focus must be a built-in focus or a custom focus"},
		{"unknown field", "[[review.custom_focuses]]\nname = \"area-qx4\"\ninstructions = \"x\"\ncolour = \"SECRET-TEXT-QX4\"\n",
			`unknown config key "review.custom_focuses.colour"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Load(withFile(minimalEnv(), tc.file))
			probs := problemsOf(t, err)
			joined := strings.Join(probs, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("problems %q, want %q", probs, tc.want)
			}
			if strings.Contains(joined, "SECRET-TEXT-QX4") || strings.Contains(joined, "Api-QX4") {
				t.Errorf("a problem quotes the entry: %q", probs)
			}
		})
	}
}

// TestFocusProfilesBounds: more than MaxCustomFocuses focuses or
// MaxFocusRules rules is a problem.
func TestFocusProfilesBounds(t *testing.T) {
	var b strings.Builder
	for i := range MaxCustomFocuses + 1 {
		b.WriteString("[[review.custom_focuses]]\nname = \"area-" + string(rune('a'+i)) + "\"\ninstructions = \"x\"\n")
	}
	_, _, err := Load(withFile(minimalEnv(), b.String()))
	if p := strings.Join(problemsOf(t, err), "\n"); !strings.Contains(p, "at most 10 custom focuses") {
		t.Errorf("problems %q", p)
	}
	b.Reset()
	for range MaxFocusRules + 1 {
		b.WriteString("[[review.focus_rules]]\npaths = [\"**\"]\nfocus = \"tests\"\n")
	}
	_, _, err = Load(withFile(minimalEnv(), b.String()))
	if p := strings.Join(problemsOf(t, err), "\n"); !strings.Contains(p, "at most 20 focus rules") {
		t.Errorf("problems %q", p)
	}
}

// TestFocusProfilesEmptyLists: empty lists are valid.
func TestFocusProfilesEmptyLists(t *testing.T) {
	cfg, _ := mustLoad(t, withFile(minimalEnv(), "[review]\ncustom_focuses = []\nfocus_rules = []\n"))
	if len(cfg.Review.CustomFocuses) != 0 || len(cfg.Review.FocusRules) != 0 {
		t.Errorf("%+v %+v", cfg.Review.CustomFocuses, cfg.Review.FocusRules)
	}
}

// TestReservedFocusNames: the built-in values and impact are reserved, and
// a valid name passes.
func TestReservedFocusNames(t *testing.T) {
	for _, n := range append(slices.Clone(BuiltinFocusNames), "impact") {
		if CustomFocusNameProblem(n) == "" {
			t.Errorf("%s is not reserved", n)
		}
	}
	for _, n := range []string{"api-contract", "ab", "a" + strings.Repeat("b", 31)} {
		if p := CustomFocusNameProblem(n); p != "" {
			t.Errorf("%s: %s", n, p)
		}
	}
	for _, n := range []string{"a" + strings.Repeat("b", 32), "1abc", "-abc", "ab_c", "ab c", ""} {
		if CustomFocusNameProblem(n) == "" {
			t.Errorf("%q accepted", n)
		}
	}
}

// TestFocusProfilesSummaryShowsNamesOnly: server_info's summary shows the
// names and the counts, never the instructions, also in a JSON dump of the
// configuration.
func TestFocusProfilesSummaryShowsNamesOnly(t *testing.T) {
	cfg, rep := mustLoad(t, withFile(minimalEnv(), focusFile))
	sum := cfg.Summary(rep)
	want := FocusProfilesSummary{CustomFocuses: []string{"api-contract", "doc-standard"}, CustomFocusCount: 2, FocusRuleCount: 2}
	if !reflect.DeepEqual(sum.FocusProfiles, want) {
		t.Errorf("summary %+v", sum.FocusProfiles)
	}
	for _, v := range []any{sum, cfg} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "DISTINCT-OPERATOR-INSTRUCTIONS-QX4") {
			t.Errorf("instructions in JSON: %s", b)
		}
	}
	empty := Defaults().Summary(nil).FocusProfiles
	if empty.CustomFocuses == nil || empty.CustomFocusCount != 0 || empty.FocusRuleCount != 0 {
		t.Errorf("empty summary %+v", empty)
	}
}

// TestFocusProfileFieldsMatchStructs: every file field of CustomFocus and
// FocusRule is a known field key, so neither can be added without the
// other.
func TestFocusProfileFieldsMatchStructs(t *testing.T) {
	for section, typ := range map[string]reflect.Type{keyCustomFocuses: reflect.TypeOf(CustomFocus{}), keyFocusRules: reflect.TypeOf(FocusRule{})} {
		var tags []string
		for i := range typ.NumField() {
			if tag := typ.Field(i).Tag.Get("toml"); tag != "-" {
				tags = append(tags, tag)
				if !isFocusProfileKey(section + "." + tag) {
					t.Errorf("%s.%s is not a known key", section, tag)
				}
			}
		}
		if len(tags) != len(focusProfileFields[section]) {
			t.Errorf("%s: fields %v, known %v", section, tags, focusProfileFields[section])
		}
	}
}
