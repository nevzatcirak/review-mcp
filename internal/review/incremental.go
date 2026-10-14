package review

import (
	"context"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/logging"
	"github.com/nevzatcirak/review-mcp/internal/projctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/review/incremental"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// The incremental review (X-36, V-1; spec v2.1 §4, WP-7d): pr_review reviews
// only the files whose content changed since the last review review-mcp
// published on the pull request. The commit that review recorded is read
// from review-mcp's own overview only (findOverview: marker and author),
// internal/review/incremental decides which files changed, and the diff the
// prompt pipeline gets holds the changed files only, each with its whole
// pull request patch. A doubt is always a full review or a changed file,
// never a skipped one.

// Values of Incremental.Mode.
const (
	// IncrementalFull: the whole pull request was reviewed (the run was
	// not incremental, no earlier review was found, or no comparison could
	// be used).
	IncrementalFull = "full"
	// IncrementalChanged: only the files that changed since the last
	// review were reviewed.
	IncrementalChanged = "incremental"
	// IncrementalUnchanged: nothing changed since the last review, so
	// nothing was reviewed: no model call and no publish.
	IncrementalUnchanged = "unchanged"
)

// IncrementalSentence is the one sentence the system prompt of an
// incremental run carries (spec §4 item 3, verbatim).
const IncrementalSentence = "This is an incremental review: only the files that changed since the last review are shown; " +
	"findings already posted on the pull request were not repeated."

// NoteNoEarlierReview: incremental=true was passed, but no overview of
// review-mcp with a reviewed commit is on the pull request (spec wording).
const NoteNoEarlierReview = "No earlier review by review-mcp was found on this pull request, so the whole pull request was reviewed."

// noteNothingChanged: the last review recorded the current head (spec
// wording); sha is that commit.
func noteNothingChanged(sha string) string {
	return "Nothing changed since the last review (commit `" + shortCommit(sha) + "`); nothing was reviewed."
}

// noteNoFileChanged: every listed file has the content it had at the last
// review's commit sha (spec wording).
func noteNoFileChanged(sha string) string {
	return "No file of the pull request changed since the last review (commit `" + shortCommit(sha) + "`); nothing was reviewed."
}

// noteCarried: findings of earlier reviews on files that did not change
// since were carried forward without a model call (spec v2.1 §7.3 item 4).
func noteCarried(n int) string {
	return countPhrase(n, "finding of earlier reviews on unchanged files is carried forward; it was",
		"findings of earlier reviews on unchanged files are carried forward; they were") + " not reviewed again."
}

// shortCommit is the 7-character form of a full commit id.
func shortCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Incremental is the incremental part of a pr_review result (X-36). The
// tool always sets it.
type Incremental struct {
	// Mode is IncrementalFull, IncrementalChanged or IncrementalUnchanged.
	Mode string `json:"mode"`
	// Since is the commit of the last review the run continued from; empty
	// for a full review.
	Since string `json:"since,omitempty"`
	// Strategy is how the changed files were found
	// (incremental.StrategyGit or incremental.StrategyAPI); empty when no
	// comparison was used.
	Strategy string `json:"strategy"`
	// ChangedFiles counts the listed files of the diff the run treats as
	// changed: all of them in a full review. UnchangedFiles counts those it
	// skipped as unchanged.
	ChangedFiles   int `json:"changed_files"`
	UnchangedFiles int `json:"unchanged_files"`
	// CarriedFindings counts the findings of earlier reviews on unchanged
	// files that the run carried forward (spec v2.1 §7.3; KeyIssue.
	// CarriedFrom); 0 outside mode IncrementalChanged.
	CarriedFindings int `json:"carried_findings"`
}

// fullIncremental is the record of a full review of d.
func fullIncremental(d *provider.Diff) *Incremental {
	return &Incremental{Mode: IncrementalFull, ChangedFiles: len(d.Files)}
}

// incrementalWanted reports whether the run is to be incremental, and
// whether that was asked for explicitly (the argument true): the argument
// replaces review.incremental for the call. A merged publish (X-33) never
// is: it makes no model call.
func incrementalWanted(cfg *config.Config, args Args) (on, explicit bool) {
	if args.Findings != nil {
		return false, false
	}
	if args.Incremental != nil {
		return *args.Incremental, *args.Incremental
	}
	return cfg.Review.Incremental, false
}

// lastReviewed returns the commit the last review recorded in review-mcp's
// own overview (findOverview reads it from that comment only, after the
// author check), or "" with the note of a full review. It uses the run's
// one read of the threads and of the token's user, which the discussion and
// the publish share, so it costs no request of its own.
//
// DESIGN-QUESTION (WP-7d DQ-2): the spec does not say what a failed lookup
// (the threads or the token's user cannot be read) does. A doubt is a full
// review, and a failure is not the ordinary "first review" case, so it is
// reported with the WP-7c sentence incremental.NoteNotCompared whether the
// run was incremental by the argument or by the configuration. The note of
// a missing earlier review, by contrast, is added only for the argument
// (spec). Recommendation: keep.
func (pl *Plan) lastReviewed(ctx context.Context, explicit bool) (sha string, notes []string) {
	found, err := pl.findOverview(ctx)
	switch {
	case err != nil:
		pl.log.Debug("review: incremental lookup failed, reviewing in full", "error", fixedError(err))
		return "", []string{incremental.NoteNotCompared}
	case found == nil || found.reviewed == "":
		if explicit {
			return "", []string{NoteNoEarlierReview}
		}
		return "", nil
	}
	pl.held = found.held
	return found.reviewed, nil
}

// carryForward returns the findings of the last review's held lines
// (pl.held) that an incremental review carries forward (spec v2.1 §7.3):
// those whose file is a listed file of d that r found unchanged since the
// last review and that is not deleted. A record of a changed file (reviewed
// again: the new review replaces it), of a file no longer in the pull
// request or of a deleted file is dropped. Each carried finding links to its
// file line at the head, whose content is that of the review that produced
// it, and has no snippet; the model never sees it.
//
// DESIGN-QUESTION (WP-7f DQ-3): the spec does not say whether carried
// findings count against review.max_findings or the effective
// max_total_findings. They do not: they are not this run's findings, and a
// cap would drop findings of unchanged files again, the gap this package
// closes. The held lines' own 12,000-byte budget bounds them. The run's
// order is kept: they follow the new findings in the order of the earlier
// overview. Recommendation: keep.
func (pl *Plan) carryForward(d *provider.Diff, r incremental.Result) []KeyIssue {
	unchanged := map[string]bool{}
	for i, f := range r.Files {
		if f.State == incremental.Unchanged && i < len(d.Files) && d.Files[i].Type != provider.ChangeDeleted {
			unchanged[d.Files[i].Path] = true
		}
	}
	var out []KeyIssue
	for _, h := range pl.held {
		if !unchanged[h.F] {
			continue
		}
		out = append(out, KeyIssue{RelevantFile: h.F, StartLine: h.S, EndLine: h.E, IssueHeader: h.H,
			IssueContent: h.C, Link: pl.p.FileLineURL(pl.ref, pl.pr, h.F, h.S), CarriedFrom: h.From})
	}
	pl.log.Debug("review: held findings", "read", len(pl.held), "carried", len(out))
	return out
}

// withCarried appends the carried findings (carryForward) to rev, after the
// findings of this run.
func (pl *Plan) withCarried(rev *Review) {
	if len(pl.carried) > 0 {
		rev.KeyIssuesToReview = append(rev.KeyIssuesToReview, pl.carried...)
	}
}

// detectChanges runs the change detection of WP-7c on the listed files of d
// since last. With repository context on and the head already fetched by
// this run, the cache is offered for the git strategy; nothing is ever
// fetched for it.
//
// DESIGN-QUESTION (WP-7d DQ-1, the architect's decision on the cache): the
// detection runs after the repository-context step of the pipeline, which
// fetches the head, as before, when the whole diff defines a symbol to look
// for; an incremental run adds no fetch and moves none. The fetch is shallow
// (the head only), so git:diff is used when the persistent cache still
// holds the last reviewed commit from an earlier run's fetch (not verified
// on a real host); otherwise the provider compares. Detecting before that step would make git:diff
// unreachable (the session has fetched nothing yet), and fetching for the
// detection is ruled out. Recommendation: keep.
func (pl *Plan) detectChanges(ctx context.Context, cfg *config.Config, d *provider.Diff, rcs *repoctx.Session,
	last string) (incremental.Result, error) {
	in := incremental.Input{Ref: pl.ref, Files: d.Files, Head: pl.pr.HeadSHA, Last: last, Provider: pl.p,
		MaxFiles: cfg.Review.IncrementalMaxFiles}
	if cr, co, ok := rcs.Changes(); ok {
		in.Local, in.Checkout = cr, co
	}
	return incremental.Detect(ctx, in)
}

// reduceDiff returns the diff of an incremental review: the changed files of
// d, whole, and the skips of d followed by every unchanged file, skipped
// with reason llmrun.SkipUnchangedSinceReview (in the coverage, never in a
// prompt). It also returns the paths of the unchanged files.
func reduceDiff(d *provider.Diff, r incremental.Result) (*provider.Diff, []string) {
	out := *d
	out.Files = nil
	out.Skipped = append([]provider.SkippedFile(nil), d.Skipped...)
	var unchanged []string
	for i, f := range r.Files {
		if f.State == incremental.Unchanged {
			out.Skipped = append(out.Skipped, provider.SkippedFile{Path: f.Path, Reason: llmrun.SkipUnchangedSinceReview})
			unchanged = append(unchanged, f.Path)
			continue
		}
		out.Files = append(out.Files, d.Files[i])
	}
	return &out, unchanged
}

// withoutUnchanged is d without the skips of unchanged files: the files the
// change touches since the last review, for the file tree (X-32 on the
// reduced diff: the directories of the changed files only, as the
// provider's own skips still count there).
func withoutUnchanged(d *provider.Diff) *provider.Diff {
	out := *d
	out.Skipped = nil
	for _, s := range d.Skipped {
		if s.Reason != llmrun.SkipUnchangedSinceReview {
			out.Skipped = append(out.Skipped, s)
		}
	}
	return &out
}

// unchangedPlan finishes Prepare for a run that reviews nothing because
// nothing changed since the last review: no prompt, no model call and no
// publish (Run). The coverage lists every listed file as unchanged, next to
// the provider's skips; no repository or project context is read (nothing
// is sent), and the CI results are read as for any review, for the result.
//
// DESIGN-QUESTION (WP-7d DQ-4): enabled_fields is empty and the review holds
// no findings, so no rendering states "No major issues detected" or "No
// security concerns" about a run that reviewed nothing; the note says why.
// The empty plan of a fully filtered diff keeps its fields as before.
// Recommendation: keep.
func (pl *Plan) unchangedPlan(ctx context.Context, deps Deps, args Args, window int, d *provider.Diff,
	inc *Incremental, note string) *Plan {
	cfg := deps.Config
	res := &Result{
		PR: PRInfo{Kind: string(pl.ref.Kind), URL: logging.RedactURL(pl.ref.URL), Number: pl.ref.Number,
			Title: pl.pr.Title, HeadSHA: pl.pr.HeadSHA},
		Review:        &Review{KeyIssuesToReview: []KeyIssue{}},
		Coverage:      buildCoverage(&diffpipe.Prepared{Skipped: d.Skipped, Incomplete: d.Incomplete}, pl.flt),
		Notes:         []string{},
		EnabledFields: []string{},
		Metadata:      Metadata{Model: cfg.LLM.Model, ContextWindow: window, ReviewedAt: reviewedAt(deps.Clock)},
		Focus:         args.Focus,
		Incremental:   inc,
	}
	if cfg.Context.Repo.Enabled {
		res.Coverage.RepoContext = llmrun.RepoContext{Status: llmrun.RepoSkipped, Reason: repoctx.ReasonNothingToReview}
	}
	pl.proj = projctx.Read(ctx, nil, "", projctx.Settings{}, nil, nil, cfg.LLM.TokenEstimateFactor, pl.log)
	res.Coverage.Guidelines, res.Coverage.FileTree = pl.proj.Coverage()
	if cfg.Review.CIStatus {
		c := ReadCI(ctx, pl.p, pl.ref, pl.pr.HeadSHA, pl.log)
		res.CI = &c
	}
	res.Notes = append(res.Notes, d.Notes...)
	res.Notes = append(res.Notes, note)
	res.Notes = append(res.Notes, llmrun.PartialNotes(&res.Coverage, tokens.Budget{})...)
	pl.d = d
	pl.Result, pl.Empty, pl.Unchanged = res, true, true
	pl.log.Debug("review: nothing changed since the last review", "url", logging.RedactURL(pl.ref.URL),
		"strategy", inc.Strategy, "unchanged", inc.UnchangedFiles)
	return pl
}
