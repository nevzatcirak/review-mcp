package describe

import "strings"

// The markers of the description region (v2 spec §4, Y-5). Both are CommonMark
// link reference definitions, which render as nothing.
const (
	// RegionStart opens the region review-mcp owns in the PR description.
	RegionStart = "[//]: # (review-mcp:describe:start)"
	// RegionEnd closes it.
	RegionEnd = "[//]: # (review-mcp:describe:end)"
	// CommentMarker is the last line of the description comment
	// (publish_mode=comment).
	CommentMarker = "[//]: # (review-mcp:describe:v1)"
)

// Fixed sentences of the publish outcomes that are not provider errors (X-6).
const (
	// MsgNoDescriptionEdit: the provider cannot edit the PR description.
	MsgNoDescriptionEdit = "This provider does not support editing the pull request description; use publish_mode=comment."
	// MsgNothingDescribed: no file was described and there is no summary,
	// so there is nothing to put into the description.
	MsgNothingDescribed = "Nothing was described, so the pull request description was not changed."
	// MsgDamagedRegion: the description has a region that is not exactly
	// one start marker followed by one end marker.
	MsgDamagedRegion = "The pull request description contains a damaged review-mcp region; fix or remove it and run again."
	// MsgChangedWhileUpdating: the description changed twice while it was
	// being updated (or a version conflict repeated); nothing was written.
	MsgChangedWhileUpdating = "The pull request description changed while it was being updated; nothing was written."
	// publishFailedMessage is shown for a publish error that is not a
	// classified provider error.
	publishFailedMessage = "the description could not be published to the pull request"
)

// damagedRegionError reports a description whose region is damaged; its text
// is the fixed sentence MsgDamagedRegion.
type damagedRegionError struct{}

func (damagedRegionError) Error() string { return MsgDamagedRegion }

var errDamagedRegion error = damagedRegionError{}

// Lines and markers. A line is the run of bytes up to a "\n" or the end of
// the text; a "\r" immediately before the "\n" (CRLF) belongs to the
// terminator, not to the line, and a lone "\r" is ordinary content. A line
// is a marker when, after trimming spaces and tabs from both ends, it equals
// the marker exactly (case-sensitive): a CRLF description, trailing spaces
// or an editor's indentation do not hide a marker, and nothing else on the
// line is tolerated. Fenced code blocks get no special treatment: a marker
// line inside one still counts, because tracking fences would let an
// unclosed fence in the author's text hide our own region on the next run
// and append a second one (the refusal below then asks for a fix, which
// writes nothing).

// textLine is one line of a text: Content is text[start:end], without its
// terminator; Next is where the following line starts.
type textLine struct{ start, end, next int }

func splitLines(text string) []textLine {
	var out []textLine
	for i := 0; i < len(text); {
		j := strings.IndexByte(text[i:], '\n')
		if j < 0 {
			out = append(out, textLine{i, len(text), len(text)})
			break
		}
		j += i
		end := j
		if end > i && text[end-1] == '\r' {
			end--
		}
		out = append(out, textLine{i, end, j + 1})
		i = j + 1
	}
	return out
}

func isMarker(text string, l textLine, marker string) bool {
	return strings.Trim(text[l.start:l.end], " \t") == marker
}

// ApplyRegion returns the PR description text with the managed region set
// to body, and whether an existing region was replaced.
//
//   - No marker at all: the region is appended after the author's text,
//     separated from it by one blank line; if the text already ends in two
//     or more line terminators nothing is added (the author's bytes are
//     never trimmed). An empty text becomes the region alone.
//   - Exactly one start marker line followed by one end marker line: the
//     bytes from the beginning of the start line to the end of the end
//     line's content are replaced. The terminator of the end line and
//     everything before and after are untouched, byte for byte.
//   - Anything else (two or more starts, an end before a start, an end
//     without a start, a start without an end, a second end): errDamagedRegion
//     and no text.
//
// The region is written with the line terminator of the text: CRLF when the
// first line break of the text is CRLF, else LF. body is LF text (a trailing
// newline is dropped).
func ApplyRegion(text, body string) (out string, replaced bool, err error) {
	lines := splitLines(text)
	var starts, ends []int
	for i, l := range lines {
		switch {
		case isMarker(text, l, RegionStart):
			starts = append(starts, i)
		case isMarker(text, l, RegionEnd):
			ends = append(ends, i)
		}
	}
	eol := "\n"
	if i := strings.IndexByte(text, '\n'); i > 0 && text[i-1] == '\r' {
		eol = "\r\n"
	}
	region := RegionStart + "\n" + strings.TrimRight(body, "\r\n") + "\n" + RegionEnd
	region = strings.ReplaceAll(region, "\n", eol)

	switch {
	case len(starts) == 0 && len(ends) == 0:
		return text + separator(text, eol) + region, false, nil
	case len(starts) == 1 && len(ends) == 1 && starts[0] < ends[0]:
		return text[:lines[starts[0]].start] + region + text[lines[ends[0]].end:], true, nil
	}
	return "", false, errDamagedRegion
}

// separator returns the line terminators to put between text and a block
// appended to it so that one blank line separates them.
func separator(text, eol string) string {
	if text == "" {
		return ""
	}
	n := 0
	for t := text; ; n++ {
		if rest, ok := strings.CutSuffix(t, "\r\n"); ok {
			t = rest
		} else if rest, ok := strings.CutSuffix(t, "\n"); ok {
			t = rest
		} else {
			break
		}
	}
	if n >= 2 {
		return ""
	}
	return strings.Repeat(eol, 2-n)
}
