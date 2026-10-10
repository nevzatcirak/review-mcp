package httpx

import (
	"context"
	"net/http"
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

// PageFetch performs the GET of one page and returns its body and its
// response headers. PagesByLink uses it so that a provider can add its own
// handling around every page request (GitHub's bounded rate-limit wait).
type PageFetch func(ctx context.Context, pathAndQuery string) ([]byte, http.Header, error)

// PagesByLink collects a JSON array paged through the RFC 8288 Link header
// (GitHub, GitLab). It requests first, then the rel="next" target of each
// page, until a page has no next link. The next page is only ever taken from
// the Link header, never guessed. Every target must lie under the client's
// base URL (PathOf); a target outside it stops the walk with a protocol
// error and is never requested. A target that repeats an earlier page, an
// unparsable Link header, or a next link after pageCap pages is a protocol
// error. fetch nil means a plain GET through c (10 MiB cap); pageCap <= 0
// means the package ceiling of 1000 pages.
func PagesByLink[T any](ctx context.Context, c *Client, fetch PageFetch, first string, pageCap int) ([]T, error) {
	if fetch == nil {
		fetch = func(ctx context.Context, pq string) ([]byte, http.Header, error) {
			r, err := c.DoRequest(ctx, Request{Method: http.MethodGet, PathAndQuery: pq, MaxBytes: MaxJSONBytes, CapKey: JSONCapKey})
			return r.Data, r.Header, err
		}
	}
	if pageCap <= 0 {
		pageCap = maxPages
	}
	var all []T
	seen := map[string]bool{}
	next := first
	for n := 0; ; n++ {
		if n == pageCap {
			return nil, pageLimitError()
		}
		if seen[next] {
			return nil, &provider.Error{Class: provider.ClassProtocol, Hint: "a paged response links back to an earlier page"}
		}
		seen[next] = true
		data, h, err := fetch(ctx, next)
		if err != nil {
			return nil, err
		}
		var items []T
		if err := decodeJSON(data, &items); err != nil {
			return nil, err
		}
		all = append(all, items...)
		target, err := NextLink(h.Values("Link"))
		if err != nil {
			return nil, err
		}
		if target == "" {
			return all, nil
		}
		if next, err = c.PathOf(target); err != nil {
			return nil, err
		}
	}
}

var errBadLink = &provider.Error{Class: provider.ClassProtocol, Hint: "the Link header of a paged response could not be read"}

// NextLink returns the target of the rel="next" link among the values of
// the Link response headers, or "" when there is none. A header that does
// not parse as "<target>; param; param, <target>; ..." or two different next
// targets are a protocol error. The target is returned as sent: callers
// pass it through Client.PathOf before any request.
func NextLink(values []string) (string, error) {
	next := ""
	for _, v := range values {
		s := v
		for {
			s = strings.TrimLeft(s, " \t,")
			if s == "" {
				break
			}
			if s[0] != '<' {
				return "", errBadLink
			}
			end := strings.IndexByte(s, '>')
			if end < 0 {
				return "", errBadLink
			}
			target := s[1:end]
			params, rest, ok := cutLinkParams(s[end+1:])
			if !ok {
				return "", errBadLink
			}
			s = rest
			if !relHas(params, "next") {
				continue
			}
			if next != "" && next != target {
				return "", errBadLink
			}
			next = target
		}
	}
	return next, nil
}

// cutLinkParams splits s at the first comma outside a quoted string. ok is
// false for an unterminated quoted string.
func cutLinkParams(s string) (params, rest string, ok bool) {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case inQuote && c == '\\':
			i++
		case c == '"':
			inQuote = !inQuote
		case !inQuote && c == ',':
			return s[:i], s[i+1:], true
		}
	}
	if inQuote {
		return "", "", false
	}
	return s, "", true
}

// relHas reports whether the link parameters carry a rel whose
// space-separated values include want (case-insensitively).
func relHas(params, want string) bool {
	for _, p := range strings.Split(params, ";") {
		name, value, found := strings.Cut(strings.TrimSpace(p), "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "rel") {
			continue
		}
		for _, r := range strings.Fields(strings.Trim(strings.TrimSpace(value), `"`)) {
			if strings.EqualFold(r, want) {
				return true
			}
		}
	}
	return false
}
