package issuectx

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/projctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Limits of the issue_context argument (spec §2.1 item 1), in characters.
const (
	MaxArgumentIssues   = 3
	MaxKeyRunes         = 64
	MaxTitleRunes       = 300
	MaxDescriptionRunes = 20000
	MaxCriteriaRunes    = 10000
)

// Input is one item of the issue_context argument as the client sent it.
// AcceptanceCriteria is a string or a list of strings (any JSON value is
// accepted here and checked by Validate).
type Input struct {
	Key                string
	Title              string
	Description        string
	AcceptanceCriteria any
	URL                string
}

// ArgumentError is an invalid issue_context. Its text names the item and the
// field, never a value.
type ArgumentError struct{ msg string }

func (e *ArgumentError) Error() string { return e.msg }

func argErr(format string, a ...any) *ArgumentError {
	return &ArgumentError{msg: fmt.Sprintf(format, a...)}
}

// DESIGN-QUESTION (WP-10b DQ-1): the spec lists the fields of an item and
// marks only url optional, while a ticket often has no acceptance criteria
// and may have no description. Required: key and title (they name the
// issue); description and acceptance_criteria may be absent or empty.
// Lengths are counted in characters, after control characters are
// stripped, as for the other text arguments (focus, findings).
// Recommendation: keep.

// Validate checks the issue_context items and returns them as Issues ready
// for the prompt: control characters stripped (line breaks kept in the
// description and the criteria), every size enforced. nil and an empty list
// mean "not given" and return nil. The error is an *ArgumentError.
func Validate(in []Input) ([]Issue, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > MaxArgumentIssues {
		return nil, argErr("issue_context must hold 1 to %d issues", MaxArgumentIssues)
	}
	out := make([]Issue, 0, len(in))
	for i, it := range in {
		n := i + 1
		key, title := cleanLine(it.Key), cleanLine(it.Title)
		switch {
		case key == "":
			return nil, argErr("issue_context[%d].key is required", n)
		case utf8.RuneCountInString(key) > MaxKeyRunes:
			return nil, argErr("issue_context[%d].key must be at most %d characters", n, MaxKeyRunes)
		case title == "":
			return nil, argErr("issue_context[%d].title is required", n)
		case utf8.RuneCountInString(title) > MaxTitleRunes:
			return nil, argErr("issue_context[%d].title must be at most %d characters", n, MaxTitleRunes)
		}
		desc := projctx.CleanText(it.Description)
		if utf8.RuneCountInString(desc) > MaxDescriptionRunes {
			return nil, argErr("issue_context[%d].description must be at most %d characters", n, MaxDescriptionRunes)
		}
		crit, ok := criteria(it.AcceptanceCriteria)
		if !ok {
			return nil, argErr("issue_context[%d].acceptance_criteria must be a string or a list of strings", n)
		}
		total := 0
		for _, c := range crit {
			total += utf8.RuneCountInString(c)
		}
		if total > MaxCriteriaRunes {
			return nil, argErr("issue_context[%d].acceptance_criteria must be at most %d characters in total", n,
				MaxCriteriaRunes)
		}
		u := strings.TrimSpace(it.URL)
		if u != "" && provider.CleanCheckURL(u) != u {
			return nil, argErr("issue_context[%d].url must be an http or https URL", n)
		}
		out = append(out, Issue{Ref: key, Title: title, Description: desc, Criteria: crit, URL: u})
	}
	return out, nil
}

// criteria reads acceptance_criteria: absent, a string (one item, its line
// breaks kept), or a list of strings. Items are cleaned and empty ones
// dropped.
func criteria(v any) ([]string, bool) {
	var raw []string
	switch x := v.(type) {
	case nil:
	case string:
		raw = []string{x}
	case []string:
		raw = x
	case []any:
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			raw = append(raw, s)
		}
	default:
		return nil, false
	}
	var out []string
	for _, s := range raw {
		if s = strings.TrimSpace(projctx.CleanText(s)); s != "" {
			out = append(out, s)
		}
	}
	return out, true
}
