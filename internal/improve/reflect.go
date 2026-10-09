package improve

import (
	"strings"
)

// MaxScore is the top of the self-review score range (0 to 10).
const MaxScore = 10

// feedback is what the self-review gave one suggestion.
type feedback struct {
	// score is nil when the matched entry had no score from 0 to MaxScore.
	score *int
	why   string
	// start and end are the new-file lines of the existing code; both nil
	// unless 1 <= start <= end.
	start, end *int
}

// reflection is the validated content of one self-review answer.
type reflection struct {
	// fb[k] is the feedback of suggestion k+1; nil when no entry matched it.
	fb []*feedback
	// unmatched counts the entries that matched no suggestion.
	unmatched int
}

// convertReflection matches a loaded self-review answer to the n
// suggestions it was given (Y-9, v2 spec §1.4). An answer without a
// code_suggestions list is unusable and gets the one re-ask.
//
// Matching (DESIGN-QUESTION in the WP-2f report: upstream matches entry i
// to suggestion i, and only when the counts are equal):
//   - an entry names its suggestion with suggestion_number (an integer, or
//     a string or an integral number holding one). An entry without a
//     usable number falls back to its position, upstream's rule, only when
//     the answer has exactly n entries; otherwise it matches nothing;
//   - a number outside 1 to n matches nothing;
//   - a relevant_file that is present must equal the numbered suggestion's
//     file (trimmed), and a suggestion_summary that is present must equal
//     its summary once both are lower-cased, their white space folded and
//     a final period removed; an entry that fails either check matches
//     nothing, so a misnumbered entry never scores another suggestion;
//   - two or more entries for the same suggestion conflict: none of them
//     is used, and the suggestion stays unscored.
//
// Every entry that matches nothing is counted in unmatched. A matched
// entry gives the score when suggestion_score is an integer from 0 to
// MaxScore (otherwise the suggestion stays unscored), the trimmed why, and
// the line range when relevant_lines_start and relevant_lines_end are
// integers with 1 <= start <= end (otherwise no range). A suggestion no
// entry matches stays unscored; it is never dropped.
func convertReflection(data map[string]any, cands []Candidate) (*reflection, error) {
	list, ok := data["code_suggestions"].([]any)
	if !ok {
		return nil, errUnusable
	}
	n := len(cands)
	out := &reflection{fb: make([]*feedback, n)}
	claims := make([][]map[string]any, n)
	for i, it := range list {
		e, ok := it.(map[string]any)
		if !ok {
			out.unmatched++
			continue
		}
		num, ok := integer(e["suggestion_number"])
		if !ok {
			if len(list) != n {
				out.unmatched++
				continue
			}
			num = i + 1
		}
		if num < 1 || num > n || !sameSuggestion(e, &cands[num-1]) {
			out.unmatched++
			continue
		}
		claims[num-1] = append(claims[num-1], e)
	}
	for k, cl := range claims {
		switch len(cl) {
		case 0:
		case 1:
			out.fb[k] = feedbackOf(cl[0])
		default:
			out.unmatched += len(cl)
		}
	}
	return out, nil
}

// sameSuggestion checks an entry's relevant_file and suggestion_summary,
// when present, against the suggestion its number names.
func sameSuggestion(e map[string]any, c *Candidate) bool {
	if v, ok := e["relevant_file"]; ok && v != nil {
		if f, _ := text(v); f != c.File {
			return false
		}
	}
	if v, ok := e["suggestion_summary"]; ok && v != nil {
		if normalizeSummary(oneLine(v)) != normalizeSummary(c.Summary) {
			return false
		}
	}
	return true
}

// normalizeSummary lower-cases s, folds its white space and removes a final
// period.
func normalizeSummary(s string) string {
	return strings.TrimSuffix(strings.Join(strings.Fields(strings.ToLower(s)), " "), ".")
}

func feedbackOf(e map[string]any) *feedback {
	fb := &feedback{}
	if s, ok := integer(e["suggestion_score"]); ok && s >= 0 && s <= MaxScore {
		fb.score = &s
		fb.why, _ = text(e["why"])
	}
	start, okStart := integer(e["relevant_lines_start"])
	end, okEnd := integer(e["relevant_lines_end"])
	if okStart && okEnd && start >= 1 && end >= start {
		fb.start, fb.end = &start, &end
	}
	return fb
}
