package bitbucketserver_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

const (
	buildsAPI = latestRepo + "/commits/headsha/builds"
	legacyAPI = "/rest/build-status/1.0/commits/headsha"
)

func build(state, key, name, url string) map[string]any {
	return map[string]any{"state": state, "key": key, "name": name, "url": url}
}

// The repository-scoped endpoint answers when it exists; on 404 the legacy
// endpoint answers. Checks.Strategy names the one that did.
func TestGetChecksStrategy(t *testing.T) {
	builds := []any{
		build("SUCCESSFUL", "ci-build", "Build", "https://ci.example.com/1"),
		build("FAILED", "ci-test", "", "https://ci.example.com/2"),
		build("INPROGRESS", "ci-lint", "Lint", ""),
		build("UNKNOWN", "ci-other", "Other", "https://ci.example.com/4"),
		build("CANCELLED", "ci-new", "Newer state", ""),
	}
	want := []provider.Check{
		{Name: "Build", State: provider.CheckSuccess, URL: "https://ci.example.com/1"},
		{Name: "ci-test", State: provider.CheckFailure, URL: "https://ci.example.com/2"},
		{Name: "Lint", State: provider.CheckPending},
		{Name: "Other", State: provider.CheckNeutral, URL: "https://ci.example.com/4"},
		{Name: "Newer state", State: provider.CheckNeutral},
	}
	for _, tc := range []struct {
		name     string
		legacy   bool
		strategy string
	}{
		{"repository_builds", false, provider.ChecksBBSBuilds},
		{"legacy_build_status", true, provider.ChecksBBSBuildStatus},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, "/bitbucket")
			f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
			if tc.legacy {
				f.handleStatus("GET", buildsAPI, 404)
				f.handlePaged(legacyAPI, nil, builds[:2], builds[2:])
			} else {
				f.handlePaged(buildsAPI, nil, builds[:2], builds[2:])
			}
			got, err := f.provider(t, nil).GetChecks(t.Context(), ref(), "headsha")
			if err != nil {
				t.Fatal(err)
			}
			if got.Strategy != tc.strategy || got.Truncated || !slices.Equal(got.Items, want) {
				t.Errorf("GetChecks = %+v, want strategy %q and %+v", got, tc.strategy, want)
			}
			if n := f.count("GET", legacyAPI); (n > 0) != tc.legacy {
				t.Errorf("legacy endpoint requested %d times", n)
			}
		})
	}
}

// Only a 404 of the repository-scoped endpoint falls back; any other
// failure is returned as it is, without the server's text.
func TestGetChecksNoFallbackOnOtherErrors(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		f := newFake(t, "")
		f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
		f.handleStatus("GET", buildsAPI, status)
		_, err := f.provider(t, nil).GetChecks(t.Context(), ref(), "headsha")
		var perr *provider.Error
		if !errors.As(err, &perr) || perr.Status != status {
			t.Errorf("status %d: error %v", status, err)
		}
		if err != nil && (strings.Contains(err.Error(), testMarker) || strings.Contains(err.Error(), testToken)) {
			t.Errorf("status %d: error text carries server text or the token: %q", status, err)
		}
		if n := f.count("GET", legacyAPI); n != 0 {
			t.Errorf("status %d: legacy endpoint requested %d times", status, n)
		}
	}
}

// A browse child may stand for a chain of single-child directories (several
// path components): its first component is a directory.
func TestListDirCollapsedChildren(t *testing.T) {
	f := newFake(t, "")
	f.handleJSON("GET", propsAPI, map[string]any{"version": "8.9.0"})
	child := func(typ string, comps ...string) map[string]any {
		return map[string]any{"type": typ, "path": map[string]any{"components": comps, "name": comps[len(comps)-1]}}
	}
	f.handle("GET", v1Repo+"/browse/src", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("at") != "basesha" {
			t.Errorf("at = %q", r.URL.Query().Get("at"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"children": map[string]any{"isLastPage": true, "values": []any{
			child("FILE", "main.go"),
			child("DIRECTORY", "pkg"),
			child("FILE", "deep", "er", "x.go"),
			child("SUBMODULE", "vendored"),
		}}})
	})
	got, err := f.provider(t, nil).ListDir(t.Context(), ref(), "src", "basesha")
	if err != nil {
		t.Fatal(err)
	}
	want := []provider.DirEntry{{Name: "deep", Dir: true}, {Name: "main.go"}, {Name: "pkg", Dir: true}, {Name: "vendored"}}
	if !slices.Equal(got.Entries, want) || got.Truncated {
		t.Errorf("ListDir = %+v, want %+v", got, want)
	}
}
