package diffpipe

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// TestPinnedGoFirst: on the compressed path, pinned files are admitted
// before the ranked rest, in input order and not sorted by size; the
// accounting invariant holds.
func TestPinnedGoFirst(t *testing.T) {
	in := compressedInput(1700, ModePlain)
	plain := mustPrepare(t, in)
	if slices.Contains(plain.Included, "new.py") {
		t.Fatalf("the test needs new.py to be left out without pinning: %q", plain.Included)
	}

	// tiny.py is after new.py in the input, and smaller.
	in.Pinned = []string{"tiny.py", "new.py"}
	p := mustPrepare(t, in)
	if len(p.Included) < 2 || !slices.Equal(p.Included[:2], []string{"new.py", "tiny.py"}) {
		t.Errorf("Included = %q, want new.py then tiny.py first (input order)", p.Included)
	}
	if !strings.HasPrefix(strings.TrimLeft(p.Text, "\n"), "## File: 'new.py'") {
		t.Errorf("the text does not start with the pinned file: %q", p.Text[:min(60, len(p.Text))])
	}
}

// TestPinnedFastPathOrder: when everything fits, the pinned files lead the
// text and the rest keeps the ranking.
func TestPinnedFastPathOrder(t *testing.T) {
	in := Input{Files: []provider.FilePatch{added("a.go", 5), added("b.py", 5), added("c.go", 5)},
		Mode: ModePlain, Budget: budget(5000), Diff: diffCfg("clip")}
	if got := mustPrepare(t, in).Included; !slices.Equal(got, []string{"a.go", "c.go", "b.py"}) {
		t.Fatalf("unpinned Included = %q", got)
	}
	in.Pinned = []string{"b.py"}
	p := mustPrepare(t, in)
	if !p.FastPath || !slices.Equal(p.Included, []string{"b.py", "a.go", "c.go"}) {
		t.Errorf("fast path %v, Included = %q", p.FastPath, p.Included)
	}
}

// TestPinnedNothingIsUnchanged: no pinned path, or only paths that are not
// in Files (a filtered or provider-skipped file), gives exactly the result
// without pinning.
func TestPinnedNothingIsUnchanged(t *testing.T) {
	for _, mode := range []Mode{ModePlain, ModeNumbered} {
		in := compressedInput(1220, mode)
		in.Skipped = []provider.SkippedFile{{Path: "vendor/x.go", Reason: provider.SkipFiltered}}
		want := mustPrepare(t, in)
		for _, pinned := range [][]string{nil, {}, {"vendor/x.go", "missing.go"}} {
			in.Pinned = pinned
			if got := mustPrepare(t, in); !reflect.DeepEqual(got, want) {
				t.Errorf("mode %v, pinned %q: the result changed", mode, pinned)
			}
		}
	}
}
