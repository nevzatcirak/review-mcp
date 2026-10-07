package gitctx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nevzatcirak/review-mcp/internal/filter"
)

// Limits of the search (WP-11b). They are fixed, not configuration keys: the
// spec adds only max_symbols and max_hits_per_symbol.
const (
	// DefaultContextLines is the number of lines kept before and after a
	// hit (RC-7).
	DefaultContextLines = 3
	maxContextLines     = 10
	// grepTotalTimeout bounds the whole Grep: the symbols still unsearched
	// when it passes are counted, not searched.
	grepTotalTimeout = 60 * time.Second
	// maxListOutput caps the object listing that finds the missing blobs
	// (rev-list, ls-tree). Past it the count of skipped files is a lower
	// bound.
	maxListOutput = 64 << 20
	// maxBlobOutput caps the blobs read for the context lines.
	maxBlobOutput = 32 << 20
	// maxSnippetLine cuts one snippet line; maxSymbolBytes and
	// maxPathspecBytes bound the command line (Windows allows 32 KiB).
	maxSnippetLine   = 400
	maxSymbolBytes   = 200
	maxPathspecBytes = 16 << 10
)

// Limits that tests lower.
var (
	// grepTimeout bounds one git invocation of the search. A search of a
	// bare repository of at most context.repo.max_repo_mb (500 MB by default)
	// takes a second or two; 15 s leaves room for a slow disk, and a
	// symbol that does not finish is skipped, not waited for.
	grepTimeout = 15 * time.Second
	// maxGrepOutput caps what one git grep may print. A common name in a big
	// repository prints megabytes; past the cap git is stopped and the hits
	// read so far are used.
	maxGrepOutput = 4 << 20
)

// Query is a search for the uses of symbols in a Checkout (WP-11b).
type Query struct {
	// Symbols are the names to search for, in rank order.
	Symbols []Symbol
	// Exclude are the pull request's own files (ChangedPaths): they are
	// excluded from the search by pathspec and dropped from the hits again.
	Exclude []string
	// Filter carries the ignore globs, the generated-file rules and the
	// lockfile, minified and binary-extension rules (internal/filter). Its
	// path rules become exclude pathspecs, and every hit is checked with
	// Include. nil applies none of it.
	Filter *filter.Filter
	// MaxHitsPerSymbol caps the hits per symbol
	// (context.repo.max_hits_per_symbol); 0 means DefaultMaxHitsPerSymbol.
	MaxHitsPerSymbol int
	// ContextLines is the number of lines kept before and after each hit; 0
	// means DefaultContextLines and a negative value none. At most 10.
	ContextLines int
}

// Hit is one use of a symbol found by Grep.
type Hit struct {
	Symbol string
	Path   string
	// Line is the 1-based line of the use.
	Line int
	// StartLine is the 1-based line of the first line of Snippet.
	StartLine int
	// Snippet is the line with its context lines, lines joined by "\n", each
	// cut at 400 bytes. It is untrusted repository text.
	Snippet string
}

// Result is what Grep found, with the counts of the coverage line (RC-9):
// "N symbols, M references from K files".
type Result struct {
	// Hits are the kept uses, symbol by symbol in the order of Query.Symbols.
	Hits []Hit
	// Symbols is the number of symbols searched.
	Symbols int
	// SymbolsWithHits is the number of symbols that have at least one hit.
	SymbolsWithHits int
	// Files is the number of distinct files among the hits.
	Files int
	// SkippedBlobs is the number of files that were not searched because
	// their blob is not in the cache: the fetch leaves out blobs above
	// 1 MiB and the search never fetches (GIT_NO_LAZY_FETCH, no protocol).
	// Only files that would have been searched are counted, not the pull
	// request's own, ignored or generated ones. A lower bound when the
	// repository listing exceeds 64 MiB.
	SkippedBlobs int
	// SkippedSymbols is the number of symbols not searched: an unusable
	// name, a search that timed out or failed, or the total time spent.
	SkippedSymbols int
	// Truncated is true when a search printed more than it was allowed to
	// and was stopped; its hits are the first ones.
	Truncated bool
}

// References is the number of hits, M of the coverage line.
func (r Result) References() int { return len(r.Hits) }

var (
	blobHeadRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64}) (\w+) (\d+)$`)
)

// Grep finds the uses of q.Symbols in the tree of c.HeadSHA.
//
// The search is "git grep -n -z -w -F -I -e <symbol> <head-sha> -- <pathspecs>"
// per symbol, against the head commit (a tree-ish) of the bare cache, never
// a working tree. The symbol is the value of -e, so a name that begins with
// "-" is not an option. The pathspecs exclude the pull request's own files,
// the files that cannot be searched because their blob is missing, and the
// path rules of q.Filter; a hit in the pull request's own files or in a
// file q.Filter does not include is dropped again afterwards, whatever git
// returned (defence in depth: the pull request's files are already in the
// prompt, and the pathspec list is clipped to keep the command line short).
//
// Every git process runs offline: the allowlisted environment of Ensure
// with no credential, protocol.allow=never with no https or http allow, and
// GIT_NO_LAZY_FETCH=1 (decision 8 on PR #13). A blob the partial clone does
// not hold therefore fails locally and at once; such files are found before
// the search (rev-list --missing=print), left out of it, and counted in
// Result.SkippedBlobs. It is never an error.
//
// The context lines are read from the blobs (git cat-file --batch) for the
// kept hits only, not printed by git grep -C: with -z, git prints a match
// and a context line the same way, so they cannot be told apart, and the
// blobs of at most max_symbols x max_hits_per_symbol hits are cheap to read.
// A file whose blob cannot be read keeps its hit with the matching line
// alone.
//
// Each git grep has a 15 second limit and the whole call 60 seconds; a
// symbol that times out is counted in SkippedSymbols and the next one is
// searched. Output past 4 MiB per symbol stops that git grep (Truncated).
// An error is returned only when no symbol could be searched at all; it is
// an *Error with a fixed reason (git's stderr is never passed on), or the
// context's own error when ctx is canceled.
func (r *Runner) Grep(ctx context.Context, c Checkout, q Query) (Result, error) {
	var res Result
	if err := ctx.Err(); err != nil {
		return res, err
	}
	sha := strings.ToLower(c.HeadSHA)
	if !shaRE.MatchString(sha) || c.GitDir == "" {
		return res, fail(ReasonUnsupported)
	}
	probe := gitProbe(r.opts.GitPath)
	if !probe.ok() {
		return res, fail(ReasonGitUnavailable)
	}
	root, err := r.openRoot(false)
	if err != nil {
		return res, fail(ReasonCache)
	}
	entry := filepath.Dir(c.GitDir)
	rel, relErr := filepath.Rel(root, entry)
	if !within(root, entry) || relErr != nil || filepath.Base(c.GitDir) != gitDirName || !realDirs(root, rel) {
		return res, fail(ReasonCache)
	}
	if fi, err := os.Lstat(c.GitDir); err != nil || !fi.IsDir() {
		return res, fail(ReasonCache)
	}
	// Tell the LRU sweep this entry is in use; best effort.
	_ = touch(filepath.Join(entry, markerName))

	maxHits := q.MaxHitsPerSymbol
	if maxHits <= 0 {
		maxHits = DefaultMaxHitsPerSymbol
	}
	ctxLines := q.ContextLines
	switch {
	case ctxLines == 0:
		ctxLines = DefaultContextLines
	case ctxLines < 0:
		ctxLines = 0
	case ctxLines > maxContextLines:
		ctxLines = maxContextLines
	}

	total, cancel := context.WithTimeout(ctx, grepTotalTimeout)
	defer cancel()
	g := &gitRun{path: probe.path, gitDir: c.GitDir, dir: entry, home: homeDir(root)}
	s := &searcher{g: g, sha: sha, q: q, exclude: toSet(q.Exclude)}

	missing, err := s.missingFiles(total)
	if err != nil {
		return res, err
	}
	res.SkippedBlobs = len(missing)
	pathspecs := s.pathspecs(missing)

	var (
		firstErr error
		searched int
		perSym   [][]rawHit
	)
	seenSym := map[string]bool{}
	for _, sym := range q.Symbols {
		if seenSym[sym.Name] {
			continue
		}
		seenSym[sym.Name] = true
		if !usableSymbol(sym.Name) {
			res.SkippedSymbols++
			continue
		}
		if total.Err() != nil {
			res.SkippedSymbols++
			continue
		}
		raw, truncated, err := s.search(total, sym.Name, pathspecs)
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			if firstErr == nil {
				firstErr = err
			}
			res.SkippedSymbols++
			continue
		}
		searched++
		res.Truncated = res.Truncated || truncated
		perSym = append(perSym, chooseHits(sym, raw, maxHits))
	}
	if searched == 0 && firstErr != nil {
		return res, firstErr
	}
	res.Symbols = searched

	var picked []Hit
	for i := range perSym {
		for _, h := range perSym[i] {
			picked = append(picked, Hit{Symbol: h.symbol, Path: h.path, Line: h.line, StartLine: h.line, Snippet: h.text})
		}
		if len(perSym[i]) > 0 {
			res.SymbolsWithHits++
		}
	}
	s.addContext(total, picked, ctxLines)
	res.Hits = picked
	files := map[string]bool{}
	for _, h := range picked {
		files[h.Path] = true
	}
	res.Files = len(files)
	return res, nil
}

// usableSymbol reports whether a name can be passed to git as one pattern:
// no newline (git would read two patterns), no NUL, not empty, not huge. A
// leading "-" is fine: the name is the value of -e.
func usableSymbol(name string) bool {
	if name == "" || len(name) > maxSymbolBytes || !utf8.ValidString(name) {
		return false
	}
	return !strings.ContainsAny(name, "\x00\r\n")
}

func toSet(paths []string) map[string]bool {
	m := make(map[string]bool, len(paths))
	for _, p := range paths {
		m[p] = true
	}
	return m
}

// searcher is one Grep call.
type searcher struct {
	g       *gitRun
	sha     string
	q       Query
	exclude map[string]bool
}

// rawHit is one line git grep printed.
type rawHit struct {
	symbol string
	path   string
	line   int
	text   string
}

// keep is the post-filter: whatever git returned, a hit in the pull
// request's own files or in a file the filter does not include is dropped.
func (s *searcher) keep(path string) bool {
	if path == "" || s.exclude[path] {
		return false
	}
	return s.q.Filter == nil || s.q.Filter.Include(path)
}

// missingFiles lists the files that would be searched but whose blob is not
// in the cache. It asks git which objects of the head tree are missing
// (rev-list --objects --missing=print: the "?<oid>" lines), and only when
// there are some maps them to paths with ls-tree. Neither command reads a
// blob, and both run offline.
func (s *searcher) missingFiles(ctx context.Context) ([]string, error) {
	lctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	res := s.g.run(lctx, gitCmd{
		args:      []string{"rev-list", "--objects", "--missing=print", s.sha},
		offline:   true,
		maxStdout: maxListOutput,
	})
	if res.err != nil {
		return nil, s.fail(ctx, res)
	}
	want := map[string]bool{}
	for _, line := range strings.Split(string(res.stdout), "\n") {
		if line != "" && line[0] == '?' {
			want[strings.TrimSpace(line[1:])] = true
		}
	}
	if len(want) == 0 {
		return nil, nil
	}
	res = s.g.run(lctx, gitCmd{
		args:      []string{"ls-tree", "-r", "-z", s.sha},
		offline:   true,
		maxStdout: maxListOutput,
	})
	if res.err != nil {
		return nil, s.fail(ctx, res)
	}
	var out []string
	for _, ent := range bytes.Split(res.stdout, []byte{0}) {
		// "<mode> SP <type> SP <oid> TAB <path>"
		meta, p, ok := bytes.Cut(ent, []byte{'\t'})
		if !ok {
			continue
		}
		f := strings.Fields(string(meta))
		if len(f) != 3 || f[1] != "blob" || !want[f[2]] {
			continue
		}
		if path := string(p); s.keep(path) {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out, nil
}

// fail maps a failed git command to an error: the context's own error when
// the caller gave up, else the fixed reason of the failure.
func (s *searcher) fail(ctx context.Context, res result) error {
	if err := ctx.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	_, err := res.failure()
	return err
}

// pathspecs builds the exclude pathspecs, most important first, within a
// byte budget: the pull request's files, the files without a blob, then the
// filter's path rules. What does not fit is covered by the post-filter.
func (s *searcher) pathspecs(missing []string) []string {
	var out []string
	used := 0
	add := func(spec string) bool {
		if used+len(spec)+1 > maxPathspecBytes {
			return false
		}
		used += len(spec) + 1
		out = append(out, spec)
		return true
	}
	literal := func(p string) string { return ":(top,exclude,literal)" + p }
	own := append([]string(nil), s.q.Exclude...)
	sort.Strings(own)
	for _, p := range own {
		if p != "" && !add(literal(p)) {
			return out
		}
	}
	for _, p := range missing {
		if !add(literal(p)) {
			return out
		}
	}
	if s.q.Filter != nil {
		for _, spec := range s.q.Filter.ExcludePathspecs() {
			if !add(strings.Replace(spec, ":(exclude,", ":(top,exclude,", 1)) {
				return out
			}
		}
	}
	return out
}

// search runs git grep for one symbol and returns the hits that survive the
// post-filter.
func (s *searcher) search(ctx context.Context, symbol string, pathspecs []string) (hits []rawHit, truncated bool, err error) {
	gctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	args := []string{"grep", "-n", "-z", "-w", "-F", "-I", "--no-color", "-e", symbol, s.sha, "--"}
	args = append(args, pathspecs...)
	res := s.g.run(gctx, gitCmd{args: args, offline: true, maxStdout: maxGrepOutput})
	if res.err != nil {
		// Exit status 1 is "no match". Anything else is a failure.
		var ee interface{ ExitCode() int }
		if errors.As(res.err, &ee) && ee.ExitCode() == 1 {
			return nil, false, nil
		}
		return nil, false, s.fail(gctx, res)
	}
	prefix := s.sha + ":"
	out := res.stdout
	if res.truncated {
		// The last record may be cut: keep whole lines only.
		if i := bytes.LastIndexByte(out, '\n'); i >= 0 {
			out = out[:i+1]
		} else {
			out = nil
		}
	}
	for len(out) > 0 {
		// "<sha>:<path> NUL <line> NUL <text> LF"; the path may hold
		// anything but NUL, so the record is read field by field.
		p, rest, ok := bytes.Cut(out, []byte{0})
		if !ok || !bytes.HasPrefix(p, []byte(prefix)) {
			break
		}
		ln, rest, ok := bytes.Cut(rest, []byte{0})
		if !ok {
			break
		}
		n, convErr := strconv.Atoi(string(ln))
		text, rest, _ := bytes.Cut(rest, []byte{'\n'})
		out = rest
		if convErr != nil || n < 1 {
			break
		}
		path := string(p[len(prefix):])
		if !s.keep(path) {
			continue
		}
		hits = append(hits, rawHit{symbol: symbol, path: path, line: n, text: cutLine(string(text))})
	}
	return hits, res.truncated, nil
}

// chooseHits keeps at most max hits of one symbol: one hit per file first,
// the files of the definition's language group before the others, then more
// hits of the same files in the same order. Within a group files are in path
// order and hits in line order, so the choice is deterministic.
func chooseHits(sym Symbol, raw []rawHit, max int) []rawHit {
	if len(raw) == 0 || max <= 0 {
		return nil
	}
	byFile := map[string][]rawHit{}
	var files []string
	for _, h := range raw {
		if _, ok := byFile[h.path]; !ok {
			files = append(files, h.path)
		}
		byFile[h.path] = append(byFile[h.path], h)
	}
	want := filter.Language(sym.Path)
	same := func(p string) bool { return filter.Language(p) == want }
	sort.SliceStable(files, func(i, j int) bool {
		if si, sj := same(files[i]), same(files[j]); si != sj {
			return si
		}
		return files[i] < files[j]
	})
	for _, f := range files {
		hs := byFile[f]
		sort.SliceStable(hs, func(i, j int) bool { return hs[i].line < hs[j].line })
	}
	var out []rawHit
	for round := 0; len(out) < max; round++ {
		progress := false
		for _, f := range files {
			if round < len(byFile[f]) {
				out = append(out, byFile[f][round])
				progress = true
				if len(out) == max {
					break
				}
			}
		}
		if !progress {
			break
		}
	}
	return out
}

// addContext replaces each hit's snippet (the matching line) by the line
// with ctxLines lines around it, read from the blobs of the head tree with
// one git cat-file --batch. A hit whose blob cannot be read keeps the
// matching line alone.
func (s *searcher) addContext(ctx context.Context, hits []Hit, ctxLines int) {
	if ctxLines == 0 || len(hits) == 0 {
		return
	}
	var paths []string
	index := map[string]int{}
	for _, h := range hits {
		if strings.ContainsAny(h.Path, "\r\n") {
			continue // cat-file reads one specification per line
		}
		if _, ok := index[h.Path]; !ok {
			index[h.Path] = len(paths)
			paths = append(paths, h.Path)
		}
	}
	if len(paths) == 0 {
		return
	}
	var in bytes.Buffer
	for _, p := range paths {
		in.WriteString(s.sha + ":" + p + "\n")
	}
	cctx, cancel := context.WithTimeout(ctx, grepTimeout)
	defer cancel()
	res := s.g.run(cctx, gitCmd{
		args:      []string{"cat-file", "--batch"},
		offline:   true,
		stdin:     in.Bytes(),
		maxStdout: maxBlobOutput,
	})
	if res.err != nil {
		return
	}
	blobs := make([][]string, len(paths))
	rest := res.stdout
	for i := range paths {
		nl := bytes.IndexByte(rest, '\n')
		if nl < 0 {
			break
		}
		head := string(rest[:nl])
		rest = rest[nl+1:]
		m := blobHeadRE.FindStringSubmatch(head)
		if m == nil {
			continue // "<spec> missing": no body follows
		}
		size, _ := strconv.Atoi(m[3])
		if size > len(rest) {
			break // cut by the output cap
		}
		body := rest[:size]
		rest = rest[size:]
		if len(rest) > 0 && rest[0] == '\n' {
			rest = rest[1:]
		}
		if m[2] == "blob" {
			blobs[i] = strings.Split(string(body), "\n")
		}
	}
	for i := range hits {
		j, ok := index[hits[i].Path]
		if !ok || blobs[j] == nil {
			continue
		}
		lines := blobs[j]
		at := hits[i].Line - 1
		if at < 0 || at >= len(lines) {
			continue
		}
		from, to := max(0, at-ctxLines), min(len(lines), at+ctxLines+1)
		var b strings.Builder
		for k := from; k < to; k++ {
			if k > from {
				b.WriteByte('\n')
			}
			b.WriteString(cutLine(strings.TrimSuffix(lines[k], "\r")))
		}
		hits[i].Snippet, hits[i].StartLine = b.String(), from+1
	}
}

// cutLine trims a trailing CR and cuts a line at maxSnippetLine bytes on a
// character boundary.
func cutLine(s string) string {
	s = strings.TrimSuffix(s, "\r")
	if len(s) <= maxSnippetLine {
		return s
	}
	cut := maxSnippetLine
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
