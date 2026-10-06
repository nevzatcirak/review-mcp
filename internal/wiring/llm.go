package wiring

import (
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// NewLLM builds the chat client of one review from the effective
// configuration (llm.* and the LLM API key secret). It is request-scoped:
// callers build one per review and keep nothing. It performs no network
// I/O, and its errors never contain the key.
func NewLLM(cfg *config.Config, logger *slog.Logger) (review.Completer, error) {
	c, err := llm.New(cfg.LLM, cfg.Secrets.LLMAPIKey, logger)
	if err != nil {
		return nil, err
	}
	return c, nil
}
