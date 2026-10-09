package review

import (
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
)

// FingerprintContentRunes is how much of issue_content a fingerprint
// covers, in runes (llmrun.FingerprintContentRunes).
const FingerprintContentRunes = llmrun.FingerprintContentRunes

// fingerprintHexLen is the length of a fingerprint in hex digits.
const fingerprintHexLen = 12

// Fingerprint markers. A marker is a CommonMark link reference definition,
// which renders as nothing.
const (
	fingerprintMarkerPrefix = "[//]: # (review-mcp:finding:"
	fingerprintMarkerSuffix = ")"
)

// Fingerprint identifies a finding across runs (spec P7 §5.3): path, the
// header and the content, normalized and hashed by llmrun.Fingerprint
// (shared with pr_improve). The line numbers are deliberately left out.
func Fingerprint(path, header, content string) string {
	return llmrun.Fingerprint(path, header, content)
}

// FingerprintMarker is the marker line of fingerprint fp:
// "[//]: # (review-mcp:finding:<fp>)". It is the last line of every inline
// comment the review posts.
func FingerprintMarker(fp string) string {
	return fingerprintMarkerPrefix + fp + fingerprintMarkerSuffix
}

// ParseFingerprintMarker returns the fingerprint of a comment body whose
// last line, ignoring trailing whitespace, is a fingerprint marker. It is
// the reading side of FingerprintMarker, for the duplicate check of WP-PR-7e.
func ParseFingerprintMarker(body string) (string, bool) {
	body = strings.TrimRight(body, " \t\r\n")
	last := body[strings.LastIndexByte(body, '\n')+1:]
	last = strings.TrimSpace(last)
	fp, ok := strings.CutPrefix(last, fingerprintMarkerPrefix)
	if !ok {
		return "", false
	}
	fp, ok = strings.CutSuffix(fp, fingerprintMarkerSuffix)
	if !ok || len(fp) != fingerprintHexLen {
		return "", false
	}
	for i := 0; i < len(fp); i++ {
		if (fp[i] < '0' || fp[i] > '9') && (fp[i] < 'a' || fp[i] > 'f') {
			return "", false
		}
	}
	return fp, true
}
