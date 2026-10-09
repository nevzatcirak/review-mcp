package improve

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/yamlrepair"
)

// suggestionKeys configures the YAML repair chain for a suggestion answer:
// upstream's keys_fix_yaml, first_key and last_key of
// _prepare_pr_code_suggestions (pr_code_suggestions.py @ 8e5a929).
var suggestionKeys = yamlrepair.Keys{
	Names: []string{"relevant_file", "suggestion_content", "existing_code", "improved_code"},
	First: "code_suggestions",
	Last:  "label",
}

// reflectKeys configures it for a self-review answer: upstream's
// analyze_self_reflection_response calls load_yaml without keys.
var reflectKeys = yamlrepair.Keys{}

// load parses a model answer (strings.TrimSpace(raw), as upstream's
// _prepare_pr_code_suggestions strips it) and returns the mapping and the
// repair trace's tactic.
func load(raw string, keys yamlrepair.Keys) (map[string]any, string) {
	data, trace := yamlrepair.Load(strings.TrimSpace(raw), keys)
	return data, trace.Tactic
}

// errUnusable is the cause of an answer without the code_suggestions list.
var errUnusable = errors.New("improve: the answer has no code_suggestions list")

// suggestions is the validated content of one suggestion answer.
type suggestions struct {
	cands []Candidate
	// incomplete, unknown and noChange count the entries dropped by the
	// validation (v2 spec §1.6).
	incomplete, unknown, noChange int
}

// convertSuggestions validates a loaded suggestion answer (v2 spec §1.6).
// shown is the set of files whose diff the call was shown: a suggestion for
// any other path is dropped and counted. For a part, shown is that part's
// own files, never the whole pull request's.
//
// An answer without a code_suggestions key, or whose value is neither a
// list nor null, is unusable and gets the one re-ask; a null value (the key
// with nothing after it) is an answer with no suggestion. Upstream treats
// both as a parse failure of the chunk.
//
// Per entry, in this order:
//   - incomplete: no relevant_file, no one_sentence_summary, no
//     existing_code, or no improved_code key (an empty improved_code is a
//     deletion and is kept), as upstream drops an entry without its needed
//     keys;
//   - unknown file: relevant_file (trimmed, matched exactly) not in shown;
//   - no change: existing_code equal to improved_code once every run of
//     white space is folded to one space and the ends are trimmed.
//
// The kept fields: relevant_file and language trimmed (language one line);
// one_sentence_summary one line; suggestion_content trimmed; existing_code
// and improved_code with leading blank lines and trailing white space
// removed (the indentation of the first line is kept); label one line, a
// label containing "critical" replaced by "possible issue" as upstream
// does with focus_only_on_problems, then cut at MaxLabelRunes runes.
func convertSuggestions(data map[string]any, shown map[string]bool) (*suggestions, error) {
	v, ok := data["code_suggestions"]
	if !ok {
		return nil, errUnusable
	}
	out := &suggestions{cands: []Candidate{}}
	if v == nil {
		return out, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, errUnusable
	}
	for _, it := range list {
		e, _ := it.(map[string]any)
		file, _ := text(e["relevant_file"])
		summary := oneLine(e["one_sentence_summary"])
		existing, okExisting := code(e["existing_code"])
		improved, okImproved := code(e["improved_code"])
		if file == "" || summary == "" || !okExisting || existing == "" || !okImproved {
			out.incomplete++
			continue
		}
		if !shown[file] {
			out.unknown++
			continue
		}
		if strings.Join(strings.Fields(existing), " ") == strings.Join(strings.Fields(improved), " ") {
			out.noChange++
			continue
		}
		c := Candidate{File: file, Language: oneLine(e["language"]), ExistingCode: existing, ImprovedCode: improved,
			Summary: summary}
		c.Content, _ = text(e["suggestion_content"])
		c.Label = label(oneLine(e["label"]))
		out.cands = append(out.cands, c)
	}
	return out, nil
}

// label applies upstream's relabelling of focus_only_on_problems
// (_prepare_pr_code_suggestions: a label containing "critical" becomes
// "possible issue", "to be less declarative") and the MaxLabelRunes cap.
func label(l string) string {
	if strings.Contains(strings.ToLower(l), "critical") {
		l = "possible issue"
	}
	r := []rune(l)
	if len(r) <= MaxLabelRunes {
		return l
	}
	return strings.TrimSpace(string(r[:MaxLabelRunes]))
}

// text returns a scalar as trimmed text: strings as they are, numbers and
// booleans in their YAML form. Lists, mappings and null are not text.
func text(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x), true
	case int, int64, float64, bool:
		return fmt.Sprint(x), true
	}
	return "", false
}

// oneLine is text with every run of white space (line breaks included)
// folded to one space; "" when empty or not text.
func oneLine(v any) string {
	s, _ := text(v)
	return strings.Join(strings.Fields(s), " ")
}

// code returns a code snippet: a scalar's text with leading blank lines and
// trailing white space removed, the first line's indentation kept. ok is
// false for a missing key, a list or a mapping.
func code(v any) (string, bool) {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case int, int64, float64, bool:
		s = fmt.Sprint(x)
	default:
		return "", false
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRight(s, " \t\r\n")
	for {
		line, rest, found := strings.Cut(s, "\n")
		if !found || strings.TrimSpace(line) != "" {
			break
		}
		s = rest
	}
	return s, true
}

// integer returns a whole number from a YAML scalar: an int, an integral
// float, or a string holding a decimal integer. Booleans are not numbers.
func integer(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		if x < math.MinInt32 || x > math.MaxInt32 {
			return 0, false
		}
		return int(x), true
	case float64:
		if x != math.Trunc(x) || x < math.MinInt32 || x > math.MaxInt32 {
			return 0, false
		}
		return int(x), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	}
	return 0, false
}
