package repocfg

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

// operatorConfig is a fresh process configuration with a few operator
// choices; two calls give two independent, equal values.
func operatorConfig() *config.Config {
	c := config.Defaults()
	c.LLM.BaseURL = "https://llm.example/v1"
	c.LLM.Model = "operator-model"
	c.LLM.Temperature = new(float64)
	c.LLM.Fallback = []config.LLMFallback{{Model: "fallback-model", BaseURL: "https://fallback.example/v1"}}
	c.Ignore.Glob = []string{"vendor/**", "dist/**"}
	c.Ignore.Regex = []string{`\.min\.js$`}
	c.Review.MaxFindings = 5
	c.Review.MaxTotalFindings = 12
	c.Review.ExtraInstructions = "operator guidance"
	c.Ask.ExtraInstructions = "operator ask guidance"
	c.Improve.MaxSuggestions = 8
	c.Improve.MinScore = 6
	c.Serve.AllowedOrigins = []string{"https://app.example"}
	return c
}

func applyText(t *testing.T, content string) (*config.Config, *Outcome) {
	t.Helper()
	base := operatorConfig()
	return base, Apply(base, readBase(t, content))
}

func TestApplyAbsentAndOff(t *testing.T) {
	base := operatorConfig()
	for name, f := range map[string]*File{
		"nil":    nil,
		"absent": {path: ".review-mcp.toml", state: stateAbsent},
	} {
		o := Apply(base, f)
		if o.Result.Status != StatusAbsent || len(o.Result.Applied) != 0 || len(o.Result.Ignored) != 0 || len(o.Notes) != 0 {
			t.Errorf("%s: %+v notes %q", name, o.Result, o.Notes)
		}
		if o.Result.Applied == nil || o.Result.Ignored == nil {
			t.Errorf("%s: the result lists must always be set", name)
		}
		if !reflect.DeepEqual(o.Config, base) || o.Config == base {
			t.Errorf("%s: want an equal, separate copy of the configuration", name)
		}
	}
	off := Apply(base, &File{path: ".review-mcp.toml", state: stateOff})
	if off.Result.Status != StatusOff || off.Result.Path != ".review-mcp.toml" || len(off.Notes) != 0 {
		t.Errorf("off: %+v notes %q", off.Result, off.Notes)
	}
}

func TestApplyFileProblems(t *testing.T) {
	base := operatorConfig()
	for name, tc := range map[string]struct {
		content string
		status  Status
	}{
		"syntax":     {"[output\nlanguage = \"tr\"", StatusInvalid},
		"too large":  {"# " + strings.Repeat("x", MaxFileBytes), StatusInvalid},
		"nothing":    {"", StatusNothingApplied},
		"only comms": {"# nothing here\n", StatusNothingApplied},
		"forbidden":  {"[llm]\nmodel = \"x\"\n", StatusNothingApplied},
		"invalid":    {"[review]\nmax_findings = 0\n", StatusNothingApplied},
	} {
		o := Apply(base, readBase(t, tc.content))
		if o.Result.Status != tc.status || len(o.Result.Applied) != 0 || !reflect.DeepEqual(o.Config, base) {
			t.Errorf("%s: %+v", name, o.Result)
		}
	}
}

func TestApplyReplaceKeys(t *testing.T) {
	_, o := applyText(t, strings.Join([]string{
		"[output]", `language = "tr"`,
		"[diff]", `ignore_generated_frameworks = ["protobuf"]`,
		"[review]", `guidelines_paths = ["docs/GUIDE.md"]`,
		"require_tests = false", "require_security = false", "require_performance = false", "require_effort_estimate = false",
		"inline_findings = false", "persistent_overview = false", "ci_status = false", "file_tree = false", "incremental = true",
		"issue_compliance = false",
	}, "\n"))
	c := o.Config
	if c.Output.Language != "tr" || !slices.Equal(c.Diff.IgnoreGeneratedFrameworks, []string{"protobuf"}) ||
		!slices.Equal(c.Review.GuidelinesPaths, []string{"docs/GUIDE.md"}) {
		t.Errorf("replace keys: language %q, frameworks %q, guidelines %q", c.Output.Language, c.Diff.IgnoreGeneratedFrameworks, c.Review.GuidelinesPaths)
	}
	r := c.Review
	if r.RequireTests || r.RequireSecurity || r.RequirePerformance || r.RequireEffortEstimate || r.InlineFindings ||
		r.PersistentOverview || r.CIStatus || r.FileTree || !r.Incremental || r.IssueCompliance {
		t.Errorf("bool keys not replaced: %+v", r)
	}
	if o.Result.Status != StatusApplied || len(o.Result.Applied) != 13 {
		t.Errorf("result %+v", o.Result)
	}
}

// TestApplyGuidelinesPathsAreExplicit: paths chosen by the repository count as
// chosen by the user, so a missing file gets the note.
func TestApplyGuidelinesPathsAreExplicit(t *testing.T) {
	base, o := applyText(t, "[review]\nguidelines_paths = [\"docs/GUIDE.md\"]\n")
	if !o.Config.Review.GuidelinesPathsExplicit || base.Review.GuidelinesPathsExplicit {
		t.Errorf("explicit: merged %v, process %v", o.Config.Review.GuidelinesPathsExplicit, base.Review.GuidelinesPathsExplicit)
	}
	if _, o := applyText(t, "[review]\nrequire_tests = false\n"); o.Config.Review.GuidelinesPathsExplicit {
		t.Error("explicit without the key")
	}
	if _, o := applyText(t, "[review]\nguidelines_paths = [\"../x\"]\n"); o.Config.Review.GuidelinesPathsExplicit {
		t.Error("explicit from an invalid value")
	}
}

func TestApplyEmptyGuidelinesTurnsThemOff(t *testing.T) {
	_, o := applyText(t, "[review]\nguidelines_paths = []\n")
	if len(o.Config.Review.GuidelinesPaths) != 0 || !slices.Contains(o.Result.Applied, "review.guidelines_paths") {
		t.Errorf("paths %q, applied %q", o.Config.Review.GuidelinesPaths, o.Result.Applied)
	}
}

func TestApplyIgnoreListsAreAdded(t *testing.T) {
	_, o := applyText(t, "[ignore]\nglob = [\"**/*_gen.go\", \"vendor/**\"]\nregex = [\"^gen/\"]\n")
	if want := []string{"vendor/**", "dist/**", "**/*_gen.go"}; !slices.Equal(o.Config.Ignore.Glob, want) {
		t.Errorf("globs %q, want the operator's then the file's, without a duplicate: %q", o.Config.Ignore.Glob, want)
	}
	if want := []string{`\.min\.js$`, "^gen/"}; !slices.Equal(o.Config.Ignore.Regex, want) {
		t.Errorf("regexes %q, want %q", o.Config.Ignore.Regex, want)
	}
}

// TestApplyTightenOnly: a file can only be stricter than the operator, in
// both directions. [canary]
func TestApplyTightenOnly(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		got        func(*config.Config) int
		want       int
	}{
		{"max_findings lower", "[review]\nmax_findings = 2", func(c *config.Config) int { return c.Review.MaxFindings }, 2},
		{"max_findings higher", "[review]\nmax_findings = 9", func(c *config.Config) int { return c.Review.MaxFindings }, 5},
		{"max_total_findings lower", "[review]\nmax_total_findings = 4", func(c *config.Config) int { return c.Review.MaxTotalFindings }, 4},
		{"max_total_findings higher", "[review]\nmax_total_findings = 40", func(c *config.Config) int { return c.Review.MaxTotalFindings }, 12},
		{"max_suggestions lower", "[improve]\nmax_suggestions = 3", func(c *config.Config) int { return c.Improve.MaxSuggestions }, 3},
		{"max_suggestions higher", "[improve]\nmax_suggestions = 30", func(c *config.Config) int { return c.Improve.MaxSuggestions }, 8},
		{"min_score higher", "[improve]\nmin_score = 9", func(c *config.Config) int { return c.Improve.MinScore }, 9},
		{"min_score lower", "[improve]\nmin_score = 2", func(c *config.Config) int { return c.Improve.MinScore }, 6},
		{"min_score zero", "[improve]\nmin_score = 0", func(c *config.Config) int { return c.Improve.MinScore }, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, o := applyText(t, tc.file)
			if got := tc.got(o.Config); got != tc.want {
				t.Errorf("effective value %d, want %d", got, tc.want)
			}
			// A looser value is capped silently and the key is still applied.
			if len(o.Result.Applied) != 1 || len(o.Notes) != 1 || !strings.HasPrefix(o.Notes[0], "Settings from `") {
				t.Errorf("applied %q, notes %q", o.Result.Applied, o.Notes)
			}
			_ = base
		})
	}
}

func TestApplyExtraInstructions(t *testing.T) {
	_, o := applyText(t, "[review]\nextra_instructions = \"  Prefer small functions.  \"\n[ask]\nextra_instructions = \"Answer briefly.\"\n")
	c := o.Config
	if c.Review.RepoInstructions != "Prefer small functions." || c.Ask.RepoInstructions != "Answer briefly." {
		t.Errorf("repo instructions %q / %q", c.Review.RepoInstructions, c.Ask.RepoInstructions)
	}
	if c.Review.ExtraInstructions != "operator guidance" || c.Ask.ExtraInstructions != "operator ask guidance" {
		t.Errorf("the operator's instructions changed: %q / %q", c.Review.ExtraInstructions, c.Ask.ExtraInstructions)
	}

	// 2,000 bytes exactly is kept; longer is cut at a rune boundary, with a note.
	_, ok := applyText(t, "[review]\nextra_instructions = \""+strings.Repeat("a", MaxInstructionBytes)+"\"\n")
	if len(ok.Config.Review.RepoInstructions) != MaxInstructionBytes || len(ok.Notes) != 1 {
		t.Errorf("exact cap: %d bytes, notes %q", len(ok.Config.Review.RepoInstructions), ok.Notes)
	}
	long := strings.Repeat("a", MaxInstructionBytes-1) + "é" + "tail" // é starts at byte 1999 and ends at 2001
	_, cut := applyText(t, "[review]\nextra_instructions = \""+long+"\"\n")
	got := cut.Config.Review.RepoInstructions
	if got != strings.Repeat("a", MaxInstructionBytes-1) {
		t.Errorf("cut text is %d bytes, want the 1999 bytes before the split rune", len(got))
	}
	want := "The value of review.extra_instructions in `.review-mcp.toml` is longer than 2,000 bytes and was cut."
	if !slices.Contains(cut.Notes, want) || !slices.Contains(cut.Result.Applied, "review.extra_instructions") {
		t.Errorf("notes %q, applied %q", cut.Notes, cut.Result.Applied)
	}
}

func TestApplyDocsStyleReplaces(t *testing.T) {
	base, o := applyText(t, "[review]\ndocs_style = \""+strings.Repeat("x", 1000)+"\"\n")
	if len(o.Config.Review.DocsStyle) != 1000 || base.Review.DocsStyle != "" ||
		!slices.Equal(o.Result.Applied, []string{"review.docs_style"}) {
		t.Errorf("docs_style %d bytes, applied %q", len(o.Config.Review.DocsStyle), o.Result.Applied)
	}
}

func TestApplyFocusesAreKept(t *testing.T) {
	_, o := applyText(t, "[review]\nfocuses = [\"security\", \"tests\"]\n")
	if !slices.Equal(o.Focuses, []string{"security", "tests"}) || !slices.Contains(o.Result.Applied, "review.focuses") {
		t.Errorf("focuses %q, applied %q", o.Focuses, o.Result.Applied)
	}
	if _, o := applyText(t, ""); o.Focuses != nil {
		t.Errorf("focuses %q without the key", o.Focuses)
	}
}

func TestApplyInvalidValues(t *testing.T) {
	for _, tc := range []struct{ key, file string }{
		{"output.language", "[output]\nlanguage = \"english_US\""},
		{"output.language", "[output]\nlanguage = 3"},
		{"ignore.glob", "[ignore]\nglob = [\"a/[b\"]"},
		{"ignore.glob", "[ignore]\nglob = \"vendor/**\""},
		{"ignore.regex", "[ignore]\nregex = [\"(unclosed\"]"},
		{"ignore.regex", "[ignore]\nregex = [\"(?=x)\"]"},
		{"diff.ignore_generated_frameworks", "[diff]\nignore_generated_frameworks = [\"nosuchfw\"]"},
		{"review.guidelines_paths", "[review]\nguidelines_paths = [\"../x.md\"]"},
		{"review.guidelines_paths", "[review]\nguidelines_paths = [\"/abs.md\"]"},
		{"review.guidelines_paths", "[review]\nguidelines_paths = [\"a.md\",\"b.md\",\"c.md\",\"d.md\",\"e.md\",\"f.md\"]"},
		{"review.require_tests", "[review]\nrequire_tests = \"yes\""},
		{"review.inline_findings", "[review]\ninline_findings = 1"},
		{"review.max_findings", "[review]\nmax_findings = 0"},
		{"review.max_findings", "[review]\nmax_findings = 21"},
		{"review.max_findings", "[review]\nmax_findings = 2.5"},
		{"review.max_findings", "[review]\nmax_findings = \"2\""},
		{"review.max_total_findings", "[review]\nmax_total_findings = 51"},
		{"improve.max_suggestions", "[improve]\nmax_suggestions = 31"},
		{"improve.min_score", "[improve]\nmin_score = 11"},
		{"improve.min_score", "[improve]\nmin_score = -1"},
		{"review.extra_instructions", "[review]\nextra_instructions = 5"},
		{"ask.extra_instructions", "[ask]\nextra_instructions = [\"x\"]"},
		{"review.focuses", "[review]\nfocuses = \"security\""},
		{"review.focuses", "[review]\nfocuses = [\"\"]"},
		{"review.focuses", "[review]\nfocuses = [3]"},
		{"review.docs_style", "[review]\ndocs_style = 3"},
		{"review.docs_style", "[review]\ndocs_style = \"" + strings.Repeat("x", 1001) + "\""},
	} {
		base, o := applyText(t, tc.file)
		if !reflect.DeepEqual(o.Config, base) || len(o.Result.Applied) != 0 || o.Focuses != nil {
			t.Errorf("%s (%q): an invalid value changed the run: applied %q", tc.key, tc.file, o.Result.Applied)
		}
		want := "The value of " + tc.key + " in `.review-mcp.toml` is not valid and was ignored."
		if !slices.Contains(o.Notes, want) || !slices.Equal(o.Result.Ignored, []string{tc.key}) {
			t.Errorf("%s (%q): notes %q, ignored %q", tc.key, tc.file, o.Notes, o.Result.Ignored)
		}
	}
}

func TestApplyInvalidValueIsNeverQuoted(t *testing.T) {
	const v = "NOT-A-LOCALE-ZQ7X"
	_, o := applyText(t, "[output]\nlanguage = \""+v+"\"\n[ignore]\nregex = [\""+v+"(\"]\n")
	if text := strings.Join(o.Notes, "\n") + strings.Join(o.Result.Ignored, ","); strings.Contains(text, v) {
		t.Errorf("a value of the file is quoted: %s", text)
	}
}

func TestApplyNotes(t *testing.T) {
	_, o := applyText(t, strings.Join([]string{
		"[output]", `language = "tr"`,
		"[ignore]", `glob = ["**/*_gen.go"]`,
		"[llm]", `model = "x"`,
		"[serve]", `listen = ":1"`,
		"[review]", "max_findings = 0",
	}, "\n"))
	want := []string{
		"Settings from `.review-mcp.toml` on the target branch were applied: output.language, ignore.glob.",
		"Settings in `.review-mcp.toml` that a repository may not change were ignored: llm.model, serve.listen.",
		"The value of review.max_findings in `.review-mcp.toml` is not valid and was ignored.",
	}
	if !slices.Equal(o.Notes, want) {
		t.Errorf("notes:\n%q\nwant:\n%q", o.Notes, want)
	}
	if want := []string{"llm.model", "serve.listen", "review.max_findings"}; !slices.Equal(o.Result.Ignored, want) {
		t.Errorf("ignored %q, want %q", o.Result.Ignored, want)
	}
}

func TestApplyIgnoredCapIncludesInvalidKeys(t *testing.T) {
	var b strings.Builder
	b.WriteString("[review]\nmax_findings = 0\n[x]\n")
	for i := 0; i < 10; i++ {
		b.WriteString("k" + string(rune('a'+i)) + " = 1\n")
	}
	_, o := applyText(t, b.String())
	if len(o.Result.Ignored) != MaxIgnoredNames+1 || o.Result.Ignored[MaxIgnoredNames] != "and 1 more" {
		t.Errorf("ignored %q, want 10 names and \"and 1 more\"", o.Result.Ignored)
	}
}

// TestProcessConfigurationIsNeverMutated: the merge works on a copy; the
// configuration it was given is equal to a fresh one afterwards. [canary]
func TestProcessConfigurationIsNeverMutated(t *testing.T) {
	base := operatorConfig()
	o := Apply(base, readBase(t, strings.Join([]string{
		"[output]", `language = "tr"`,
		"[ignore]", `glob = ["**/*_gen.go"]`, `regex = ["^gen/"]`,
		"[diff]", `ignore_generated_frameworks = ["protobuf"]`,
		"[review]", `guidelines_paths = ["a.md"]`, "max_findings = 2", "max_total_findings = 4", "require_tests = false",
		`extra_instructions = "repo guidance"`,
		"[ask]", `extra_instructions = "repo ask guidance"`,
		"[improve]", "max_suggestions = 2", "min_score = 9",
	}, "\n")))
	if len(o.Result.Applied) == 0 {
		t.Fatal("nothing was applied")
	}
	if want := operatorConfig(); !reflect.DeepEqual(base, want) {
		t.Errorf("the process configuration changed:\n got %+v\nwant %+v", base, want)
	}
	// Changing the copy afterwards (the tool arguments) does not reach it either.
	o.Config.Ignore.Glob[0] = "changed"
	o.Config.LLM.Fallback[0].Model = "changed"
	*o.Config.LLM.Temperature = 1.5
	if want := operatorConfig(); !reflect.DeepEqual(base, want) {
		t.Error("the copy shares memory with the process configuration")
	}
}

// TestApplyListsAreNeverNil: a file with only allowed, valid keys ignores
// nothing, and Ignored is an empty list that marshals as [] (the tools'
// output schemas require an array); Applied is never nil either.
func TestApplyListsAreNeverNil(t *testing.T) {
	_, o := applyText(t, "[review]\nissue_compliance = false\n")
	if o.Result.Status != StatusApplied || o.Result.Ignored == nil || len(o.Result.Ignored) != 0 {
		t.Fatalf("result %+v: want applied with Ignored == []string{}", o.Result)
	}
	raw, err := json.Marshal(o.Result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"ignored":[]`) || !strings.Contains(string(raw), `"applied":["review.issue_compliance"]`) {
		t.Errorf("JSON %s", raw)
	}
	for _, text := range []string{"", "# comments only\n", "[review]\nmax_findings = \"x\"\n", "[llm]\nmodel = \"m\"\n", "not toml ["} {
		_, o := applyText(t, text)
		if o.Result.Applied == nil || o.Result.Ignored == nil {
			t.Errorf("file %q: result %+v has a nil list", text, o.Result)
		}
	}
}

// TestApplyRelatedContractGlobsAreAdded: the repository's
// related.contract_globs are added to the operator's, without duplicates;
// an invalid glob makes the key invalid.
func TestApplyRelatedContractGlobsAreAdded(t *testing.T) {
	base := operatorConfig()
	base.Related.ContractGlobs = []string{"*.messages.ts"}
	o := Apply(base, readBase(t, "[related]\ncontract_globs = [\"shared/types/**\", \"*.messages.ts\"]\n"))
	if want := []string{"*.messages.ts", "shared/types/**"}; !slices.Equal(o.Config.Related.ContractGlobs, want) {
		t.Errorf("globs %q, want %q", o.Config.Related.ContractGlobs, want)
	}
	if !slices.Equal(o.Result.Applied, []string{"related.contract_globs"}) || len(base.Related.ContractGlobs) != 1 {
		t.Errorf("applied %q, operator globs %q", o.Result.Applied, base.Related.ContractGlobs)
	}
	_, o = applyText(t, "[related]\ncontract_globs = [\"[bad\"]\nmax_tokens = 1\n")
	if !slices.Equal(o.Result.Ignored, []string{"related.max_tokens", "related.contract_globs"}) {
		t.Errorf("ignored %q: max_tokens is not allowed and the glob is invalid", o.Result.Ignored)
	}
}

// TestApplyRelatedRepositoryKeysAreOperatorOnly: a repository config cannot
// set related.max_identifiers or related.from_repo_config (v2.5 spec V-13b
// §0 items 4 and 6, operator keys): both are ignored, and the operator's
// values stay.
func TestApplyRelatedRepositoryKeysAreOperatorOnly(t *testing.T) {
	base := operatorConfig()
	o := Apply(base, readBase(t, "[related]\nmax_identifiers = 50\nfrom_repo_config = true\n"))
	if !slices.Equal(o.Result.Applied, []string{}) ||
		!slices.Equal(o.Result.Ignored, []string{"related.max_identifiers", "related.from_repo_config"}) {
		t.Errorf("applied %q, ignored %q", o.Result.Applied, o.Result.Ignored)
	}
	if o.Config.Related.MaxIdentifiers != base.Related.MaxIdentifiers || o.Config.Related.FromRepoConfig {
		t.Errorf("related %+v", o.Config.Related)
	}
}
