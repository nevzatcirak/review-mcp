package diffpipe

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

const (
	// separator joins the per-file renders (upstream "\n".join(patches)).
	separator = "\n"
	// sectionSeparator precedes each omitted-file section
	// (_append_metadata_section).
	sectionSeparator = "\n\n"
	// sectionHeadroom is upstream's delta_tokens in get_pr_diff: the
	// omitted-file sections are built only when more than this many tokens
	// remain below the hard limit.
	sectionHeadroom = 10
)

// entry is one file_dict entry of the compressed path: a file with its
// compressed render and that render's estimate.
type entry struct {
	f      *file
	text   string
	tokens int
}

// compress is upstream's compressed path (spec §4.4; porting map §A step 8)
// followed by the v1 large_patch_policy rule (§4.5). It runs only after the
// fast path did not fit, so every non-skipped file has its fast render
// estimate in fastTokens.
func compress(in Input, c *counter, groups []group) (*Prepared, error) {
	numbered := in.Mode == ModeNumbered
	soft, hard := in.Budget.SoftLimit(), in.Budget.HardLimit()

	// Rank: within each group, by the fast-path estimate descending
	// (upstream sorts by file.tokens, which pr_generate_extended_diff set
	// from the extended render), keeping the provider order on ties (a
	// stable sort, as Python's sorted(reverse=True) is). Group order stays.
	//
	// Decision (lead; architect may override on PR #4): spec §4.4 lists "render and estimate each file" before
	// "sort by token count", which reads as the compressed render's count;
	// upstream sorts by the fast-path (extended) render's count — chose
	// upstream's key because §4.4 is "upstream-exact" and the oracle
	// distinguishes the two (sorting by the compressed count fails 6 of the
	// committed goldens and 303 of 800 randomized cases).
	var sorted []*file
	for _, g := range groups {
		fs := slices.Clone(g.files)
		slices.SortStableFunc(fs, func(a, b *file) int { return b.fastTokens - a.fastTokens })
		sorted = append(sorted, fs...)
	}

	// Build file_dict: no extension, deletion handling (spec §3.3), the
	// compressed render.
	var entries []entry
	var deletedNames []string // upstream deleted_files_list
	for _, f := range sorted {
		if f.skip != "" {
			continue
		}
		hunks := f.hunks
		if f.fp.HeadStatus != provider.ContentFetchFailed {
			kept, deleted := patch.HandleDeletions(f.fp.Type, f.fp.HeadContent, f.hunks)
			if deleted {
				deletedNames = append(deletedNames, f.fp.Path)
				continue
			}
			hunks = kept
		}
		text := patch.RenderCompressed(patch.NewFile(*f.fp, hunks), numbered)
		if text == "" {
			f.skip = SkipEmptyDiff
			continue
		}
		entries = append(entries, entry{f: f, text: text})
	}
	texts := make([]string, len(entries))
	for i := range entries {
		texts[i] = entries[i].text
	}
	for i, n := range c.countAll(texts) {
		entries[i].tokens = n
	}

	admitted := admit(c, entries, soft, hard)

	body := make([]string, len(admitted))
	inBody := make([]bool, len(entries))
	for i, k := range admitted {
		body[i] = entries[k].text
		inBody[k] = true
	}
	p := &Prepared{}
	for _, k := range admitted {
		p.Included = append(p.Included, entries[k].f.fp.Path)
	}

	// spec §4.5: the v1 large_patch_policy rule (a deliberate deviation:
	// upstream applies the policy only when packing several calls, and its
	// single-call path returns just the omitted-file sections here).
	//
	// Decision (lead; architect may override on PR #4): §4.5 says ErrDoesNotFit when "the diff text is still
	// empty", but the omitted-file sections would make the text non-empty
	// even when no file was admitted (and the §4.5 canary requires
	// ErrDoesNotFit under skip) — chose: when no file is admitted while a
	// non-deleted file exists and the policy yields no clipped file, return
	// ErrDoesNotFit; a PR whose only remaining entries are deleted files
	// gets the sections (that is all upstream shows for deleted files on
	// this path), and an empty final text is ErrDoesNotFit too.
	if len(admitted) == 0 {
		top := -1
		for k, e := range entries {
			if e.f.fp.Type != provider.ChangeDeleted {
				top = k
				break
			}
		}
		if top >= 0 {
			policy := in.Diff.LargePatchPolicy
			clipped := ""
			// Upstream's own fallback for an unset policy is "skip"
			// (config.get("large_patch_policy", "skip") != "clip").
			if policy == "clip" {
				clipped = clipToFit(c, entries[top].text, soft, in.Budget.Factor)
			}
			if clipped == "" {
				return nil, fmt.Errorf("diffpipe: no file fits the diff budget (soft limit %d, large_patch_policy %q): %w",
					soft, policy, tokens.ErrDoesNotFit)
			}
			body = []string{clipped}
			inBody[top] = true
			p.Clipped = []string{entries[top].f.fp.Path}
		}
	}

	// Coverage: every entry not in the body, by change type, in file_dict
	// order; the deleted files dropped by handle_patch_deletions come first
	// in the deleted list, as in upstream's section.
	//
	// Decision (lead; architect may override on PR #4): a deleted file on the compressed path has its patch
	// dropped by design (handle_patch_deletions) and is shown only by name
	// under "Deleted files:"; is it Included or Omitted? — chose
	// Omitted.Deleted because its content is not in the text (Included means
	// "content in Text"), it is listed exactly where upstream lists it, and
	// P4's coverage section (X-3) then reports it as deleted, whether or not
	// the budget left room for the section.
	p.Omitted.Deleted = append(p.Omitted.Deleted, deletedNames...)
	for k, e := range entries {
		if inBody[k] {
			continue
		}
		switch e.f.fp.Type {
		case provider.ChangeAdded:
			p.Omitted.Added = append(p.Omitted.Added, e.f.fp.Path)
		case provider.ChangeDeleted:
			p.Omitted.Deleted = append(p.Omitted.Deleted, e.f.fp.Path)
		default:
			// Decision (lead; architect may override on PR #4): upstream lists EDIT_TYPE.UNKNOWN in no
			// section; a provider.ChangeType outside the four constants
			// would be such a file — chose to treat it as modified (listed
			// and accounted) because silently dropping it hides a file
			// from the model and breaks §4.6.
			p.Omitted.Modified = append(p.Omitted.Modified, e.f.fp.Path)
		}
	}

	p.Text = appendSections(c, strings.Join(body, separator), hard, p.Omitted)
	if p.Text == "" {
		return nil, fmt.Errorf("diffpipe: nothing fits the diff budget (hard limit %d): %w", hard, tokens.ErrDoesNotFit)
	}
	return p, nil
}

// admit is upstream's generate_full_patch for one call: it returns the
// indexes of the admitted entries, in order.
//
//   - Once the running total exceeds the hard limit, every further file is
//     skipped outright ("File was fully skipped, no more tokens").
//   - Otherwise a file is admitted iff running + tokens + separator <= the
//     soft limit; the separator's estimate is charged for every file after
//     the first admitted one. Rejected files are kept whole (no clipping).
//   - The admitted renders are then joined and counted exactly (raw and
//     stripped); when that exceeds the soft limit, the longest verified
//     fitting prefix is kept. Counts are never assumed additive.
func admit(c *counter, entries []entry, soft, hard int) []int {
	running := 0
	sep := -1
	var admitted []int
	for k, e := range entries {
		if running > hard {
			continue
		}
		cost := e.tokens
		if len(admitted) > 0 {
			if sep < 0 {
				sep = c.count(separator)
			}
			cost += sep
		}
		if running+cost > soft {
			continue
		}
		admitted = append(admitted, k)
		running += cost
	}
	if len(admitted) == 0 {
		return nil
	}
	fits := func(n int) bool {
		parts := make([]string, n)
		for i, k := range admitted[:n] {
			parts[i] = entries[k].text
		}
		return c.fitsWithin(strings.Join(parts, separator), soft)
	}
	if fits(len(admitted)) {
		return admitted
	}
	return admitted[:verifiedPrefix(len(admitted)-1, fits)]
}

// verifiedPrefix is upstream's _find_verified_fitting_prefix_length: a
// binary search over prefix lengths 0..maxLen in which every accepted length
// was verified by fits directly. Fits is not assumed monotone, so a longer
// fitting prefix may exist; the result always fits (or is 0).
func verifiedPrefix(maxLen int, fits func(n int) bool) int {
	low, high := 0, maxLen
	for low < high {
		mid := (low + high + 1) / 2
		if fits(mid) {
			low = mid
		} else {
			high = mid - 1
		}
	}
	if low == 0 && maxLen > 0 && fits(1) {
		return 1
	}
	return low
}

// clipToFit clips one compressed render to the soft limit for the clip
// policy (§4.5): tokens.Clip with deleteLastLine and the truncation marker,
// verified against the body's own fit rule (raw and stripped counts), with
// the clip budget lowered in 10% steps if the stripped count is the larger.
//
// Decision (lead; architect may override on PR #4): §4.5 only asks for a non-empty clip; should a clip that
// keeps nothing but whitespace and the marker count? — chose to reject it
// (then the result is ErrDoesNotFit) because it would show the model no
// file content at all, not even the file name.
func clipToFit(c *counter, text string, soft int, factor float64) string {
	for limit := soft; limit > 0; limit -= max(1, limit/10) {
		clipped := tokens.Clip(text, limit, factor, true)
		if strings.TrimSpace(strings.TrimSuffix(clipped, tokens.TruncationMarker)) == "" {
			return ""
		}
		if c.fitsWithin(clipped, soft) {
			return clipped
		}
	}
	return ""
}

// appendSections is the omitted-file part of get_pr_diff: when more than
// sectionHeadroom tokens remain below the hard limit, it builds the added,
// modified and deleted sections (in that order) and appends each one, via
// appendSection, while it still fits.
//
// Upstream's test is "remaining > delta_tokens" (strictly more than 10),
// reproduced as such; spec §4.4.6 paraphrases it as "at least 10". With one
// call the hard limit is always 500 above the soft one, so the test cannot
// fail in v1; it is kept for parity.
func appendSections(c *counter, diff string, hard int, o Omitted) string {
	cur := c.count(diff)
	if hard-cur <= sectionHeadroom {
		return diff
	}
	for _, s := range []struct {
		header string
		names  []string
	}{
		{patch.AddedFilesHeader, o.Added},
		{patch.ModifiedFilesHeader, o.Modified},
		{patch.DeletedFilesHeader, o.Deleted},
	} {
		if len(s.names) == 0 {
			continue
		}
		// Upstream's format: the header (which ends in "\n"), then "\n"
		// before every name, so the names follow one blank line.
		section := s.header + "\n" + strings.Join(s.names, "\n")
		diff, cur = appendSection(c, diff, cur, section, hard)
	}
	return diff
}

// appendSection is upstream's _append_metadata_section: the section is
// clipped to the room left below the hard limit (minus the separator) and
// appended only when the exact recount of the whole text (raw and
// stripped) still fits. The running count becomes that recount.
func appendSection(c *counter, diff string, cur int, section string, hard int) (string, int) {
	budget := hard - cur - c.count(sectionSeparator)
	if budget <= 0 {
		return diff, cur
	}
	// Decision (lead; architect may override on PR #4): upstream's clip_tokens returns its first heuristic
	// cut unverified, so a clipped section may estimate above budget and
	// still be appended when the whole-text recount fits (counts are not
	// additive); tokens.Clip verifies and shrinks the cut, giving a shorter
	// section in that case (1 committed golden, 0 of 800 randomized cases) —
	// chose tokens.Clip because spec §4.4.6 and §2.3 require it and it never
	// overshoots; the whole-text recount below is upstream's either way.
	clipped := tokens.Clip(section, budget, c.factor, false)
	if clipped == "" {
		return diff, cur
	}
	candidate := diff + sectionSeparator + clipped
	if c.fitsWithin(candidate, hard) {
		return candidate, c.rawAndStripped(candidate)
	}
	return diff, cur
}
