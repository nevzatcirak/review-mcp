package yamlrepair

import (
	"strings"
	"unicode/utf8"
)

// fence is the markdown code fence the prompts end with (DQ-7).
const fence = "```"

// preprocess applies upstream load_yaml's preprocessing to a raw answer, in
// upstream order (porting map §D steps 1-5).
func preprocess(raw string) string {
	// 1. Trim the surrounding newlines.
	text := strings.Trim(raw, "\n")

	// 2. Strip a leading fence whose label is yaml/yml (any case) or empty,
	// but only when the label is the complete info string, so a key such as
	// "yml_config" stays intact. Without such a fence, strip a bare leading
	// "yaml" (upstream strips it even when it starts a longer word).
	unfenced := text
	if end, ok := fenceOpening(text, 0); ok {
		unfenced = text[end:]
	}
	if unfenced == text {
		unfenced = strings.TrimPrefix(text, "yaml")
	}
	text = pyRStrip(unfenced)

	// 3. Drop a sign-off after the wrapper's closing fence.
	text = dropSignOffAfterWrapperFence(text)

	// 4. Strip a closing fence that ends the text.
	if lastLine(text) == fence {
		text = strings.TrimSuffix(text, fence)
	}

	// 5. Delete control characters that can never be YAML content.
	return sanitizeControlChars(text)
}

// fenceOpening matches an opening fence at offset i: "```", optional spaces
// and tabs, an optional yaml/yml label (any case) followed by optional
// spaces and tabs, and then a line break ("\n" or "\r\n"). It returns the
// offset of the line break, which is not part of the match.
//
// This is both upstream's leading-fence pattern in load_yaml and the
// opening half of try_fix_yaml's snippet pattern; the two accept exactly
// the same openings.
func fenceOpening(s string, i int) (int, bool) {
	if !strings.HasPrefix(s[i:], fence) {
		return 0, false
	}
	afterSpace := skipSpaceTab(s, i+len(fence))
	for _, labelEnd := range yamlLabelEnd(s, afterSpace) {
		j := skipSpaceTab(s, labelEnd)
		if _, ok := newlineAt(s, j); ok {
			return j, true
		}
	}
	return 0, false
}

// sanitizeControlChars deletes the C0 control characters other than TAB, LF
// and CR, and DEL (upstream's sanitize_yaml_control_chars). The C1 range
// \x80-\x9f is kept on purpose: the re-encoding tactic needs it to repair
// mojibake. The deleted bytes never occur inside a multi-byte UTF-8
// sequence, so deleting them byte by byte is safe.
func sanitizeControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if isIllegalControl(r) {
			return -1
		}
		return r
	}, s)
}

func isIllegalControl(r rune) bool {
	switch {
	case r <= 0x08, r == 0x0b, r == 0x0c, r >= 0x0e && r <= 0x1f, r == 0x7f:
		return true
	}
	return false
}

// dropSignOffAfterWrapperFence drops a closing remark the model added after
// the wrapper's closing fence (upstream's drop_sign_off_after_wrapper_fence).
// The prompts end with an open fence, so the answer usually carries only a
// closing one, sometimes followed by a sign-off that would otherwise make
// the document unparseable or end up inside the last block scalar.
//
// Only the last content-level fence line counts. Its tail is dropped only
// when it is not more of the answer and the text before the fence parses as
// a mapping.
func dropSignOffAfterWrapperFence(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if pyRStrip(lines[i]) != fence {
			continue
		}
		tail := strings.Join(lines[i+1:], "\n")
		if pyStrip(tail) == "" || looksLikeMoreAnswer(tail) {
			// Nothing to drop, or dropping it would publish a partial
			// answer where a parse failure at least triggers a retry.
			return text
		}
		candidate := strings.Join(lines[:i], "\n")
		if v, err := parse(candidate); err == nil {
			if _, ok := v.(map[string]any); ok {
				return candidate
			}
		}
		return text
	}
	return text
}

// looksLikeMoreAnswer reports whether the text after a fence continues the
// answer rather than signing off (upstream's _looks_like_more_answer): its
// first non-blank line starts with a "key:" or the whole tail parses as a
// mapping or a list.
func looksLikeMoreAnswer(tail string) bool {
	first := ""
	for _, line := range strings.Split(tail, "\n") {
		if pyStrip(line) != "" {
			first = line
			break
		}
	}
	if startsWithKey(first) {
		return true
	}
	v, err := parse(tail)
	if err != nil {
		return false
	}
	switch v.(type) {
	case map[string]any, []any:
		return true
	}
	return false
}

// startsWithKey matches upstream's ^[A-Za-z_][A-Za-z0-9_]*:(\s|$) on one
// line.
func startsWithKey(line string) bool {
	i := 0
	for i < len(line) {
		c := line[i]
		isLetter := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_'
		if !isLetter && (i == 0 || c < '0' || c > '9') {
			break
		}
		i++
	}
	if i == 0 || i >= len(line) || line[i] != ':' {
		return false
	}
	rest := line[i+1:]
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return pyIsSpace(r)
}
