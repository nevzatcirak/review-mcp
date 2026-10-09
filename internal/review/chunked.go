package review

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// A review in parts (X-19, v1.1 spec WP-11e2): when the prepared diff leaves
// files out, the files are packed into up to review.max_chunks parts
// (diffpipe.PrepareChunks), each part is one model call with the same
// scaffolding plus a part line, and the answers are merged into one review.

// part is one model call of a review.
type part struct {
	prep *diffpipe.Prepared
	fit  *fitted
	// cov holds the part's own files after the request-size guard:
	// Included, Clipped, DeletedListed, and in Omitted only the files the
	// guard cut. It is set for a review in parts only.
	cov Coverage
}

// stageCallingModelPartFormat is the progress stage of part I of N.
const stageCallingModelPartFormat = StageCallingModel + " (part %d of %d)"

// CallingModelPart is the progress stage of the model call of part i of a
// review in n > 1 parts: "calling model (part I of N)". A review in one call
// reports StageCallingModel.
func CallingModelPart(i, n int) string {
	return fmt.Sprintf(stageCallingModelPartFormat, i, n)
}

// StageParts returns N for a stage made by CallingModelPart, so a progress
// reporter can size its total; ok is false for any other stage.
func StageParts(stage string) (n int, ok bool) {
	rest, ok := strings.CutPrefix(stage, StageCallingModel+" (part ")
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, ")")
	if !ok {
		return 0, false
	}
	_, total, ok := strings.Cut(rest, " of ")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(total)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// noteFailedPart: a part whose model call failed (X-19). class is a fixed
// error class (failureClass), never error text.
func noteFailedPart(i, n int, class string) string {
	return fmt.Sprintf("Part %d of %d failed (%s); its files were not reviewed.", i, n, class)
}

// noteTotalCap: findings dropped by review.max_total_findings after the
// merge.
func noteTotalCap(n int) string {
	return countPhrase(n, "further finding was", "further findings were") +
		" not shown because of review.max_total_findings."
}

// failureClass is the fixed class of a part's error for its note
// (llmrun.FailureClass, shared with pr_describe).
func failureClass(err error) string { return llmrun.FailureClass(err) }

// leavesFilesOut reports whether a prepared diff left files for a further
// part (llmrun.LeavesFilesOut, shared with pr_describe).
func leavesFilesOut(p *diffpipe.Prepared) bool { return llmrun.LeavesFilesOut(p) }

// planParts tries to plan the review in parts. It reports false, leaving pl
// as it was, when the review stays one call: the scaffolding with the part
// line leaves no diff budget, or the packing yields one part.
//
// The diff budget of every part reserves the part line: the scaffolding is
// measured with PartHeader(maxChunks, maxChunks), the longest line the
// review can have. A review in one call keeps the budget without it, so
// its prompts are those of v1.0.
//
// With repository context (rcs non-nil) and reserve > 0, the budget of every
// part also holds reserve tokens (context.repo.max_tokens) for the part's own
// block (X-22, v1.1 spec WP-11c); the caller decides on the reservation
// (the head is fetched, there are symbols). Each part searches the symbols of
// its own files only, excluding its own files from the search. A use in
// another file of the pull request is shown, marked with the part that
// reviews the file (the head SHA holds the new version) or as not reviewed.
func (pl *Plan) planParts(ctx context.Context, dIn diffpipe.Input, in PromptInput, maxChunks int,
	rcs *repoctx.Session, reserve int) (bool, error) {
	res, log := pl.Result, pl.log
	factor := dIn.Budget.Factor
	hdr := in
	hdr.PartHeader = PartHeader(maxChunks, maxChunks)
	promptTokens, err := ScaffoldingTokens(hdr, factor)
	if err != nil {
		return false, err
	}
	promptTokens += reserve
	budget := dIn.Budget
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

	types := map[string]provider.ChangeType{}
	for _, f := range dIn.Files {
		types[f.Path] = f.Type
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
			// Each part searches the symbols of its own files only (X-19).
			var o repoctx.Outcome
			own := diffFiles(dIn.Files, concat(prep.Included, prep.DeletedListed), prep.Clipped)
			sc := &repoctx.Scope{Exclude: gitctxPaths(own), Marks: marks}
			_, fit, o = placeRepo(ctx, rcs, pin, diffFiles(dIn.Files, prep.Included, prep.Clipped), sc, prep.Text, fit, budget)
			repos = append(repos, o)
			log.Debug("review: repository context", "part", i+1, "status", o.Status, "reason", o.Reason,
				"symbols", o.Symbols, "references", o.References, "files", o.Files, "omitted", o.Omitted)
		}
		pt := &part{prep: prep, fit: fit, cov: Coverage{
			Included:      llmrun.NonNil(append([]string(nil), prep.Included...)),
			Clipped:       llmrun.NonNil(append([]string(nil), prep.Clipped...)),
			DeletedListed: llmrun.NonNil(append([]string(nil), prep.DeletedListed...)),
			Omitted:       OmittedFiles{Added: []string{}, Modified: []string{}, Deleted: []string{}},
		}}
		tokensOfPart := prep.Tokens
		if fit.keptLines >= 0 {
			trimCoverage(&pt.cov, prep.Text, fit.keptLines, types)
			tokensOfPart = tokens.Estimate(fit.diff, factor)
			trimmed = true
			log.Debug("review: diff trimmed by the request-size guard", "part", i+1, "kept_lines", fit.keptLines,
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
	// Decision (lead; architect may override on 11e2): for a review in
	// parts, prompt_tokens is the scaffolding with the part line,
	// diff_tokens the sum over the parts, request_tokens the largest
	// request (the one to compare with the context window), and fast_path
	// false (the whole diff did not fit one call).
	m.PromptTokens, m.DiffTokens, m.RequestTokens, m.FastPath = promptTokens, diffTokens, requestTokens, false
	if trimmed {
		m.DiffTrimmed = true
		res.Notes = append(res.Notes, NoteDiffTrimmed)
	}
	if c := len(res.Coverage.Clipped); c > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(noteClippedFormat, countPhrase(c, "file was", "files were")))
	}
	reached := n == maxChunks && leftOut(ch.Omitted)
	res.Notes = append(res.Notes, llmrun.ChunkedPartialNotes(&res.Coverage, budget, reached)...)
	res.Notes = append(res.Notes, repoNotes...)
	log.Debug("review: diff prepared in parts", "parts", n, "max_chunks", maxChunks, "too_large", len(ch.TooLarge),
		"left_out", len(ch.Omitted.Added)+len(ch.Omitted.Modified)+len(ch.Omitted.Deleted),
		"prompt_tokens", promptTokens, "diff_tokens", diffTokens)
	return true, nil
}

func leftOut(o diffpipe.Omitted) bool { return len(o.Added)+len(o.Modified)+len(o.Deleted) > 0 }

// partsCoverage is the coverage of a review in parts (X-19 honesty,
// llmrun.PartsCoverage, shared with pr_describe): the parts' files in part
// order, then the files no part reviewed. failed marks the parts whose model
// call failed (nil: none); their files are Skipped with reason
// model_call_failed. The repository context summed over the parts is added.
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

func concat(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// mergeParts merges the answers of a review in parts (RC-12, v1.1 spec
// WP-11e2 item 2) and records the failed parts in the result: their files
// become not reviewed and each gets a note. answers[i] is nil for a failed
// part, whose fixed error class is classes[i]; at least one is not nil.
//
//   - Findings: in part order, then model order. A finding whose
//     fingerprint (X-13) an earlier part already returned is dropped. The
//     list is then capped at pl.maxTotal, with a note for the rest.
//   - Effort: the largest of the parts that returned one.
//   - Tests: true if any part says true; false if every part that answered
//     says false; nil if none answered.
//   - Security and performance concerns (mergeConcerns): the concern texts
//     when at least one part has a concern; "No" only when every
//     successful part said "No"; otherwise nil, with a note for each part
//     that left the field out, so that "No" never covers files whose part
//     said nothing (architect, DQ-6 on 11e2).
func (pl *Plan) mergeParts(answers []*partAnswer, classes []string) *Review {
	res := pl.Result
	n := len(answers)
	failed := make([]bool, n)
	var failNotes []string
	for i, a := range answers {
		if a == nil {
			failed[i] = true
			failNotes = append(failNotes, noteFailedPart(i+1, n, classes[i]))
		}
	}
	res.Coverage = pl.partsCoverage(failed)
	res.Notes = append(res.Notes, failNotes...)

	merged := &Review{KeyIssuesToReview: []KeyIssue{}}
	seen := map[string]bool{}
	var security, performance []partText
	var securityMissing, performanceMissing []int
	truncated, reasked, tactic, dups := false, false, "", 0
	var partNotes []string
	for i, a := range answers {
		if a == nil {
			continue
		}
		if tactic == "" {
			tactic = a.tactic
		}
		truncated = truncated || a.truncated
		reasked = reasked || a.reasked
		for _, note := range a.conv.Notes {
			partNotes = append(partNotes, fmt.Sprintf("Part %d: %s", i+1, note))
		}
		r := a.rev
		var mine []string
		for _, ki := range r.KeyIssuesToReview {
			fp := Fingerprint(ki.RelevantFile, ki.IssueHeader, ki.IssueContent)
			if seen[fp] {
				dups++
				continue
			}
			mine = append(mine, fp)
			merged.KeyIssuesToReview = append(merged.KeyIssuesToReview, ki)
		}
		// Duplicates within one answer stay, as in a review in one call;
		// only a finding an earlier part returned is dropped.
		for _, fp := range mine {
			seen[fp] = true
		}
		if e := r.EstimatedEffortToReview; e != nil && (merged.EstimatedEffortToReview == nil || *e > *merged.EstimatedEffortToReview) {
			v := *e
			merged.EstimatedEffortToReview = &v
		}
		if t := r.RelevantTests; t != nil {
			v := *t || (merged.RelevantTests != nil && *merged.RelevantTests)
			merged.RelevantTests = &v
		}
		if s := r.SecurityConcerns; s != nil {
			security = append(security, partText{i + 1, *s})
		} else {
			securityMissing = append(securityMissing, i+1)
		}
		if s := r.PerformanceConcerns; s != nil {
			performance = append(performance, partText{i + 1, *s})
		} else {
			performanceMissing = append(performanceMissing, i+1)
		}
	}
	var concernNotes []string
	if pl.toggles.Security {
		var notes []string
		merged.SecurityConcerns, notes = mergeConcerns(security, securityMissing, SecurityNo, "security")
		concernNotes = append(concernNotes, notes...)
	}
	if pl.toggles.Performance {
		var notes []string
		merged.PerformanceConcerns, notes = mergeConcerns(performance, performanceMissing, PerformanceNo, "performance")
		concernNotes = append(concernNotes, notes...)
	}

	m := &res.Metadata
	m.Truncated, m.Reasked, m.RepairTactic = truncated, reasked, tactic
	if truncated {
		res.Notes = append(res.Notes, NoteTruncated)
	}
	if reasked {
		res.Notes = append(res.Notes, NoteReasked)
	}
	res.Notes = append(res.Notes, partNotes...)
	res.Notes = append(res.Notes, concernNotes...)
	if extra := len(merged.KeyIssuesToReview) - pl.maxTotal; pl.maxTotal > 0 && extra > 0 {
		merged.KeyIssuesToReview = merged.KeyIssuesToReview[:pl.maxTotal]
		res.Notes = append(res.Notes, noteTotalCap(extra))
	}
	pl.log.Debug("review: parts merged", "parts", n, "findings", len(merged.KeyIssuesToReview),
		"duplicates_dropped", dups, "max_total_findings", pl.maxTotal)
	return merged
}

// partText is one part's answer to a concerns field.
type partText struct {
	part int
	text string
}

// noteUnanswered: a successful part left a concerns field out while the
// other parts said "No" (mergeConcerns). question is "security" or
// "performance"; the sentence is fixed and carries no model text.
func noteUnanswered(part int, question string) string {
	return fmt.Sprintf("Part %d did not answer the %s question; nothing is concluded about its files.", part, question)
}

// mergeConcerns merges the successful parts' answers to a No-or-text field
// (security_concerns, performance_concerns). answers are the parts that
// answered, missing the numbers of the parts that left the field out.
//
//   - At least one part has a concern: the concern texts (true statements
//     about their parts), joined by a blank line, each prefixed with
//     "Part I: " only when more than one part has one.
//   - Every successful part said no: no.
//   - Some parts said no and others left the field out: nil, and one note
//     per silent part (noteUnanswered). "No" would claim something about
//     files whose part said nothing.
//   - No part answered: nil without a note, as a review in one call leaves
//     an unanswered field (a warning in the debug log only).
func mergeConcerns(answers []partText, missing []int, no, question string) (*string, []string) {
	if len(answers) == 0 {
		return nil, nil
	}
	var concerns []partText
	for _, a := range answers {
		if a.text != no {
			concerns = append(concerns, a)
		}
	}
	switch len(concerns) {
	case 0:
		if len(missing) == 0 {
			s := no
			return &s, nil
		}
		notes := make([]string, len(missing))
		for i, part := range missing {
			notes[i] = noteUnanswered(part, question)
		}
		return nil, notes
	case 1:
		s := concerns[0].text
		return &s, nil
	}
	texts := make([]string, len(concerns))
	for i, c := range concerns {
		texts[i] = "Part " + strconv.Itoa(c.part) + ": " + c.text
	}
	s := strings.Join(texts, "\n\n")
	return &s, nil
}

// partMarks gives, for every file of the pull request a part's search may
// meet, the text after the symbol of a use in it (repoctx.Scope.Marks): the
// part that reviews the file, as packed, else "not reviewed" (left out by
// the budget, too large for a part, skipped by the provider).
func partMarks(dIn diffpipe.Input, ch *diffpipe.Chunks) map[string]string {
	marks := map[string]string{}
	for _, f := range dIn.Skipped {
		marks[f.Path] = repoctx.MarkNotReviewed
	}
	for i := range dIn.Files {
		marks[dIn.Files[i].Path] = repoctx.MarkNotReviewed
	}
	for j, prep := range ch.Parts {
		for _, f := range concat(prep.Included, prep.Clipped, prep.DeletedListed) {
			marks[f] = repoctx.MarkReviewedInPart(j + 1)
		}
	}
	return marks
}
