package improve

import (
	"context"
	"errors"
	"slices"

	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
	"github.com/nevzatcirak/review-mcp/internal/yamlrepair"
)

// errReflectDoesNotFit is the cause of a self-review request that does not
// fit the context window; it is not sent.
var errReflectDoesNotFit = errors.New("improve: the self-review request does not fit the context window")

// scored is one suggestion after its part's self-review.
type scored struct {
	part int // 0-based
	c    Candidate
	fb   *feedback // nil: unscored
}

// partOutcome is what one part contributed.
type partOutcome struct {
	kept []scored
	// counts of the validation, the self-review and the score threshold.
	incomplete, unknown, noChange int
	unscored, unmatched, dropped  int
	reflectFailed                 bool
}

// finish makes the calls of a non-empty plan, part by part: the suggestion
// call, then the self-review call of its suggestions; then it merges the
// parts (v2 spec §1.5).
//
// The parts run sequentially, as pr_review's and pr_describe's do. A part
// whose suggestion call fails makes its files not reviewed
// (model_call_failed) with a note; when every part fails, the run fails
// with the first part's error, and a run in one call fails with its error.
func (pl *Plan) finish(ctx context.Context, deps Deps) (*Result, error) {
	res, log := pl.Result, pl.log
	n := len(pl.parts)
	outs := make([]*partOutcome, n)
	failed := make([]bool, n)
	var failNotes []string
	var firstErr error
	nFailed := 0
	for i, pt := range pl.parts {
		if n == 1 {
			progress(deps, StageCallingModel)
		} else {
			progress(deps, CallingModelPart(i+1, n))
		}
		shown := shownFiles(&res.Coverage)
		if n > 1 {
			shown = shownFiles(&pt.cov)
		}
		s, info, err := call(ctx, pl, deps, pt.fit.prompts, pt.fit.reaskUser, suggestionKeys,
			func(data map[string]any) (*suggestions, error) { return convertSuggestions(data, shown) })
		res.Metadata.LLMCalls += info.calls
		if err != nil {
			if n == 1 || ctx.Err() != nil {
				// One call: its error is the run's. A cancelled run stops at
				// once.
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			nFailed++
			failed[i] = true
			class := llmrun.FailureClass(err)
			failNotes = append(failNotes, noteFailedPart(i+1, n, class))
			log.Debug("improve: part failed", "part", i+1, "parts", n, "class", class)
			continue
		}
		pl.record(info)
		o, err := pl.score(ctx, deps, pt, s, i, n)
		if err != nil {
			return nil, err
		}
		outs[i] = o
		log.Debug("improve: part done", "part", i+1, "parts", n, "suggestions", len(s.cands),
			"kept", len(o.kept), "dropped_by_score", o.dropped, "unscored", o.unscored,
			"self_review_failed", o.reflectFailed, "incomplete", s.incomplete, "unknown_files", s.unknown,
			"no_change", s.noChange)
	}
	if nFailed == n {
		// Every part failed: the first part's classified error, as for a
		// run in one call.
		return nil, firstErr
	}
	if n > 1 {
		res.Coverage = pl.partsCoverage(failed)
		res.Notes = append(res.Notes, failNotes...)
	}
	pl.merge(outs)
	log.Debug("improve: done", "parts", n, "failed_parts", nFailed, "suggestions", len(res.Suggestions),
		"llm_calls", res.Metadata.LLMCalls, "self_review_calls", res.Metadata.SelfReviewCalls)
	return res, nil
}

// score runs the self-review of one part's validated suggestions and
// applies improve.min_score. A part without a suggestion
// makes no self-review call. A failed self-review (the call fails, its
// answer is unusable after the one re-ask, or its request does not fit)
// keeps every suggestion of the part, unscored. Only a cancelled context
// is returned as an error.
func (pl *Plan) score(ctx context.Context, deps Deps, pt *part, s *suggestions, i, n int) (*partOutcome, error) {
	o := &partOutcome{incomplete: s.incomplete, unknown: s.unknown, noChange: s.noChange}
	if len(s.cands) == 0 {
		return o, nil
	}
	if n == 1 {
		progress(deps, StageScoring)
	} else {
		progress(deps, ScoringPart(i+1, n))
	}
	r, info, err := pl.reflect(ctx, deps, pt.fit.diff, s.cands)
	pl.Result.Metadata.LLMCalls += info.calls
	pl.Result.Metadata.SelfReviewCalls += info.calls
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		pl.log.Debug("improve: self-review failed", "part", i+1, "parts", n, "class", llmrun.FailureClass(err),
			"does_not_fit", errors.Is(err, errReflectDoesNotFit))
		o.reflectFailed = true
		for _, c := range s.cands {
			o.kept = append(o.kept, scored{part: i, c: c})
		}
		return o, nil
	}
	pl.record(info)
	o.unmatched = r.unmatched
	for k, c := range s.cands {
		fb := r.fb[k]
		if fb == nil || fb.score == nil {
			o.unscored++
		} else if *fb.score < pl.minScore {
			o.dropped++
			continue
		}
		o.kept = append(o.kept, scored{part: i, c: c, fb: fb})
	}
	return o, nil
}

// reflect makes the self-review call of one part: its numbered diff (the
// one its suggestion call was sent) and its suggestions, numbered from 1.
func (pl *Plan) reflect(ctx context.Context, deps Deps, diff string, cands []Candidate) (*reflection, callInfo, error) {
	p, err := RenderReflectPrompts(ReflectInput{Language: pl.language, Diff: diff, Suggestions: cands})
	if err != nil {
		return nil, callInfo{}, err
	}
	ru, err := withReaskNote(p.User)
	if err != nil {
		return nil, callInfo{}, err
	}
	b := pl.Budget
	if tokens.RequestTokens(p.System, ru, b.Factor) > b.ContextWindow-b.HardReserve() {
		return nil, callInfo{}, errReflectDoesNotFit
	}
	return call(ctx, pl, deps, p, ru, reflectKeys, func(data map[string]any) (*reflection, error) {
		return convertReflection(data, cands)
	})
}

// merge merges the parts' suggestions into the result (v2 spec §1.5) and
// adds the notes of the validation, the self-review and the merge.
//
//   - Order: one global ranking over every part, not per part. First the
//     scored suggestions, by score descending; equal scores keep the part
//     order, then the model's order within the part. Then the unscored
//     suggestions (not checked by the self-review), in part order, then
//     the model's order.
//   - Duplicates: a suggestion whose fingerprint (X-13: the file, the
//     summary and the existing code) an earlier one in that ranking has is
//     dropped, within a part as across parts. Dedup runs on the ranked
//     list, so the occurrence kept is the higher-ranked one (the higher
//     score; on equal scores, the earlier part and model position; a
//     scored one before an unscored one).
//   - Cap: improve.max_suggestions on the deduplicated ranking, with a
//     note for the rest. So the cap never keeps a lower score of an
//     earlier part while cutting a higher score of a later part.
func (pl *Plan) merge(outs []*partOutcome) {
	res := pl.Result
	var all []scored
	var incomplete, unknown, noChange, unscored, unmatched, dropped int
	var failNotes []string
	for i, o := range outs {
		if o == nil {
			continue
		}
		incomplete += o.incomplete
		unknown += o.unknown
		noChange += o.noChange
		unscored += o.unscored
		unmatched += o.unmatched
		dropped += o.dropped
		if o.reflectFailed {
			if len(outs) == 1 {
				failNotes = append(failNotes, NoteNotScoredOneCall)
			} else {
				failNotes = append(failNotes, noteNotScoredPart(i+1))
			}
		}
		// Part order, then the model's order: the tie-break of the ranking.
		all = append(all, o.kept...)
	}
	// A stable sort over that order is the global ranking.
	slices.SortStableFunc(all, func(a, b scored) int {
		return scoreOf(b) - scoreOf(a)
	})

	seen := map[string]bool{}
	dups := 0
	out := []Suggestion{}
	for _, s := range all {
		fp := llmrun.Fingerprint(s.c.File, s.c.Summary, s.c.ExistingCode)
		if seen[fp] {
			dups++
			continue
		}
		seen[fp] = true
		out = append(out, suggestionOf(s))
	}
	capped := 0
	if pl.maxTotal > 0 && len(out) > pl.maxTotal {
		capped = len(out) - pl.maxTotal
		out = out[:pl.maxTotal]
	}
	res.Suggestions = out

	if unknown > 0 {
		res.Notes = append(res.Notes, noteUnknownFiles(unknown))
	}
	if incomplete > 0 {
		res.Notes = append(res.Notes, noteIncomplete(incomplete))
	}
	if noChange > 0 {
		res.Notes = append(res.Notes, noteNoChange(noChange))
	}
	res.Notes = append(res.Notes, failNotes...)
	if unmatched > 0 {
		res.Notes = append(res.Notes, noteUnmatchedEntries(unmatched))
	}
	if unscored > 0 {
		res.Notes = append(res.Notes, noteUnscored(unscored))
	}
	if dropped > 0 {
		res.Notes = append(res.Notes, noteScoreDropped(dropped, pl.minScore))
	}
	if dups > 0 {
		res.Notes = append(res.Notes, noteDuplicates(dups))
	}
	if capped > 0 {
		res.Notes = append(res.Notes, noteTotalCap(capped))
	}
	pl.log.Debug("improve: parts merged", "parts", len(outs), "suggestions", len(out),
		"duplicates_dropped", dups, "capped", capped, "max_suggestions", pl.maxTotal)
}

// scoreOf ranks a suggestion in the merge: its score (0-10), or -1 when it
// is unscored, so that unscored suggestions sort after every scored one.
func scoreOf(s scored) int {
	if s.fb == nil || s.fb.score == nil {
		return -1
	}
	return *s.fb.score
}

// suggestionOf builds the result entry of a merged suggestion. Verified
// and Anchor keep their interim values until WP-2g and WP-2h.
func suggestionOf(s scored) Suggestion {
	out := Suggestion{
		File: s.c.File, Language: s.c.Language, Label: s.c.Label, Summary: s.c.Summary, Content: s.c.Content,
		ExistingCode: s.c.ExistingCode, ImprovedCode: s.c.ImprovedCode,
	}
	if fb := s.fb; fb != nil {
		out.StartLine, out.EndLine = fb.start, fb.end
		if fb.score != nil {
			out.Score, out.Why = fb.score, fb.why
		}
	}
	return out
}

// shownFiles is the set of files a suggestion call was shown with their
// content, the only paths its suggestions may name (v2 spec §1.6): the
// coverage's included and clipped files. A deleted file listed by name has
// no code to quote.
func shownFiles(c *Coverage) map[string]bool {
	shown := map[string]bool{}
	for _, l := range [][]string{c.Included, c.Clipped} {
		for _, p := range l {
			shown[p] = true
		}
	}
	return shown
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
// (as pr_review). The returned info holds also on an error; an answer that
// is still unusable after the re-ask is ErrUnparseable.
func call[T any](ctx context.Context, pl *Plan, deps Deps, p Prompts, reaskUser string, keys yamlrepair.Keys,
	conv func(map[string]any) (T, error)) (T, callInfo, error) {
	var info callInfo
	var zero T
	for attempt := range 2 {
		user := p.User
		if attempt == 1 {
			user = reaskUser
			info.reasked = true
		}
		resp, err := deps.LLM.Complete(ctx, p.System, user)
		info.calls++
		if err != nil {
			return zero, info, err
		}
		info.truncated = resp.Truncated
		data, tactic := load(resp.Content, keys)
		info.tactic = tactic
		pl.log.Debug("improve: answer loaded", "attempt", attempt+1, "repair_tactic", tactic,
			"truncated", resp.Truncated, "prompt_tokens_reported", resp.Usage.PromptTokens,
			"completion_tokens_reported", resp.Usage.CompletionTokens)
		v, err := conv(data)
		if err == nil {
			return v, info, nil
		}
		if attempt == 1 {
			return zero, info, ErrUnparseable.WithCause(err)
		}
	}
	return zero, info, ErrUnparseable // not reached
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
