// Package projctx gives pr_review and pr_improve the project's own context
// (X-32, design Y-23): the repository's guidelines file and the listing of
// the directories a pull request touches, both read at the pull request's
// base revision (the one its diff was computed against) and rendered as
// fenced, budgeted prompt blocks under fixed headers that mark them as data.
//
// Reading at the base means that a pull request cannot rewrite the rules it
// is judged by: a guidelines file the pull request adds or changes is read
// as the target branch has it, never from the head.
//
// The reads go through a Source: the provider's API (ProviderSource) or,
// when repository context (X-22) already holds the base commit, the local
// cache first (Prefer). Nothing here fails a tool: every failure is a status
// and a fixed note. Neither the files' text nor directory or entry names are
// logged (X-8); the debug lines carry statuses, error classes and counts.
//
// It is shared by internal/review and internal/improve, which must not
// import each other.
package projctx

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Source reads files and directory listings at one revision. Errors follow
// provider.Provider's GetFileAt and ListDir: not_found for a path that does
// not exist at the revision, too_large for a file over diff.max_file_bytes.
type Source interface {
	FileAt(ctx context.Context, path string) ([]byte, error)
	ListDir(ctx context.Context, dir string) (*provider.DirListing, error)
}

// ProviderSource reads revision rev of ref's repository through p's API.
func ProviderSource(p provider.Provider, ref provider.PRRef, rev string) Source {
	return &apiSource{p: p, ref: ref, rev: rev}
}

type apiSource struct {
	p   provider.Provider
	ref provider.PRRef
	rev string
}

func (s *apiSource) FileAt(ctx context.Context, path string) ([]byte, error) {
	return s.p.GetFileAt(ctx, s.ref, path, s.rev)
}

func (s *apiSource) ListDir(ctx context.Context, dir string) (*provider.DirListing, error) {
	return s.p.ListDir(ctx, s.ref, dir, s.rev)
}

// Prefer reads from primary and, when it fails for any reason other than a
// definite answer (not_found, too_large), from fallback. primary is the
// local cache of repository context (X-22), fallback the provider: the
// same rules and the same notes apply whichever answers.
func Prefer(primary, fallback Source) Source {
	return &preferSource{primary: primary, fallback: fallback}
}

type preferSource struct{ primary, fallback Source }

// definite reports an answer the cache gives as authoritatively as the API.
func definite(err error) bool {
	return err == nil || errors.Is(err, provider.ErrNotFound) || errors.Is(err, provider.ErrTooLarge)
}

func (s *preferSource) FileAt(ctx context.Context, path string) ([]byte, error) {
	b, err := s.primary.FileAt(ctx, path)
	if definite(err) || ctx.Err() != nil {
		return b, err
	}
	return s.fallback.FileAt(ctx, path)
}

func (s *preferSource) ListDir(ctx context.Context, dir string) (*provider.DirListing, error) {
	l, err := s.primary.ListDir(ctx, dir)
	if definite(err) || ctx.Err() != nil {
		return l, err
	}
	return s.fallback.ListDir(ctx, dir)
}

// Settings are the review.* values of X-32. They apply to pr_review and
// pr_improve.
type Settings struct {
	// Paths are review.guidelines_paths and Explicit says whether the user
	// set them (config.Review.GuidelinesPathsExplicit).
	Paths    []string
	Explicit bool
	// MaxGuidelinesTokens is review.max_guidelines_tokens; 0 turns the
	// guidelines off.
	MaxGuidelinesTokens int
	// FileTree is review.file_tree and MaxTreeTokens review.max_tree_tokens
	// (0 turns the tree off).
	FileTree      bool
	MaxTreeTokens int
}

// SettingsFrom maps the configuration to Settings.
func SettingsFrom(r config.Review) Settings {
	return Settings{
		Paths: slices.Clone(r.GuidelinesPaths), Explicit: r.GuidelinesPathsExplicit,
		MaxGuidelinesTokens: r.MaxGuidelinesTokens, FileTree: r.FileTree, MaxTreeTokens: r.MaxTreeTokens,
	}
}

// guidelinesOn and treeOn say whether the configuration asks for each part.
func (s Settings) guidelinesOn() bool { return len(s.Paths) > 0 && s.MaxGuidelinesTokens > 0 }
func (s Settings) treeOn() bool       { return s.FileTree && s.MaxTreeTokens > 0 }

// Context is the project context of one run: what was read and the blocks
// rendered from it. The pipelines shrink it, tree first, while the prompt
// has no room for the diff (DropTree and CutGuidelines while the
// scaffolding leaves no diff budget at all; Shrink while the prepared diff
// does not fit, DiffDoesNotFit).
type Context struct {
	factor float64
	g      guidelines
	t      tree
}

// Read reads the guidelines and the listings of the directories files and
// skipped touch through src (at the base revision the caller chose), and
// renders both blocks within their budgets. rev is only checked for being
// set: without a base revision nothing can be read, and both parts that are
// on are unreadable. It never fails.
func Read(ctx context.Context, src Source, rev string, s Settings, files []provider.FilePatch,
	skipped []provider.SkippedFile, factor float64, log *slog.Logger) *Context {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	c := &Context{factor: factor}
	c.g = readGuidelines(ctx, src, rev, s, log)
	c.g.render(s.MaxGuidelinesTokens, factor)
	c.t = readTree(ctx, src, rev, s, files, skipped, log)
	c.t.render(s.MaxTreeTokens, factor)
	log.Debug("project context: read", "guidelines", c.g.coverageStatus(), "guidelines_tokens", c.g.tokens,
		"tree", c.t.coverageStatus(), "directories", c.t.shown, "touched_directories", len(c.t.dirs)+c.t.more)
	return c
}

// Guidelines is the rendered guidelines block (GuidelinesHeader and the
// fenced text), or "".
func (c *Context) Guidelines() string { return c.g.block }

// Tree is the rendered file-tree block (TreeHeader and the fenced
// listing), or "".
func (c *Context) Tree() string { return c.t.block }

// DropTree leaves the file tree out to make room for the diff. It reports
// whether there was a block to drop.
func (c *Context) DropTree() bool {
	if c.t.block == "" {
		return false
	}
	c.t.block, c.t.shown, c.t.droppedForDiff = "", 0, true
	return true
}

// CutGuidelines shortens the guidelines block by at least over tokens (at a
// line boundary, as the budget cut) to make room for the diff; when no line
// is left the block is dropped. It reports whether there was a block to
// shorten.
func (c *Context) CutGuidelines(over int) bool {
	if c.g.block == "" {
		return false
	}
	c.g.cutForDiff = true
	c.g.render(c.g.tokens-max(over, 1), c.factor)
	return true
}

// HasBlocks reports whether the prompt carries a block of the project
// context.
func (c *Context) HasBlocks() bool { return c.g.block != "" || c.t.block != "" }

// Shrink makes room for the diff by one step, in the order of X-32: the
// file tree is dropped first, then the guidelines are halved (at a line
// boundary) until none is left. It reports false when there is nothing
// left to shrink.
func (c *Context) Shrink() bool {
	if c.DropTree() {
		return true
	}
	return c.CutGuidelines(c.g.tokens / 2)
}

// DiffDoesNotFit reports whether a prepared diff did not fit the prompt:
// it could not be prepared at all (tokens.ErrDoesNotFit), a file was
// clipped, or, for a run in one call (oneCall), files were left out. A run
// in parts gives files that were left out to a further part, so for it
// that alone is no reason to shrink.
func DiffDoesNotFit(p *diffpipe.Prepared, err error, oneCall bool) bool {
	if err != nil {
		return errors.Is(err, tokens.ErrDoesNotFit)
	}
	return len(p.Clipped) > 0 || (oneCall && llmrun.LeavesFilesOut(p))
}

// Coverage returns coverage.guidelines and coverage.file_tree; nil, nil for
// a nil Context.
func (c *Context) Coverage() (*llmrun.Guidelines, *llmrun.FileTree) {
	if c == nil {
		return nil, nil
	}
	return &llmrun.Guidelines{Path: c.g.path, Status: c.g.coverageStatus(), Tokens: c.g.tokens},
		&llmrun.FileTree{Directories: c.t.shown, Status: c.t.coverageStatus()}
}

// Notes returns the fixed notes of both parts, guidelines first.
func (c *Context) Notes() []string {
	return append(c.g.notes(), c.t.notes()...)
}

// errorClass is the fixed class of a read error, for the debug log.
func errorClass(err error) string {
	var pe *provider.Error
	if errors.As(err, &pe) {
		return string(pe.Class)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "unclassified"
}

// maxNameRunes caps a path or entry name in a block, in runes.
const maxNameRunes = 200

// cleanName makes an untrusted path or entry name safe for one line of a
// prompt block: invalid UTF-8 is replaced, every control character
// (newlines and tabs included) and every bidirectional formatting character
// becomes U+FFFD, so that a name can neither break the line nor hide or
// reorder text, and the result is cut to maxNameRunes runes with "…".
func cleanName(s string) string {
	s = strings.ToValidUTF8(s, "\ufffd")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || isBidiControl(r) || r == '\u2028' || r == '\u2029' {
			return '\ufffd'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > maxNameRunes {
		s = string(r[:maxNameRunes]) + "…"
	}
	return s
}

// isBidiControl reports a bidirectional formatting character (embedding,
// override, isolate or mark).
func isBidiControl(r rune) bool {
	return r == '\u200e' || r == '\u200f' || r == '\u061c' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}

// sortedByCount returns the keys of counts, the largest count first and
// then by name.
func sortedByCount(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

// ReadFor reads the project context of a pull request at the base revision
// its diff used (pr.BaseSHA): from the cache of repository context when rcs
// (nil when repository context is off) already holds that commit, else
// through p. cfg supplies the review.* settings and diff.max_file_bytes.
func ReadFor(ctx context.Context, cfg *config.Config, p provider.Provider, ref provider.PRRef, pr *provider.PullRequest,
	d *provider.Diff, rcs *repoctx.Session, log *slog.Logger) *Context {
	s := SettingsFrom(cfg.Review)
	src, from := ProviderSource(p, ref, pr.BaseSHA), "api"
	if s.guidelinesOn() || s.treeOn() {
		if rr := rcs.AtRevision(ctx, pr.BaseSHA, cfg.Diff.MaxFileBytes); rr != nil {
			src, from = Prefer(rr, src), "cache"
		}
	}
	if log != nil {
		log.Debug("project context: source", "from", from)
	}
	return Read(ctx, src, pr.BaseSHA, s, d.Files, d.Skipped, cfg.LLM.TokenEstimateFactor, log)
}
