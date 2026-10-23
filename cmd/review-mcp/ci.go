package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/issuectx"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/review"
	"github.com/nevzatcirak/review-mcp/internal/tools"
	"github.com/nevzatcirak/review-mcp/internal/wiring"
)

// CI mode (X-46, v2.6 spec V-4 §1). "review-mcp ci" runs pr_review,
// pr_describe or pr_improve once for the pull request of a pipeline. Unlike
// diag, its flags, exit codes and output are a stable interface: they change
// only with a CHANGELOG entry.

const ciUsageText = `usage:
  review-mcp ci review   [--pr-url URL] [--publish] [--related URL]... [--focus NAME]
                         [--incremental] [--max-findings N] [--output-language L]
                         [--issue-context-file PATH] [--json] [--timeout D]
                         [--fail-on security|any|issue-not-compliant] [--fail-on-partial]
  review-mcp ci describe [--pr-url URL] [--publish] [--output-language L]
                         [--issue-context-file PATH] [--json] [--timeout D]
                         [--fail-on-partial]
  review-mcp ci improve  [--pr-url URL] [--publish] [--output-language L]
                         [--json] [--timeout D] [--fail-on-partial]

ci runs one review, description or set of suggestions for a pull request and
prints the result as markdown (or, with --json, the tool's structured result)
on stdout. Nothing is posted without --publish. The code under review is
never built or run.

--pr-url URL              the pull request; without it, the pull request of
                          the pipeline (GitHub Actions or Gitea Actions on a
                          pull_request or pull_request_target event, a
                          GitLab CI merge request pipeline)
--publish                 post the result as the tool does (review: the
                          overview and inline comments; describe: the
                          description region; improve: the suggestions)
--related URL             review: a related pull request or repository URL,
                          repeatable, at most 4 (the related argument)
--focus NAME              review: the focus argument; not with --publish
--incremental             review: the incremental argument
--max-findings N          review: the max_findings argument, 1 to 20
--output-language L       the output_language argument, for example en-US
--issue-context-file PATH review, describe: a JSON file holding the
                          issue_context argument (1 to 3 issues)
--json                    print the structured result instead of markdown
--timeout D               bound the whole run (a Go duration, default 30m,
                          at least 1m)
--fail-on LEVEL           review: exit 3 when the review meets LEVEL:
                          security (the security concerns are set, or a
                          finding of a --focus security run), any (at least
                          one finding, carried findings included) or
                          issue-not-compliant (the issue verdict is
                          not_compliant)
--fail-on-partial         exit 3 when the coverage is partial

With GITHUB_STEP_SUMMARY set (GitHub and Gitea Actions), the markdown is also
appended to that file.

exit codes:
  0  done: printed, and posted with --publish (also with findings or partial
     coverage, unless a --fail-on flag applies)
  1  the run failed: configuration, provider, model, publish or timeout;
     the error class is on stderr
  2  usage error, or no pull request found; nothing is loaded or sent
  3  a --fail-on condition is met; the result was printed and posted first
`

func ciUsage(w io.Writer) { _, _ = fmt.Fprint(w, ciUsageText) }

// The commands of "review-mcp ci".
const (
	ciReview   = "review"
	ciDescribe = "describe"
	ciImprove  = "improve"
)

// Values of --fail-on.
const (
	ciFailOnSecurity     = "security"
	ciFailOnAny          = "any"
	ciFailOnNotCompliant = "issue-not-compliant"
)

// The environment variable of the GitHub and Gitea Actions step summary.
const ciEnvStepSummary = "GITHUB_STEP_SUMMARY"

// Bounds of --timeout and the size limit of --issue-context-file.
const (
	ciDefaultTimeout    = 30 * time.Minute
	ciMinTimeout        = time.Minute
	ciMaxIssueFileBytes = 1 << 20
)

// The repository cache under the runner's temporary directory (V-4 §1.5).
const (
	ciEnvRunnerTemp   = "RUNNER_TEMP"
	ciRepoCacheSubdir = "review-mcp-repos"
)

// Fixed stderr lines: the --timeout deadline, and no pull request found.
const (
	ciTimeoutLine        = "the run did not finish within --timeout (class: timeout)"
	ciNoPullRequestShape = "no pull request: pass --pr-url (detected: %s)"
)

// ciEnv reads one environment variable. CI mode reads the environment only
// through it, and only the variables the spec names (V-4 §1.2, §1.4, §1.5);
// the configuration comes from the config loader (REVIEW_MCP_* only). It is
// injectable for tests.
type ciEnv func(key string) string

// ciOptions is a parsed and checked "ci" command line.
type ciOptions struct {
	cmd   string
	prURL string
	// detected is the CI the pull request was detected from; "" when
	// --pr-url gave it.
	detected string
	publish  bool
	// failOn is the --fail-on level ("" when not given; review only).
	failOn        string
	failOnPartial bool
	asJSON        bool
	timeout       time.Duration

	// The tool arguments of the command; only the fields its tool has are
	// set.
	reviewArgs   tools.PRReviewArgs
	describeArgs tools.PRDescribeArgs
	improveArgs  tools.PRImproveArgs
}

// usageError is a refused command line. Its text is printed on stderr as is
// and names the problem, never a value from a file.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErrorf(format string, a ...any) *usageError {
	return &usageError{fmt.Sprintf(format, a...)}
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// runCI implements "review-mcp ci ...". Every usage error exits 2 before the
// configuration is loaded, so nothing is loaded or sent; then the command
// runs and exits 0, 1 or 3 (V-4 §1.3).
func runCI(args []string, stdout, stderr io.Writer, load configLoader, env ciEnv) int {
	opts, err := parseCI(args, stderr, env)
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
	ciRepoCacheDefault(cfg, env)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return ciExecute(ctx, opts, cfg, logger, env, stdout, stderr)
}

// parseCI parses and checks the command line. Every check needs no
// configuration and no network: the flags, the tool arguments (the tool's
// own Validate, so the CLI refuses what the tool refuses with the same
// sentence), the issue context file and the pull request (--pr-url, or
// detected from the pipeline). flag.ErrHelp and the flag
// package's own errors have already been printed with the usage; the other
// errors are *usageError.
func parseCI(args []string, stderr io.Writer, env ciEnv) (*ciOptions, error) {
	if len(args) == 0 {
		ciUsage(stderr)
		return nil, errors.New("no command")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case ciReview, ciDescribe, ciImprove:
	default:
		ciUsage(stderr)
		return nil, errors.New("unknown command")
	}
	name := "ci " + cmd
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { ciUsage(stderr) }
	o := &ciOptions{cmd: cmd}
	fs.StringVar(&o.prURL, "pr-url", "", "the pull request `URL`")
	fs.BoolVar(&o.publish, "publish", false, "post the result")
	fs.BoolVar(&o.asJSON, "json", false, "print the structured result instead of markdown")
	fs.DurationVar(&o.timeout, "timeout", ciDefaultTimeout, "bound the whole run")
	fs.BoolVar(&o.failOnPartial, "fail-on-partial", false, "exit 3 when the coverage is partial")
	outputLanguage := fs.String("output-language", "", "the output_language argument")
	var (
		related     stringList
		focus       *string
		incremental *bool
		maxFindings *int
		issueFile   *string
	)
	if cmd == ciReview {
		fs.Var(&related, "related", "a related pull request or repository `URL` (repeatable, at most 4)")
		focus = fs.String("focus", "", "the focus argument")
		incremental = fs.Bool("incremental", false, "the incremental argument")
		maxFindings = fs.Int("max-findings", 0, "the max_findings argument, 1 to 20")
		fs.StringVar(&o.failOn, "fail-on", "", "exit 3 when the review meets this `level`: security, any or issue-not-compliant")
	}
	if cmd == ciReview || cmd == ciDescribe {
		issueFile = fs.String("issue-context-file", "", "a JSON file holding the issue_context argument")
	}
	if err := fs.Parse(rest); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, usageErrorf("%s: unexpected argument; the pull request is given with --pr-url", name)
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
	var issues []issuectx.Input
	if issueFile != nil && flagSet(fs, "issue-context-file") {
		var err error
		if issues, err = readIssueContextFile(*issueFile); err != nil {
			return nil, usageErrorf("%s: --issue-context-file: %s", name, err.Error())
		}
	}

	var err error
	switch cmd {
	case ciReview:
		a := tools.PRReviewArgs{
			OutputLanguage: *outputLanguage, Publish: o.publish, Focus: *focus,
			IssueContext: issues, Related: related,
		}
		if flagSet(fs, "max-findings") {
			a.MaxFindings = maxFindings
		}
		if flagSet(fs, "incremental") {
			a.Incremental = incremental
		}
		if err = a.Validate(); err == nil {
			// The modes of X-33 (focus with publish) need no configuration.
			_, err = review.Args{Focus: a.Focus, Publish: a.Publish}.Validated(nil)
		}
		o.reviewArgs = a
	case ciDescribe:
		a := tools.PRDescribeArgs{OutputLanguage: *outputLanguage, Publish: o.publish, IssueContext: issues}
		if o.publish {
			// --publish updates the description as pr_describe does with
			// publish_mode=description; the title is never changed.
			a.PublishMode = tools.PublishModeDescription
		}
		err = a.Validate()
		o.describeArgs = a
	case ciImprove:
		a := tools.PRImproveArgs{OutputLanguage: *outputLanguage, Publish: o.publish}
		err = a.Validate()
		o.improveArgs = a
	}
	if err != nil {
		return nil, usageErrorf("%s: %s", name, tools.UserMessage(err))
	}
	// --pr-url wins over detection; without it, the pipeline's own
	// variables name the pull request.
	if o.prURL == "" {
		u, ci := detectPullRequest(env)
		if u == "" {
			return nil, usageErrorf(ciNoPullRequestShape, ci)
		}
		o.prURL, o.detected = u, ci
	}
	o.setPRURL(o.prURL)
	return o, nil
}

// setPRURL sets the pull request of every tool argument.
func (o *ciOptions) setPRURL(u string) {
	o.prURL = u
	o.reviewArgs.PRURL, o.describeArgs.PRURL, o.improveArgs.PRURL = u, u, u
}

// flagSet reports whether the flag name was given on the command line.
func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// issueContextFileItem is one item of the --issue-context-file JSON: the
// shape of the issue_context tool argument.
type issueContextFileItem struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// AcceptanceCriteria is a string or a list of strings; issuectx.Validate
	// checks it.
	AcceptanceCriteria any    `json:"acceptance_criteria,omitempty"`
	URL                string `json:"url,omitempty"`
}

// readIssueContextFile reads and validates the --issue-context-file:
// a JSON array of 1 to 3 issue_context items, at most 1 MiB, checked by
// issuectx.Validate exactly as the tool argument is. The error text names
// the problem, never the file's content.
func readIssueContextFile(path string) ([]issuectx.Input, error) {
	if path == "" {
		return nil, errors.New("the path is empty")
	}
	f, err := os.Open(path) //nolint:gosec // G304: the path is the operator's own flag
	if err != nil {
		return nil, errors.New("the file cannot be read")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, ciMaxIssueFileBytes+1))
	if err != nil {
		return nil, errors.New("the file cannot be read")
	}
	if len(data) > ciMaxIssueFileBytes {
		return nil, errors.New("the file is larger than 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var items []issueContextFileItem
	if err := dec.Decode(&items); err != nil || dec.More() {
		return nil, errors.New("the file must hold one JSON array of issue_context objects " +
			"(key, title, description, acceptance_criteria, url)")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("issue_context must hold 1 to %d issues", issuectx.MaxArgumentIssues)
	}
	in := make([]issuectx.Input, len(items))
	for i, it := range items {
		in[i] = issuectx.Input{Key: it.Key, Title: it.Title, Description: it.Description,
			AcceptanceCriteria: it.AcceptanceCriteria, URL: it.URL}
	}
	if _, err := issuectx.Validate(in); err != nil {
		return nil, err
	}
	return in, nil
}

// ciRepoCacheDefault puts the repository-context cache in the runner's own
// temporary directory when context.repo.cache_dir is unset (V-4 §1.5). The
// CI's checkout is never read.
func ciRepoCacheDefault(cfg *config.Config, env ciEnv) {
	if cfg.Context.Repo.CacheDir != "" {
		return
	}
	if dir := env(ciEnvRunnerTemp); dir != "" && filepath.IsAbs(dir) {
		cfg.Context.Repo.CacheDir = filepath.Join(dir, ciRepoCacheSubdir)
	}
}

// ciOutcome is what one run produced, for the output and the exit code.
type ciOutcome struct {
	// result is the tool's structured result and markdown its client
	// markdown.
	result   any
	markdown string
	// published is the publish outcome: nil without --publish.
	published *ciPublished
	// partial is the result's coverage.partial.
	partial bool
	// review is the pr_review result, for --fail-on; nil for the other
	// commands.
	review *review.Result
}

// ciPublished is the common part of the three tools' publish results.
type ciPublished struct {
	ok    bool
	id    string
	error string
	// what names what was posted, for the final line.
	what string
}

// ciExecute runs the command with the loaded configuration and returns the
// exit code. stdout gets the result (and the step summary its markdown);
// stderr the logs and one final line, followed on exit 3 by the line that
// says which --fail-on condition was met.
func ciExecute(ctx context.Context, o *ciOptions, cfg *config.Config, logger *slog.Logger, env ciEnv, stdout, stderr io.Writer) int {
	prefix := "ci " + o.cmd + ": "
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	// The URL is logged only redacted, as the tools log it.
	logger.Info(prefix+"pull request", "url", logging.RedactURL(o.prURL), "detected", o.detected, "publish", o.publish)

	resolver := wiring.NewResolver(cfg, logger)
	defer resolver.CloseIdleConnections()
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
	progress := func(stage string) { logger.Info(prefix + stage) }

	out, err := ciRun(ctx, o, cfg, logger, resolver, newLLM, progress)
	if err != nil {
		return ciFail(ctx, stderr, prefix, tools.UserMessage(err))
	}
	if o.asJSON {
		err = writeJSON(stdout, out.result)
	} else {
		_, err = io.WriteString(stdout, out.markdown)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, prefix+"could not write the result to stdout")
		return 1
	}
	ciStepSummary(env, out.markdown, logger, prefix)
	// The --fail-on check logs its warnings before the final line.
	reason := ciFailReason(o, out, logger, prefix)
	switch p := out.published; {
	case p == nil && o.publish:
		_, _ = fmt.Fprintln(stderr, prefix+"nothing posted")
	case p == nil:
		_, _ = fmt.Fprintln(stderr, prefix+"printed only (no --publish)")
	case !p.ok:
		return ciFail(ctx, stderr, prefix, "the result was not posted: "+p.error)
	case p.id != "":
		_, _ = fmt.Fprintln(stderr, prefix+"posted "+p.what+" "+p.id)
	default:
		_, _ = fmt.Fprintln(stderr, prefix+"posted "+p.what)
	}
	if reason != "" {
		_, _ = fmt.Fprintln(stderr, prefix+"exit 3: "+reason)
		return 3
	}
	return 0
}

// ciFailReason returns why the run exits 3 (V-4 §1.3), or "" when no
// --fail-on flag applies. It is called after the result was printed and
// posted.
func ciFailReason(o *ciOptions, out *ciOutcome, logger *slog.Logger, prefix string) string {
	if res := out.review; res != nil && o.failOn != "" {
		var findings []review.KeyIssue
		if res.Review != nil {
			findings = res.Review.KeyIssuesToReview
		}
		switch o.failOn {
		case ciFailOnAny:
			// Carried findings (X-36) are in the list: they count.
			if len(findings) > 0 {
				return fmt.Sprintf("--fail-on any is met (%d findings)", len(findings))
			}
		case ciFailOnSecurity:
			// A finding has no category: the security level is the
			// review's security concerns, or any finding of a run focused
			// on security.
			if res.Review != nil && res.Review.HasSecurityConcerns() {
				return "--fail-on security is met (security concerns)"
			}
			if res.Focus == ciFailOnSecurity && len(findings) > 0 {
				return fmt.Sprintf("--fail-on security is met (%d findings of a security-focused review)", len(findings))
			}
			if res.Focus != ciFailOnSecurity && !slices.Contains(res.EnabledFields, review.KeySecurityConcerns) {
				logger.Warn(prefix + "--fail-on security: the security_concerns field is off (review.require_security), so the review cannot meet it")
			}
		case ciFailOnNotCompliant:
			if res.Issue != nil && res.Issue.Compliance != nil && res.Issue.Compliance.Verdict == review.VerdictNotCompliant {
				return "--fail-on issue-not-compliant is met (verdict not_compliant)"
			}
			if res.Issue == nil || res.Issue.Compliance == nil {
				logger.Warn(prefix + "--fail-on issue-not-compliant: no issue was checked, so the review cannot meet it")
			}
		}
	}
	if o.failOnPartial && out.partial {
		return "--fail-on-partial is met (the coverage is partial)"
	}
	return ""
}

// ciStepSummary appends the markdown to the GitHub and Gitea Actions step
// summary file when GITHUB_STEP_SUMMARY is set (V-4 §1.4). A failure is a
// warning, never an error.
func ciStepSummary(env ciEnv, markdown string, logger *slog.Logger, prefix string) {
	path := env(ciEnvStepSummary)
	if path == "" {
		return
	}
	if !strings.HasSuffix(markdown, "\n") {
		markdown += "\n"
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: the runner's own GITHUB_STEP_SUMMARY
	if err == nil {
		_, err = io.WriteString(f, markdown)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		logger.Warn(prefix + "the step summary could not be written")
	}
}

// ciFail prints the final error line and returns exit code 1. When the
// --timeout deadline passed, the line is the timeout class whatever error
// the cut-off request reported.
func ciFail(ctx context.Context, stderr io.Writer, prefix, msg string) int {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		msg = ciTimeoutLine
	}
	_, _ = fmt.Fprintln(stderr, prefix+msg)
	return 1
}

// ciRun runs the tool of the command through its Prepare and Run, exactly as
// the MCP handlers do, and collects the outcome.
func ciRun(ctx context.Context, o *ciOptions, cfg *config.Config, logger *slog.Logger, resolver tools.PRResolver,
	newLLM func(*config.Config, *slog.Logger) (review.Completer, error), progress func(string)) (*ciOutcome, error) {
	switch o.cmd {
	case ciReview:
		call, err := tools.PreparePRReview(tools.ReviewDeps{Config: cfg, Resolver: resolver, NewLLM: newLLM, Logger: logger}, o.reviewArgs)
		if err != nil {
			return nil, err
		}
		res, text, err := call.Run(ctx, progress)
		if err != nil {
			return nil, err
		}
		out := &ciOutcome{result: res, markdown: text, partial: res.Coverage.Partial, review: res}
		if p := res.Publish; p != nil {
			out.published = &ciPublished{ok: p.Published, id: p.CommentID, error: p.Error, what: "overview"}
		}
		return out, nil
	case ciDescribe:
		call, err := tools.PreparePRDescribe(tools.DescribeDeps{Config: cfg, Resolver: resolver, NewLLM: newLLM, Logger: logger}, o.describeArgs)
		if err != nil {
			return nil, err
		}
		res, text, err := call.Run(ctx, progress)
		if err != nil {
			return nil, err
		}
		out := &ciOutcome{result: res, markdown: text, partial: res.Coverage.Partial}
		if p := res.Publish; p != nil {
			what := "description comment"
			if p.Mode == tools.PublishModeDescription {
				what = "description"
			}
			out.published = &ciPublished{ok: p.Published, id: p.CommentID, error: p.Error, what: what}
		}
		return out, nil
	default:
		call, err := tools.PreparePRImprove(tools.ImproveDeps{Config: cfg, Resolver: resolver, NewLLM: newLLM, Logger: logger}, o.improveArgs)
		if err != nil {
			return nil, err
		}
		res, text, err := call.Run(ctx, progress)
		if err != nil {
			return nil, err
		}
		out := &ciOutcome{result: res, markdown: text, partial: res.Coverage.Partial}
		if p := res.Publish; p != nil {
			out.published = &ciPublished{ok: p.Published, id: p.CommentID, error: p.Error, what: "overview"}
		}
		return out, nil
	}
}
