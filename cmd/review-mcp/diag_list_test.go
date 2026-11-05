package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/tools"
)

const (
	listDiagTitle = "LISTDIAG-TITLE-5b7e"
	listDiagToken = "FAKE-diag-list-token-5b7e-do-not-leak" //nolint:gosec // synthetic test value
)

// listGitea serves the Gitea endpoints of a list: the version probe and the
// issue search, which answers one pull request, or status when it is set.
type listGitea struct {
	srv      *httptest.Server
	status   atomic.Int64
	searches atomic.Int64
}

func newListGitea(t *testing.T) *listGitea {
	t.Helper()
	f := &listGitea{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/gitea/api/v1/version":
			_, _ = w.Write([]byte(`{"version":"1.21.11"}`))
		case "/gitea/api/v1/repos/issues/search":
			f.searches.Add(1)
			if st := int(f.status.Load()); st != 0 {
				http.Error(w, "denied "+listDiagToken, st)
				return
			}
			_, _ = w.Write([]byte(`[{"number":7,"title":"` + listDiagTitle + ` ` + "`x`" + `","updated_at":"2026-11-04T10:00:00Z",` +
				`"user":{"login":"alice"},"repository":{"owner":"octo","name":"demo"},"pull_request":{"draft":true}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *listGitea) env() map[string]string {
	env := diagEnv(nil, nil)
	env["REVIEW_MCP_GITEA_BASE_URL"] = f.srv.URL + "/gitea"
	env["REVIEW_MCP_GITEA_TOKEN"] = listDiagToken
	return env
}

// TestDiagListMarkdownAndJSON: the markdown is the default, --json prints
// the structured result; exit 0.
func TestDiagListMarkdownAndJSON(t *testing.T) {
	f := newListGitea(t)
	code, out, errs := diag(f.env(), "list", "authored")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	if !strings.HasPrefix(out, "## Your open pull requests\n") || !strings.Contains(out, listDiagTitle) ||
		!strings.Contains(out, "[demo!7]("+f.srv.URL+"/gitea/octo/demo/pulls/7)") || !strings.Contains(out, "· draft") {
		t.Errorf("markdown:\n%s", out)
	}

	code, out, errs = diag(f.env(), "list", "review-requests", "--include-assigned", "--json", "--limit", "5", "--provider", "gitea")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	var res tools.PRListResult
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("stdout is not a PRListResult: %v\n%s", err, out)
	}
	if res.Role != tools.ListRoleReviewRequests || len(res.Items) != 1 || res.Items[0].Number != 7 ||
		len(res.Items[0].Roles) != 2 || len(res.Providers) != 1 || res.Providers[0].Status != tools.ListStatusOK {
		t.Errorf("result = %+v", res)
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
	for _, s := range []string{out, errs} {
		if strings.Contains(s, listDiagToken) {
			t.Error("the token is in the output")
		}
	}
}

// TestDiagListFailedProviderExitsOne: a failed provider is reported in the
// output and the exit code is 1; the token and the host's text are not.
func TestDiagListFailedProviderExitsOne(t *testing.T) {
	f := newListGitea(t)
	f.status.Store(http.StatusForbidden)
	code, out, errs := diag(f.env(), "list", "review-requests")
	if code != 1 {
		t.Fatalf("exit %d; want 1\nstdout:\n%s\nstderr:\n%s", code, out, errs)
	}
	if !strings.Contains(out, "No pull requests were listed.") || !strings.Contains(out, "`gitea`: failed (forbidden)") {
		t.Errorf("markdown:\n%s", out)
	}
	for _, s := range []string{out, errs} {
		if strings.Contains(s, listDiagToken) || strings.Contains(s, "denied") {
			t.Errorf("output leaks:\n%s", s)
		}
	}
}

// TestDiagListUsage: usage errors exit 2 and send nothing.
func TestDiagListUsage(t *testing.T) {
	f := newListGitea(t)
	for name, args := range map[string][]string{
		"no subcommand":          {"list"},
		"unknown subcommand":     {"list", "assigned"},
		"limit zero":             {"list", "authored", "--limit", "0"},
		"limit too large":        {"list", "authored", "--limit", "101"},
		"limit not a number":     {"list", "authored", "--limit", "many"},
		"assigned on authored":   {"list", "authored", "--include-assigned"},
		"unknown provider":       {"list", "authored", "--provider", "svn"},
		"provider not enabled":   {"list", "authored", "--provider", "github"},
		"stray argument":         {"list", "authored", "now"},
		"unknown flag":           {"list", "review-requests", "--all"},
		"provider flag no value": {"list", "authored", "--provider"},
	} {
		if code, out, errs := diag(f.env(), args...); code != 2 || out != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr:\n%s", name, code, out, errs)
		}
	}
	if n := f.searches.Load(); n != 0 {
		t.Errorf("usage errors sent %d searches", n)
	}
	// The unconfigured provider is named with the configured ones.
	_, _, errs := diag(f.env(), "list", "authored", "--provider", "github")
	if !strings.Contains(errs, "the github provider is not configured; configured providers: gitea") {
		t.Errorf("stderr:\n%s", errs)
	}
}

// TestDiagListNoProvider: with no provider configured the configuration is
// invalid (as for every diag command); exit 1, nothing on stdout.
func TestDiagListNoProvider(t *testing.T) {
	code, out, errs := diag(diagEnv(nil, nil), "list", "authored")
	if code != 1 || out != "" || !strings.Contains(errs, "no provider enabled") {
		t.Errorf("exit %d, stdout %q, stderr:\n%s", code, out, errs)
	}
}

// TestDiagListUsageText: the usage names the subcommand.
func TestDiagListUsageText(t *testing.T) {
	if !strings.Contains(diagUsageText, "review-mcp diag list review-requests|authored") ||
		!strings.Contains(diagUsageText, "diag list     ") {
		t.Error("diagUsageText lacks diag list")
	}
}
