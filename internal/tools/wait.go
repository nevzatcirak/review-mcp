package tools

import (
	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// InvalidWaitSecondsMessage is the fixed sentence for an out-of-range
// wait_seconds argument (X-6, X-16).
const InvalidWaitSecondsMessage = "wait_seconds must be an integer from 0 to 600"

// WaitSeconds returns how long a stdio pr_review, pr_ask or job_result call
// waits for its result (X-16): the wait_seconds argument when given, else
// llm.wait_seconds (cfg may be nil in a degraded start: the compiled default
// then applies). An argument outside 0-600 is an *ArgumentError.
func WaitSeconds(arg *int, cfg *config.Config) (int, error) {
	if arg != nil {
		if *arg < 0 || *arg > config.MaxWaitSeconds {
			return 0, &ArgumentError{InvalidWaitSecondsMessage}
		}
		return *arg, nil
	}
	if cfg == nil {
		cfg = config.Defaults()
	}
	return cfg.LLM.WaitSeconds, nil
}

// pinResolution resolves rawURL once, before the run, so that a URL that does
// not resolve (or, in serve mode, a missing credential) fails before any
// network I/O and before a background job exists. The returned resolver
// answers that URL with the same result, so the pipeline does not resolve it
// (and build a provider) a second time; any other URL goes to r.
func pinResolution(r PRResolver, rawURL string) (PRResolver, error) {
	ref, p, err := r.Resolve(rawURL)
	if err != nil {
		return nil, err
	}
	return pinnedResolver{inner: r, url: rawURL, ref: ref, p: p}, nil
}

type pinnedResolver struct {
	inner PRResolver
	url   string
	ref   provider.PRRef
	p     provider.Provider
}

func (r pinnedResolver) Resolve(rawURL string) (provider.PRRef, provider.Provider, error) {
	if rawURL == r.url {
		return r.ref, r.p, nil
	}
	return r.inner.Resolve(rawURL)
}
