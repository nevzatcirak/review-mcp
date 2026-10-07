package diffpipe

import (
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

var update = flag.Bool("update", false, "rewrite the chunk goldens")

// checkChunks asserts the chunk invariant documented on Chunks: every input
// file is exactly once in the union of the parts' Included, Clipped and
// DeletedListed, TooLarge, the final Omitted or Skipped; the overall lists
// are the parts' lists joined; only the first part carries the provider's
// skips; every part satisfies the single-call accounting over its own
// files and fits the hard limit.
func checkChunks(t *testing.T, in Input, ch *Chunks, maxChunks int) {
	t.Helper()
	if len(ch.Parts) == 0 || len(ch.Parts) > maxChunks {
		t.Fatalf("%d parts, want 1 to %d", len(ch.Parts), maxChunks)
	}
	seen := map[string]string{}
	add := func(list string, paths []string) {
		for _, path := range paths {
			if prev, ok := seen[path]; ok {
				t.Errorf("%q is in both %s and %s", path, prev, list)
			}
			seen[path] = list
		}
	}
	var included, clipped, listed []string
	var skipped []provider.SkippedFile
	for i, p := range ch.Parts {
		part := fmt.Sprintf("part %d ", i+1)
		add(part+"Included", p.Included)
		add(part+"Clipped", p.Clipped)
		add(part+"DeletedListed", p.DeletedListed)
		included = append(included, p.Included...)
		clipped = append(clipped, p.Clipped...)
		listed = append(listed, p.DeletedListed...)
		skipped = append(skipped, p.Skipped...)
		if i > 0 {
			for _, s := range p.Skipped {
				if s.Reason != SkipEmptyDiff && s.Reason != SkipUnparseablePatch {
					t.Errorf("part %d carries the provider skip %v", i+1, s)
				}
			}
		}
		if p.Tokens > in.Budget.HardLimit() {
			t.Errorf("part %d counts %d > hard limit %d", i+1, p.Tokens, in.Budget.HardLimit())
		}
	}
	add("TooLarge", ch.TooLarge)
	add("Omitted.Added", ch.Omitted.Added)
	add("Omitted.Modified", ch.Omitted.Modified)
	add("Omitted.Deleted", ch.Omitted.Deleted)
	for _, s := range ch.Skipped {
		add("Skipped", []string{s.Path})
	}
	for _, f := range in.Files {
		if _, ok := seen[f.Path]; !ok {
			t.Errorf("input file %q is in no list", f.Path)
		}
	}
	for _, s := range in.Skipped {
		if seen[s.Path] != "Skipped" {
			t.Errorf("provider-skipped %q is not (only) in Skipped", s.Path)
		}
	}
	total := len(ch.Included) + len(ch.Clipped) + len(ch.DeletedListed) + len(ch.TooLarge) +
		len(ch.Omitted.Added) + len(ch.Omitted.Modified) + len(ch.Omitted.Deleted) + len(ch.Skipped)
	if want := len(in.Files) + len(in.Skipped); total != want {
		t.Errorf("accounted %d entries, want %d (one per input file)", total, want)
	}
	if !slices.Equal(ch.Included, included) || !slices.Equal(ch.Clipped, clipped) ||
		!slices.Equal(ch.DeletedListed, listed) || !slices.Equal(ch.Skipped, skipped) {
		t.Errorf("overall lists are not the parts' lists joined: %+v", ch)
	}
	if len(ch.Skipped) < len(in.Skipped) || !slices.Equal(ch.Skipped[:len(in.Skipped)], in.Skipped) {
		t.Errorf("Skipped does not start with the provider's skipped files untouched: %v", ch.Skipped)
	}
	for i, p := range ch.Parts[1:] {
		sub := Input{Mode: in.Mode, Budget: in.Budget, Diff: in.Diff}
		for _, f := range in.Files {
			if slices.Contains(slices.Concat(p.Included, p.Clipped, p.DeletedListed, p.Omitted.Added,
				p.Omitted.Modified, p.Omitted.Deleted), f.Path) || slices.ContainsFunc(p.Skipped,
				func(s provider.SkippedFile) bool { return s.Path == f.Path }) {
				sub.Files = append(sub.Files, f)
			}
		}
		t.Run(fmt.Sprintf("part %d", i+2), func(t *testing.T) { checkAccounting(t, sub, p) })
	}
}

// chunkInput is a pull request of the given files with the clip or skip
// policy and soft limit soft, a provider-skipped binary file and a pure
// rename (empty_diff), so the accounting of skips is visible.
func chunkInput(soft int, policy string, files ...provider.FilePatch) Input {
	files = append(files, provider.FilePatch{Path: "moved.go", OldPath: "old.go", Type: provider.ChangeRenamed})
	return Input{Files: files, Skipped: []provider.SkippedFile{{Path: "logo.png", Reason: provider.SkipBinary}},
		Mode: ModeNumbered, Budget: budget(soft), Diff: diffCfg(policy)}
}

func mustPrepareChunks(t *testing.T, in Input, maxChunks int) *Chunks {
	t.Helper()
	ch, err := PrepareChunks(in, maxChunks)
	if err != nil {
		t.Fatalf("PrepareChunks: %v", err)
	}
	checkChunks(t, in, ch, maxChunks)
	first, err := Prepare(in)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !reflect.DeepEqual(ch.Parts[0], first) {
		t.Errorf("part 1 differs from Prepare(in)")
	}
	return ch
}

// threeChunkInput needs three parts at soft limit 900: six Go files of
// about 350 to 430 tokens each (numbered), so two fit a part; a deleted
// file listed by name in part 1.
func threeChunkInput() Input {
	return chunkInput(900, "clip",
		added("a.go", 28), added("b.go", 27), modified("c.go", 32, 4), added("d.go", 25), modified("e.go", 30, 4),
		added("f.go", 23), deleted("gone.go", 20))
}

func TestChunksThreeParts(t *testing.T) {
	ch := mustPrepareChunks(t, threeChunkInput(), 8)
	if len(ch.Parts) != 3 {
		t.Fatalf("%d parts, want 3", len(ch.Parts))
	}
	for i, p := range ch.Parts {
		if len(p.Included) == 0 || len(p.Clipped) != 0 {
			t.Errorf("part %d: Included %q, Clipped %q", i+1, p.Included, p.Clipped)
		}
	}
	if len(ch.Included) != 6 || !slices.Equal(ch.DeletedListed, []string{"gone.go"}) ||
		len(ch.Omitted.Added)+len(ch.Omitted.Modified)+len(ch.Omitted.Deleted)+len(ch.TooLarge) != 0 {
		t.Errorf("overall: %+v", ch)
	}
	checkChunkGolden(t, "three_parts", ch)
}

// TestChunksClippedSecondPart: a file larger than a whole part is clipped
// as the second part's content (clip policy); everything else fits part 1.
func TestChunksClippedSecondPart(t *testing.T) {
	in := chunkInput(600, "clip", added("a.go", 10), modified("huge.go", 300, 2), added("b.go", 12), deleted("gone.go", 5))
	ch := mustPrepareChunks(t, in, 8)
	if len(ch.Parts) != 2 {
		t.Fatalf("%d parts, want 2", len(ch.Parts))
	}
	if p := ch.Parts[1]; !slices.Equal(p.Clipped, []string{"huge.go"}) || len(p.Included) != 0 ||
		!strings.Contains(p.Text, tokens.TruncationMarker) {
		t.Errorf("part 2: Clipped %q, Included %q", p.Clipped, p.Included)
	}
	if !slices.Equal(ch.Clipped, []string{"huge.go"}) || len(ch.TooLarge) != 0 {
		t.Errorf("overall: %+v", ch)
	}
	checkChunkGolden(t, "clipped_second_part", ch)
}

// tooLargeMiddleInput ranks a.go, c.go (Go, the heaviest group), huge.py
// (Python), notes.zzz (Other): huge.py is in the middle of the rank order
// and larger than a whole part (about 1000 tokens); a.go and c.go (about
// 670 each) do not fit one part together.
func tooLargeMiddleInput(policy string) Input {
	return chunkInput(800, policy,
		added("notes.zzz", 4), added("a.go", 45), modified("huge.py", 80, 4), added("c.go", 44))
}

// TestChunksTooLargeInTheMiddle: under skip, the file larger than a whole
// part becomes too_large and packing goes on; under clip the same file is
// a part of its own, clipped.
func TestChunksTooLargeInTheMiddle(t *testing.T) {
	ch := mustPrepareChunks(t, tooLargeMiddleInput("skip"), 8)
	if !slices.Equal(ch.TooLarge, []string{"huge.py"}) || len(ch.Clipped) != 0 {
		t.Fatalf("TooLarge %q, Clipped %q", ch.TooLarge, ch.Clipped)
	}
	if !slices.Equal(ch.Parts[0].Included, []string{"a.go", "notes.zzz"}) ||
		!slices.Equal(ch.Included, []string{"a.go", "notes.zzz", "c.go"}) {
		t.Errorf("Included: part 1 %q, overall %q", ch.Parts[0].Included, ch.Included)
	}
	if len(ch.Omitted.Added)+len(ch.Omitted.Modified)+len(ch.Omitted.Deleted) != 0 {
		t.Errorf("Omitted = %+v, want none", ch.Omitted)
	}
	checkChunkGolden(t, "too_large_middle_skip", ch)

	clip := mustPrepareChunks(t, tooLargeMiddleInput("clip"), 8)
	if !slices.Equal(clip.Clipped, []string{"huge.py"}) || len(clip.TooLarge) != 0 ||
		!slices.Equal(clip.Parts[len(clip.Parts)-1].Clipped, []string{"huge.py"}) {
		t.Errorf("clip: Clipped %q, TooLarge %q", clip.Clipped, clip.TooLarge)
	}
}

// TestChunksHitMaxChunks: the three-part pull request with maxChunks 2
// leaves the third part's files Omitted.
func TestChunksHitMaxChunks(t *testing.T) {
	in := threeChunkInput()
	full := mustPrepareChunks(t, in, 8)
	ch := mustPrepareChunks(t, in, 2)
	if len(ch.Parts) != 2 || !reflect.DeepEqual(ch.Parts, full.Parts[:2]) {
		t.Fatalf("%d parts, want the first two of the full packing", len(ch.Parts))
	}
	left := slices.Concat(ch.Omitted.Added, ch.Omitted.Modified)
	slices.Sort(left)
	want := slices.Clone(full.Parts[2].Included)
	slices.Sort(want)
	if !slices.Equal(left, want) || len(ch.Omitted.Deleted) != 0 {
		t.Errorf("Omitted = %+v, want the third part's files %q", ch.Omitted, want)
	}
	checkChunkGolden(t, "max_chunks", ch)
}

// TestChunksStopWithoutProgress: a deleted file the deletion handling keeps
// (it has head content) is never clipped, since large_patch_policy skips
// deleted files. When only such a file is left and it does not fit, a
// further part would review nothing: packing stops and the file stays
// Omitted. Its render is a short "was deleted" line, so a test estimator
// makes that line too large to admit.
func TestChunksStopWithoutProgress(t *testing.T) {
	kept := deleted("kept_gone.go", 5)
	kept.HeadContent = ptr("kept\n")
	in := chunkInput(600, "clip", added("a.go", 10), kept)
	c := newCounter(0.3, func(s string) int { return len(s)/3 + 10000*strings.Count(s, "was deleted") })
	ch, err := prepareChunks(in, 8, c)
	if err != nil {
		t.Fatal(err)
	}
	checkChunks(t, in, ch, 8)
	if len(ch.Parts) != 1 || !slices.Equal(ch.Parts[0].Included, []string{"a.go"}) ||
		!slices.Equal(ch.Omitted.Deleted, []string{"kept_gone.go"}) || len(ch.DeletedListed) != 0 {
		t.Errorf("%d parts, Included %q, Omitted %+v", len(ch.Parts), ch.Parts[0].Included, ch.Omitted)
	}
}

func TestPrepareChunksErrors(t *testing.T) {
	in := chunkInput(300, "skip", modified("huge.go", 300, 2))
	if _, err := PrepareChunks(in, 8); !errors.Is(err, tokens.ErrDoesNotFit) {
		t.Errorf("chunk 1 does not fit: err = %v, want ErrDoesNotFit", err)
	}
	for _, n := range []int{0, -1} {
		if _, err := PrepareChunks(threeChunkInput(), n); err == nil || errors.Is(err, tokens.ErrDoesNotFit) {
			t.Errorf("maxChunks %d: err = %v, want a programming error", n, err)
		}
	}
}

// TestChunksMatchPrepareOnFixtures: for every upstream fixture, part 1 is
// Prepare(in) (any maxChunks), maxChunks 1 equals Prepare in every list,
// and the full packing keeps the chunk invariant.
func TestChunksMatchPrepareOnFixtures(t *testing.T) {
	entries, err := os.ReadDir(goldenDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			g, _, _ := loadGolden(t, filepath.Join(goldenDir(), e.Name()))
			in := g.toInput()
			want, wantErr := Prepare(in)
			for _, maxChunks := range []int{1, 2, 32} {
				ch, err := PrepareChunks(in, maxChunks)
				if wantErr != nil {
					if err == nil || err.Error() != wantErr.Error() {
						t.Fatalf("maxChunks %d: err = %v, want Prepare's %v", maxChunks, err, wantErr)
					}
					continue
				}
				if err != nil {
					t.Fatalf("maxChunks %d: %v", maxChunks, err)
				}
				if !reflect.DeepEqual(ch.Parts[0], want) {
					t.Errorf("maxChunks %d: part 1 differs from Prepare(in)", maxChunks)
				}
				checkChunks(t, in, ch, maxChunks)
				if maxChunks != 1 {
					continue
				}
				same := func(a, b []string) bool { return len(a) == 0 && len(b) == 0 || slices.Equal(a, b) }
				if len(ch.Parts) != 1 || len(ch.TooLarge) != 0 || !same(ch.Included, want.Included) ||
					!same(ch.Clipped, want.Clipped) || !same(ch.DeletedListed, want.DeletedListed) ||
					!same(ch.Omitted.Added, want.Omitted.Added) || !same(ch.Omitted.Modified, want.Omitted.Modified) ||
					!same(ch.Omitted.Deleted, want.Omitted.Deleted) || !slices.Equal(ch.Skipped, want.Skipped) {
					t.Errorf("maxChunks 1 differs from Prepare:\n got %+v\nwant %+v", ch, want)
				}
			}
		})
	}
}

// TestChunkInvariantProperty: the randomized pull requests of the
// accounting property (fixed seed, 600 cases, up to 19 files) with a
// random maxChunks always keep the chunk invariant: no file is in two
// parts, none is lost, and the provider's skips are counted once. The cheap
// estimator keeps it fast under -race, as in TestAccountingProperty.
func TestChunkInvariantProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20261007)) //nolint:gosec // reproducible test inputs, not security-relevant
	multi := 0
	for i := range 600 {
		in := randomInput(rng, 20, 80)
		maxChunks := 1 + rng.Intn(8)
		c := newCounter(in.Budget.Factor, func(s string) int { return len(s) / 3 })
		ch, err := prepareChunks(in, maxChunks, c)
		if errors.Is(err, tokens.ErrDoesNotFit) {
			if _, perr := prepare(in, c); !errors.Is(perr, tokens.ErrDoesNotFit) {
				t.Fatalf("case %d: ErrDoesNotFit although Prepare fits", i)
			}
			continue
		}
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if len(ch.Parts) > 1 {
			multi++
		}
		checkChunks(t, in, ch, maxChunks)
		if t.Failed() {
			t.Fatalf("case %d failed (maxChunks %d)", i, maxChunks)
		}
	}
	// The generator must exercise packing, not only single parts.
	if multi < 100 {
		t.Fatalf("only %d of 600 cases needed more than one part", multi)
	}
}

// checkChunkGolden compares a readable dump of ch with
// testdata/chunks/<name>.golden (-update rewrites it).
func checkChunkGolden(t *testing.T, name string, ch *Chunks) {
	t.Helper()
	got := dumpChunks(ch)
	path := filepath.Join("testdata", "chunks", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatalf("%v (run go test ./internal/diffpipe -run TestChunks -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs (run go test ./internal/diffpipe -run TestChunks -update after checking the change)\n%s",
			path, firstDiff(got, string(want)))
	}
}

func dumpChunks(ch *Chunks) string {
	var b strings.Builder
	list := func(name string, paths []string) {
		fmt.Fprintf(&b, "%s: %s\n", name, strings.Join(paths, ", "))
	}
	skips := func(ss []provider.SkippedFile) []string {
		var out []string
		for _, s := range ss {
			out = append(out, s.Path+" ("+s.Reason+")")
		}
		return out
	}
	omitted := func(o Omitted) {
		list("omitted.added", o.Added)
		list("omitted.modified", o.Modified)
		list("omitted.deleted", o.Deleted)
	}
	for i, p := range ch.Parts {
		fmt.Fprintf(&b, "=== part %d of %d (fast_path %v, tokens %d)\n", i+1, len(ch.Parts), p.FastPath, p.Tokens)
		list("included", p.Included)
		list("clipped", p.Clipped)
		list("deleted_listed", p.DeletedListed)
		omitted(p.Omitted)
		list("skipped", skips(p.Skipped))
		b.WriteString("--- text\n" + p.Text + "\n")
	}
	b.WriteString("=== overall\n")
	list("included", ch.Included)
	list("clipped", ch.Clipped)
	list("deleted_listed", ch.DeletedListed)
	list("too_large", ch.TooLarge)
	omitted(ch.Omitted)
	list("skipped", skips(ch.Skipped))
	return b.String()
}
