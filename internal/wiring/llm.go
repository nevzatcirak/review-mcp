package wiring

import (
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// NewLLM builds the model client of one run from the effective
// configuration (llm.*, the LLM API key secret and the fallback models). It
// is request-scoped: callers build one per run and keep nothing; the
// endpoints' rate limiters are the process's (X-37). It performs no network
// I/O, and its errors never contain a key.
//
// The client is the run's fallback chain (X-38): the primary model, then
// each fallback with its effective configuration and the key the key rule
// gives it (config.FallbackKey): its own key, the primary key on the
// primary's base URL only, or no key at all, so that the primary key never
// reaches another host.
func NewLLM(cfg *config.Config, logger *slog.Logger) (review.Completer, error) {
	return newLLM(cfg, logger)
}

func newLLM(cfg *config.Config, logger *slog.Logger, opts ...llm.Option) (*llm.Chain, error) {
	primary, err := llm.New(cfg.LLM, cfg.Secrets.LLMAPIKey, logger, opts...)
	if err != nil {
		return nil, err
	}
	fallbacks := make([]*llm.Client, 0, len(cfg.LLM.Fallback))
	for i := range cfg.LLM.Fallback {
		key, _ := cfg.FallbackKey(i)
		fo := opts
		if !key.IsSet() {
			fo = append(append([]llm.Option(nil), opts...), llm.WithoutKey())
		}
		c, err := llm.New(cfg.EffectiveFallback(i), key, logger, fo...)
		if err != nil {
			return nil, err
		}
		fallbacks = append(fallbacks, c)
	}
	return llm.NewChain(primary, fallbacks, cfg.LLM.TokenEstimateFactor), nil
}
