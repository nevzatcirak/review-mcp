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
	"github.com/nevzatcirak/review-mcp/internal/describe"
	describerender "github.com/nevzatcirak/review-mcp/internal/describe/render"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// runDiagDescribe parses "diag describe" arguments and runs the command.
// Usage errors exit 2 before the configuration is loaded, so nothing is
// sent. There is no --publish: publishing is WP-2d.
func runDiagDescribe(rest []string, stdout, stderr io.Writer, load configLoader) int {
	fs := flag.NewFlagSet("diag describe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { diagUsage(stderr) }
	dryRun := fs.Bool("dry-run", false, "run everything up to the model calls and print a JSON budget report; the model is not called")
	showPrompt := fs.Bool("show-prompt", false, "print the rendered system and user prompts of the first call to stdout after the output")
	asJSON := fs.Bool("json", false, "print the pr_describe structured result as JSON instead of markdown")
	prURL, ok := parseDiagArgs(fs, rest, stderr)
	if !ok {
		return 2
	}
	if *dryRun && *asJSON {
		_, _ = fmt.Fprintln(stderr, "diag describe: --dry-run already prints JSON; --json cannot be combined with it")
		diagUsage(stderr)
		return 2
	}
	cfg, logger, code := diagSetup(load, stderr)
	if cfg == nil {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	args := describe.Args{PRURL: prURL}
	if *dryRun {
		return diagDescribeDryRun(ctx, cfg, logger, args, *showPrompt, stdout, stderr)
	}
	return diagDescribe(ctx, cfg, logger, args, *showPrompt, *asJSON, stdout, stderr)
}

// describeDryRunReport is the JSON document of diag describe --dry-run. It
// has no field for prompt, diff or PR text: the prompts are printed only
// with --show-prompt, after the JSON.
type describeDryRunReport struct {
	DryRun bool              `json:"dry_run"`
	Empty  bool              `json:"empty"`
	Budget budgetJSON        `json:"budget"`
	Tokens dryRunTokens      `json:"tokens"`
	Fast   bool              `json:"fast_path"`
	Cover  describe.Coverage `json:"coverage"`
	Notes  []string          `json:"notes"`
	// CommitMessages is the number of commit messages read.
	CommitMessages int `json:"commit_messages"`
	// ElapsedMS is the time to fetch and prepare.
	ElapsedMS int64 `json:"elapsed_ms"`
}

func buildDescribeDryRunReport(pl *describe.Plan, elapsed time.Duration) describeDryRunReport {
	b, m := pl.Budget, pl.Result.Metadata
	notes := pl.Result.Notes
	if pl.Empty {
		notes = append(append([]string{}, notes...), describe.NoteNoReviewableChanges)
	}
	return describeDryRunReport{
		DryRun: true,
		Empty:  pl.Empty,
		Budget: budgetJSON{
			ContextWindow: b.ContextWindow, SoftLimit: b.SoftLimit(), HardLimit: b.HardLimit(),
			PromptTokens: b.PromptTokens, Factor: b.Factor, Limit: b.Limit(), MaxDiffTokens: b.MaxDiffTokens,
		},
		Tokens:         dryRunTokens{Prompt: m.PromptTokens, Diff: m.DiffTokens, Request: m.RequestTokens, ContextWindow: m.ContextWindow},
		Fast:           m.FastPath,
		Cover:          pl.Result.Coverage,
		Notes:          notes,
		CommitMessages: m.CommitMessages,
		ElapsedMS:      elapsed.Milliseconds(),
	}
}

// diagDescribeDryRun runs the describe pipeline up to the model calls and
// prints the JSON report. The model is never called; the only LLM request is
// the context-window probe, and only when llm.context_window is unset.
func diagDescribeDryRun(ctx context.Context, cfg *config.Config, logger *slog.Logger, args describe.Args, showPrompt bool, stdout, stderr io.Writer) int {
	start := time.Now()
	client, closeLLM, err := dryRunLLM(cfg, logger)
	if err != nil {
		return reportError(stderr, err)
	}
	defer closeLLM()
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	pl, err := describe.Prepare(ctx, describe.Deps{Config: cfg, Logger: logger, Resolver: resolver, LLM: client}, args)
	if err != nil {
		return reportError(stderr, err)
	}
	if err := writeJSON(stdout, buildDescribeDryRunReport(pl, time.Since(start))); err != nil {
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

// diagDescribe runs the full pipeline and prints the client markdown (or,
// with --json, the structured result).
func diagDescribe(ctx context.Context, cfg *config.Config, logger *slog.Logger, args describe.Args, showPrompt, asJSON bool, stdout, stderr io.Writer) int {
	client, err := wiring.NewLLM(cfg, logger)
	if err != nil {
		return reportError(stderr, err)
	}
	defer closeIdle(client)
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	rec := &promptRecorder{Completer: client}
	res, err := describe.Run(ctx, describe.Deps{Config: cfg, Logger: logger, Resolver: resolver, LLM: rec}, args)
	if err != nil {
		return reportError(stderr, err)
	}
	if asJSON {
		err = writeJSON(stdout, res)
	} else {
		_, err = io.WriteString(stdout, describerender.Client(res))
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the description to stdout")
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
