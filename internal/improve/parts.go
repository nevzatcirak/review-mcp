package improve

import (
	"context"
	"errors"
	"fmt"

	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// diffPromptTokens is the PromptTokens of the diff budget for the
// suggestion prompts of in: the larger of
//
//   - the suggestion scaffolding (in with an empty diff) plus repoReserve,
//     the repository-context reservation of a run in parts, and
//   - the self-review scaffolding (empty diff, no suggestion) plus the
//     budget's hard output reserve, the room for the suggestions the
//     self-review request repeats.
//
// The self-review call is sent the same diff as its suggestion call, so the
// diff must fit both requests. Its suggestion list is at most the
// suggestion call's answer, which the hard reserve (llm.max_output_tokens,
// at least 1000) bounds.
//
// DESIGN-QUESTION: how is the self-review request budgeted? — chose to
// reserve its scaffolding and the hard output reserve in the diff budget
// (the larger of the two scaffoldings wins), so that a part packed to the
// limit still leaves room to score it; a self-review request that does not
// fit all the same is not sent and counts as a failed self-review (its
// suggestions are kept unscored). The cost is up to about a thousand
// tokens of diff per call when the self-review side is the larger.
func diffPromptTokens(in PromptInput, b tokens.Budget, repoReserve int) (int, error) {
	s, err := ScaffoldingTokens(in, b.Factor)
	if err != nil {
		return 0, err
	}
	r, err := reflectScaffoldingTokens(in.Language, b.Factor)
	if err != nil {
		return 0, err
	}
	return max(s+repoReserve, r+b.HardReserve()), nil
}

// planParts tries to plan the run in parts (X-19), exactly as pr_review
// plans a review in parts: the scaffolding of the prompts is measured with
// PartHeader(maxChunks, maxChunks), the longest part line, plus the
// repository-context reservation, and diffpipe.PrepareChunks packs the
// files into at most maxChunks parts against that budget. It reports false,
// leaving pl as it was, when the run stays one call: the part scaffolding
// leaves no diff budget, or the packing yields one part.
//
// With repository context (rcs non-nil), each part searches the symbols of
// its own files only and marks a use in another file of the pull request
// with the part that reviews it, as pr_review does (repoctx.Scope).
func (pl *Plan) planParts(ctx context.Context, dIn diffpipe.Input, in PromptInput, maxChunks int,
	rcs *repoctx.Session, reserve int, types map[string]provider.ChangeType) (bool, error) {
	res, log := pl.Result, pl.log
	factor := dIn.Budget.Factor
	hdr := in
	hdr.PartHeader = PartHeader(maxChunks, maxChunks)
	budget := dIn.Budget
	promptTokens, err := diffPromptTokens(hdr, budget, reserve)
	if err != nil {
		return false, err
	}
	budget.PromptTokens = promptTokens
	if budget.RequireCapacity() != nil {
		return false, nil
	}
	dIn.Budget = budget
	ch, err := diffpipe.PrepareChunks(dIn, maxChunks)
	if errors.Is(err, tokens.ErrDoesNotFit) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(ch.Parts) < 2 {
		return false, nil
	}

	n := len(ch.Parts)
	parts := make([]*part, n)
	diffTokens, requestTokens, trimmed := 0, 0, false
	var repos []repoctx.Outcome
	marks := partMarks(dIn, ch)
	for i, prep := range ch.Parts {
		pin := in
		pin.PartHeader = PartHeader(i+1, n)
		fit, err := fitPrompts(pin, prep.Text, budget)
		if err != nil {
			return false, err
		}
		if rcs != nil {
			var o repoctx.Outcome
			own := diffFiles(dIn.Files, append(append([]string(nil), prep.Included...), prep.DeletedListed...), prep.Clipped)
			sc := &repoctx.Scope{Exclude: gitctxPaths(own), Marks: marks}
			_, fit, o = placeRepo(ctx, rcs, pin, diffFiles(dIn.Files, prep.Included, prep.Clipped), sc, prep.Text, fit, budget)
			repos = append(repos, o)
			log.Debug("improve: repository context", "part", i+1, "status", o.Status, "reason", o.Reason,
				"symbols", o.Symbols, "references", o.References, "files", o.Files, "omitted", o.Omitted)
		}
		pt := &part{prep: prep, fit: fit, cov: Coverage{
			Included:      llmrun.NonNil(append([]string(nil), prep.Included...)),
			Clipped:       llmrun.NonNil(append([]string(nil), prep.Clipped...)),
			DeletedListed: llmrun.NonNil(append([]string(nil), prep.DeletedListed...)),
			Omitted:       llmrun.OmittedFiles{Added: []string{}, Modified: []string{}, Deleted: []string{}},
		}}
		tokensOfPart := prep.Tokens
		if fit.keptLines >= 0 {
			llmrun.TrimCoverage(&pt.cov, prep.Text, fit.keptLines, types)
			tokensOfPart = tokens.Estimate(fit.diff, factor)
			trimmed = true
			log.Debug("improve: diff trimmed by the request-size guard", "part", i+1, "kept_lines", fit.keptLines,
				"request_tokens", fit.requestTokens)
		}
		diffTokens += tokensOfPart
		requestTokens = max(requestTokens, fit.requestTokens)
		parts[i] = pt
	}

	pl.parts, pl.chunks, pl.Budget = parts, ch, budget
	var repoNotes []string
	pl.repoCtx, repoNotes = repoctx.Summarize(repos)
	pl.RepoReport = repoctx.ReportOf(repos)
	pl.Prompts = parts[0].fit.prompts
	res.Coverage = pl.partsCoverage(nil)
	m := &res.Metadata
	// As for pr_review in parts: prompt_tokens is the scaffolding with the
	// part line, diff_tokens the sum over the parts, request_tokens the
	// largest suggestion request, fast_path false.
	m.PromptTokens, m.DiffTokens, m.RequestTokens, m.FastPath = promptTokens, diffTokens, requestTokens, false
	if trimmed {
		m.DiffTrimmed = true
		res.Notes = append(res.Notes, NoteDiffTrimmed)
	}
	if c := len(res.Coverage.Clipped); c > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(llmrun.NoteClippedFormat, llmrun.CountPhrase(c, "file was", "files were")))
	}
	reached := n == maxChunks && llmrun.LeavesFilesOut(&diffpipe.Prepared{Omitted: ch.Omitted})
	res.Notes = append(res.Notes, llmrun.ChunkedPartialNotes(&res.Coverage, budget, reached)...)
	res.Notes = append(res.Notes, repoNotes...)
	log.Debug("improve: diff prepared in parts", "parts", n, "max_chunks", maxChunks, "too_large", len(ch.TooLarge),
		"left_out", len(ch.Omitted.Added)+len(ch.Omitted.Modified)+len(ch.Omitted.Deleted),
		"prompt_tokens", promptTokens, "diff_tokens", diffTokens)
	return true, nil
}

// partsCoverage is the coverage of a run in parts (llmrun.PartsCoverage):
// the parts' files in part order, then the files no part reviewed. failed
// marks the parts whose suggestion call failed (nil: none). The repository
// context summed over the parts is added.
func (pl *Plan) partsCoverage(failed []bool) Coverage {
	covs := make([]Coverage, len(pl.parts))
	for i, pt := range pl.parts {
		covs[i] = pt.cov
	}
	c := llmrun.PartsCoverage(pl.chunks, covs, failed, pl.flt)
	if pl.repoCtx.Status != "" {
		c.RepoContext = pl.repoCtx
	}
	return c
}

// partMarks gives, for every file of the pull request a part's search may
// meet, the text after the symbol of a use in it (repoctx.Scope.Marks), as
// pr_review's: the part that reviews the file, else "not reviewed".
func partMarks(dIn diffpipe.Input, ch *diffpipe.Chunks) map[string]string {
	marks := map[string]string{}
	for _, f := range dIn.Skipped {
		marks[f.Path] = repoctx.MarkNotReviewed
	}
	for i := range dIn.Files {
		marks[dIn.Files[i].Path] = repoctx.MarkNotReviewed
	}
	for j, prep := range ch.Parts {
		for _, l := range [][]string{prep.Included, prep.Clipped, prep.DeletedListed} {
			for _, f := range l {
				marks[f] = repoctx.MarkReviewedInPart(j + 1)
			}
		}
	}
	return marks
}

// fitted is the outcome of the request-size guard.
type fitted struct {
	prompts Prompts
	// reaskUser is the user prompt of the re-ask.
	reaskUser string
	// requestTokens is tokens.RequestTokens of prompts.
	requestTokens int
	// diff is the diff in the prompts; keptLines is its line count when the
	// guard trimmed it, -1 otherwise. The part's self-review is sent this
	// diff.
	diff      string
	keptLines int
}

// fitPrompts renders the final suggestion prompts and applies the
// request-size guard (llmrun.Fit), checking the re-ask request, as
// pr_review does.
func fitPrompts(in PromptInput, diff string, b tokens.Budget) (*fitted, error) {
	f, err := llmrun.Fit(diff, b, func(d string) (llmrun.Rendered, error) {
		in.Diff = d
		p, err := RenderPrompts(in)
		if err != nil {
			return llmrun.Rendered{}, err
		}
		ru, err := withReaskNote(p.User)
		if err != nil {
			return llmrun.Rendered{}, err
		}
		return llmrun.Rendered{System: p.System, User: p.User, GuardUser: ru}, nil
	})
	if err != nil {
		return nil, err
	}
	return &fitted{
		prompts:       Prompts{System: f.Rendered.System, User: f.Rendered.User},
		reaskUser:     f.Rendered.GuardUser,
		requestTokens: f.RequestTokens,
		diff:          f.Diff,
		keptLines:     f.KeptLines,
	}, nil
}
