package repoctx

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Impact trace (X-50, v2.10 spec V-18 §2.4): the chains of uses of the
// changed symbols, traced once per review on the head commit the session
// fetched (gitctx.Runner.Trace), rendered as one tree per seed under a
// fixed header and placed in the room the diff leaves, after the
// repository-context block. Names, paths and snippets are never logged.

// ImpactHeader is the sentence above the fenced impact block (§2.4,
// verbatim).
const ImpactHeader = "Impact chains of symbols changed in this pull request (read-only context, found by name matching: " +
	"a link may be wrong, and calls through reflection, dependency injection, events or dynamic dispatch are missing). " +
	"For each chain, check whether the change breaks or silently alters what the callers rely on (arguments, return values, " +
	"null or error cases, units, ordering, side effects). Report a problem in a caller only when this pull request causes it, " +
	"and name the caller's file and line. Lines marked [entry point] are where users or other systems reach the code."

// Tracer is what the impact trace needs from internal/gitctx beyond a
// Backend. *gitctx.Runner implements it; a Backend that does not makes
// the trace fail (unsupported).
type Tracer interface {
	Trace(ctx context.Context, c gitctx.Checkout, q gitctx.ImpactQuery) (gitctx.ImpactResult, error)
}

var _ Tracer = (*gitctx.Runner)(nil)

// ImpactSettings are the context.impact.* values of one run, after
// precedence (tool argument > repository file > operator).
type ImpactSettings struct {
	Depth, MaxCallersPerNode, MaxLinks, MaxTokens int
	Scope                                         gitctx.ImpactScope
}

// ImpactSettingsFrom maps the configuration to ImpactSettings. paths, when
// not nil, replace context.impact.paths (the impact_paths argument).
func ImpactSettingsFrom(c config.ContextImpact, paths []string) ImpactSettings {
	sc := gitctx.ImpactScope{Paths: slices.Clone(c.Paths), ExcludePaths: slices.Clone(c.ExcludePaths), EntryPoints: slices.Clone(c.EntryPoints)}
	if paths != nil {
		sc.Paths = slices.Clone(paths)
	}
	return ImpactSettings{Depth: c.Depth, MaxCallersPerNode: c.MaxCallersPerNode, MaxLinks: c.MaxLinks, MaxTokens: c.MaxTokens, Scope: sc}
}

// Traced is the trace of one review.
type Traced struct {
	// Status is llmrun.ImpactOn, llmrun.ImpactNoSymbols or
	// llmrun.ImpactFailed; Reason is the gitctx reason of a failure (for
	// the debug log only).
	Status, Reason string
	Result         gitctx.ImpactResult
}

// Trace traces the uses of the symbols of files, the files of the pull
// request the review covers (the reduced diff of an incremental run). The
// seeds are ExtractSymbols' (capped by context.repo.max_symbols) and the
// definitions around the body-only hunks (gitctx.BodyHunkLines); a hit on
// an added line of one of files is in the diff and dropped (AddedLines).
// The files ExcludeWhole names are left out. It fetches the head when no
// search did yet; a failure is Status llmrun.ImpactFailed, never an error.
func (s *Session) Trace(ctx context.Context, files []provider.FilePatch, set ImpactSettings) Traced {
	seeds := gitctx.ExtractSymbols(files, s.settings.MaxSymbols)
	body := gitctx.BodyHunkLines(files)
	for p := range s.whole {
		delete(body, p)
	}
	if !slices.ContainsFunc(seeds, func(sym gitctx.Symbol) bool { return !sym.New && !sym.Test }) && len(body) == 0 {
		return Traced{Status: llmrun.ImpactNoSymbols}
	}
	tr, ok := s.backend.(Tracer)
	if !ok {
		return Traced{Status: llmrun.ImpactFailed, Reason: gitctx.ReasonUnsupported}
	}
	if reason := s.ready(ctx); reason != "" {
		s.log.Debug("impact trace: not available", "reason", reason)
		return Traced{Status: llmrun.ImpactFailed, Reason: reason}
	}
	var exclude []string
	for p := range s.whole {
		exclude = append(exclude, p)
	}
	sort.Strings(exclude)
	hunks := AddedLines(files)
	for _, p := range exclude {
		delete(hunks, p)
	}
	res, err := tr.Trace(ctx, s.co, gitctx.ImpactQuery{
		Seeds: seeds, BodyLines: body, Hunks: hunks, Exclude: exclude, Filter: s.flt, Scope: set.Scope,
		Depth: set.Depth, MaxCallersPerNode: set.MaxCallersPerNode, MaxLinks: set.MaxLinks, Log: s.log,
	})
	if err != nil {
		reason := reasonOf(err)
		s.log.Debug("impact trace: failed", "reason", reason)
		return Traced{Status: llmrun.ImpactFailed, Reason: reason}
	}
	return Traced{Status: llmrun.ImpactOn, Result: res}
}

// AddedLines returns, for each file of files that exists at the head
// commit (not deleted, not binary), the head-side ranges of its added
// lines: the trace's ImpactQuery.Hunks (review of WP-20a, DQ-2). A use on
// such a line is in the diff; a use on any other line of the file, a
// context line of a hunk included, is code whose link to the change the
// diff does not show. A file whose patch does not parse has an empty list:
// its uses are kept and marked as in the pull request. The repository-
// context block keeps its shown ranges (ShownLines).
func AddedLines(files []provider.FilePatch) map[string][]gitctx.LineRange {
	out := map[string][]gitctx.LineRange{}
	for _, fp := range files {
		if fp.Type == provider.ChangeDeleted || fp.Binary || fp.Path == "" {
			continue
		}
		ranges := []gitctx.LineRange{}
		hunks, err := patch.ParseHunks(fp.Patch)
		if err == nil {
			for _, h := range hunks {
				if h.Malformed() {
					continue
				}
				head := h.NewStart
				for _, l := range h.Lines {
					switch l.Op {
					case ' ':
						head++
					case '+':
						if n := len(ranges); n > 0 && ranges[n-1].To == head-1 {
							ranges[n-1].To = head
						} else {
							ranges = append(ranges, gitctx.LineRange{From: head, To: head})
						}
						head++
					}
				}
			}
		}
		out[fp.Path] = ranges
	}
	return out
}

// ImpactBlock is the rendered impact block and what it carries.
type ImpactBlock struct {
	// Text is the header and the fenced trees; "" when no tree fits or none
	// has a link.
	Text string
	// Trees and TreesOmitted count the trees shown and those left out by
	// the token budget; NodesOmitted the links of the trees left out.
	Trees, TreesOmitted, NodesOmitted int
	// Paths are the files of the nodes shown, in order.
	Paths []string
}

// impactTree is one rendered seed with its links.
type impactTree struct {
	text  string
	links int
	paths []string
}

// RenderImpact renders the trees of res whose seed keep accepts (nil keeps
// every seed), in rank order, within maxTokens (tokens.Estimate of the
// whole block, header and fence included). Trees are taken whole: the
// first one that does not fit and every one after it are counted as left
// out. A seed with no link and no further use is not rendered: there is
// nothing to say about it.
func RenderImpact(res gitctx.ImpactResult, keep func(seed gitctx.ImpactNode) bool, maxTokens int, factor float64) ImpactBlock {
	trees := impactTrees(res, keep)
	if len(trees) == 0 {
		return ImpactBlock{}
	}
	texts := make([]string, len(trees))
	for i, t := range trees {
		texts[i] = t.text
	}
	fits := func(n int) bool {
		return maxTokens > 0 && tokens.Estimate(impactText(texts[:n]), factor) <= maxTokens
	}
	n := 0
	for n < len(texts) && fits(n+1) {
		n++
	}
	var b ImpactBlock
	if n > 0 {
		b.Text = impactText(texts[:n])
	}
	b.Trees, b.TreesOmitted = n, len(trees)-n
	for _, t := range trees[:n] {
		for _, p := range t.paths {
			if !slices.Contains(b.Paths, p) {
				b.Paths = append(b.Paths, p)
			}
		}
	}
	for _, t := range trees[n:] {
		b.NodesOmitted += t.links
	}
	return b
}

// impactText joins trees under ImpactHeader inside a fence no repository
// text can close.
func impactText(trees []string) string {
	var b strings.Builder
	b.WriteString(ImpactHeader + "\n")
	mdutil.WriteFenced(&b, strings.Join(trees, "\n\n"), "", "")
	return strings.TrimRight(b.String(), "\n")
}

// impactTrees builds the trees of res, one per kept seed with at least one
// link or one further use, in the order of the seeds.
func impactTrees(res gitctx.ImpactResult, keep func(seed gitctx.ImpactNode) bool) []impactTree {
	children := map[int][]int{}
	for i, n := range res.Nodes {
		if n.Parent >= 0 {
			children[n.Parent] = append(children[n.Parent], i)
		}
	}
	var out []impactTree
	for i, n := range res.Nodes {
		if n.Hop != 0 || (keep != nil && !keep(n)) {
			continue
		}
		if len(children[i]) == 0 && n.MoreUses == 0 {
			continue
		}
		var lines []string
		t := impactTree{}
		head := cleanLine(n.Name, pathRunes) + "  (" + rankWords(n.Rank) + ")"
		if n.Path != "" {
			head += "  " + location(n.Path, n.DefLine)
			t.paths = append(t.paths, n.Path)
		}
		lines = append(lines, head)
		var walk func(idx int)
		walk = func(idx int) {
			for _, c := range children[idx] {
				t.links++
				lines = append(lines, nodeLines(res.Nodes[c])...)
				if !slices.Contains(t.paths, res.Nodes[c].Path) {
					t.paths = append(t.paths, res.Nodes[c].Path)
				}
				walk(c)
			}
			if more := moreLine(res.Nodes[idx]); more != "" {
				lines = append(lines, more)
			}
		}
		walk(i)
		t.text = strings.Join(lines, "\n")
		out = append(out, t)
	}
	return out
}

// nodeLines renders one link: its line with the marks, its definition, and
// its use (with the lines around it at hop 1, the use line alone deeper).
func nodeLines(n gitctx.ImpactNode) []string {
	pad := strings.Repeat("  ", n.Hop)
	head := pad + "← " + cleanLine(n.Name, pathRunes) + "  " + location(n.Path, n.DefLine)
	for _, m := range nodeMarks(n) {
		head += "  [" + m + "]"
	}
	detail := pad + "    "
	out := []string{head}
	if n.DefText != "" {
		out = append(out, detail+codeLine(n.DefText))
	}
	use := strings.Split(strings.ReplaceAll(n.UseText, "\r", ""), "\n")
	start := max(1, n.UseLine-gitctx.ImpactUseLines)
	at := n.UseLine - start
	if n.Hop > 1 || at < 0 || at >= len(use) {
		line := n.UseText
		if at >= 0 && at < len(use) {
			line = use[at]
		}
		return append(out, detail+"uses at :"+strconv.Itoa(n.UseLine)+": "+codeLine(line))
	}
	out = append(out, detail+"uses at :"+strconv.Itoa(n.UseLine)+":")
	for k, l := range use {
		out = append(out, detail+"  "+strconv.Itoa(start+k)+": "+codeLine(l))
	}
	return out
}

// maxImpactLine cuts one line of repository text in the block, in bytes.
const maxImpactLine = 400

// codeLine sanitizes one line of code for the block (review DQ-2 of
// WP-20b): its leading whitespace stays as it is (tabs included), so a
// nested use keeps its indentation; invalid UTF-8 is replaced, control
// characters other than tab are dropped, a stray line break becomes a
// space, trailing whitespace is trimmed, and the line is cut at
// maxImpactLine bytes on a rune boundary. The fence around the block is
// chosen after the text (mdutil.WriteFenced), so the line cannot close it.
func codeLine(s string) string {
	s = strings.ReplaceAll(cleanText(s), "\n", " ")
	if len(s) > maxImpactLine {
		cut := maxImpactLine
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.TrimRight(s[:cut], " \t")
	}
	return s
}

// nodeMarks are the bracketed marks of a link.
func nodeMarks(n gitctx.ImpactNode) []string {
	var m []string
	if n.Entry {
		m = append(m, "entry point")
	}
	if n.Kind == gitctx.KindInherits {
		m = append(m, "extends, implements or overrides")
	}
	if n.InPR {
		m = append(m, "changed file")
	}
	if n.DefLine == 0 {
		m = append(m, "file level")
	}
	switch n.NotFollowed {
	case gitctx.NotFollowedTest:
		m = append(m, "test")
	case gitctx.NotFollowedBudget:
		m = append(m, "not followed: budget")
	}
	return m
}

// moreLine is the line after a node's links that counts its further uses.
func moreLine(n gitctx.ImpactNode) string {
	if n.MoreUses <= 0 {
		return ""
	}
	pad := strings.Repeat("  ", n.Hop+1)
	tail := " not shown"
	if n.NotFollowed == gitctx.NotFollowedWidelyUsed {
		tail = " not followed (widely used)"
	}
	return pad + "⋯ " + llmrun.CountPhrase(n.MoreUses, "more use", "more uses") + " of " + cleanLine(n.Name, pathRunes) + tail
}

// location is "path:line", or the path alone without a line.
func location(path string, line int) string {
	if line <= 0 {
		return cleanLine(path, pathRunes)
	}
	return cleanLine(path, pathRunes) + ":" + strconv.Itoa(line)
}

// rankWords names a seed's rank (§2.4).
func rankWords(r gitctx.Rank) string {
	switch r {
	case gitctx.RankRemoved:
		return "removed or renamed"
	case gitctx.RankSignature:
		return "signature changed"
	}
	return "body changed"
}

// ImpactNotFollowed builds coverage.impact.not_followed from a trace and
// the links the prompt left out (omitted, added to budget).
func ImpactNotFollowed(res gitctx.ImpactResult, omitted int) llmrun.ImpactNotFollowed {
	by := res.NotFollowedBy
	return llmrun.ImpactNotFollowed{
		WidelyUsed: by[gitctx.NotFollowedWidelyUsed],
		Depth:      by[gitctx.NotFollowedDepth],
		Budget:     by[gitctx.NotFollowedBudget] + omitted,
		OutOfScope: by[gitctx.NotFollowedOutOfScope],
		Test:       by[gitctx.NotFollowedTest],
	}
}
