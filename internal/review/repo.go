package review

import (
	"context"
	"slices"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Repository context in the review pipeline (v1.1 spec WP-11c, X-22, RC-8,
// RC-9). repoctx does the git work, the budgeting and the rendering; this
// file connects it to the prompts.
//
// The diff always wins: the block is placed only after the diff is prepared
// and the prompts fit, into the room that is left below the soft reserve
// (repoctx.Session.Attach). A block that does not fit whole entries into
// that room, or whose rendering would make the request-size guard trim the
// diff, is dropped with a note; the diff, its coverage and its prompt are
// then exactly those of a review without repository context. A review in
// parts reserves context.repo.max_tokens in the packing budget of every part
// (planParts), so each part has the room for its own block.

// placeRepo puts the repository-context block into the prompt of one part
// (or of the whole review) when the diff leaves room for it. files are the
// files whose content is in this prompt's diff and fit0 is the fitted prompt
// without the block. It returns the prompt input and the fitted prompt to
// use, and the outcome.
func placeRepo(ctx context.Context, sess *repoctx.Session, in PromptInput, files []provider.FilePatch,
	diff string, fit0 *fitted, b tokens.Budget) (PromptInput, *fitted, repoctx.Outcome) {
	in1, fit1 := in, fit0
	out := sess.Attach(ctx, files, b, fit0.requestTokens, fit0.keptLines >= 0, func(block string) (bool, error) {
		cand := in
		cand.RepoContext = block
		f, err := fitPrompts(cand, diff, b)
		if err != nil {
			return false, err
		}
		in1, fit1 = cand, f
		return f.keptLines >= 0, nil
	})
	if out.Block == "" {
		return in, fit0, out
	}
	return in1, fit1, out
}

// diffFiles returns the files of all whose content is in the prepared diff
// (included or clipped), in the order of all.
func diffFiles(all []provider.FilePatch, included, clipped []string) []provider.FilePatch {
	var out []provider.FilePatch
	for i := range all {
		if slices.Contains(included, all[i].Path) || slices.Contains(clipped, all[i].Path) {
			out = append(out, all[i])
		}
	}
	return out
}
