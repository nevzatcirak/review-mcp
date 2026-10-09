package llmrun

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// FingerprintContentRunes is how much of the content a fingerprint covers,
// in runes (spec P7 §5.3: "the first 200 characters").
const FingerprintContentRunes = 200

// fingerprintHexLen is the length of a fingerprint in hex digits.
const fingerprintHexLen = 12

// Fingerprint identifies a finding or a suggestion across parts and runs
// (spec P7 §5.3, X-13): the first 12 hex digits of the SHA-256 of path, the
// normalized header and the normalized first FingerprintContentRunes runes
// of content, joined by "\n". Normalizing lower-cases and collapses every
// whitespace run to one space, trimming both ends. The line numbers are
// deliberately left out, so that a later commit that moves the code does
// not make the finding new. pr_review passes the finding's header and
// content, pr_improve the suggestion's summary and existing code. It moved
// here from internal/review unchanged.
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
