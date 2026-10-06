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
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// defaultPromptTokens is the default of --prompt-tokens: an approximation of
// the prompt scaffolding (system prompt, instructions, empty diff) until the
// prompt layer (P4) measures the real prompts.
const defaultPromptTokens = 1500

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
	promptStr := fs.String("prompt-tokens", strconv.Itoa(defaultPromptTokens), "estimated prompt scaffolding `N` in tokens (an approximation until P4)")
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
	cfg, logger, code := diagSetup(load, stderr)
	if cfg == nil {
		return code
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
	ref, p, err := wiring.NewResolver(cfg, logger).Resolve(prURL)
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
		ContextWindow:   cfg.LLM.ContextWindow,
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
