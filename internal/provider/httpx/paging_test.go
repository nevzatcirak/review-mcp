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
