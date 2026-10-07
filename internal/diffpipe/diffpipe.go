// Package diffpipe assembles the diff text one LLM call sees (spec P3 §4):
// it ranks the changed files by language, tries the extended fast path,
// and otherwise runs upstream's compressed path (budget admission, exact
// recount with a verified-prefix search, omitted-file sections), then
// applies the v1 large_patch_policy rule and accounts for every file.
//
// Behaviour mirrors PR-Agent at commit
// 8e5a9295973b24af4b70cafd0b660a230811ef9e (pr_agent/algo/pr_processing.py:
// get_pr_diff, pr_generate_extended_diff, pr_generate_compressed_diff,
// generate_full_patch, _find_verified_fitting_prefix_length,
// _append_metadata_section, _count_raw_and_stripped_tokens). The goldens
// under testdata/upstream are produced by running upstream's get_pr_diff
// (see the README there). No upstream code is copied; the contract strings
// come from internal/patch/literals.go and tokens.TruncationMarker.
//
// Deliberate deviations (spec §0.4), each cited where it applies:
//   - static context only (DQ-1, in internal/patch);
//   - language ranking from the PR's own patch bytes (DQ-2, rank.go);
//   - no model registry: the budget comes from tokens.Budget (DQ-3, DQ-4);
//   - every count is tokens.Estimate, factor included (DQ-5);
//   - the v1 large_patch_policy rule (§4.5, compress.go);
//   - tokens.Clip verifies its result and shrinks it, where upstream's
//     clip_tokens returns its first heuristic cut (spec §2.3).
package diffpipe

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"unicode"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Mode selects the render format.
type Mode int

const (
	// ModePlain is upstream's plain mode (pr_ask).
	ModePlain Mode = iota
	// ModeNumbered is upstream's decoupled, line-numbered mode (pr_review).
	ModeNumbered
)

// Skipped-file reasons added by Prepare. Files the provider skipped keep
// their provider reason (provider.Skip*).
const (
	// SkipEmptyDiff marks a file that renders to nothing, for example a pure
	// rename or a mode change without hunks. Upstream drops such a file
	// silently on both paths; here it is accounted for.
	//
	// Decision (lead; architect may override on PR #4): where does a file upstream skips silently go in the
	// §4.6 accounting? — chose Skipped, appended after the provider's
	// entries with this reason, because it was not processed for a
	// non-budget reason (as the provider's skips), listing it under Omitted
	// would claim a budget cut, and Included would claim content in Text.
	SkipEmptyDiff = "empty_diff"
	// SkipUnparseablePatch marks a file whose patch is not a hunk-only
	// unified diff (patch.ParseHunks failed).
	SkipUnparseablePatch = "unparseable_patch"
	// SkipTooLarge marks a file PrepareChunks could not fit into a chunk of
	// its own under large_patch_policy skip (Chunks.TooLarge). Prepare never
	// uses it: for a single call such a file is ErrDoesNotFit.
	SkipTooLarge = "too_large"
)

// Input is one pull request's diff, the render mode and the budget.
type Input struct {
	Files []provider.FilePatch
	// Skipped comes from the provider: filtered, binary, limits,
	// fetch_failed. It is passed through untouched.
	Skipped []provider.SkippedFile
	Mode    Mode
	Budget  tokens.Budget
	Diff    config.Diff
}

// Omitted lists the files left out of the diff body, by change type, in
// upstream's section order (the order of the compressed path's ranking).
type Omitted struct {
	// Added holds added files.
	Added []string
	// Modified holds modified and renamed files (upstream lists
	// EDIT_TYPE.RENAMED under "Additional modified files").
	Modified []string
	// Deleted holds deleted files whose names are not in Text: on the
	// compressed path a deleted file whose patch upstream drops
	// (handle_patch_deletions) when the deleted-files section did not fit
	// or was clipped before its name, plus any deleted file with a patch
	// that was not admitted for budget (listed by name or not). A dropped
	// deletion whose name is in Text is in Prepared.DeletedListed instead
	// (X-20).
	Deleted []string
}

// Prepared is the assembled diff and its coverage accounting.
//
// Accounting invariant (spec §4.6, extended by X-20): every element of
// Input.Files appears in exactly one of Included, Omitted.Added,
// Omitted.Modified, Omitted.Deleted, Clipped, DeletedListed, or Skipped (as
// an entry Prepare appends, with reason SkipEmptyDiff or
// SkipUnparseablePatch); Skipped starts with Input.Skipped, verbatim and in
// order, and no provider-skipped file is counted anywhere else. Hence
//
//	len(Included) + len(Omitted.Added) + len(Omitted.Modified) +
//	len(Omitted.Deleted) + len(Clipped) + len(DeletedListed) + len(Skipped)
//	== len(Input.Files) + len(Input.Skipped).
//
// Included and Clipped are exactly the files whose content is in Text, and
// DeletedListed the deleted files whose name is in Text in place of their
// content. Omitted is the full list of the other files whose content is not
// in Text; the sections at the end of Text name them only when the budget
// allows it (upstream behaviour), so Omitted is the source of truth for
// coverage.
type Prepared struct {
	// Text is the diff the prompt embeds.
	Text string
	// FastPath reports that the full extended diff fit (nothing omitted).
	FastPath bool
	// Tokens is tokens.Estimate(Text, Budget.Factor).
	Tokens int
	// Included holds the paths whose full render is in Text, in output
	// order.
	Included []string
	// Omitted holds the paths dropped from the diff body.
	Omitted Omitted
	// Clipped holds the paths included in clipped form (§4.5).
	Clipped []string
	// DeletedListed holds the deleted files whose patch the compressed path
	// drops by design (handle_patch_deletions) and whose names are in
	// Text's deleted-files section, in that section's order. The model was
	// shown the deletion, so they count as reviewed (X-20). Always empty
	// on the fast path, which renders deletions in full.
	DeletedListed []string
	// Skipped holds Input.Skipped followed by the files Prepare could not
	// render (SkipEmptyDiff, SkipUnparseablePatch), in input order.
	Skipped []provider.SkippedFile
}

// Prepare assembles the diff for one call. It returns an error wrapping
// tokens.ErrDoesNotFit when the budget has no room for diff content
// (Budget.RequireCapacity) or when no file fits and large_patch_policy
// cannot help (§4.5).
func Prepare(in Input) (*Prepared, error) {
	return prepare(in, estimateCounter(in.Budget.Factor))
}

// estimateCounter is the counter Prepare and PrepareChunks use:
// tokens.Estimate with the budget's factor.
func estimateCounter(factor float64) *counter {
	return newCounter(factor, func(s string) int { return tokens.Estimate(s, factor) })
}

// tooLargeError is the ErrDoesNotFit of the compressed path when no file was
// admitted and large_patch_policy produced no clipped file: it names the
// top-ranked file the policy was applied to, so PrepareChunks can set that
// file aside (SkipTooLarge). Error and Unwrap are those of err, so Prepare's
// callers see the same error as before.
type tooLargeError struct {
	path string
	err  error
}

func (e *tooLargeError) Error() string { return e.err.Error() }
func (e *tooLargeError) Unwrap() error { return e.err }

// file is one parsed input file.
type file struct {
	fp    *provider.FilePatch
	hunks []patch.Hunk
	lang  string
	// fast is the fast-path render and fastTokens its estimate (computed
	// only when the compressed path runs; upstream's file.tokens).
	fast       string
	fastTokens int
	// skip is the reason the file is not rendered, if any.
	skip string
}

func prepare(in Input, c *counter) (*Prepared, error) {
	return prepareRanked(in, c, nil)
}

// prepareRanked is prepare with the language groups in langOrder when it is
// not nil (PrepareChunks passes the order of the whole pull request, so a
// later chunk keeps the original rank order; see orderGroups).
func prepareRanked(in Input, c *counter, langOrder []string) (*Prepared, error) {
	if in.Mode != ModePlain && in.Mode != ModeNumbered {
		return nil, fmt.Errorf("diffpipe: unknown mode %d", in.Mode)
	}
	if err := in.Budget.RequireCapacity(); err != nil {
		return nil, fmt.Errorf("diffpipe: soft limit %d leaves no room for diff content: %w",
			in.Budget.SoftLimit(), err)
	}

	files := make([]*file, len(in.Files))
	for i := range in.Files {
		f := &file{fp: &in.Files[i]}
		hunks, err := patch.ParseHunks(f.fp.Patch)
		if err != nil {
			// Decision (lead; architect may override on PR #4): what to do with a patch ParseHunks rejects
			// (upstream has no parser and would feed the string through)?
			// — chose to account for it in Skipped with reason
			// unparseable_patch because rendering it would bypass the hunk
			// model (DQ-10) and dropping it silently breaks §4.6.
			f.skip = SkipUnparseablePatch
		}
		f.hunks = hunks
		files[i] = f
	}
	groups := rank(files)
	if langOrder != nil {
		orderGroups(groups, langOrder)
	}

	p, err := assemble(in, c, groups)
	if err != nil {
		return nil, err
	}
	p.Skipped = append([]provider.SkippedFile(nil), in.Skipped...)
	for _, f := range files {
		if f.skip != "" {
			p.Skipped = append(p.Skipped, provider.SkippedFile{Path: f.fp.Path, Reason: f.skip})
		}
	}
	p.Tokens = c.count(p.Text)
	return p, nil
}

// assemble runs the fast path and, when it does not fit, the compressed
// path (get_pr_diff).
func assemble(in Input, c *counter, groups []group) (*Prepared, error) {
	numbered := in.Mode == ModeNumbered
	var parts, paths []string
	var rendered []*file
	for _, g := range groups {
		for _, f := range g.files {
			if f.skip != "" {
				continue
			}
			ext := patch.ExtendFile(*f.fp, f.hunks, in.Diff)
			pf := patch.NewFile(*f.fp, ext)
			if numbered {
				f.fast = patch.RenderDecoupled(pf, true)
			} else {
				f.fast = patch.RenderPlain(pf)
			}
			if f.fast == "" {
				// pr_generate_extended_diff skips a file without a patch.
				f.skip = SkipEmptyDiff
				continue
			}
			parts = append(parts, f.fast)
			paths = append(paths, f.fp.Path)
			rendered = append(rendered, f)
		}
	}

	// Fit test: upstream compares max(count(joined), count(joined.strip()))
	// strictly below the soft budget. An empty diff counts 0.
	joined := strings.Join(parts, "\n")
	if c.fitsStrictly(joined, in.Budget.SoftLimit()) {
		// Decision (lead; architect may override on PR #4): an empty fast-path diff (every file skipped or
		// empty) — return ErrDoesNotFit or an empty Text? — chose an empty
		// Text without error because nothing was dropped for budget and
		// ErrDoesNotFit would tell the user to raise the context window.
		return &Prepared{Text: joined, FastPath: true, Included: paths}, nil
	}

	// upstream file.tokens: the estimate of each fast-path render, the
	// ranking key of the compressed path.
	texts := make([]string, len(rendered))
	for i, f := range rendered {
		texts[i] = f.fast
	}
	for i, n := range c.countAll(texts) {
		rendered[i].fastTokens = n
	}
	return compress(in, c, groups)
}

// counter memoizes the estimator for one Prepare call. The same strings are
// counted more than once (the exact recount and the section budget start
// from the same body), and BPE counting dominates the cost.
type counter struct {
	// factor is the estimate factor, for tokens.Clip.
	factor float64
	est    func(string) int
	mu     sync.Mutex
	memo   map[string]int
}

func newCounter(factor float64, est func(string) int) *counter {
	return &counter{factor: factor, est: est, memo: map[string]int{}}
}

// count returns the estimate of s.
func (c *counter) count(s string) int {
	if s == "" {
		return 0
	}
	c.mu.Lock()
	n, ok := c.memo[s]
	c.mu.Unlock()
	if ok {
		return n
	}
	n = c.est(s)
	c.mu.Lock()
	c.memo[s] = n
	c.mu.Unlock()
	return n
}

// countAll counts texts concurrently; results are in input order.
func (c *counter) countAll(texts []string) []int {
	out := make([]int, len(texts))
	workers := min(runtime.GOMAXPROCS(0), len(texts))
	var wg sync.WaitGroup
	next := make(chan int)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				out[i] = c.count(texts[i])
			}
		}()
	}
	for i := range texts {
		next <- i
	}
	close(next)
	wg.Wait()
	return out
}

// rawAndStripped is upstream's _count_raw_and_stripped_tokens: the larger of
// the counts of s and of s with surrounding whitespace removed.
func (c *counter) rawAndStripped(s string) int {
	n := c.count(s)
	if st := pyStrip(s); st != s {
		n = max(n, c.count(st))
	}
	return n
}

// fitsStrictly reports rawAndStripped(s) < limit, counting the stripped
// form only when the raw form fits.
func (c *counter) fitsStrictly(s string, limit int) bool {
	if c.count(s) >= limit {
		return false
	}
	return c.count(pyStrip(s)) < limit
}

// fitsWithin reports rawAndStripped(s) <= limit, counting the stripped
// form only when the raw form fits.
func (c *counter) fitsWithin(s string, limit int) bool {
	if c.count(s) > limit {
		return false
	}
	return c.count(pyStrip(s)) <= limit
}

// pyStrip mirrors Python's str.strip() without arguments (Python also
// treats \x1c..\x1f as whitespace).
func pyStrip(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
	})
}
