package patch

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// goldenCase is one golden case directory. Inputs: case.json, patch.diff,
// base.txt and head.txt (absent = not fetched).
//
// There are two kinds of case, kept in separate directories and tests:
//   - oracle cases (testdata/upstream, TestUpstreamGoldens): the out.* files
//     were written by the upstream oracle (see testdata/upstream/README.md);
//     Deviation is empty;
//   - deviation cases (testdata/deviations, TestDeviationGoldens): v1
//     deliberately differs from upstream, so the out.* files are not oracle
//     output. Deviation names the decision (for example "D3") and the case
//     directory holds a NOTE explaining it and how the outputs were derived.
type goldenCase struct {
	Path                 string   `json:"path"`
	OldPath              string   `json:"old_path"`
	Type                 string   `json:"type"`
	HeadStatus           string   `json:"head_status"`
	Before               int      `json:"before"`
	After                int      `json:"after"`
	SkipExtendExtensions []string `json:"skip_extend_extensions"`
	Deviation            string   `json:"deviation"`
}

// goldenDirEnv points the oracle golden test at another oracle-generated
// directory with the same layout (used for wider local differential runs).
const goldenDirEnv = "REVIEW_MCP_PATCH_GOLDEN_DIR"

// deviationDir holds the intentional-deviation cases (README "Deviations").
const deviationDir = "testdata/deviations"

// upstreamProductName must not appear in any render (architect decision D4,
// PR #4): the model and the users see the name of the tool they run.
const upstreamProductName = "PR-Agent"

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

func loadGolden(t *testing.T, dir string) (goldenCase, provider.FilePatch, config.Diff) {
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
	return c, fp, d
}

func caseNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func goldenCases(t *testing.T) []string {
	t.Helper()
	names := caseNames(t, goldenDir())
	if len(names) < 14 {
		t.Fatalf("expected the full golden set, found %d cases", len(names))
	}
	return names
}

// TestUpstreamGoldens runs every oracle case through every view and compares
// with the oracle output byte for byte. [canary] for the off-by-one in the
// numbered render (spec §3.4) and for the mismatching-header rule (§3.2).
func TestUpstreamGoldens(t *testing.T) {
	for _, name := range goldenCases(t) {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(goldenDir(), name)
			c, fp, d := loadGolden(t, dir)
			if c.Deviation != "" {
				t.Fatalf("deviation case %q (%s) in the oracle directory; move it to %s", name, c.Deviation, deviationDir)
			}
			runGolden(t, dir, fp, d)
		})
	}
}

// TestDeviationGoldens runs every intentional-deviation case like an oracle
// case. Its expected outputs are not upstream's (see each case's NOTE and the
// README section "Deviations"). [canary] for architect decision D3 (PR #4):
// deviation_head_fetch_failed_with_patch fails if a fetch-failed file with a
// patch renders the unreadable notice again. [canary] for architect decision
// D4 (PR #4): deviation_unreadable_notice fails if the notice names
// upstream's product instead of review-mcp.
func TestDeviationGoldens(t *testing.T) {
	names := caseNames(t, deviationDir)
	if len(names) == 0 {
		t.Fatal("no deviation cases found")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(deviationDir, name)
			c, fp, d := loadGolden(t, dir)
			if c.Deviation == "" {
				t.Fatalf("case.json must name the deviation (field %q)", "deviation")
			}
			if _, ok := readOptional(t, filepath.Join(dir, "NOTE")); !ok {
				t.Fatal("a deviation case needs a NOTE file")
			}
			runGolden(t, dir, fp, d)
		})
	}
}

// runGolden runs one case through ParseHunks, ExtendFile, HandleDeletions
// and every renderer and compares each present out.* file byte for byte.
func runGolden(t *testing.T, dir string, fp provider.FilePatch, d config.Diff) {
	t.Helper()
	hunks, err := ParseHunks(fp.Patch)
	if err != nil {
		t.Fatalf("ParseHunks: %v", err)
	}
	checked := 0
	check := func(out, got string) {
		t.Helper()
		// Architect decision D4 (PR #4): no render names the upstream
		// product; only provenance comments may mention it.
		if strings.Contains(got, upstreamProductName) {
			t.Errorf("%s: render contains %q:\n%q", out, upstreamProductName, got)
		}
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
	if deleted != wantDeleted {
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
}

// TestNoUpstreamProductNameInGoldens: architect decision D4 (PR #4). No
// expected output, oracle or deviation, names the upstream product. Only
// provenance text (READMEs, NOTE files, comments) may mention it.
func TestNoUpstreamProductNameInGoldens(t *testing.T) {
	for _, root := range []string{"testdata/upstream", deviationDir} {
		outs, err := filepath.Glob(filepath.Join(root, "*", "out.*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(outs) == 0 {
			t.Fatalf("no out.* files under %s", root)
		}
		for _, out := range outs {
			b, err := os.ReadFile(out) //nolint:gosec // test fixture path
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), upstreamProductName) {
				t.Errorf("%s contains %q", out, upstreamProductName)
			}
		}
	}
}
