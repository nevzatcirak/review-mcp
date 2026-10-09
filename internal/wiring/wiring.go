// Package wiring is the single place that connects the provider factories to
// the PR-URL resolver (X-2). Every entry point (the MCP server and the diag
// commands) builds its resolver here so that URLs are resolved identically.
package wiring

import (
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/bitbucketserver"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
	"github.com/nevzatcirak/review-mcp/internal/provider/github"
)

// NewResolver builds a Resolver from every provider factory. Providers that
// are not enabled in cfg are ignored by the resolver itself. It performs no
// network I/O.
func NewResolver(cfg *config.Config, logger *slog.Logger) *provider.Resolver {
	return provider.NewResolver(cfg, logger, gitea.NewFactory(), bitbucketserver.NewFactory(), github.NewFactory())
}
