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
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/review/render"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// Separator lines of diag review. The prompts follow the main output of the
// command, each under its own line; they go to stdout only, never to the log.
const (
	systemPromptSeparator = "--- system prompt ---"
	userPromptSeparator   = "--- user prompt ---"
)

// runDiagReview parses "diag review" arguments and runs the command. Usage
// errors exit 2 before the configuration is loaded, so nothing is sent.
func runDiagReview(rest []string, stdout, stderr io.Writer, load configLoader) int {
	fs := flag.NewFlagSet("diag review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { diagUsage(stderr) }
	dryRun := fs.Bool("dry-run", false, "run everything up to the model call and print a JSON budget report; the model is not called")
	showPrompt := fs.Bool("show-prompt", false, "print the rendered system and user prompts to stdout after the output")
	publish := fs.Bool("publish", false, "also post the review as a PR comment")
	asJSON := fs.Bool("json", false, "print the review result as JSON (the pr_review structured result) instead of markdown")
	repoCtx := fs.String("repo-context", "", "`on` or `off`: override context.repo.enabled for this run (repository context, stdio only)")
	prURL, ok := parseDiagArgs(fs, rest, stderr)
	if !ok {
		return 2
	}
	if *repoCtx != "" && *repoCtx != "on" && *repoCtx != "off" {
		_, _ = fmt.Fprintln(stderr, "diag review: --repo-context must be on or off")
		diagUsage(stderr)
		return 2
	}
	if *dryRun && *asJSON {
		_, _ = fmt.Fprintln(stderr, "diag review: --dry-run already prints JSON; --json cannot be combined with it")
		diagUsage(stderr)
		return 2
	}
	if *dryRun && *publish {
		_, _ = fmt.Fprintln(stderr, "diag review: --dry-run and --publish cannot be combined (a dry run posts nothing)")
		diagUsage(stderr)
		return 2
	}
	cfg, logger, code := diagSetup(load, stderr)
	if cfg == nil {
		return code
	}
	switch *repoCtx {
	case "on":
		cfg.Context.Repo.Enabled = true
	case "off":
		cfg.Context.Repo.Enabled = false
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *dryRun {
		return diagReviewDryRun(ctx, cfg, logger, prURL, *showPrompt, stdout, stderr)
	}
	return diagReview(ctx, cfg, logger, prURL, *publish, *showPrompt, *asJSON, stdout, stderr)
}

// dryRunReport is the JSON document of diag review --dry-run. It has no field
// for prompt, diff or PR text: the prompts are printed only with
// --show-prompt, after the JSON.
type dryRunReport struct {
	DryRun bool            `json:"dry_run"`
	PR     review.PRInfo   `json:"pr"`
	Empty  bool            `json:"empty"`
	Budget budgetJSON      `json:"budget"`
	Tokens dryRunTokens    `json:"tokens"`
	Fast   bool            `json:"fast_path"`
	Cover  review.Coverage `json:"coverage"`
	Notes  []string        `json:"notes"`
	// RepoContext is the block's tokens and the symbols searched (RC-9);
	// absent when repository context is off.
	RepoContext *repoctx.Report `json:"repo_context,omitempty"`
	// ElapsedMS is the time to fetch and prepare.
	ElapsedMS int64 `json:"elapsed_ms"`
}

// dryRunTokens are the estimates of the request a real run would send.
type dryRunTokens struct {
	// Prompt is the scaffolding around the diff (instructions, title,
	// description).
	Prompt int `json:"prompt"`
	// Diff is the prepared diff.
	Diff int `json:"diff"`
	// Request is the whole request estimate; 0 when there is nothing to
	// review.
	Request int `json:"request"`
	// ContextWindow is llm.context_window, for comparison.
	ContextWindow int `json:"context_window"`
}

func buildDryRunReport(pl *review.Plan, elapsed time.Duration) dryRunReport {
	b, m := pl.Budget, pl.Result.Metadata
	notes := pl.Result.Notes
	if pl.Empty {
		notes = append(append([]string{}, notes...), review.NoteNoReviewableChanges)
	}
	return dryRunReport{
		DryRun: true,
		PR:     pl.Result.PR,
		Empty:  pl.Empty,
		Budget: budgetJSON{
			ContextWindow: b.ContextWindow, SoftLimit: b.SoftLimit(), HardLimit: b.HardLimit(),
			PromptTokens: b.PromptTokens, Factor: b.Factor, Limit: b.Limit(), MaxDiffTokens: b.MaxDiffTokens,
		},
		Tokens:      dryRunTokens{Prompt: m.PromptTokens, Diff: m.DiffTokens, Request: m.RequestTokens, ContextWindow: m.ContextWindow},
		Fast:        m.FastPath,
		Cover:       pl.Result.Coverage,
		Notes:       notes,
		RepoContext: pl.RepoReport,
		ElapsedMS:   elapsed.Milliseconds(),
	}
}

// writePrompts prints both prompts under their separator lines.
func writePrompts(w io.Writer, system, user string) error {
	_, err := fmt.Fprintf(w, "%s\n%s\n%s\n%s\n", systemPromptSeparator, system, userPromptSeparator, user)
	return err
}

// dryRunLLM returns the chat client a dry run needs only to resolve an unset
// llm.context_window (X-15): the probe is a GET of the model list, no
// completion is ever requested. With llm.context_window set it returns nil
// and the dry run builds no client at all. close must be called.
func dryRunLLM(cfg *config.Config, logger *slog.Logger) (c review.Completer, closeFn func(), err error) {
	if cfg.LLM.ContextWindow > 0 {
		return nil, func() {}, nil
	}
	c, err = wiring.NewLLM(cfg, logger)
	if err != nil {
		return nil, func() {}, err
	}
	return c, func() { closeIdle(c) }, nil
}

// diagReviewDryRun runs the review pipeline up to the model call and prints
// the JSON report. The model is never called; the only LLM request is the
// context-window probe, and only when llm.context_window is unset.
func diagReviewDryRun(ctx context.Context, cfg *config.Config, logger *slog.Logger, prURL string, showPrompt bool, stdout, stderr io.Writer) int {
	start := time.Now()
	client, closeLLM, err := dryRunLLM(cfg, logger)
	if err != nil {
		return reportError(stderr, err)
	}
	defer closeLLM()
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	pl, err := review.Prepare(ctx, review.Deps{
		Config: cfg, Logger: logger, Resolver: resolver, LLM: client,
	}, review.Args{PRURL: prURL})
	if err != nil {
		return reportError(stderr, err)
	}
	if err := writeJSON(stdout, buildDryRunReport(pl, time.Since(start))); err != nil {
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

// closeIdle closes the idle connections of a chat client built for one diag
// command, like the MCP tool handlers do when a call ends.
func closeIdle(c review.Completer) {
	if ic, ok := c.(provider.IdleCloser); ok {
		ic.CloseIdleConnections()
	}
}

// promptRecorder wraps the chat client to keep the first request's prompts
// for --show-prompt. It is the only place the prompts are held, and they
// reach stdout only.
type promptRecorder struct {
	review.Completer
	system, user string
	seen         bool
}

func (r *promptRecorder) Complete(ctx context.Context, system, user string) (*llm.Response, error) {
	if !r.seen {
		r.seen, r.system, r.user = true, system, user
	}
	return r.Completer.Complete(ctx, system, user)
}

// ResolveContextWindow forwards to the wrapped client, so wrapping it does
// not hide the context-window probe from the pipeline (X-15).
func (r *promptRecorder) ResolveContextWindow(ctx context.Context) (int, string, error) {
	if w, ok := r.Completer.(llmrun.WindowResolver); ok {
		return w.ResolveContextWindow(ctx)
	}
	return 0, "", llm.NoContextWindowError()
}

// diagReview runs the full review and prints the client markdown; with
// publish it also posts the provider rendering.
func diagReview(ctx context.Context, cfg *config.Config, logger *slog.Logger, prURL string, publish, showPrompt, asJSON bool, stdout, stderr io.Writer) int {
	client, err := wiring.NewLLM(cfg, logger)
	if err != nil {
		return reportError(stderr, err)
	}
	defer closeIdle(client)
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	rec := &promptRecorder{Completer: client}
	res, err := review.Run(ctx, review.Deps{
		Config: cfg, Logger: logger, Resolver: resolver, LLM: rec,
		RenderProvider: render.Provider, RenderInline: render.Inline,
	}, review.Args{PRURL: prURL, Publish: publish}.WithConfigDefaults(cfg))
	if err != nil {
		return reportError(stderr, err)
	}
	if asJSON {
		err = writeJSON(stdout, res)
	} else {
		_, err = io.WriteString(stdout, render.Client(res))
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the review to stdout")
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
			_, _ = fmt.Fprintln(stderr, "diag review: the review was not posted: "+res.Publish.Error)
			return 1
		}
		_, _ = fmt.Fprintln(stderr, "diag review: posted comment "+res.Publish.CommentID)
	}
	return 0
}
