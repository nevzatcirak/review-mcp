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

// A token with only one of Checks and Commit statuses: when exactly one list
// fails with auth or not_found, the other is returned with Partial set.
func TestGetChecksPartial(t *testing.T) {
	commit := "/repos/octo/demo/commits/headsha"
	runs := map[string]any{"check_runs": []any{
		map[string]any{"name": "build", "status": "completed", "conclusion": "failure"},
	}}
	statuses := map[string]any{"statuses": []any{
		map[string]any{"context": "legacy/ci", "state": "success"},
	}}
	for name, tc := range map[string]struct {
		failRuns bool
		status   int
		want     []provider.Check
	}{
		"statuses 403": {false, http.StatusForbidden, []provider.Check{{Name: "build", State: provider.CheckFailure}}},
		"statuses 404": {false, http.StatusNotFound, []provider.Check{{Name: "build", State: provider.CheckFailure}}},
		"runs 403":     {true, http.StatusForbidden, []provider.Check{{Name: "legacy/ci", State: provider.CheckSuccess}}},
		"runs 401":     {true, http.StatusUnauthorized, []provider.Check{{Name: "legacy/ci", State: provider.CheckSuccess}}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, "/api/v3")
			fail := func(w http.ResponseWriter, _ *http.Request) { fakeError(w, tc.status) }
			if tc.failRuns {
				f.handle(http.MethodGet, commit+"/check-runs", fail)
				f.json(commit+"/status", statuses)
			} else {
				f.json(commit+"/check-runs", runs)
				f.handle(http.MethodGet, commit+"/status", fail)
			}
			p, _ := f.provider(f.config(""), time.Now())
			got, err := p.GetChecks(t.Context(), testRef(), "headsha")
			if err != nil {
				t.Fatalf("GetChecks: %v, want a partial result", err)
			}
			if !got.Partial || got.Truncated || !slices.Equal(got.Items, tc.want) {
				t.Errorf("GetChecks = %+v, want %+v and Partial", got, tc.want)
			}
		})
	}
}

// Both lists failing, or one failing with any class but auth and not_found,
// fails the call with the class of the status.
func TestGetChecksFails(t *testing.T) {
	commit := "/repos/octo/demo/commits/headsha"
	for name, tc := range map[string]struct {
		runs, status int // 0 answers the list
		want         provider.ErrorClass
		code         int
	}{
		"both 403":          {http.StatusForbidden, http.StatusForbidden, provider.ClassAuth, 403},
		"both 404":          {http.StatusNotFound, http.StatusNotFound, provider.ClassNotFound, 404},
		"statuses 500":      {0, http.StatusInternalServerError, provider.ClassUpstream, 500},
		"runs 500":          {http.StatusInternalServerError, 0, provider.ClassUpstream, 500},
		"runs 403, sts 500": {http.StatusForbidden, http.StatusInternalServerError, provider.ClassAuth, 403},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, "/api/v3")
			for path, code := range map[string]int{commit + "/check-runs": tc.runs, commit + "/status": tc.status} {
				if code == 0 {
					f.json(path, map[string]any{"check_runs": []any{}, "statuses": []any{}})
					continue
				}
				f.handle(http.MethodGet, path, func(w http.ResponseWriter, _ *http.Request) { fakeError(w, code) })
			}
			p, _ := f.provider(f.config(""), time.Now())
			got, err := p.GetChecks(t.Context(), testRef(), "headsha")
			if err == nil || err.Error() != (&provider.Error{Class: tc.want, Status: tc.code}).Error() {
				t.Errorf("GetChecks = %+v, %v; want the fixed %s sentence", got, err, tc.want)
			}
		})
	}
}
