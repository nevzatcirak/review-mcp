package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tools"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

const diagUsageText = `usage:
  review-mcp diag pr <PR_URL> [--show-patch <path>]
  review-mcp diag comment <PR_URL> --body <TEXT>
  review-mcp diag comments <PR_URL> [--include-resolved]
  review-mcp diag reply <PR_URL> --comment-id <ID> --body <TEXT>
  review-mcp diag diff <PR_URL> [--mode plain|numbered] [--prompt-tokens N]
                       [--context-window N]
  review-mcp diag review <PR_URL> [--dry-run] [--show-prompt] [--publish] [--json]
                         [--repo-context on|off]
  review-mcp diag ask <PR_URL> --question <TEXT> [--dry-run] [--show-prompt] [--publish]
  review-mcp diag cache [--prune]

diag pr      fetch a pull request and print a JSON connectivity report;
              --show-patch prints the hunk-only patch of one changed file
              (matched by its "path" in the report) after the JSON
diag comment  post one PR-level comment and print {"id": ..., "url": ...}
diag comments list the comment threads (the pr_comments structured result) as
              JSON; resolved threads are hidden unless --include-resolved
diag reply    reply to the comment --comment-id and print
              {"id": ..., "url": ..., "in_thread": ...}; in_thread is false when
              the provider posted a PR-level comment quoting the original
diag diff     run the diff pipeline (provider, file filter, token budget) and
              print a JSON header, the line "--- prepared diff ---" and the
              exact diff text a review would embed; --mode picks the render
              format (default plain); --prompt-tokens N is the estimated size
              of the prompt scaffolding in tokens (default 2205, the measured
              maximum of the review prompts); --context-window N overrides
              llm.context_window and needs no LLM access (without it and
              without llm.context_window, the window is read from the
              endpoint)
diag review   review the pull request with the configured LLM and print the
              client markdown; --publish also posts it as a PR comment.
              --dry-run runs everything up to the model call, prints a JSON
              report (prompt, diff and request tokens, budget, coverage) and
              sends no completion to the model (with llm.context_window
              unset it still lists the endpoint's models once to read the
              window); it cannot be combined with
              --publish. --show-prompt prints the rendered system and user
              prompts to stdout after the output, under separator lines
              (they are never logged). --json prints the pr_review structured
              result as JSON instead of markdown (not with --dry-run).
              --repo-context on|off overrides context.repo.enabled for this
              run; the coverage line says whether repository context was used
diag ask      answer --question (required, at most 8000 characters) about the
              pull request with the configured LLM and print the client
              markdown; --publish also posts the question and answer as a PR
              comment. --dry-run and --show-prompt behave as for diag review;
              --dry-run cannot be combined with --publish
diag cache    list the repository-context cache (context.repo.cache_dir) as
              JSON: each repository with its size, last use and idle days;
              --prune first deletes repositories idle longer than
              context.repo.idle_days, then least-recently-used ones until the
              cache fits context.repo.max_cache_mb (what every use does)
`

func diagUsage(w io.Writer) { _, _ = fmt.Fprint(w, diagUsageText) }

// runDiag implements "review-mcp diag ...". Unlike the MCP modes it writes
// its result to stdout, like "version"; logs and errors go to stderr.
func runDiag(args []string, stdout, stderr io.Writer, load configLoader) int {
	if len(args) == 0 {
		diagUsage(stderr)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "pr":
		fs := flag.NewFlagSet("diag pr", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { diagUsage(stderr) }
		showPatch := fs.String("show-patch", "", "print the hunk-only patch of this changed `path` after the JSON")
		prURL, ok := parseDiagArgs(fs, rest, stderr)
		if !ok {
			return 2
		}
		if showPatchSet(fs) && *showPatch == "" {
			_, _ = fmt.Fprintln(stderr, "diag pr: --show-patch needs a non-empty path")
			diagUsage(stderr)
			return 2
		}
		cfg, logger, code := diagSetup(load, stderr)
		if cfg == nil {
			return code
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return diagPR(ctx, cfg, logger, prURL, *showPatch, stdout, stderr)
	case "comment":
		fs := flag.NewFlagSet("diag comment", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { diagUsage(stderr) }
		body := fs.String("body", "", "comment `text` (required, posted verbatim)")
		prURL, ok := parseDiagArgs(fs, rest, stderr)
		if !ok {
			return 2
		}
		if *body == "" {
			_, _ = fmt.Fprintln(stderr, "diag comment: --body is required and must not be empty")
			diagUsage(stderr)
			return 2
		}
		cfg, logger, code := diagSetup(load, stderr)
		if cfg == nil {
			return code
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return diagComment(ctx, cfg, logger, prURL, *body, stdout, stderr)
	case "comments":
		fs := flag.NewFlagSet("diag comments", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { diagUsage(stderr) }
		includeResolved := fs.Bool("include-resolved", false, "also list resolved threads")
		prURL, ok := parseDiagArgs(fs, rest, stderr)
		if !ok {
			return 2
		}
		cfg, logger, code := diagSetup(load, stderr)
		if cfg == nil {
			return code
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return diagComments(ctx, cfg, logger, prURL, *includeResolved, stdout, stderr)
	case "reply":
		fs := flag.NewFlagSet("diag reply", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { diagUsage(stderr) }
		commentID := fs.String("comment-id", "", "`id` of the comment to reply to (a positive integer)")
		body := fs.String("body", "", "reply `text` (required, posted verbatim)")
		prURL, ok := parseDiagArgs(fs, rest, stderr)
		if !ok {
			return 2
		}
		if !provider.IsPositiveInt(*commentID) {
			_, _ = fmt.Fprintln(stderr, "diag reply: --comment-id is required and must be a positive integer")
			diagUsage(stderr)
			return 2
		}
		if strings.TrimSpace(*body) == "" {
			_, _ = fmt.Fprintln(stderr, "diag reply: --body is required and must not be empty")
			diagUsage(stderr)
			return 2
		}
		cfg, logger, code := diagSetup(load, stderr)
		if cfg == nil {
			return code
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return diagReply(ctx, cfg, logger, prURL, *commentID, *body, stdout, stderr)
	case "diff":
		return runDiagDiff(rest, stdout, stderr, load)
	case "review":
		return runDiagReview(rest, stdout, stderr, load)
	case "ask":
		return runDiagAsk(rest, stdout, stderr, load)
	case "cache":
		return runDiagCache(rest, stdout, stderr, load)
	default:
		diagUsage(stderr)
		return 2
	}
}

// parseDiagArgs parses flags that may appear before or after the single
// positional PR URL (the stdlib flag package stops at the first non-flag).
func parseDiagArgs(fs *flag.FlagSet, args []string, stderr io.Writer) (prURL string, ok bool) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", false
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	if len(pos) != 1 || pos[0] == "" {
		_, _ = fmt.Fprintf(stderr, "%s: exactly one PR URL is required\n", fs.Name())
		diagUsage(stderr)
		return "", false
	}
	return pos[0], true
}

func showPatchSet(fs *flag.FlagSet) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "show-patch" {
			set = true
		}
	})
	return set
}

// diagSetup loads the configuration and builds the stderr logger. When the
// configuration is invalid it prints every problem (one per line) and returns
// a nil config with exit code 1; nothing touches the network before this
// point.
func diagSetup(load configLoader, stderr io.Writer) (*config.Config, *slog.Logger, int) {
	cfg, rep, err := load()
	if err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			for _, p := range ve.Problems {
				_, _ = fmt.Fprintln(stderr, logging.RedactText(p))
			}
		} else {
			_, _ = fmt.Fprintln(stderr, logging.RedactText(err.Error()))
		}
		return nil, nil, 1
	}
	logger := logging.New(stderr, logLevel(cfg, nil))
	if rep != nil {
		for _, w := range rep.Warnings {
			logger.Warn(logging.RedactText(w))
		}
	}
	return cfg, logger, 0
}

// reportError prints a failure to stderr and returns exit code 1. The text
// comes from tools.UserMessage: a fixed X-6 sentence for a *provider.Error,
// otherwise a generic sentence that never echoes the error.
func reportError(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintln(stderr, tools.UserMessage(err))
	return 1
}

// ---- diag pr ----

type refJSON struct {
	Namespace string `json:"namespace"`
	Repo      string `json:"repo"`
	Number    int64  `json:"number"`
	URL       string `json:"url"`
}

type fileJSON struct {
	Path       string `json:"path"`
	OldPath    string `json:"old_path"`
	Type       string `json:"type"`
	Additions  int    `json:"additions"`
	Deletions  int    `json:"deletions"`
	PatchBytes int    `json:"patch_bytes"`
	BaseStatus string `json:"base_status"`
	HeadStatus string `json:"head_status"`
	Binary     bool   `json:"binary"`
}

type skippedJSON struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type totalsJSON struct {
	Files      int `json:"files"`
	Skipped    int `json:"skipped"`
	Additions  int `json:"additions"`
	Deletions  int `json:"deletions"`
	PatchBytes int `json:"patch_bytes"`
}

// prReport is the diag pr JSON document. It deliberately has no field for the
// PR description or commit bodies.
type prReport struct {
	Kind         string        `json:"kind"`
	Ref          refJSON       `json:"ref"`
	Title        string        `json:"title"`
	SourceBranch string        `json:"source_branch"`
	TargetBranch string        `json:"target_branch"`
	HeadSHA      string        `json:"head_sha"`
	BaseSHA      string        `json:"base_sha"`
	BaseStrategy string        `json:"base_strategy"`
	CommitCount  int           `json:"commit_count"`
	Commits      []string      `json:"commits"`
	Files        []fileJSON    `json:"files"`
	Skipped      []skippedJSON `json:"skipped"`
	Totals       totalsJSON    `json:"totals"`
	ElapsedMS    int64         `json:"elapsed_ms"`
}

func buildPRReport(ref provider.PRRef, pr *provider.PullRequest, commits []string, d *provider.Diff, elapsed time.Duration) prReport {
	r := prReport{
		Kind: string(ref.Kind),
		Ref: refJSON{
			Namespace: ref.Namespace, Repo: ref.Repo, Number: ref.Number,
			URL: logging.RedactURL(ref.URL),
		},
		Title:        pr.Title,
		SourceBranch: pr.SourceBranch,
		TargetBranch: pr.TargetBranch,
		HeadSHA:      pr.HeadSHA,
		BaseSHA:      pr.BaseSHA,
		BaseStrategy: d.BaseStrategy,
		CommitCount:  len(commits),
		Commits:      []string{},
		Files:        []fileJSON{},
		Skipped:      []skippedJSON{},
		ElapsedMS:    elapsed.Milliseconds(),
	}
	for _, c := range commits {
		first, _, _ := strings.Cut(c, "\n")
		r.Commits = append(r.Commits, strings.TrimRight(first, "\r"))
	}
	for _, f := range d.Files {
		r.Files = append(r.Files, fileJSON{
			Path: f.Path, OldPath: f.OldPath, Type: string(f.Type),
			Additions: f.Additions, Deletions: f.Deletions, PatchBytes: len(f.Patch),
			BaseStatus: string(f.BaseStatus), HeadStatus: string(f.HeadStatus), Binary: f.Binary,
		})
		r.Totals.Additions += f.Additions
		r.Totals.Deletions += f.Deletions
		r.Totals.PatchBytes += len(f.Patch)
	}
	for _, s := range d.Skipped {
		r.Skipped = append(r.Skipped, skippedJSON{Path: s.Path, Reason: s.Reason})
	}
	r.Totals.Files = len(r.Files)
	r.Totals.Skipped = len(r.Skipped)
	return r
}

// writeJSON pretty-prints v with a 2-space indent and one trailing newline.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func diagPR(ctx context.Context, cfg *config.Config, logger *slog.Logger, prURL, showPatch string, stdout, stderr io.Writer) int {
	start := time.Now()
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	ref, p, err := resolver.Resolve(prURL)
	if err != nil {
		return reportError(stderr, err)
	}
	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return reportError(stderr, err)
	}
	commits, err := p.GetCommitMessages(ctx, ref)
	if err != nil {
		return reportError(stderr, err)
	}
	d, err := p.GetDiff(ctx, ref, pr, provider.DiffOptions{})
	if err != nil {
		return reportError(stderr, err)
	}
	if err := writeJSON(stdout, buildPRReport(ref, pr, commits, d, time.Since(start))); err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the report to stdout")
		return 1
	}
	if showPatch == "" {
		return 0
	}
	for _, f := range d.Files {
		if f.Path == showPatch {
			_, _ = fmt.Fprintf(stdout, "--- patch: %s ---\n%s", f.Path, f.Patch)
			return 0
		}
	}
	_, _ = fmt.Fprintln(stderr, "--show-patch: no changed file has that path (see \"files\" in the report)")
	return 1
}

// ---- diag comment ----

func diagComment(ctx context.Context, cfg *config.Config, logger *slog.Logger, prURL, body string, stdout, stderr io.Writer) int {
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	ref, p, err := resolver.Resolve(prURL)
	if err != nil {
		return reportError(stderr, err)
	}
	c, err := p.PostComment(ctx, ref, body)
	if err != nil {
		return reportError(stderr, err)
	}
	// Decision: the comment URL is printed as the provider built it (the
	// configured base URL plus the comment id, so it carries no credentials;
	// config validation rejects userinfo), keeping deep links such as
	// #issuecomment-55. Logs stay redacted.
	out := struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}{c.ID, c.URL}
	if err := writeJSON(stdout, out); err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the result to stdout")
		return 1
	}
	return 0
}

// ---- diag comments / diag reply ----

// diagComments prints the structured result of the pr_comments tool.
func diagComments(ctx context.Context, cfg *config.Config, logger *slog.Logger, prURL string, includeResolved bool, stdout, stderr io.Writer) int {
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	res, err := tools.PRComments(ctx, resolver, prURL, includeResolved)
	if err != nil {
		return reportError(stderr, err)
	}
	if err := writeJSON(stdout, res); err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the result to stdout")
		return 1
	}
	return 0
}

// diagReply prints the structured result of the pr_comment_reply tool.
func diagReply(ctx context.Context, cfg *config.Config, logger *slog.Logger, prURL, commentID, body string, stdout, stderr io.Writer) int {
	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
	res, err := tools.PRCommentReply(ctx, resolver, prURL, commentID, body)
	if err != nil {
		return reportError(stderr, err)
	}
	if err := writeJSON(stdout, res); err != nil {
		_, _ = fmt.Fprintln(stderr, "could not write the result to stdout")
		return 1
	}
	return 0
}
