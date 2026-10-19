package gitctx

import (
	"context"
	"sort"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/filter"
)

// Term is one fixed string of a consumer search (v2.5 spec V-13b §2.2): an
// identifier the primary pull request removes, renames or changes, searched
// as a whole word, or a route prefix, searched anywhere in a line.
type Term struct {
	Text string
	Word bool
}

// TermQuery is the search of a related repository for the changed contract
// surface of a pull request.
type TermQuery struct {
	// Terms are searched in order, each once.
	Terms []Term
	// Filter leaves out the files it does not include (the operator's
	// ignore.* rules and the generated, lockfile, minified and binary
	// rules), by pathspec and again by the post-filter. nil applies none.
	// Test files are searched: a consumer's tests are consumers too.
	Filter *filter.Filter
	// Paths, when set, confines the search to these directory prefixes
	// (slash-separated, relative to the root); a prefix that cannot name a
	// path is refused (unsupported).
	Paths []string
	// MaxPerTerm caps the hits kept per term and MaxTotal the hits kept in
	// all (0: no cap); the first ones by path, then line, are kept.
	// ContextLines is the number of lines kept before and after a hit (at
	// most 10).
	MaxPerTerm, MaxTotal, ContextLines int
}

// TermResult is what SearchTerms found.
type TermResult struct {
	// Hits are the kept hits, term by term in the order of TermQuery.Terms,
	// each term's in path, then line order; Hit.Symbol is the term's text.
	Hits []Hit
	// Found counts every hit of every searched term, before the caps (a
	// lower bound when Truncated).
	Found int
	// Searched counts the terms searched; Skipped those not searched (an
	// unusable text, a search that timed out or failed, or the total time
	// spent).
	Searched, Skipped int
	// SkippedBlobs counts the files not searched because the partial clone
	// lacks their blob (never fetched).
	SkippedBlobs int
	// Truncated is true when a search printed more than it was allowed to;
	// its hits are the first ones.
	Truncated bool
}

// SearchTerms searches the tree of c.HeadSHA (the pinned commit of a related
// repository) for q.Terms: "git grep -n -z [-w] -F -I -e <term> <commit> --
// <pathspecs>" per term, with every rule of Grep: offline (no credential,
// protocol.allow=never, GIT_NO_LAZY_FETCH=1), files without a blob left out
// and counted, 15 s per search and 60 s in all, 4 MiB of output per search,
// the term as the value of -e so that it is never an option. Nothing in the
// repository is built or run. The context lines are read from the blobs of
// the kept hits. An error is returned only when no term could be searched
// at all; it is an *Error with a fixed reason, or ctx's own error.
func (r *Runner) SearchTerms(ctx context.Context, c Checkout, q TermQuery) (TermResult, error) {
	var res TermResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	sha := strings.ToLower(c.HeadSHA)
	if !shaRE.MatchString(sha) {
		return res, fail(ReasonUnsupported)
	}
	prefixes, ok := cleanPrefixes(q.Paths)
	if !ok {
		return res, fail(ReasonUnsupported)
	}
	g, err := r.openCheckout(c)
	if err != nil {
		return res, err
	}
	total, cancel := context.WithTimeout(ctx, grepTotalTimeout)
	defer cancel()
	s := &searcher{g: g, sha: sha, q: Query{Filter: q.Filter}, exclude: map[string]bool{}}
	missing, err := s.missingFiles(total)
	if err != nil {
		return res, err
	}
	res.SkippedBlobs = len(missing)
	var specs []string
	for _, p := range prefixes {
		specs = append(specs, ":(top,literal)"+p)
	}
	specs = append(specs, s.pathspecs(missing)...)
	within := func(path string) bool { return len(prefixes) == 0 || withinPrefixes(path, prefixes) }

	var firstErr error
	seen := map[string]bool{}
	for _, t := range q.Terms {
		if seen[t.Text] {
			continue
		}
		seen[t.Text] = true
		if !usableSymbol(t.Text) || total.Err() != nil {
			res.Skipped++
			continue
		}
		raw, truncated, err := s.search(total, t.Text, specs, t.Word)
		if err != nil {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			if firstErr == nil {
				firstErr = err
			}
			res.Skipped++
			continue
		}
		res.Searched++
		res.Truncated = res.Truncated || truncated
		kept := raw[:0]
		for _, h := range raw {
			if within(h.path) {
				kept = append(kept, h)
			}
		}
		res.Found += len(kept)
		sort.SliceStable(kept, func(i, j int) bool {
			if kept[i].path != kept[j].path {
				return kept[i].path < kept[j].path
			}
			return kept[i].line < kept[j].line
		})
		for i, h := range kept {
			if (q.MaxPerTerm > 0 && i == q.MaxPerTerm) || (q.MaxTotal > 0 && len(res.Hits) == q.MaxTotal) {
				break
			}
			res.Hits = append(res.Hits, Hit{Symbol: t.Text, Path: h.path, Line: h.line, StartLine: h.line, Snippet: h.text})
		}
	}
	if res.Searched == 0 && firstErr != nil {
		return res, firstErr
	}
	if n := min(q.ContextLines, maxContextLines); n > 0 {
		s.addContext(total, res.Hits, n)
	}
	return res, nil
}

// cleanPrefixes returns the directory prefixes of a confined search without
// a trailing "/"; ok is false when one cannot name a path.
func cleanPrefixes(paths []string) ([]string, bool) {
	prefixes := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSuffix(p, "/")
		if p == "" || !usablePath(p) {
			return nil, false
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, true
}

// withinPrefixes reports whether path is one of prefixes or lies below one.
func withinPrefixes(path string, prefixes []string) bool {
	for _, p := range prefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
