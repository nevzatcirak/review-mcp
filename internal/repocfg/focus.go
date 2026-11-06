package repocfg

import (
	"fmt"
	"slices"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

// The focus profiles of the repository config (X-51, v2.9 spec V-19 §1.2).
// The file is read at the base revision, so a pull request cannot change
// the focuses, the rules or the style file it is reviewed with.
const (
	keyCustomFocuses = "review.custom_focuses"
	keyFocusRules    = "review.focus_rules"
)

// MaxDocsStyleFileBytes is the longest review.docs_style_file (§2.2).
const MaxDocsStyleFileBytes = 200

// NoteFocusRedefined is the note of a repository custom focus that replaces
// the operator's focus of the same name (§1.2, verbatim). name is a valid
// focus name, so it needs no cleaning.
func NoteFocusRedefined(name string) string {
	return "The repository config redefines the focus `" + name + "`."
}

// applyCustomFocuses handles review.custom_focuses: each valid entry is
// merged into the run's custom focuses (Config.Review.CustomFocuses, where
// the operator's are), replacing an operator's focus of the same name in
// place with a note, or added after them. invalid names each invalid entry
// by its 0-based index ("review.custom_focuses[2]"), never by its text, or
// the key itself when v is not a list of tables. An entry is invalid when it
// is not a table, has a key other than name, instructions and docs_style,
// fails config.CheckCustomFocus (a reserved name among others), repeats an
// earlier entry's name, or comes after the config.MaxCustomFocuses-th entry.
func applyCustomFocuses(o *Outcome, v any) (applied bool, invalid []string) {
	entries, ok := tables(v)
	if !ok {
		return false, []string{keyCustomFocuses}
	}
	merged := slices.Clone(o.Config.Review.CustomFocuses)
	var names []string
	for i, e := range entries {
		f, ok := customFocusEntry(e)
		if ok {
			var problem string
			f, problem = config.CheckCustomFocus(f)
			ok = problem == "" && !slices.Contains(names, f.Name)
		}
		if !ok || i >= config.MaxCustomFocuses {
			invalid = append(invalid, fmt.Sprintf("%s[%d]", keyCustomFocuses, i))
			continue
		}
		names = append(names, f.Name)
		f.Source = config.FocusSourceRepoConfig
		if j := slices.IndexFunc(merged, func(m config.CustomFocus) bool { return m.Name == f.Name }); j >= 0 {
			merged[j] = f
			o.redefined = append(o.redefined, NoteFocusRedefined(f.Name))
			continue
		}
		merged = append(merged, f)
	}
	o.Config.Review.CustomFocuses = merged
	return len(names) > 0, invalid
}

// customFocusEntry reads one entry of review.custom_focuses; ok is false
// when it is not a table, a field has the wrong type or a key is unknown.
func customFocusEntry(e any) (config.CustomFocus, bool) {
	m, ok := e.(map[string]any)
	if !ok {
		return config.CustomFocus{}, false
	}
	var f config.CustomFocus
	for k, v := range m {
		switch k {
		case "name":
			f.Name, ok = v.(string)
		case "instructions":
			f.Instructions, ok = v.(string)
		case "docs_style":
			f.DocsStyle, ok = v.(bool)
		default:
			ok = false
		}
		if !ok {
			return config.CustomFocus{}, false
		}
	}
	return f, true
}

// applyFocusRules handles review.focus_rules. Rules never merge across
// sources: when the file sets the key to a list, its valid rules replace
// the operator's, so an empty list turns the operator's rules off. A rule
// may name a built-in focus or any custom focus of the run (the repository's
// own and the operator's), so review.custom_focuses is applied first.
// invalid names each invalid entry by its 0-based index, or the key itself
// when v is not a list of tables (the operator's rules then stay). An entry
// is invalid when it is not a table, has a key other than paths and focus,
// fails config.CheckFocusRule, or comes after the config.MaxFocusRules-th
// entry.
//
// DESIGN-QUESTION (WP-23a): with a list whose every entry is invalid, the
// operator's rules are still replaced (by none), each entry named in a note,
// rather than kept: the file asked for its own rules. Recommendation: keep.
func applyFocusRules(o *Outcome, v any) (applied bool, invalid []string) {
	entries, ok := tables(v)
	if !ok {
		return false, []string{keyFocusRules}
	}
	visible := config.CustomFocusNames(o.Config.Review.CustomFocuses)
	rules := []config.FocusRule{}
	for i, e := range entries {
		r, ok := focusRuleEntry(e)
		if ok {
			var problem string
			r, problem = config.CheckFocusRule(r, visible)
			ok = problem == ""
		}
		if !ok || i >= config.MaxFocusRules {
			invalid = append(invalid, fmt.Sprintf("%s[%d]", keyFocusRules, i))
			continue
		}
		rules = append(rules, r)
	}
	o.Config.Review.FocusRules = rules
	return true, invalid
}

// focusRuleEntry reads one entry of review.focus_rules; ok is false when it
// is not a table, a field has the wrong type or a key is unknown.
func focusRuleEntry(e any) (config.FocusRule, bool) {
	m, ok := e.(map[string]any)
	if !ok {
		return config.FocusRule{}, false
	}
	var r config.FocusRule
	for k, v := range m {
		switch k {
		case "paths":
			r.Paths, ok = stringList(v)
		case "focus":
			r.Focus, ok = v.(string)
		default:
			ok = false
		}
		if !ok {
			return config.FocusRule{}, false
		}
	}
	return r, true
}
