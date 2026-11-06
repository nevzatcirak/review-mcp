package mcpserver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// rulesFile is a repository config with two custom focuses and two rules.
const rulesFile = `
[[review.custom_focuses]]
name = "api-contract"
instructions = "Public contracts: request shapes and status codes."

[[review.custom_focuses]]
name = "doc-standard"
instructions = "Documentation against the writing standard."
docs_style = true

[[review.focus_rules]]
paths = ["**/*.md", "docs/**"]
focus = "doc-standard"

[[review.focus_rules]]
paths = ["**/controller/**", "**/*Topic*.java"]
focus = "api-contract"
`

// whyLines are the lines under "Why these areas".
func whyLines(text string) []string {
	var out []string
	_, rest, _ := strings.Cut(text, "   Why these areas:\n")
	for _, l := range strings.Split(rest, "\n") {
		if !strings.HasPrefix(l, "   - ") {
			break
		}
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

// TestPromptRulesAddTheirFocuses: with rules, the focuses whose rules match
// a changed file are appended to the default list, each with its reason; the
// message holds counts, never file names.
func TestPromptRulesAddTheirFocuses(t *testing.T) {
	f := &promptFake{file: rulesFile, changed: []string{
		"docs/setup.md", "README.md", "src/controller/UserController.java", "src/Main.java"}}
	text := promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR})
	if got, want := gotFocuses(text), wantFocuses("security", "architecture", "performance", "tests", "docs", "doc-standard", "api-contract"); got != want {
		t.Fatalf("focuses = %s, want %s", got, want)
	}
	want := []string{
		"- security — default", "- architecture — default", "- performance — default", "- tests — default",
		"- docs — documentation files changed",
		"- doc-standard — rule 1 matched 2 files",
		"- api-contract — rule 2 matched 1 file",
	}
	if got := whyLines(text); !slices.Equal(got, want) {
		t.Errorf("why lines:\n%q\nwant\n%q", got, want)
	}
	for _, name := range f.changed {
		if strings.Contains(text, name) {
			t.Errorf("the message names the file %q", name)
		}
	}
	if f.diffs != 1 {
		t.Errorf("the changed files were listed %d times, want once", f.diffs)
	}
}

// TestPromptRuleAddsItsFocusOnce [canary g]: a rule whose globs match many
// files, or several rules for one focus, add the focus once.
func TestPromptRuleAddsItsFocusOnce(t *testing.T) {
	cfg := rulesFile + `
[[review.focus_rules]]
paths = ["**/*.java"]
focus = "api-contract"
`
	f := &promptFake{file: cfg, changed: []string{
		"docs/a.md", "docs/b.md", "docs/c.md", "README.md",
		"src/controller/A.java", "src/controller/B.java", "src/ATopic.java"}}
	text := promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR})
	names := strings.Split(gotFocuses(text), ",")
	for _, n := range []string{"doc-standard", "api-contract"} {
		if c := strings.Count(","+strings.Join(names, ",")+",", ","+n+","); c != 1 {
			t.Errorf("%s appears %d times in %v", n, c, names)
		}
	}
	if !slices.Contains(whyLines(text), "- doc-standard — rule 1 matched 4 files") ||
		!slices.Contains(whyLines(text), "- api-contract — rule 2 matched 3 files") {
		t.Errorf("why lines %q", whyLines(text))
	}
	if n := len(whyLines(text)); n != len(names) {
		t.Errorf("%d why lines for %d focuses", n, len(names))
	}
}

// TestPromptRulesSkipFilesTheIgnoreRulesDrop: a changed file the ignore rules
// drop does not trigger a rule.
func TestPromptRulesSkipFilesTheIgnoreRulesDrop(t *testing.T) {
	f := &promptFake{file: rulesFile, changed: []string{"vendor/lib/controller/x.go", "src/Main.java"}}
	text := promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR})
	if got := gotFocuses(text); got != wantFocuses(builtinDefault...) {
		t.Errorf("focuses = %s", got)
	}
}

// TestPromptWithoutRulesKeepsTheDefaultList [canary f]: without focus rules
// the list is what 2.8 gave for the same pull request, for any changed
// files, and the message is the golden text with its reasons.
func TestPromptWithoutRulesKeepsTheDefaultList(t *testing.T) {
	for _, tc := range []struct {
		name    string
		file    string
		changed []string
		want    []string
	}{
		{"code only", "", []string{"src/controller/UserController.java", "src/ATopic.java", "docs/x.sql"}, builtinDefault},
		{"docs changed", "", []string{"docs/setup.md", "src/controller/UserController.java"}, append(slices.Clone(builtinDefault), "docs")},
		{"customs defined, no rules", "[[review.custom_focuses]]\nname = \"api-contract\"\ninstructions = \"x\"\n",
			[]string{"src/controller/UserController.java"}, builtinDefault},
		{"empty rules", "[review]\nfocus_rules = []\n", []string{"src/controller/UserController.java", "README.md"}, append(slices.Clone(builtinDefault), "docs")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &promptFake{file: tc.file, changed: tc.changed}
			text := promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR})
			if got := gotFocuses(text); got != wantFocuses(tc.want...) {
				t.Errorf("focuses = %s, want %s", got, wantFocuses(tc.want...))
			}
			for _, l := range whyLines(text) {
				if !strings.HasSuffix(l, "default") && !strings.HasSuffix(l, "documentation files changed") {
					t.Errorf("a reason other than the default ones: %q", l)
				}
			}
			if strings.Contains(text, "cut at") || strings.Contains(text, "could not be read") {
				t.Errorf("message = %s", text)
			}
		})
	}
	// The full text for a pull request that changes documentation.
	f := &promptFake{changed: []string{"docs/setup.md"}}
	text := promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR})
	want := multiPerspectiveText(promptPR, planFocuses(promptContext{docsChanged: true}), false)
	if text != want {
		t.Errorf("message = %s\nwant %s", text, want)
	}
	if !strings.Contains(want, "   - docs — documentation files changed\n2. Merge") {
		t.Errorf("golden lacks the docs reason: %s", want)
	}
}

// TestPromptCapsTheListAtEight [canary h]: the default list is cut at 8 in
// its order, and the message names what was cut.
func TestPromptCapsTheListAtEight(t *testing.T) {
	var b strings.Builder
	b.WriteString("[review]\nfocuses = [\"security\", \"architecture\", \"performance\", \"ux\", \"tests\", \"docs\"]\n")
	for _, n := range []string{"area-a", "area-b", "area-c"} {
		b.WriteString("[[review.custom_focuses]]\nname = \"" + n + "\"\ninstructions = \"x\"\n")
	}
	for _, n := range []string{"area-a", "area-b", "area-c"} {
		b.WriteString("[[review.focus_rules]]\npaths = [\"**/*.go\"]\nfocus = \"" + n + "\"\n")
	}
	f := &promptFake{file: b.String(), changed: []string{"main.go"}}
	text := promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR})
	if got, want := gotFocuses(text), wantFocuses("security", "architecture", "performance", "ux", "tests", "docs", "area-a", "area-b"); got != want {
		t.Errorf("focuses = %s, want %s", got, want)
	}
	if !strings.Contains(text, "The list is cut at 8 areas; not included: area-c.\n") {
		t.Errorf("the cut is not named: %s", text)
	}
	if n := len(whyLines(text)); n != 8 {
		t.Errorf("%d why lines, want 8", n)
	}
}

// TestPromptFailedReadFallsBackWithoutRules: a failed listing of the changed
// files gives the built-in default, no rules, and says why.
func TestPromptFailedReadFallsBackWithoutRules(t *testing.T) {
	for name, f := range map[string]*promptFake{
		"listing":      {file: rulesFile, changed: []string{"docs/a.md"}, diffErr: &provider.Error{Class: provider.ClassUpstream}},
		"config read":  {fileErr: &provider.Error{Class: provider.ClassTransport}, changed: []string{"docs/a.md"}},
		"pull request": {file: rulesFile, changed: []string{"docs/a.md"}, prErr: &provider.Error{Class: provider.ClassAuth}},
	} {
		t.Run(name, func(t *testing.T) {
			text := promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR})
			if got := gotFocuses(text); got != wantFocuses(builtinDefault...) {
				t.Errorf("focuses = %s", got)
			}
			if !strings.Contains(text, "   The repository config could not be read; the default areas are used.\n") {
				t.Errorf("no failure sentence: %s", text)
			}
		})
	}
}

// TestPromptRulesNameReviewFocusesCustoms: review.focuses may name custom
// focuses; an undefined name makes the list invalid (the default is used).
func TestPromptReviewFocusesMayNameCustomFocuses(t *testing.T) {
	cfg := "[review]\nfocuses = [\"api-contract\", \"tests\"]\n" + `
[[review.custom_focuses]]
name = "api-contract"
instructions = "x"
`
	text := promptText(t, promptSession(t, &promptFake{file: cfg}), map[string]string{"pr_url": promptPR})
	if got := gotFocuses(text); got != "api-contract,tests" {
		t.Errorf("focuses = %s", got)
	}
	if !slices.Contains(whyLines(text), "- api-contract — review.focuses") {
		t.Errorf("why lines %q", whyLines(text))
	}
	undefined := "[review]\nfocuses = [\"api-contract\", \"tests\"]\n"
	text = promptText(t, promptSession(t, &promptFake{file: undefined}), map[string]string{"pr_url": promptPR})
	if got := gotFocuses(text); got != wantFocuses(builtinDefault...) {
		t.Errorf("focuses = %s", got)
	}
}

// TestPromptFocusesArgumentAcceptsCustomNames: a custom name the repository
// defines is accepted (one read), an unknown name is refused with the
// defined names listed, or with a statement that none is defined; the
// refusal never echoes a value.
func TestPromptFocusesArgumentAcceptsCustomNames(t *testing.T) {
	f := &promptFake{file: rulesFile}
	text := promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR, "focuses": "Doc-Standard, security"})
	if got := gotFocuses(text); got != "doc-standard,security" {
		t.Errorf("focuses = %s", got)
	}
	if !slices.Contains(whyLines(text), "- doc-standard — requested") || f.diffs != 0 {
		t.Errorf("why lines %q, listings %d", whyLines(text), f.diffs)
	}
	refuse := func(f *promptFake, focuses string) string {
		cs := promptSession(t, f)
		_, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "multi_perspective_review", Arguments: map[string]string{"pr_url": promptPR, "focuses": focuses}})
		var je *jsonrpc.Error
		if err == nil || !errors.As(err, &je) || je.Code != jsonrpc.CodeInvalidParams {
			t.Fatalf("err = %v, want InvalidParams", err)
		}
		return je.Message
	}
	if m := refuse(&promptFake{file: rulesFile}, "no-such-focus"); !strings.Contains(m, "custom focuses defined: api-contract, doc-standard") ||
		strings.Contains(m, "no-such-focus") {
		t.Errorf("message = %q", m)
	}
	if m := refuse(&promptFake{}, "no-such-focus"); !strings.Contains(m, "no custom focus is defined") {
		t.Errorf("message = %q", m)
	}
	if m := refuse(&promptFake{}, "security, two words"); !strings.Contains(m, "no custom focus is defined") {
		t.Errorf("message = %q", m)
	}
	// A built-in value reads nothing.
	f = &promptFake{file: rulesFile}
	promptText(t, promptSession(t, f), map[string]string{"pr_url": promptPR, "focuses": "tests"})
	if f.prCalls != 0 {
		t.Errorf("a built-in value read the pull request %d times", f.prCalls)
	}
}
