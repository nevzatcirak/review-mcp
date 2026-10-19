package render

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/review"
)

// TestRelatedPullRequestsRenderAsBefore [canary]: the overviews of a result
// whose related items are all pull requests (the serialised runs of
// ../testdata/related_pr_only.golden) render byte for byte as 2.4.0
// rendered them (testdata/related_pr_only_*.md).
func TestRelatedPullRequestsRenderAsBefore(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "related_pr_only.golden"))
	if err != nil {
		t.Fatal(err)
	}
	var runs []review.Result
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r struct {
			Related []review.RelatedItem `json:"related"`
			Notes   []string             `json:"notes"`
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		res := *fixtures()["all_fields"]
		res.Coverage.Related, res.Notes = r.Related, r.Notes
		runs = append(runs, res)
	}
	if len(runs) != 2 {
		t.Fatalf("%d runs in the golden", len(runs))
	}
	for name, render := range map[string]func(*review.Result) string{
		"client": Client,
		"gfm":    func(r *review.Result) string { return Provider(r, capsGitea) },
		"plain":  func(r *review.Result) string { return Provider(r, capsBB) },
	} {
		var got bytes.Buffer
		for i := range runs {
			got.WriteString(render(&runs[i]))
			got.WriteString("\n----\n")
		}
		path := filepath.Join("testdata", "related_pr_only_"+name+".md")
		if *update {
			if err := os.WriteFile(path, got.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Errorf("%s: the overview of pull request items differs from 2.4.0:\n%s", name, got.String())
		}
	}
}
