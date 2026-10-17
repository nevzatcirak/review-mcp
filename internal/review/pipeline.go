package review

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/llm"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/projctx"
	"github.com/nevzatcirak/review-mcp/internal/prompt"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repocfg"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/review/incremental"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
	"github.com/nevzatcirak/review-mcp/internal/yamlrepair"
)

// Resolver maps a PR URL to a reference and a provider (X-2). It has the
// shape of tools.PRResolver and is implemented by *provider.Resolver; it is
// declared here so this package does not import internal/tools.
type Resolver interface {
	Resolve(rawURL string) (provider.PRRef, provider.Provider, error)
}

// Completer is one chat completion. It is implemented by *llm.Client, which
// owns the transport retries (DQ-9 step 1).
type Completer interface {
	Complete(ctx context.Context, system, user string) (*llm.Response, error)
}

// ProviderRenderer renders a result as a published PR comment for a
// provider's capabilities (DQ-16 provider profile; WP-PR-4d).
type ProviderRenderer func(res *Result, caps provider.Capabilities) string

// InlineRenderer renders the body of a finding's inline comment for a
// provider's capabilities (spec P7 §3.2), without the fingerprint marker:
// the pipeline appends the marker as the body's last line.
type InlineRenderer func(ki *KeyIssue, caps provider.Capabilities) string

// Deps are the injectable dependencies of Run.
type Deps struct {
	// Config is the effective configuration and ConfigErr its load error;
	// a nil Config or a non-nil ConfigErr is the degraded mode (step 1).
	Config    *config.Config
	ConfigErr error
	// Logger receives debug lines with names and numbers only (X-8). Nil
	// discards.
	Logger   *slog.Logger
	Resolver Resolver
	LLM      Completer
	// Clock supplies the prompt date and the run time shown in the
	// published overview (Metadata.ReviewedAt); nil is the wall clock.
	Clock prompt.Clock
	// RenderProvider renders the overview comment published with
	// Args.Publish.
	RenderProvider ProviderRenderer
	// RenderInline renders the inline comment of an anchorable finding
	// published with Args.Publish. Nil posts no inline comments.
	RenderInline InlineRenderer
	// Progress, when set, is called with a Stage* word as the pipeline
	// advances (the pr_review tool turns them into MCP progress
	// notifications). Nil is ignored. It must not block.
	Progress func(stage string)
	// RepoContext is the git backend of repository context (X-22); nil
	// selects the real one (gitctx.New from the configuration). It is used
	// only when context.repo.enabled is set. Tests substitute a fake.
	RepoContext repoctx.Backend
}

// Args are the per-call arguments (DQ-25, X-1). Zero values fall back to
// the configuration.
//
// DESIGN-QUESTION: can a call clear review.extra_instructions with an
// empty extra_instructions argument? — chose no (empty means "not given")
// because the MCP arguments are optional strings and the spec defines no
// separate "unset" form; validating output_language and the 1-20 range of
// max_findings is the tool layer's job (WP-PR-4e).
type Args struct {
	PRURL string
	// ExtraInstructions replaces review.extra_instructions when non-empty.
	ExtraInstructions string
	// OutputLanguage replaces output.language when non-empty.
	OutputLanguage string
	// MaxFindings replaces review.max_findings when positive.
	MaxFindings int
	// Publish also posts the review as a PR comment, and its anchorable
	// findings as inline comments (X-11).
	Publish bool
	// InlineFindings turns the inline comments of a publish on or off; nil
	// means on.
	//
	// DESIGN-QUESTION: where does review.inline_findings live before
	// WP-PR-7f adds the config row and the pr_review argument (spec P7
	// §6.2)? — chose a per-call option whose nil value is the documented
	// default (true), so that 7f only adds the row as the nil fallback and
	// maps the tool argument here; adding the row now would split one
	// config change over two packages.
	InlineFindings *bool
	// PersistentOverview looks up the overview of an earlier run and edits
	// it in place instead of posting a new one (X-12); nil means on.
	// Like InlineFindings, it is a per-call option until WP-PR-7f adds
	// review.persistent_overview as its nil fallback and the tool argument.
	PersistentOverview *bool
	// MaxDiscussionTokens is the token budget of the existing-discussion
	// block of the prompt (spec P7 §5.2): nil means
	// DefaultMaxDiscussionTokens, 0 or less turns the block off.
	//
	// DESIGN-QUESTION: where does review.max_discussion_tokens live before
	// WP-PR-7f adds its config row (spec P7 §6.2 lists it there)? — chose a
	// per-call option, as for InlineFindings and PersistentOverview: its nil
	// value is the compiled default, and 7f adds the row and the
	// REVIEW_MCP_REVIEW_MAX_DISCUSSION_TOKENS variable as the value mapped
	// here, in the one package that owns the config table.
	MaxDiscussionTokens *int
	// MaxChunks is the most model calls (parts) of the review (X-19); 0 or
	// less takes review.max_chunks, and 1 reviews in one call as v1.0 did.
	MaxChunks int
	// Focus narrows the review to one area (X-33): a known value
	// (FocusValues) or a free text; "" reviews everything. A focused run
	// never publishes (Validated).
	Focus string
	// Findings, when not nil (an empty list included), are published
	// without a model call instead of a model's review (X-33, merged
	// publish); they need Publish and exclude Focus (Validated).
	Findings []SuppliedFinding
	// SecurityConcerns and PerformanceConcerns are the concerns texts of a
	// merged publish; "" leaves the field absent. They need Findings.
	SecurityConcerns    string
	PerformanceConcerns string
	// Incremental replaces review.incremental for the call when not nil
	// (X-36): review only the files that changed since the last review.
	// It is not filled by WithConfigDefaults: Prepare reads the
	// configuration itself, because only an explicit true asks for the note
	// of a pull request without an earlier review. A merged publish
	// (Findings) ignores it.
	Incremental *bool
}

// WithConfigDefaults returns a with every option the call left unset (nil,
// or 0 for MaxChunks) taken from cfg: review.inline_findings,
// review.persistent_overview, review.max_discussion_tokens and
// review.max_chunks. A value the call set wins. Without it, nil
// keeps the documented defaults (on, on, DefaultMaxDiscussionTokens).
func (a Args) WithConfigDefaults(cfg *config.Config) Args {
	if cfg == nil {
		return a
	}
	if a.InlineFindings == nil {
		v := cfg.Review.InlineFindings
		a.InlineFindings = &v
	}
	if a.PersistentOverview == nil {
		v := cfg.Review.PersistentOverview
		a.PersistentOverview = &v
	}
	if a.MaxDiscussionTokens == nil {
		v := cfg.Review.MaxDiscussionTokens
		a.MaxDiscussionTokens = &v
	}
	if a.MaxChunks <= 0 {
		a.MaxChunks = cfg.Review.MaxChunks
	}
	return a
}

// errNoWiring reports a caller bug: Run needs a resolver and an LLM.
var errNoWiring = errors.New("review: resolver or LLM dependency missing")

// Progress stages reported through Deps.Progress (and by the pr_review tool
// for the last one). They are fixed words, never PR content.
const (
	StageFetching = "fetching"
	// StageRepoContext is reported, only when context.repo.enabled is set and
	// the diff defines symbols to look for, while the pull request head is
	// fetched (X-22). It precedes StagePreparingDiff.
	StageRepoContext   = "fetching repository context"
	StagePreparingDiff = "preparing diff"
	StageCallingModel  = "calling model"
	StageRendering     = "rendering"
)

// Plan is the outcome of steps 1 to 6 of the pipeline: everything up to the
// model call. Run continues from it; diag review --dry-run prints it.
type Plan struct {
	// Result is the review so far: PR, coverage, notes and the metadata
	// known before the model call (prompt, diff and request tokens).
	Result *Result
	// Budget is the token budget the diff was prepared against.
	Budget tokens.Budget
	// Prompts are the final prompts of the first model call (of part 1 for
	// a review in parts). They are zero when Empty is set. Callers must
	// never log them (X-8).
	Prompts Prompts
	// Empty reports that nothing reviewable is left after filtering, so no
	// model call is made (step 5).
	Empty bool
	// Unchanged reports an incremental run with nothing to review (X-36):
	// the last review recorded the current head, or no listed file changed
	// since it. Empty is set too, so every caller that skips the model for
	// an empty plan skips it here; Run also publishes nothing.
	Unchanged bool

	// parts are the model calls of the review, in order: one for a review
	// in one call, N for a review in parts (X-19). Each has its prepared
	// diff, its final prompts and its own coverage.
	parts []*part
	// chunks is the packing of a review in parts; nil for one call.
	chunks *diffpipe.Chunks
	// flt is the filter, for the coverage of a review in parts.
	flt *filter.Filter
	// maxTotal caps the merged findings (config.EffectiveMaxTotalFindings).
	maxTotal int
	// repoCtx is the repository context of a review in parts, summed over
	// the parts (repoCoverage); zero for a review in one call.
	repoCtx llmrun.RepoContext
	// RepoReport is the repository-context detail for diag review
	// --dry-run; nil when repository context is off.
	RepoReport *repoctx.Report
	// proj is the project context (X-32), whose coverage a review in parts
	// carries too.
	proj *projctx.Context
	// cfg is the run's merged configuration and args the call's arguments
	// with the configuration's defaults filled in (X-40); repo is the
	// repository config the merge came from.
	cfg         *config.Config
	args        Args
	repo        *repocfg.Outcome
	ref         provider.PRRef
	p           provider.Provider
	pr          *provider.PullRequest
	d           *provider.Diff
	toggles     Toggles
	maxFindings int
	log         *slog.Logger
	// own are review-mcp's own inline finding comments already on the PR
	// (X-29, spec P7 §5.3); a finding on the place of one of them, or with
	// the fingerprint of one, is not posted again. Prepare fills it from
	// the PR's threads when the run will publish inline comments.
	own []ownFinding

	// held are the records of the held lines of the overview the
	// incremental lookup found (spec v2.1 §7.2); carried are the findings
	// carried forward from them (carryForward), appended after the run's
	// own findings.
	held    []heldRecord
	carried []KeyIssue
	// heldNote is the note of overview-only findings without a held line
	// that the last render of the overview added; "" for none.
	heldNote string

	// The PR's threads and the token's user, each read at most once per
	// run (listThreads, currentUser).
	threadsRead bool
	threads     []provider.Thread
	threadsErr  error
	meRead      bool
	me          provider.User
	meErr       error
}

// reviewedAt is the run time for Metadata.ReviewedAt: RFC 3339 in UTC, to
// the second.
func reviewedAt(c prompt.Clock) string {
	if c == nil {
		c = prompt.SystemClock{}
	}
	return c.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

func progress(deps Deps, stage string) {
	if deps.Progress != nil {
		deps.Progress(stage)
	}
}

// Run reviews one pull request (spec P4 §4.3). It returns a classified
// error (*Error, *provider.Error, *llm.Error) or the result; a failed
// publish is reported in the result, never as an error.
func Run(ctx context.Context, deps Deps, args Args) (*Result, error) {
	res, err := run(ctx, deps, args)
	if res != nil {
		// The rate-limit waits of the run's client (X-37, v2.1 spec V-6
		// §2.3): in the result, after the run, so a published comment
		// does not carry them.
		res.Metadata.RateLimitWaits, res.Metadata.RateLimitWaitMS = llmrun.RateLimitWaits(deps.LLM)
		if note := llmrun.RateLimitNote(res.Metadata.RateLimitWaitMS); note != "" {
			res.Notes = append(res.Notes, note)
		}
		// The models that served the run (X-38, v2.1 spec V-6 §3.4).
		fb := llmrun.Fallback(deps.LLM, res.Metadata.Model, res.Metadata.LLMCalls)
		if fb.Model != "" {
			res.Metadata.Model = fb.Model
		}
		res.Metadata.ModelsUsed, res.Metadata.FallbackCalls = fb.ModelsUsed, fb.FallbackCalls
		res.Notes = append(res.Notes, fb.Notes...)
	}
	return res, err
}

// run is Run without the rate-limit report.
func run(ctx context.Context, deps Deps, args Args) (*Result, error) {
	if deps.Config == nil || deps.ConfigErr != nil {
		return nil, ErrConfigInvalid
	}
	if deps.Resolver == nil || deps.LLM == nil {
		return nil, errNoWiring
	}
	args, err := args.Validated(deps.Config)
	if err != nil {
		return nil, err
	}
	pl, err := Prepare(ctx, deps, args)
	if err != nil {
		return nil, err
	}
	// From here the run uses the configuration merged with the repository
	// config and the arguments completed from it (X-40).
	deps.Config, args = pl.cfg, pl.args
	if args.Findings != nil {
		// A merged publish (X-33): the client's findings take the place of
		// the model's answer, and no model is called.
		return pl.publishSupplied(ctx, deps, args), nil
	}
	if pl.Unchanged {
		// Nothing changed since the last review (X-36): no model call, and
		// nothing is posted or edited, even with publish.
		return pl.Result, nil
	}
	res := pl.Result
	if pl.Empty {
		// Nothing to review: no LLM call (P3 review, 3d DESIGN-QUESTION 4).
		// DESIGN-QUESTION: is this empty review published when publish is
		// set? — chose yes because step 13 applies to every returned
		// review and the comment then shows the coverage and the note.
		res.Review = &Review{KeyIssuesToReview: []KeyIssue{}}
		pl.withCarried(res.Review)
		res.Notes = append(res.Notes, NoteNoReviewableChanges)
		publish(ctx, deps, args, pl)
		return res, nil
	}
	return pl.finish(ctx, deps, args)
}

// Prepare runs steps 1 to 6 of the pipeline: configuration, resolution,
// diff fetch, description, token measurement, diff preparation and the
// request-size guard. It sends nothing to the model and needs no
// Deps.LLM.
func Prepare(ctx context.Context, deps Deps, args Args) (*Plan, error) {
	// Step 1: config.
	if deps.Config == nil || deps.ConfigErr != nil {
		return nil, ErrConfigInvalid
	}
	if deps.Resolver == nil {
		return nil, errNoWiring
	}
	cfg := deps.Config
	log := deps.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// Step 2: resolve and read the PR.
	progress(deps, StageFetching)
	ref, p, err := deps.Resolver.Resolve(args.PRURL)
	if err != nil {
		return nil, err
	}
	// The context window comes from llm.context_window or, when unset, from
	// the endpoint (X-15). The probe runs before any provider request, so a
	// failing probe sends nothing to the provider.
	window, _, err := llmrun.ContextWindow(ctx, cfg, deps.LLM)
	if err != nil {
		return nil, err
	}
	pr, err := p.GetPullRequest(ctx, ref)
	if err != nil {
		return nil, err
	}

	// The repository config (X-40) is read once, at the base revision, now
	// that the pull request is known and before anything depends on the
	// settings: the run continues on the merged copy of the configuration,
	// and the options the call left unset take their defaults from it.
	rc := repocfg.ForRun(ctx, log, cfg, p, ref, pr)
	cfg = rc.Config
	deps.Config = cfg
	args = args.WithConfigDefaults(cfg)
	toggles := TogglesFrom(cfg.Review)
	factor := cfg.LLM.TokenEstimateFactor
	maxFindings := cfg.Review.MaxFindings
	if args.MaxFindings > 0 {
		maxFindings = args.MaxFindings
	}
	maxChunks := args.MaxChunks
	if maxChunks <= 0 {
		maxChunks = cfg.Review.MaxChunks
	}
	extra := cfg.Review.ExtraInstructions
	if args.ExtraInstructions != "" {
		extra = args.ExtraInstructions
	}
	language := cfg.Output.Language
	if args.OutputLanguage != "" {
		language = args.OutputLanguage
	}
	flt, err := filter.New(cfg)
	if err != nil {
		return nil, err
	}
	d, err := p.GetDiff(ctx, ref, pr, provider.DiffOptions{Include: flt.Include})
	if err != nil {
		return nil, err
	}

	// The plan is built here so that the incremental lookup below shares the
	// run's one read of the threads and of the token's user with the
	// discussion and the publish.
	pl := &Plan{ref: ref, p: p, pr: pr, d: d, toggles: toggles, maxFindings: maxFindings, log: log, flt: flt,
		maxTotal: config.EffectiveMaxTotalFindings(cfg.Review, maxFindings), cfg: cfg, args: args, repo: rc}
	supplied := args.Findings != nil

	// An incremental review (X-36) continues from the commit the last
	// review recorded in review-mcp's own overview. Without one the review
	// is full. When it is the current head, nothing changed: the run ends
	// here, before anything is fetched for the prompt.
	inc := fullIncremental(d)
	var incNotes []string
	last := ""
	if on, explicit := incrementalWanted(cfg, args); on {
		last, incNotes = pl.lastReviewed(ctx, explicit)
		if last != "" && last == ValidReviewedSHA(pr.HeadSHA) {
			all := incremental.Result{Files: make([]incremental.File, len(d.Files))}
			for i, f := range d.Files {
				all.Files[i] = incremental.File{Path: f.Path, OldPath: f.OldPath, State: incremental.Unchanged}
			}
			rd, _ := reduceDiff(d, all)
			inc = &Incremental{Mode: IncrementalUnchanged, Since: last, UnchangedFiles: len(d.Files)}
			return pl.unchangedPlan(ctx, deps, args, window, rd, inc, noteNothingChanged(last)), nil
		}
	}

	// Repository context (X-22): the session is opened here and the head is
	// fetched now (its own progress stage) when the diff defines a symbol to
	// look for. With review.max_chunks > 1 the block's budget is reserved up
	// front, as for the parts of a review in parts: files the reservation
	// pushes out go to a further part, so nothing is lost. With max_chunks
	// 1 a reservation would leave files unreviewed, so the diff always wins
	// there: the block uses only the room the diff leaves (placeRepo).
	// A merged publish (Args.Findings, X-33) sends no prompt, so nothing
	// that only feeds the prompt is fetched: no repository context and no
	// project context.
	var rcs *repoctx.Session
	fetched := false
	if cfg.Context.Repo.Enabled && !supplied {
		rcs = repoctx.Open(cfg, deps.RepoContext, ref, p, pr.HeadSHA, d.Files, d.Skipped, flt, log)
		if len(rcs.Symbols(d.Files)) > 0 {
			progress(deps, StageRepoContext)
			fetched = rcs.Ready(ctx) == ""
		}
	}

	// The files that changed since the last review (WP-7c). From here on the
	// diff holds only them, each with its whole patch; the unchanged files
	// are skipped with their own reason, which the coverage lists and does
	// not count as lost. X-19 parts, X-29 duplicates, X-31 and X-32 work on
	// the reduced diff unchanged; the file tree lists the directories of the
	// changed files only (treeDiff).
	treeDiff := d
	if last != "" {
		r, err := pl.detectChanges(ctx, cfg, d, rcs, last)
		if err != nil {
			return nil, err
		}
		changed, unchanged := r.Counts()
		switch {
		case r.Full:
			incNotes = append(incNotes, r.Notes...)
		case changed == 0 && unchanged > 0:
			rd, _ := reduceDiff(d, r)
			inc = &Incremental{Mode: IncrementalUnchanged, Since: last, Strategy: r.Strategy, UnchangedFiles: unchanged}
			return pl.unchangedPlan(ctx, deps, args, window, rd, inc, noteNoFileChanged(last)), nil
		default:
			// A focused run (X-33) carries nothing (WP-7f DQ-4, overridden):
			// it never publishes, every focused run of the same head would
			// list the same carried findings (N copies in the client's merged
			// set), and SuppliedFinding has no carried_from, so a carried
			// finding passed back would become a fresh one (planned inline,
			// recorded with the wrong "from").
			if args.Focus == "" {
				pl.carried = pl.carryForward(d, r)
			}
			if n := len(pl.carried); n > 0 {
				incNotes = append(incNotes, noteCarried(n))
			}
			rd, paths := reduceDiff(d, r)
			inc = &Incremental{Mode: IncrementalChanged, Since: last, Strategy: r.Strategy, ChangedFiles: changed,
				UnchangedFiles: unchanged, CarriedFindings: len(pl.carried)}
			d, treeDiff = rd, withoutUnchanged(rd)
			pl.d = d
			rcs.Narrow(d.Files, paths)
		}
		log.Debug("review: changes since the last review", "mode", inc.Mode, "strategy", r.Strategy,
			"changed", changed, "unchanged", unchanged)
	}
	reserve := 0
	if fetched && maxChunks > 1 && len(rcs.Symbols(d.Files)) > 0 {
		reserve = rcs.Settings().MaxTokens
	}
	progress(deps, StagePreparingDiff)

	// The PR's discussion, for the prompt and for the duplicate check; a
	// failure never fails the review.
	maxDisc := DefaultMaxDiscussionTokens
	if args.MaxDiscussionTokens != nil {
		maxDisc = *args.MaxDiscussionTokens
	}
	disc, discNotes := pl.readDiscussion(ctx, maxDisc, args.Publish && inlineEnabled(deps, args), factor)

	// The head commit's CI results (X-31): a failed read is an unreadable
	// summary, never a failed review.
	var ci *CI
	ciBlock, ciLeft := "", false
	if cfg.Review.CIStatus {
		c := ReadCI(ctx, p, ref, pr.HeadSHA, log)
		ci = &c
		ciBlock, ciLeft = CIBlock(ci, cfg.Review.MaxCITokens, factor)
	}

	// The project's guidelines and the listing of the directories the change
	// touches (X-32), read at the base revision the diff used, so that the
	// pull request cannot rewrite the rules it is judged by. A failed read
	// is a status and a note, never a failed review.
	var proj *projctx.Context
	if supplied {
		proj = projctx.Read(ctx, nil, "", projctx.Settings{}, nil, nil, factor, log)
	} else {
		proj = projctx.ReadFor(ctx, cfg, p, ref, pr, treeDiff, rcs, log)
	}
	pl.proj = proj

	// Step 3: description.
	in := PromptInput{
		Toggles:           toggles,
		MaxFindings:       maxFindings,
		ExtraInstructions: extra,
		RepoInstructions:  projctx.RepoGuidance(cfg.RepoConfig.Path, cfg.Review.RepoInstructions),
		Language:          language,
		Title:             pr.Title,
		Branch:            pr.SourceBranch,
		Description:       tokens.ClipDescription(pr.Description, cfg.Diff.MaxDescriptionTokens, factor),
		Discussion:        disc.Block,
		CI:                ciBlock,
		Guidelines:        proj.Guidelines(),
		FileTree:          proj.Tree(),
		Focus:             args.Focus,
		Incremental:       inc.Mode == IncrementalChanged,
		Date:              prompt.Date(deps.Clock),
	}

	// Step 4: measure the scaffolding and check the budget.
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
	// When the scaffolding leaves no room for the diff, the optional blocks
	// give way in a fixed order: the file tree, then the guidelines (cut at
	// a line boundary by what is missing, until none is left), then the
	// discussion, then the CI results. The blocks of X-32 go first, so
	// that the discussion and the CI block keep the room they had before.
	// The blocks of X-32 give way again, in the same order, when the diff
	// prepared below does not fit beside them (step 5).
	remeasure := func() error {
		promptTokens, err = ScaffoldingTokens(in, factor)
		budget.PromptTokens = promptTokens
		return err
	}
	if budget.RequireCapacity() != nil && proj.DropTree() {
		in.FileTree = ""
		if err := remeasure(); err != nil {
			return nil, err
		}
	}
	for budget.RequireCapacity() != nil && proj.CutGuidelines(1-budget.SoftLimit()) {
		in.Guidelines = proj.Guidelines()
		if err := remeasure(); err != nil {
			return nil, err
		}
	}
	if budget.RequireCapacity() != nil && in.Discussion != "" {
		// The discussion is optional: a context window too small for it
		// still reviews the diff, without it.
		disc = discussion{Omitted: disc.Included + disc.Omitted}
		in.Discussion = ""
		if promptTokens, err = ScaffoldingTokens(in, factor); err != nil {
			return nil, err
		}
		budget.PromptTokens = promptTokens
	}
	if budget.RequireCapacity() != nil && in.CI != "" {
		// The CI block is optional too, and goes second: the discussion is
		// the larger of the two.
		in.CI, ciLeft = "", true
		if promptTokens, err = ScaffoldingTokens(in, factor); err != nil {
			return nil, err
		}
		budget.PromptTokens = promptTokens
	}
	if err := budget.RequireCapacity(); err != nil {
		return nil, doesNotFit(err)
	}

	// Step 5: prepare the numbered diff, against the budget without the
	// repository-context reservation (dIn) or with it (reserved). The
	// reservation is dropped when it leaves no capacity or no diff.
	base := budget
	withReserve := func(r int) tokens.Budget {
		b := base
		b.PromptTokens += r
		return b
	}
	if reserve > 0 && withReserve(reserve).RequireCapacity() != nil {
		reserve = 0
	}
	dIn := diffpipe.Input{Files: d.Files, Skipped: d.Skipped, Incomplete: d.Incomplete, Mode: diffpipe.ModeNumbered, Budget: base, Diff: cfg.Diff}
	prepare := func(r int) (*diffpipe.Prepared, error) {
		in := dIn
		in.Budget = withReserve(r)
		return diffpipe.Prepare(in)
	}
	prep, err := prepare(reserve)
	if reserve > 0 && (errors.Is(err, tokens.ErrDoesNotFit) || (err == nil && prep.Text == "")) {
		reserve = 0
		prep, err = prepare(0)
	}
	// A diff that does not fit beside the blocks of X-32 (it cannot be
	// prepared, a file is clipped, or a review in one call leaves files
	// out) makes them give way in the same order: the file tree, then the
	// guidelines, halved until the diff fits. Only when the diff would fit
	// without them, so that they never go for nothing.
	oneCall := maxChunks <= 1
	if projctx.DiffDoesNotFit(prep, err, oneCall) && proj.HasBlocks() {
		bare := in
		bare.Guidelines, bare.FileTree = "", ""
		bareTokens, serr := ScaffoldingTokens(bare, factor)
		if serr != nil {
			return nil, serr
		}
		probe := dIn
		probe.Budget.PromptTokens = bareTokens
		if bp, berr := diffpipe.Prepare(probe); !projctx.DiffDoesNotFit(bp, berr, oneCall) {
			for projctx.DiffDoesNotFit(prep, err, oneCall) && proj.Shrink() {
				in.Guidelines, in.FileTree = proj.Guidelines(), proj.Tree()
				if serr := remeasure(); serr != nil {
					return nil, serr
				}
				base, dIn.Budget = budget, budget
				prep, err = prepare(reserve)
			}
		}
	}
	if err != nil {
		if errors.Is(err, tokens.ErrDoesNotFit) {
			return nil, doesNotFit(err)
		}
		return nil, err
	}
	budget = withReserve(reserve)
	res := &Result{
		PR: PRInfo{Kind: string(ref.Kind), URL: logging.RedactURL(ref.URL), Number: ref.Number, Title: pr.Title,
			HeadSHA: pr.HeadSHA},
		Reviewed:      ReviewedCommit{SHA: ValidReviewedSHA(pr.HeadSHA), Since: inc.Since},
		Coverage:      buildCoverage(prep, flt),
		Notes:         []string{},
		EnabledFields: []string{},
		Metadata: Metadata{
			Model: cfg.LLM.Model, ContextWindow: window, PromptTokens: promptTokens,
			DiffTokens: prep.Tokens, FastPath: prep.FastPath, ReviewedAt: reviewedAt(deps.Clock),
			// The threads shown to the model: the review cannot know which
			// of its findings the discussion made it drop.
			AlreadyDiscussed: disc.Included,
		},
		Focus:       args.Focus,
		Incremental: inc,
		RepoConfig:  rc.Result,
	}
	// The notes of the repository config come first (X-40).
	res.Notes = append(res.Notes, rc.Notes...)
	// Changed files the provider could not list at all (GitHub lists at
	// most 3000) are named by its fixed note.
	res.Notes = append(res.Notes, d.Notes...)
	res.Notes = append(res.Notes, incNotes...)
	res.Notes = append(res.Notes, discNotes...)
	if disc.Omitted > 0 {
		res.Notes = append(res.Notes, noteDiscussionLeftOut(disc.Omitted))
	}
	res.CI = ci
	if ciLeft {
		res.Notes = append(res.Notes, NoteCILeftOut)
	}
	res.Notes = append(res.Notes, proj.Notes()...)
	res.Coverage.Guidelines, res.Coverage.FileTree = proj.Coverage()
	for _, f := range enabledFields(toggles) {
		res.EnabledFields = append(res.EnabledFields, f.key)
	}
	log.Debug("review: diff prepared", "url", logging.RedactURL(ref.URL), "files", len(d.Files),
		"provider_skipped", len(d.Skipped), "included", len(prep.Included), "clipped", len(prep.Clipped),
		"fast_path", prep.FastPath, "prompt_tokens", promptTokens, "diff_tokens", prep.Tokens,
		"discussion_threads", disc.Included, "discussion_omitted", disc.Omitted)

	pl.Result, pl.Budget = res, budget
	if prep.Text == "" {
		pl.Empty = true
		if rcs != nil {
			// No model call, so no context: skipped, not off ("off" means
			// only "disabled").
			res.Coverage.RepoContext = llmrun.RepoContext{Status: llmrun.RepoSkipped, Reason: repoctx.ReasonNothingToReview}
		}
		res.Notes = append(res.Notes, llmrun.PartialNotes(&res.Coverage, budget)...)
		return pl, nil
	}

	// A diff that leaves files out is reviewed in parts when
	// review.max_chunks allows it (X-19); otherwise, and when the packing
	// yields one part, the review is the one call below, unchanged. When the
	// reservation made the diff leave files out and no plan in parts
	// results, the diff is prepared again without it: the reservation must
	// never cost a file.
	if maxChunks > 1 && leavesFilesOut(prep) {
		ok, err := pl.planParts(ctx, dIn, in, maxChunks, rcs, reserve)
		if err != nil {
			return nil, err
		}
		if ok {
			return pl, nil
		}
		if reserve > 0 {
			if prep, err = prepare(0); err != nil {
				if errors.Is(err, tokens.ErrDoesNotFit) {
					return nil, doesNotFit(err)
				}
				return nil, err
			}
			budget = base
			pl.Budget = budget
			res.Coverage, res.Metadata.DiffTokens, res.Metadata.FastPath = buildCoverage(prep, flt), prep.Tokens, prep.FastPath
			res.Coverage.Guidelines, res.Coverage.FileTree = proj.Coverage()
			if leavesFilesOut(prep) {
				if ok, err = pl.planParts(ctx, dIn, in, maxChunks, rcs, 0); err != nil {
					return nil, err
				} else if ok {
					return pl, nil
				}
			}
		}
	}

	// Step 6: render the final prompts behind the request-size guard.
	fit, err := fitPrompts(in, prep.Text, budget)
	if err != nil {
		return nil, err
	}
	var repoNotes []string
	if rcs != nil {
		// The block goes into the room the diff leaves (placeRepo); the
		// prompt tokens then include it.
		pin, pfit, o := placeRepo(ctx, rcs, in, diffFiles(d.Files, prep.Included, prep.Clipped), nil, prep.Text, fit, budget)
		fit = pfit
		if pin.RepoContext != "" {
			if res.Metadata.PromptTokens, err = ScaffoldingTokens(pin, factor); err != nil {
				return nil, err
			}
		}
		res.Coverage.RepoContext, repoNotes = repoctx.Summarize([]repoctx.Outcome{o})
		pl.RepoReport = repoctx.ReportOf([]repoctx.Outcome{o})
		log.Debug("review: repository context", "status", res.Coverage.RepoContext.Status,
			"reason", res.Coverage.RepoContext.Reason, "symbols", o.Symbols, "references", o.References,
			"files", o.Files, "omitted", o.Omitted)
	}
	pl.parts = []*part{{prep: prep, fit: fit}}
	pl.Prompts = fit.prompts
	res.Metadata.RequestTokens = fit.requestTokens
	res.Coverage.ModelCalls = 1
	if fit.keptLines >= 0 {
		types := map[string]provider.ChangeType{}
		for _, f := range d.Files {
			types[f.Path] = f.Type
		}
		trimCoverage(&res.Coverage, prep.Text, fit.keptLines, types)
		res.Metadata.DiffTrimmed = true
		res.Metadata.DiffTokens = tokens.Estimate(fit.diff, factor)
		res.Notes = append(res.Notes, NoteDiffTrimmed)
		log.Debug("review: diff trimmed by the request-size guard", "kept_lines", fit.keptLines,
			"request_tokens", fit.requestTokens)
	}
	if n := len(res.Coverage.Clipped); n > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(noteClippedFormat, countPhrase(n, "file was", "files were")))
	}
	res.Notes = append(res.Notes, llmrun.PartialNotes(&res.Coverage, budget)...)
	res.Notes = append(res.Notes, repoNotes...)
	return pl, nil
}

// finish runs steps 7 to 13 of a non-empty plan: one model call per part,
// the merge of the parts' answers (X-19), snippets, links and the publish.
func (pl *Plan) finish(ctx context.Context, deps Deps, args Args) (*Result, error) {
	res, log := pl.Result, pl.log
	n := len(pl.parts)

	// Steps 7 and 8, per part and sequentially: call the model, parse,
	// re-ask once on a parse failure.
	answers := make([]*partAnswer, n)
	classes := make([]string, n)
	var firstErr error
	failed := 0
	for i, pt := range pl.parts {
		if n == 1 {
			progress(deps, StageCallingModel)
		} else {
			progress(deps, CallingModelPart(i+1, n))
		}
		a, err := pl.callPart(ctx, deps, pt.fit, i+1, n)
		res.Metadata.LLMCalls += a.calls
		if err != nil {
			if n == 1 || ctx.Err() != nil {
				// One call: its error is the run's, as before. A cancelled
				// run stops at once.
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			failed++
			classes[i] = failureClass(err)
			log.Debug("review: part failed", "part", i+1, "parts", n, "class", classes[i])
			continue
		}
		answers[i] = a
	}
	if failed == n {
		// Every part failed: the first part's classified error, as for a
		// review in one call.
		return nil, firstErr
	}

	var rev *Review
	if n == 1 {
		a := answers[0]
		rev = a.rev
		res.Metadata.Truncated, res.Metadata.RepairTactic, res.Metadata.Reasked = a.truncated, a.tactic, a.reasked
		if a.truncated {
			res.Notes = append(res.Notes, NoteTruncated)
		}
		if a.reasked {
			res.Notes = append(res.Notes, NoteReasked)
		}
		res.Notes = append(res.Notes, a.conv.Notes...)
	} else {
		rev = pl.mergeParts(answers, classes)
	}

	// Steps 10 and 11: snippets and links, on the merged list against every
	// file of the PR.
	pl.attachSnippets(rev)
	pl.withCarried(rev)
	res.Review = rev
	log.Debug("review: done", "findings", len(rev.KeyIssuesToReview),
		"llm_calls", res.Metadata.LLMCalls, "reasked", res.Metadata.Reasked, "parts", n, "failed_parts", failed)

	// Step 13: publish.
	publish(ctx, deps, args, pl)
	return res, nil
}

// partAnswer is the converted answer of one part.
type partAnswer struct {
	rev  *Review
	conv *Conversion
	// calls counts the completions (1, or 2 with the re-ask), also when the
	// part failed.
	calls              int
	reasked, truncated bool
	tactic             string
}

// callPart runs steps 7 and 8 for one part: the call, the YAML repair, the
// conversion and at most one re-ask. The returned answer is never nil; its
// calls count holds also on an error.
func (pl *Plan) callPart(ctx context.Context, deps Deps, fit *fitted, i, n int) (*partAnswer, error) {
	log := pl.log
	keys := RepairKeys(pl.toggles)
	a := &partAnswer{}
	var unusable error
	for attempt := range 3 {
		user := fit.prompts.User
		var resp *llm.Response
		var err error
		switch attempt {
		case 0, 1:
			if attempt == 1 {
				user = fit.reaskUser
				a.reasked = true
			}
			resp, err = deps.LLM.Complete(ctx, fit.prompts.System, user)
			a.calls++
		default:
			// Still unusable after the re-ask: the next model of the
			// chain gets the original prompt once (X-38).
			var ok bool
			resp, ok, err = llmrun.CompleteNext(ctx, deps.LLM, fit.prompts.System, user)
			if err == nil && !ok {
				return a, ErrUnparseable.WithCause(unusable)
			}
			a.calls++
		}
		if err != nil {
			return a, err
		}
		// Truncated describes the answer that is converted: a cut-off first
		// answer followed by a complete re-ask is not truncated.
		a.truncated = resp.Truncated
		data, trace := yamlrepair.Load(strings.TrimSpace(resp.Content), keys)
		a.tactic = trace.Tactic
		log.Debug("review: answer loaded", "part", i, "parts", n, "attempt", attempt+1, "repair_tactic", trace.Tactic,
			"truncated", resp.Truncated, "prompt_tokens_reported", resp.Usage.PromptTokens,
			"completion_tokens_reported", resp.Usage.CompletionTokens)
		// Step 9: validate and convert.
		a.rev, a.conv, err = Convert(data, pl.toggles, pl.maxFindings)
		if err == nil {
			break
		}
		unusable = err
		if attempt == 2 {
			return a, ErrUnparseable.WithCause(err)
		}
	}
	for _, w := range a.conv.Warnings {
		log.Debug("review: field warning", "part", i, "warning", w)
	}
	return a, nil
}
