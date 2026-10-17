package config

import "slices"

// Clone returns a deep copy of c: no slice or pointer of the copy shares
// memory with c, so changing the copy never changes c. A run that merges
// repository settings (X-40) works on such a copy, and the process
// configuration is never mutated. TestCloneSharesNothing fails when a field
// with a slice or pointer is added and not copied here.
func (c *Config) Clone() *Config {
	cp := *c
	cp.LLM.MaxOutputTokens = clonePtr(c.LLM.MaxOutputTokens)
	cp.LLM.Temperature = clonePtr(c.LLM.Temperature)
	cp.LLM.Seed = clonePtr(c.LLM.Seed)
	cp.LLM.ReasoningEffort = clonePtr(c.LLM.ReasoningEffort)
	if c.LLM.Fallback != nil {
		cp.LLM.Fallback = make([]LLMFallback, len(c.LLM.Fallback))
		for i, f := range c.LLM.Fallback {
			f.MaxOutputTokens = clonePtr(f.MaxOutputTokens)
			f.ReasoningEffort = clonePtr(f.ReasoningEffort)
			cp.LLM.Fallback[i] = f
		}
	}
	cp.Diff.SkipExtendExtensions = slices.Clone(c.Diff.SkipExtendExtensions)
	cp.Diff.MaxTokens = clonePtr(c.Diff.MaxTokens)
	cp.Diff.IgnoreGeneratedFrameworks = slices.Clone(c.Diff.IgnoreGeneratedFrameworks)
	cp.Ignore.Glob = slices.Clone(c.Ignore.Glob)
	cp.Ignore.Regex = slices.Clone(c.Ignore.Regex)
	cp.Review.GuidelinesPaths = slices.Clone(c.Review.GuidelinesPaths)
	cp.Serve.AllowedOrigins = slices.Clone(c.Serve.AllowedOrigins)
	return &cp
}
