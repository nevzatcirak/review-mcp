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
	"strconv"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// defaultPromptTokens is the default of --prompt-tokens: the measured maximum
// of the review prompt scaffolding (system and user prompt around an empty
// diff, with the framing allowance). It was measured in WP-PR-4c with every
// field on, tr-TR and extra instructions, and re-measured in WP-PR-7d after
// the performance field (X-12) was added (2056 before); reproduce it with
//
//	go test ./internal/review -run TestScaffoldingTokens -v
//
// (the last row of the table: request tokens 2205). A review of a real PR
// adds its title, branch, description and existing-discussion block (X-13,
// at most 1500 tokens by default) on top; diag review --dry-run reports the
// exact figure for one PR.
const defaultPromptTokens = 2205

// diffSeparator is the line between the JSON header and the prepared diff.
const diffSeparator = "--- prepared diff ---"

// doesNotFitMessage is the fixed sentence printed for tokens.ErrDoesNotFit.
const doesNotFitMessage = "the pull request diff does not fit the configured context window; " +
	"raise llm.context_window (REVIEW_MCP_LLM_CONTEXT_WINDOW) or narrow the pull request"

// parseDiffMode maps the --mode value to a diffpipe.Mode.
func parseDiffMode(s string) (diffpipe.Mode, bool) {
	switch s {
	case "plain":
		return diffpipe.ModePlain, true
	case "numbered":
		return diffpipe.ModeNumbered, true
	}
	return diffpipe.ModePlain, false
}

// runDiagDiff parses "diag diff" arguments and runs the command.
func runDiagDiff(rest []string, stdout, stderr io.Writer, load configLoader) int {
	fs := flag.NewFlagSet("diag diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { diagUsage(stderr) }
	modeStr := fs.String("mode", "plain", "render `mode`: plain or numbered")
	promptStr := fs.String("prompt-tokens", strconv.Itoa(defaultPromptTokens), "estimated prompt scaffolding `N` in tokens (default: the measured maximum of the review prompts)")
	windowStr := fs.String("context-window", "", "context window `N` in tokens; overrides llm.context_window and needs no LLM access (default: llm.context_window, else the endpoint's)")
	prURL, ok := parseDiagArgs(fs, rest, stderr)
	if !ok {
		return 2
	}
	mode, ok := parseDiffMode(*modeStr)
	if !ok {
		_, _ = fmt.Fprintln(stderr, "diag diff: --mode must be plain or numbered")
		diagUsage(stderr)
		return 2
	}
	promptTokens, err := strconv.Atoi(*promptStr)
	if err != nil || promptTokens < 0 {
		_, _ = fmt.Fprintln(stderr, "diag diff: --prompt-tokens must be a non-negative integer")
		diagUsage(stderr)
		return 2
	}
	window := 0
	if *windowStr != "" {
		n, err := strconv.Atoi(*windowStr)
		if err != nil || n < llm.MinContextWindow {
			_, _ = fmt.Fprintf(stderr, "diag diff: --context-window must be an integer >= %d\n", llm.MinContextWindow)
			diagUsage(stderr)
			return 2
		}
		window = n
	}
	cfg, logger, code := diagSetup(load, stderr)
	if cfg == nil {
		return code
	}
	if window > 0 {
		// The flag overrides llm.context_window; with it no LLM request is
		// made.
		cfg.LLM.ContextWindow = window
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return diagDiff(ctx, cfg, logger, prURL, mode, promptTokens, stdout, stderr)
}

type budgetJSON struct {
	ContextWindow int     `json:"context_window"`
	SoftLimit     int     `json:"soft_limit"`
	HardLimit     int     `json:"hard_limit"`
	PromptTokens  int     `json:"prompt_tokens"`
	Factor        float64 `json:"factor"`
}

type omittedJSON struct {
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Deleted  []string `json:"deleted"`
}

// diffReport is the JSON header of diag diff. It has no field for diff
// content: the prepared text is printed after the header, on stdout only.
//
// Decision: Skipped and Filtered are disjoint. Filtered holds the files the
// provider skipped with reason "filtered", each with the reason filter.Explain
// gives for its path; Skipped holds every other skip (provider reasons and the
// diffpipe reasons empty_diff and unparseable_patch). Each file therefore
// appears in exactly one list of the report.
type diffReport struct {
	Budget    budgetJSON    `json:"budget"`
	FastPath  bool          `json:"fast_path"`
	Tokens    int           `json:"tokens"`
	Included  []string      `json:"included"`
	Omitted   omittedJSON   `json:"omitted"`
	Clipped   []string      `json:"clipped"`
	Skipped   []skippedJSON `json:"skipped"`
	Filtered  []skippedJSON `json:"filtered"`
	ElapsedMS int64         `json:"elapsed_ms"`
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func buildDiffReport(b tokens.Budget, f *filter.Filter, p *diffpipe.Prepared, elapsed time.Duration) diffReport {
	r := diffReport{
		Budget: budgetJSON{
			ContextWindow: b.ContextWindow, SoftLimit: b.SoftLimit(), HardLimit: b.HardLimit(),
			PromptTokens: b.PromptTokens, Factor: b.Factor,
		},
		FastPath: p.FastPath,
		Tokens:   p.Tokens,
		Included: nonNil(p.Included),
		Omitted: omittedJSON{
			Added: nonNil(p.Omitted.Added), Modified: nonNil(p.Omitted.Modified), Deleted: nonNil(p.Omitted.Deleted),
		},
		Clipped:   nonNil(p.Clipped),
		Skipped:   []skippedJSON{},
		Filtered:  []skippedJSON{},
		ElapsedMS: elapsed.Milliseconds(),
	}
	for _, s := range p.Skipped {
		if s.Reason != provider.SkipFiltered {
			r.Skipped = append(r.Skipped, skippedJSON{Path: s.Path, Reason: s.Reason})
			continue
		}
		reason := provider.SkipFiltered
		if included, why := f.Explain(s.Path); !included && why != "" {
			reason = why
		}
		r.Filtered = append(r.Filtered, skippedJSON{Path: s.Path, Reason: reason})
	}
	return r
}

// diagDiff runs provider -> filter -> diffpipe.Prepare and prints the JSON
// header, the separator line and the prepared text, all on stdout. The diff
// text never reaches the logger; debug logs carry counts and the redacted URL.
func diagDiff(ctx context.Context, cfg *config.Config, logger *slog.Logger, prURL string, mode diffpipe.Mode, promptTokens int, stdout, stderr io.Writer) int {
	start := time.Now()
	flt, err := filter.New(cfg)
	if err != nil {
		return reportError(stderr, err)
	}
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	ref, p, err := resolver.Resolve(prURL)
	if err != nil {
		return reportError(stderr, err)
	}
	// An unset llm.context_window is resolved from the endpoint before any
	// provider request (X-15); a set one, or --context-window, needs no
	// LLM access.
	var client review.Completer
	if cfg.LLM.ContextWindow == 0 {
		if client, err = wiring.NewLLM(cfg, logger); err != nil {
			return reportError(stderr, err)
		}
		defer closeIdle(client)
	}
	window, _, err := llmrun.ContextWindow(ctx, cfg, client)
	if err != nil {
		return reportError(stderr, err)
	}
	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return reportError(stderr, err)
	}
	d, err := p.GetDiff(ctx, ref, pr, provider.DiffOptions{Include: flt.Include})
	if err != nil {
		return reportError(stderr, err)
	}
	budget := tokens.Budget{
		ContextWindow:   window,
		MaxOutputTokens: cfg.LLM.MaxOutputTokens,
		PromptTokens:    promptTokens,
		Factor:          cfg.LLM.TokenEstimateFactor,
	}
	prep, err := diffpipe.Prepare(diffpipe.Input{
		Files: d.Files, Skipped: d.Skipped, Mode: mode, Budget: budget, Diff: cfg.Diff,
	})
	if err != nil {
		if errors.Is(err, tokens.ErrDoesNotFit) {
			_, _ = fmt.Fprintln(stderr, doesNotFitMessage)
			return 1
		}
		return reportError(stderr, err)
	}
	logger.Debug("diff prepared", "url", logging.RedactURL(ref.URL), "files", len(d.Files),
		"provider_skipped", len(d.Skipped), "included", len(prep.Included), "fast_path", prep.FastPath, "tokens", prep.Tokens)
	if err := writeJSON(stdout, buildDiffReport(budget, flt, prep, time.Since(start))); err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the report to stdout")
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "%s\n%s", diffSeparator, prep.Text); err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the diff to stdout")
		return 1
	}
	return 0
}
