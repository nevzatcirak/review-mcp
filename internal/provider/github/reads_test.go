package github

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func TestMapCheckRun(t *testing.T) {
	cases := []struct {
		status, conclusion string
		want               provider.CheckState
	}{
		{"queued", "", provider.CheckPending},
		{"in_progress", "", provider.CheckPending},
		{"completed", "success", provider.CheckSuccess},
		{"completed", "failure", provider.CheckFailure},
		{"completed", "neutral", provider.CheckNeutral},
		{"completed", "cancelled", provider.CheckError},
		{"completed", "skipped", provider.CheckSkipped},
		{"completed", "timed_out", provider.CheckFailure},
		{"completed", "action_required", provider.CheckPending},
		{"completed", "stale", provider.CheckNeutral},
		{"completed", "", provider.CheckNeutral},
		{"completed", "brand_new", provider.CheckNeutral},
		{"brand_new", "success", provider.CheckNeutral},
	}
	for _, c := range cases {
		if got := mapCheckRun(c.status, c.conclusion); got != c.want {
			t.Errorf("mapCheckRun(%q, %q) = %q, want %q", c.status, c.conclusion, got, c.want)
		}
	}
	for in, want := range map[string]provider.CheckState{
		"success": provider.CheckSuccess, "failure": provider.CheckFailure, "error": provider.CheckError,
		"pending": provider.CheckPending, "brand_new": provider.CheckNeutral,
	} {
		if got := mapCommitStatus(in); got != want {
			t.Errorf("mapCommitStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

// Check runs and legacy statuses are merged by name; a check run wins on a
// clash. A run without a usable html_url links its details_url.
func TestGetChecksMergesByName(t *testing.T) {
	f := newFake(t, "/api/v3")
	commit := "/repos/octo/demo/commits/headsha"
	f.json(commit+"/check-runs", map[string]any{"total_count": 2, "check_runs": []any{
		map[string]any{"name": "build", "status": "completed", "conclusion": "success",
			"html_url": "https://github.example.com/octo/demo/runs/1"},
		map[string]any{"name": "deploy", "status": "queued", "conclusion": nil,
			"html_url": "", "details_url": "https://ci.example.com/deploy/2"},
	}})
	f.json(commit+"/status", map[string]any{"state": "failure", "statuses": []any{
		map[string]any{"context": "build", "state": "failure", "target_url": "https://ci.example.com/old"},
		map[string]any{"context": "legacy/ci", "state": "error", "target_url": "https://ci.example.com/legacy"},
	}})
	p, _ := f.provider(f.config(""), time.Now())
	got, err := p.GetChecks(t.Context(), testRef(), "headsha")
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.Check{
		{Name: "build", State: provider.CheckSuccess, URL: "https://github.example.com/octo/demo/runs/1"},
		{Name: "deploy", State: provider.CheckPending, URL: "https://ci.example.com/deploy/2"},
		{Name: "legacy/ci", State: provider.CheckError, URL: "https://ci.example.com/legacy"},
	}
	if !slices.Equal(got.Items, want) || got.Truncated || got.Strategy != provider.ChecksGitHubRunsAndStatuses {
		t.Errorf("GetChecks = %+v, want %+v", got, want)
	}
	for _, c := range got.Items {
		if c.Required != nil {
			t.Errorf("%s: Required is set; GitHub cannot say cheaply", c.Name)
		}
	}
}

// A failure of either list fails the call with the class of the status.
func TestGetChecksEitherListFails(t *testing.T) {
	f := newFake(t, "/api/v3")
	commit := "/repos/octo/demo/commits/headsha"
	f.json(commit+"/check-runs", map[string]any{"check_runs": []any{}})
	f.handle(http.MethodGet, commit+"/status", func(w http.ResponseWriter, _ *http.Request) { fakeError(w, http.StatusForbidden) })
	p, _ := f.provider(f.config(""), time.Now())
	_, err := p.GetChecks(t.Context(), testRef(), "headsha")
	if err == nil || err.Error() != (&provider.Error{Class: provider.ClassAuth, Status: 403}).Error() {
		t.Errorf("GetChecks: error %v, want the fixed auth sentence", err)
	}
}
