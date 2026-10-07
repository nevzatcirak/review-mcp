package review

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/diffpipe"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

func TestWithReaskNote(t *testing.T) {
	got, err := withReaskNote("a\n" + ResponseLine + "\n```yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got != "a\n"+ReaskNote+"\n"+ResponseLine+"\n```yaml" {
		t.Errorf("got %q", got)
	}
	// Only the last response line counts (a PR text may quote it).
	got, _ = withReaskNote("x\n" + ResponseLine + "\ny\n" + ResponseLine + "\n```yaml")
	if strings.Count(got, ReaskNote) != 1 || !strings.HasSuffix(got, ReaskNote+"\n"+ResponseLine+"\n```yaml") {
		t.Errorf("got %q", got)
	}
	if _, err := withReaskNote("no response line"); err == nil {
		t.Errorf("missing response line accepted")
	}
}

// threeFiles is a prepared numbered diff of three files (fast path), plus
// their change types.
func threeFiles(t *testing.T) (*diffpipe.Prepared, map[string]provider.ChangeType) {
	t.Helper()
	body := func(tag string) string {
		var b strings.Builder
		b.WriteString("@@ -1,1 +1,30 @@\n x\n")
		for i := range 29 {
			b.WriteString("+" + tag + " added line number " + strings.Repeat("z", i%7) + "\n")
		}
		return b.String()
	}
	files := []provider.FilePatch{
		{Path: "a.go", Type: provider.ChangeModified, Patch: body("alpha"), HeadStatus: provider.ContentNotFetchedSizeCap},
		{Path: "b.go", Type: provider.ChangeAdded, Patch: body("beta"), HeadStatus: provider.ContentNotFetchedSizeCap},
		{Path: "c.go", Type: provider.ChangeModified, Patch: body("gamma"), HeadStatus: provider.ContentNotFetchedSizeCap},
	}
	p, err := diffpipe.Prepare(diffpipe.Input{Files: files, Mode: diffpipe.ModeNumbered,
		Budget: tokens.Budget{ContextWindow: 100000, Factor: 0.3}, Diff: config.Defaults().Diff})
	if err != nil {
		t.Fatal(err)
	}
	if !p.FastPath || len(p.Included) != 3 {
		t.Fatalf("prepared %+v", p)
	}
	types := map[string]provider.ChangeType{}
	for _, f := range files {
		types[f.Path] = f.Type
	}
	return p, types
}

func coverageOf(p *diffpipe.Prepared) Coverage { return buildCoverage(p, nil) }

func TestFitPromptsNoTrim(t *testing.T) {
	p, _ := threeFiles(t)
	in := sampleInput()
	f, err := fitPrompts(in, p.Text, tokens.Budget{ContextWindow: 100000, Factor: 0.3})
	if err != nil {
		t.Fatal(err)
	}
	if f.keptLines != -1 || f.diff != p.Text || !strings.Contains(f.prompts.User, p.Text) {
		t.Errorf("trimmed without need: kept %d", f.keptLines)
	}
	if f.requestTokens != tokens.RequestTokens(f.prompts.System, f.prompts.User, 0.3) {
		t.Errorf("request tokens")
	}
}

// TestFitPromptsTrims: when the final request exceeds the window, the diff
// is cut to the longest line prefix that fits, and the coverage reports the
// cut files.
func TestFitPromptsTrims(t *testing.T) {
	p, types := threeFiles(t)
	in := sampleInput()
	b := tokens.Budget{Factor: 0.3}
	request := func(diff string) int {
		in := in
		in.Diff = diff
		pr, err := RenderPrompts(in)
		if err != nil {
			t.Fatal(err)
		}
		ru, _ := withReaskNote(pr.User)
		return tokens.RequestTokens(pr.System, ru, 0.3)
	}
	// Room for a bit more than half of the diff.
	lines := strings.Split(p.Text, "\n")
	half := strings.Join(lines[:len(lines)/2], "\n") + tokens.TruncationMarker
	b.ContextWindow = request(half) + b.HardReserve() + 5
	if request(p.Text)+b.HardReserve() <= b.ContextWindow {
		t.Fatal("test setup: the full diff fits")
	}

	f, err := fitPrompts(in, p.Text, b)
	if err != nil {
		t.Fatal(err)
	}
	if f.keptLines < len(lines)/2 || f.keptLines >= len(lines) {
		t.Fatalf("kept %d of %d lines", f.keptLines, len(lines))
	}
	if !strings.HasSuffix(f.diff, tokens.TruncationMarker) || !strings.Contains(f.prompts.User, f.diff) {
		t.Errorf("trimmed diff not in the prompt")
	}
	if got := tokens.RequestTokens(f.prompts.System, f.reaskUser, 0.3) + b.HardReserve(); got > b.ContextWindow {
		t.Errorf("trimmed request %d exceeds the window %d", got, b.ContextWindow)
	}

	c := coverageOf(p)
	trimCoverage(&c, p.Text, f.keptLines, types)
	if !slices.Equal(c.Included, []string{"a.go"}) || !slices.Equal(c.Clipped, []string{"b.go"}) ||
		!slices.Equal(c.Omitted.Modified, []string{"c.go"}) {
		t.Errorf("coverage after trim = %+v", c)
	}
}

func TestFitPromptsNothingFits(t *testing.T) {
	p, _ := threeFiles(t)
	_, err := fitPrompts(sampleInput(), p.Text, tokens.Budget{ContextWindow: 1500, Factor: 0.3})
	if !errors.Is(err, ErrDoesNotFit) || !errors.Is(err, ErrFallbackEligible) || !errors.Is(err, tokens.ErrDoesNotFit) {
		t.Errorf("err = %v", err)
	}
}

func TestTrimCoverage(t *testing.T) {
	p, types := threeFiles(t)
	lines := strings.Split(p.Text, "\n")
	start := func(path string) int {
		for i, l := range lines {
			if l == "## File: '"+path+"'" {
				return i
			}
		}
		t.Fatalf("no header for %s", path)
		return 0
	}
	a, b, c := start("a.go"), start("b.go"), start("c.go")
	for _, tc := range []struct {
		name                          string
		kept                          int
		included, clipped, added, mod []string
	}{
		{"all kept", len(lines), []string{"a.go", "b.go", "c.go"}, nil, nil, nil},
		{"cut inside c", c + 3, []string{"a.go", "b.go"}, []string{"c.go"}, nil, nil},
		{"cut at c's header", c, []string{"a.go", "b.go"}, nil, nil, []string{"c.go"}},
		{"cut right after b's header", b + 1, []string{"a.go"}, []string{"b.go"}, nil, []string{"c.go"}},
		{"cut at b's header", b, []string{"a.go"}, nil, []string{"b.go"}, []string{"c.go"}},
		{"cut inside a", a + 2, nil, []string{"a.go"}, []string{"b.go"}, []string{"c.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cov := coverageOf(p)
			trimCoverage(&cov, p.Text, tc.kept, types)
			if !slices.Equal(cov.Included, nonNil(tc.included)) || !slices.Equal(cov.Clipped, nonNil(tc.clipped)) ||
				!slices.Equal(cov.Omitted.Added, nonNil(tc.added)) || !slices.Equal(cov.Omitted.Modified, nonNil(tc.mod)) {
				t.Errorf("coverage = %+v", cov)
			}
			// X-18: trimming recomputes the summary from the lists.
			if cov.TotalFiles != 3 || cov.ReviewedFiles != len(tc.included) ||
				cov.ReviewedFiles+cov.NotReviewedFiles != cov.TotalFiles || cov.Partial != (len(tc.included) != 3) {
				t.Errorf("summary after trim: partial %v, reviewed %d, not reviewed %d, total %d",
					cov.Partial, cov.ReviewedFiles, cov.NotReviewedFiles, cov.TotalFiles)
			}
		})
	}

	// A file whose header cannot be found is never reported as complete.
	cov := coverageOf(p)
	cov.Included[1] = "renamed-in-between.go"
	trimCoverage(&cov, p.Text, len(lines), types)
	if !slices.Equal(cov.Included, []string{"a.go"}) || !slices.Equal(cov.Clipped, []string{"renamed-in-between.go", "c.go"}) {
		t.Errorf("lost header: %+v", cov)
	}

	// A file the budget already clipped stays clipped when it is kept.
	cov = Coverage{Included: []string{}, Clipped: []string{"a.go"}}
	trimCoverage(&cov, p.Text, b-1, types)
	if !slices.Equal(cov.Clipped, []string{"a.go"}) || len(cov.Included) != 0 {
		t.Errorf("clipped file: %+v", cov)
	}
}

// TestTrimCoverageSections: the omitted-file sections after the body do not
// belong to the last file.
func TestTrimCoverageSections(t *testing.T) {
	text := "\n\n## File: 'a.go'\n\n@@ -1 +1 @@\n__new hunk__\n1 +x\n\n\nAdditional added files (insufficient token budget to process):\n\nb.go\nc.go"
	lines := strings.Split(text, "\n")
	cov := Coverage{Included: []string{"a.go"}, Clipped: []string{}}
	trimCoverage(&cov, text, len(lines)-1, map[string]provider.ChangeType{})
	if !slices.Equal(cov.Included, []string{"a.go"}) || len(cov.Clipped) != 0 {
		t.Errorf("coverage = %+v", cov)
	}
}
