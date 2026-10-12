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

// Cap bounds a capped page walk (the *Capped functions): it stops after
// Pages pages or as soon as it holds more than Items items, whichever comes
// first, and reports truncated when the host had more. Zero means no bound
// of that kind; the package ceiling of 1000 pages still applies and stays
// a protocol error.
type Cap struct {
	Pages, Items int
}

// PagesUntilEmpty collects a Gitea-style paged JSON array. path must not
// contain page or limit parameters; "page=N" (from 1) and, when limit > 0,
// "limit=L" are appended. Requests continue until a page comes back empty: a
// short page is NOT the end. More than 1000 pages is a protocol error.
func PagesUntilEmpty[T any](ctx context.Context, c *Client, path string, limit int) ([]T, error) {
	all, _, err := PagesUntilEmptyCapped(ctx, c, path, limit, Cap{}, func(p *[]T) []T { return *p })
	return all, err
}

// PagesUntilEmptyCapped is PagesUntilEmpty for a page whose JSON body is
// an E holding the items (items extracts them), bounded by cp. Gitea does
// not say whether a page is the last, so a walk that stops at cp.Pages
// reports truncated whenever its last page was not empty.
func PagesUntilEmptyCapped[E, T any](ctx context.Context, c *Client, path string, limit int, cp Cap, items func(*E) []T) ([]T, bool, error) {
	var all []T
	for page := 1; page <= maxPages; page++ {
		q := "page=" + strconv.Itoa(page)
		if limit > 0 {
			q += "&limit=" + strconv.Itoa(limit)
		}
		var env E
		if err := c.GetJSON(ctx, appendQuery(path, q), &env); err != nil {
			return nil, false, err
		}
		got := items(&env)
		if len(got) == 0 {
			return all, false, nil
		}
		all = append(all, got...)
		if cp.Items > 0 && len(all) > cp.Items {
			return all[:cp.Items], true, nil
		}
		if page == cp.Pages {
			return all, true, nil
		}
	}
	return nil, false, pageLimitError()
}

// StartLimitPage is one page of a Bitbucket Server paged resource.
type StartLimitPage[T any] struct {
	Values        []T  `json:"values"`
	IsLastPage    bool `json:"isLastPage"`
	NextPageStart *int `json:"nextPageStart"`
}

// PagesStartLimit collects a Bitbucket Server paged resource
// ({values, isLastPage, nextPageStart}). path must not contain start or
// limit. A missing or non-advancing nextPageStart while isLastPage is false,
// or more than 1000 pages, is a protocol error.
func PagesStartLimit[T any](ctx context.Context, c *Client, path string, limit int) ([]T, error) {
	all, _, err := PagesStartLimitCapped(ctx, c, path, limit, Cap{}, func(p *StartLimitPage[T]) *StartLimitPage[T] { return p })
	return all, err
}

// PagesStartLimitCapped is PagesStartLimit for a response body E that
// holds the page (page extracts it, such as the children of a Bitbucket
// browse response), bounded by cp. A body without a page (page returns
// nil) is a protocol error.
func PagesStartLimitCapped[E, T any](ctx context.Context, c *Client, path string, limit int, cp Cap, page func(*E) *StartLimitPage[T]) ([]T, bool, error) {
	var all []T
	start := 0
	for n := 1; n <= maxPages; n++ {
		q := "start=" + strconv.Itoa(start)
		if limit > 0 {
			q += "&limit=" + strconv.Itoa(limit)
		}
		var env E
		if err := c.GetJSON(ctx, appendQuery(path, q), &env); err != nil {
			return nil, false, err
		}
		pg := page(&env)
		if pg == nil {
			return nil, false, &provider.Error{Class: provider.ClassProtocol, Hint: "response is not valid JSON of the expected shape"}
		}
		all = append(all, pg.Values...)
		if cp.Items > 0 && len(all) > cp.Items {
			return all[:cp.Items], true, nil
		}
		if pg.IsLastPage {
			return all, false, nil
		}
		if n == cp.Pages {
			return all, true, nil
		}
		if pg.NextPageStart == nil || *pg.NextPageStart <= start {
			return nil, false, &provider.Error{Class: provider.ClassProtocol, Hint: "paged response lacks a usable nextPageStart"}
		}
		start = *pg.NextPageStart
	}
	return nil, false, pageLimitError()
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
	all, _, err := pagesByLink(ctx, c, fetch, first, pageCap, true, 0, func(p *[]T) []T { return *p })
	return all, err
}

// PagesByLinkCapped is PagesByLink for a page whose JSON body is an E
// holding the items (items extracts them, such as the check_runs of a
// GitHub response), bounded by cp: a next link after cp.Pages pages, or
// more than cp.Items items, stops the walk with truncated instead of an
// error.
func PagesByLinkCapped[E, T any](ctx context.Context, c *Client, fetch PageFetch, first string, cp Cap, items func(*E) []T) ([]T, bool, error) {
	return pagesByLink(ctx, c, fetch, first, cp.Pages, false, cp.Items, items)
}

// pagesByLink is the walk of PagesByLink and PagesByLinkCapped. A next link
// after pageCap pages is a protocol error when capErr is set, else it ends
// the walk with truncated; maxItems > 0 bounds the items.
func pagesByLink[E, T any](ctx context.Context, c *Client, fetch PageFetch, first string, pageCap int, capErr bool, maxItems int, items func(*E) []T) ([]T, bool, error) {
	if fetch == nil {
		fetch = func(ctx context.Context, pq string) ([]byte, http.Header, error) {
			r, err := c.DoRequest(ctx, Request{Method: http.MethodGet, PathAndQuery: pq, MaxBytes: MaxJSONBytes, CapKey: JSONCapKey})
			return r.Data, r.Header, err
		}
	}
	if pageCap <= 0 {
		pageCap, capErr = maxPages, true
	}
	var all []T
	seen := map[string]bool{}
	next := first
	for n := 0; ; n++ {
		if n == pageCap {
			if capErr {
				return nil, false, pageLimitError()
			}
			return all, true, nil
		}
		if seen[next] {
			return nil, false, &provider.Error{Class: provider.ClassProtocol, Hint: "a paged response links back to an earlier page"}
		}
		seen[next] = true
		data, h, err := fetch(ctx, next)
		if err != nil {
			return nil, false, err
		}
		var env E
		if err := decodeJSON(data, &env); err != nil {
			return nil, false, err
		}
		all = append(all, items(&env)...)
		if maxItems > 0 && len(all) > maxItems {
			return all[:maxItems], true, nil
		}
		target, err := NextLink(h.Values("Link"))
		if err != nil {
			return nil, false, err
		}
		if target == "" {
			return all, false, nil
		}
		if next, err = c.PathOf(target); err != nil {
			return nil, false, err
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
