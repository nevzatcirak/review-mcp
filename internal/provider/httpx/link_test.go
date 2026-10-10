package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

func TestNextLink(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   string
		bad    bool
	}{
		{"none", nil, "", false},
		{"empty", []string{""}, "", false},
		{"github", []string{`<https://api.example.com/repositories/1/pulls/7/files?page=2>; rel="next", <https://api.example.com/repositories/1/pulls/7/files?page=30>; rel="last"`},
			"https://api.example.com/repositories/1/pulls/7/files?page=2", false},
		{"last_page", []string{`<https://api.example.com/x?page=1>; rel="prev", <https://api.example.com/x?page=1>; rel="first"`}, "", false},
		{"rel_list_and_case", []string{`<https://h.example.com/a?c=x>; title="a, b; c"; REL="last Next"`}, "https://h.example.com/a?c=x", false},
		{"unquoted_rel", []string{`<https://h.example.com/a>; rel=next`}, "https://h.example.com/a", false},
		{"two_headers", []string{`<https://h.example.com/p1>; rel="prev"`, `<https://h.example.com/p3>; rel="next"`}, "https://h.example.com/p3", false},
		{"comma_in_target", []string{`<https://h.example.com/a?x=1,2>; rel="next"`}, "https://h.example.com/a?x=1,2", false},
		{"same_next_twice", []string{`<https://h.example.com/a>; rel="next", <https://h.example.com/a>; rel="next"`}, "https://h.example.com/a", false},
		{"two_different_next", []string{`<https://h.example.com/a>; rel="next", <https://h.example.com/b>; rel="next"`}, "", true},
		{"no_brackets", []string{`https://h.example.com/a; rel="next"`}, "", true},
		{"unterminated_target", []string{`<https://h.example.com/a; rel="next"`}, "", true},
		{"unterminated_quote", []string{`<https://h.example.com/a>; rel="next`}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NextLink(c.values)
			if c.bad {
				if !errors.Is(err, provider.ErrProtocol) {
					t.Fatalf("err = %v, want protocol", err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("NextLink = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestPathOf(t *testing.T) {
	c := newClient(t, "https://ghe.example.com/ghe/api/v3", nil)
	ok := map[string]string{
		"https://ghe.example.com/ghe/api/v3/repositories/1/pulls/7/files?page=2": "/repositories/1/pulls/7/files?page=2",
		"https://GHE.EXAMPLE.COM:443/ghe/api/v3/x":                               "/x",
		"HTTPS://ghe.example.com/ghe/api/v3/a%2Fb/c/?q=1":                        "/a%2Fb/c/?q=1",
		"https://ghe.example.com/ghe/api/%76%33/x":                               "/x",
		"/ghe/api/v3/rel?cursor=abc":                                             "/rel?cursor=abc",
		"https://ghe.example.com/ghe/api/v3":                                     "/",
	}
	for in, want := range ok {
		got, err := c.PathOf(in)
		if err != nil || got != want {
			t.Errorf("PathOf(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"http://ghe.example.com/ghe/api/v3/x",       // scheme
		"https://ghe.example.com:8443/ghe/api/v3/x", // port
		"https://other.example.com/ghe/api/v3/x",    // host
		"https://ghe.example.com/ghe/api/v4/x",      // path prefix
		"https://ghe.example.com/ghe/api/v3x",       // not at a segment boundary
		"https://ghe.example.com/ghe/x",             // above the base
		"https://ghe.example.com/ghe/api/v3/../../x",
		"https://ghe.example.com/ghe/api/v3/%2E%2E/x",
		"https://ghe.example.com/ghe/api/v3/./x",
		"https://u:p@ghe.example.com/ghe/api/v3/x",
		"https://ghe.example.com/ghe/api/v3/x#frag",
		"//other.example.com/ghe/api/v3/x",
		"/other/x",
		"https://ghe.example.com/ghe/api/v3/%zz",
		"::not a url",
	} {
		got, err := c.PathOf(in)
		if !errors.Is(err, provider.ErrProtocol) || got != "" {
			t.Errorf("PathOf(%q) = %q, %v; want a protocol error", in, got, err)
			continue
		}
		if strings.Contains(err.Error(), "example.com") {
			t.Errorf("PathOf(%q): error %q echoes the URL", in, err)
		}
	}
}

// linkServer serves pages of item{n} through the Link header with an opaque
// cursor. pages[i] is the content of page i; next(i) is the Link target of
// page i ("" for none).
func linkServer(t *testing.T, pages [][]item, next func(base string, i int) string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		i := 0
		if c := r.URL.Query().Get("cursor"); c != "" {
			_, _ = fmt.Sscanf(c, "p%d", &i)
		}
		if i >= len(pages) {
			http.Error(w, "no such page", http.StatusNotFound)
			return
		}
		if target := next(srv.URL, i); target != "" {
			w.Header().Set("Link", `<`+target+`>; rel="next"`)
		}
		_ = json.NewEncoder(w).Encode(pages[i])
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestPagesByLink(t *testing.T) {
	pages := [][]item{{{1}, {2}}, {{3}}, {{4}, {5}}}
	srv, n := linkServer(t, pages, func(base string, i int) string {
		if i+1 < len(pages) {
			return fmt.Sprintf("%s/ctx/elsewhere?cursor=p%d", base, i+1)
		}
		return ""
	})
	c := newClient(t, srv.URL+"/ctx", nil)
	got, err := PagesByLink[item](context.Background(), c, nil, "/list?per_page=2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[0].N != 1 || got[4].N != 5 || n.Load() != 3 {
		t.Fatalf("got %v after %d requests", got, n.Load())
	}
}

func TestPagesByLinkStops(t *testing.T) {
	t.Run("page_cap", func(t *testing.T) {
		srv, n := linkServer(t, [][]item{{}, {}, {}, {}, {}}, func(base string, i int) string {
			return fmt.Sprintf("%s/ctx/l?cursor=p%d", base, i+1)
		})
		c := newClient(t, srv.URL+"/ctx", nil)
		_, err := PagesByLink[item](context.Background(), c, nil, "/l", 3)
		if !errors.Is(err, provider.ErrProtocol) || n.Load() != 3 {
			t.Fatalf("err = %v after %d requests, want protocol after 3", err, n.Load())
		}
	})
	t.Run("repeated_page", func(t *testing.T) {
		srv, n := linkServer(t, [][]item{{{1}}, {{2}}}, func(base string, _ int) string {
			return base + "/ctx/l?cursor=p1" // page 1 links to itself
		})
		c := newClient(t, srv.URL+"/ctx", nil)
		_, err := PagesByLink[item](context.Background(), c, nil, "/l", 0)
		if !errors.Is(err, provider.ErrProtocol) || n.Load() != 2 {
			t.Fatalf("err = %v after %d requests, want protocol after 2", err, n.Load())
		}
	})
	t.Run("outside_base", func(t *testing.T) {
		srv, n := linkServer(t, [][]item{{{1}}, {{2}}}, func(base string, _ int) string {
			return base + "/other/l?cursor=p1"
		})
		c := newClient(t, srv.URL+"/ctx", nil)
		_, err := PagesByLink[item](context.Background(), c, nil, "/l", 0)
		if !errors.Is(err, provider.ErrProtocol) || n.Load() != 1 {
			t.Fatalf("err = %v after %d requests, want protocol after 1", err, n.Load())
		}
	})
	t.Run("custom_fetch", func(t *testing.T) {
		c := newClient(t, "https://h.example.com/ctx", nil)
		var seen []string
		fetch := func(_ context.Context, pq string) ([]byte, http.Header, error) {
			seen = append(seen, pq)
			h := http.Header{}
			if len(seen) == 1 {
				h.Set("Link", `<https://h.example.com/ctx/next?c=1>; rel="next"`)
			}
			return []byte(`[{"n":7}]`), h, nil
		}
		got, err := PagesByLink[item](context.Background(), c, fetch, "/first", 0)
		if err != nil || len(got) != 2 || len(seen) != 2 || seen[1] != "/next?c=1" {
			t.Fatalf("got %v %v, fetched %q", got, err, seen)
		}
	})
}

// TestRequestOptions: Accept, fixed headers and Classify apply per client,
// DoRequest returns the headers of a non-2xx response without its body, and
// a client without them sends the default Accept and maps statuses as
// before.
func TestRequestOptions(t *testing.T) {
	var accepts, versions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepts = append(accepts, r.Header.Get("Accept"))
		versions = append(versions, r.Header.Get("X-Api-Version"))
		if r.URL.Path == "/limited" {
			w.Header().Set("X-Limit", "0")
			http.Error(w, "body "+testToken, http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	plain := newClient(t, srv.URL, nil)
	custom := newClient(t, srv.URL, func(o *Options) {
		o.Accept = "application/vnd.example+json"
		o.Headers = map[string]string{"X-Api-Version": "2"}
		o.Classify = func(status int, h http.Header) *provider.Error {
			if status == http.StatusForbidden && h.Get("X-Limit") == "0" {
				return &provider.Error{Class: provider.ClassRateLimited, Status: status}
			}
			return nil
		}
	})
	ctx := context.Background()
	if _, _, err := plain.Get(ctx, "/ok", MaxJSONBytes, JSONCapKey); err != nil {
		t.Fatal(err)
	}
	if _, _, err := plain.Get(ctx, "/limited", MaxJSONBytes, JSONCapKey); !errors.Is(err, provider.ErrAuth) {
		t.Fatalf("plain client: err = %v, want auth", err)
	}
	if _, err := custom.DoRequest(ctx, Request{Method: http.MethodGet, PathAndQuery: "/ok", Accept: "application/raw", MaxBytes: 10}); err != nil {
		t.Fatal(err)
	}
	r, err := custom.DoRequest(ctx, Request{Method: http.MethodGet, PathAndQuery: "/limited", MaxBytes: 10})
	if !errors.Is(err, provider.ErrRateLimited) || r.Status != http.StatusForbidden || r.Header.Get("X-Limit") != "0" || r.Data != nil {
		t.Fatalf("custom client: %+v, %v", r, err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("error %q carries the body", err)
	}
	wantAccepts := []string{"application/json, */*;q=0.5", "application/json, */*;q=0.5", "application/raw", "application/vnd.example+json"}
	wantVersions := []string{"", "", "2", "2"}
	for i := range wantAccepts {
		if accepts[i] != wantAccepts[i] || versions[i] != wantVersions[i] {
			t.Errorf("request %d: Accept %q version %q, want %q %q", i, accepts[i], versions[i], wantAccepts[i], wantVersions[i])
		}
	}
}
