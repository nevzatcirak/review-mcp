package diffpipe

import (
	"slices"

	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// MainLanguage returns the language of the first group of the DQ-2 ranking
// of files (filter.OtherLanguage when that is all there is), or "" when
// files is empty. pr_ask names it in its prompt (spec P5 §1.1); it is the
// same ranking Prepare orders the diff by.
func MainLanguage(files []provider.FilePatch) string {
	fs := make([]*file, len(files))
	for i := range files {
		fs[i] = &file{fp: &files[i]}
	}
	groups := rank(fs)
	if len(groups) == 0 {
		return ""
	}
	return groups[0].lang
}

// group is one language group of the ranking, its files in provider order.
type group struct {
	lang   string
	weight int
	files  []*file
	// pinned marks the group of Input.Pinned files (pinFirst): it comes
	// first and the compressed path keeps its order instead of sorting it
	// by size.
	pinned bool
}

// pinFirst moves the files whose path is in pinned out of their language
// groups into one leading pinned group, in input order (files is the input
// order). Groups left empty are dropped; the other groups and their files
// keep their order. Paths of pinned that no file has are ignored.
func pinFirst(groups []group, files []*file, pinned []string) []group {
	want := make(map[string]bool, len(pinned))
	for _, p := range pinned {
		want[p] = true
	}
	head := group{pinned: true}
	for _, f := range files {
		if want[f.fp.Path] {
			head.files = append(head.files, f)
		}
	}
	if len(head.files) == 0 {
		return groups
	}
	out := []group{head}
	for _, g := range groups {
		g.files = slices.DeleteFunc(slices.Clone(g.files), func(f *file) bool { return want[f.fp.Path] })
		if len(g.files) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// rank groups files by language (spec §4.2).
//
// Deliberate deviation (DQ-2): upstream's sort_files_by_main_languages
// orders the groups by the repository's language sizes from the provider
// API. Here a group's weight is the sum of its files' patch byte lengths,
// computed from the pull request itself, so every provider ranks the same
// way without an extra call. Groups are sorted by weight descending, ties by
// language name; "Other" (no language matched) always comes last, as
// upstream's catch-all bucket does. Within a group the provider's order is
// kept. The result never depends on map iteration order.
func rank(files []*file) []group {
	index := map[string]int{}
	var groups []group
	for _, f := range files {
		f.lang = filter.Language(f.fp.Path)
		i, ok := index[f.lang]
		if !ok {
			i = len(groups)
			index[f.lang] = i
			groups = append(groups, group{lang: f.lang})
		}
		groups[i].weight += len(f.fp.Patch)
		groups[i].files = append(groups[i].files, f)
	}
	slices.SortStableFunc(groups, func(a, b group) int {
		if ao, bo := a.lang == filter.OtherLanguage, b.lang == filter.OtherLanguage; ao != bo {
			if ao {
				return 1
			}
			return -1
		}
		if a.weight != b.weight {
			return b.weight - a.weight
		}
		switch {
		case a.lang < b.lang:
			return -1
		case a.lang > b.lang:
			return 1
		}
		return 0
	})
	return groups
}

// rankOrder returns the language order of the DQ-2 ranking of files.
func rankOrder(files []provider.FilePatch) []string {
	fs := make([]*file, len(files))
	for i := range files {
		fs[i] = &file{fp: &files[i]}
	}
	groups := rank(fs)
	order := make([]string, len(groups))
	for i, g := range groups {
		order[i] = g.lang
	}
	return order
}

// orderGroups sorts groups into the language order given (the ranking of a
// larger file set, see PrepareChunks). A subset of the files can weigh
// differently from the whole, so rank alone could reorder the groups of a
// later chunk. A language missing from order (not expected) goes last, in
// rank order.
func orderGroups(groups []group, order []string) {
	pos := make(map[string]int, len(order))
	for i, lang := range order {
		pos[lang] = i
	}
	at := func(lang string) int {
		if i, ok := pos[lang]; ok {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(groups, func(a, b group) int { return at(a.lang) - at(b.lang) })
}
