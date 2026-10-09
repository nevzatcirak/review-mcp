package describe

import (
	"errors"
	"fmt"

	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// planParts tries to plan the description in parts (X-19, Y-6), exactly as
// pr_review plans a review in parts: the scaffolding of the part-mode
// prompts is measured with PartHeader(maxChunks, maxChunks), the longest
// part line, and diffpipe.PrepareChunks packs the files into at most
// maxChunks parts against that budget. It reports false, leaving pl as it
// was, when the description stays one call: the part-mode scaffolding
// leaves no diff budget, or the packing yields one part.
//
// Each part asks for the files walkthrough only (PromptInput.FilesOnly).
// The reduce call that follows the parts carries no diff and is budgeted on
// its own (reducePrompts).
func (pl *Plan) planParts(dIn diffpipe.Input, in PromptInput, maxChunks int) (bool, error) {
	res, log := pl.Result, pl.log
	factor := dIn.Budget.Factor
	hdr := in
	hdr.FilesOnly = true
	hdr.PartHeader = PartHeader(maxChunks, maxChunks)
	promptTokens, err := ScaffoldingTokens(hdr, factor)
	if err != nil {
		return false, err
	}
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

	types := changeTypes(dIn.Files)
	n := len(ch.Parts)
	parts := make([]*part, n)
	diffTokens, requestTokens, trimmed := 0, 0, false
	for i, prep := range ch.Parts {
		pin := in
		pin.FilesOnly = true
		pin.PartHeader = PartHeader(i+1, n)
		fit, err := fitPrompts(pin, prep.Text, budget)
		if err != nil {
			return false, err
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
			log.Debug("describe: diff trimmed by the request-size guard", "part", i+1, "kept_lines", fit.keptLines,
				"request_tokens", fit.requestTokens)
		}
		diffTokens += tokensOfPart
		requestTokens = max(requestTokens, fit.requestTokens)
		parts[i] = pt
	}

	pl.parts, pl.chunks, pl.Budget = parts, ch, budget
	pl.Prompts = parts[0].fit.prompts
	covs := make([]Coverage, n)
	for i, pt := range parts {
		covs[i] = pt.cov
	}
	res.Coverage = llmrun.PartsCoverage(ch, covs, nil, pl.flt)
	m := &res.Metadata
	// As for pr_review in parts: prompt_tokens is the part-mode scaffolding
	// with the part line, diff_tokens the sum over the parts,
	// request_tokens the largest request with a diff, fast_path false.
	m.PromptTokens, m.DiffTokens, m.RequestTokens, m.FastPath = promptTokens, diffTokens, requestTokens, false
	if trimmed {
		m.DiffTrimmed = true
		res.Notes = append(res.Notes, NoteDiffTrimmed)
	}
	if c := len(res.Coverage.Clipped); c > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf(llmrun.NoteClippedFormat, llmrun.CountPhrase(c, "file was", "files were")))
	}
	reached := n == maxChunks && llmrun.LeavesFilesOut(&diffpipe.Prepared{Omitted: ch.Omitted})
	res.Notes = append(res.Notes, partialNotes(llmrun.ChunkedPartialNotes(&res.Coverage, budget, reached))...)
	log.Debug("describe: diff prepared in parts", "parts", n, "max_chunks", maxChunks, "too_large", len(ch.TooLarge),
		"left_out", len(ch.Omitted.Added)+len(ch.Omitted.Modified)+len(ch.Omitted.Deleted),
		"prompt_tokens", promptTokens, "diff_tokens", diffTokens)
	return true, nil
}

// fitted is the outcome of the request-size guard.
type fitted struct {
	prompts Prompts
	// reaskUser is the user prompt of the re-ask.
	reaskUser string
	// requestTokens is tokens.RequestTokens of prompts.
	requestTokens int
	// diff is the diff in the prompts; keptLines is its line count when the
	// guard trimmed it, -1 otherwise.
	diff      string
	keptLines int
}

// fitPrompts renders the final prompts and applies the request-size guard
// (llmrun.Fit), checking the re-ask request, as pr_review does.
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
