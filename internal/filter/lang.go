package filter

import (
	"strings"
	"sync"

	"github.com/nevzatcirak/review-mcp/internal/filter/data"
)

// OtherLanguage is returned when no language matches.
const OtherLanguage = "Other"

type langMatcher struct {
	exact    map[string]string
	folded   map[string]map[string]struct{}
	maxDepth int
}

var (
	matcherOnce sync.Once
	matcher     *langMatcher
)

func buildMatcher() *langMatcher {
	m := &langMatcher{exact: map[string]string{}, folded: map[string]map[string]struct{}{}}
	for _, l := range data.LanguageExtensions {
		for _, e := range l.Extensions {
			tok := strings.TrimLeft(e, "*")
			if _, ok := m.exact[tok]; !ok { // first language in table order wins
				m.exact[tok] = l.Name
			}
			k := strings.ToLower(tok)
			if m.folded[k] == nil {
				m.folded[k] = map[string]struct{}{}
			}
			m.folded[k][l.Name] = struct{}{}
			if strings.HasPrefix(tok, ".") {
				if d := strings.Count(tok, "."); d > m.maxDepth {
					m.maxDepth = d
				}
			}
		}
	}
	return m
}

// Language classifies a file path by language using the embedded table. It
// tries the whole basename, then dotted suffixes from the longest (bounded by
// the deepest multi-dot token in the table) down to the shortest. For each
// candidate an exact match wins, otherwise a case-folded match is accepted
// only when it identifies a single language. No match returns "Other".
func Language(p string) string {
	matcherOnce.Do(func() { matcher = buildMatcher() })
	name := strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	parts := strings.Split(name, ".")
	cands := []string{name}
	start := len(parts) - matcher.maxDepth
	if start < 1 {
		start = 1
	}
	for i := start; i < len(parts); i++ {
		cands = append(cands, "."+strings.Join(parts[i:], "."))
	}
	for _, c := range cands {
		if lang, ok := matcher.exact[c]; ok {
			return lang
		}
		if set := matcher.folded[strings.ToLower(c)]; len(set) == 1 {
			for lang := range set {
				return lang
			}
		}
	}
	return OtherLanguage
}
