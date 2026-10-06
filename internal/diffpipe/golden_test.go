package diffpipe

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/patch"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// goldenInput is testdata/upstream/<case>/input.json.
type goldenInput struct {
	Budget struct {
		ContextWindow   int     `json:"context_window"`
		PromptTokens    int     `json:"prompt_tokens"`
		Factor          float64 `json:"factor"`
		MaxOutputTokens *int    `json:"max_output_tokens"`
	} `json:"budget"`
	Mode string      `json:"mode"`
	Diff config.Diff `json:"diff"`
	// Files mirrors provider.FilePatch.
	Files []struct {
		Path       string  `json:"path"`
		OldPath    string  `json:"old_path"`
		Type       string  `json:"type"`
		Patch      string  `json:"patch"`
		Base       *string `json:"base"`
		Head       *string `json:"head"`
		HeadStatus string  `json:"head_status"`
	} `json:"files"`
	Skipped []struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"skipped"`
}

// goldenOutput is testdata/upstream/<case>/out.json, written by the oracle.
type goldenOutput struct {
	Languages       []string `json:"languages"`
	Tokens          int      `json:"tokens"`
	FastPath        bool     `json:"fast_path"`
	Included        []string `json:"included"`
	UpstreamDropped []string `json:"upstream_dropped"`
	Omitted         struct {
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Deleted  []string `json:"deleted"`
	} `json:"omitted"`
	FileDictOrder []string `json:"file_dict_order"`
	PolicyCase    bool     `json:"policy_case"`
	// ClipOvershootOffset is set when upstream's unverified clip_tokens
	// result for a section exceeded its section budget (see the README):
	// the byte length of upstream's text before that section.
	ClipOvershootOffset *int `json:"clip_overshoot_offset"`
}

func (g goldenInput) toInput() Input {
	in := Input{
		Budget: tokens.Budget{
			ContextWindow:   g.Budget.ContextWindow,
			PromptTokens:    g.Budget.PromptTokens,
			Factor:          g.Budget.Factor,
			MaxOutputTokens: g.Budget.MaxOutputTokens,
		},
		Diff: g.Diff,
	}
	if g.Mode == "numbered" {
		in.Mode = ModeNumbered
	}
	for _, f := range g.Files {
		baseStatus := provider.ContentFull
		if f.Base == nil {
			baseStatus = provider.ContentNotApplicable
		}
		in.Files = append(in.Files, provider.FilePatch{
			Path: f.Path, OldPath: f.OldPath, Type: provider.ChangeType(f.Type), Patch: f.Patch,
			BaseContent: f.Base, HeadContent: f.Head,
			BaseStatus: baseStatus, HeadStatus: provider.ContentStatus(f.HeadStatus),
		})
	}
	for _, s := range g.Skipped {
		in.Skipped = append(in.Skipped, provider.SkippedFile{Path: s.Path, Reason: s.Reason})
	}
	return in
}

func goldenDir() string {
	if d := os.Getenv("REVIEW_MCP_DIFFPIPE_GOLDEN_DIR"); d != "" {
		return d
	}
	return filepath.Join("testdata", "upstream")
}

func loadGolden(t *testing.T, dir string) (goldenInput, goldenOutput, string) {
	t.Helper()
	var in goldenInput
	var out goldenOutput
	for name, v := range map[string]any{"input.json": &in, "out.json": &out} {
		b, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // test fixture path
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	text, err := os.ReadFile(filepath.Join(dir, "out.diff.txt")) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	return in, out, string(text)
}

// TestUpstreamGoldens runs every oracle case through Prepare and compares
// the text, the path flags and the coverage lists with upstream's
// get_pr_diff byte for byte (see testdata/upstream/README.md).
func TestUpstreamGoldens(t *testing.T) {
	entries, err := os.ReadDir(goldenDir())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n++
		t.Run(e.Name(), func(t *testing.T) {
			g, want, wantText := loadGolden(t, filepath.Join(goldenDir(), e.Name()))
			in := g.toInput()
			checkRanking(t, in, want.Languages)
			got, err := Prepare(in)
			if want.PolicyCase {
				checkPolicyCase(t, in, want, got, err)
				return
			}
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}
			if off := want.ClipOvershootOffset; off != nil {
				checkClipOvershoot(t, in, got, wantText, *off)
			} else {
				if got.Text != wantText {
					t.Errorf("text differs from upstream\n%s", firstDiff(got.Text, wantText))
				}
				if got.Tokens != want.Tokens {
					t.Errorf("Tokens = %d, want %d", got.Tokens, want.Tokens)
				}
			}
			if got.FastPath != want.FastPath {
				t.Errorf("FastPath = %v, want %v", got.FastPath, want.FastPath)
			}
			eq(t, "Included", got.Included, want.Included)
			eq(t, "Omitted.Added", got.Omitted.Added, want.Omitted.Added)
			eq(t, "Omitted.Modified", got.Omitted.Modified, want.Omitted.Modified)
			eq(t, "Omitted.Deleted", got.Omitted.Deleted, want.Omitted.Deleted)
			if len(got.Clipped) != 0 {
				t.Errorf("Clipped = %q, want none", got.Clipped)
			}
			checkSkipped(t, in, want, got)
			checkAccounting(t, in, got)
		})
	}
	if n == 0 {
		t.Fatal("no golden cases found")
	}
}

// checkRanking compares the DQ-2 group order with the oracle's.
func checkRanking(t *testing.T, in Input, want []string) {
	t.Helper()
	files := make([]*file, len(in.Files))
	for i := range in.Files {
		files[i] = &file{fp: &in.Files[i]}
	}
	var got []string
	for _, g := range rank(files) {
		got = append(got, g.lang)
	}
	eq(t, "language order", got, want)
}

// checkClipOvershoot covers the known section-clipping deviation: upstream
// appended (or dropped) a section whose heuristic clip exceeded the section
// budget, where tokens.Clip verifies and shrinks. The text must equal
// upstream's up to that section; after it, every appended section must be
// a well-formed (possibly clipped) omitted-file section, and the whole text
// must stay within the hard limit.
func checkClipOvershoot(t *testing.T, in Input, got *Prepared, want string, off int) {
	t.Helper()
	if len(got.Text) < off || got.Text[:off] != want[:off] {
		t.Fatalf("text differs from upstream before the clip-overshoot section\n%s",
			firstDiff(got.Text[:min(off, len(got.Text))], want[:off]))
	}
	full := map[string]string{
		patch.AddedFilesHeader:    patch.AddedFilesHeader + "\n" + strings.Join(got.Omitted.Added, "\n"),
		patch.ModifiedFilesHeader: patch.ModifiedFilesHeader + "\n" + strings.Join(got.Omitted.Modified, "\n"),
		patch.DeletedFilesHeader:  patch.DeletedFilesHeader + "\n" + strings.Join(got.Omitted.Deleted, "\n"),
	}
	rest := got.Text[off:]
	for rest != "" {
		if !strings.HasPrefix(rest, sectionSeparator) {
			t.Fatalf("unexpected text after the sections: %q", rest)
		}
		rest = rest[len(sectionSeparator):]
		matched := false
		for header, section := range full {
			if !strings.HasPrefix(rest, header) {
				continue
			}
			matched = true
			end := len(rest)
			for h := range full {
				if i := strings.Index(rest[len(header):], sectionSeparator+h); i >= 0 {
					end = min(end, len(header)+i)
				}
			}
			s := rest[:end]
			if s != section && !strings.HasPrefix(section, strings.TrimSuffix(s, tokens.TruncationMarker)) {
				t.Fatalf("section is not a clip of %q: %q", section, s)
			}
			rest = rest[end:]
		}
		if !matched {
			t.Fatalf("unexpected text after the sections: %q", rest)
		}
	}
	if got.Tokens > in.Budget.HardLimit() {
		t.Fatalf("Tokens = %d over the hard limit %d", got.Tokens, in.Budget.HardLimit())
	}
}

// checkSkipped: the provider's skipped files first, untouched, then the
// files upstream drops silently (no patch), as empty_diff.
func checkSkipped(t *testing.T, in Input, want goldenOutput, got *Prepared) {
	t.Helper()
	wantSkipped := slices.Clone(in.Skipped)
	dropped := map[string]bool{}
	for _, p := range want.UpstreamDropped {
		dropped[p] = true
	}
	for _, f := range in.Files {
		if dropped[f.Path] {
			wantSkipped = append(wantSkipped, provider.SkippedFile{Path: f.Path, Reason: SkipEmptyDiff})
		}
	}
	if !slices.Equal(got.Skipped, wantSkipped) {
		t.Errorf("Skipped = %v, want %v", got.Skipped, wantSkipped)
	}
}

// checkPolicyCase covers the spec §4.5 deviation: upstream admits nothing
// and returns only the omitted-file sections; v1 clips the top-ranked
// non-deleted file (clip) or reports ErrDoesNotFit (skip).
func checkPolicyCase(t *testing.T, in Input, want goldenOutput, got *Prepared, err error) {
	t.Helper()
	if in.Diff.LargePatchPolicy != "clip" {
		if !errors.Is(err, tokens.ErrDoesNotFit) {
			t.Fatalf("skip policy: err = %v, want ErrDoesNotFit", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("clip policy: %v", err)
	}
	top := ""
	for _, p := range want.FileDictOrder {
		for _, f := range in.Files {
			if f.Path == p && f.Type != provider.ChangeDeleted && top == "" {
				top = p
			}
		}
	}
	eq(t, "Clipped", got.Clipped, []string{top})
	if len(got.Included) != 0 {
		t.Errorf("Included = %q, want none", got.Included)
	}
	if !strings.Contains(got.Text, "## File: '"+top+"'") || !strings.Contains(got.Text, tokens.TruncationMarker) {
		t.Errorf("clipped text lacks the file header or the truncation marker:\n%.300s", got.Text)
	}
	if got.Tokens > in.Budget.HardLimit() {
		t.Errorf("Tokens = %d, over the hard limit %d", got.Tokens, in.Budget.HardLimit())
	}
	rm := func(s []string) []string {
		return slices.DeleteFunc(slices.Clone(s), func(p string) bool { return p == top })
	}
	eq(t, "Omitted.Added", got.Omitted.Added, rm(want.Omitted.Added))
	eq(t, "Omitted.Modified", got.Omitted.Modified, rm(want.Omitted.Modified))
	eq(t, "Omitted.Deleted", got.Omitted.Deleted, rm(want.Omitted.Deleted))
	checkAccounting(t, in, got)
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s =\n  %q\nwant\n  %q", what, got, want)
	}
}

// firstDiff describes where two texts first differ.
func firstDiff(got, want string) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	lo := max(0, i-120)
	return "first difference at byte " + itoa(i) + " (got len " + itoa(len(got)) + ", want len " + itoa(len(want)) + ")\n" +
		"got:  " + quote(got[lo:min(len(got), i+120)]) + "\nwant: " + quote(want[lo:min(len(want), i+120)])
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }
