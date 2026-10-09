package improve

import (
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Reasons a suggestion is not verified (Suggestion.UnverifiedReason).
const (
	// UnverifiedNotFound: the quoted code is not at the given lines, and
	// the head file has no match of it (or no range was given and the
	// search found none).
	UnverifiedNotFound = "not_found"
	// UnverifiedAmbiguous: the quoted code is not at the given lines (or no
	// range was given), and the head file has it at more than one place.
	UnverifiedAmbiguous = "ambiguous"
	// UnverifiedHeadUnavailable: the head file's complete content was not
	// fetched (a size or file limit, a fetch failure, a binary file), so
	// nothing could be compared.
	UnverifiedHeadUnavailable = "head_unavailable"
)

// verdict is the outcome of verifying one suggestion.
type verdict struct {
	// start and end are the verified range; meaningful when ok.
	start, end int
	ok         bool
	// corrected: a given range did not match and the unique match in the
	// head file replaced it.
	corrected bool
	// reason is one of the Unverified constants when !ok.
	reason string
}

// verify checks a suggestion's existing code against the head file (v2
// spec §2, Y-10):
//
//   - The head file's complete content is needed (FilePatch.HeadStatus
//     full); without it the suggestion is UnverifiedHeadUnavailable.
//   - A given range (the self-review's new-file lines) that lies inside
//     the file (1 <= start <= end <= the file's line count) and whose
//     lines equal the existing code after normalizeSnippet is verified as
//     it is.
//   - Otherwise, or without a range, the head file is searched for the
//     existing code: every window of as many lines is compared after
//     normalizeSnippet. A unique match gives the range (corrected when a
//     range was given); none is UnverifiedNotFound, several are
//     UnverifiedAmbiguous. The first of several is never taken.
//
// A range outside the file is a range that does not match, so the search
// may still find the code; the range a verified suggestion ends with always
// lies inside the file.
func verify(fp *provider.FilePatch, existing string, start, end *int) verdict {
	if fp == nil || fp.Binary || fp.HeadStatus != provider.ContentFull || fp.HeadContent == nil {
		return verdict{reason: UnverifiedHeadUnavailable}
	}
	file := llmrun.FileLines(*fp.HeadContent)
	want := normalizeSnippet(strings.Split(strings.ReplaceAll(existing, "\r\n", "\n"), "\n"))
	if start != nil && end != nil {
		if s, e := *start, *end; s >= 1 && s <= e && e <= len(file) && slices.Equal(normalizeSnippet(file[s-1:e]), want) {
			return verdict{start: s, end: e, ok: true}
		}
	}
	at, n := search(file, want)
	switch n {
	case 1:
		return verdict{start: at, end: at + len(want) - 1, ok: true, corrected: start != nil}
	case 0:
		return verdict{reason: UnverifiedNotFound}
	default:
		return verdict{reason: UnverifiedAmbiguous}
	}
}

// search finds want (normalized) in the file's lines. It returns the
// 1-based first line of the first match and the number of matches, counted
// up to 2: a second match is enough to make the code ambiguous.
func search(file, want []string) (at, n int) {
	if len(want) == 0 {
		return 0, 0
	}
	for i := 0; i+len(want) <= len(file); i++ {
		w := file[i : i+len(want)]
		if !sameTrimmed(w, want) || !slices.Equal(normalizeSnippet(w), want) {
			continue
		}
		if n == 0 {
			at = i + 1
		}
		n++
		if n == 2 {
			break
		}
	}
	return at, n
}

// sameTrimmed is a cheap filter before normalizeSnippet: the lines are
// equal once their surrounding white space is removed.
func sameTrimmed(a, b []string) bool {
	for i := range a {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}

// normalizeSnippet is the snippet normalisation of the verification (v2
// spec §2, "trailing whitespace, CRLF, common indentation"): a final "\r"
// and the trailing white space of every line are removed, a line of white
// space only becomes empty, and the longest leading white-space prefix that
// every non-empty line shares is removed (the common indentation, as
// Python's textwrap.dedent: tabs and spaces are not converted). The input
// is not changed.
func normalizeSnippet(lines []string) []string {
	out := make([]string, len(lines))
	prefix, first := "", true
	for i, l := range lines {
		l = strings.TrimRight(l, " \t\r")
		out[i] = l
		if l == "" {
			continue
		}
		ind := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		if first {
			prefix, first = ind, false
			continue
		}
		prefix = commonPrefix(prefix, ind)
	}
	if prefix != "" {
		for i, l := range out {
			out[i] = strings.TrimPrefix(l, prefix)
		}
	}
	return out
}

func commonPrefix(a, b string) string {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}

// verifyAll verifies the merged suggestions against the head files (v2
// spec §2) in place: a verified suggestion gets its range and Verified, an
// unverified one keeps the self-review's range (if any) and gets its
// UnverifiedReason. It returns how many given ranges were corrected.
func (pl *Plan) verifyAll(out []Suggestion) (corrected int) {
	unverified := map[string]int{}
	for i := range out {
		s := &out[i]
		v := verify(pl.files[s.File], s.ExistingCode, s.StartLine, s.EndLine)
		if !v.ok {
			s.UnverifiedReason = v.reason
			unverified[v.reason]++
			continue
		}
		start, end := v.start, v.end
		s.StartLine, s.EndLine, s.Verified = &start, &end, true
		if v.corrected {
			corrected++
		}
	}
	pl.log.Debug("improve: suggestions verified", "suggestions", len(out), "corrected", corrected,
		"not_found", unverified[UnverifiedNotFound], "ambiguous", unverified[UnverifiedAmbiguous],
		"head_unavailable", unverified[UnverifiedHeadUnavailable])
	return corrected
}

// noteRangesCorrected: given line ranges that did not match the quoted code
// and were replaced by its unique match in the head file (v2 spec §2.2).
func noteRangesCorrected(n int) string {
	return llmrun.CountPhrase(n, "suggestion line range was", "suggestion line ranges were") + " corrected."
}
