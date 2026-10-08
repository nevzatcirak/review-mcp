package filter

import (
	"slices"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/filter/data"
)

// ExcludePathspecs returns git pathspecs (":(exclude,glob)...") for the
// path-shaped rules of the filter: lockfile names, minified suffixes, bad
// extensions (case-insensitive), the generated-code globs of the configured
// frameworks and ignore.glob. A search can pass them to git so that git does
// not even read those files.
//
// They are an optimisation, not the rule: git's glob syntax has no braces, and
// ignore.regex cannot be expressed as a pathspec at all, so a rule that does
// not translate is left out and Include remains the authority. A caller must
// still apply Include to every path git returns.
//
// A pattern without "/" matches the basename at any depth ("**/" prefix, the
// same as matchGlob); a pattern with "/" is anchored at the repository root.
func (f *Filter) ExcludePathspecs() []string {
	var out []string
	add := func(magic, pattern string) {
		out = append(out, ":(exclude,"+magic+")"+pattern)
	}
	for name := range f.lockfiles {
		add("glob", "**/"+escapeGlob(name))
	}
	for _, s := range data.MinifiedSuffixes {
		add("glob,icase", "**/*"+escapeGlob(s))
	}
	for ext := range f.badExt {
		add("glob,icase", "**/*."+escapeGlob(ext))
	}
	translate := func(globs []string) {
		for _, g := range globs {
			if strings.ContainsAny(g, "{}") || g == "" {
				continue // doublestar braces: no equivalent in a git pathspec
			}
			if !strings.Contains(g, "/") {
				g = "**/" + g
			}
			add("glob", g)
		}
	}
	for _, gs := range f.generated {
		translate(gs.globs)
	}
	translate(f.globs)
	return sortedUnique(out)
}

// escapeGlob protects glob metacharacters in a literal name.
func escapeGlob(s string) string {
	if !strings.ContainsAny(s, `*?[\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(`*?[\`, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func sortedUnique(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}
