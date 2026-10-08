package diffpipe

import (
	"errors"
	"fmt"
	"slices"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Chunks is the outcome of PrepareChunks: the prepared diff of every part
// (one model call each, X-19) and the accounting over all of them.
//
// Chunk invariant (v1.1 spec WP-11e1): every element of Input.Files and
// Input.Skipped appears exactly once across Included, Clipped,
// DeletedListed, TooLarge, Omitted.Added, Omitted.Modified, Omitted.Deleted
// and Skipped. Hence
//
//	len(Included) + len(Clipped) + len(DeletedListed) + len(TooLarge) +
//	len(Omitted.Added) + len(Omitted.Modified) + len(Omitted.Deleted) +
//	len(Skipped) == len(Input.Files) + len(Input.Skipped).
//
// A part's own Omitted lists the files that part left out, most of which a
// later part reviews; only Chunks.Omitted is the coverage after packing.
type Chunks struct {
	// Parts holds the prepared diffs in call order. Parts[0] is exactly
	// Prepare(in); there is always at least one part.
	Parts []*Prepared
	// Included, Clipped and DeletedListed are the parts' lists joined in
	// part order.
	Included      []string
	Clipped       []string
	DeletedListed []string
	// Omitted holds the files no part reviewed because packing stopped
	// (maxChunks parts exist, or no further part could make progress), in
	// the last part's section order.
	Omitted Omitted
	// Skipped is Parts[0].Skipped (the provider's skips verbatim, then the
	// files Prepare could not render), followed by any file a later part
	// could not render, in packing order. A further part whose only entries
	// are Skipped is not added to Parts (it would be a model call with
	// nothing to review); its entries are still accounted here.
	Skipped []provider.SkippedFile
	// TooLarge holds the files that did not fit a part of their own under
	// large_patch_policy skip, in the order they were set aside. They are
	// not reviewed; a coverage lists them as skipped with reason
	// SkipTooLarge.
	TooLarge []string
}

// PrepareChunks splits the reviewable files into at most maxChunks
// prepared diffs. Chunk 1 is exactly Prepare(in). Every further chunk
// is Prepare over the files earlier chunks did not include, clip or
// list, in the original rank order.
//
// Details (v1.1 spec WP-11e1):
//   - A further chunk gets the remaining provider.FilePatch entries and no
//     provider skips: those are accounted once, from chunk 1. Its language
//     groups keep the order of the whole pull request's ranking.
//   - A deleted file listed by name (DeletedListed) in one chunk is not
//     given to a later one, so a name is listed in at most one chunk.
//   - When a further chunk admits nothing, large_patch_policy applies to its
//     top file as in Prepare: with clip the clipped file is that chunk's
//     content; with skip (or when the clip keeps nothing) the file goes to
//     TooLarge and the chunk is prepared again without it.
//   - A further chunk becomes a part (one model call) only when it reviews
//     something: len(Included) + len(Clipped) + len(DeletedListed) > 0. A
//     chunk whose only entries are Skipped is not a part: those entries are
//     appended to Chunks.Skipped and taken, and packing continues with the
//     files left.
//   - An error wrapping tokens.ErrDoesNotFit comes only from chunk 1, as
//     from Prepare. Packing stops when no file remains, when maxChunks parts
//     exist, or when a further chunk would neither review nor skip anything
//     (only deleted files remain and none of them fits by name or by
//     patch); the files left are Omitted.
//
// maxChunks below 1 is a programming error (configuration allows 1 to 32):
// PrepareChunks returns an error that does not wrap tokens.ErrDoesNotFit,
// as it does for an unknown mode.
func PrepareChunks(in Input, maxChunks int) (*Chunks, error) {
	return prepareChunks(in, maxChunks, estimateCounter(in.Budget.Factor))
}

func prepareChunks(in Input, maxChunks int, c *counter) (*Chunks, error) {
	if maxChunks < 1 {
		return nil, fmt.Errorf("diffpipe: maxChunks %d is less than 1", maxChunks)
	}
	first, err := prepare(in, c)
	if err != nil {
		return nil, err
	}
	ch := &Chunks{}
	// taken holds every path a part has accounted for (reviewed, listed,
	// skipped) or set aside as too large: the "already taken" filter.
	taken := map[string]bool{}
	ch.addPart(first, taken)
	last := first
	order := rankOrder(in.Files)

pack:
	for len(ch.Parts) < maxChunks {
		for {
			rest := remainingFiles(in.Files, taken)
			if len(rest) == 0 {
				break pack
			}
			p, err := prepareRanked(Input{Files: rest, Mode: in.Mode, Budget: in.Budget, Diff: in.Diff}, c, order)
			var tl *tooLargeError
			switch {
			case errors.As(err, &tl):
				ch.TooLarge = append(ch.TooLarge, tl.path)
				taken[tl.path] = true
				continue
			case errors.Is(err, tokens.ErrDoesNotFit):
				// Only deleted files remain and not even the
				// deleted-files section fits.
				break pack
			case err != nil:
				return nil, err
			}
			added, progress := ch.acceptPart(p, taken)
			if !progress {
				// Only deleted files with a patch too large to admit
				// remain (the policy never applies to a deleted file):
				// another part would repeat this one.
				break pack
			}
			if added {
				last = p
				break
			}
			// A part with only Skipped entries: they are accounted and
			// taken, so the next attempt has fewer files.
		}
	}
	ch.Omitted = leftOver(in.Files, taken, last.Omitted)
	return ch, nil
}

// acceptPart decides what a further part p becomes. It is a part (one
// model call) only when it reviews something: len(Included) + len(Clipped)
// + len(DeletedListed) > 0; then it is added and acceptPart reports added
// and progress. A part with no reviewable content but Skipped entries is
// not added: its Skipped entries are appended to ch.Skipped and taken, and
// acceptPart reports progress only. With neither, it reports no progress
// and packing stops.
func (ch *Chunks) acceptPart(p *Prepared, taken map[string]bool) (added, progress bool) {
	if len(p.Included)+len(p.Clipped)+len(p.DeletedListed) > 0 {
		ch.addPart(p, taken)
		return true, true
	}
	if len(p.Skipped) == 0 {
		return false, false
	}
	ch.Skipped = append(ch.Skipped, p.Skipped...)
	for _, s := range p.Skipped {
		taken[s.Path] = true
	}
	return false, true
}

// addPart appends a part and its accounting, and marks its files taken.
func (ch *Chunks) addPart(p *Prepared, taken map[string]bool) {
	ch.Parts = append(ch.Parts, p)
	ch.Included = append(ch.Included, p.Included...)
	ch.Clipped = append(ch.Clipped, p.Clipped...)
	ch.DeletedListed = append(ch.DeletedListed, p.DeletedListed...)
	ch.Skipped = append(ch.Skipped, p.Skipped...)
	for _, path := range slices.Concat(p.Included, p.Clipped, p.DeletedListed) {
		taken[path] = true
	}
	for _, s := range p.Skipped {
		taken[s.Path] = true
	}
}

// remainingFiles returns the files not yet taken, in input order (the
// ranking is applied by prepareRanked).
func remainingFiles(files []provider.FilePatch, taken map[string]bool) []provider.FilePatch {
	var rest []provider.FilePatch
	for _, f := range files {
		if !taken[f.Path] {
			rest = append(rest, f)
		}
	}
	return rest
}

// leftOver is the overall Omitted: the files not taken, in the order of the
// last part's Omitted lists (every such file is in them, since the last part
// was prepared over a superset of them). A file missing there, which the
// invariant rules out, is added by change type rather than lost.
func leftOver(files []provider.FilePatch, taken map[string]bool, last Omitted) Omitted {
	listed := map[string]bool{}
	keep := func(paths []string) []string {
		var out []string
		for _, p := range paths {
			if !taken[p] && !listed[p] {
				out = append(out, p)
				listed[p] = true
			}
		}
		return out
	}
	o := Omitted{Added: keep(last.Added), Modified: keep(last.Modified), Deleted: keep(last.Deleted)}
	for _, f := range files {
		if taken[f.Path] || listed[f.Path] {
			continue
		}
		listed[f.Path] = true
		switch f.Type {
		case provider.ChangeAdded:
			o.Added = append(o.Added, f.Path)
		case provider.ChangeDeleted:
			o.Deleted = append(o.Deleted, f.Path)
		default:
			o.Modified = append(o.Modified, f.Path)
		}
	}
	return o
}
