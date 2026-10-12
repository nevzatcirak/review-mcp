package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

type item struct {
	N int `json:"n"`
}

// [canary] Gitea: a short page followed by a non-empty page; all items are
// collected.
func TestPagesUntilEmptyShortPageIsNotTheEnd(t *testing.T) {
	pages := map[string][]item{"1": {{1}, {2}, {3}}, "2": {{4}}, "3": {{5}, {6}}, "4": {}}
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		_ = json.NewEncoder(w).Encode(pages[r.URL.Query().Get("page")])
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, nil)
	got, err := PagesUntilEmpty[item](context.Background(), c, "/files?ref=x", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 || got[0].N != 1 || got[5].N != 6 {
		t.Fatalf("got %v", got)
	}
	if len(queries) != 4 || queries[0] != "ref=x&page=1&limit=3" {
		t.Fatalf("queries %v", queries)
	}
}

func TestPagesUntilEmptyNullAndEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("null")) }))
	defer srv.Close()
	c := newClient(t, srv.URL, nil)
	got, err := PagesUntilEmpty[item](context.Background(), c, "/x", 0)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}

// [canary] A fake server that never ends yields a protocol error at the page
// cap. The cap is lowered for the test; the production value is asserted.
func TestPageCap(t *testing.T) {
	if maxPages != 1000 {
		t.Fatalf("production page cap is %d, want 1000", maxPages)
	}
	old := maxPages
	maxPages = 5
	defer func() { maxPages = old }()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if r.URL.Query().Has("page") {
			_ = json.NewEncoder(w).Encode([]item{{int(n)}})
			return
		}
		_, _ = fmt.Fprintf(w, `{"values":[{"n":%d}],"isLastPage":false,"nextPageStart":%d}`, n, n)
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, nil)

	_, err := PagesUntilEmpty[item](context.Background(), c, "/x", 1)
	if !errors.Is(err, provider.ErrProtocol) || hits.Load() != 5 {
		t.Fatalf("PagesUntilEmpty: err=%v hits=%d", err, hits.Load())
	}
	hits.Store(0)
	_, err = PagesStartLimit[item](context.Background(), c, "/y", 1)
	if !errors.Is(err, provider.ErrProtocol) || hits.Load() != 5 {
		t.Fatalf("PagesStartLimit: err=%v hits=%d", err, hits.Load())
	}
}

func TestPagesStartLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, _ := strconv.Atoi(r.URL.Query().Get("start"))
		switch start {
		case 0:
			_, _ = w.Write([]byte(`{"values":[{"n":1},{"n":2}],"size":2,"limit":2,"start":0,"isLastPage":false,"nextPageStart":2}`))
		case 2:
			_, _ = w.Write([]byte(`{"values":[{"n":3}],"isLastPage":true}`))
		default:
			t.Errorf("unexpected start %d", start)
		}
	}))
	defer srv.Close()
	c := newClient(t, srv.URL, nil)
	got, err := PagesStartLimit[item](context.Background(), c, "/changes?since=a", 2)
	if err != nil || len(got) != 3 || got[2].N != 3 {
		t.Fatal(got, err)
	}
}

func TestPagesStartLimitMissingOrStuckNextPageStart(t *testing.T) {
	for name, body := range map[string]string{
		"missing": `{"values":[{"n":1}],"isLastPage":false}`,
		"stuck":   `{"values":[{"n":1}],"isLastPage":false,"nextPageStart":0}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		c := newClient(t, srv.URL, nil)
		_, err := PagesStartLimit[item](context.Background(), c, "/x", 1)
		srv.Close()
		if !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestPagesPropagateErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) }))
	defer srv.Close()
	c := newClient(t, srv.URL, nil)
	if _, err := PagesUntilEmpty[item](context.Background(), c, "/x", 1); !errors.Is(err, provider.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := PagesStartLimit[item](context.Background(), c, "/x", 1); !errors.Is(err, provider.ErrNotFound) {
		t.Fatal(err)
	}
}

type envelope struct {
	Items []item `json:"items"`
}

func envItems(e *envelope) []item { return e.Items }

// The capped walks stop past cp.Items items (cutting to it) or at cp.Pages
// pages, and report truncated only when the host had more; at exactly
// cp.Items items with nothing more, they are not truncated.
func TestCappedWalks(t *testing.T) {
	// Six items on three pages of two.
	untilEmpty := func(w http.ResponseWriter, r *http.Request) {
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var items []item
		if p >= 1 && p <= 3 {
			items = []item{{2*p - 1}, {2 * p}}
		}
		_ = json.NewEncoder(w).Encode(envelope{Items: items})
	}
	startLimit := func(w http.ResponseWriter, r *http.Request) {
		s, _ := strconv.Atoi(r.URL.Query().Get("start"))
		_, _ = fmt.Fprintf(w, `{"page":{"values":[{"n":%d},{"n":%d}],"isLastPage":%v,"nextPageStart":%d}}`, s+1, s+2, s+2 >= 6, s+2)
	}
	type startEnv struct {
		Page *StartLimitPage[item] `json:"page"`
	}
	for _, tc := range []struct {
		name      string
		cp        Cap
		want      int
		truncated bool
	}{
		{"unbounded", Cap{}, 6, false},
		{"items_at_cap", Cap{Items: 6}, 6, false},
		{"items_over_cap", Cap{Items: 5}, 5, true},
		{"items_cut_mid_page", Cap{Items: 3}, 3, true},
		{"pages", Cap{Pages: 2}, 4, true},
		{"pages_at_end", Cap{Pages: 3}, 6, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := func(what string, got []item, truncated bool, err error) {
				t.Helper()
				want := tc.truncated
				if what == "PagesUntilEmptyCapped" && tc.cp.Pages > 0 {
					// Gitea cannot tell the last page: the page cap always
					// reports truncated.
					want = true
				}
				if err != nil || len(got) != tc.want || truncated != want {
					t.Errorf("%s: %d items, truncated %v, err %v; want %d, %v", what, len(got), truncated, err, tc.want, want)
				}
				for i, it := range got {
					if it.N != i+1 {
						t.Errorf("%s: item %d is %d", what, i, it.N)
					}
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(untilEmpty))
			defer srv.Close()
			got, tr, err := PagesUntilEmptyCapped(context.Background(), newClient(t, srv.URL, nil), "/x", 2, tc.cp, envItems)
			check("PagesUntilEmptyCapped", got, tr, err)

			srv2 := httptest.NewServer(http.HandlerFunc(startLimit))
			defer srv2.Close()
			got, tr, err = PagesStartLimitCapped(context.Background(), newClient(t, srv2.URL, nil), "/y", 2, tc.cp,
				func(e *startEnv) *StartLimitPage[item] { return e.Page })
			check("PagesStartLimitCapped", got, tr, err)

			c := newClient(t, "https://h.example.com/ctx", nil)
			fetch := func(_ context.Context, pq string) ([]byte, http.Header, error) {
				p, _ := strconv.Atoi(pq[len(pq)-1:])
				h := http.Header{}
				if p < 3 {
					h.Set("Link", fmt.Sprintf(`<https://h.example.com/ctx/l?p=%d>; rel="next"`, p+1))
				}
				b, _ := json.Marshal(envelope{Items: []item{{2*p - 1}, {2 * p}}})
				return b, h, nil
			}
			got, tr, err = PagesByLinkCapped(context.Background(), c, fetch, "/l?p=1", tc.cp, envItems)
			check("PagesByLinkCapped", got, tr, err)
		})
	}
}

func TestPagesStartLimitCappedWithoutPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"lines":[]}`)) }))
	defer srv.Close()
	type browse struct {
		Children *StartLimitPage[item] `json:"children"`
	}
	_, _, err := PagesStartLimitCapped(context.Background(), newClient(t, srv.URL, nil), "/b", 2, Cap{},
		func(b *browse) *StartLimitPage[item] { return b.Children })
	if !errors.Is(err, provider.ErrProtocol) {
		t.Fatalf("err = %v, want protocol", err)
	}
}
