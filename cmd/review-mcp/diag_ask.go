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

	"github.com/nevzatcirak/review-mcp/internal/ask"
	askrender "github.com/nevzatcirak/review-mcp/internal/ask/render"
	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/tools"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// runDiagAsk parses "diag ask" arguments and runs the command. Usage errors
// (including a missing, empty or over-long question) exit 2 before the
// configuration is loaded, so nothing is sent.
func runDiagAsk(rest []string, stdout, stderr io.Writer, load configLoader) int {
	fs := flag.NewFlagSet("diag ask", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { diagUsage(stderr) }
	question := fs.String("question", "", "the question to answer about the pull request (required, at most 8000 characters)")
	dryRun := fs.Bool("dry-run", false, "run everything up to the model call and print a JSON budget report; the model is not called")
	showPrompt := fs.Bool("show-prompt", false, "print the rendered system and user prompts to stdout after the output")
	publish := fs.Bool("publish", false, "also post the question and answer as a PR comment")
	prURL, ok := parseDiagArgs(fs, rest, stderr)
	if !ok {
		return 2
	}
	if _, err := ask.ValidateQuestion(*question); err != nil {
		// The fixed sentence never contains the question.
		_, _ = fmt.Fprintln(stderr, "diag ask: --question: "+tools.UserMessage(err))
		diagUsage(stderr)
		return 2
	}
	if *dryRun && *publish {
		_, _ = fmt.Fprintln(stderr, "diag ask: --dry-run and --publish cannot be combined (a dry run posts nothing)")
		diagUsage(stderr)
		return 2
	}
	cfg, logger, code := diagSetup(load, stderr)
	if cfg == nil {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	args := ask.Args{PRURL: prURL, Question: *question, Publish: *publish}
	if *dryRun {
		return diagAskDryRun(ctx, cfg, logger, args, *showPrompt, stdout, stderr)
	}
	return diagAsk(ctx, cfg, logger, args, *showPrompt, stdout, stderr)
}

// askDryRunReport is the JSON document of diag ask --dry-run. It has no
// field for the question, prompt, diff or PR text: the prompts are printed
// only with --show-prompt, after the JSON.
type askDryRunReport struct {
	DryRun bool         `json:"dry_run"`
	Empty  bool         `json:"empty"`
	Budget budgetJSON   `json:"budget"`
	Tokens dryRunTokens `json:"tokens"`
	Fast   bool         `json:"fast_path"`
	Cover  ask.Coverage `json:"coverage"`
	Notes  []string     `json:"notes"`
	// ElapsedMS is the time to fetch and prepare.
	ElapsedMS int64 `json:"elapsed_ms"`
}

func buildAskDryRunReport(pl *ask.Plan, elapsed time.Duration) askDryRunReport {
	b, m := pl.Budget, pl.Result.Metadata
	notes := pl.Result.Notes
	if pl.Empty {
		notes = append(append([]string{}, notes...), ask.NoteNoReviewableChanges)
	}
	return askDryRunReport{
		DryRun: true,
		Empty:  pl.Empty,
		Budget: budgetJSON{
			ContextWindow: b.ContextWindow, SoftLimit: b.SoftLimit(), HardLimit: b.HardLimit(),
			PromptTokens: b.PromptTokens, Factor: b.Factor,
		},
		Tokens:    dryRunTokens{Prompt: m.PromptTokens, Diff: m.DiffTokens, Request: m.RequestTokens, ContextWindow: m.ContextWindow},
		Fast:      m.FastPath,
		Cover:     pl.Result.Coverage,
		Notes:     notes,
		ElapsedMS: elapsed.Milliseconds(),
	}
}

// diagAskDryRun runs the ask pipeline up to the model call and prints the
// JSON report. No LLM client is built: nothing can reach the model.
func diagAskDryRun(ctx context.Context, cfg *config.Config, logger *slog.Logger, args ask.Args, showPrompt bool, stdout, stderr io.Writer) int {
	start := time.Now()
	pl, err := ask.Prepare(ctx, ask.Deps{
		Config: cfg, Logger: logger, Resolver: wiring.NewResolver(cfg, logger),
	}, args)
	if err != nil {
		return reportError(stderr, err)
	}
	if err := writeJSON(stdout, buildAskDryRunReport(pl, time.Since(start))); err != nil {
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

// diagAsk runs the full pipeline and prints the client markdown; with
// publish it also posts the provider rendering.
func diagAsk(ctx context.Context, cfg *config.Config, logger *slog.Logger, args ask.Args, showPrompt bool, stdout, stderr io.Writer) int {
	client, err := wiring.NewLLM(cfg, logger)
	if err != nil {
		return reportError(stderr, err)
	}
	rec := &promptRecorder{Completer: client}
	res, err := ask.Run(ctx, ask.Deps{
		Config: cfg, Logger: logger, Resolver: wiring.NewResolver(cfg, logger), LLM: rec,
		RenderProvider: askrender.Provider,
	}, args)
	if err != nil {
		return reportError(stderr, err)
	}
	if _, err := io.WriteString(stdout, askrender.Client(res)); err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the answer to stdout")
		return 1
	}
	if showPrompt && rec.seen {
		if err := writePrompts(stdout, rec.system, rec.user); err != nil {
			_, _ = fmt.Fprintln(stderr, "could not write the prompts to stdout")
			return 1
		}
	}
	if res.Publish != nil {
		if !res.Publish.Published {
			_, _ = fmt.Fprintln(stderr, "diag ask: the answer was not posted: "+res.Publish.Error)
			return 1
		}
		_, _ = fmt.Fprintln(stderr, "diag ask: posted comment "+res.Publish.CommentID)
	}
	return 0
}
