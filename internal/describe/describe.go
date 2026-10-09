// Package describe implements the pr_describe pipeline (v2 spec §3, X-26,
// design Y-4 to Y-7): the adapted description prompts and Run, which
// generates a title, the PR types, a short summary and a walkthrough of the
// changed files.
//
// It runs the same fetch, filter, budget and prepare steps as pr_review,
// with the plain diff view of pr_ask and no discussion or repository
// context. A diff that leaves files out is described in parts (X-19, Y-6):
// each part asks for the files walkthrough only, and one reduce call over
// the parts' walkthrough, without the diff, writes the title, the types and
// the summary.
//
// Dependency rule (deps_test.go): nothing in this package's dependency
// closure may import internal/review, internal/ask, the repository context
// (internal/repoctx, internal/gitctx) or the tool and transport layers. What
// it shares with pr_review lives in internal/llmrun; it parses YAML with
// internal/yamlrepair, as pr_review does.
package describe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Resolver maps a PR URL to a reference and a provider (X-2). It has the
// shape of tools.PRResolver and is implemented by *provider.Resolver.
type Resolver interface {
	Resolve(rawURL string) (provider.PRRef, provider.Provider, error)
}

// Completer is one chat completion. It is implemented by *llm.Client, which
// owns the transport retries (DQ-9 step 1).
type Completer interface {
	Complete(ctx context.Context, system, user string) (*llm.Response, error)
}

// Deps are the injectable dependencies of Run.
type Deps struct {
	// Config is the effective configuration and ConfigErr its load error;
	// a nil Config or a non-nil ConfigErr is the degraded mode.
	Config    *config.Config
	ConfigErr error
	// Logger receives debug lines with names and numbers only (X-8). Nil
	// discards.
	Logger   *slog.Logger
	Resolver Resolver
	LLM      Completer
	// Progress, when set, is called with a Stage* word (or CallingModelPart)
	// as the pipeline advances. Nil is ignored. It must not block.
	Progress func(stage string)
	// RenderProvider renders the markdown published with Args.Publish (a
	// comment, or the body of the description region). Nil makes a publish
	// fail with the fixed message.
	RenderProvider ProviderRenderer
}

// Args are the per-call arguments. Zero values fall back to the
// configuration.
type Args struct {
	PRURL string
	// OutputLanguage replaces output.language when non-empty. It applies to
	// every call of the run: the parts and the reduce call.
	OutputLanguage string
	// MaxChunks is the most calls with a diff (parts) of the description
	// (X-19); 0 or less takes review.max_chunks, and 1 describes in one
	// call.
	MaxChunks int
	// Publish writes the description to the PR (v2 spec §4). The tool layer
	// validates PublishMode and UpdateTitle before the pipeline runs.
	Publish bool
	// PublishMode is "" (PublishModeComment), PublishModeComment or
	// PublishModeDescription.
	PublishMode string
	// UpdateTitle, with PublishModeDescription, replaces the PR title with
	// the generated one.
	UpdateTitle bool
}

// errNoWiring reports a caller bug: Run needs a resolver and an LLM.
var errNoWiring = errors.New("describe: resolver or LLM dependency missing")

// Progress stages reported through Deps.Progress. They are fixed words,
// never PR content. The first four are pr_review's.
const (
	StageFetching      = "fetching"
	StagePreparingDiff = "preparing diff"
	StageCallingModel  = "calling model"
	StageRendering     = "rendering"
	// StageSummarizing is the reduce call of a description in parts; it
	// follows the parts' "calling model (part I of N)" stages.
	StageSummarizing = "calling model (summary)"
)

// stageCallingModelPartFormat is the progress stage of part I of N; it is
// pr_review's text, so a progress reporter sizes its total the same way.
const stageCallingModelPartFormat = StageCallingModel + " (part %d of %d)"

// CallingModelPart is the progress stage of the model call of part i of a
// description in n > 1 parts: "calling model (part I of N)".
func CallingModelPart(i, n int) string {
	return fmt.Sprintf(stageCallingModelPartFormat, i, n)
}

// Error classes of the describe pipeline.
const (
	// ClassUnparseable: the answer had no usable description mapping, also
	// after the one re-ask.
	ClassUnparseable llmrun.ErrorClass = "describe_unparseable"
)

// ErrUnparseable is the classified error of an unusable answer. Its text is
// a fixed sentence.
var ErrUnparseable = &llmrun.Error{Class: ClassUnparseable, Eligible: true,
	Message: "the model's answer could not be parsed as a pull request description, also after one retry; " +
		"try again, or check that llm.model follows the YAML output instructions"}

// Plan is the outcome of the steps up to the model calls. Run continues
// from it; diag describe --dry-run prints it.
type Plan struct {
	// Result is the description so far: coverage, notes and the metadata
	// known before the model calls.
	Result *Result
	// Budget is the token budget the diff was prepared against.
	Budget tokens.Budget
	// Prompts are the final prompts of the first model call (of part 1 for
	// a description in parts). They are zero when Empty is set. Callers
	// must never log them (X-8).
	Prompts Prompts
	// Empty reports that nothing is left to describe after filtering, so no
	// model call is made.
	Empty bool

	// parts are the calls with a diff: one for a description in one call, N
	// for a description in parts (X-19).
	parts []*part
	// chunks is the packing of a description in parts; nil for one call.
	chunks *diffpipe.Chunks
	flt    *filter.Filter
	ref    provider.PRRef
	p      provider.Provider
	// pr is the PR as it was read at the start of the run.
	pr *provider.PullRequest
	// in is the PR text of the prompts, for the reduce call.
	in  PromptInput
	log *slog.Logger
}

// part is one model call with a diff.
type part struct {
	prep *diffpipe.Prepared
	fit  *fitted
	// cov holds the part's own files after the request-size guard (a
	// description in parts only), as in pr_review.
	cov Coverage
}

func progress(deps Deps, stage string) {
	if deps.Progress != nil {
		deps.Progress(stage)
	}
}

// Run describes one pull request. It returns a classified error
// (*llmrun.Error, *provider.Error, *llm.Error) or the result.
func Run(ctx context.Context, deps Deps, args Args) (*Result, error) {
	if deps.Config == nil || deps.ConfigErr != nil {
		return nil, llmrun.ErrConfigInvalid
	}
	if deps.Resolver == nil || deps.LLM == nil {
		return nil, errNoWiring
	}
	pl, err := Prepare(ctx, deps, args)
	if err != nil {
		return nil, err
	}
	if pl.Empty {
		// Nothing to describe: no model call.
		//
		// DESIGN-QUESTION: is this empty description published when publish
		// is set? — chose yes, as pr_review does: the comment or region then
		// shows the coverage and the note.
		pl.Result.Notes = append(pl.Result.Notes, NoteNoReviewableChanges)
		pl.publish(ctx, deps, args)
		return pl.Result, nil
	}
	finish := pl.finishParts
	if len(pl.parts) == 1 {
		finish = pl.finishOne
	}
	res, err := finish(ctx, deps)
	if err != nil {
		return nil, err
	}
	pl.publish(ctx, deps, args)
	return res, nil
}

// Prepare runs the steps up to the model calls: configuration, resolution,
// the pull request, its filtered diff and commit messages, token
// measurement, diff preparation (in parts when needed) and the
// request-size guard. It sends nothing to the model and needs no Deps.LLM
// (besides the context-window probe when llm.context_window is unset).
func Prepare(ctx context.Context, deps Deps, args Args) (*Plan, error) {
	if deps.Config == nil || deps.ConfigErr != nil {
		return nil, llmrun.ErrConfigInvalid
	}
	if deps.Resolver == nil {
		return nil, errNoWiring
	}
	cfg := deps.Config
	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	factor := cfg.LLM.TokenEstimateFactor
	language := cfg.Output.Language
	if args.OutputLanguage != "" {
		language = args.OutputLanguage
	}
	maxChunks := args.MaxChunks
	if maxChunks <= 0 {
		maxChunks = cfg.Review.MaxChunks
	}

	// Resolve, fetch the PR, its filtered diff and its commit messages.
	progress(deps, StageFetching)
	flt, err := filter.New(cfg)
	if err != nil {
		return nil, err
	}
	ref, p, err := deps.Resolver.Resolve(args.PRURL)
	if err != nil {
		return nil, err
	}
	// The context window comes from llm.context_window or, when unset, from
	// the endpoint (X-15), before any provider request.
	window, _, err := llmrun.ContextWindow(ctx, cfg, deps.LLM)
	if err != nil {
		return nil, err
	}
	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return nil, err
	}
	d, err := p.GetDiff(ctx, ref, pr, provider.DiffOptions{Include: flt.Include})
	if err != nil {
		return nil, err
	}
	// The commit messages are context, not the subject: a failure to read
	// them does not fail the description, it is noted.
	commits, commitsErr := p.GetCommitMessages(ctx, ref)
	if commitsErr != nil {
		if ctx.Err() != nil {
			return nil, commitsErr
		}
		log.Debug("describe: commit messages not read", "class", llmrun.FailureClass(commitsErr))
		commits = nil
	}
	progress(deps, StagePreparingDiff)

	in := PromptInput{
		Language:       language,
		Title:          pr.Title,
		Branch:         pr.SourceBranch,
		TargetBranch:   pr.TargetBranch,
		Description:    tokens.ClipDescription(pr.Description, cfg.Diff.MaxDescriptionTokens, factor),
		CommitMessages: CommitBlock(commits, cfg.Diff.MaxCommitsTokens, factor),
	}
	promptTokens, err := ScaffoldingTokens(in, factor)
	if err != nil {
		return nil, err
	}
	budget := tokens.Budget{
		ContextWindow:   window,
		MaxOutputTokens: cfg.LLM.MaxOutputTokens,
		PromptTokens:    promptTokens,
		Factor:          factor,
		MaxDiffTokens:   tokens.Cap(cfg.Diff.MaxTokens),
	}
	if err := budget.RequireCapacity(); err != nil {
		return nil, llmrun.DoesNotFit(err)
	}

	// The plain diff (ModePlain): the description is about what changed,
	// not about line anchors.
	dIn := diffpipe.Input{Files: d.Files, Skipped: d.Skipped, Mode: diffpipe.ModePlain, Budget: budget, Diff: cfg.Diff}
	prep, err := diffpipe.Prepare(dIn)
	if err != nil {
		if errors.Is(err, tokens.ErrDoesNotFit) {
			return nil, llmrun.DoesNotFit(err)
		}
		return nil, err
	}
	res := &Result{
		Files:    []File{},
		Coverage: llmrun.BuildCoverage(prep, flt),
		Notes:    []string{},
		Metadata: Metadata{
			Model: cfg.LLM.Model, ContextWindow: window, PromptTokens: promptTokens,
			DiffTokens: prep.Tokens, FastPath: prep.FastPath, CommitMessages: len(commits),
		},
	}
	if commitsErr != nil {
		res.Notes = append(res.Notes, NoteCommitsUnavailable)
	}
	log.Debug("describe: diff prepared", "url", logging.RedactURL(ref.URL), "files", len(d.Files),
		"provider_skipped", len(d.Skipped), "included", len(prep.Included), "clipped", len(prep.Clipped),
		"fast_path", prep.FastPath, "prompt_tokens", promptTokens, "diff_tokens", prep.Tokens,
		"commit_messages", len(commits))

	pl := &Plan{Result: res, Budget: budget, flt: flt, ref: ref, p: p, pr: pr, in: in, log: log}
	if prep.Text == "" {
		pl.Empty = true
		res.Notes = append(res.Notes, partialNotes(llmrun.PartialNotes(&res.Coverage, budget))...)
		return pl, nil
	}

	// A diff that leaves files out is described in parts when
	// review.max_chunks allows it (X-19, Y-6), exactly as pr_review packs
	// them; otherwise, and when the packing yields one part, the
	// description is the one call below.
	if maxChunks > 1 && llmrun.LeavesFilesOut(prep) {
		ok, err := pl.planParts(dIn, in, maxChunks)
		if err != nil {
			return nil, err
		}
		if ok {
			return pl, nil
		}
	}

	fit, err := fitPrompts(in, prep.Text, budget)
	if err != nil {
		return nil, err
	}
	pl.parts = []*part{{prep: prep, fit: fit}}
	pl.Prompts = fit.prompts
	res.Metadata.RequestTokens = fit.requestTokens
	res.Coverage.ModelCalls = 1
	if fit.keptLines >= 0 {
		llmrun.TrimCoverage(&res.Coverage, prep.Text, fit.keptLines, changeTypes(d.Files))
		res.Metadata.DiffTrimmed = true
		res.Metadata.DiffTokens = tokens.Estimate(fit.diff, factor)
		res.Notes = append(res.Notes, NoteDiffTrimmed)
		log.Debug("describe: diff trimmed by the request-size guard", "kept_lines", fit.keptLines,
			"request_tokens", fit.requestTokens)
	}
	if n := len(res.Coverage.Clipped); n > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(llmrun.NoteClippedFormat,
			llmrun.CountPhrase(n, "file was", "files were")))
	}
	res.Notes = append(res.Notes, partialNotes(llmrun.PartialNotes(&res.Coverage, budget))...)
	return pl, nil
}

func changeTypes(files []provider.FilePatch) map[string]provider.ChangeType {
	types := map[string]provider.ChangeType{}
	for _, f := range files {
		types[f.Path] = f.Type
	}
	return types
}

// partialNotes words the shared X-18 hints for pr_describe: "To review
// every file" becomes "To describe every file". The other notes are kept.
func partialNotes(notes []string) []string {
	for i, n := range notes {
		notes[i] = strings.Replace(n, "To review every file", "To describe every file", 1)
	}
	return notes
}

// finishOne makes the call of a description in one call: the full schema,
// validated against the files the call was shown.
func (pl *Plan) finishOne(ctx context.Context, deps Deps) (*Result, error) {
	res, log := pl.Result, pl.log
	pt := pl.parts[0]
	progress(deps, StageCallingModel)
	shown := shownFiles(&res.Coverage)
	a, info, err := pl.call(ctx, deps, pt.fit.prompts, pt.fit.reaskUser, func(data map[string]any) (*answer, error) {
		return convert(data, modeFull, shown)
	})
	res.Metadata.LLMCalls += info.calls
	if err != nil {
		return nil, err
	}
	pl.record(info)
	res.Title, res.Type, res.Description, res.Files = a.title, a.types, a.description, a.files
	res.Notes = append(res.Notes, a.notes()...)
	if n := markNotReturned(&res.Coverage, a.returned); n > 0 {
		res.Notes = append(res.Notes, noteNotReturned(n))
	}
	log.Debug("describe: done", "files", len(res.Files), "types", len(res.Type), "title", res.Title != nil,
		"llm_calls", res.Metadata.LLMCalls, "reasked", res.Metadata.Reasked, "unknown_files", a.unknown,
		"duplicate_files", a.duplicates, "types_dropped", a.typesDropped)
	return res, nil
}

// finishParts makes the calls of a description in parts (Y-6): one call per
// part for its files walkthrough, validated against the part's own files,
// then one reduce call over the merged walkthrough for the title, the types
// and the summary.
func (pl *Plan) finishParts(ctx context.Context, deps Deps) (*Result, error) {
	res, log := pl.Result, pl.log
	n := len(pl.parts)
	answers := make([]*answer, n)
	failed := make([]bool, n)
	var failNotes []string
	var firstErr error
	nFailed := 0
	for i, pt := range pl.parts {
		progress(deps, CallingModelPart(i+1, n))
		shown := shownFiles(&pt.cov)
		a, info, err := pl.call(ctx, deps, pt.fit.prompts, pt.fit.reaskUser, func(data map[string]any) (*answer, error) {
			return convert(data, modeFiles, shown)
		})
		res.Metadata.LLMCalls += info.calls
		if err != nil {
			if ctx.Err() != nil {
				// A cancelled run stops at once.
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			nFailed++
			failed[i] = true
			class := llmrun.FailureClass(err)
			failNotes = append(failNotes, noteFailedPart(i+1, n, class))
			log.Debug("describe: part failed", "part", i+1, "parts", n, "class", class)
			continue
		}
		pl.record(info)
		answers[i] = a
	}
	if nFailed == n {
		// Every part failed: the first part's classified error, as for a
		// description in one call.
		return nil, firstErr
	}

	covs := make([]Coverage, n)
	for i, pt := range pl.parts {
		covs[i] = pt.cov
	}
	res.Coverage = llmrun.PartsCoverage(pl.chunks, covs, failed, pl.flt)
	res.Notes = append(res.Notes, failNotes...)

	// The walkthrough in part order; each part's entries are already
	// limited to its own files, so no path appears in two parts.
	merged := &answer{returned: map[string]bool{}}
	for _, a := range answers {
		if a == nil {
			continue
		}
		merged.files = append(merged.files, a.files...)
		merged.unknown += a.unknown
		merged.duplicates += a.duplicates
		for p := range a.returned {
			merged.returned[p] = true
		}
	}
	res.Files = llmrun.NonNil(merged.files)
	res.Notes = append(res.Notes, merged.notes()...)
	if c := markNotReturned(&res.Coverage, merged.returned); c > 0 {
		res.Notes = append(res.Notes, noteNotReturned(c))
	}

	progress(deps, StageSummarizing)
	pl.reduce(ctx, deps)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	log.Debug("describe: done", "parts", n, "failed_parts", nFailed, "files", len(res.Files),
		"types", len(res.Type), "title", res.Title != nil, "llm_calls", res.Metadata.LLMCalls,
		"unknown_files", merged.unknown, "duplicate_files", merged.duplicates)
	return res, nil
}

// reduce makes the reduce call of a description in parts and records its
// outcome: the title, the types and the summary, or, when the call fails or
// its request cannot fit, the fallback of v2 spec §3.3 (no title, no types,
// the files' titles as the description, NoteReduceFailed).
func (pl *Plan) reduce(ctx context.Context, deps Deps) {
	res, log := pl.Result, pl.log
	prompts, reaskUser, withSummaries, ok := pl.reducePrompts()
	if !ok {
		log.Debug("describe: reduce request does not fit", "files", len(res.Files))
		pl.reduceFallback()
		return
	}
	if !withSummaries {
		res.Notes = append(res.Notes, NoteReduceWithoutSummaries)
	}
	a, info, err := pl.call(ctx, deps, prompts, reaskUser, func(data map[string]any) (*answer, error) {
		return convert(data, modeReduce, nil)
	})
	res.Metadata.LLMCalls += info.calls
	if err != nil {
		log.Debug("describe: reduce failed", "class", llmrun.FailureClass(err))
		pl.reduceFallback()
		return
	}
	pl.record(info)
	res.Title, res.Type, res.Description = a.title, a.types, a.description
	res.Notes = append(res.Notes, a.notes()...)
}

// reducePrompts renders the reduce prompts with the files' summaries, or,
// when that request does not fit the context window, with their titles
// only. ok is false when neither fits.
func (pl *Plan) reducePrompts() (p Prompts, reaskUser string, withSummaries, ok bool) {
	b := pl.Budget
	limit := b.ContextWindow - b.HardReserve()
	for _, summaries := range []bool{true, false} {
		r, err := RenderReducePrompts(ReduceInput{PR: pl.in, Walkthrough: Walkthrough(pl.Result.Files, summaries)})
		if err != nil {
			return Prompts{}, "", false, false
		}
		ru, err := withReaskNote(r.User)
		if err != nil {
			return Prompts{}, "", false, false
		}
		if tokens.RequestTokens(r.System, ru, b.Factor) <= limit {
			return r, ru, summaries, true
		}
	}
	return Prompts{}, "", false, false
}

// reduceFallback is the outcome of a failed reduce call (v2 spec §3.3).
func (pl *Plan) reduceFallback() {
	res := pl.Result
	res.Title, res.Type = nil, nil
	var lines []string
	for _, f := range res.Files {
		if f.Title != "" {
			lines = append(lines, "- "+f.Title)
		}
	}
	res.Description = nil
	if len(lines) > 0 {
		s := strings.Join(lines, "\n")
		res.Description = &s
	}
	res.Notes = append(res.Notes, NoteReduceFailed)
}

// callInfo describes the completions of one call.
type callInfo struct {
	// calls counts the completions (1, or 2 with the re-ask), also when the
	// call failed.
	calls              int
	reasked, truncated bool
	tactic             string
}

// call makes one model call: the completion, the YAML repair, the
// conversion and at most one re-ask with the same prompts plus ReaskNote
// (as pr_review). The returned info holds also on an error.
func (pl *Plan) call(ctx context.Context, deps Deps, p Prompts, reaskUser string,
	conv func(map[string]any) (*answer, error)) (*answer, callInfo, error) {
	var info callInfo
	for attempt := range 2 {
		user := p.User
		if attempt == 1 {
			user = reaskUser
			info.reasked = true
		}
		resp, err := deps.LLM.Complete(ctx, p.System, user)
		info.calls++
		if err != nil {
			return nil, info, err
		}
		info.truncated = resp.Truncated
		data, tactic := load(resp.Content)
		info.tactic = tactic
		pl.log.Debug("describe: answer loaded", "attempt", attempt+1, "repair_tactic", tactic,
			"truncated", resp.Truncated, "prompt_tokens_reported", resp.Usage.PromptTokens,
			"completion_tokens_reported", resp.Usage.CompletionTokens)
		a, err := conv(data)
		if err == nil {
			return a, info, nil
		}
		if attempt == 1 {
			return nil, info, ErrUnparseable.WithCause(err)
		}
	}
	return nil, info, ErrUnparseable // not reached
}

// record adds a successful call's flags to the metadata and its notes once.
func (pl *Plan) record(info callInfo) {
	res := pl.Result
	m := &res.Metadata
	if m.RepairTactic == "" {
		m.RepairTactic = info.tactic
	}
	if info.truncated && !m.Truncated {
		m.Truncated = true
		res.Notes = append(res.Notes, NoteTruncated)
	}
	if info.reasked && !m.Reasked {
		m.Reasked = true
		res.Notes = append(res.Notes, NoteReasked)
	}
}

// shownFiles is the set of files a call was shown, the only paths its
// walkthrough may name (v2 spec §3.4): the coverage's included, clipped and
// listed-deleted files.
func shownFiles(c *Coverage) map[string]bool {
	shown := map[string]bool{}
	for _, l := range [][]string{c.Included, c.Clipped, c.DeletedListed} {
		for _, p := range l {
			shown[p] = true
		}
	}
	return shown
}

// markNotReturned moves the included and listed-deleted files that got no
// walkthrough entry to Skipped with reason SkipNotReturned (Y-7) and returns
// how many moved. A clipped file stays clipped: it is not described either
// way.
func markNotReturned(c *Coverage, returned map[string]bool) int {
	moved := 0
	keep := func(paths []string) []string {
		out := []string{}
		for _, p := range paths {
			if returned[p] {
				out = append(out, p)
				continue
			}
			c.Skipped = append(c.Skipped, llmrun.SkippedFile{Path: p, Reason: SkipNotReturned})
			moved++
		}
		return out
	}
	c.Included = keep(c.Included)
	c.DeletedListed = keep(c.DeletedListed)
	c.Finalize()
	return moved
}
