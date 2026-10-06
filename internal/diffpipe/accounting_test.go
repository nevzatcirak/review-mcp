package diffpipe

import (
	"slices"
	"testing"
)

// checkAccounting asserts the spec §4.6 invariant documented on Prepared:
// every input file is in exactly one of Included, Omitted.*, Clipped or
// Skipped; the provider's skipped files open Skipped verbatim; Included and
// Clipped are exactly the files whose content is in the text.
func checkAccounting(t *testing.T, in Input, p *Prepared) {
	t.Helper()
	if len(p.Skipped) < len(in.Skipped) || !slices.Equal(p.Skipped[:len(in.Skipped)], in.Skipped) {
		t.Fatalf("Skipped does not start with the provider's skipped files untouched:\n got %v\nwant prefix %v",
			p.Skipped, in.Skipped)
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
	add("Included", p.Included)
	add("Omitted.Added", p.Omitted.Added)
	add("Omitted.Modified", p.Omitted.Modified)
	add("Omitted.Deleted", p.Omitted.Deleted)
	add("Clipped", p.Clipped)
	for _, s := range p.Skipped {
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
	total := len(p.Included) + len(p.Omitted.Added) + len(p.Omitted.Modified) + len(p.Omitted.Deleted) +
		len(p.Clipped) + len(p.Skipped)
	if want := len(in.Files) + len(in.Skipped); total != want {
		t.Errorf("accounted %d entries, want %d (one per input file)", total, want)
	}
	for _, s := range p.Skipped[len(in.Skipped):] {
		if s.Reason != SkipEmptyDiff && s.Reason != SkipUnparseablePatch {
			t.Errorf("Prepare-added Skipped entry %v has an unexpected reason", s)
		}
	}
}
