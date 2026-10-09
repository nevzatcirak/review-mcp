package improve

import (
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// blockCode returns the improved code of a verified, anchorable suggestion
// ready for a native suggestion block that replaces the lines StartLine to
// EndLine of fp (design §5, the suggestion-block precondition): the
// improved code re-indented by reindent against the real lines. The real
// lines are the head file's (its complete content) or, for a suggestion
// verified by walking the patch, the patch's new-side lines. ok is false
// when the real lines cannot be read or the re-indentation is not safe;
// the suggestion then keeps the diff block.
func blockCode(fp *provider.FilePatch, s *Suggestion) (string, bool) {
	if fp == nil || s.StartLine == nil || s.EndLine == nil {
		return "", false
	}
	var real []string
	var ok bool
	if fp.HeadStatus == provider.ContentFull && fp.HeadContent != nil {
		real, ok = llmrun.HeadLines(*fp.HeadContent, *s.StartLine, *s.EndLine)
	} else {
		real, ok = llmrun.PatchLines(fp.Patch, *s.StartLine, *s.EndLine)
	}
	if !ok {
		return "", false
	}
	return reindent(real, s.ExistingCode, s.ImprovedCode)
}

// reindent moves improved, written against the model's quote existing, onto
// the indentation of the real lines that quote stands for.
//
// The rule: the indentation of a group of lines is the longest leading
// white-space prefix (spaces and tabs, compared byte for byte) that all its
// non-blank lines share. Let R be that of the real lines and E that of
// existing (after CRLF is made LF).
//
//   - The real lines must equal existing after normalizeSnippet (the
//     verification's comparison); otherwise ok is false.
//   - R == E: the model quoted the lines as they are; improved is returned
//     unchanged.
//   - R starts with E and is longer: the model dedented its quote by
//     P = R[len(E):]. Every non-blank line of improved gets P in front;
//     blank and white-space-only lines stay as they are.
//   - Otherwise ok is false: the model added indentation (E is longer than
//     R), or the prefixes are incompatible (tabs against spaces, or a
//     dedent that removed white space from the middle of R). A wrongly
//     indented block would replace the real lines when applied, so it is
//     never produced; the caller keeps the diff block.
//
// Only the frame of the quote is corrected: indentation inside improved,
// relative to its own lines, is the model's and is kept as written. CRLF in
// improved becomes LF, as the block renders it.
func reindent(real []string, existing, improved string) (string, bool) {
	quote := strings.Split(strings.ReplaceAll(existing, "\r\n", "\n"), "\n")
	if !slices.Equal(normalizeSnippet(real), normalizeSnippet(quote)) {
		return "", false
	}
	r, e := indentation(real), indentation(quote)
	if !strings.HasPrefix(r, e) {
		return "", false
	}
	improved = strings.ReplaceAll(improved, "\r\n", "\n")
	p := r[len(e):]
	if p == "" {
		return improved, true
	}
	lines := strings.Split(improved, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = p + l
		}
	}
	return strings.Join(lines, "\n"), true
}

// indentation is the longest leading white-space prefix that every
// non-blank line shares; "" when there is no non-blank line.
func indentation(lines []string) string {
	prefix, first := "", true
	for _, l := range lines {
		l = strings.TrimRight(l, " \t\r")
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
	return prefix
}
