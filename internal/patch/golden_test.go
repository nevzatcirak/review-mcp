package patch

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// goldenCase is one directory under testdata/upstream. Inputs: case.json,
// patch.diff, base.txt and head.txt (absent = not fetched). Expected outputs
// are the out.* files written by the upstream oracle (see the README).
type goldenCase struct {
	Path                 string   `json:"path"`
	OldPath              string   `json:"old_path"`
	Type                 string   `json:"type"`
	HeadStatus           string   `json:"head_status"`
	Before               int      `json:"before"`
	After                int      `json:"after"`
	SkipExtendExtensions []string `json:"skip_extend_extensions"`
}

// goldenDirEnv points the golden test at another oracle-generated directory
// with the same layout (used for wider local differential runs).
const goldenDirEnv = "REVIEW_MCP_PATCH_GOLDEN_DIR"

func goldenDir() string {
	if d := os.Getenv(goldenDirEnv); d != "" {
		return d
	}
	return "testdata/upstream"
}

func readOptional(t *testing.T, path string) (*string, bool) {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test fixture path
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	s := string(b)
	return &s, true
}

func loadGolden(t *testing.T, dir string) (provider.FilePatch, config.Diff) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "case.json")) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	var c goldenCase
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	p, ok := readOptional(t, filepath.Join(dir, "patch.diff"))
	if !ok {
		t.Fatal("missing patch.diff")
	}
	base, _ := readOptional(t, filepath.Join(dir, "base.txt"))
	head, _ := readOptional(t, filepath.Join(dir, "head.txt"))
	fp := provider.FilePatch{
		Path: c.Path, OldPath: c.OldPath, Type: provider.ChangeType(c.Type), Patch: *p,
		BaseContent: base, HeadContent: head, HeadStatus: provider.ContentStatus(c.HeadStatus),
	}
	d := config.Diff{ExtraLinesBefore: c.Before, ExtraLinesAfter: c.After, SkipExtendExtensions: c.SkipExtendExtensions}
	return fp, d
}

func goldenCases(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(goldenDir())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) < 14 {
		t.Fatalf("expected the full golden set, found %d cases", len(names))
	}
	return names
}

// TestUpstreamGoldens runs every golden case through every view and compares
// with the oracle output byte for byte. [canary] for the off-by-one in the
// numbered render (spec §3.4) and for the mismatching-header rule (§3.2).
func TestUpstreamGoldens(t *testing.T) {
	for _, name := range goldenCases(t) {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(goldenDir(), name)
			fp, d := loadGolden(t, dir)
			hunks, err := ParseHunks(fp.Patch)
			if err != nil {
				t.Fatalf("ParseHunks: %v", err)
			}
			checked := 0
			check := func(out, got string) {
				t.Helper()
				want, ok := readOptional(t, filepath.Join(dir, out))
				if !ok {
					return
				}
				checked++
				if got != *want {
					t.Errorf("%s mismatch\n got: %q\nwant: %q", out, got, *want)
				}
			}

			// Raw round trip: the model serializes back to the input bytes.
			if got := patchText(hunks); got != fp.Patch {
				t.Errorf("raw round trip mismatch\n got: %q\nwant: %q", got, fp.Patch)
			}

			// Fast path.
			ext := ExtendFile(fp, hunks, d)
			check("out.extended.patch", patchText(ext))
			check("out.plain.txt", RenderPlain(NewFile(fp, ext)))
			check("out.numbered.txt", RenderDecoupled(NewFile(fp, ext), true))
			check("out.raw-numbered.txt", RenderDecoupled(NewFile(fp, hunks), true))

			// Compressed path.
			kept, deleted := HandleDeletions(fp.Type, fp.HeadContent, hunks)
			_, wantDeleted := readOptional(t, filepath.Join(dir, "out.deleted"))
			if fp.HeadStatus != provider.ContentFetchFailed && deleted != wantDeleted {
				t.Errorf("deleted = %v, want %v", deleted, wantDeleted)
			}
			if !deleted {
				check("out.deletions.patch", patchText(kept))
				check("out.compressed-plain.txt", RenderCompressed(NewFile(fp, kept), false))
				check("out.compressed-numbered.txt", RenderCompressed(NewFile(fp, kept), true))
			}
			if checked < 3 {
				t.Fatalf("only %d golden outputs checked", checked)
			}
		})
	}
}
