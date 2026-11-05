package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/nevzatcirak/review-mcp/internal/tools"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// runDiagList implements "review-mcp diag list review-requests|authored
// [--provider K] [--limit N] [--include-assigned] [--json]": the engine of
// the pr_list_review_requests and pr_list_authored tools, over the
// configured providers. It prints the client markdown, or with --json the
// structured result. Exit 0 when no provider failed, 1 when any provider is
// failed (or the call could not run), 2 on usage.
//
// DESIGN-QUESTION (WP-19b DQ-6): the spec's exit codes name "ok" and
// "failed" only. A provider that is unsupported (its host cannot list the
// role) does not make the exit code 1: nothing failed, and the status says
// so. An argument the engine refuses (a limit outside 1..100, a provider
// that is unknown or not configured) is a usage error, 2; "no provider is
// configured" is 1. Recommendation: keep.
func runDiagList(args []string, stdout, stderr io.Writer, load configLoader) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "diag list: expected review-requests or authored")
		diagUsage(stderr)
		return 2
	}
	var role string
	switch args[0] {
	case "review-requests":
		role = tools.ListRoleReviewRequests
	case "authored":
		role = tools.ListRoleAuthored
	default:
		_, _ = fmt.Fprintln(stderr, "diag list: expected review-requests or authored")
		diagUsage(stderr)
		return 2
	}
	fs := flag.NewFlagSet("diag list "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { diagUsage(stderr) }
	prov := fs.String("provider", "", "restrict the list to one `kind`: gitea, bitbucket_server, github or gitlab")
	limit := fs.Int("limit", tools.DefaultListLimit, "most pull requests to list, 1 to 100")
	asJSON := fs.Bool("json", false, "print the structured result as JSON instead of markdown")
	var includeAssigned *bool
	if role == tools.ListRoleReviewRequests {
		includeAssigned = fs.Bool("include-assigned", false, "also list pull requests assigned to you")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "diag list: takes no arguments besides its flags")
		diagUsage(stderr)
		return 2
	}
	a := tools.PRListArgs{Role: role, Provider: *prov}
	// An absent --limit is the default; a given one is checked by the
	// engine, so --limit 0 is refused rather than read as unset.
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "limit" {
			a.Limit = limit
		}
	})
	if includeAssigned != nil {
		a.IncludeAssigned = *includeAssigned
	}

	cfg, logger, code := diagSetup(load, stderr)
	if cfg == nil {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	res, err := tools.PRList(ctx, tools.ListSourceOf(resolver), a, logger)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, tools.UserMessage(err))
		var ae *tools.ArgumentError
		if errors.As(err, &ae) && ae.Error() != tools.ListNoProviderMessage {
			return 2
		}
		return 1
	}
	if *asJSON {
		if err := writeJSON(stdout, res); err != nil {
			_, _ = fmt.Fprintln(stderr, "could not write the result to stdout")
			return 1
		}
	} else if _, err := io.WriteString(stdout, tools.RenderPRListMarkdown(res)); err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the result to stdout")
		return 1
	}
	for _, p := range res.Providers {
		if p.Status == tools.ListStatusFailed {
			return 1
		}
	}
	return 0
}
