package llmrun

import (
	"context"
	"errors"
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// PublishResult is the outcome of publishing: the posted comment, or the
// classified error. A failed publish never discards the answer.
type PublishResult struct {
	Published bool   `json:"published"`
	CommentID string `json:"comment_id,omitempty"`
	URL       string `json:"url,omitempty"`
	Error     string `json:"error,omitempty"`
}

// PostResult posts the comment body render returns for the provider's
// capabilities and records the outcome in out, which the caller has already
// stored in its result (so a renderer sees a non-nil Publish). It never
// fails the run; a nil render is recorded as a failure. The body is
// slash-sanitised when the provider has QuickActions (provider.SanitizeBody).
// failed is the fixed sentence shown for an error that is not a classified
// provider error.
func PostResult(ctx context.Context, log *slog.Logger, ref provider.PRRef, p provider.Provider,
	out *PublishResult, failed string, render func(caps provider.Capabilities) string) {
	if render == nil {
		out.Error = failed
		log.Debug("publish skipped, no provider renderer")
		return
	}
	caps := p.Capabilities()
	c, err := p.PostComment(ctx, ref, provider.SanitizeBody(caps, render(caps)))
	if err != nil || c == nil {
		msg := failed
		var pe *provider.Error
		if errors.As(err, &pe) {
			msg = pe.Error()
		}
		out.Error = msg
		log.Debug("publish failed", "error", msg)
		return
	}
	out.Published = true
	out.CommentID = c.ID
	out.URL = c.URL
}
