package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/improve"
	improverender "github.com/nevzatcirak/review-mcp/internal/improve/render"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// runDiagImprove parses "diag improve" arguments and runs the command.
// Usage errors exit 2 before the configuration is loaded, so nothing is
// sent. There is no --publish: diag improve never posts.
func runDiagImprove(rest []string, stdout, stderr io.Writer, load configLoader) int {
	fs := flag.NewFlagSet("diag improve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { diagUsage(stderr) }
	dryRun := fs.Bool("dry-run", false, "run everything up to the model calls and print a JSON budget report; the model is not called")
	showPrompt := fs.Bool("show-prompt", false, "print the rendered system and user prompts of the first suggestion call to stdout after the output")
	asJSON := fs.Bool("json", false, "print the pr_improve structured result as JSON instead of markdown")
	prURL, ok := parseDiagArgs(fs, rest, stderr)
	if !ok {
		return 2
	}
	if *dryRun && *asJSON {
		_, _ = fmt.Fprintln(stderr, "diag improve: --dry-run already prints JSON; --json cannot be combined with it")
		diagUsage(stderr)
		return 2
	}
	cfg, logger, code := diagSetup(load, stderr)
	if cfg == nil {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	args := improve.Args{PRURL: prURL}
	if *dryRun {
		return diagImproveDryRun(ctx, cfg, logger, args, *showPrompt, stdout, stderr)
	}
	return diagImprove(ctx, cfg, logger, args, *showPrompt, *asJSON, stdout, stderr)
}

// improveDryRunReport is the JSON document of diag improve --dry-run. It
// has no field for prompt, diff or PR text: the prompts are printed only
// with --show-prompt, after the JSON.
type improveDryRunReport struct {
	DryRun bool             `json:"dry_run"`
	Empty  bool             `json:"empty"`
	Budget budgetJSON       `json:"budget"`
	Tokens dryRunTokens     `json:"tokens"`
	Fast   bool             `json:"fast_path"`
	Cover  improve.Coverage `json:"coverage"`
	Notes  []string         `json:"notes"`
	// AlreadyDiscussed is the number of discussion threads in the prompt.
	AlreadyDiscussed int `json:"already_discussed"`
	// RepoContext is the repository-context detail; absent when it is off.
	RepoContext *repoctx.Report `json:"repo_context,omitempty"`
	// ElapsedMS is the time to fetch and prepare.
	ElapsedMS int64 `json:"elapsed_ms"`
}

func buildImproveDryRunReport(pl *improve.Plan, elapsed time.Duration) improveDryRunReport {
	b, m := pl.Budget, pl.Result.Metadata
	notes := pl.Result.Notes
	if pl.Empty {
		notes = append(append([]string{}, notes...), improve.NoteNoReviewableChanges)
	}
	return improveDryRunReport{
		DryRun: true,
		Empty:  pl.Empty,
		Budget: budgetJSON{
			ContextWindow: b.ContextWindow, SoftLimit: b.SoftLimit(), HardLimit: b.HardLimit(),
			PromptTokens: b.PromptTokens, Factor: b.Factor, Limit: b.Limit(), MaxDiffTokens: b.MaxDiffTokens,
		},
		Tokens:           dryRunTokens{Prompt: m.PromptTokens, Diff: m.DiffTokens, Request: m.RequestTokens, ContextWindow: m.ContextWindow},
		Fast:             m.FastPath,
		Cover:            pl.Result.Coverage,
		Notes:            notes,
		AlreadyDiscussed: m.AlreadyDiscussed,
		RepoContext:      pl.RepoReport,
		ElapsedMS:        elapsed.Milliseconds(),
	}
}

// diagImproveDryRun runs the improve pipeline up to the model calls and
// prints the JSON report. The model is never called; the only LLM request is
// the context-window probe, and only when llm.context_window is unset.
func diagImproveDryRun(ctx context.Context, cfg *config.Config, logger *slog.Logger, args improve.Args, showPrompt bool, stdout, stderr io.Writer) int {
	start := time.Now()
	client, closeLLM, err := dryRunLLM(cfg, logger)
	if err != nil {
		return reportError(stderr, err)
	}
	defer closeLLM()
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	pl, err := improve.Prepare(ctx, improve.Deps{Config: cfg, Logger: logger, Resolver: resolver, LLM: client}, args)
	if err != nil {
		return reportError(stderr, err)
	}
	if err := writeJSON(stdout, buildImproveDryRunReport(pl, time.Since(start))); err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the report to stdout")
		return 1
	}
	if showPrompt && !pl.Empty {
		if err := writePrompts(stdout, pl.Prompts.System, pl.Prompts.User); err != nil {
			_, _ = fmt.Fprintln(stderr, "could not write the prompts to stdout")
			return 1
		}
	}
	return 0
}

// diagImprove runs the full pipeline and prints the client markdown (or,
// with --json, the structured result). Nothing is posted.
func diagImprove(ctx context.Context, cfg *config.Config, logger *slog.Logger, args improve.Args, showPrompt, asJSON bool, stdout, stderr io.Writer) int {
	client, err := wiring.NewLLM(cfg, logger)
	if err != nil {
		return reportError(stderr, err)
	}
	defer closeIdle(client)
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	rec := &promptRecorder{Completer: client}
	res, err := improve.Run(ctx, improve.Deps{Config: cfg, Logger: logger, Resolver: resolver, LLM: rec}, args)
	if err != nil {
		return reportError(stderr, err)
	}
	if asJSON {
		err = writeJSON(stdout, res)
	} else {
		_, err = io.WriteString(stdout, improverender.Client(res))
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the suggestions to stdout")
		return 1
	}
	if showPrompt && rec.seen {
		if err := writePrompts(stdout, rec.system, rec.user); err != nil {
			_, _ = fmt.Fprintln(stderr, "could not write the prompts to stdout")
			return 1
		}
	}
	return 0
}
