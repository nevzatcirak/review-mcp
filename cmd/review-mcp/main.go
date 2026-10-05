// Command review-mcp is an MCP server for AI-powered pull-request tools.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches on the first argument and returns the process exit code.
// stdout is only written for the non-MCP "version" subcommand.
func run(args []string, stdout, stderr io.Writer) int {
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
		logger := logging.New(stderr, slog.LevelInfo)
		logger.Error("stdio server not yet implemented")
		return 1
	case "version":
		fs := flag.NewFlagSet("version", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		bi := version.Info()
		_, _ = fmt.Fprintf(stdout, "review-mcp %s (%s) %s\n", bi.Version, bi.Commit, bi.GoVersion)
		return 0
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
  serve     HTTP mode (not available in this version)
`)
}
