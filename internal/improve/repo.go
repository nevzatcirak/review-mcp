package improve

import (
	"context"
	"slices"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Repository context in the improve pipeline (X-22), wired as pr_review
// wires it (internal/review/repo.go): repoctx does the git work, the
// budgeting and the rendering; this file connects it to the suggestion
// prompts. The diff always wins: in a run in one call the block goes only
// into the room the diff leaves, and a run in parts reserves
// context.repo.max_tokens in the packing budget of every part. The
// self-review prompts carry no repository context, as upstream's do not.

// placeRepo puts the repository-context block into the suggestion prompt
// of one part (or of the whole run) when the diff leaves room for it. files
// are the files whose content is in this prompt's diff and fit0 is the
// fitted prompt without the block. It returns the prompt input and the
// fitted prompt to use, and the outcome.
func placeRepo(ctx context.Context, sess *repoctx.Session, in PromptInput, files []provider.FilePatch,
	sc *repoctx.Scope, diff string, fit0 *fitted, b tokens.Budget) (PromptInput, *fitted, repoctx.Outcome) {
	in1, fit1 := in, fit0
	out := sess.Attach(ctx, files, sc, b, fit0.requestTokens, fit0.keptLines >= 0, func(block string) (bool, error) {
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

// gitctxPaths are the paths of files and the old paths of renamed ones
// (gitctx.ChangedPaths), for the exclusion of a search.
func gitctxPaths(files []provider.FilePatch) []string { return gitctx.ChangedPaths(files) }
