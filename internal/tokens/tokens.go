// Package tokens provides the offline token estimator, the diff-content
// budget arithmetic and the verified clipping used by the diff pipeline.
//
// Every count is an estimate: one o200k_base BPE count scaled by a safety
// factor (DQ-5). The BPE vocabulary is compiled into the binary by
// github.com/tiktoken-go/tokenizer, so nothing here ever touches the network
// (leak-first stance, X-8).
//
// Tokenizer module version: v0.8.1 (architect decision D1). It requires
// go 1.26, which this module now declares, and golangci-lint v2.14.0 (built
// with Go 1.27) lints it. v0.8.1 keeps o200k_base embedded in the binary and
// has no network path, and counts are identical to v0.7.0 and to the Python
// tiktoken oracle. Its regexp2/v2 splitter with generated matching code is
// about 2.6x faster than v0.7.0. One shared codec is used on purpose: a
// codec per worker (sync.Pool) was benchmarked and was slower, since the
// codec is read-only after construction and regexp2 pools its runners.
package tokens

import (
	"errors"
	"math"
	"strings"
	"sync"

	"github.com/tiktoken-go/tokenizer"
)

const (
	// minReserve is the hard output reserve floor (DQ-4); upstream's
	// OUTPUT_BUFFER_TOKENS_HARD_THRESHOLD.
	minReserve = 1000
	// softExtra is added on top of the hard reserve to get the soft reserve
	// (DQ-4); upstream's soft threshold is 1500 = 1000 + 500.
	softExtra = 500
	// messageFraming is the per-message framing allowance and replyFraming the
	// once-per-request reply allowance; upstream's MESSAGE_FRAMING_TOKEN_ALLOWANCE
	// and REPLY_FRAMING_TOKEN_ALLOWANCE.
	messageFraming = 16
	replyFraming   = 16
	// clipSafety is the 10% safety factor of the clipping heuristic.
	clipSafety = 0.9
	// clipShrink is the geometric shrink step used while verifying a clip.
	clipShrink = 0.9
)

// TruncationMarker is appended to clipped text. It matches upstream
// clip_tokens at the pinned commit (see NOTICE): a newline followed by
// "...(truncated)" with no trailing newline.
const TruncationMarker = "\n...(truncated)"

// ErrDoesNotFit reports that the diff-content budget is exhausted before any
// diff text is added (the FallbackEligible seam of DQ-9).
var ErrDoesNotFit = errors.New("tokens: prompt and output reserve leave no room for diff content")

var (
	codecOnce sync.Once
	codec     tokenizer.Codec
	codecErr  error
)

func loadCodec() (tokenizer.Codec, error) {
	codecOnce.Do(func() {
		codec, codecErr = tokenizer.Get(tokenizer.O200kBase)
	})
	return codec, codecErr
}

// Raw returns the plain o200k_base BPE token count of text. Special-token
// spellings such as "<|endoftext|>" are counted as ordinary text.
//
// If the embedded codec fails to load
// or encode, Raw returns the UTF-8 byte length (an upper bound on the BPE count,
// since a token covers at least one byte) because it fails conservative
// rather than undercounting and the embedded vocabulary makes this path
// unreachable in practice.
func Raw(text string) int {
	if text == "" {
		return 0
	}
	c, err := loadCodec()
	if err != nil {
		return len(text)
	}
	n, err := c.Count(text)
	if err != nil {
		return len(text)
	}
	return n
}

// Estimate returns ceil(Raw(text) * (1 + factor)). Every budget decision
// uses Estimate (DQ-5).
func Estimate(text string, factor float64) int {
	return scale(Raw(text), factor)
}

func scale(raw int, factor float64) int {
	return int(math.Ceil(float64(raw) * (1 + factor)))
}

// Budget holds the inputs of the diff-content budget and derives the limits
// from them (DQ-3, DQ-4).
type Budget struct {
	// ContextWindow is llm.context_window.
	ContextWindow int
	// MaxOutputTokens is llm.max_output_tokens; nil means unset.
	MaxOutputTokens *int
	// PromptTokens is the estimate of the rendered prompt scaffolding with an
	// empty diff. The prompt layer computes it.
	PromptTokens int
	// Factor is llm.token_estimate_factor.
	Factor float64
}

// HardReserve is max(MaxOutputTokens or 0, 1000).
func (b Budget) HardReserve() int {
	r := 0
	if b.MaxOutputTokens != nil {
		r = *b.MaxOutputTokens
	}
	return max(r, minReserve)
}

// SoftReserve is HardReserve + 500.
func (b Budget) SoftReserve() int { return b.HardReserve() + softExtra }

// SoftLimit is the diff-content budget for the fast path and per-file
// admission: ContextWindow - SoftReserve - PromptTokens. It may be <= 0.
func (b Budget) SoftLimit() int {
	return b.ContextWindow - b.SoftReserve() - b.PromptTokens
}

// HardLimit is the stop-adding ceiling: ContextWindow - HardReserve -
// PromptTokens. It may be <= 0.
func (b Budget) HardLimit() int {
	return b.ContextWindow - b.HardReserve() - b.PromptTokens
}

// RequireCapacity returns ErrDoesNotFit when SoftLimit is <= 0.
func (b Budget) RequireCapacity() error {
	if b.SoftLimit() <= 0 {
		return ErrDoesNotFit
	}
	return nil
}

// RequestTokens estimates a two-message request: both messages plus the
// framing allowance of two messages and one reply (3 x 16).
func RequestTokens(system, user string, factor float64) int {
	return Estimate(system, factor) + Estimate(user, factor) + 2*messageFraming + replyFraming
}

// Clip shortens text so that Estimate(result, factor) <= maxTokens.
//
//   - maxTokens <= 0 returns "".
//   - Text that already fits is returned unchanged.
//   - Otherwise a heuristic cut keeps chars = 0.9 * runes/estimate * maxTokens
//     runes (length is measured in runes, not bytes, and never splits a
//     rune), optionally backs up to the last newline, and appends
//     TruncationMarker.
//   - The result is then verified by an exact recount, including the marker,
//     and shrunk in 10% steps until it fits. BPE counts are not additive or
//     monotone across a cut, so the heuristic alone is never trusted.
//
// If nothing fits, Clip returns "".
func Clip(text string, maxTokens int, factor float64, deleteLastLine bool) string {
	if maxTokens <= 0 || text == "" {
		return ""
	}
	est := Estimate(text, factor)
	if est <= maxTokens {
		return text
	}
	runes := []rune(text)
	chars := int(clipSafety * float64(len(runes)) / float64(est) * float64(maxTokens))
	for chars > 0 {
		cand := cut(runes, chars, deleteLastLine) + TruncationMarker
		if Estimate(cand, factor) <= maxTokens {
			return cand
		}
		// Strictly decreasing, so the loop terminates.
		chars = min(chars-1, int(float64(chars)*clipShrink))
	}
	return ""
}

// cut returns the first n runes as a string; with deleteLastLine it drops
// everything from the last newline on (unchanged when there is no newline,
// as upstream does).
func cut(runes []rune, n int, deleteLastLine bool) string {
	s := string(runes[:min(n, len(runes))])
	if deleteLastLine {
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			s = s[:i]
		}
	}
	return s
}

// ClipDescription clips a PR description to maxTokens
// (diff.max_description_tokens).
func ClipDescription(text string, maxTokens int, factor float64) string {
	return Clip(text, maxTokens, factor, false)
}

// ClipCommits clips the commit-message text to maxTokens
// (diff.max_commits_tokens).
func ClipCommits(text string, maxTokens int, factor float64) string {
	return Clip(text, maxTokens, factor, false)
}
