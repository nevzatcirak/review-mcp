package diffpipe

import (
	"slices"

	"github.com/nevzatcirak/review-mcp/internal/filter"
)

// group is one language group of the ranking, its files in provider order.
type group struct {
	lang   string
	weight int
	files  []*file
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
