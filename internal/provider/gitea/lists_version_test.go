package gitea_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/provider/contract"
	"github.com/nevzatcirak/review-mcp/internal/provider/gitea"
)

// TestListVersionSupported: the issue search's role filters exist since
// Gitea 1.14 (WP-19a DQ-10). A Forgejo version is compared by its
// "+gitea-A.B" part, or else counts as new enough from major 2; anything
// else that does not parse is unknown.
func TestListVersionSupported(t *testing.T) {
	for _, c := range []struct {
		v         string
		ok, known bool
	}{
		{"1.13.0", false, true},
		{"1.13.7", false, true},
		{"1.12", false, true},
		{"0.9.99", false, true},
		{"1.14.0", true, true},
		{"1.14", true, true},
		{"1.21.11", true, true},
		{"1.22.0+dev-123-gabc", true, true},
		{"1.14.0-rc1", true, true},
		{"1.13.0-rc1+meta", false, true},
		{"7.0.1+gitea-1.21.11", true, true},
		{"7.0.1+gitea-1.13.2", false, true},
		{"1.19.0-2+gitea-1.19.0", true, true},
		{"11.0.0", true, true},
		{"2", true, true},
		{"7.0.1+gitea-dev", true, true},
		{"", false, false},
		{"1", false, false},
		{"development", false, false},
		{"v1.21.0", false, false},
		{"1x.2", false, false},
		{"1.x", false, false},
		{"1234567.0", false, false},
	} {
		if ok, known := gitea.ListVersionSupported(c.v); ok != c.ok || known != c.known {
			t.Errorf("ListVersionSupported(%q) = %v, %v; want %v, %v", c.v, ok, known, c.ok, c.known)
		}
	}
}

const searchPath = "GET /api/v1/repos/issues/search"

// versionListSpec is a list of one valid pull request a reviewer has.
func versionListSpec() contract.ListSpec {
	return contract.ListSpec{TokenUser: contract.User{ID: 900, Login: "review-bot"},
		Items: map[provider.ListRole][]contract.ListItem{provider.ListReviewer: {{Namespace: "octo", Repo: "demo",
			Number: 7, Title: "T", Author: contract.User{ID: 1, Login: "alice"}}}},
		Env: contract.Env{Token: "tok-gitea-version"}} //nolint:gosec // synthetic test value
}

// TestListRefusedBefore114: on Gitea 1.13 the list is unsupported_version
// with HintListMinVersion, and the version GET is the only request: the
// search, which would ignore the role filter there, is never sent.
func TestListRefusedBefore114(t *testing.T) {
	p, _, h := serveCtLists(t, versionListSpec())
	h.versionBody = `{"version":"1.13.0"}`
	l, err := p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListReviewer, Limit: 30})
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Class != provider.ClassUnsupportedVersion || pe.Hint != gitea.HintListMinVersion || l != nil {
		t.Errorf("ListMyPullRequests on 1.13.0 = %+v, %v; want nil and unsupported_version: %s", l, err, gitea.HintListMinVersion)
	}
	if got := h.requests(); len(got) != 1 || got[0] != "GET /api/v1/version" {
		t.Errorf("requests %q, want only GET /api/v1/version (no search on a Gitea before 1.14)", got)
	}
}

// TestListGoesAheadOnVersion: a new enough version, and any answer that
// does not give a version, let the list go on to the search.
func TestListGoesAheadOnVersion(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"1.14.0", 0, `{"version":"1.14.0"}`},
		{"dev build", 0, `{"version":"1.22.0+dev-123-gabc"}`},
		{"forgejo", 0, `{"version":"7.0.1+gitea-1.21.11"}`},
		{"not numeric", 0, `{"version":"development"}`},
		{"no version", 0, `{}`},
		{"empty body", 0, ``},
		{"not JSON", 0, `<html>Gitea</html>`},
		{"404", http.StatusNotFound, ``},
		{"401", http.StatusUnauthorized, ``},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, _, h := serveCtLists(t, versionListSpec())
			h.versionStatus, h.versionBody = c.status, c.body
			l, err := p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListReviewer, Limit: 30})
			if err != nil || len(l.Items) != 1 {
				t.Fatalf("ListMyPullRequests = %+v, %v; want the one pull request", l, err)
			}
			if got := h.requests(); len(got) != 2 || got[0] != "GET /api/v1/version" || got[1] != searchPath {
				t.Errorf("requests %q, want the version GET, then the search", got)
			}
		})
	}
}

// TestListVersionUnknownLog: a version that does not parse is logged at
// debug level as unknown, without the version text.
func TestListVersionUnknownLog(t *testing.T) {
	ls := versionListSpec()
	h := &ctIssueSearchVersion{body: `{"version":"secret-build-7f3a"}`}
	var buf bytes.Buffer
	cfg := config.Defaults()
	cfg.Gitea.BaseURL = contract.StartListServer(t, ls, h)
	cfg.Secrets.GiteaToken = config.NewSecret(ls.Env.Token)
	p, err := gitea.NewFactory().New(cfg, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.(provider.PRLister).ListMyPullRequests(t.Context(), provider.ListQuery{Role: provider.ListAuthor, Limit: 30})
	if out := buf.String(); !strings.Contains(out, "gitea version unknown") || strings.Contains(out, "secret-build") {
		t.Errorf("log %q: want \"gitea version unknown\" and no version text", out)
	}
}

// ctIssueSearchVersion answers the version GET with body and every other
// request with an empty search.
type ctIssueSearchVersion struct{ body string }

func (h *ctIssueSearchVersion) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/version" {
		_, _ = w.Write([]byte(h.body))
		return
	}
	w.Header().Set("X-Total-Count", "0")
	ctJSON(w, http.StatusOK, []any{})
}
