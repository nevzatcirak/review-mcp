// Command review-mcp is an MCP server for AI-powered pull-request tools.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/mcpserver"
	"github.com/nevzatcirak/review-mcp/internal/version"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the production entry point: it wires the real stdin and the given
// stdout (os.Stdout in main) to runWith, with the configuration read from the
// real environment. It returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	return runWith(args, os.Stdin, stdout, stderr, config.LoadFromOS)
}

// configLoader loads the configuration; see config.Load for its contract.
type configLoader func() (*config.Config, *config.Report, error)

// runWith dispatches on the first argument and returns the process exit code.
// stdin, stdout and load are injectable for tests. stdout carries the MCP
// protocol in stdio mode and is otherwise written only for the "version"
// and "diag" subcommands; all diagnostics go to stderr.
func runWith(args []string, stdin io.Reader, stdout, stderr io.Writer, load configLoader) int {
	cmd := "stdio"
	rest := args
	if len(args) > 0 {
		cmd, rest = args[0], args[1:]
	}

	switch cmd {
	case "stdio":
		fs := flag.NewFlagSet("stdio", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		return runStdio(stdin, stdout, stderr, load)
	case "version":
		fs := flag.NewFlagSet("version", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		bi := version.Info()
		_, _ = fmt.Fprintf(stdout, "review-mcp %s (%s) %s\n", bi.Version, bi.Commit, bi.GoVersion)
		return 0
	case "diag":
		return runDiag(rest, stdout, stderr, load)
	case "serve":
		_, _ = fmt.Fprintln(stderr, "serve mode is not available in this version")
		return 2
	default:
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage: review-mcp [command]

commands:
  stdio     run the MCP server over stdio (default)
  version   print version information
  diag pr <PR_URL> [--show-patch <path>]
            fetch a pull request and print a JSON connectivity report
  diag comment <PR_URL> --body <TEXT>
            post one PR-level comment
  diag comments <PR_URL> [--include-resolved]
            list the comment threads of a pull request as JSON
  diag reply <PR_URL> --comment-id <ID> --body <TEXT>
            reply to a pull request comment
  diag diff <PR_URL> [--mode plain|numbered] [--prompt-tokens N]
            print the prepared (filtered, budgeted) diff and how it was built
  serve     HTTP mode (not available in this version)
`)
}

// runStdio loads the configuration and serves MCP over stdin/stdout.
//
// Degraded start: when the configuration is invalid the server still starts,
// logs the aggregated error to stderr, and reports status "config_invalid"
// through server_info, so a client user can see what to fix instead of an
// opaque "server failed".
func runStdio(stdin io.Reader, stdout, stderr io.Writer, load configLoader) int {
	cfg, rep, loadErr := load()
	logger := logging.New(stderr, logLevel(cfg, loadErr))

	if loadErr != nil {
		// Only *config.ValidationError is known to be secret-free; redact the
		// text of anything else defensively.
		logger.Error("configuration invalid; starting in degraded mode", "error", logging.RedactText(loadErr.Error()))
	}
	kinds := []string{}
	for _, p := range cfg.Summary(rep).Providers {
		kinds = append(kinds, p.Kind)
	}
	bi := version.Info()
	logger.Info("review-mcp starting",
		"version", bi.Version, "providers", kinds, "warnings", len(rep.Warnings), "config_valid", loadErr == nil)
	for _, w := range rep.Warnings {
		logger.Warn(logging.RedactText(w))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := mcpserver.New(mcpserver.Deps{Config: cfg, Report: rep, LoadErr: loadErr, Logger: logger, NewResolver: wiring.NewResolver, NewLLM: wiring.NewLLM})
	err := mcpserver.RunIO(ctx, srv, io.NopCloser(stdin), nopWriteCloser{stdout})
	switch {
	case err == nil:
		logger.Info("session ended")
		return 0
	case errors.Is(err, context.Canceled):
		logger.Info("shutting down on signal")
		return 0
	default:
		logger.Error("server stopped with an error", "error", logging.RedactText(err.Error()))
		return 1
	}
}

// logLevel returns the configured level when the configuration is valid, and
// info otherwise.
func logLevel(cfg *config.Config, loadErr error) slog.Level {
	if loadErr != nil {
		return slog.LevelInfo
	}
	lvl, err := logging.ParseLevel(cfg.Log.Level)
	if err != nil {
		return slog.LevelInfo
	}
	return lvl
}

// nopWriteCloser lets the MCP transport use an io.Writer it must not close
// (closing the real os.Stdout is pointless and closing a test buffer is wrong).
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
