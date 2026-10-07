package llmrun

import (
	"slices"
	"strconv"

	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Notes both pipelines add.
const (
	// NoteNoReviewableChanges: every file was filtered, skipped or empty,
	// so the model was not called.
	NoteNoReviewableChanges = "No reviewable changes after filtering."
	// NoteDiffTrimmed: the request-size guard shortened the diff.
	NoteDiffTrimmed = "The diff was shortened to fit the context window; the coverage section lists the files that are incomplete or left out."
	// NoteClippedFormat: files included only in part; the argument is a
	// CountPhrase.
	NoteClippedFormat = "%s included only in part (clipped) to fit the context window."
	// NoteRaiseLimit: the hint of a partial result whose files were lost to
	// the diff budget, when diff.max_tokens is the limit that applied (X-18).
	NoteRaiseLimit = "To review every file, raise or unset diff.max_tokens, or use a model with a larger context window."
	// NoteLargerWindow is the same hint when the context window is the limit
	// that applied, so diff.max_tokens would not help and is not named.
	NoteLargerWindow = "To review every file, use a model with a larger context window."
	// NoteProviderSkips: some files were lost for a reason the diff budget
	// does not change (too large for the provider, over its file limit,
	// unreadable).
	NoteProviderSkips = "Some changed files were skipped or could not be read from the provider (see Coverage); a larger diff budget does not change that."
	// NoteRaiseChunksLimit and NoteRaiseChunksWindow are NoteRaiseLimit and
	// NoteLargerWindow of a chunked review that used every part it may
	// (review.max_chunks) and still left files out (X-19).
	NoteRaiseChunksLimit  = "To review every file, raise review.max_chunks, raise or unset diff.max_tokens, or use a model with a larger context window."
	NoteRaiseChunksWindow = "To review every file, raise review.max_chunks, or use a model with a larger context window."
)

// PartialNotes returns the notes of a partial result (X-18), none for a
// complete one. The budget hint appears when at least one file was lost to
// the diff budget (clipped or omitted) and names diff.max_tokens only when
// that cap was the limit that applied (Budget.Limit). A file the provider
// skipped or could not read gets its own note, since no budget setting helps.
func PartialNotes(c *Coverage, b tokens.Budget) []string {
	return ChunkedPartialNotes(c, b, false)
}

// ChunkedPartialNotes is PartialNotes for a review in parts (X-19).
// chunksReached reports that the review used every part review.max_chunks
// allows and files were still left out; the budget hint then also names
// review.max_chunks. A file too large for a part of its own (too_large) is a
// budget loss: a larger window or diff budget helps, so it gets the budget
// hint, not NoteProviderSkips. A file of a failed part (model_call_failed)
// has its own note from the pipeline and gets neither.
func ChunkedPartialNotes(c *Coverage, b tokens.Budget, chunksReached bool) []string {
	if !c.Tally().Partial {
		return nil
	}
	var notes []string
	budgetLoss := len(c.Clipped)+len(c.Omitted.Added)+len(c.Omitted.Modified)+len(c.Omitted.Deleted) > 0 ||
		slices.ContainsFunc(c.Skipped, func(s SkippedFile) bool { return s.Reason == diffpipe.SkipTooLarge })
	if budgetLoss {
		limit := b.Limit() == tokens.LimitDiffMaxTokens
		switch {
		case chunksReached && limit:
			notes = append(notes, NoteRaiseChunksLimit)
		case chunksReached:
			notes = append(notes, NoteRaiseChunksWindow)
		case limit:
			notes = append(notes, NoteRaiseLimit)
		default:
			notes = append(notes, NoteLargerWindow)
		}
	}
	for _, s := range c.Skipped {
		if skipLosesReviewableFile(s.Reason) && s.Reason != diffpipe.SkipTooLarge && s.Reason != SkipModelCallFailed {
			notes = append(notes, NoteProviderSkips)
			break
		}
	}
	return notes
}

// CountPhrase returns "1 <one>" or "n <many>".
func CountPhrase(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
