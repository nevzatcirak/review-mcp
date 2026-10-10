package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/version"
)

func TestParsePRPath(t *testing.T) {
	cases := []struct {
		in      string
		ns, rep string
		n       int64
		ok      bool
	}{
		{"/octo/demo/pull/7", "octo", "demo", 7, true},
		{"/octo/demo/pull/7/files", "octo", "demo", 7, true},
		{"/octo/demo/pull/7/commits", "octo", "demo", 7, true},
		{"/octo/demo/pull/7/commits/abc123", "octo", "demo", 7, true},
		{"/octo/demo/pull/007", "octo", "demo", 7, true},
		{"/o%20x/de%6Do/pull/12", "o x", "demo", 12, true},
		{"/octo/demo.js/pull/3", "octo", "demo.js", 3, true},
		{"/octo/demo/pulls/7", "", "", 0, false},
		{"/octo/demo/pull/0", "", "", 0, false},
		{"/octo/demo/pull/-1", "", "", 0, false},
		{"/octo/demo/pull/+1", "", "", 0, false},
		{"/octo/demo/pull/1x", "", "", 0, false},
		{"/octo/demo/pull/7.diff", "", "", 0, false},
		{"/octo/demo/pull/", "", "", 0, false},
		{"/octo/demo/pull", "", "", 0, false},
		{"/octo/demo/issues/7", "", "", 0, false},
		{"//demo/pull/7", "", "", 0, false},
		{"/octo//pull/7", "", "", 0, false},
		{"/../demo/pull/7", "", "", 0, false},
		{"/octo/%2e%2e/pull/7", "", "", 0, false},
		{"/octo%2Fsub/demo/pull/7", "", "", 0, false},
		{"/octo%2fsub/demo/pull/7", "", "", 0, false},
		{"/octo/de%2Fmo/pull/7", "", "", 0, false},
		{"/octo/de%2fmo/pull/7", "", "", 0, false},
		{"/octo/sub/demo/pull/7", "", "", 0, false},
		{"/octo/de%zzmo/pull/7", "", "", 0, false},
		{"/octo/demo/pull/99999999999999999999", "", "", 0, false},
		{"octo/demo/pull/7", "", "", 0, false},
		{"", "", "", 0, false},
	}
	for _, c := range cases {
		ns, rep, n, err := NewFactory().ParsePRPath(c.in)
		if (err == nil) != c.ok {
			t.Errorf("%q: err = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && (ns != c.ns || rep != c.rep || n != c.n) {
			t.Errorf("%q: got %q %q %d", c.in, ns, rep, n)
		}
	}
}

// TestResolverGHESWithSubPath: a GitHub Enterprise Server web base with a
// sub-path resolves its PR URLs (the "#..." fragment and trailing segments
// included), and its API calls go to {base}/api/v3.
func TestResolverGHESWithSubPath(t *testing.T) {
	cfg := config.Defaults()
	cfg.GitHub.BaseURL = "https://github.example.com/ghe"
	cfg.Secrets.GitHubToken = config.NewSecret(testToken)
	r := provider.NewResolver(cfg, nil, NewFactory())
	for _, u := range []string{
		"https://github.example.com/ghe/octo/demo/pull/7",
		"https://github.example.com/ghe/octo/demo/pull/7/files",
		"https://github.example.com/ghe/octo/demo/pull/7/commits",
		"https://github.example.com/ghe/octo/demo/pull/7#discussion_r1",
		"https://GITHUB.EXAMPLE.COM:443/ghe/octo/demo/pull/7/files?w=1#diff-1",
	} {
		ref, p, err := r.Resolve(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		if ref.Kind != provider.KindGitHub || ref.Namespace != "octo" || ref.Repo != "demo" || ref.Number != 7 || p.Kind() != provider.KindGitHub {
			t.Errorf("%s: %+v", u, ref)
		}
	}
	for _, u := range []string{
		"https://github.example.com/octo/demo/pull/7",           // outside the sub-path
		"https://github.example.com/ghe/octo/sub/demo/pull/7",   // two-segment owner
		"https://github.example.com/ghe/octo%2Fsub/demo/pull/7", // escaped slash
		"https://github.example.com/ghe/octo/de%2Fmo/pull/7",
	} {
		if _, _, err := r.Resolve(u); err == nil {
			t.Errorf("%s: resolved, want an error", u)
		}
	}
}

func TestNewRequiresBaseURL(t *testing.T) {
	_, err := NewFactory().New(config.Defaults(), nil)
	if !errors.Is(err, provider.ErrURLNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

// TestAPIBase: the API base is derived from the web base ({base}/api/v3,
// under a sub-path too) unless github.api_url names it.
func TestAPIBase(t *testing.T) {
	user := map[string]any{"login": "review-bot", "id": 900}
	t.Run("derived_under_sub_path", func(t *testing.T) {
		f := newFake(t, "/ghe/api/v3")
		f.json("/user", user)
		p, _ := f.provider(f.config("/ghe"), time.Now())
		if _, err := p.CurrentUser(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := f.requests(); !slices.Equal(got, []string{"/ghe/api/v3/user"}) {
			t.Errorf("requests = %q", got)
		}
	})
	t.Run("configured", func(t *testing.T) {
		f := newFake(t, "/custom/api")
		f.json("/user", user)
		cfg := f.config("/ghe")
		cfg.GitHub.APIURL = f.srv.URL + "/custom/api/"
		p, _ := f.provider(cfg, time.Now())
		if _, err := p.CurrentUser(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := f.requests(); !slices.Equal(got, []string{"/custom/api/user"}) {
			t.Errorf("requests = %q", got)
		}
	})
}

func TestRequestHeaders(t *testing.T) {
	f := newFake(t, "/api/v3")
	var got http.Header
	f.handle(http.MethodGet, "/user", func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		writeJSON(w, map[string]any{"login": "review-bot", "id": 900})
	})
	p, _ := f.provider(f.config(""), time.Now())
	u, err := p.CurrentUser(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if u != (provider.User{ID: "900", Name: "review-bot"}) {
		t.Errorf("CurrentUser = %+v", u)
	}
	for k, want := range map[string]string{
		"Authorization":        "Bearer " + testToken,
		"Accept":               "application/vnd.github+json",
		"X-Github-Api-Version": "2022-11-28",
		"User-Agent":           "review-mcp/" + version.Info().Version,
	} {
		if got.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, got.Get(k), want)
		}
	}
}

// pagedHandler serves n items of the form {"commit":{"message":"m<i>"}} in
// pages of size, the first under the requested path and every further page
// only through an opaque cursor in the Link header. A request that names a
// page number fails the test.
func pagedHandler(t *testing.T, f *fake, n, size int, item func(i int) any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("page") != "" {
			t.Errorf("request %s names a page: the next page must come from the Link header", r.URL.RequestURI())
			fakeError(w, http.StatusBadRequest)
			return
		}
		start := 0
		if c := q.Get("after"); c != "" {
			raw, _ := base64.RawURLEncoding.DecodeString(c)
			start, _ = strconv.Atoi(string(raw))
		}
		end := min(start+size, n)
		if end < n {
			next := f.srv.URL + f.prefix + "/repositories/4242/pulls/7/commits?per_page=100&after=" +
				base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
			w.Header().Set("Link", `<`+next+`>; rel="next", <`+f.srv.URL+f.prefix+`/repositories/4242/pulls/7/commits?per_page=100>; rel="first"`)
		}
		items := []any{}
		for i := start; i < end; i++ {
			items = append(items, item(i))
		}
		writeJSON(w, items)
	}
}

// TestPaginationFollowsLink: every page after the first is taken from the
// Link header's rel="next" target (here under another path and with an
// opaque cursor), and the items keep their order.
func TestPaginationFollowsLink(t *testing.T) {
	f := newFake(t, "/api/v3")
	h := pagedHandler(t, f, 7, 3, func(i int) any { return map[string]any{"commit": map[string]any{"message": "m" + strconv.Itoa(i)}} })
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/7/commits", h)
	f.handle(http.MethodGet, "/repositories/4242/pulls/7/commits", h)
	p, _ := f.provider(f.config(""), time.Now())
	msgs, err := p.GetCommitMessages(t.Context(), testRef())
	if err != nil {
		t.Fatalf("GetCommitMessages: %v", err)
	}
	if want := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6"}; !slices.Equal(msgs, want) {
		t.Errorf("messages = %q, want %q", msgs, want)
	}
	reqs := f.requests()
	if len(reqs) != 3 || reqs[0] != "/api/v3/repos/octo/demo/pulls/7/commits?per_page=100" ||
		!strings.HasPrefix(reqs[1], "/api/v3/repositories/4242/pulls/7/commits?per_page=100&after=") {
		t.Errorf("requests = %q", reqs)
	}
}

// TestLinkOutsideAPIBaseRefused: a next link that leaves the pinned API base
// (scheme, host, port or path prefix) stops the walk with a protocol error,
// and nothing is requested from it.
func TestLinkOutsideAPIBaseRefused(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
		writeJSON(w, []any{})
	}))
	t.Cleanup(other.Close)
	f := newFake(t, "/ghe/api/v3")
	origin := f.srv.URL
	host := strings.TrimPrefix(origin, "http://")
	for _, target := range []string{
		other.URL + "/ghe/api/v3/repos/octo/demo/pulls/7/commits?page=2",      // other port
		"https://" + host + "/ghe/api/v3/repos/octo/demo/pulls/7/commits",     // other scheme
		"http://github.example.com/ghe/api/v3/repos/octo/demo/pulls/7/c",      // other host
		origin + "/ghe/api/v4/repos/octo/demo/pulls/7/commits",                // outside the path prefix
		origin + "/ghe/api/v3x/repos",                                         // prefix not at a segment boundary
		origin + "/ghe/api/v3/../../other",                                    // dot segments
		origin + "/ghe/api/v3/%2e%2e/x",                                       // escaped dot segments
		"http://user:pw@" + host + "/ghe/api/v3/repos/octo/demo/pulls/7/c",    // userinfo
		origin + "/ghe/api/v3/repos/octo/demo/pulls/7/commits?after=2#frag",   // fragment
		"/other/api/v3/repos",                                                 // relative, outside the base
		"//" + strings.TrimPrefix(other.URL, "http://") + "/ghe/api/v3/repos", // scheme-relative, other host
	} {
		t.Run(target, func(t *testing.T) {
			f.mu.Lock()
			f.reqs = nil
			f.mu.Unlock()
			f.handle(http.MethodGet, "/repos/octo/demo/pulls/7/commits", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Link", `<`+target+`>; rel="next"`)
				writeJSON(w, []any{map[string]any{"commit": map[string]any{"message": "m"}}})
			})
			p, _ := f.provider(f.config("/ghe"), time.Now())
			_, err := p.GetCommitMessages(t.Context(), testRef())
			if !errors.Is(err, provider.ErrProtocol) {
				t.Fatalf("err = %v, want protocol", err)
			}
			if strings.Contains(err.Error(), target) || strings.Contains(err.Error(), "github.example.com") {
				t.Errorf("error %q echoes the link", err)
			}
			if n := len(f.requests()); n != 1 {
				t.Errorf("%d requests to the API, want 1", n)
			}
		})
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("%d requests left the API base", n)
	}
}

// TestPageCap: a server that keeps sending next links is stopped at the
// list's page cap with a protocol error.
func TestPageCap(t *testing.T) {
	f := newFake(t, "/api/v3")
	var n atomic.Int32
	h := func(w http.ResponseWriter, _ *http.Request) {
		i := n.Add(1)
		w.Header().Set("Link", `<`+f.srv.URL+`/api/v3/repositories/4242/pulls/7/commits?after=`+strconv.Itoa(int(i))+`>; rel="next"`)
		writeJSON(w, []any{})
	}
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/7/commits", h)
	f.handle(http.MethodGet, "/repositories/4242/pulls/7/commits", h)
	p, _ := f.provider(f.config(""), time.Now())
	_, err := p.GetCommitMessages(t.Context(), testRef())
	if !errors.Is(err, provider.ErrProtocol) || !strings.Contains(err.Error(), "page limit") {
		t.Fatalf("err = %v, want the page-limit protocol error", err)
	}
	if got := n.Load(); got != commitsPageCap {
		t.Errorf("%d pages requested, want the cap %d", got, commitsPageCap)
	}
}

// TestRateLimit: a 403 or 429 with X-RateLimit-Remaining: 0 or Retry-After
// is rate_limited with the reset time in UTC; the one bounded wait happens
// only within MaxRateLimitWait and before the deadline, and is followed by
// exactly one retry; a 403 without those headers is auth (forbidden).
func TestRateLimit(t *testing.T) {
	// The clock is the real one (whole seconds), so that a context deadline
	// derived from it is meaningful to the HTTP client too.
	clock := time.Now().UTC().Truncate(time.Second)
	epoch := func(d time.Duration) string { return strconv.FormatInt(clock.Add(d).Unix(), 10) }
	limited := func(status int, reset time.Duration) string {
		return "the server rate-limited the request (HTTP " + strconv.Itoa(status) + "): " + RateLimitHint(clock.Add(reset))
	}
	cases := []struct {
		name      string
		status    int
		headers   map[string]string
		recovers  bool          // the retry succeeds
		deadline  time.Duration // 0: no deadline
		wantErr   *provider.Error
		wantText  string
		wantWaits []time.Duration
		wantReqs  int
	}{
		{
			name: "primary_403_reset_far", status: 403,
			headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": epoch(30 * time.Minute)},
			wantErr: provider.ErrRateLimited, wantReqs: 1,
			wantText: limited(403, 30*time.Minute),
		},
		{
			name: "primary_403_reset_soon_waits_once", status: 403, recovers: true,
			headers:   map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": epoch(20 * time.Second)},
			wantWaits: []time.Duration{20 * time.Second}, wantReqs: 2,
		},
		{
			name: "secondary_429_retry_after_waits_once", status: 429, recovers: true,
			headers:   map[string]string{"Retry-After": "45"},
			wantWaits: []time.Duration{45 * time.Second}, wantReqs: 2,
		},
		{
			name: "secondary_403_still_limited_after_the_one_retry", status: 403,
			headers: map[string]string{"Retry-After": "60"},
			wantErr: provider.ErrRateLimited, wantWaits: []time.Duration{60 * time.Second}, wantReqs: 2,
			wantText: limited(403, 60*time.Second),
		},
		{
			name: "wait_over_60s_not_taken", status: 403,
			headers: map[string]string{"Retry-After": "61"},
			wantErr: provider.ErrRateLimited, wantReqs: 1,
			wantText: limited(403, 61*time.Second),
		},
		{
			name: "wait_past_deadline_not_taken", status: 429, deadline: 10 * time.Second,
			headers: map[string]string{"Retry-After": "30"},
			wantErr: provider.ErrRateLimited, wantReqs: 1,
			wantText: limited(429, 30*time.Second),
		},
		{
			name: "429_without_reset", status: 429,
			wantErr: provider.ErrRateLimited, wantReqs: 1,
			wantText: "the server rate-limited the request (HTTP 429)",
		},
		{
			name: "unusable_reset_not_shown", status: 403,
			headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "soon"},
			wantErr: provider.ErrRateLimited, wantReqs: 1,
			wantText: "the server rate-limited the request (HTTP 403)",
		},
		{
			name: "forbidden_without_rate_limit_headers", status: 403,
			wantErr: provider.ErrAuth, wantReqs: 1,
			wantText: "authentication failed: check the token and its scopes (HTTP 403)",
		},
		{
			name: "forbidden_with_remaining_quota", status: 403,
			headers: map[string]string{"X-RateLimit-Remaining": "12", "X-RateLimit-Reset": epoch(time.Minute)},
			wantErr: provider.ErrAuth, wantReqs: 1,
			wantText: "authentication failed: check the token and its scopes (HTTP 403)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, "/api/v3")
			var n atomic.Int32
			f.handle(http.MethodGet, "/user", func(w http.ResponseWriter, _ *http.Request) {
				if n.Add(1) == 2 && c.recovers {
					writeJSON(w, map[string]any{"login": "review-bot", "id": 900})
					return
				}
				for k, v := range c.headers {
					w.Header().Set(k, v)
				}
				fakeError(w, c.status)
			})
			p, slept := f.provider(f.config(""), clock)
			ctx := context.Background()
			if c.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, clock.Add(c.deadline))
				defer cancel()
			}
			_, err := p.CurrentUser(ctx)
			switch {
			case c.wantErr == nil && err != nil:
				t.Fatalf("err = %v, want success after the retry", err)
			case c.wantErr != nil && !errors.Is(err, c.wantErr):
				t.Fatalf("err = %v, want class %s", err, c.wantErr.Class)
			}
			if c.wantErr != nil && err.Error() != c.wantText {
				t.Errorf("error text = %q, want %q", err.Error(), c.wantText)
			}
			if err != nil && (strings.Contains(err.Error(), testSentinel) || strings.Contains(err.Error(), testToken)) {
				t.Errorf("error text %q carries the body or the token", err)
			}
			if !slices.Equal(*slept, c.wantWaits) {
				t.Errorf("waits = %v, want %v", *slept, c.wantWaits)
			}
			if got := int(n.Load()); got != c.wantReqs {
				t.Errorf("%d requests, want %d", got, c.wantReqs)
			}
		})
	}
}

// TestRateLimitSentence pins the fixed sentence of a rate limit: the reset
// time in UTC, from X-RateLimit-Reset (epoch seconds) or Retry-After
// (seconds or an HTTP date), and never anything of the body.
func TestRateLimitSentence(t *testing.T) {
	now := time.Date(2026, 10, 9, 14, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	for _, c := range []struct {
		status  int
		headers map[string]string
		want    string
	}{
		{403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(now.Add(30*time.Minute).Unix(), 10)},
			"the server rate-limited the request (HTTP 403): retry after 2026-10-09 12:30:00 UTC"},
		{429, map[string]string{"Retry-After": "90"},
			"the server rate-limited the request (HTTP 429): retry after 2026-10-09 12:01:30 UTC"},
		{403, map[string]string{"Retry-After": "Fri, 09 Oct 2026 12:05:00 GMT"},
			"the server rate-limited the request (HTTP 403): retry after 2026-10-09 12:05:00 UTC"},
		{403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "99999999999"},
			"the server rate-limited the request (HTTP 403)"},
		{403, nil, "authentication failed: check the token and its scopes (HTTP 403)"},
	} {
		h := http.Header{}
		for k, v := range c.headers {
			h.Set(k, v)
		}
		if got := classify(c.status, h, now).Error(); got != c.want {
			t.Errorf("%d %v: %q, want %q", c.status, c.headers, got, c.want)
		}
	}
}

// TestRateLimitWaitCanceled: a wait cut short by the caller's context ends
// the call with a transport error and no retry.
func TestRateLimitWaitCanceled(t *testing.T) {
	f := newFake(t, "/api/v3")
	var n atomic.Int32
	f.handle(http.MethodGet, "/user", func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", "5")
		fakeError(w, http.StatusTooManyRequests)
	})
	p, _ := f.provider(f.config(""), time.Now())
	p.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	_, err := p.CurrentUser(t.Context())
	if !errors.Is(err, provider.ErrTransport) || n.Load() != 1 {
		t.Fatalf("err = %v after %d requests, want transport after 1", err, n.Load())
	}
}

func TestGetPullRequest(t *testing.T) {
	const base, head, mergeBase = "tip0000", "head000", "mb00000"
	t.Run("merge_base_and_fields", func(t *testing.T) {
		f := newFake(t, "/api/v3")
		pj := prJSON(base, head)
		pj["body"] = nil
		pj["state"] = "closed"
		pj["merged"] = false
		pj["merged_at"] = "2026-10-01T10:00:00Z"
		pj["draft"] = true
		pj["mergeable"] = nil
		pj["mergeable_state"] = "unknown"
		pj["changed_files"] = 3
		f.json("/repos/octo/demo/pulls/7", pj)
		f.json("/repos/octo/demo/compare/"+base+"..."+head, map[string]any{"merge_base_commit": map[string]any{"sha": mergeBase}})
		p, _ := f.provider(f.config(""), time.Now())
		pr, err := p.GetPullRequest(t.Context(), testRef())
		if err != nil {
			t.Fatal(err)
		}
		if pr.Title != "Add feature" || pr.Description != "" || pr.Author != "alice" || pr.SourceBranch != "feature" ||
			pr.TargetBranch != "main" || pr.HeadSHA != head || pr.BaseSHA != mergeBase ||
			pr.BaseStrategy != provider.BaseGitHubMergeBase || pr.State != "closed" || !pr.Merged ||
			pr.Draft == nil || !*pr.Draft || pr.Mergeable != nil || pr.MergeableState != "unknown" ||
			pr.ChangedFiles != 3 || pr.WebURL != "https://github.example.com/octo/demo/pull/7" {
			t.Errorf("pull request = %+v", pr)
		}
		if got := f.requests(); got[1] != "/api/v3/repos/octo/demo/compare/"+base+"..."+head+"?per_page=1" {
			t.Errorf("compare request = %q", got[1])
		}
	})
	for _, c := range []struct {
		name    string
		compare http.HandlerFunc
	}{
		{"compare_fails", func(w http.ResponseWriter, _ *http.Request) { fakeError(w, http.StatusNotFound) }},
		{"compare_without_merge_base", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]any{"status": "diverged"}) }},
		{"compare_not_json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }},
	} {
		t.Run(c.name+"_falls_back_to_base_sha", func(t *testing.T) {
			f := newFake(t, "/api/v3")
			f.json("/repos/octo/demo/pulls/7", prJSON(base, head))
			f.handle(http.MethodGet, "/repos/octo/demo/compare/"+base+"..."+head, c.compare)
			p, _ := f.provider(f.config(""), time.Now())
			pr, err := p.GetPullRequest(t.Context(), testRef())
			if err != nil {
				t.Fatal(err)
			}
			if pr.BaseSHA != base || pr.BaseStrategy != provider.BaseGitHubBaseSHA {
				t.Errorf("base %q strategy %q, want %q %q", pr.BaseSHA, pr.BaseStrategy, base, provider.BaseGitHubBaseSHA)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// TestDiffFileShapes: GitHub's file statuses and patch-less files map to
// the provider-neutral shapes.
func TestDiffFileShapes(t *testing.T) {
	f := newFake(t, "/api/v3")
	file := func(name, status string, add, del int, patch *string, prev string) map[string]any {
		m := map[string]any{"filename": name, "status": status, "additions": add, "deletions": del, "changes": add + del}
		if patch != nil {
			m["patch"] = *patch
		}
		if prev != "" {
			m["previous_filename"] = prev
		}
		return m
	}
	f.json("/repos/octo/demo/pulls/7/files", []any{
		file("a/modified.go", "modified", 1, 1, strPtr("@@ -1 +1 @@\n-a\n+b"), ""),
		file("a/copied.go", "copied", 2, 0, strPtr("@@ -0,0 +1,2 @@\n+x\n+y"), "a/source.go"),
		file("a/new.go", "renamed", 1, 1, strPtr("@@ -1,2 +1,2 @@\n k\n-a\n+b"), "a/old.go"),
		file("a/moved.go", "renamed", 0, 0, nil, "a/was.go"),
		file("a/mode.sh", "changed", 0, 0, nil, ""),
		file("a/huge.sql", "modified", 9000, 10, nil, ""),
		file("a/logo.png", "modified", 0, 0, nil, ""),
		file("a/empty.txt", "added", 0, 0, nil, ""),
		file("a/empty2.txt", "modified", 0, 0, strPtr(""), ""),
		file("a/headers.go", "modified", 1, 0, strPtr("diff --git a/x b/x\n@@ -1 +1,2 @@\n k\n+n\n\\ No newline at end of file"), ""),
		file("a/nohunk.go", "modified", 1, 0, strPtr("garbage"), ""),
		file("a/gone.go", "removed", 0, 1, strPtr("@@ -1 +0,0 @@\n-a"), ""),
		file("a/unchanged.go", "unchanged", 0, 0, strPtr("@@ -1 +1 @@\n k"), ""),
		file("a/filtered.go", "modified", 1, 1, strPtr("@@ -1 +1 @@\n-a\n+b"), ""),
	})
	cfg := f.config("")
	cfg.Diff.MaxFilesFullContent = 0 // no content requests
	p, _ := f.provider(cfg, time.Now())
	pr := &provider.PullRequest{BaseSHA: "b", HeadSHA: "h", BaseStrategy: provider.BaseGitHubMergeBase, ChangedFiles: 14}
	d, err := p.GetDiff(t.Context(), testRef(), pr, provider.DiffOptions{Include: func(p string) bool { return p != "a/filtered.go" }})
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		path, old string
		typ       provider.ChangeType
		patch     string
	}
	var got []row
	for _, fp := range d.Files {
		got = append(got, row{fp.Path, fp.OldPath, fp.Type, fp.Patch})
		if fp.BaseStatus != provider.ContentNotFetchedFileCap && fp.BaseStatus != provider.ContentNotApplicable {
			t.Errorf("%s: base status %q", fp.Path, fp.BaseStatus)
		}
	}
	want := []row{
		{"a/modified.go", "", provider.ChangeModified, "@@ -1 +1 @@\n-a\n+b\n"},
		{"a/copied.go", "", provider.ChangeAdded, "@@ -0,0 +1,2 @@\n+x\n+y\n"},
		{"a/new.go", "a/old.go", provider.ChangeRenamed, "@@ -1,2 +1,2 @@\n k\n-a\n+b\n"},
		{"a/moved.go", "a/was.go", provider.ChangeRenamed, ""},
		{"a/mode.sh", "", provider.ChangeModified, ""},
		{"a/empty.txt", "", provider.ChangeAdded, ""},
		{"a/empty2.txt", "", provider.ChangeModified, ""},
		{"a/headers.go", "", provider.ChangeModified, "@@ -1 +1,2 @@\n k\n+n\n\\ No newline at end of file\n"},
		{"a/gone.go", "", provider.ChangeDeleted, "@@ -1 +0,0 @@\n-a\n"},
		{"a/unchanged.go", "", provider.ChangeModified, "@@ -1 +1 @@\n k\n"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("files =\n%+v\nwant\n%+v", got, want)
	}
	wantSkipped := []provider.SkippedFile{
		{Path: "a/huge.sql", Reason: provider.SkipSizeLimit},
		{Path: "a/logo.png", Reason: provider.SkipBinary},
		{Path: "a/nohunk.go", Reason: provider.SkipFetchFailed},
		{Path: "a/filtered.go", Reason: provider.SkipFiltered},
	}
	if !slices.Equal(d.Skipped, wantSkipped) {
		t.Errorf("skipped = %+v, want %+v", d.Skipped, wantSkipped)
	}
	if d.Notes != nil || d.BaseStrategy != provider.BaseGitHubMergeBase {
		t.Errorf("notes %q, strategy %q", d.Notes, d.BaseStrategy)
	}
}

// TestDiffFileListingCap: GitHub lists at most 3000 files; the ones beyond
// are reported in a fixed note with their number, taken from the pull
// request's changed_files.
func TestDiffFileListingCap(t *testing.T) {
	f := newFake(t, "/api/v3")
	const listed, changed = maxListedFiles, maxListedFiles + 2
	h := func(w http.ResponseWriter, r *http.Request) {
		start := 0
		if c := r.URL.Query().Get("after"); c != "" {
			start, _ = strconv.Atoi(c)
		}
		end := min(start+perPage, listed)
		if end < listed {
			w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/repositories/4242/pulls/7/files?per_page=100&after=%d>; rel="next"`, f.srv.URL, end))
		}
		var items []any
		for i := start; i < end; i++ {
			items = append(items, map[string]any{"filename": fmt.Sprintf("gen/f%04d.txt", i), "status": "added",
				"additions": 1, "deletions": 0, "changes": 1, "patch": "@@ -0,0 +1 @@\n+x"})
		}
		writeJSON(w, items)
	}
	f.handle(http.MethodGet, "/repos/octo/demo/pulls/7/files", h)
	f.handle(http.MethodGet, "/repositories/4242/pulls/7/files", h)
	cfg := f.config("")
	cfg.Diff.MaxFilesFullContent = 0
	p, _ := f.provider(cfg, time.Now())
	pr := &provider.PullRequest{BaseSHA: "b", HeadSHA: "h", BaseStrategy: provider.BaseGitHubMergeBase, ChangedFiles: changed}
	d, err := p.GetDiff(t.Context(), testRef(), pr, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != listed {
		t.Errorf("%d files listed, want %d", len(d.Files), listed)
	}
	want := "GitHub lists at most 3000 files of a pull request: 2 more changed files were not listed, so they are not reviewed (file_limit)."
	if !slices.Equal(d.Notes, []string{want}) {
		t.Errorf("notes = %q, want %q", d.Notes, want)
	}
	if n := len(f.requests()); n != listed/perPage {
		t.Errorf("%d page requests, want %d", n, listed/perPage)
	}
}

// TestDiffContents: contents are fetched raw at the base and head
// revisions, a file over diff.max_file_bytes is not_fetched_size_limit, and
// files beyond diff.max_files_full_content are not_fetched_file_limit.
func TestDiffContents(t *testing.T) {
	f := newFake(t, "/api/v3")
	f.json("/repos/octo/demo/pulls/7/files", []any{
		map[string]any{"filename": "dir/a b.go", "status": "renamed", "previous_filename": "dir/old.go",
			"additions": 1, "deletions": 1, "changes": 2, "patch": "@@ -1 +1 @@\n-a\n+b"},
		map[string]any{"filename": "big.txt", "status": "modified", "additions": 1, "deletions": 1, "changes": 2, "patch": "@@ -1 +1 @@\n-a\n+b"},
		map[string]any{"filename": "third.txt", "status": "modified", "additions": 1, "deletions": 1, "changes": 2, "patch": "@@ -1 +1 @@\n-a\n+b"},
	})
	raw := func(content string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Accept") != "application/vnd.github.raw" {
				t.Errorf("%s: Accept %q", r.URL.RequestURI(), r.Header.Get("Accept"))
			}
			_, _ = w.Write([]byte(content))
		}
	}
	f.handle(http.MethodGet, "/repos/octo/demo/contents/dir/old.go", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "base-sha" {
			t.Errorf("base content at ref %q", r.URL.Query().Get("ref"))
		}
		raw("a\n")(w, r)
	})
	f.handle(http.MethodGet, "/repos/octo/demo/contents/dir/a%20b.go", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ref") != "head-sha" {
			t.Errorf("head content at ref %q", r.URL.Query().Get("ref"))
		}
		raw("b\n")(w, r)
	})
	f.handle(http.MethodGet, "/repos/octo/demo/contents/big.txt", raw(strings.Repeat("x", 65)))
	cfg := f.config("")
	cfg.Diff.MaxFilesFullContent = 2
	cfg.Diff.MaxFileBytes = 64
	p, _ := f.provider(cfg, time.Now())
	pr := &provider.PullRequest{BaseSHA: "base-sha", HeadSHA: "head-sha", BaseStrategy: provider.BaseGitHubMergeBase}
	d, err := p.GetDiff(t.Context(), testRef(), pr, provider.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 3 {
		t.Fatalf("files = %+v", d.Files)
	}
	a, big, third := d.Files[0], d.Files[1], d.Files[2]
	if a.BaseContent == nil || *a.BaseContent != "a\n" || a.HeadContent == nil || *a.HeadContent != "b\n" ||
		a.BaseStatus != provider.ContentFull || a.HeadStatus != provider.ContentFull {
		t.Errorf("renamed file: %+v", a)
	}
	if big.BaseStatus != provider.ContentNotFetchedSizeCap || big.HeadStatus != provider.ContentNotFetchedSizeCap ||
		big.BaseContent != nil || big.HeadContent != nil {
		t.Errorf("big file: %+v", big)
	}
	if third.BaseStatus != provider.ContentNotFetchedFileCap || third.HeadStatus != provider.ContentNotFetchedFileCap {
		t.Errorf("third file: %+v", third)
	}
}

func TestFileLineURL(t *testing.T) {
	cfg := config.Defaults()
	cfg.GitHub.BaseURL = "https://github.example.com/ghe/"
	pp, err := NewFactory().New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := &provider.PullRequest{HeadSHA: "abc123"}
	if got, want := pp.FileLineURL(testRef(), pr, "dir/a b.go", 12), "https://github.example.com/ghe/octo/demo/blob/abc123/dir/a%20b.go#L12"; got != want {
		t.Errorf("FileLineURL = %q, want %q", got, want)
	}
	if got := pp.FileLineURL(testRef(), &provider.PullRequest{}, "a.go", 1); got != "" {
		t.Errorf("FileLineURL without a head = %q", got)
	}
}
