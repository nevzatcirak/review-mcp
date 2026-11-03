package provider

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ListRole selects which of the token user's pull requests a list holds
// (X-48).
type ListRole string

// List roles.
const (
	ListReviewer ListRole = "reviewer"
	ListAssignee ListRole = "assignee"
	ListAuthor   ListRole = "author"
)

// ListQuery is one list request: open pull requests only (X-48).
type ListQuery struct {
	Role ListRole
	// Limit is 1..MaxListLimit, already validated by the caller; a provider
	// refuses any other value before any request (ValidateListQuery).
	Limit int
}

// MaxListLimit is the largest ListQuery.Limit.
const MaxListLimit = 100

// Cuts of the untrusted text of a listed pull request, in runes.
const (
	MaxListTitleRunes  = 200
	MaxListAuthorRunes = 100
)

// ListedPR is one pull request of a list. Title and Author are untrusted
// third-party text, already cleaned with CleanListText.
type ListedPR struct {
	// Ref names the pull request: Kind, Namespace, Repo and Number from the
	// parsed response, URL built by the provider from its configured web
	// base (ListedRef), never copied from the response (X-48 §0 item 7).
	Ref    PRRef
	Title  string
	Author string
	// TargetBranch is nil when the list endpoint does not carry it, and
	// when the name it carries is not a ValidBranchName.
	TargetBranch *string
	// Draft is nil when the host does not report it.
	Draft     *bool
	UpdatedAt time.Time
	// MyStatus is the token user's own reviewer status where the list
	// carries it (Bitbucket Server: "approved", "needs_work",
	// "unapproved"); "" otherwise. Never another user's status.
	MyStatus string
}

// Values of ListedPR.MyStatus.
const (
	MyStatusApproved   = "approved"
	MyStatusNeedsWork  = "needs_work"
	MyStatusUnapproved = "unapproved"
)

// PRList is one provider's answer to one ListQuery.
type PRList struct {
	// Items are newest update first, at most the query's Limit
	// (FinishList).
	Items []ListedPR
	// HasMore is true when the host says that more pull requests match
	// than this one page holds.
	HasMore bool
	// Dropped counts the items of the host's page that were left out
	// because their repository path or number did not validate
	// (ListedRef), or, on GitHub, because they were not a pull request of
	// the configured host.
	//
	// DESIGN-QUESTION (WP-19a DQ-1): the spec's PRList has no such field,
	// but §1.2 asks for a note in the tool result when items are dropped,
	// which the tool can only write when it knows. Recommendation: keep.
	Dropped int
}

// PRLister is implemented by providers whose Capabilities().Lists allows
// at least one role. The contract suite checks both directions.
type PRLister interface {
	// ListMyPullRequests returns one page of the token user's open pull
	// requests in the role q.Role. It validates q with ValidateListQuery
	// before any request is sent. It only ever sends GET requests.
	ListMyPullRequests(ctx context.Context, q ListQuery) (*PRList, error)
}

// ListCaps says which roles the host can list for the token's own user.
type ListCaps struct{ Reviewer, Assignee, Author bool }

// Allows reports whether c allows role; it is false for an unknown role.
func (c ListCaps) Allows(role ListRole) bool {
	switch role {
	case ListReviewer:
		return c.Reviewer
	case ListAssignee:
		return c.Assignee
	case ListAuthor:
		return c.Author
	}
	return false
}

// Any reports whether c allows at least one role.
func (c ListCaps) Any() bool { return c.Reviewer || c.Assignee || c.Author }

// ValidateListQuery checks q against caps. Providers call it before any
// request is sent. A role caps does not allow (an unknown one included) is
// an unsupported error; a Limit outside 1..MaxListLimit a protocol error.
// Both carry fixed hints.
func ValidateListQuery(caps ListCaps, q ListQuery) error {
	switch {
	case !caps.Allows(q.Role):
		return &Error{Class: ClassUnsupported, Hint: "the provider cannot list pull requests in this role"}
	case q.Limit < 1 || q.Limit > MaxListLimit:
		return &Error{Class: ClassProtocol, Hint: "invalid list limit"}
	}
	return nil
}

// CleanListText makes an untrusted title or name of a list safe to show:
// invalid UTF-8 is replaced; newlines, carriage returns and tabs become
// spaces; every other C0 or C1 control character, U+2028, U+2029 and the
// bidirectional formatting characters are removed; runs of spaces become
// one space; the result is trimmed and cut at max runes (a rune boundary),
// then trimmed again. One implementation serves all four providers.
func CleanListText(s string, maxRunes int) string {
	s = strings.ToValidUTF8(s, "�")
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == ' ':
			space = true
			continue
		case unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || isBidiControl(r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := b.String()
	if maxRunes >= 0 && utf8.RuneCountInString(out) > maxRunes {
		out = strings.TrimRight(string([]rune(out)[:maxRunes]), " ")
	}
	return out
}

// ListBranch is the TargetBranch of a listed pull request for the name the
// host sent: nil for "" and for a name that is not a ValidBranchName, which
// is never shown.
func ListBranch(name string) *string {
	if !ValidBranchName(name) {
		return nil
	}
	return &name
}

// ListedRef builds the PRRef of a listed pull request from the parts the
// provider parsed out of the host's answer. remainder is the pull request's
// escaped web path below webBase, built by the provider's own URL builder;
// it is parsed with parse (the provider's Factory.ParsePRPath, the
// validation of the resolver), and ok is false unless the parse succeeds
// and gives back exactly namespace, repo and number. The URL is webBase
// followed by remainder: built, never copied from a response.
func ListedRef(kind Kind, webBase, remainder string, namespace, repo string, number int64,
	parse func(remainder string) (string, string, int64, error)) (PRRef, bool) {
	if namespace == "" || repo == "" || number <= 0 || !strings.HasPrefix(remainder, "/") {
		return PRRef{}, false
	}
	ns, r, n, err := parse(remainder)
	if err != nil || ns != namespace || r != repo || n != number {
		return PRRef{}, false
	}
	return PRRef{Kind: kind, Namespace: namespace, Repo: repo, Number: number,
		URL: strings.TrimRight(webBase, "/") + remainder}, true
}

// FinishList sorts items by UpdatedAt, newest first (then by URL, for a
// stable order), cuts them to limit and sets HasMore when items were cut
// or hasMore is already true (the host said there were more). Providers
// build every PRList with it.
func FinishList(items []ListedPR, limit int, hasMore bool, dropped int) *PRList {
	sorted := slices.Clone(items)
	slices.SortStableFunc(sorted, func(a, b ListedPR) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.Ref.URL, b.Ref.URL)
	})
	if limit >= 0 && len(sorted) > limit {
		sorted, hasMore = sorted[:limit], true
	}
	if sorted == nil {
		sorted = []ListedPR{}
	}
	return &PRList{Items: sorted, HasMore: hasMore, Dropped: dropped}
}
