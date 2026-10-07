package review

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// FingerprintContentRunes is how much of issue_content a fingerprint
// covers, in runes (spec P7 §5.3: "the first 200 characters").
const FingerprintContentRunes = 200

// fingerprintHexLen is the length of a fingerprint in hex digits.
const fingerprintHexLen = 12

// Fingerprint markers. A marker is a CommonMark link reference definition,
// which renders as nothing.
const (
	fingerprintMarkerPrefix = "[//]: # (review-mcp:finding:"
	fingerprintMarkerSuffix = ")"
)

// Fingerprint identifies a finding across runs (spec P7 §5.3): the first
// 12 hex digits of the SHA-256 of path, the normalized header and the
// normalized first FingerprintContentRunes runes of content, joined by
// "\n". Normalizing lower-cases and collapses every whitespace run to one
// space, trimming both ends. The line numbers are deliberately left out, so
// that a later commit that moves the code does not make the finding new.
//
// DESIGN-QUESTION: are "the first 200 chars" bytes or runes, and are they
// taken before or after normalizing? — chose runes, because a byte cut can
// split a multi-byte character and the output language may be any
// language; and the cut is taken from the trimmed content before
// normalizing, as the spec's formula reads (normalized(first 200 chars)).
func Fingerprint(path, header, content string) string {
	content = strings.TrimSpace(content)
	if r := []rune(content); len(r) > FingerprintContentRunes {
		content = string(r[:FingerprintContentRunes])
	}
	sum := sha256.Sum256([]byte(path + "\n" + normalizeForFingerprint(header) + "\n" + normalizeForFingerprint(content)))
	return hex.EncodeToString(sum[:])[:fingerprintHexLen]
}

func normalizeForFingerprint(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
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
