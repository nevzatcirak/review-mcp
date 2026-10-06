package main

import (
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
)

// newResolver is the single place that wires the provider factories into the
// PR-URL resolver (X-2). The diag commands use it today; the MCP tools reuse
// it so every entry point resolves URLs identically.
func newResolver(cfg *config.Config, logger *slog.Logger) *provider.Resolver {
	return provider.NewResolver(cfg, logger, gitea.NewFactory(), bitbucketserver.NewFactory())
}
