package ask

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmezard/go-difflib/difflib"
)

var update = flag.Bool("update", false, "rewrite the prompt goldens and the upstream diffs")

const promptCasesDir = "testdata/prompts"

// promptCase is one case.json (written by testdata/oracle/make_cases.py).
type promptCase struct {
	ExtraInstructions string `json:"extra_instructions"`
	Language          string `json:"language"`
	MainLanguage      string `json:"main_language"`
	Title             string `json:"title"`
	Branch            string `json:"branch"`
	Description       string `json:"description"`
	Question          string `json:"question"`
	Diff              string `json:"diff"`
}

func (c *promptCase) input() PromptInput {
	return PromptInput{
		ExtraInstructions: c.ExtraInstructions,
		Language:          c.Language,
		MainLanguage:      c.MainLanguage,
		Title:             c.Title,
		Branch:            c.Branch,
		Description:       c.Description,
		Question:          c.Question,
		Diff:              c.Diff,
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkGolden compares got with the file at path, or rewrites it with
// -update.
func checkGolden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want := readFile(t, path); got != want {
		t.Errorf("%s differs from the rendered text (run go test -run TestPromptGoldens -update after checking the change)\n%s",
			path, unifiedDiff(want, got, "golden", "rendered"))
	}
}

func unifiedDiff(a, b, from, to string) string {
	s, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(a), B: difflib.SplitLines(b),
		FromFile: from, ToFile: to, Context: 1,
	})
	if err != nil {
		return "diff failed: " + err.Error()
	}
	return s
}

// TestPromptGoldens renders every case and compares the prompts with the
// committed goldens (system.txt, user.txt). It also recomputes the diff
// against upstream's rendering (upstream.*.txt, written by the oracle) and
// compares it with the committed upstream.diff, so the diff summary in the
// testdata README cannot go stale.
func TestPromptGoldens(t *testing.T) {
	entries, err := os.ReadDir(promptCasesDir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n++
		dir := filepath.Join(promptCasesDir, e.Name())
		t.Run(e.Name(), func(t *testing.T) {
			var c promptCase
			if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "case.json"))), &c); err != nil {
				t.Fatal(err)
			}
			p, err := RenderPrompts(c.input())
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, filepath.Join(dir, "system.txt"), p.System)
			checkGolden(t, filepath.Join(dir, "user.txt"), p.User)

			var d strings.Builder
			d.WriteString(unifiedDiff(readFile(t, filepath.Join(dir, "upstream.system.txt")), p.System,
				"upstream.system.txt", "system.txt"))
			d.WriteString(unifiedDiff(readFile(t, filepath.Join(dir, "upstream.user.txt")), p.User,
				"upstream.user.txt", "user.txt"))
			checkGolden(t, filepath.Join(dir, "upstream.diff"), d.String())
		})
	}
	if n < 8 {
		t.Fatalf("found %d prompt cases, want at least 8", n)
	}
}
