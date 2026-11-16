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
	"path/filepath"
	"syscall"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/issuectx"
	"github.com/nevzatcirak/review-mcp/internal/provider/local"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// Local review from the command line (X-52, v2.10 spec V-20 §3.1).
// "review-mcp local review" runs local_review once, for a pre-push hook or
// by hand. Its flags, exit codes and output are a stable interface, like
// CI mode's: they change only with a CHANGELOG entry.

const localUsageText = `usage:
  review-mcp local review [--path DIR] [--base REF] [--uncommitted=true|false] [--untracked]
                          [--focus NAME] [--max-findings N] [--output-language L]
                          [--issue-context-file PATH] [--json] [--timeout D]
                          [--fail-on security|any|issue-not-compliant]
                          [--impact on|off] [--impact-paths P1,P2]

local review reviews the work of a git repository on this machine that is
not pushed yet: the commits after the merge base with the base, plus the
uncommitted changes to tracked files. It prints the review as markdown (or,
with --json, the local_review structured result) on stdout. Nothing is
published, the repository is only read, and the code is never built or run.
The command never asks a question on the terminal.

--path DIR                the work tree or a directory in it (default: the
                          current directory)
--base REF                the base: a ref name or a commit; default, in
                          order: the current branch's upstream, origin/HEAD,
                          origin/main, origin/master, main, master
--uncommitted=BOOL        review the uncommitted changes to tracked files,
                          staged and unstaged (default true)
--untracked               also review untracked files that are not ignored;
                          files whose names suggest secrets are left out
--focus NAME              the focus argument
--max-findings N          the max_findings argument, 1 to 20
--output-language L       the output_language argument, for example en-US
--issue-context-file PATH a JSON file holding the issue_context argument
                          (1 to 3 issues)
--impact on|off           override context.impact.enabled for this run (the
                          impact trace; it needs context.repo.enabled)
--impact-paths P1,P2      the impact_paths argument: directory prefixes
                          that limit the impact trace for this run
--json                    print the structured result instead of markdown
--timeout D               bound the whole run (a Go duration, default 30m,
                          at least 1m)
--fail-on LEVEL           exit 3 when the review meets LEVEL: security (the
                          security concerns are set, or a finding of a
                          --focus security run), any (at least one finding)
                          or issue-not-compliant (the issue verdict is
                          not_compliant)

exit codes:
  0  done: the review was printed (also with findings, unless --fail-on
     applies)
  1  the run failed: configuration, model, git or timeout; the reason is on
     stderr
  2  usage error, or no repository or base; nothing is sent
  3  a --fail-on condition is met; the review was printed first
`

func localUsage(w io.Writer) { _, _ = fmt.Fprint(w, localUsageText) }

// The prefix of the command's stderr lines, its final line on success, and
// the line when local.enabled is false.
const (
	localPrefix      = "local review: "
	localDoneLine    = "local review: printed; nothing is published"
	localOffLine     = "local review: local reviews are turned off (local.enabled is false)"
	localNoCwdLine   = "local review: the current directory cannot be read; pass --path"
	localTimeoutLine = "local review: the run did not finish within --timeout"
)

// localOptions is a parsed and checked "local review" command line.
type localOptions struct {
	failOn  string
	asJSON  bool
	timeout time.Duration
	args    tools.LocalReviewArgs
	// impact is --impact: on, off or "" (the configuration decides).
	impact string
}

// runLocal implements "review-mcp local ...". A usage error exits 2 before
// the configuration is loaded; a missing repository or base exits 2 too,
// after it (local.allowed_roots is configuration). The command reads no
// standard input. getwd is os.Getwd outside tests.
func runLocal(args []string, stdout, stderr io.Writer, load configLoader, getwd func() (string, error)) int {
	opts, err := parseLocal(args, stderr, getwd)
	if err != nil {
		var ue *usageError
		if errors.As(err, &ue) {
			_, _ = fmt.Fprintln(stderr, ue.msg)
		}
		return 2
	}
	cfg, logger, code := diagSetup(load, stderr)
	if cfg == nil {
		return code
	}
	if !cfg.Local.Enabled {
		_, _ = fmt.Fprintln(stderr, localOffLine)
		return 1
	}
	applyImpactFlag(cfg, opts.impact)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return localExecute(ctx, opts, cfg, logger, stdout, stderr)
}

// parseLocal parses and checks the command line without configuration,
// git or network: the flags, the tool arguments (the tool's own checks,
// with the same sentences) and the issue context file. --path is made
// absolute against the current directory. flag.ErrHelp and the flag
// package's own errors have already been printed with the usage; the other
// errors are *usageError.
func parseLocal(args []string, stderr io.Writer, getwd func() (string, error)) (*localOptions, error) {
	if len(args) == 0 || args[0] != "review" {
		localUsage(stderr)
		return nil, errors.New("unknown command")
	}
	const name = "local review"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { localUsage(stderr) }
	o := &localOptions{}
	path := fs.String("path", "", "the work tree or a directory in it")
	base := fs.String("base", "", "the base ref or commit")
	uncommitted := fs.Bool("uncommitted", true, "review the uncommitted changes to tracked files")
	untracked := fs.Bool("untracked", false, "also review untracked files that are not ignored")
	focus := fs.String("focus", "", "the focus argument")
	maxFindings := fs.Int("max-findings", 0, "the max_findings argument, 1 to 20")
	outputLanguage := fs.String("output-language", "", "the output_language argument")
	issueFile := fs.String("issue-context-file", "", "a JSON file holding the issue_context argument")
	fs.StringVar(&o.impact, "impact", "", "`on` or `off`: override context.impact.enabled for this run (the impact trace; needs repository context)")
	impactPaths := fs.String("impact-paths", "", "comma-separated `prefixes`: the impact_paths argument")
	fs.BoolVar(&o.asJSON, "json", false, "print the structured result instead of markdown")
	fs.DurationVar(&o.timeout, "timeout", ciDefaultTimeout, "bound the whole run")
	fs.StringVar(&o.failOn, "fail-on", "", "exit 3 when the review meets this `level`: security, any or issue-not-compliant")
	if err := fs.Parse(args[1:]); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, usageErrorf("%s: unexpected argument; the repository is given with --path", name)
	}
	if o.timeout < ciMinTimeout {
		return nil, usageErrorf("%s: --timeout must be a Go duration of at least 1m", name)
	}
	switch o.failOn {
	case "", ciFailOnSecurity, ciFailOnAny, ciFailOnNotCompliant:
	default:
		return nil, usageErrorf("%s: --fail-on must be security, any or issue-not-compliant", name)
	}
	if o.failOn == "" && flagSet(fs, "fail-on") {
		return nil, usageErrorf("%s: --fail-on must be security, any or issue-not-compliant", name)
	}
	if flagSet(fs, "path") && *path == "" {
		return nil, usageErrorf("%s: --path is empty", name)
	}
	if o.impact != "" && o.impact != "on" && o.impact != "off" {
		return nil, usageErrorf("%s: --impact must be on or off", name)
	}
	paths, err := impactPathsFlag(*impactPaths, flagSet(fs, "impact-paths"))
	if err != nil {
		return nil, usageErrorf("%s: --impact-paths: %s", name, tools.UserMessage(err))
	}
	var issues []issuectx.Input
	if flagSet(fs, "issue-context-file") {
		var err error
		if issues, err = readIssueContextFile(*issueFile); err != nil {
			return nil, usageErrorf("%s: --issue-context-file: %s", name, err.Error())
		}
	}
	a := tools.LocalReviewArgs{Base: *base, Focus: *focus, OutputLanguage: *outputLanguage, IssueContext: issues,
		IncludeUncommitted: uncommitted, IncludeUntracked: untracked, ImpactPaths: paths}
	if flagSet(fs, "max-findings") {
		a.MaxFindings = maxFindings
	}
	// The checks local_review makes before it opens the repository.
	if err := (tools.PRReviewArgs{OutputLanguage: a.OutputLanguage, MaxFindings: a.MaxFindings, IssueContext: a.IssueContext}).Validate(); err != nil {
		return nil, usageErrorf("%s: %s", name, tools.UserMessage(err))
	}
	if _, err := (review.Args{Focus: a.Focus, Local: true}).Validated(nil); err != nil {
		return nil, usageErrorf("%s: %s", name, tools.UserMessage(err))
	}
	dir := *path
	if dir == "" || !filepath.IsAbs(dir) {
		wd, err := getwd()
		if err != nil {
			return nil, &usageError{localNoCwdLine}
		}
		dir = filepath.Join(wd, dir)
	}
	a.RepoPath = filepath.Clean(dir)
	o.args = a
	return o, nil
}

// localExecute runs the review with the loaded configuration and returns
// the exit code. stdout gets the result; stderr the logs and one final
// line, followed on exit 3 by the line that says which --fail-on condition
// was met.
func localExecute(ctx context.Context, o *localOptions, cfg *config.Config, logger *slog.Logger, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	var clients []review.Completer
	newLLM := func(cfg *config.Config, log *slog.Logger) (review.Completer, error) {
		c, err := wiring.NewLLM(cfg, log)
		if err == nil {
			clients = append(clients, c)
		}
		return c, err
	}
	defer func() {
		for _, c := range clients {
			closeIdle(c)
		}
	}()
	// No provider is resolved: the local source is the only one (E4).
	deps := tools.ReviewDeps{Config: cfg, NewLLM: newLLM, Logger: logger}
	call, err := tools.PrepareLocalReview(ctx, deps, o.args)
	if err != nil {
		return localFail(ctx, stderr, err)
	}
	logger.Debug(localPrefix+"repository", tools.LocalLogFields(call.Info())...)
	progress := func(stage string) { logger.Info(localPrefix + stage) }
	res, text, err := call.Run(ctx, progress)
	if err != nil {
		return localFail(ctx, stderr, err)
	}
	if o.asJSON {
		err = writeJSON(stdout, res)
	} else {
		_, err = io.WriteString(stdout, text)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, localPrefix+"could not write the result to stdout")
		return 1
	}
	// The --fail-on rules are CI mode's, on the same result.
	reason := ciFailReason(&ciOptions{failOn: o.failOn}, &ciOutcome{review: res}, logger, localPrefix)
	_, _ = fmt.Fprintln(stderr, localDoneLine)
	if reason != "" {
		_, _ = fmt.Fprintln(stderr, localPrefix+"exit 3: "+reason)
		return 3
	}
	return 0
}

// localFail prints the final error line and returns the exit code: 2 when
// the repository or the base is missing or refused (localUsageReason), 1
// for any other failure. When the --timeout deadline passed, the line is
// the timeout line whatever error the cut-off command reported.
func localFail(ctx context.Context, stderr io.Writer, err error) int {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		_, _ = fmt.Fprintln(stderr, localTimeoutLine)
		return 1
	}
	msg := tools.UserMessage(err)
	code := 1
	if localUsageReason(err) {
		code = 2
	}
	// The local sentences already start with "local review: ".
	if len(msg) < len(localPrefix) || msg[:len(localPrefix)] != localPrefix {
		msg = localPrefix + msg
	}
	_, _ = fmt.Fprintln(stderr, msg)
	return code
}

// localUsageReason reports whether err means "no repository or base"
// (exit 2): the path is refused or holds no usable work tree, or no base
// or merge base is found. git missing or failing, a timeout, a diff over
// the size limit and the temporary directory are failed runs (exit 1).
func localUsageReason(err error) bool {
	var be *local.Error
	if errors.As(err, &be) {
		return true
	}
	var le *gitctx.LocalError
	if errors.As(err, &le) {
		switch le.Reason {
		case gitctx.LocalNotAbsolute, gitctx.LocalNotFound, gitctx.LocalOutsideRoots, gitctx.LocalNotRepository,
			gitctx.LocalBare, gitctx.LocalDubiousOwnership, gitctx.LocalUnbornHead:
			return true
		}
	}
	return false
}
