package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/mdutil"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// The two list tools (V-15, X-48) share one engine: they differ only in the
// roles they ask the providers for and in the sentences of their markdown.

// Roles of a list result (PRListResult.Role).
const (
	ListRoleReviewRequests = "review_requests"
	ListRoleAuthored       = "authored"
)

// Limits of the list tools. They are constants, not configuration keys
// (X-48 §0 item 10).
const (
	DefaultListLimit = 30
	MaxListLimit     = provider.MaxListLimit
)

// Status values of ListProviderStatus.
const (
	ListStatusOK          = "ok"
	ListStatusFailed      = "failed"
	ListStatusUnsupported = "unsupported"
	ListStatusNoToken     = "no_token"
)

// Fixed reasons of a failed provider (X-6): the only text a provider
// failure contributes to a list result.
const (
	ListReasonAuth               = "auth"
	ListReasonForbidden          = "forbidden"
	ListReasonRateLimited        = "rate_limited"
	ListReasonTimeout            = "timeout"
	ListReasonUnavailable        = "unavailable"
	ListReasonUnsupportedVersion = "unsupported_version"
	ListReasonBadResponse        = "bad_response"
)

// ListReasons are the values of ListProviderStatus.Reason, for the declared
// schema.
var ListReasons = []string{ListReasonAuth, ListReasonForbidden, ListReasonRateLimited, ListReasonTimeout,
	ListReasonUnavailable, ListReasonUnsupportedVersion, ListReasonBadResponse}

// Notes of a list result. They are fixed sentences; docs/pr-lists.md copies
// them from here.
const (
	// NoteListMore is added when the cut dropped items or a provider has
	// more pull requests than its page held.
	NoteListMore = "More pull requests exist than this list shows; raise limit (at most 100) or name a provider."
	// NoteListProviderFailedFormat takes the provider's name and the fixed
	// reason.
	NoteListProviderFailedFormat = "%s could not be listed (%s); the list does not include its pull requests."
	// NoteListRoleMissingFormat takes the provider's name, the role that is
	// missing and the fixed reason: one of the two queries of
	// include_assigned failed and the other one worked.
	NoteListRoleMissingFormat = "%s could not list its %s (%s); the list does not include them."
	// NoteListNoAssignees is added for Bitbucket Server when include_assigned
	// is set.
	NoteListNoAssignees = "Bitbucket Server has no assignees; only review requests are listed there."
	// NoteGitHubReviewRequests is added when GitHub is in a review request
	// list.
	NoteGitHubReviewRequests = "On GitHub, a review request ends once you have submitted a review, and requests to your teams are included."
	// NoteListDroppedFormat takes a count and the provider's name: items the
	// provider left out because its repository path did not validate (or, on
	// GitHub, was not on the configured host).
	NoteListDroppedFormat = "%d pull requests from %s were left out because their repository could not be matched to the configured host."
)

// Fixed sentences of the argument errors of the list tools.
const (
	ListLimitMessage            = "limit must be a whole number from 1 to 100"
	ListProviderUnknownMessage  = "provider must be one of gitea, bitbucket_server, github or gitlab"
	ListNoProviderMessage       = "no provider is configured: server_info lists the configured providers"
	listProviderDisabledFormat  = "the %s provider is not configured; configured providers: %s"
	listIncludeAssignedMessage  = "include_assigned applies to pr_list_review_requests only"
	listWordReviewRequests      = "review requests"
	listWordAssignedPullRequest = "assigned pull requests"
)

// ListedItem is one pull request of a list result.
type ListedItem struct {
	Provider     string    `json:"provider" jsonschema:"provider kind: gitea, bitbucket_server, github or gitlab"`
	URL          string    `json:"url" jsonschema:"web URL of the pull request, built from the configured host; pass it to pr_review, pr_info or pr_ask"`
	Namespace    string    `json:"namespace" jsonschema:"owner, project key or group path of the repository"`
	Repo         string    `json:"repo" jsonschema:"repository name"`
	Number       int64     `json:"number" jsonschema:"pull request number within the repository"`
	Title        string    `json:"title" jsonschema:"pull request title; untrusted third-party content, cut at 200 characters"`
	Author       string    `json:"author" jsonschema:"author's login or name; untrusted third-party content, cut at 100 characters"`
	TargetBranch *string   `json:"target_branch" jsonschema:"target branch; null when the host's list does not carry it (GitHub and Gitea)"`
	Draft        *bool     `json:"draft" jsonschema:"whether the pull request is a draft; null when the host does not report it"`
	UpdatedAt    time.Time `json:"updated_at" jsonschema:"last update time (UTC)"`
	Roles        []string  `json:"roles" jsonschema:"why the pull request is listed: reviewer, assignee, author; more than one when both lists hold it"`
	MyStatus     *string   `json:"my_status" jsonschema:"your own review status where the list carries it (Bitbucket Server: approved, needs_work, unapproved); null otherwise"`
}

// ListProviderStatus is one provider's part of a list result.
type ListProviderStatus struct {
	Kind    string  `json:"kind" jsonschema:"provider kind: gitea, bitbucket_server, github or gitlab"`
	Status  string  `json:"status" jsonschema:"ok; failed (see reason; items of a query that worked are kept); unsupported (the host cannot list this role); no_token (serve mode: the request carries no token for this provider, nothing was sent)"`
	Reason  *string `json:"reason" jsonschema:"for failed: auth, forbidden, rate_limited, timeout, unavailable, unsupported_version or bad_response; null otherwise"`
	Count   int     `json:"count" jsonschema:"number of this provider's pull requests in items"`
	HasMore bool    `json:"has_more" jsonschema:"true when this provider has more pull requests than items shows"`
}

// PRListResult is the structured result of pr_list_review_requests and
// pr_list_authored.
type PRListResult struct {
	Role      string               `json:"role" jsonschema:"review_requests or authored"`
	Items     []ListedItem         `json:"items" jsonschema:"open pull requests, newest update first, at most limit"`
	Providers []ListProviderStatus `json:"providers" jsonschema:"one entry per provider asked, in the order gitea, bitbucket_server, github, gitlab"`
	Notes     []string             `json:"notes" jsonschema:"fixed notes about what the list cannot show"`
}

// PRListArgs are the arguments of the list tools.
type PRListArgs struct {
	// Role is ListRoleReviewRequests or ListRoleAuthored.
	Role string
	// Provider restricts the call to one kind; "" asks every enabled one.
	Provider string
	// Limit is nil for the default; otherwise 1..MaxListLimit.
	Limit           *int
	IncludeAssigned bool
}

// providerNames are the names the notes use.
var providerNames = map[provider.Kind]string{
	provider.KindGitea:           "Gitea",
	provider.KindBitbucketServer: "Bitbucket Server",
	provider.KindGitHub:          "GitHub",
	provider.KindGitLab:          "GitLab",
}

func providerName(k provider.Kind) string {
	if n, ok := providerNames[k]; ok {
		return n
	}
	return string(k)
}

// listOutcome is what one provider contributed.
type listOutcome struct {
	status  ListProviderStatus
	items   []provider.ListedPR
	roles   [][]provider.ListRole // roles[i] are the roles of items[i]
	notes   []string
	dropped int
}

// PRList runs the list for a.Role over the providers src offers, in
// parallel, and merges what they answer. A provider that fails is reported
// with a fixed reason; it never empties or silently shortens the list. The
// logger receives counts and kinds only: never a title, a name, a URL or a
// token.
func PRList(ctx context.Context, src ListSource, a PRListArgs, log *slog.Logger) (PRListResult, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	limit := DefaultListLimit
	if a.Limit != nil {
		limit = *a.Limit
	}
	if limit < 1 || limit > MaxListLimit {
		return PRListResult{}, &ArgumentError{ListLimitMessage}
	}
	if a.IncludeAssigned && a.Role != ListRoleReviewRequests {
		return PRListResult{}, &ArgumentError{listIncludeAssignedMessage}
	}
	if src == nil {
		return PRListResult{}, &ArgumentError{ListNoProviderMessage}
	}
	enabled := src.EnabledKinds()
	if len(enabled) == 0 {
		return PRListResult{}, &ArgumentError{ListNoProviderMessage}
	}
	kinds := enabled
	if a.Provider != "" {
		k := provider.Kind(a.Provider)
		if !slices.Contains([]provider.Kind{provider.KindGitea, provider.KindBitbucketServer, provider.KindGitHub,
			provider.KindGitLab}, k) {
			return PRListResult{}, &ArgumentError{ListProviderUnknownMessage}
		}
		if !slices.Contains(enabled, k) {
			return PRListResult{}, &ArgumentError{fmt.Sprintf(listProviderDisabledFormat, k, kindList(enabled))}
		}
		kinds = []provider.Kind{k}
	}

	providers, noToken := src.ListProviders(kinds)
	byKind := map[provider.Kind]provider.Provider{}
	for _, p := range providers {
		byKind[p.Kind()] = p
	}

	outcomes := make([]*listOutcome, len(kinds))
	var wg sync.WaitGroup
	for i, k := range kinds {
		switch p, built := byKind[k]; {
		case slices.Contains(noToken, k):
			outcomes[i] = &listOutcome{status: ListProviderStatus{Kind: string(k), Status: ListStatusNoToken}}
		case !built:
			// Enabled, but the provider could not be built.
			outcomes[i] = failedOutcome(k, ListReasonUnavailable)
		default:
			wg.Add(1)
			go func() {
				defer wg.Done()
				outcomes[i] = listOne(ctx, p, a, limit)
			}()
		}
	}
	wg.Wait()

	res := mergeOutcomes(a.Role, outcomes, limit)
	failed, noTok := 0, 0
	for _, s := range res.Providers {
		switch s.Status {
		case ListStatusFailed:
			failed++
		case ListStatusNoToken:
			noTok++
		}
		log.Debug("pull request list", "role", a.Role, "provider", s.Kind, "status", s.Status, "count", s.Count,
			"has_more", s.HasMore)
	}
	log.Debug("pull request lists merged", "role", a.Role, "providers", len(res.Providers), "items", len(res.Items),
		"failed", failed, "no_token", noTok, "limit", limit)
	return res, nil
}

func kindList(ks []provider.Kind) string {
	names := make([]string, len(ks))
	for i, k := range ks {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

func failedOutcome(k provider.Kind, reason string) *listOutcome {
	return &listOutcome{
		status: ListProviderStatus{Kind: string(k), Status: ListStatusFailed, Reason: &reason},
		notes:  []string{fmt.Sprintf(NoteListProviderFailedFormat, providerName(k), reason)},
	}
}

// mainRole is the role a list tool asks every provider for.
func mainRole(role string) provider.ListRole {
	if role == ListRoleAuthored {
		return provider.ListAuthor
	}
	return provider.ListReviewer
}

// listOne runs the queries of one provider.
func listOne(ctx context.Context, p provider.Provider, a PRListArgs, limit int) *listOutcome {
	kind := p.Kind()
	out := &listOutcome{status: ListProviderStatus{Kind: string(kind), Status: ListStatusOK}}
	lister, ok := p.(provider.PRLister)
	caps := p.Capabilities().Lists
	main := mainRole(a.Role)
	if !ok || !caps.Allows(main) {
		out.status.Status = ListStatusUnsupported
		return out
	}

	type query struct {
		role provider.ListRole
		word string // names the role in NoteListRoleMissingFormat
	}
	queries := []query{{main, listWordReviewRequests}}
	if a.Role == ListRoleAuthored {
		queries[0].word = "pull requests"
	}
	if a.IncludeAssigned {
		if caps.Allows(provider.ListAssignee) {
			queries = append(queries, query{provider.ListAssignee, listWordAssignedPullRequest})
		} else if kind == provider.KindBitbucketServer {
			out.notes = append(out.notes, NoteListNoAssignees)
		}
	}

	var firstReason string
	var failedWords []string
	var failedReasons []string
	index := map[string]int{} // URL -> position in out.items
	for qi, q := range queries {
		l, err := lister.ListMyPullRequests(ctx, provider.ListQuery{Role: q.role, Limit: limit})
		if err == nil && l == nil {
			err = &provider.Error{Class: provider.ClassProtocol}
		}
		if err != nil {
			// DESIGN-QUESTION (WP-19b DQ-2): the spec leaves open what an
			// unsupported error of the main query is. It is the status
			// unsupported (no reason) and no note; the same error of the
			// assignee query cannot happen for a provider whose caps allow
			// the role and is treated as any other failure. Recommendation:
			// keep.
			if qi == 0 && errors.Is(err, provider.ErrUnsupported) {
				out.status.Status = ListStatusUnsupported
				return out
			}
			reason := listReason(err)
			if firstReason == "" {
				firstReason = reason
			}
			failedWords = append(failedWords, q.word)
			failedReasons = append(failedReasons, reason)
			continue
		}
		out.dropped += l.Dropped
		out.status.HasMore = out.status.HasMore || l.HasMore
		for _, it := range l.Items {
			if i, ok := index[it.Ref.URL]; ok {
				out.roles[i] = append(out.roles[i], q.role)
				continue
			}
			index[it.Ref.URL] = len(out.items)
			out.items = append(out.items, it)
			out.roles = append(out.roles, []provider.ListRole{q.role})
		}
	}

	switch {
	case len(failedWords) == len(queries):
		// Nothing worked: the provider is failed and has no items.
		out.status.Status, out.status.Reason = ListStatusFailed, &firstReason
		out.status.HasMore = false
		out.notes = append(out.notes, fmt.Sprintf(NoteListProviderFailedFormat, providerName(kind), firstReason))
	case len(failedWords) > 0:
		// One query of include_assigned worked, the other one failed: keep
		// the items and name the role that is missing.
		out.status.Status, out.status.Reason = ListStatusFailed, &firstReason
		for i, w := range failedWords {
			out.notes = append(out.notes, fmt.Sprintf(NoteListRoleMissingFormat, providerName(kind), w, failedReasons[i]))
		}
	}
	return out
}

// listReason maps an error of a list request to one of the fixed reasons.
//
// DESIGN-QUESTION (WP-19b DQ-1): the spec names the reasons but not the
// mapping from the provider error classes. auth is a 401 (or an auth error
// without a status) and forbidden a 403; rate_limited is rate_limited;
// timeout is a transport error whose hint says so (or a deadline), and every
// other transport error and upstream (5xx) is unavailable;
// unsupported_version is itself; not_found, protocol, too_large and every
// other class, and an error that is not a provider error, are bad_response.
// A 404 from a list endpoint (an old host without it) is therefore
// bad_response, not unsupported_version. Recommendation: keep; revisit
// after the acceptance runs against real hosts.
func listReason(err error) string {
	var pe *provider.Error
	if errors.As(err, &pe) {
		switch pe.Class {
		case provider.ClassAuth:
			if pe.Status == 403 {
				return ListReasonForbidden
			}
			return ListReasonAuth
		case provider.ClassRateLimited:
			return ListReasonRateLimited
		case provider.ClassTransport:
			if pe.Hint == "timeout" {
				return ListReasonTimeout
			}
			return ListReasonUnavailable
		case provider.ClassUpstream:
			return ListReasonUnavailable
		case provider.ClassUnsupportedVersion:
			return ListReasonUnsupportedVersion
		}
		return ListReasonBadResponse
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ListReasonTimeout
	}
	return ListReasonBadResponse
}

// mergeOutcomes builds the result: items sorted by update time (newest
// first), then provider kind, then URL, cut at limit; one status per
// provider; the notes.
func mergeOutcomes(role string, outcomes []*listOutcome, limit int) PRListResult {
	type entry struct {
		kind  string
		it    provider.ListedPR
		roles []provider.ListRole
	}
	var all []entry
	for _, o := range outcomes {
		for i, it := range o.items {
			all = append(all, entry{string(it.Ref.Kind), it, o.roles[i]})
		}
	}
	slices.SortStableFunc(all, func(a, b entry) int {
		if c := b.it.UpdatedAt.Compare(a.it.UpdatedAt); c != 0 {
			return c
		}
		if c := strings.Compare(a.kind, b.kind); c != 0 {
			return c
		}
		return strings.Compare(a.it.Ref.URL, b.it.Ref.URL)
	})
	cut := false
	lost := map[string]bool{}
	if len(all) > limit {
		cut = true
		for _, e := range all[limit:] {
			lost[e.kind] = true
		}
		all = all[:limit]
	}

	res := PRListResult{Role: role, Items: make([]ListedItem, 0, len(all)), Providers: []ListProviderStatus{},
		Notes: []string{}}
	counts := map[string]int{}
	for _, e := range all {
		counts[e.kind]++
		roles := make([]string, len(e.roles))
		for i, r := range e.roles {
			roles[i] = string(r)
		}
		item := ListedItem{
			Provider: e.kind, URL: e.it.Ref.URL, Namespace: e.it.Ref.Namespace, Repo: e.it.Ref.Repo,
			Number: e.it.Ref.Number, Title: e.it.Title, Author: e.it.Author, TargetBranch: e.it.TargetBranch,
			Draft: e.it.Draft, UpdatedAt: e.it.UpdatedAt.UTC(), Roles: roles,
		}
		if e.it.MyStatus != "" {
			s := e.it.MyStatus
			item.MyStatus = &s
		}
		res.Items = append(res.Items, item)
	}

	more := cut
	var notes []string
	for _, o := range outcomes {
		s := o.status
		// DESIGN-QUESTION (WP-19b DQ-3): the spec does not say what count
		// means once the items of the two include_assigned queries are
		// merged and the list is cut. It is the number of the provider's
		// pull requests in items, so that the counts add up to len(items);
		// has_more is the provider's own signal, or true when the cut
		// dropped one of its items. Recommendation: keep.
		s.Count = counts[s.Kind]
		s.HasMore = s.HasMore || lost[s.Kind]
		more = more || s.HasMore
		res.Providers = append(res.Providers, s)
		notes = append(notes, o.notes...)
		if o.dropped > 0 {
			notes = append(notes, fmt.Sprintf(NoteListDroppedFormat, o.dropped, providerName(provider.Kind(s.Kind))))
		}
		if role == ListRoleReviewRequests && s.Kind == string(provider.KindGitHub) && s.Status == ListStatusOK {
			notes = append(notes, NoteGitHubReviewRequests)
		}
	}
	if more {
		notes = append(notes, NoteListMore)
	}
	res.Notes = append(res.Notes, notes...)
	return res
}

// RenderPRListMarkdown renders res for a client. Third-party text (title,
// author, target branch) appears only inside code spans (X-30); the
// pull request is a link to its built URL. It is a list, not a table, so
// that no cell can be broken by a title.
func RenderPRListMarkdown(res PRListResult) string {
	var b strings.Builder
	heading, empty := "Open pull requests that ask for your review", "No open pull requests ask for your review."
	if res.Role == ListRoleAuthored {
		heading, empty = "Your open pull requests", "You have no open pull requests."
	}
	b.WriteString("## " + heading + "\n\n")
	allOK := true
	for _, s := range res.Providers {
		if s.Status != ListStatusOK {
			allOK = false
		}
	}
	if len(res.Items) == 0 {
		if allOK {
			b.WriteString(empty + "\n")
		} else {
			b.WriteString("No pull requests were listed.\n")
		}
	}
	for _, it := range res.Items {
		b.WriteString("- " + mdutil.CodeSpan(it.Provider) + " " + itemLink(it) + " " + mdutil.CodeSpan(it.Title) +
			" by " + mdutil.CodeSpan(it.Author))
		if it.Draft != nil && *it.Draft {
			b.WriteString(" · draft")
		}
		if it.TargetBranch != nil {
			b.WriteString(" · target " + mdutil.CodeSpan(*it.TargetBranch))
		}
		b.WriteString(" · updated " + it.UpdatedAt.UTC().Format("2006-01-02"))
		if len(it.Roles) > 0 {
			b.WriteString(" · as " + strings.Join(it.Roles, ", "))
		}
		if it.MyStatus != nil {
			b.WriteString(" · your status " + mdutil.CodeSpan(*it.MyStatus))
		}
		b.WriteString("\n")
	}
	b.WriteString("\nProviders:\n")
	for _, s := range res.Providers {
		b.WriteString("- " + mdutil.CodeSpan(s.Kind) + ": " + providerLine(s) + "\n")
	}
	if len(res.Notes) > 0 {
		b.WriteString("\nNotes:\n")
		for _, n := range res.Notes {
			b.WriteString("- " + n + "\n")
		}
	}
	return b.String()
}

// itemLink is the link of a pull request: namespace/repo!number, to its built
// URL. The namespace is part of the text so that two repositories with the
// same name in different namespaces look different; a GitLab nested group
// keeps its full path (group/sub/project!42).
func itemLink(it ListedItem) string {
	text := mdutil.Escape(it.Namespace + "/" + it.Repo + "!" + strconv.FormatInt(it.Number, 10))
	dest := strings.NewReplacer("(", "%28", ")", "%29", "<", "%3C", ">", "%3E", " ", "%20").Replace(it.URL)
	return "[" + text + "](" + dest + ")"
}

func providerLine(s ListProviderStatus) string {
	var line string
	switch s.Status {
	case ListStatusFailed:
		reason := "failed"
		if s.Reason != nil {
			reason = "failed (" + *s.Reason + ")"
		}
		line = reason
		if s.Count > 0 {
			line += ", " + strconv.Itoa(s.Count) + " listed"
		}
	case ListStatusUnsupported:
		line = "not supported"
	case ListStatusNoToken:
		line = "no token in this request"
	default:
		line = "ok, " + strconv.Itoa(s.Count) + " listed"
	}
	if s.HasMore {
		line += ", more exist"
	}
	return line
}
