package diffpipe

import (
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// --- fixtures -------------------------------------------------------------

func ptr(s string) *string { return &s }

func lines(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("    %s_%d = compute(%d)\n", prefix, i, i)
	}
	return out
}

// added returns an added file of n lines.
func added(path string, n int) provider.FilePatch {
	ls := lines("value", n)
	var b strings.Builder
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", n)
	for _, l := range ls {
		b.WriteString("+" + l)
	}
	return provider.FilePatch{Path: path, Type: provider.ChangeAdded, Patch: b.String(),
		HeadContent: ptr(strings.Join(ls, "")), BaseStatus: provider.ContentNotApplicable, HeadStatus: provider.ContentFull}
}

// modified returns a file of n lines whose every step-th line changed.
func modified(path string, n, step int) provider.FilePatch {
	base := lines("value", n)
	head := slices.Clone(base)
	var b strings.Builder
	for i := 0; i < n; i += step {
		head[i] = fmt.Sprintf("    changed_%d = compute(%d)\n", i, i+1)
		fmt.Fprintf(&b, "@@ -%d +%d @@\n-%s+%s", i+1, i+1, base[i], head[i])
	}
	return provider.FilePatch{Path: path, Type: provider.ChangeModified, Patch: b.String(),
		BaseContent: ptr(strings.Join(base, "")), HeadContent: ptr(strings.Join(head, "")),
		BaseStatus: provider.ContentFull, HeadStatus: provider.ContentFull}
}

// deleted returns a deleted file of n lines.
func deleted(path string, n int) provider.FilePatch {
	ls := lines("value", n)
	var b strings.Builder
	fmt.Fprintf(&b, "@@ -1,%d +0,0 @@\n", n)
	for _, l := range ls {
		b.WriteString("-" + l)
	}
	return provider.FilePatch{Path: path, Type: provider.ChangeDeleted, Patch: b.String(),
		BaseContent: ptr(strings.Join(ls, "")), BaseStatus: provider.ContentFull, HeadStatus: provider.ContentNotApplicable}
}

func renamed(path string, n, step int) provider.FilePatch {
	f := modified(path, n, step)
	f.Type, f.OldPath = provider.ChangeRenamed, "old/"+path
	return f
}

func diffCfg(policy string) config.Diff {
	return config.Diff{ExtraLinesBefore: 5, ExtraLinesAfter: 1, SkipExtendExtensions: []string{".md", ".txt"},
		LargePatchPolicy: policy}
}

// budget returns a budget whose soft limit is soft (factor 0.3, no prompt).
func budget(soft int) tokens.Budget {
	return tokens.Budget{ContextWindow: soft + 1500, Factor: 0.3}
}

func mustPrepare(t *testing.T, in Input) *Prepared {
	t.Helper()
	p, err := Prepare(in)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	checkAccounting(t, in, p)
	return p
}

// --- API and budget -------------------------------------------------------

func TestPrepareRequiresCapacity(t *testing.T) {
	in := Input{Files: []provider.FilePatch{added("a.go", 3)}, Budget: tokens.Budget{ContextWindow: 1500, Factor: 0.3},
		Diff: diffCfg("clip")}
	if _, err := Prepare(in); !errors.Is(err, tokens.ErrDoesNotFit) {
		t.Fatalf("err = %v, want ErrDoesNotFit", err)
	}
}

func TestPrepareRejectsUnknownMode(t *testing.T) {
	in := Input{Mode: Mode(7), Budget: budget(1000), Diff: diffCfg("clip")}
	if _, err := Prepare(in); err == nil || errors.Is(err, tokens.ErrDoesNotFit) {
		t.Fatalf("err = %v, want an unknown-mode error", err)
	}
}

func TestFastPathKeepsGroupThenProviderOrder(t *testing.T) {
	in := Input{
		Files: []provider.FilePatch{
			added("b.py", 2), added("z.go", 10), added("a.go", 2), added("notes.zzz", 40), added("a.py", 2),
		},
		Budget: budget(100000), Diff: diffCfg("clip"),
	}
	p := mustPrepare(t, in)
	if !p.FastPath {
		t.Fatal("FastPath = false")
	}
	// Go weighs more than Python; "Other" is last although it is heaviest.
	want := []string{"z.go", "a.go", "b.py", "a.py", "notes.zzz"}
	if !slices.Equal(p.Included, want) {
		t.Fatalf("Included = %q, want %q", p.Included, want)
	}
	if p.Tokens != tokens.Estimate(p.Text, 0.3) {
		t.Fatalf("Tokens = %d, want Estimate(Text) = %d", p.Tokens, tokens.Estimate(p.Text, 0.3))
	}
	if len(p.Omitted.Added)+len(p.Omitted.Modified)+len(p.Omitted.Deleted)+len(p.Clipped)+len(p.DeletedListed) != 0 {
		t.Fatalf("fast path omitted, clipped or listed files: %+v", p)
	}
}

func TestRankTiesByNameOtherLast(t *testing.T) {
	fps := []provider.FilePatch{
		{Path: "x.zzz", Patch: strings.Repeat("x", 100)},
		{Path: "b.rb", Patch: "1234"},
		{Path: "a.py", Patch: "12"},
		{Path: "c.go", Patch: "1234"},
		{Path: "d.py", Patch: "34"},
	}
	files := make([]*file, len(fps))
	for i := range fps {
		files[i] = &file{fp: &fps[i]}
	}
	var got []string
	for _, g := range rank(files) {
		got = append(got, fmt.Sprintf("%s:%d", g.lang, g.weight))
	}
	want := []string{"Go:4", "Python:4", "Ruby:4", "Other:100"}
	if !slices.Equal(got, want) {
		t.Fatalf("groups = %q, want %q", got, want)
	}
}

func TestEmptyAndUnparseableFilesAreSkipped(t *testing.T) {
	pure := provider.FilePatch{Path: "moved.go", OldPath: "old.go", Type: provider.ChangeRenamed}
	bad := provider.FilePatch{Path: "bad.go", Type: provider.ChangeModified, Patch: "@@ -1 +1 @@\nno prefix\n"}
	in := Input{
		Files:   []provider.FilePatch{pure, added("a.go", 3), bad},
		Skipped: []provider.SkippedFile{{Path: "logo.png", Reason: provider.SkipBinary}},
		Budget:  budget(100000), Diff: diffCfg("clip"),
	}
	p := mustPrepare(t, in)
	want := []provider.SkippedFile{
		{Path: "logo.png", Reason: provider.SkipBinary},
		{Path: "moved.go", Reason: SkipEmptyDiff},
		{Path: "bad.go", Reason: SkipUnparseablePatch},
	}
	if !slices.Equal(p.Skipped, want) {
		t.Fatalf("Skipped = %v, want %v", p.Skipped, want)
	}
}

func TestEmptyFastPath(t *testing.T) {
	in := Input{Files: []provider.FilePatch{{Path: "moved.go", Type: provider.ChangeRenamed}},
		Budget: budget(1000), Diff: diffCfg("clip")}
	p := mustPrepare(t, in)
	if p.Text != "" || !p.FastPath || p.Tokens != 0 {
		t.Fatalf("got %+v, want an empty fast-path diff", p)
	}
}

// --- compressed path --------------------------------------------------------

// compressedInput has more content than soft allows: Go files of growing
// size, a renamed and a deleted file, one Python file.
func compressedInput(soft int, mode Mode) Input {
	return Input{
		Files: []provider.FilePatch{
			modified("small.go", 40, 20), modified("big.go", 200, 3), modified("mid.go", 100, 5),
			renamed("moved.go", 100, 4), deleted("gone.go", 60), added("new.py", 120), added("tiny.py", 3),
		},
		Mode: mode, Budget: budget(soft), Diff: diffCfg("clip"),
	}
}

func TestCompressedAdmission(t *testing.T) {
	// Compressed-render estimates (factor 0.3), plain / numbered: big.go
	// 2363/3321, moved.go 890/1247, mid.go 713/999, small.go 81/110,
	// new.py 1585/1748, tiny.py 64/75; the Go group is ranked by the
	// fast-path estimates: big, moved, mid, small. The soft limits admit
	// mid + small + tiny (+ separators) but not moved.
	for mode, soft := range map[Mode]int{ModePlain: 880, ModeNumbered: 1220} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			in := compressedInput(soft, mode)
			p := mustPrepare(t, in)
			if p.FastPath {
				t.Fatal("FastPath = true")
			}
			// Go group first, by size: big.go and moved.go do not fit, mid.go
			// and small.go do; gone.go is listed by name only (X-20:
			// DeletedListed, not Omitted).
			if !slices.Equal(p.Included, []string{"mid.go", "small.go", "tiny.py"}) {
				t.Fatalf("Included = %q", p.Included)
			}
			if !slices.Equal(p.DeletedListed, []string{"gone.go"}) {
				t.Fatalf("DeletedListed = %q", p.DeletedListed)
			}
			if !slices.Equal(p.Omitted.Modified, []string{"big.go", "moved.go"}) ||
				len(p.Omitted.Deleted) != 0 ||
				!slices.Equal(p.Omitted.Added, []string{"new.py"}) {
				t.Fatalf("Omitted = %+v", p.Omitted)
			}
			// Sections in upstream order and format.
			want := "\n\n" + patch.AddedFilesHeader + "\nnew.py" +
				"\n\n" + patch.ModifiedFilesHeader + "\nbig.go\nmoved.go" +
				"\n\n" + patch.DeletedFilesHeader + "\ngone.go"
			if !strings.HasSuffix(p.Text, want) {
				t.Fatalf("text does not end with the sections:\n%q", p.Text[max(0, len(p.Text)-400):])
			}
			if p.Tokens > in.Budget.HardLimit() {
				t.Fatalf("Tokens = %d > hard limit %d", p.Tokens, in.Budget.HardLimit())
			}
		})
	}
}

func TestCompressedOnlyDeletedFiles(t *testing.T) {
	in := Input{Files: []provider.FilePatch{deleted("a.go", 300), deleted("b.go", 300)},
		Budget: budget(500), Diff: diffCfg("skip")}
	p := mustPrepare(t, in)
	want := "\n\n" + patch.DeletedFilesHeader + "\na.go\nb.go"
	if p.Text != want || len(p.Included) != 0 || !slices.Equal(p.DeletedListed, []string{"a.go", "b.go"}) ||
		len(p.Omitted.Deleted) != 0 {
		t.Fatalf("got text %q, %+v", p.Text, p)
	}
}

// TestCompressedFetchFailed: architect decision D3 (PR #4). On the
// compressed path a fetch-failed file with a patch is rendered normally,
// deletion handling included (its deletion-only hunk is dropped), and only a
// fetch-failed file with an empty patch renders the unreadable notice.
func TestCompressedFetchFailed(t *testing.T) {
	gitea := provider.FilePatch{Path: "gitea.go", Type: provider.ChangeModified,
		Patch:       "@@ -1 +1 @@\n-old_line\n+new_line\n@@ -10 +9,0 @@\n-dropped_line\n",
		BaseContent: ptr("base\n"), BaseStatus: provider.ContentFull, HeadStatus: provider.ContentFetchFailed}
	empty := provider.FilePatch{Path: "empty.go", Type: provider.ChangeModified,
		BaseStatus: provider.ContentFull, HeadStatus: provider.ContentFetchFailed}
	for _, mode := range []Mode{ModePlain, ModeNumbered} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			in := Input{Files: []provider.FilePatch{added("big.py", 400), gitea, empty},
				Mode: mode, Budget: budget(600), Diff: diffCfg("clip")}
			p := mustPrepare(t, in)
			if p.FastPath {
				t.Fatal("FastPath = true")
			}
			if !slices.Contains(p.Included, "gitea.go") || !slices.Contains(p.Included, "empty.go") {
				t.Fatalf("Included = %q", p.Included)
			}
			if !strings.Contains(p.Text, "new_line") || strings.Contains(p.Text, "dropped_line") {
				t.Errorf("gitea.go: want the patch without its deletion-only hunk:\n%s", p.Text)
			}
			if n := strings.Count(p.Text, "could not be read"); n != 1 ||
				!strings.Contains(p.Text, "## File: 'empty.go'\n\n> **This file could not be read.** review-mcp failed") ||
				strings.Contains(p.Text, "PR-Agent") {
				t.Errorf("want exactly one notice, for empty.go (got %d):\n%s", n, p.Text)
			}
		})
	}
}

func TestCompressedStableTiesKeepProviderOrder(t *testing.T) {
	var fps []provider.FilePatch
	for _, n := range []string{"c", "a", "d", "b"} {
		f := added(n+".go", 30)
		fps = append(fps, f)
	}
	fps = append(fps, added("filler.go", 400))
	// Each tie costs 415, filler.go 5226 and ranks first (largest); the
	// four ties plus three separators fit in 1700.
	p := mustPrepare(t, Input{Files: fps, Budget: budget(1700), Diff: diffCfg("clip")})
	if !slices.Equal(p.Included, []string{"c.go", "a.go", "d.go", "b.go"}) ||
		!slices.Equal(p.Omitted.Added, []string{"filler.go"}) {
		t.Fatalf("Included = %q, Omitted = %+v", p.Included, p.Omitted)
	}
}

// TestHardLimitSkip drives admit with a running total above the hard
// limit. Prepare cannot reach this branch (the soft limit is always 500
// below the hard one, and admission never passes the soft limit), but it is
// upstream's and kept for parity.
func TestHardLimitSkip(t *testing.T) {
	c := newCounter(0, func(s string) int { return len(s) })
	es := []entry{{text: "aaaa", tokens: 4}, {text: "bb", tokens: 2}, {text: "c", tokens: 1}}
	// soft 100 > hard 3: the first entry pushes the running total over hard.
	if got := admit(c, es, 100, 3); !slices.Equal(got, []int{0}) {
		t.Fatalf("admit = %v, want [0]", got)
	}
	if got := admit(c, es, 100, 100); !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("admit = %v, want [0 1 2]", got)
	}
}

func TestVerifiedPrefixMirrorsUpstream(t *testing.T) {
	for _, tc := range []struct {
		maxLen int
		fits   []bool // fits[n] for prefix length n
		want   int
	}{
		{3, []bool{true, true, true, false, false}, 2},
		{4, []bool{true, true, false, false, false}, 1},
		{4, []bool{true, false, false, false, false}, 0},
		// Non-monotone: 1 does not fit but 3 does; the search still lands
		// on a verified length, and may miss a longer one.
		{4, []bool{true, false, true, true, false}, 3},
		{4, []bool{true, true, false, true, false}, 1},
		{4, []bool{true, true, false, false, true}, 1},
		{0, []bool{true, true}, 0},
	} {
		var probes []int
		got := verifiedPrefix(tc.maxLen, func(n int) bool { probes = append(probes, n); return tc.fits[n] })
		if got != tc.want {
			t.Errorf("verifiedPrefix(%d, %v) = %d, want %d", tc.maxLen, tc.fits, got, tc.want)
		}
		if got > 0 && !slices.Contains(probes, got) {
			t.Errorf("result %d was never verified (probes %v)", got, probes)
		}
	}
}

// TestVerifiedPrefixCutsFiles [canary (c)]: the per-file estimates sum
// below the soft limit, but the joined text counts more (here: a test
// estimator charges 200 tokens for every join point "\n\n\n## File"). The
// exact recount must notice and cut files with the binary search.
func TestVerifiedPrefixCutsFiles(t *testing.T) {
	est := func(s string) int { return len(s)/8 + 200*strings.Count(s, "\n\n\n## File") }
	var fps []provider.FilePatch
	for i := range 6 {
		fps = append(fps, added(fmt.Sprintf("f%d.go", i), 5))
	}
	in := Input{Files: fps, Budget: budget(600), Diff: diffCfg("clip")}
	c := newCounter(0.3, est)
	p, err := prepare(in, c)
	if err != nil {
		t.Fatal(err)
	}
	checkAccounting(t, in, p)
	// Each file costs a few dozen here, so the admission loop takes all six; the
	// joined text pays 5 x 200 more, so only a prefix of three verifies
	// (3 x ~40 + 2 x 200 + 2 separators <= 600).
	if len(p.Included) != 3 || !slices.Equal(p.Included, []string{"f0.go", "f1.go", "f2.go"}) {
		t.Fatalf("Included = %q, want the verified prefix f0..f2", p.Included)
	}
	if !slices.Equal(p.Omitted.Added, []string{"f3.go", "f4.go", "f5.go"}) {
		t.Fatalf("Omitted.Added = %q", p.Omitted.Added)
	}
	body := p.Text[:strings.Index(p.Text, "\n\n"+patch.AddedFilesHeader)]
	if n := c.rawAndStripped(body); n > in.Budget.SoftLimit() {
		t.Fatalf("body counts %d > soft limit %d", n, in.Budget.SoftLimit())
	}
}

// --- large_patch_policy (§4.5) ---------------------------------------------

// TestLargePatchPolicy [canary (b)]: one file larger than the budget gives
// a clipped, non-empty diff under clip and ErrDoesNotFit under skip.
func TestLargePatchPolicy(t *testing.T) {
	files := []provider.FilePatch{modified("huge.go", 300, 2), deleted("gone.go", 10)}
	for _, mode := range []Mode{ModePlain, ModeNumbered} {
		in := Input{Files: files, Mode: mode, Budget: budget(300), Diff: diffCfg("clip")}
		p := mustPrepare(t, in)
		if !slices.Equal(p.Clipped, []string{"huge.go"}) || len(p.Included) != 0 {
			t.Fatalf("clip: Clipped = %q, Included = %q", p.Clipped, p.Included)
		}
		if !strings.Contains(p.Text, "## File: 'huge.go'") || !strings.Contains(p.Text, tokens.TruncationMarker) {
			t.Fatalf("clip: text lacks header or marker: %q", p.Text)
		}
		body := p.Text
		if i := strings.Index(body, "\n\n"+patch.DeletedFilesHeader); i >= 0 {
			body = body[:i]
		}
		if n := tokens.Estimate(body, 0.3); n > in.Budget.SoftLimit() || n == 0 {
			t.Fatalf("clip: clipped body counts %d, soft limit %d", n, in.Budget.SoftLimit())
		}
		if !strings.HasSuffix(p.Text, "\n\n"+patch.DeletedFilesHeader+"\ngone.go") {
			t.Fatalf("clip: deleted section missing: %q", p.Text[max(0, len(p.Text)-100):])
		}

		in.Diff.LargePatchPolicy = "skip"
		if _, err := Prepare(in); !errors.Is(err, tokens.ErrDoesNotFit) {
			t.Fatalf("skip: err = %v, want ErrDoesNotFit", err)
		}
	}
}

// --- determinism (§4.7) -----------------------------------------------------

// TestDeterministic50Runs [canary (d)]: equal-weight language groups and
// equal-size files; 50 runs must give byte-identical results.
func TestDeterministic50Runs(t *testing.T) {
	var fps []provider.FilePatch
	for _, ext := range []string{".go", ".py", ".rb", ".js", ".java", ".c"} {
		fps = append(fps, added("same"+ext, 4), added("pad"+ext, 4))
	}
	for _, tc := range []struct {
		mode Mode
		soft int
		fast bool
	}{{ModePlain, 100000, true}, {ModeNumbered, 400, false}} {
		in := Input{Files: fps, Mode: tc.mode, Budget: budget(tc.soft), Diff: diffCfg("clip")}
		first := mustPrepare(t, in)
		if first.FastPath != tc.fast {
			t.Fatalf("soft %d: FastPath = %v", tc.soft, first.FastPath)
		}
		for range 49 {
			p, err := Prepare(in)
			if err != nil {
				t.Fatal(err)
			}
			if p.Text != first.Text || !slices.Equal(p.Included, first.Included) ||
				!slices.Equal(p.Omitted.Added, first.Omitted.Added) || p.Tokens != first.Tokens {
				t.Fatalf("soft %d: run differs: %q vs %q", tc.soft, p.Included, first.Included)
			}
		}
	}
}

// --- accounting property (§4.6) ---------------------------------------------

// TestAccountingProperty [canary (a)]: randomized file sets and budgets
// (fixed seed, 250 cases) always satisfy the accounting invariant, and the
// text never exceeds the hard limit. The admission logic runs with a cheap
// deterministic estimator (len/3) so the test stays fast under -race; the
// invariant does not depend on the tokenizer, and
// TestAccountingPropertyRealTokenizer repeats a smaller run with
// tokens.Estimate.
func TestAccountingProperty(t *testing.T) {
	accountingProperty(t, 250, 80, func(float64) func(string) int {
		return func(s string) int { return len(s) / 3 }
	})
}

func TestAccountingPropertyRealTokenizer(t *testing.T) {
	accountingProperty(t, 40, 20, func(f float64) func(string) int {
		return func(s string) int { return tokens.Estimate(s, f) }
	})
}

func accountingProperty(t *testing.T, cases, maxLines int, estimator func(float64) func(string) int) {
	rng := rand.New(rand.NewSource(20261006)) //nolint:gosec // reproducible test inputs, not security-relevant
	for i := range cases {
		in := randomInput(rng, 12, maxLines)
		c := newCounter(in.Budget.Factor, estimator(in.Budget.Factor))
		p, err := prepare(in, c)
		if errors.Is(err, tokens.ErrDoesNotFit) {
			if in.Diff.LargePatchPolicy == "clip" && in.Budget.SoftLimit() > 200 {
				t.Errorf("case %d: clip policy gave ErrDoesNotFit with soft limit %d", i, in.Budget.SoftLimit())
			}
			continue
		}
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		checkAccounting(t, in, p)
		if n := c.count(p.Text); n > in.Budget.HardLimit() {
			t.Errorf("case %d: text counts %d > hard limit %d", i, n, in.Budget.HardLimit())
		}
		for _, path := range slices.Concat(p.Included, p.Clipped) {
			if !strings.Contains(p.Text, "'"+path+"'") {
				t.Errorf("case %d: included %q not in the text", i, path)
			}
		}
		if t.Failed() {
			t.Fatalf("case %d failed", i)
		}
	}
}

// randomInput is the randomized pull request of the property tests: up to
// maxFiles-1 files of every kind (added, modified, renamed, deleted, a pure
// rename without hunks, a fetch-failed head) with 1 to maxLines lines, up to
// two provider-skipped files, a random mode, policy and budget. Paths are
// unique within one input.
func randomInput(rng *rand.Rand, maxFiles, maxLines int) Input {
	exts := []string{".go", ".py", ".md", ".zzz", ".js"}
	var in Input
	n := rng.Intn(maxFiles)
	for j := range n {
		path := fmt.Sprintf("d%d/f%d%s", rng.Intn(3), j, exts[rng.Intn(len(exts))])
		size := 1 + rng.Intn(maxLines)
		var f provider.FilePatch
		switch rng.Intn(7) {
		case 0:
			f = deleted(path, size)
		case 1:
			f = renamed(path, size, 1+rng.Intn(5))
		case 2:
			f = provider.FilePatch{Path: path, Type: provider.ChangeRenamed} // empty patch
		case 3:
			f = modified(path, size, 1+rng.Intn(5))
			f.HeadContent, f.HeadStatus = nil, provider.ContentFetchFailed
		case 4:
			f = modified(path, size, 1+rng.Intn(5))
		default:
			f = added(path, size)
		}
		in.Files = append(in.Files, f)
	}
	for j := range rng.Intn(3) {
		in.Skipped = append(in.Skipped, provider.SkippedFile{Path: fmt.Sprintf("skip/%d.bin", j), Reason: provider.SkipBinary})
	}
	in.Mode = Mode(rng.Intn(2))
	in.Diff = diffCfg([]string{"clip", "skip"}[rng.Intn(2)])
	in.Budget = tokens.Budget{ContextWindow: 1500 + 1 + rng.Intn(maxLines*40), PromptTokens: rng.Intn(200),
		Factor: []float64{0, 0.3, 1}[rng.Intn(3)]}
	if in.Budget.SoftLimit() <= 0 {
		in.Budget.PromptTokens = 0
	}
	return in
}
