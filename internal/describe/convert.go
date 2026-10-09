package describe

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/yamlrepair"
)

// repairKeys configures the YAML repair chain: the keys whose values tactic
// 1 forces into block scalars are upstream's keys_fix list of
// PRDescription (pr_description.py @ 8e5a929) without "language", which the
// schema does not have. Upstream's describe path passes no first or last
// key, so the key-window tactic does not run.
var repairKeys = yamlrepair.Keys{Names: []string{"filename", "changes_summary", "changes_title", "description", "title"}}

// load parses a model answer (strings.TrimSpace(raw), as pr_review does)
// and returns the mapping and the repair trace's tactic.
func load(raw string) (map[string]any, string) {
	data, trace := yamlrepair.Load(strings.TrimSpace(raw), repairKeys)
	return data, trace.Tactic
}

// mode is what a call asks for.
type mode int

const (
	// modeFull: a description in one call (title, type, description,
	// pr_files).
	modeFull mode = iota
	// modeFiles: one part of a description in parts (pr_files only); any
	// other field of the answer is ignored.
	modeFiles
	// modeReduce: the reduce call (title, type, description); pr_files is
	// ignored.
	modeReduce
)

// errUnusable is the cause of an answer with nothing the call asked for.
var errUnusable = errors.New("describe: the answer has none of the fields asked for")

// answer is the validated content of one answer.
type answer struct {
	title, description *string
	// types is nil when the answer has no type field.
	types        []string
	typesDropped int
	files        []File
	// returned holds the paths of files.
	returned map[string]bool
	// unknown counts the entries whose path is not among the files shown
	// (or that have no path), duplicates the later entries of a path.
	unknown, duplicates int
}

// notes are the validation notes of the answer (counts only).
func (a *answer) notes() []string {
	var out []string
	if a.typesDropped > 0 {
		out = append(out, noteTypesDropped(a.typesDropped))
	}
	if a.unknown > 0 {
		out = append(out, noteUnknownFiles(a.unknown))
	}
	if a.duplicates > 0 {
		out = append(out, noteDuplicateFiles(a.duplicates))
	}
	return out
}

// convert validates a loaded answer (v2 spec §3.4) for the call's mode.
// shown is the set of files the call was shown: a walkthrough entry for
// any other path is dropped and counted. For a part, shown is that part's
// own files, never the whole pull request's.
//
//   - type: a list (or one comma-separated string) of AllowedTypes,
//     matched without regard to case or to "_" for " "; others are dropped
//     and counted; repeats are dropped silently.
//   - title: one line (line breaks folded to spaces), trimmed; empty is
//     nil. description: trimmed; empty is nil.
//   - pr_files: a list of entries with a filename (trimmed, matched
//     exactly against shown), changes_title (one line), changes_summary
//     (trimmed) and label (one line, at most MaxLabelRunes runes). The first
//     usable entry of a path is kept; an entry with neither a title nor a
//     summary describes nothing and is ignored, so its file counts as not
//     returned.
//
// An answer with none of the fields its mode asks for (modeFiles: a
// pr_files list) is unusable and gets the one re-ask.
func convert(data map[string]any, m mode, shown map[string]bool) (*answer, error) {
	a := &answer{returned: map[string]bool{}}
	usable := false
	if m != modeFiles {
		if v, ok := data["title"]; ok {
			a.title = oneLine(v)
			usable = usable || a.title != nil
		}
		if v, ok := data["description"]; ok {
			if s, ok := text(v); ok && s != "" {
				a.description = &s
				usable = true
			}
		}
		if v, ok := data["type"]; ok && v != nil {
			a.types, a.typesDropped = types(v)
			usable = true
		}
	}
	if m != modeReduce {
		if list, ok := data["pr_files"].([]any); ok {
			usable = true
			a.files = []File{}
			for _, it := range list {
				a.addFile(it, shown)
			}
		} else if m == modeFiles {
			return nil, errUnusable
		}
	}
	if !usable {
		return nil, errUnusable
	}
	if a.files == nil {
		a.files = []File{}
	}
	return a, nil
}

// addFile validates one pr_files entry.
func (a *answer) addFile(it any, shown map[string]bool) {
	e, _ := it.(map[string]any)
	path, _ := text(e["filename"])
	if path == "" || !shown[path] {
		a.unknown++
		return
	}
	if a.returned[path] {
		a.duplicates++
		return
	}
	f := File{Path: path}
	if t := oneLine(e["changes_title"]); t != nil {
		f.Title = *t
	}
	f.Summary, _ = text(e["changes_summary"])
	if f.Title == "" && f.Summary == "" {
		return
	}
	if l := oneLine(e["label"]); l != nil {
		f.Label = capRunes(*l, MaxLabelRunes)
	}
	a.returned[path] = true
	a.files = append(a.files, f)
}

// text returns a scalar as trimmed text: strings as they are, numbers and
// booleans in their YAML form (a title such as "2026" parses as a number).
// Lists, mappings and null are not text.
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
// folded to one space; nil when empty.
func oneLine(v any) *string {
	s, ok := text(v)
	if !ok {
		return nil
	}
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return nil
	}
	return &s
}

func capRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}

// types validates the type field: the canonical AllowedTypes values in the
// answer's order, without repeats, and the count of values dropped.
func types(v any) ([]string, int) {
	var raw []any
	switch x := v.(type) {
	case []any:
		raw = x
	case string:
		for _, s := range strings.Split(x, ",") {
			raw = append(raw, s)
		}
	default:
		raw = []any{x}
	}
	out := []string{}
	dropped := 0
	for _, r := range raw {
		s, _ := text(r)
		canon := ""
		for _, t := range AllowedTypes {
			if strings.EqualFold(strings.ReplaceAll(s, "_", " "), t) {
				canon = t
				break
			}
		}
		switch {
		case canon == "":
			dropped++
		case !slices.Contains(out, canon):
			out = append(out, canon)
		}
	}
	return out, dropped
}
