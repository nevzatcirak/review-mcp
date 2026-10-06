package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/serve"
	"github.com/nevzatcirak/review-mcp/internal/version"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// serveLoader loads the configuration for the given options; see
// config.LoadWith.
type serveLoader func(opts config.LoadOptions) (*config.Config, *config.Report, error)

// listenFunc opens the listener (net.Listen in production).
type listenFunc func(network, address string) (net.Listener, error)

// runServe runs "review-mcp serve [--listen host:port]" until ctx is done
// (SIGINT or SIGTERM in production) and returns the exit code.
//
// Unlike stdio there is no degraded start: an invalid configuration is
// logged (token-free) and the command exits 2 before any listener is opened.
// stdout is never written.
func runServe(ctx context.Context, args []string, stderr io.Writer, load serveLoader, listen listenFunc) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listenAddr := fs.String("listen", "", "host:port to listen on (overrides serve.listen)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintln(stderr, "serve takes no arguments; usage: review-mcp serve [--listen host:port]")
		return 2
	}

	cfg, rep, loadErr := load(config.LoadOptions{Mode: config.ModeServe, Listen: *listenAddr})
	logger := logging.New(stderr, logLevel(cfg, loadErr))
	if loadErr != nil {
		// Only *config.ValidationError is known to be secret-free; redact the
		// text of anything else defensively.
		logger.Error("configuration invalid; serve mode does not start", "error", logging.RedactText(loadErr.Error()))
		return 2
	}
	for _, w := range rep.Warnings {
		logger.Warn(logging.RedactText(w))
	}

	srv, err := serve.New(serve.Options{
		Config: cfg, Report: rep, Logger: logger,
		NewResolver: wiring.NewResolver, NewLLM: wiring.NewLLM,
	})
	if err != nil {
		logger.Error("serve mode does not start", "error", logging.RedactText(err.Error()))
		return 2
	}
	ln, err := listen("tcp", cfg.Serve.Listen)
	if err != nil {
		logger.Error("cannot listen", "address", cfg.Serve.Listen, "error", logging.RedactText(err.Error()))
		return 1
	}

	tlsState := "off"
	if srv.TLSEnabled() {
		tlsState = "on"
	}
	logger.Info("review-mcp serving", "url", srv.URL(), "tls", tlsState,
		"llm_key_source", cfg.Serve.LLMKeySource, "version", version.Info().Version)

	if err := srv.Serve(ctx, ln); err != nil {
		logger.Error("server stopped with an error", "error", logging.RedactText(err.Error()))
		return 1
	}
	logger.Info("server stopped")
	return 0
}
