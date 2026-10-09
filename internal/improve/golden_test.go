package improve

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmezard/go-difflib/difflib"
)

var update = flag.Bool("update", false, "rewrite the prompt and run goldens")

const promptCasesDir = "testdata/prompts"

// promptCase is one case.json (written by hand; there is no upstream oracle
// for pr_improve, see testdata/README.md).
type promptCase struct {
	// Kind is "suggest" (one call or a part) or "reflect".
	Kind           string      `json:"kind"`
	Language       string      `json:"language"`
	Title          string      `json:"title"`
	Date           string      `json:"date"`
	Branch         string      `json:"branch"`
	TargetBranch   string      `json:"target_branch"`
	Description    string      `json:"description"`
	Discussion     string      `json:"discussion"`
	RepoContext    string      `json:"repo_context"`
	MaxSuggestions int         `json:"max_suggestions"`
	PartHeader     string      `json:"part_header"`
	Diff           string      `json:"diff"`
	Suggestions    []Candidate `json:"suggestions"`
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
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want := readFile(t, path); got != want {
		t.Errorf("%s differs from the rendered text (run go test -run %s -update after checking the change)\n%s",
			path, t.Name(), unifiedDiff(want, got, "golden", "rendered"))
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
// committed goldens (system.txt, user.txt).
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
			var p Prompts
			switch c.Kind {
			case "suggest":
				p, err = RenderPrompts(PromptInput{
					Language: c.Language, Title: c.Title, Date: c.Date, Branch: c.Branch, TargetBranch: c.TargetBranch,
					Description: c.Description, Discussion: c.Discussion, RepoContext: c.RepoContext,
					MaxSuggestions: c.MaxSuggestions, PartHeader: c.PartHeader, Diff: c.Diff,
				})
			case "reflect":
				p, err = RenderReflectPrompts(ReflectInput{Language: c.Language, Diff: c.Diff, Suggestions: c.Suggestions})
			default:
				t.Fatalf("unknown kind %q", c.Kind)
			}
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, filepath.Join(dir, "system.txt"), p.System)
			checkGolden(t, filepath.Join(dir, "user.txt"), p.User)
		})
	}
	if n < 5 {
		t.Fatalf("found %d prompt cases, want at least 5", n)
	}
}

// TestPromptCasesUseThePipelineTexts: the hand-written cases carry the
// pipeline's own part line and discussion header, so the goldens show what
// the pipeline sends.
func TestPromptCasesUseThePipelineTexts(t *testing.T) {
	read := func(name string) promptCase {
		var c promptCase
		if err := json.Unmarshal([]byte(readFile(t, filepath.Join(promptCasesDir, name, "case.json"))), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := read("part"); c.PartHeader != PartHeader(2, 3) {
		t.Errorf("part case: part_header = %q, want %q", c.PartHeader, PartHeader(2, 3))
	}
	if c := read("base"); !strings.HasPrefix(c.Discussion, DiscussionHeader+"\n") {
		t.Errorf("base case: the discussion does not start with DiscussionHeader")
	}
}
