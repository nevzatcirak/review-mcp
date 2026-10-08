// Package repoctx adds repository context (X-22, RC-8 and RC-9 of the v1.1
// design note) to the prompts of pr_review and pr_ask: it fetches the pull
// request head through internal/gitctx, searches the uses of the symbols the
// diff changes, and renders the budgeted prompt block.
//
// It is the one place both pipelines share, so that internal/ask does not
// import internal/review. It depends on internal/gitctx, never the reverse.
//
// Nothing here fails a review. Every gitctx failure becomes a fixed reason
// (Found.Skipped, a gitctx.Reason* word) and the caller turns it into a
// fixed note and coverage.repo_context.status "skipped". Snippets are
// untrusted repository text: they are sanitised, fenced adaptively, and
// never logged; neither are symbol names or paths (counts only).
package repoctx

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/llmrun"
	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// Header is the sentence above the fenced block (RC-8, verbatim).
const Header = "Related code that uses symbols changed in this pull request (read-only context; it may be incomplete). " +
	"Entries marked as changed in this pull request are reviewed in another part or not at all. " +
	"Use this code to judge the effect of the change on callers and implementations. " +
	"Do not report issues in it unless the pull request causes them."

// ReasonNothingToReview is the reason of a review whose diff is empty after
// filtering: no model call is made, so no repository context is either. It
// is "skipped", not "off": "off" means only "disabled".
const ReasonNothingToReview = "nothing_to_review"

// MarkNotReviewed marks a use in a file of the pull request that no prompt
// of the review carries (left out by the budget, too large for a part,
// skipped by the provider). The text is shown after the entry's symbol.
const MarkNotReviewed = "changed in this pull request; not reviewed"

// MarkReviewedInPart marks a use in a file of the pull request that part j
// of a review in parts reviews.
func MarkReviewedInPart(j int) string {
	return "changed in this pull request; reviewed in part " + strconv.Itoa(j)
}

// ReasonBudget is the reason of a block left out because the diff needs the
// room (the diff always wins). The other reasons are gitctx's.
const ReasonBudget = "budget"

// NoteBudget is the note of a block left out for the diff's sake.
const NoteBudget = "repository context skipped: the prompt has no room for it beside the diff"

// NoteFor returns the fixed note for a skip reason.
func NoteFor(reason string) string {
	switch reason {
	case ReasonBudget:
		return NoteBudget
	case ReasonNothingToReview:
		return "repository context skipped: there is nothing to review"
	}
	return gitctx.Note(reason)
}

// NoteClipped is the note of a block clipped by whole entries; n entries
// were left out.
func NoteClipped(n int) string {
	return llmrun.CountPhrase(n, "repository-context reference was", "repository-context references were") +
		" left out to stay within context.repo.max_tokens and the room beside the diff."
}

// NotePartsSkipped is the note of a review in parts in which the block is
// missing from some parts.
func NotePartsSkipped(n int, reason string) string {
	return "Repository context is missing from " + llmrun.CountPhrase(n, "part", "parts") + " (" + reason + ")."
}

// Backend is what a Session needs from internal/gitctx. *gitctx.Runner
// implements it; tests substitute a fake.
type Backend interface {
	Ensure(ctx context.Context, repo gitctx.Repo, pr gitctx.PR) (gitctx.Checkout, error)
	Grep(ctx context.Context, c gitctx.Checkout, q gitctx.Query) (gitctx.Result, error)
}

// Settings are the context.repo.* values a Session uses.
type Settings struct {
	MaxSymbols, MaxHitsPerSymbol, MaxTokens int
}

// SettingsFrom maps the configuration to Settings.
func SettingsFrom(c config.ContextRepo) Settings {
	return Settings{MaxSymbols: c.MaxSymbols, MaxHitsPerSymbol: c.MaxHitsPerSymbol, MaxTokens: c.MaxTokens}
}

// Session is the repository context of one review: the pull request head is
// fetched at most once (the first Find that has a symbol to search), however
// many parts the review has. It is safe for concurrent use.
type Session struct {
	backend  Backend
	repo     gitctx.Repo
	repoOK   bool
	pr       gitctx.PR
	exclude  []string
	marks    map[string]string
	flt      *filter.Filter
	settings Settings
	log      *slog.Logger

	once sync.Once
	co   gitctx.Checkout
	err  error
}

// Open returns the Session of a review of pr in ref. cfg.Context.Repo must be
// enabled (the caller checks); backend nil selects the real gitctx runner.
// files are every reviewable file of the pull request: by default their
// paths are excluded from the search, being in the prompt already (a Scope
// narrows that for a part of a review in parts). skipped are the files the
// provider did not hand over; a use in one is shown, marked MarkNotReviewed.
// Nothing is fetched until Find.
func Open(cfg *config.Config, backend Backend, ref provider.PRRef, p provider.Provider, headSHA string,
	files []provider.FilePatch, skipped []provider.SkippedFile, flt *filter.Filter, log *slog.Logger) *Session {
	if backend == nil {
		backend = gitctx.New(gitctx.OptionsFromConfig(cfg.Context.Repo))
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	repo, ok := gitctx.RepoFor(cfg, ref, p)
	marks := map[string]string{}
	for _, f := range skipped {
		marks[f.Path] = MarkNotReviewed
	}
	return &Session{
		backend: backend, repo: repo, repoOK: ok,
		pr:       gitctx.PR{Number: ref.Number, HeadSHA: headSHA},
		exclude:  gitctx.ChangedPaths(files),
		marks:    marks,
		flt:      flt,
		settings: SettingsFrom(cfg.Context.Repo),
		log:      log,
	}
}

// Scope narrows a search to one prompt of a review in parts (X-19): the
// files whose diff is in the prompt are excluded (they are in it already),
// and the other files of the pull request are searched like any file and
// marked. The zero Scope pointer (nil) is a review in one call: every file
// of the pull request is excluded.
type Scope struct {
	// Exclude are the paths of the prompt's own files, old paths of renamed
	// files included (gitctx.ChangedPaths).
	Exclude []string
	// Marks gives the text that follows the symbol of a use in a file of the
	// pull request that this prompt does not carry (MarkReviewedInPart,
	// MarkNotReviewed), by path.
	Marks map[string]string
}

// Settings returns the session's settings.
func (s *Session) Settings() Settings { return s.settings }

// Found is what Find returns for one set of files.
type Found struct {
	// Symbols is the number of symbols searched and Names their names, in
	// rank order. Names are for the diag dry run; they are never logged.
	Symbols int
	Names   []string
	// Hits are the uses found, in symbol rank order.
	Hits []gitctx.Hit
	// Skipped is the fixed reason repository context is missing ("" when the
	// search ran, also when it found nothing).
	Skipped string
}

// Symbols returns the symbols of files, ranked and capped. It does no I/O.
func (s *Session) Symbols(files []provider.FilePatch) []gitctx.Symbol {
	return gitctx.ExtractSymbols(files, s.settings.MaxSymbols)
}

// ready fetches the head once. The error is a gitctx reason.
func (s *Session) ready(ctx context.Context) string {
	s.once.Do(func() {
		if !s.repoOK {
			s.err = &gitctx.Error{Reason: gitctx.ReasonUnsupported}
			return
		}
		s.co, s.err = s.backend.Ensure(ctx, s.repo, s.pr)
	})
	if s.err != nil {
		return reasonOf(s.err)
	}
	return ""
}

// Ready fetches the pull request head (once) and returns "" or the fixed
// reason it cannot be used.
func (s *Session) Ready(ctx context.Context) string { return s.ready(ctx) }

// Find searches the uses of the symbols of files. With no symbol it does no
// git work at all. A gitctx failure is Found.Skipped, never an error.
func (s *Session) Find(ctx context.Context, files []provider.FilePatch) Found {
	return s.FindIn(ctx, files, nil)
}

// FindIn is Find for a prompt of a review in parts (sc non-nil) or in one
// call (sc nil).
func (s *Session) FindIn(ctx context.Context, files []provider.FilePatch, sc *Scope) Found {
	syms := s.Symbols(files)
	if len(syms) == 0 {
		return Found{}
	}
	if reason := s.ready(ctx); reason != "" {
		s.log.Debug("repository context: not available", "reason", reason)
		return Found{Skipped: reason}
	}
	exclude := s.exclude
	if sc != nil {
		exclude = sc.Exclude
	}
	res, err := s.backend.Grep(ctx, s.co, gitctx.Query{
		Symbols: syms, Exclude: exclude, Filter: s.flt, MaxHitsPerSymbol: s.settings.MaxHitsPerSymbol,
	})
	if err != nil {
		reason := reasonOf(err)
		s.log.Debug("repository context: search failed", "reason", reason)
		return Found{Symbols: len(syms), Names: names(syms), Skipped: reason}
	}
	s.log.Debug("repository context: searched", "symbols", res.Symbols, "symbols_with_hits", res.SymbolsWithHits,
		"references", res.References(), "files", res.Files, "skipped_blobs", res.SkippedBlobs,
		"skipped_symbols", res.SkippedSymbols, "truncated", res.Truncated)
	return Found{Symbols: res.Symbols, Names: names(syms), Hits: res.Hits}
}

func names(syms []gitctx.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.Name
	}
	return out
}

// reasonOf maps an error to a fixed reason; a context error is a timeout
// when the deadline passed.
func reasonOf(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return gitctx.ReasonTimeout
	}
	return gitctx.ReasonOf(err)
}

// Block is the rendered prompt block and what it carries.
type Block struct {
	// Text is the header and the fenced entries; "" when no entry fits.
	Text string
	// Entries and Omitted count the uses shown and the ones left out.
	Entries, Omitted int
	// Files is the number of distinct files among the shown uses.
	Files int
}

// Render renders the block for hits within maxTokens (tokens.Estimate of the
// whole block, header and fence included, the estimator that clips the
// discussion). Entries are taken whole, in rank order, until the next one
// does not fit. maxTokens <= 0 or no hit yields an empty block; when not
// even the first entry fits, the block is empty and every use is omitted.
func Render(hits []gitctx.Hit, maxTokens int, factor float64) Block {
	return RenderMarked(hits, maxTokens, factor, nil)
}

// RenderMarked is Render with marks: the text after the symbol of an entry
// whose path is a key (see Scope.Marks).
func RenderMarked(hits []gitctx.Hit, maxTokens int, factor float64, marks map[string]string) Block {
	if len(hits) == 0 {
		return Block{}
	}
	if maxTokens <= 0 {
		return Block{Omitted: len(hits)}
	}
	entries := make([]string, len(hits))
	for i := range hits {
		entries[i] = entry(&hits[i], marks[hits[i].Path])
	}
	// The largest prefix that fits, by bisection (an estimate per probe
	// costs a tokenizer pass over the whole block), then verified downward:
	// BPE counts are not strictly monotone across a cut.
	fits := func(n int) bool { return tokens.Estimate(blockText(entries[:n]), factor) <= maxTokens }
	lo, hi := 0, len(hits)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(mid) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for lo > 0 && !fits(lo) {
		lo--
	}
	var best Block
	if lo > 0 {
		best = Block{Text: blockText(entries[:lo]), Entries: lo}
	}
	best.Omitted = len(hits) - best.Entries
	files := map[string]bool{}
	for i := 0; i < best.Entries; i++ {
		files[hits[i].Path] = true
	}
	best.Files = len(files)
	return best
}

// blockText joins the entries under the header, inside a fence that no
// snippet can close: mdutil.WriteFenced makes it one backtick longer than
// the longest backtick run in the entries.
func blockText(entries []string) string {
	var b strings.Builder
	b.WriteString(Header + "\n")
	mdutil.WriteFenced(&b, strings.Join(entries, "\n\n"), "", "")
	return strings.TrimRight(b.String(), "\n")
}

const pathRunes = 200

// entry is "[<path>:<line>] uses <symbol>", the mark in parentheses when
// there is one, and the snippet.
func entry(h *gitctx.Hit, mark string) string {
	head := "[" + cleanLine(h.Path, pathRunes) + ":" + strconv.Itoa(h.Line) + "] uses " + cleanLine(h.Symbol, pathRunes)
	if mark != "" {
		head += " (" + mark + ")"
	}
	return head + "\n" + cleanText(h.Snippet)
}

// cleanText sanitizes repository text for the prompt, with the rules of the
// discussion block: invalid UTF-8 is replaced, carriage returns become line
// feeds and control characters other than line feed and tab are dropped.
func cleanText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
	s = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimRight(s, " \t\n")
}

// cleanLine is cleanText for a value that must stay on one line (a path or a
// symbol), cut at limit runes.
func cleanLine(s string, limit int) string {
	s = strings.Join(strings.Fields(cleanText(s)), " ")
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit])
	}
	return s
}

// Outcome is what one prompt got of repository context.
type Outcome struct {
	// Status is llmrun.RepoUsed or llmrun.RepoSkipped; Reason is set when
	// skipped.
	Status, Reason string
	// Symbols is the number of symbols searched; References and Files count
	// the uses shown and their distinct files; Omitted the uses clipped
	// from the block.
	Symbols, References, Files, Omitted int
	// Names are the symbols searched, in rank order (for the diag dry run,
	// never logged).
	Names []string
	// Block is the text to put in the prompt; "" when no use was placed.
	// BlockTokens is its estimate.
	Block       string
	BlockTokens int
}

// Attach searches the uses of the symbols of files (the files whose content
// is in the diff of one prompt) and places the block into the room the diff
// leaves: the diff always wins.
//
// request0 is the estimated request without the block (tokens.RequestTokens)
// and trimmed0 whether the request-size guard already trimmed the diff; b is
// the budget the diff was prepared against. The block gets at most
// min(context.repo.max_tokens, window - soft reserve - request0) tokens,
// clipped by whole entries. render renders the final prompts with the block
// and reports whether the guard trimmed the diff; a block that makes it do
// so is dropped. A dropped block is Reason ReasonBudget, a gitctx failure
// the gitctx reason; neither is an error. sc is nil for a review in one call
// and the Scope of the part otherwise (see Scope).
func (s *Session) Attach(ctx context.Context, files []provider.FilePatch, sc *Scope, b tokens.Budget, request0 int,
	trimmed0 bool, render func(block string) (trimmed bool, err error)) Outcome {
	out := Outcome{Status: llmrun.RepoUsed}
	found := s.FindIn(ctx, files, sc)
	if found.Skipped != "" {
		return Outcome{Status: llmrun.RepoSkipped, Reason: found.Skipped, Symbols: found.Symbols, Names: found.Names}
	}
	out.Symbols, out.Names = found.Symbols, found.Names
	room := b.ContextWindow - b.SoftReserve() - request0
	if trimmed0 {
		room = 0
	}
	marks := s.marks
	if sc != nil {
		marks = sc.Marks
	}
	placed, err := Place(found.Hits, marks, s.settings.MaxTokens, room, b.Factor, func(text string) (bool, error) {
		trimmed, err := render(text)
		return !trimmed, err
	})
	if err != nil || placed.Reason != "" {
		return Outcome{Status: llmrun.RepoSkipped, Reason: ReasonBudget, Symbols: found.Symbols, Names: found.Names}
	}
	if placed.Block.Entries > 0 {
		out.Block, out.References, out.Files, out.Omitted = placed.Block.Text, placed.Block.Entries, placed.Block.Files, placed.Block.Omitted
		out.BlockTokens = tokens.Estimate(out.Block, b.Factor)
	}
	return out
}

// Summarize turns the outcomes of the prompts of a review into the
// structured coverage and the notes. One outcome per prompt; none means
// repository context is off.
//
// A review is "used" when at least one prompt searched the repository (also
// when it found nothing: the counts say 0), "skipped" with the first reason
// when none did. Symbols are the symbols searched, References and Files
// those shown, summed over the prompts.
func Summarize(outs []Outcome) (llmrun.RepoContext, []string) {
	if len(outs) == 0 {
		return llmrun.RepoContext{Status: llmrun.RepoOff}, nil
	}
	var rc llmrun.RepoContext
	var notes []string
	omitted, skipped, firstSkip := 0, 0, ""
	for _, o := range outs {
		if o.Status == llmrun.RepoSkipped {
			if skipped == 0 {
				firstSkip = o.Reason
			}
			skipped++
			continue
		}
		rc.Status = llmrun.RepoUsed
		rc.Symbols += o.Symbols
		rc.References += o.References
		rc.Files += o.Files
		omitted += o.Omitted
	}
	if rc.Status != llmrun.RepoUsed {
		return llmrun.RepoContext{Status: llmrun.RepoSkipped, Reason: firstSkip}, []string{NoteFor(firstSkip)}
	}
	if skipped > 0 {
		notes = append(notes, NotePartsSkipped(skipped, firstSkip))
	}
	if omitted > 0 {
		notes = append(notes, NoteClipped(omitted))
	}
	return rc, notes
}

// Placed is the outcome of Place.
type Placed struct {
	Block Block
	// Reason is ReasonBudget when there were uses but none was placed.
	Reason string
}

// Place chooses the block for hits: the most whole entries that fit
// min(maxTokens, room) tokens, accepted only if try approves the rendered
// text. room is what the request has left beside the diff; try renders the
// final prompts with the block and reports whether they still hold the whole
// diff (the diff always wins). Uses that do not fit are dropped, all of them
// with ReasonBudget.
func Place(hits []gitctx.Hit, marks map[string]string, maxTokens, room int, factor float64, try func(block string) (bool, error)) (Placed, error) {
	if len(hits) == 0 {
		return Placed{}, nil
	}
	b := RenderMarked(hits, min(maxTokens, room), factor, marks)
	if b.Entries == 0 {
		return Placed{Block: b, Reason: ReasonBudget}, nil
	}
	ok, err := try(b.Text)
	if err != nil {
		return Placed{}, err
	}
	if !ok {
		return Placed{Block: Block{Omitted: len(hits)}, Reason: ReasonBudget}, nil
	}
	return Placed{Block: b}, nil
}

// Report is the repository-context detail of a dry run (RC-9): the tokens of
// the block(s) and the symbols searched, for diag review --dry-run. It is
// nil when repository context is off.
type Report struct {
	// Tokens is the estimate of the block(s) in the prompt(s), summed over
	// the parts of a review in parts.
	Tokens int `json:"tokens"`
	// Symbols are the symbols searched, in rank order, without duplicates
	// across parts.
	Symbols []string `json:"symbols"`
}

// ReportOf builds the Report of the outcomes of a review's prompts; nil for
// none.
func ReportOf(outs []Outcome) *Report {
	if len(outs) == 0 {
		return nil
	}
	r := &Report{Symbols: []string{}}
	seen := map[string]bool{}
	for _, o := range outs {
		r.Tokens += o.BlockTokens
		for _, n := range o.Names {
			if !seen[n] {
				seen[n] = true
				r.Symbols = append(r.Symbols, n)
			}
		}
	}
	return r
}
