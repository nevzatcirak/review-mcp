// Package mcpserver adapts the transport-agnostic tools to the MCP Go SDK. It
// is the only package that imports the SDK.
//
// Stdout belongs to the MCP protocol: nothing in this package writes to it
// except through an MCP transport, and logs go to a caller-supplied
// *slog.Logger (stderr in the real binary).
package mcpserver

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/tools"
	"github.com/nevzatcirak/review-mcp/internal/version"
)

// Deps are the dependencies of the registered tools.
type Deps struct {
	// Config and Report come from config.Load. They may be best-effort values
	// when LoadErr is non-nil (degraded start).
	Config *config.Config
	Report *config.Report
	// LoadErr is the error returned by config.Load, if any.
	LoadErr error
	// Logger receives the SDK's own diagnostics. Nil discards them.
	Logger *slog.Logger
}

// serverInfoDescription is the one-sentence tool description.
const serverInfoDescription = "Reports the review-mcp version, enabled providers, and the effective non-secret configuration, including configuration problems."

// New builds an MCP server with every tool registered. The transport is
// chosen by the caller, so a future HTTP mode can reuse it.
func New(deps Deps) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: tools.ServerName, Version: version.Info().Version},
		&mcp.ServerOptions{Logger: deps.Logger},
	)
	registerServerInfo(s, deps)
	return s
}

// serverInfoInput is empty: server_info takes no arguments.
type serverInfoInput struct{}

func registerServerInfo(s *mcp.Server, deps Deps) {
	f := false
	mcp.AddTool(s, &mcp.Tool{
		Name:        "server_info",
		Description: serverInfoDescription,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			DestructiveHint: &f,
			OpenWorldHint:   &f,
		},
	}, func(context.Context, *mcp.CallToolRequest, serverInfoInput) (*mcp.CallToolResult, tools.ServerInfoResult, error) {
		res := tools.ServerInfo(deps.Config, deps.Report, deps.LoadErr)
		// Content carries the markdown; the SDK fills StructuredContent from
		// the returned value and validates it against the inferred schema.
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: tools.RenderServerInfoMarkdown(res)}},
		}, res, nil
	})
}

// RunStdio runs s over the process's real stdin and stdout until the client
// disconnects (stdin EOF) or ctx is canceled.
func RunStdio(ctx context.Context, s *mcp.Server) error {
	return RunIO(ctx, s, os.Stdin, os.Stdout)
}

// RunIO is RunStdio over arbitrary streams, so tests can drive the stdio code
// path without touching the process's standard streams. in and out are closed
// when the session ends.
func RunIO(ctx context.Context, s *mcp.Server, in io.ReadCloser, out io.WriteCloser) error {
	return s.Run(ctx, &mcp.IOTransport{Reader: in, Writer: out})
}
