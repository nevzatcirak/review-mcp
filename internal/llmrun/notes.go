package llmrun

import "strconv"

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
)

// CountPhrase returns "1 <one>" or "n <many>".
func CountPhrase(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
