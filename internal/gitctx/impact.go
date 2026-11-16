package gitctx

import (
	"context"
	"log/slog"
	pathpkg "path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Impact trace (V-18 spec §1.2, X-50).
//
// From the changed symbols, the trace follows each use to the definition
// that encloses it (EnclosingDefinition), then the uses of that definition,
// up to a depth: B calls the changed A, C calls B. It matches names, not
// types: an overload, a call through reflection, dependency injection, an
// event or dynamic dispatch can be linked wrongly or missed.
//
// Every read is the symbol search's: git grep and git cat-file --batch on
// the head commit of the bare cache, offline, with the same pathspecs,
// filter, timeouts and output caps (Grep). Nothing is built, run or loaded
// as code (R-1).

// Bounds and defaults of an ImpactQuery. A value of 0 selects the default;
// a value outside the bounds is clamped.
const (
	DefaultImpactDepth             = 2
	MaxImpactDepth                 = 3
	DefaultImpactMaxCallersPerNode = 5
	MaxImpactMaxCallersPerNode     = 20
	DefaultImpactMaxLinks          = 40
	MaxImpactMaxLinks              = 200
	// widelyUsedFactor: a name whose search returns more than this many
	// times MaxCallersPerNode hits is widely used (§0 item 5).
	widelyUsedFactor = 3
	// impactUseLines is the number of lines kept around a use.
	impactUseLines = 2
	// maxBodySeeds caps the seeds taken from body-only hunks.
	maxBodySeeds = DefaultMaxSymbols
)

// Reasons a node is not followed (ImpactNode.NotFollowed).
const (
	NotFollowedWidelyUsed = "widely_used"
	NotFollowedDepth      = "depth"
	NotFollowedBudget     = "budget"
	NotFollowedOutOfScope = "out_of_scope"
)

// Kinds of link (ImpactNode.Kind).
const (
	KindCall     = "call"
	KindInherits = "inherits"
)

// ImpactQuery is a trace from the symbols of a pull request.
type ImpactQuery struct {
	// Seeds are the symbols to trace from, in rank order (ExtractSymbols).
	// Test symbols and new-only symbols are dropped: nothing can call
	// them yet.
	Seeds []Symbol
	// BodyLines are, for each changed file, head-side lines of hunks that
	// define no symbol (BodyHunkLines): a change inside a body. The
	// definition enclosing each line at head becomes a seed of rank
	// RankChanged, unless its name is a seed already (spec §6 item 2).
	BodyLines map[string][]int
	// Hunks are the pull request's own files and their changed line ranges
	// (head side). A hit inside a range is dropped: the diff shows it. A hit
	// outside every range is kept with InPR set and followed.
	Hunks map[string][]LineRange
	// Filter is the filter of the search, as in Query.
	Filter *filter.Filter
	// Scope limits where hits are taken from and marks entry points.
	Scope ImpactScope
	// Depth is the number of hops, 1 to 3 (0: 2).
	Depth int
	// MaxCallersPerNode caps the uses kept per node, 1 to 20 (0: 5).
	MaxCallersPerNode int
	// MaxLinks caps the nodes found by the trace, seeds not counted, 1 to
	// 200 (0: 40).
	MaxLinks int
	// Log, when set, receives one debug record of counts. It never carries
	// a name, a path or repository text.
	Log *slog.Logger
}

// ImpactScope says where to trace and what matters.
type ImpactScope struct {
	// Paths, when set, keeps only hits below these directory prefixes ("" is
	// the whole repository). ExcludePaths drops hits below these.
	Paths, ExcludePaths []string
	// EntryPoints are globs (filter syntax) of the files where users or
	// other systems reach the code. They mark nodes; marked nodes are still
	// followed.
	EntryPoints []string
}

// ImpactNode is one node of the trace: a seed, or the definition that
// encloses a use of its parent.
type ImpactNode struct {
	// Name is the definition's name; for a use outside any definition, the
	// base name of its file.
	Name string
	// Path is the file of the definition. For a seed it is the changed file
	// the symbol was taken from.
	Path string
	// DefLine and DefText are the definition's line and its text (untrusted
	// repository text, one line). A seed has them when it comes from a
	// body-only hunk; a file-level use has neither.
	DefLine int
	DefText string
	// UseLine is the line of Path that uses the parent; UseText is that
	// line with up to 2 lines around it (untrusted). Zero for a seed.
	UseLine int
	UseText string
	// Hop is 0 for a seed, else the distance from it.
	Hop int
	// Parent is the index of the parent node, -1 for a seed.
	Parent int
	// Rank is the seed's rank (seeds only; RankChanged for a body-only
	// seed).
	Rank Rank
	// Kind is KindCall or KindInherits: the use line extends, implements or
	// overrides, names the parent after ":" in a class header (in Python,
	// within the parentheses of one), or defines the parent's name itself
	// (an override or implementation in another file). "" for a seed.
	Kind string
	// Entry: Path matches an entry point.
	Entry bool
	// InPR: Path is a file of the pull request (the use is outside its
	// hunks).
	InPR bool
	// NotFollowed says why the node's uses were not searched: "" (they
	// were, or a search failed), NotFollowedWidelyUsed (its own search
	// returned too many hits, or its parent's did), NotFollowedDepth (the
	// last hop, or a use outside any definition) or NotFollowedBudget
	// (MaxLinks or the time limit was reached first).
	NotFollowed string
	// MoreUses is the number of uses of the node found beyond the ones kept
	// (MaxCallersPerNode).
	MoreUses int
}

// ImpactResult is a trace with its counts.
type ImpactResult struct {
	// Nodes are the seeds first, in rank order, then the nodes of hop 1, 2
	// and 3 in the order found.
	Nodes []ImpactNode
	// Seeds is the number of seeds, BodySeeds those from body-only hunks.
	Seeds, BodySeeds int
	// Links is the number of nodes that are not seeds.
	Links int
	// DepthReached is the largest hop of a node.
	DepthReached int
	// NotFollowed is the sum of NotFollowedBy: per reason, the nodes marked
	// with it, plus for NotFollowedOutOfScope the hits dropped by the scope
	// and for NotFollowedBudget the hits dropped once MaxLinks was reached.
	NotFollowed   int
	NotFollowedBy map[string]int
	// EntryPointsReached is the number of nodes other than seeds that are
	// entry points.
	EntryPointsReached int
	// SkippedBlobs counts the files not searched because the cache lacks
	// their blob, as in Result.
	SkippedBlobs int
	// Truncated is true when the time limit stopped the trace or a search
	// printed more than it was allowed to.
	Truncated bool
}

// BodyHunkLines returns, for each code file of files (not deleted, not
// binary, not a test or documentation file), the head-side line of every
// hunk whose added and removed lines define no symbol: a change inside a
// body. The line is the hunk's first non-blank added line or, for a hunk
// that only removes lines, the head line just before the removal. Lines of
// a file are in hunk order. It is pure.
func BodyHunkLines(files []provider.FilePatch) map[string][]int {
	out := map[string][]int{}
	for _, fp := range files {
		if fp.Binary || fp.Patch == "" || fp.Path == "" || fp.Type == provider.ChangeDeleted || IsTestPath(fp.Path) || filter.IsDocumentation(fp.Path) {
			continue
		}
		lang := filter.Language(fp.Path)
		if proseLanguages[lang] {
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
			if line, ok := bodyLine(h, lang); ok {
				out[fp.Path] = append(out[fp.Path], line)
			}
		}
	}
	return out
}

// bodyLine is BodyHunkLines for one hunk.
func bodyLine(h patch.Hunk, lang string) (int, bool) {
	head, added, removed := h.NewStart, 0, 0
	for _, l := range h.Lines {
		text := strings.TrimRight(l.Text, "\r\n")
		switch l.Op {
		case ' ':
			head++
		case '+':
			if _, ok := definitionName(text, lang, false); ok {
				return 0, false
			}
			if added == 0 && strings.TrimSpace(text) != "" {
				added = head
			}
			head++
		case '-':
			if _, ok := definitionName(text, lang, false); ok {
				return 0, false
			}
			if removed == 0 {
				removed = max(head-1, 1)
			}
		}
	}
	switch {
	case added > 0:
		return added, true
	case removed > 0:
		return removed, true
	}
	return 0, false
}

// visitKey identifies a node by the definition it stands for.
type visitKey struct {
	path string
	line int
}

// tracer is one Trace call.
type tracer struct {
	s     *searcher
	q     ImpactQuery
	res   *ImpactResult
	nodes []ImpactNode
	// syms are the symbols whose own definitions are not uses, by node
	// index: a seed's symbol (its changed files), or a node's name in the
	// node's file.
	syms     []Symbol
	root     []int // the seed of each node
	visited  map[visitKey]bool
	seedOwn  map[string]Symbol // ExtractSymbols seeds by name
	paths    []string
	excludes []string
	links    int
}

// Trace follows the uses of q's seeds through the head tree of c, breadth
// first and seeds in rank order, up to q.Depth hops (see the package notes
// above and ImpactQuery). Each hop searches the names of the previous hop's
// nodes with Grep's machinery (whole word, fixed string, the filter, the
// pull request's hunks), keeps up to MaxCallersPerNode uses per name
// (files of the seed's language first, then distinct files, files outside
// the pull request before its own), and reads each kept use's file at head
// to find the definition around it. A definition already in the trace is
// not added twice, so cycles end.
//
// A hit outside q.Scope.Paths or inside q.Scope.ExcludePaths is dropped
// before it is counted. A name whose search returns more than 3 times
// MaxCallersPerNode hits keeps its first ones and is not followed further.
// The trace stops at MaxLinks nodes, at Depth, or at the search's total
// time limit (Truncated).
//
// An error is returned only when nothing could be searched: an *Error with
// a fixed reason, or the context's own error.
func (r *Runner) Trace(ctx context.Context, c Checkout, q ImpactQuery) (ImpactResult, error) {
	res := ImpactResult{NotFollowedBy: map[string]int{}}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	started := time.Now()
	sha := strings.ToLower(c.HeadSHA)
	if !shaRE.MatchString(sha) {
		return res, fail(ReasonUnsupported)
	}
	paths, excludes, ok := cleanScope(q.Scope)
	if !ok {
		return res, fail(ReasonUnsupported)
	}
	q.Depth = clampDefault(q.Depth, DefaultImpactDepth, MaxImpactDepth)
	q.MaxCallersPerNode = clampDefault(q.MaxCallersPerNode, DefaultImpactMaxCallersPerNode, MaxImpactMaxCallersPerNode)
	q.MaxLinks = clampDefault(q.MaxLinks, DefaultImpactMaxLinks, MaxImpactMaxLinks)

	seeds := traceableSeeds(q.Seeds)
	if len(seeds) == 0 && len(q.BodyLines) == 0 {
		return res, nil
	}
	g, err := r.openCheckout(c)
	if err != nil {
		return res, err
	}
	total, cancel := context.WithTimeout(ctx, grepTotalTimeout)
	defer cancel()
	s := &searcher{g: g, sha: sha, q: Query{Hunks: q.Hunks, Filter: q.Filter}, exclude: map[string]bool{}}
	missing, err := s.missingFiles(total)
	if err != nil {
		return res, err
	}
	res.SkippedBlobs = len(missing)
	t := &tracer{s: s, q: q, res: &res, visited: map[visitKey]bool{}, seedOwn: map[string]Symbol{}, paths: paths, excludes: excludes}
	t.addSeeds(total, seeds)
	err = t.run(ctx, total, s.pathspecs(missing))
	res.Nodes = t.nodes
	if err != nil {
		return res, err
	}
	t.count()
	if q.Log != nil {
		q.Log.Debug("impact trace", "seeds", res.Seeds, "body_seeds", res.BodySeeds, "links", res.Links,
			"depth_reached", res.DepthReached, "not_followed", res.NotFollowed,
			"widely_used", res.NotFollowedBy[NotFollowedWidelyUsed], "depth", res.NotFollowedBy[NotFollowedDepth],
			"budget", res.NotFollowedBy[NotFollowedBudget], "out_of_scope", res.NotFollowedBy[NotFollowedOutOfScope],
			"entry_points_reached", res.EntryPointsReached, "skipped_blobs", res.SkippedBlobs,
			"truncated", res.Truncated, "duration_ms", time.Since(started).Milliseconds())
	}
	return res, nil
}

// clampDefault maps 0 (or less) to def and a value above hi to hi.
func clampDefault(v, def, hi int) int {
	if v <= 0 {
		return def
	}
	return min(v, hi)
}

// cleanScope validates the scope's prefixes. An empty prefix in Paths is the
// whole repository, so Paths then confines nothing; empty exclusions are
// ignored.
func cleanScope(sc ImpactScope) (paths, excludes []string, ok bool) {
	var inc []string
	for _, p := range sc.Paths {
		if p == "" {
			inc = nil
			break
		}
		inc = append(inc, p)
	}
	paths, ok = cleanPrefixes(inc)
	if !ok {
		return nil, nil, false
	}
	excludes, ok = cleanPrefixes(slices.DeleteFunc(slices.Clone(sc.ExcludePaths), func(p string) bool { return p == "" }))
	return paths, excludes, ok
}

// traceableSeeds drops test, new-only, unusable and repeated names.
func traceableSeeds(in []Symbol) []Symbol {
	var out []Symbol
	seen := map[string]bool{}
	for _, s := range in {
		if s.Test || s.New || seen[s.Name] || !usableSymbol(s.Name) {
			continue
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	return out
}

// addSeeds makes the seed nodes: the symbols of the diff, with the
// definitions of body-only hunks inserted after the removed and
// signature-changed ones.
func (t *tracer) addSeeds(ctx context.Context, seeds []Symbol) {
	type seed struct {
		sym Symbol
		def Definition
	}
	known := map[string]bool{}
	for _, s := range seeds {
		known[s.Name] = true
	}
	var body []seed
	files := make([]string, 0, len(t.q.BodyLines))
	for p := range t.q.BodyLines {
		if p != "" && !IsTestPath(p) && (t.q.Filter == nil || t.q.Filter.Include(p)) {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	if len(files) > 0 {
		blobs := t.s.readBlobs(ctx, files)
		for _, p := range files {
			lines := blobs[p]
			at := slices.Clone(t.q.BodyLines[p])
			sort.Ints(at)
			for _, n := range at {
				d, ok := enclosingIn(lines, filter.Language(p), n)
				if !ok || known[d.Name] || !usableSymbol(d.Name) || len(body) == maxBodySeeds {
					continue
				}
				known[d.Name] = true
				body = append(body, seed{sym: Symbol{Name: d.Name, Path: p, Rank: RankChanged, Paths: []string{p}}, def: d})
			}
		}
	}
	split := len(seeds)
	for i, s := range seeds {
		if s.Rank != RankRemoved && s.Rank != RankSignature {
			split = i
			break
		}
	}
	all := make([]seed, 0, len(seeds)+len(body))
	for _, s := range seeds[:split] {
		all = append(all, seed{sym: s})
	}
	all = append(all, body...)
	for _, s := range seeds[split:] {
		all = append(all, seed{sym: s})
	}
	for _, sd := range all {
		i := len(t.nodes)
		t.nodes = append(t.nodes, ImpactNode{
			Name: sd.sym.Name, Path: sd.sym.Path, DefLine: sd.def.Line, DefText: sd.def.Text,
			Parent: -1, Rank: sd.sym.Rank, Entry: t.entry(sd.sym.Path),
		})
		t.syms = append(t.syms, sd.sym)
		t.root = append(t.root, i)
		if sd.def.Line > 0 {
			t.visited[visitKey{sd.sym.Path, sd.def.Line}] = true
			t.res.BodySeeds++
		} else {
			t.seedOwn[sd.sym.Name] = sd.sym
		}
	}
	t.res.Seeds = len(all)
}

// run walks the hops. It returns an error only when no name could be
// searched at all.
func (t *tracer) run(ctx, total context.Context, pathspecs []string) error {
	frontier := make([]int, len(t.nodes))
	for i := range frontier {
		frontier[i] = i
	}
	searched := map[string]bool{}
	var firstErr error
	ok := 0
	for hop := 1; hop <= t.q.Depth && len(frontier) > 0; hop++ {
		var next []int
		for k, idx := range frontier {
			if t.links >= t.q.MaxLinks || total.Err() != nil {
				t.res.Truncated = t.res.Truncated || total.Err() != nil
				t.markBudget(frontier[k:])
				next = nil
				break
			}
			n := t.nodes[idx]
			if n.NotFollowed != "" || searched[n.Name] {
				continue
			}
			searched[n.Name] = true
			raw, truncated, err := t.s.search(total, n.Name, pathspecs, true)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if total.Err() != nil {
					t.res.Truncated = true
					t.markBudget(frontier[k:])
					next = nil
					break
				}
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			ok++
			t.res.Truncated = t.res.Truncated || truncated
			next = append(next, t.expand(total, idx, hop, raw)...)
		}
		frontier = next
	}
	if ok == 0 && firstErr != nil {
		return firstErr
	}
	return nil
}

// expand adds the nodes of one name's hits and returns those to follow.
func (t *tracer) expand(ctx context.Context, idx, hop int, raw []rawHit) []int {
	self := t.syms[idx]
	raw = slices.DeleteFunc(raw, func(h rawHit) bool { return self.OwnDefinition(h.path, h.text) })
	kept := raw[:0]
	for _, h := range raw {
		if t.inScope(h.path) {
			kept = append(kept, h)
		} else {
			t.res.NotFollowedBy[NotFollowedOutOfScope]++
		}
	}
	raw = kept
	maxHits := t.q.MaxCallersPerNode
	wide := len(raw) > widelyUsedFactor*maxHits
	chosen := chooseHits(Symbol{Name: self.Name, Path: t.syms[t.root[idx]].Path}, raw, maxHits)
	t.nodes[idx].MoreUses = len(raw) - len(chosen)
	if wide {
		t.nodes[idx].NotFollowed = NotFollowedWidelyUsed
	}
	files := make([]string, 0, len(chosen))
	for _, h := range chosen {
		files = append(files, h.path)
	}
	blobs := t.s.readBlobs(ctx, files)
	parent := t.nodes[idx]
	var follow []int
	for _, h := range chosen {
		lines := blobs[h.path]
		lang := filter.Language(h.path)
		d, ok := enclosingIn(lines, lang, h.line)
		if ok && t.isSeedDefinition(h.path, d.Name) {
			continue // the seed itself (a recursive call) or another seed
		}
		key := visitKey{h.path, d.Line}
		if t.visited[key] {
			continue
		}
		if t.links >= t.q.MaxLinks {
			t.res.NotFollowedBy[NotFollowedBudget]++
			continue
		}
		t.visited[key] = true
		use, _ := around(lines, h.line, impactUseLines)
		if use == "" {
			use = h.text
		}
		node := ImpactNode{
			Name: d.Name, Path: h.path, DefLine: d.Line, DefText: d.Text,
			UseLine: h.line, UseText: use, Hop: hop, Parent: idx,
			Kind: linkKind(h.text, parent.Name, lang), Entry: t.entry(h.path), InPR: h.changed,
		}
		switch {
		case !ok:
			node.Name, node.NotFollowed = pathpkg.Base(h.path), NotFollowedDepth
		case wide:
			node.NotFollowed = NotFollowedWidelyUsed
		case hop == t.q.Depth:
			node.NotFollowed = NotFollowedDepth
		}
		i := len(t.nodes)
		t.nodes = append(t.nodes, node)
		t.syms = append(t.syms, Symbol{Name: node.Name, Path: node.Path, Paths: []string{node.Path}})
		t.root = append(t.root, t.root[idx])
		t.links++
		if node.NotFollowed == "" {
			follow = append(follow, i)
		}
	}
	return follow
}

// isSeedDefinition reports whether a definition named name in path is one
// of the seeds of the diff in its own files: a use there is the seed's own
// body, already in the trace as the seed.
func (t *tracer) isSeedDefinition(path, name string) bool {
	s, ok := t.seedOwn[name]
	return ok && (path == s.Path || slices.Contains(s.Paths, path))
}

// markBudget marks the unsearched nodes of idxs as not followed for the
// budget.
func (t *tracer) markBudget(idxs []int) {
	for _, i := range idxs {
		if t.nodes[i].NotFollowed == "" {
			t.nodes[i].NotFollowed = NotFollowedBudget
		}
	}
}

// inScope applies the scope's directory prefixes.
func (t *tracer) inScope(path string) bool {
	if len(t.paths) > 0 && !withinPrefixes(path, t.paths) {
		return false
	}
	return !withinPrefixes(path, t.excludes)
}

// entry reports whether path matches an entry-point glob.
func (t *tracer) entry(path string) bool {
	if path == "" {
		return false
	}
	for _, g := range t.q.Scope.EntryPoints {
		if filter.MatchGlob(g, path) {
			return true
		}
	}
	return false
}

// count fills the result's counts from the nodes.
func (t *tracer) count() {
	r := t.res
	r.Links = t.links
	for _, n := range t.nodes {
		r.DepthReached = max(r.DepthReached, n.Hop)
		if n.NotFollowed != "" {
			r.NotFollowedBy[n.NotFollowed]++
		}
		if n.Hop > 0 && n.Entry {
			r.EntryPointsReached++
		}
	}
	for _, v := range r.NotFollowedBy {
		r.NotFollowed += v
	}
}

var (
	inheritWordRE = regexp.MustCompile(`\b(?:extends|implements|override)\b`)
	typeHeadRE    = regexp.MustCompile(`\b` + typeKinds + `\s+\w+`)
)

// linkKind classifies the use of name on line (a hit's text) in a file of
// language lang: KindInherits when the line extends, implements or
// overrides, when it is a class header that names name after ":" (its own
// parenthesised parameters skipped) or, in Python, within its parentheses,
// or when it defines name itself (an override or an implementation in
// another file); KindCall otherwise.
func linkKind(line, name, lang string) string {
	line = strings.TrimSpace(line)
	if inheritWordRE.MatchString(line) {
		return KindInherits
	}
	if def, ok := definitionName(line, lang, true); ok && def == name {
		return KindInherits
	}
	loc := typeHeadRE.FindStringIndex(line)
	if loc == nil {
		return KindCall
	}
	rest := strings.TrimSpace(line[loc[1]:])
	if lang == "Python" && strings.HasPrefix(rest, "(") && hasWord(rest, name) {
		return KindInherits
	}
	rest = skipParens(rest)
	if i := strings.IndexByte(rest, ':'); i >= 0 && hasWord(rest[i+1:], name) {
		return KindInherits
	}
	return KindCall
}

// skipParens drops a leading balanced parenthesised group ("(a: A) : B" is
// ": B"), and generic parameters before it.
func skipParens(s string) string {
	if strings.HasPrefix(s, "<") {
		if i := strings.IndexByte(s, '>'); i >= 0 {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	if !strings.HasPrefix(s, "(") {
		return s
	}
	depth := 0
	for i, r := range s {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[i+1:]
			}
		}
	}
	return s
}

// hasWord reports whether s holds name as a whole word.
func hasWord(s, name string) bool {
	isWord := func(b byte) bool {
		return b == '_' || b == '$' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
	}
	for from := 0; ; {
		i := strings.Index(s[from:], name)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(name)
		if (i == 0 || !isWord(s[i-1])) && (end == len(s) || !isWord(s[end])) {
			return true
		}
		from = i + 1
	}
}
