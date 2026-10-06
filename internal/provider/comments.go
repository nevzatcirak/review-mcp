package provider

import (
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
