package related

import (
	"path"
	"regexp"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/related/data"
)

// The changed contract surface of a primary diff (v2.5 spec V-13b §2.1):
// the names a consumer in another repository may use that the pull request
// removes, renames or changes. It is derived by fixed token rules from the
// changed lines of the contract files and from the changed definitions of
// every file, never by parsing an API description (backlog). A related
// repository is searched for these names, and only for them.

// IdentifierKind is the kind of a name of the surface. The kinds are listed
// in the order Surface puts them.
type IdentifierKind string

const (
	// KindGone: a token of a removed line of a contract file that no added
	// line of the same file carries (a removed or renamed field, operation or
	// message).
	KindGone IdentifierKind = "gone"
	// KindRoute: the static prefix of a path key on a removed line of an API
	// description in YAML or JSON. It is searched without word boundaries.
	KindRoute IdentifierKind = "route"
	// KindChanged: a token of a removed line and of an added line of the same
	// hunk of a contract file (a field whose type, requiredness or format
	// changed).
	KindChanged IdentifierKind = "changed"
	// KindSymbol: an exported symbol whose definition line was removed or
	// changed (gitctx.ExtractSymbols).
	KindSymbol IdentifierKind = "symbol"
)

// Identifier is one name of the changed contract surface.
type Identifier struct {
	// Text is the name, or for a route its static prefix ("/users").
	Text string
	Kind IdentifierKind
}

// Word reports whether the identifier is searched as a whole word: every
// kind but a route.
func (id Identifier) Word() bool { return id.Kind != KindRoute }

// tokenRE is a token of a changed line (spec §2.1 item 2), matched against a
// whole run of word characters: a run that starts with a digit or is longer
// than 100 characters is no token.
var (
	wordRunRE = regexp.MustCompile(`[A-Za-z0-9_]+`)
	tokenRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{2,99}$`)
	// routeKeyRE is a line whose key is a path (spec §2.1 item 3).
	routeKeyRE = regexp.MustCompile(`^\s*["']?(/[^"'\s:]*)["']?\s*:`)
)

// minRouteSegment is the shortest static segment that makes a route prefix
// worth a search: "/v1" alone matches everything.
const minRouteSegment = 3

// stopWords is the stop list of internal/related/data, lower case.
var stopWords = func() map[string]bool {
	m := map[string]bool{}
	for _, list := range [][]string{data.SchemaKeywords, data.TypeNames, data.CommonWords} {
		for _, w := range list {
			m[strings.ToLower(w)] = true
		}
	}
	return m
}()

// Stopped reports whether a token is on the stop list (compared in lower
// case).
func Stopped(token string) bool { return stopWords[strings.ToLower(token)] }

// maxSymbols bounds the symbols ExtractSymbols returns for the surface: the
// cap of the surface applies after them, so this only bounds the work.
const maxSymbols = 1000

// Surface returns the changed contract surface of files (the primary diff
// after filtering; an incremental run passes the files it reviews), at most
// max identifiers (config.DefaultRelatedIdentifiers when max <= 0). flt
// names the contract files (filter.Filter.ContractTier, tier 1 and 2, with
// related.contract_globs); nil uses the fixed table alone. It is pure.
//
// The order is: gone (in diff order), then routes, then changed, then the
// changed exported symbols (in gitctx's rank order); a name is kept once,
// with its first kind. Derive also returns how many the cap dropped.
func Surface(files []provider.FilePatch, flt *filter.Filter, max int) []Identifier {
	ids, _ := Derive(files, flt, max)
	return ids
}

// Derive is Surface with the number of identifiers the cap dropped (the
// note of spec §2.4 item 3).
func Derive(files []provider.FilePatch, flt *filter.Filter, max int) ([]Identifier, int) {
	if max <= 0 {
		max = config.DefaultRelatedIdentifiers
	}
	all := surfaceAll(files, flt)
	if len(all) <= max {
		return all, 0
	}
	return all[:max], len(all) - max
}

// surfaceAll is the whole surface, in order, without a cap.
func surfaceAll(files []provider.FilePatch, flt *filter.Filter) []Identifier {
	var gone, routes, changed []string
	for _, fp := range files {
		if fp.Binary || fp.Patch == "" {
			continue
		}
		tier := flt.ContractTier(fp.Path)
		if tier != filter.TierDescription && tier != filter.TierSurface {
			continue
		}
		hunks, err := patch.ParseHunks(fp.Patch)
		if err != nil {
			continue
		}
		g, c := contractTokens(hunks)
		gone, changed = append(gone, g...), append(changed, c...)
		if tier == filter.TierDescription && structuredData(fp.Path) {
			routes = append(routes, routePrefixes(hunks)...)
		}
	}
	var symbols []string
	for _, s := range gitctx.ExtractSymbols(files, maxSymbols) {
		if (s.Rank == gitctx.RankRemoved || s.Rank == gitctx.RankSignature) && !s.Test {
			symbols = append(symbols, s.Name)
		}
	}

	var out []Identifier
	seen := map[string]bool{}
	add := func(kind IdentifierKind, names []string, stop bool) {
		for _, n := range names {
			if seen[n] || (stop && Stopped(n)) {
				continue
			}
			seen[n] = true
			out = append(out, Identifier{Text: n, Kind: kind})
		}
	}
	add(KindGone, gone, true)
	add(KindRoute, routes, false)
	add(KindChanged, changed, true)
	add(KindSymbol, symbols, true)
	return out
}

// contractTokens returns the gone and changed tokens of one contract file's
// hunks, each in diff order: a token of a removed line is gone when no added
// line of the file carries it, and changed when an added line of the same
// hunk does. A token whose only added line is in another hunk (a moved
// line) is neither: the name still exists.
func contractTokens(hunks []patch.Hunk) (gone, changed []string) {
	addedInFile := map[string]bool{}
	addedInHunk := make([]map[string]bool, len(hunks))
	for i, h := range hunks {
		addedInHunk[i] = map[string]bool{}
		if h.Malformed() {
			continue
		}
		for _, l := range h.Lines {
			if l.Op != '+' {
				continue
			}
			for _, t := range lineTokens(l.Text) {
				addedInFile[t], addedInHunk[i][t] = true, true
			}
		}
	}
	for i, h := range hunks {
		if h.Malformed() {
			continue
		}
		for _, l := range h.Lines {
			if l.Op != '-' {
				continue
			}
			for _, t := range lineTokens(l.Text) {
				switch {
				case !addedInFile[t]:
					gone = append(gone, t)
				case addedInHunk[i][t]:
					changed = append(changed, t)
				}
			}
		}
	}
	return gone, changed
}

// lineTokens returns the tokens of a line, in order.
func lineTokens(line string) []string {
	var out []string
	for _, run := range wordRunRE.FindAllString(line, -1) {
		if tokenRE.MatchString(run) {
			out = append(out, run)
		}
	}
	return out
}

// structuredData reports whether an API description is YAML or JSON, by its
// extension.
func structuredData(p string) bool {
	switch strings.ToLower(path.Ext(strings.ReplaceAll(p, "\\", "/"))) {
	case ".yaml", ".yml", ".json":
		return true
	}
	return false
}

// routePrefixes returns the static prefixes of the path keys of the removed
// lines of hunks, in diff order (spec §2.1 item 3): the segments before the
// first "{…}" segment, kept only when one of them is a static segment of at
// least minRouteSegment characters.
func routePrefixes(hunks []patch.Hunk) []string {
	var out []string
	for _, h := range hunks {
		if h.Malformed() {
			continue
		}
		for _, l := range h.Lines {
			if l.Op != '-' {
				continue
			}
			m := routeKeyRE.FindStringSubmatch(strings.TrimRight(l.Text, "\r\n"))
			if m == nil {
				continue
			}
			if p, ok := staticPrefix(m[1]); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// staticPrefix is the route's segments before its first "{…}" segment,
// joined with "/" behind a leading "/"; ok is false when none of them is a
// static segment of minRouteSegment characters or more.
func staticPrefix(route string) (string, bool) {
	var keep []string
	long := false
	for _, seg := range strings.Split(strings.TrimPrefix(route, "/"), "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			break
		}
		if seg == "" {
			continue
		}
		keep = append(keep, seg)
		long = long || len(seg) >= minRouteSegment
	}
	if !long {
		return "", false
	}
	return "/" + strings.Join(keep, "/"), true
}
