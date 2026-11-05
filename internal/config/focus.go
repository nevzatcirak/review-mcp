package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Focus profiles (X-51, v2.9 spec V-19 §1.1): named custom focuses and the
// rules that add a focus to the multi_perspective_review default list when
// a changed file matches one of their globs.
//
//	[[review.custom_focuses]]
//	name = "api-contract"
//	instructions = "Public contracts: request and response shapes, status codes."
//	docs_style = false
//
//	[[review.focus_rules]]
//	paths = ["**/controller/**"]
//	focus = "api-contract"
//
// Environment variables cannot express lists of tables: both keys exist in
// the configuration file and the repository config only.

// Bounds of the focus profiles (§0 item 7, §1.1).
const (
	// MaxCustomFocuses is the most custom focuses of one source (the
	// operator's configuration, or one repository config).
	MaxCustomFocuses = 10
	// MaxFocusRules is the most focus rules of one source.
	MaxFocusRules = 20
	// MaxFocusRulePaths is the most globs of one rule.
	MaxFocusRulePaths = 20
	// MaxCustomInstructionsBytes is the longest instructions of a custom
	// focus, in bytes of UTF-8.
	MaxCustomInstructionsBytes = 1000
)

// Sources of a custom focus: who defined it (§1.3). The values are those of
// a review result's focus_source.
const (
	FocusSourceOperator   = "operator"
	FocusSourceRepoConfig = "repo_config"
)

// BuiltinFocusNames are the built-in focus values (X-33), in the order the
// tools document them. review.FocusValues is the same list; a test keeps
// the two equal.
var BuiltinFocusNames = []string{"security", "architecture", "performance", "ux", "tests", "docs"}

// ReservedFocusNames are the names a custom focus cannot take (§0 item 4):
// the built-in values and impact, which V-18 adds.
var ReservedFocusNames = append(slices.Clone(BuiltinFocusNames), "impact")

// customFocusNameRE is the syntax of a custom focus name (§1.1).
var customFocusNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// CustomFocus is one entry of [[review.custom_focuses]].
type CustomFocus struct {
	// Name is the focus name: CustomFocusNameProblem holds its syntax.
	Name string `toml:"name" json:"name"`
	// Instructions describe the area. They reach the model only in the user
	// prompt, fenced under a header that names their source; never in the
	// system prompt, never in a log, a note or server_info.
	Instructions string `toml:"instructions" json:"-"`
	// DocsStyle also gives the focus the documentation style block.
	DocsStyle bool `toml:"docs_style" json:"docs_style"`
	// Source is FocusSourceOperator or FocusSourceRepoConfig: set when the
	// entry is loaded or merged, not a key.
	Source string `toml:"-" json:"source"`
}

// FocusRule is one entry of [[review.focus_rules]].
type FocusRule struct {
	// Paths are 1 to MaxFocusRulePaths globs with the syntax of ignore.glob.
	Paths []string `toml:"paths" json:"paths"`
	// Focus is a built-in focus value or a custom focus name visible to the
	// rule's source, in lower case once validated.
	Focus string `toml:"focus" json:"focus"`
}

// Keys of the focus profiles.
const (
	keyCustomFocuses = "review.custom_focuses"
	keyFocusRules    = "review.focus_rules"
)

// focusProfileFields are the field keys of the two lists' entries.
var focusProfileFields = map[string][]string{
	keyCustomFocuses: {"name", "instructions", "docs_style"},
	keyFocusRules:    {"paths", "focus"},
}

func init() {
	sections[keyCustomFocuses] = true
	sections[keyFocusRules] = true
}

// isFocusProfileKey reports whether a file key is a field of an entry of
// review.custom_focuses or review.focus_rules (the decoder lists those
// without the entry's index).
func isFocusProfileKey(key string) bool {
	for section, fields := range focusProfileFields {
		if field, ok := strings.CutPrefix(key, section+"."); ok && slices.Contains(fields, field) {
			return true
		}
	}
	return false
}

// CustomFocusNameProblem returns why name cannot be the name of a custom
// focus, or "". The reason never repeats the name.
func CustomFocusNameProblem(name string) string {
	switch {
	case !customFocusNameRE.MatchString(name):
		return "name must be 2 to 32 characters: a lower-case letter, then lower-case letters, digits or \"-\""
	case slices.Contains(ReservedFocusNames, name):
		return "name is reserved for a built-in focus"
	}
	return ""
}

// CleanFocusInstructions returns the instructions of a custom focus in
// their stored form (line endings as "\n", surrounding white space
// trimmed) and why they are not valid, or "". Valid instructions are 1 to
// MaxCustomInstructionsBytes bytes of UTF-8 without control characters
// other than the line feed and without bidirectional formatting or line
// and paragraph separator characters. The reason never repeats the text.
func CleanFocusInstructions(s string) (string, string) {
	s = strings.TrimSpace(strings.NewReplacer("\r\n", "\n").Replace(s))
	switch {
	case s == "":
		return "", "instructions must not be empty"
	case len(s) > MaxCustomInstructionsBytes:
		return "", fmt.Sprintf("instructions are longer than %d bytes", MaxCustomInstructionsBytes)
	case !utf8.ValidString(s):
		return "", "instructions must be valid UTF-8"
	}
	for _, r := range s {
		if r != '\n' && (unicode.IsControl(r) || IsBidiFormat(r) || r == '\u2028' || r == '\u2029') {
			return "", "instructions must not contain control or bidirectional formatting characters other than line breaks"
		}
	}
	return s, ""
}

// IsBidiFormat reports a bidirectional formatting character (embedding,
// override, isolate or mark), which could make the text read differently
// from what it holds.
func IsBidiFormat(r rune) bool {
	return r == '\u200e' || r == '\u200f' || r == '\u061c' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}

// CheckCustomFocus validates one custom focus entry and returns it in its
// stored form, or why it is not valid. Uniqueness within the source is the
// caller's.
func CheckCustomFocus(f CustomFocus) (CustomFocus, string) {
	if p := CustomFocusNameProblem(f.Name); p != "" {
		return f, p
	}
	text, p := CleanFocusInstructions(f.Instructions)
	if p != "" {
		return f, p
	}
	f.Instructions = text
	return f, ""
}

// CheckFocusRule validates one focus rule and returns it with its focus in
// lower case, or why it is not valid. visible are the custom focus names the
// rule may name besides the built-in values.
func CheckFocusRule(r FocusRule, visible []string) (FocusRule, string) {
	if len(r.Paths) == 0 || len(r.Paths) > MaxFocusRulePaths {
		return r, fmt.Sprintf("paths must hold 1 to %d globs", MaxFocusRulePaths)
	}
	for _, g := range r.Paths {
		if p := GlobProblem(g); p != "" {
			return r, "paths: " + p
		}
	}
	focus := strings.ToLower(strings.TrimSpace(r.Focus))
	if !slices.Contains(BuiltinFocusNames, focus) && !slices.Contains(visible, focus) {
		return r, "focus must be a built-in focus or a custom focus defined in the same configuration"
	}
	r.Paths = slices.Clone(r.Paths)
	r.Focus = focus
	return r, ""
}

// CustomFocusNames are the names of list, in order.
func CustomFocusNames(list []CustomFocus) []string {
	out := make([]string, len(list))
	for i, f := range list {
		out[i] = f.Name
	}
	return out
}

// validateFocusProfiles checks the operator's custom focuses and focus
// rules (§1.1). Problems name the key and the entry's index, never a value.
func (l *loader) validateFocusProfiles() {
	r := &l.cfg.Review
	if len(r.CustomFocuses) > MaxCustomFocuses {
		l.problem("%s: at most %d custom focuses may be defined (found %d)", keyCustomFocuses, MaxCustomFocuses, len(r.CustomFocuses))
	} else {
		var names []string
		for i := range r.CustomFocuses {
			f, p := CheckCustomFocus(r.CustomFocuses[i])
			switch {
			case p != "":
				l.problem("%s[%d]: %s", keyCustomFocuses, i, p)
				continue
			case slices.Contains(names, f.Name):
				l.problem("%s[%d]: name is already defined by an earlier entry", keyCustomFocuses, i)
				continue
			}
			f.Source = FocusSourceOperator
			r.CustomFocuses[i] = f
			names = append(names, f.Name)
		}
	}
	if len(r.FocusRules) > MaxFocusRules {
		l.problem("%s: at most %d focus rules may be defined (found %d)", keyFocusRules, MaxFocusRules, len(r.FocusRules))
		return
	}
	visible := CustomFocusNames(r.CustomFocuses)
	for i := range r.FocusRules {
		fr, p := CheckFocusRule(r.FocusRules[i], visible)
		if p != "" {
			l.problem("%s[%d]: %s", keyFocusRules, i, p)
			continue
		}
		r.FocusRules[i] = fr
	}
}

// FocusProfilesSummary is what server_info shows of the focus profiles:
// the names of the operator's custom focuses and the counts, never the
// instructions.
type FocusProfilesSummary struct {
	// CustomFocuses are the names of the operator's custom focuses, in
	// order.
	CustomFocuses []string `json:"custom_focuses"`
	// CustomFocusCount is len(CustomFocuses).
	CustomFocusCount int `json:"custom_focus_count"`
	// FocusRuleCount is the number of the operator's focus rules.
	FocusRuleCount int `json:"focus_rule_count"`
}

// focusProfilesSummary builds FocusProfilesSummary. A name that is not
// valid (a configuration with problems) is not shown, only counted.
func (c *Config) focusProfilesSummary() FocusProfilesSummary {
	names := []string{}
	for _, f := range c.Review.CustomFocuses {
		if customFocusNameRE.MatchString(f.Name) {
			names = append(names, f.Name)
		}
	}
	return FocusProfilesSummary{
		CustomFocuses:    names,
		CustomFocusCount: len(c.Review.CustomFocuses),
		FocusRuleCount:   len(c.Review.FocusRules),
	}
}
