package provider

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ValidateReply checks the arguments of Provider.ReplyToComment. Providers
// call it before any request is sent. An empty or whitespace-only body and a
// commentID that is not a positive integer made of ASCII digits both give a
// protocol error with a fixed hint.
func ValidateReply(commentID, body string) error {
	if strings.TrimSpace(body) == "" {
		return &Error{Class: ClassProtocol, Hint: "empty body"}
	}
	if !IsPositiveInt(commentID) {
		return &Error{Class: ClassProtocol, Hint: "invalid comment id"}
	}
	return nil
}

// ValidateEdit checks the arguments of Provider.EditComment with the same
// rules and hints as ValidateReply.
func ValidateEdit(commentID, body string) error {
	return ValidateReply(commentID, body)
}

// ValidateInlineComments checks the items of Provider.PostInlineComments.
// Providers call it before any request is sent. Every item needs a path (and
// an old path, when set) without control characters, a positive line, the
// line type added or context, and a body that is not empty or
// whitespace-only. The first invalid item gives a protocol error with a
// fixed hint; the item's content is never part of the error.
func ValidateInlineComments(items []InlineComment) error {
	for i := range items {
		it := &items[i]
		switch {
		case !validPath(it.Path) || (it.OldPath != "" && !validPath(it.OldPath)):
			return &Error{Class: ClassProtocol, Hint: "invalid inline comment path"}
		case it.Line <= 0:
			return &Error{Class: ClassProtocol, Hint: "invalid inline comment line"}
		case it.LineType != LineAdded && it.LineType != LineContext:
			return &Error{Class: ClassProtocol, Hint: "invalid inline comment line type"}
		case strings.TrimSpace(it.Body) == "":
			return &Error{Class: ClassProtocol, Hint: "empty body"}
		}
	}
	return nil
}

func validPath(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] == 0x7f {
			return false
		}
	}
	return true
}

// IsUser reports whether the author identified by id and name is u. When
// both sides carry an id, the ids decide. Otherwise the names decide,
// compared case-insensitively (both providers treat user names as unique
// regardless of case). An empty name or id never matches.
func IsUser(u User, id, name string) bool {
	if u.ID != "" && id != "" {
		return u.ID == id
	}
	return u.Name != "" && name != "" && strings.EqualFold(u.Name, name)
}

// StopsBatch reports whether err makes further requests of a batch
// pointless: an auth failure, a rate limit, or a canceled or expired
// context. Providers then report the remaining items with the same error
// instead of sending more requests.
func StopsBatch(err error) bool {
	return errors.Is(err, ErrAuth) || errors.Is(err, ErrRateLimited) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.Is(err, ErrTransport) && transportHint(err) == "canceled")
}

func transportHint(err error) string {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Hint
	}
	return ""
}

// genericItemError is the InlineResult.Error for an error that is not a
// *Error.
const genericItemError = "the comment could not be posted"

// ItemError returns the fixed sentence for InlineResult.Error: the
// sentence of a *Error (X-6), or a generic sentence for any other error.
func ItemError(err error) string {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Error()
	}
	return genericItemError
}

// IsPositiveInt reports whether s is a positive base-10 int64 written with
// ASCII digits only (no sign, no "+", no spaces).
func IsPositiveInt(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0
}

// SortThreads orders threads in place: general threads first, by the root
// comment's CreatedAt ascending; then inline threads by path, line and root
// CreatedAt. Ties are broken by root ID (numeric when both are numeric,
// otherwise lexical) so the order is deterministic.
func SortThreads(ts []Thread) {
	sort.SliceStable(ts, func(i, j int) bool {
		a, b := &ts[i], &ts[j]
		if ka, kb := kindRank(a.Kind), kindRank(b.Kind); ka != kb {
			return ka < kb
		}
		if a.Kind == ThreadInline {
			if a.Path != b.Path {
				return a.Path < b.Path
			}
			if a.Line != b.Line {
				return a.Line < b.Line
			}
		}
		ta, tb := rootTime(a), rootTime(b)
		if !ta.Equal(tb) {
			return ta.Before(tb)
		}
		return idLess(a.ID, b.ID)
	})
}

func kindRank(k ThreadKind) int {
	if k == ThreadGeneral {
		return 0
	}
	return 1
}

func rootTime(t *Thread) (ts time.Time) {
	if len(t.Comments) > 0 {
		return t.Comments[0].CreatedAt
	}
	return ts
}

func idLess(a, b string) bool {
	na, ea := strconv.ParseInt(a, 10, 64)
	nb, eb := strconv.ParseInt(b, 10, 64)
	if ea == nil && eb == nil && na != nb {
		return na < nb
	}
	return a < b
}
