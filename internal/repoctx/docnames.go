package repoctx

import (
	"regexp"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Bounds of the names a documentation change mentions (v2.1 spec V-5 §3a
// item 3).
const (
	// MaxDocNames is the most distinct names taken from the documentation
	// of one diff.
	MaxDocNames = 30
	// MinDocNameBytes and MaxDocNameBytes bound one name.
	MinDocNameBytes = 2
	MaxDocNameBytes = 100
)

// docNameRE accepts an identifier, or a dotted, dashed or slashed name
// (llm.max_rate_wait_seconds, --publish, internal/llm): an optional leading
// "-" or "--", then word segments joined by single ".", "-" or "/".
var docNameRE = regexp.MustCompile(`^(?:--?)?[A-Za-z_][A-Za-z0-9_]*(?:[./-][A-Za-z0-9_]+)*$`)

// DocNames returns the names that the changed documentation lines of files
// mention as inline code (`name` in Markdown, “name“ in reStructuredText):
// only added lines of documentation files (filter.IsDocumentation) are
// read, so unchanged and removed text never contributes. A span is kept
// when it is MinDocNameBytes to MaxDocNameBytes long and docNameRE accepts
// it; at most MaxDocNames distinct names, in the order they appear.
func DocNames(files []provider.FilePatch) []string {
	var out []string
	seen := map[string]bool{}
	for _, fp := range files {
		if fp.Binary || fp.Patch == "" || !filter.IsDocumentation(fp.Path) {
			continue
		}
		hunks, err := patch.ParseHunks(fp.Patch)
		if err != nil {
			continue
		}
		for _, h := range hunks {
			if h.Malformed() {
				continue
			}
			for _, l := range h.Lines {
				if l.Op != '+' {
					continue
				}
				for _, name := range codeSpans(strings.TrimRight(l.Text, "\r\n")) {
					if seen[name] || len(name) < MinDocNameBytes || len(name) > MaxDocNameBytes || !docNameRE.MatchString(name) {
						continue
					}
					seen[name] = true
					out = append(out, name)
					if len(out) == MaxDocNames {
						return out
					}
				}
			}
		}
	}
	return out
}

// codeSpans returns the inline code spans of one line: the text between a
// run of n backticks and the next run of exactly n, trimmed. A run without
// a matching closer opens nothing.
func codeSpans(line string) []string {
	var out []string
	for i := 0; i < len(line); {
		if line[i] != '`' {
			i++
			continue
		}
		open := runEnd(line, i)
		n := open - i
		closeAt := -1
		for k := open; k < len(line); {
			if line[k] != '`' {
				k++
				continue
			}
			e := runEnd(line, k)
			if e-k == n {
				closeAt = k
				break
			}
			k = e
		}
		if closeAt < 0 {
			i = open
			continue
		}
		out = append(out, strings.TrimSpace(line[open:closeAt]))
		i = closeAt + n
	}
	return out
}

// runEnd is the index after the run of backticks that starts at i.
func runEnd(s string, i int) int {
	for i < len(s) && s[i] == '`' {
		i++
	}
	return i
}

// docSymbols are the names of DocNames as symbols. They have no source
// file (Path ""), so that the search prefers uses in code to uses in other
// documentation (gitctx's hit choice).
func docSymbols(files []provider.FilePatch) []gitctx.Symbol {
	names := DocNames(files)
	out := make([]gitctx.Symbol, len(names))
	for i, n := range names {
		out[i] = gitctx.Symbol{Name: n, Rank: gitctx.RankChanged}
	}
	return out
}
