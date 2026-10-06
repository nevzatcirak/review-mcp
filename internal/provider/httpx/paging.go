package httpx

import (
	"context"
	"strconv"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// maxPages is the hard page ceiling; it is a variable only so tests can
// lower it.
var maxPages = 1000

func appendQuery(path, query string) string {
	if strings.Contains(path, "?") {
		return path + "&" + query
	}
	return path + "?" + query
}

func pageLimitError() error {
	return &provider.Error{Class: provider.ClassProtocol, Hint: "pagination did not terminate within the page limit"}
}

// PagesUntilEmpty collects a Gitea-style paged JSON array. path must not
// contain page or limit parameters; "page=N" (from 1) and, when limit > 0,
// "limit=L" are appended. Requests continue until a page comes back empty: a
// short page is NOT the end. More than 1000 pages is a protocol error.
func PagesUntilEmpty[T any](ctx context.Context, c *Client, path string, limit int) ([]T, error) {
	var all []T
	for page := 1; page <= maxPages; page++ {
		q := "page=" + strconv.Itoa(page)
		if limit > 0 {
			q += "&limit=" + strconv.Itoa(limit)
		}
		var items []T
		if err := c.GetJSON(ctx, appendQuery(path, q), &items); err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return all, nil
		}
		all = append(all, items...)
	}
	return nil, pageLimitError()
}

type startLimitPage[T any] struct {
	Values        []T  `json:"values"`
	IsLastPage    bool `json:"isLastPage"`
	NextPageStart *int `json:"nextPageStart"`
}

// PagesStartLimit collects a Bitbucket Server paged resource
// ({values, isLastPage, nextPageStart}). path must not contain start or
// limit. A missing or non-advancing nextPageStart while isLastPage is false,
// or more than 1000 pages, is a protocol error.
func PagesStartLimit[T any](ctx context.Context, c *Client, path string, limit int) ([]T, error) {
	var all []T
	start := 0
	for n := 0; n < maxPages; n++ {
		q := "start=" + strconv.Itoa(start)
		if limit > 0 {
			q += "&limit=" + strconv.Itoa(limit)
		}
		var pg startLimitPage[T]
		if err := c.GetJSON(ctx, appendQuery(path, q), &pg); err != nil {
			return nil, err
		}
		all = append(all, pg.Values...)
		if pg.IsLastPage {
			return all, nil
		}
		if pg.NextPageStart == nil || *pg.NextPageStart <= start {
			return nil, &provider.Error{Class: provider.ClassProtocol, Hint: "paged response lacks a usable nextPageStart"}
		}
		start = *pg.NextPageStart
	}
	return nil, pageLimitError()
}
