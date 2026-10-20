package llmrun

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Paths the run never saw (v2.5 spec V-13b §6 item 1, WP-13f): the model
// may name a file in its text that no part of the prompt showed it, such as
// a path it made up from a neighbouring one. Nothing in the text is
// rewritten; a deterministic check only marks what it cannot confirm. It is
// shared by pr_review (the text of every finding) and pr_ask (the answer).

// pathSpanRE matches the whole content of a code span that looks like a
// repository path: an optional R1: to R4: label naming a related item, at
// least one '/', a last segment with an extension of 1 to 8 letters or
// digits, only A–Z a–z 0–9 / . _ -, and an optional :line or :line-line
// suffix, which the check ignores.
var pathSpanRE = regexp.MustCompile(`^(?:(R[1-4]):)?([A-Za-z0-9._/-]*/[A-Za-z0-9._-]*\.[A-Za-z0-9]{1,8})(?::[0-9]+(?:-[0-9]+)?)?$`)

// PathSpan is a code span of a text that looks like a repository path:
// Label is its R1 to R4 label ("" without one) and Path the path without
// the label and the line suffix.
type PathSpan struct {
	Label, Path string
}

// PathSpans returns the code spans of text that look like a repository path
// (pathSpanRE), in order; a span in a fenced code block is not one.
func PathSpans(text string) []PathSpan {
	var out []PathSpan
	for _, c := range mdutil.CodeSpans(text) {
		if m := pathSpanRE.FindStringSubmatch(c); m != nil {
			out = append(out, PathSpan{Label: m[1], Path: m[2]})
		}
	}
	return out
}

// Seen are the files a run showed the model, by where it saw them: the
// files of the primary pull request and its repository (Add), and those of
// each related item under its label, R1 to R4 (AddRelated). The zero value
// is empty and ready to use.
type Seen struct {
	all     map[string]bool
	related map[string]map[string]bool
}

// Add records paths as seen without a label: a file of the primary diff,
// of the repository-context hits, read or matched by a repository tool in
// the primary repository, the guidelines file or a file of the file tree.
func (s *Seen) Add(paths ...string) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if s.all == nil {
			s.all = map[string]bool{}
		}
		s.all[p] = true
	}
}

// AddDiff records the files of the primary diff d as seen: every changed
// file, the old path of a renamed one, and the files the provider skipped.
func (s *Seen) AddDiff(d *provider.Diff) {
	if d == nil {
		return
	}
	for _, f := range d.Files {
		s.Add(f.Path, f.OldPath)
	}
	for _, f := range d.Skipped {
		s.Add(f.Path)
	}
}

// AddRelated records paths as seen in the related item label (R1 to R4): a
// file shown or listed in a related pull request's section, or a file of a
// related repository's kept hits or one a repository tool read or matched
// there. A path without a label is checked against these too.
func (s *Seen) AddRelated(label string, paths ...string) {
	s.Add(paths...)
	for _, p := range paths {
		if p == "" {
			continue
		}
		if s.related == nil {
			s.related = map[string]map[string]bool{}
		}
		if s.related[label] == nil {
			s.related[label] = map[string]bool{}
		}
		s.related[label][p] = true
	}
}

// saw reports whether the run saw sp: in the related item its label names,
// or anywhere without a label.
func (s *Seen) saw(sp PathSpan) bool {
	if sp.Label != "" {
		return s.related[sp.Label][sp.Path]
	}
	return s.all[sp.Path]
}

// Unseen returns the paths of the path-like code spans of texts that the
// run never saw (PathSpans, saw), each once, in the order they first
// appear; nil when there is none. A path is given without its label and
// line suffix.
func (s *Seen) Unseen(texts ...string) []string {
	var out []string
	for _, t := range texts {
		for _, sp := range PathSpans(t) {
			if !s.saw(sp) && !slices.Contains(out, sp.Path) {
				out = append(out, sp.Path)
			}
		}
	}
	return out
}

// MaxUnseenListed is the most paths the mark line names; the rest are
// counted ("and N more").
const MaxUnseenListed = 5

// UnseenLine is the mark line of paths the run never saw:
// "Path not seen in this review: `p`" or "Paths not seen in this review:
// `p`, `q`", at most MaxUnseenListed paths, then "and N more"; "" for none.
// A path holds no backtick (pathSpanRE), so a plain span cannot break.
func UnseenLine(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	head := "Paths not seen in this review: "
	if len(paths) == 1 {
		head = "Path not seen in this review: "
	}
	shown := paths[:min(len(paths), MaxUnseenListed)]
	spans := make([]string, len(shown))
	for i, p := range shown {
		spans[i] = "`" + p + "`"
	}
	line := head + strings.Join(spans, ", ")
	if more := len(paths) - len(shown); more > 0 {
		line += " and " + strconv.Itoa(more) + " more"
	}
	return line
}

// WithUnseen returns text with the mark line of paths as its last line,
// after a blank line; text unchanged when paths is empty.
func WithUnseen(text string, paths []string) string {
	if len(paths) == 0 {
		return text
	}
	return strings.TrimRight(text, " \t\r\n") + "\n\n" + UnseenLine(paths)
}
