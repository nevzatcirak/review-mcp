package review

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// relatedGolden is the serialised coverage.related and notes of runs whose
// related items are all pull requests, written by 2.4.0 (v2.5 spec V-13b
// §1.4: a 2.4 result with only pull request items is byte-identical).
const relatedGolden = "testdata/related_pr_only.golden"

// relatedPROnlyJSON runs the related cases of a 2.4 result (read in full,
// the primary, not configured, a duplicate, forbidden; and partial) and
// serialises their coverage.related and notes as the tools do (encoding/json).
func relatedPROnlyJSON(t *testing.T) []byte {
	t.Helper()
	h, _ := relatedHarness(goodAnswer)
	base := h.deps.Resolver.(relResolver)
	const missing = "https://gitlab.example/grp/gone/-/merge_requests/4"
	const nowhere = "https://unknown.example/a/b/pulls/1"
	h.deps.Resolver = failResolver{relResolver: base, errs: map[string]error{
		nowhere: &provider.Error{Class: provider.ClassURLNotConfigured},
		missing: &provider.Error{Class: provider.ClassAuth, Status: 403},
	}}
	mixed := runRelated(t, h, Args{Related: []string{relatedURL, testPRURL, nowhere, relatedURL, missing}})
	h, _ = relatedHarness(goodAnswer)
	h.deps.Config.Related.MaxTokens = 800
	partial := runRelated(t, h, Args{Related: []string{relatedURL}})
	type run struct {
		Related []RelatedItem `json:"related"`
		Notes   []string      `json:"notes"`
	}
	var buf bytes.Buffer
	for _, r := range []*Result{mixed, partial} {
		b, err := json.Marshal(run{Related: r.Coverage.Related, Notes: r.Notes})
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// TestRelatedPullRequestsByteIdentical [canary]: coverage.related of pull
// request items serialises byte for byte as 2.4.0 wrote it: none of the
// repository fields (kind, branch, commit, identifiers, matches,
// matches_shown, source) appears on a pull request item.
func TestRelatedPullRequestsByteIdentical(t *testing.T) {
	got := relatedPROnlyJSON(t)
	path := filepath.FromSlash(relatedGolden)
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed testdata path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("coverage.related of pull request items differs from 2.4.0:\ngot  %s\nwant %s", got, want)
	}
}
