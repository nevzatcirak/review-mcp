package repocfg

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/filter/data"
)

// Bounds of the allowed values (X-40 §1.2).
const (
	// MaxInstructionBytes caps review.extra_instructions and
	// ask.extra_instructions of the file; longer text is cut.
	MaxInstructionBytes = 2000
	// MaxGuidelinesPaths is the most review.guidelines_paths of the file.
	MaxGuidelinesPaths = 5
	// MaxFocuses and MaxFocusBytes bound review.focuses of the file.
	MaxFocuses    = 20
	MaxFocusBytes = 32
)

// Result is the repo_config of a tool result (X-40 §1.3). Every field is
// always set.
type Result struct {
	// Path is the configured path of the file.
	Path string `json:"path"`
	// Status is applied, nothing_applied, absent, invalid, off or unreadable.
	Status Status `json:"status"`
	// Applied are the keys applied, in allow-list order. Never nil, whatever
	// the status: the tools' output schemas require an array.
	Applied []string `json:"applied"`
	// Ignored are the keys ignored (a key a repository may not change, or an
	// allowed key with an invalid value): at most MaxIgnoredNames, then
	// "and N more". Key names are sanitised.
	Ignored []string `json:"ignored"`
}

// Outcome is a run's view of the repository config: the merged configuration
// and what is to be reported.
type Outcome struct {
	// Config is the run's own copy of the configuration with the allowed
	// settings merged. The configuration it was made from is never changed.
	Config *config.Config
	// Result is the repo_config of the tool result.
	Result Result
	// Notes are the notes of the result (X-40 §1.5), in the order shown.
	Notes []string
	// Focuses is review.focuses of the file, as written (cleaned, not yet
	// checked against the known focus values): the default of the
	// multi_perspective_review prompt. Nil when the file has none.
	Focuses []string
	// Related are the related repositories the file declares
	// ([[related.repos]], v2.5 spec V-13b §3.2), the valid ones in file
	// order, kept only with related.from_repo_config. Nil when there are
	// none or reading them is off.
	Related []Declared
}

// Apply merges the allowed settings of f into a copy of base and reports what
// was done. base is not changed. A file that was not read, or that is not
// valid, changes nothing. f may be nil (an absent file).
//
// Precedence is tool argument > repository file > operator configuration;
// the caller applies tool arguments on Outcome.Config afterwards. Ignore
// lists are added to the operator's, numeric keys may only tighten the
// operator's value, review.extra_instructions and ask.extra_instructions are
// kept apart (Review.RepoInstructions, Ask.RepoInstructions) so that the
// prompt can fence them.
func Apply(base *config.Config, f *File) *Outcome {
	if f == nil {
		f = &File{path: base.RepoConfig.Path, state: stateAbsent}
	}
	o := &Outcome{Config: base.Clone(), Result: Result{Path: f.path, Applied: []string{}, Ignored: []string{}}}
	switch f.state {
	case stateOff:
		o.Result.Status = StatusOff
	case stateAbsent:
		o.Result.Status = StatusAbsent
	case stateUnreadable:
		o.Result.Status = StatusUnreadable
	case stateTooLarge, stateSyntax:
		o.Result.Status = StatusInvalid
	}
	if f.state != stateParsed {
		o.Notes = f.Notes()
		return o
	}

	var invalid, cut, off []string
	for _, key := range allowList {
		v, ok := f.values[key]
		if !ok {
			continue
		}
		if key == keyRelatedRepos {
			applied, bad, isOff := applyRelated(o, v)
			switch {
			case isOff:
				off = append(off, key)
			case applied:
				o.Result.Applied = append(o.Result.Applied, key)
			}
			invalid = append(invalid, bad...)
			continue
		}
		switch applyKey(o, key, v) {
		case keyApplied:
			o.Result.Applied = append(o.Result.Applied, key)
		case keyCut:
			o.Result.Applied = append(o.Result.Applied, key)
			cut = append(cut, key)
		case keyInvalid:
			invalid = append(invalid, key)
		}
	}

	o.Result.Status = StatusApplied
	if len(o.Result.Applied) == 0 {
		o.Result.Status = StatusNothingApplied
	}
	o.Result.Ignored = capNames(append(append(slices.Clone(f.ignored), invalid...), off...))

	if len(o.Result.Applied) > 0 {
		o.Notes = append(o.Notes, fmt.Sprintf("Settings from `%s` on the target branch were applied: %s.", f.path, strings.Join(o.Result.Applied, ", ")))
	}
	o.Notes = append(o.Notes, f.Notes()...)
	for _, k := range invalid {
		o.Notes = append(o.Notes, fmt.Sprintf("The value of %s in `%s` is not valid and was ignored.", k, f.path))
	}
	for _, k := range cut {
		o.Notes = append(o.Notes, fmt.Sprintf("The value of %s in `%s` is longer than 2,000 bytes and was cut.", k, f.path))
	}
	if len(off) > 0 {
		o.Notes = append(o.Notes, NoteRelatedOff)
	}
	return o
}

// capNames lists at most MaxIgnoredNames names, then "and N more". It never
// returns nil: no names is an empty list, which the tools' output schemas
// require as an array.
func capNames(names []string) []string {
	if len(names) <= MaxIgnoredNames {
		return append([]string{}, names...)
	}
	return append(slices.Clone(names[:MaxIgnoredNames]), fmt.Sprintf("and %d more", len(names)-MaxIgnoredNames))
}

type keyResult int

const (
	keyApplied keyResult = iota
	keyInvalid
	// keyCut: applied, but the value was cut to its cap.
	keyCut
)

// applyKey validates the value v of the allowed key and merges it into
// o.Config. The validation is that of the operator key; an invalid value
// changes nothing.
func applyKey(o *Outcome, key string, v any) keyResult {
	c := o.Config
	switch key {
	case "output.language":
		s, ok := v.(string)
		if !ok || !config.ValidLocale(s) {
			return keyInvalid
		}
		c.Output.Language = s
	case "ignore.glob":
		l, ok := stringList(v)
		if !ok || slices.ContainsFunc(l, func(s string) bool { return config.GlobProblem(s) != "" }) {
			return keyInvalid
		}
		c.Ignore.Glob = addUnique(c.Ignore.Glob, l)
	case "ignore.regex":
		l, ok := stringList(v)
		if !ok || slices.ContainsFunc(l, func(s string) bool { return config.RegexProblem(s) != "" }) {
			return keyInvalid
		}
		c.Ignore.Regex = addUnique(c.Ignore.Regex, l)
	case "diff.ignore_generated_frameworks":
		l, ok := stringList(v)
		if !ok || slices.ContainsFunc(l, func(s string) bool { _, known := data.GeneratedCode[s]; return !known }) {
			return keyInvalid
		}
		c.Diff.IgnoreGeneratedFrameworks = l
	case "review.guidelines_paths":
		l, ok := stringList(v)
		if !ok || len(l) > MaxGuidelinesPaths || slices.ContainsFunc(l, func(s string) bool { return config.GuidelinesPathProblem(s) != "" }) {
			return keyInvalid
		}
		c.Review.GuidelinesPaths = l
		// The maintainers chose these paths, so a missing file there is worth
		// the existing note, as for paths the operator set.
		c.Review.GuidelinesPathsExplicit = true
	case "review.require_tests":
		return setBool(v, &c.Review.RequireTests)
	case "review.require_security":
		return setBool(v, &c.Review.RequireSecurity)
	case "review.require_performance":
		return setBool(v, &c.Review.RequirePerformance)
	case "review.require_effort_estimate":
		return setBool(v, &c.Review.RequireEffortEstimate)
	case "review.inline_findings":
		return setBool(v, &c.Review.InlineFindings)
	case "review.persistent_overview":
		return setBool(v, &c.Review.PersistentOverview)
	case "review.ci_status":
		return setBool(v, &c.Review.CIStatus)
	case "review.file_tree":
		return setBool(v, &c.Review.FileTree)
	case "review.incremental":
		return setBool(v, &c.Review.Incremental)
	case "related.contract_globs":
		l, ok := stringList(v)
		if !ok || slices.ContainsFunc(l, func(s string) bool { return config.GlobProblem(s) != "" }) {
			return keyInvalid
		}
		c.Related.ContractGlobs = addUnique(c.Related.ContractGlobs, l)
	case "review.issue_compliance":
		return setBool(v, &c.Review.IssueCompliance)
	case "review.max_findings":
		return tighten(v, 1, 20, &c.Review.MaxFindings, false)
	case "review.max_total_findings":
		return tighten(v, 1, config.MaxMaxTotalFindings, &c.Review.MaxTotalFindings, false)
	case "improve.max_suggestions":
		return tighten(v, 1, config.MaxImproveSuggestions, &c.Improve.MaxSuggestions, false)
	case "improve.min_score":
		return tighten(v, 0, config.MaxImproveMinScore, &c.Improve.MinScore, true)
	case "review.extra_instructions":
		return setInstructions(v, &c.Review.RepoInstructions)
	case "ask.extra_instructions":
		return setInstructions(v, &c.Ask.RepoInstructions)
	case "review.docs_style":
		s, ok := v.(string)
		if !ok || len(s) > config.MaxDocsStyleBytes {
			return keyInvalid
		}
		c.Review.DocsStyle = s
	case "review.focuses":
		l, ok := stringList(v)
		if !ok || len(l) > MaxFocuses || slices.ContainsFunc(l, func(s string) bool { return !validFocusText(s) }) {
			return keyInvalid
		}
		o.Focuses = l
	}
	return keyApplied
}

// stringList reads a TOML array of strings.
func stringList(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(arr))
	for i, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

// addUnique appends the items of add that base does not hold yet.
func addUnique(base, add []string) []string {
	out := slices.Clone(base)
	for _, s := range add {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func setBool(v any, dst *bool) keyResult {
	b, ok := v.(bool)
	if !ok {
		return keyInvalid
	}
	*dst = b
	return keyApplied
}

// tighten merges the integer v, which must be in [lo, hi], into *dst so that
// the file can only be stricter than the operator: the smaller value, or the
// larger one when higher is stricter (improve.min_score). A looser value is
// capped by the operator's, silently.
func tighten(v any, lo, hi int, dst *int, higherIsStricter bool) keyResult {
	n, ok := v.(int64)
	if !ok || n < int64(lo) || n > int64(hi) {
		return keyInvalid
	}
	if higherIsStricter {
		*dst = max(*dst, int(n))
	} else {
		*dst = min(*dst, int(n))
	}
	return keyApplied
}

// setInstructions stores the repository's extra instructions: the text is
// trimmed and cut at MaxInstructionBytes on a rune boundary.
func setInstructions(v any, dst *string) keyResult {
	s, ok := v.(string)
	if !ok {
		return keyInvalid
	}
	s = strings.TrimSpace(s)
	res := keyApplied
	if len(s) > MaxInstructionBytes {
		s = cutRunes(s, MaxInstructionBytes)
		res = keyCut
	}
	*dst = s
	return res
}

// cutRunes cuts s to at most n bytes without splitting a rune.
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// validFocusText reports a plausible focus value: 1 to MaxFocusBytes bytes,
// no control character. Whether it is a known focus is decided where the
// list is used.
func validFocusText(s string) bool {
	return s != "" && len(s) <= MaxFocusBytes && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
