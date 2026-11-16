package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/nevzatcirak/review-mcp/tools/evalset/cases"
)

// largeCaseID is the 2,000-token case (amendment 2026-11-16 item 4): its
// chain needs more than 2,000 tokens of repository context. It counts in
// cascade recall and recall by hops, and is reported once more on its own
// row (WP-21a DQ-2, DQ-5).
const largeCaseID = "go-large-cascade"

// Matching rules (V-9 spec §2.3).
const (
	ruleLocation = "location"
	ruleMention  = "mention"
	ruleJudge    = "judge"
	// widen is how far a location may lie outside the issue's lines.
	widen = 3
	// mentionNeed is how many distinct entries a mention match needs: the
	// anchor (the issue file's base name or a must_mention identifier) and
	// one more must_mention entry (canary c2).
	mentionNeed = 2
)

// scored is the outcome of one finding.
type scored struct {
	f finding
	// issue is the index of the matched issue, -1 for none.
	issue int
	rule  string
	// verdict and reason are the judge's, for a false-positive candidate
	// the judge examined.
	verdict, reason string
}

// runScore is the scored outcome of one run.
type runScore struct {
	rec      runRecord
	c        *cases.Case
	findings []scored
	// matched[i] is the rule that matched issue i ("" when missed).
	matched []string
}

// matchRun applies the matching rules to the findings of one run. Each
// finding matches at most one issue and each issue at most one finding,
// greedily by issue severity, then by line distance.
func matchRun(c *cases.Case, fs []finding) ([]scored, []string) {
	type cand struct {
		fi, ii, dist int
		rule         string
	}
	var cands []cand
	for ii, is := range c.Issues {
		for fi, f := range fs {
			if d, ok := locationMatch(f, is); ok {
				cands = append(cands, cand{fi, ii, d, ruleLocation})
			} else if !locatedIn(f, is.Path) && mentionMatch(f, is) {
				cands = append(cands, cand{fi, ii, 1 << 30, ruleMention})
			}
		}
	}
	sort.SliceStable(cands, func(a, b int) bool {
		x, y := cands[a], cands[b]
		if r, s := sevRank(c.Issues[x.ii].Severity), sevRank(c.Issues[y.ii].Severity); r != s {
			return r < s
		}
		if x.dist != y.dist {
			return x.dist < y.dist
		}
		if x.ii != y.ii {
			return x.ii < y.ii
		}
		return x.fi < y.fi
	})
	out := make([]scored, len(fs))
	for i, f := range fs {
		out[i] = scored{f: f, issue: -1}
	}
	matched := make([]string, len(c.Issues))
	for _, k := range cands {
		if out[k.fi].issue >= 0 || matched[k.ii] != "" {
			continue
		}
		out[k.fi].issue, out[k.fi].rule = k.ii, k.rule
		matched[k.ii] = k.rule
	}
	return out, matched
}

func sevRank(s string) int {
	switch s {
	case "high":
		return 0
	case "medium":
		return 1
	}
	return 2
}

func sevWeight(s string) int {
	switch s {
	case "high":
		return 3
	case "medium":
		return 2
	}
	return 1
}

// normPath makes a finding's path comparable to an issue path.
func normPath(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
	for strings.HasPrefix(p, "./") || strings.HasPrefix(p, "/") {
		p = strings.TrimPrefix(strings.TrimPrefix(p, "./"), "/")
	}
	return p
}

// locatedIn reports whether the finding has a location in the file p.
func locatedIn(f finding, p string) bool {
	return f.Start > 0 && normPath(f.File) == p
}

// locationMatch is rule 1: same path, and the finding's lines overlap the
// issue's lines widened by 3 on each side. The distance is 0 when the lines
// overlap the issue's own lines, else the gap.
func locationMatch(f finding, is cases.Issue) (int, bool) {
	if !locatedIn(f, is.Path) {
		return 0, false
	}
	s, e := f.Start, max(f.End, f.Start)
	lo, hi := is.Lines[0], is.Lines[1]
	if s > hi+widen || e < lo-widen {
		return 0, false
	}
	switch {
	case s > hi:
		return s - hi, true
	case e < lo:
		return lo - e, true
	}
	return 0, true
}

// mentionMatch is rule 2, for a finding without a location in the issue's
// file: its text names an anchor (the issue file's base name, or a
// must_mention entry that is a code identifier) and holds at least
// mentionNeed distinct entries in all, the base name counting as one.
// Identifiers match case-sensitively, words case-insensitively.
func mentionMatch(f finding, is cases.Issue) bool {
	text := f.Header + "\n" + f.Content
	hits := map[string]bool{}
	anchored := false
	base := path.Base(is.Path)
	if containsIdent(text, base) {
		hits[base] = true
		anchored = true
	}
	for _, m := range is.MustMention {
		if isIdentifier(m) {
			if containsIdent(text, m) {
				hits[m] = true
				anchored = true
			}
		} else if containsWord(text, m) {
			hits[m] = true
		}
	}
	return anchored && len(hits) >= mentionNeed
}

// isIdentifier reports whether a must_mention entry is a code identifier:
// one token with a letter that is not a plain lower-case word (RenewalJob,
// errors.Is, merge.go, overdue[0], EUR). "currency", "sign-in" and "0 cents"
// are words.
func isIdentifier(s string) bool {
	if s == "" || strings.ContainsFunc(s, unicode.IsSpace) || !strings.ContainsFunc(s, unicode.IsLetter) {
		return false
	}
	return strings.ContainsFunc(s, func(r rune) bool { return (r < 'a' || r > 'z') && r != '-' })
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// containsIdent finds s in text case-sensitively, not as part of a longer
// identifier: an edge of s that is a word character must not touch another.
func containsIdent(text, s string) bool {
	return findBounded(text, s)
}

// containsWord finds s in text case-insensitively, as a whole word or
// phrase ("renew" does not match "renewal").
func containsWord(text, s string) bool {
	return findBounded(strings.ToLower(text), strings.ToLower(s))
}

func findBounded(text, s string) bool {
	if s == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(text[from:], s)
		if i < 0 {
			return false
		}
		i += from
		j := i + len(s)
		okLeft := !isWordByte(s[0]) || i == 0 || !isWordByte(text[i-1])
		okRight := !isWordByte(s[len(s)-1]) || j == len(text) || !isWordByte(text[j])
		if okLeft && okRight {
			return true
		}
		from = i + 1
	}
}

func cmdScore(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("evalset score", flag.ContinueOnError)
	fs.SetOutput(stderr)
	runs := fs.String("runs", "", "the `directory` of evalset run")
	corpus := fs.String("corpus", filepath.Join("tools", "evalset", "corpus"), "the corpus `directory`")
	judge := fs.Bool("judge", false, "let a judge model re-examine the unmatched findings (settings from EVALSET_JUDGE_*)")
	seed := fs.Int64("seed", 1, "`seed` of the blind sample of judged items")
	out := fs.String("out", "", "`directory` for the report (required; new or empty)")
	cacheFlag := fs.String("cache-dir", "", "the repository-context cache `directory` (default: the one recorded in run.json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(code int, format string, a ...any) int {
		_, _ = fmt.Fprintf(stderr, "evalset score: "+format+"\n", a...)
		return code
	}
	if fs.NArg() > 0 {
		return fail(2, "unexpected argument")
	}
	if *runs == "" {
		return fail(2, "-runs is required")
	}
	var rf runFile
	if err := readJSON(filepath.Join(*runs, "run.json"), &rf); err != nil {
		return fail(2, "cannot read run.json in -runs")
	}
	all, err := cases.Load(os.DirFS(*corpus))
	if err != nil {
		return fail(2, "cannot read the corpus in -corpus")
	}
	byID := map[string]*cases.Case{}
	for _, c := range all {
		byID[c.ID] = c
	}
	var jc *judgeConfig
	if *judge {
		cfg, err := judgeFromEnv(getenv)
		if err != nil {
			return fail(2, "%s", err.Error())
		}
		// Refused before any request when the judge is the reviewer.
		if err := checkJudgeModel(cfg.Model, reviewerModels(rf)); err != nil {
			return fail(2, "%s", err.Error())
		}
		jc = &cfg
	}
	cache := *cacheFlag
	if cache == "" {
		cache = rf.CacheDir
	}
	if err := checkOut("-out", *out, cache); err != nil {
		return fail(2, "%s", err.Error())
	}

	var scoredRuns []*runScore
	for _, rec := range rf.Runs {
		c, ok := byID[rec.Case]
		if !ok {
			return fail(2, "run.json names the case %s, which is not in the corpus", rec.Case)
		}
		rs := &runScore{rec: rec, c: c, matched: make([]string, len(c.Issues))}
		if rec.OK {
			var res reviewResult
			if err := readJSON(filepath.Join(*runs, filepath.FromSlash(rec.File)), &res); err != nil || res.Review == nil {
				return fail(2, "cannot read the result of case %s, variant %s, repeat %d", rec.Case, rec.Variant, rec.Repeat)
			}
			rs.findings, rs.matched = matchRun(c, res.Review.Issues)
		}
		scoredRuns = append(scoredRuns, rs)
	}
	judged := 0
	if jc != nil {
		n, errs := runJudge(ctx, *jc, scoredRuns)
		judged = n
		if errs > 0 {
			_, _ = fmt.Fprintf(stderr, "evalset score: the judge could not answer %d items; they stay false positives\n", errs)
		}
	}
	rep := buildReport(rf, scoredRuns, jc)
	if err := writeOutputs(*out, rep, scoredRuns, jc != nil, *seed); err != nil {
		return fail(1, "cannot write the outputs")
	}
	_, _ = fmt.Fprintf(stdout, "evalset score: %d runs (%d failed), %d findings, %d matched, %d judged; see %s\n",
		len(scoredRuns), rep.failedRuns, rep.findings, rep.matchedFindings, judged, filepath.Join(*out, "report.md"))
	return 0
}

// reviewerModels are the models recorded in the runs.
func reviewerModels(rf runFile) []string {
	seen := map[string]bool{}
	var out []string
	add := func(m string) {
		if m = strings.TrimSpace(m); m != "" && !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	for _, r := range rf.Runs {
		add(r.Model)
		for _, m := range r.ModelsUsed {
			add(m)
		}
	}
	sort.Strings(out)
	return out
}
