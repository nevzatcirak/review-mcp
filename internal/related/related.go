// Package related reads the related pull requests of a review (X-41, v2.3
// spec V-13a): pull requests the caller names as read-only context of a
// primary pull request, on any configured provider. review-mcp never
// discovers them, never publishes to them, and reads only what the review
// needs: the pull request's title, state, draft flag, target branch, head
// revision and diff (no description, no comments).
//
// From v2.5 (spec V-13b) a related item may also be a repository, or a
// branch of it: it is fetched read-only at one commit, pinned when the run
// reads it, through the repository-context machinery (gitctx.EnsureBranch),
// and only with context.repo.enabled. Nothing in it is built or run (R-1).
//
// Every related URL goes through the run's resolver, the same one as the
// primary's: an URL that matches no configured provider is never sent
// anywhere, and in serve mode a provider without the caller's token is
// refused before any request. A related pull request that cannot be read
// never fails the run, and never means that it does not exist. Logs carry
// an item's index, provider kind, status and reason only: never its URL
// path, title or diff; for a repository item also its item kind and counts,
// never its branch, commit, identifiers, paths or hits.
package related

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Limits of the related argument (spec §1.1).
const (
	// MaxItems is the most related pull requests of one call.
	MaxItems = 4
	// MaxURLBytes is the longest related URL.
	MaxURLBytes = 2048
)

// Status values of a related item (coverage.related[].status).
const (
	// StatusFull: every changed file was shown.
	StatusFull = "full"
	// StatusPartial: some changed files were listed by path only.
	StatusPartial = "partial"
	// StatusNotPlaced: read, but not in the prompt.
	StatusNotPlaced = "not_placed"
	// StatusFailed: not read (Reason says why).
	StatusFailed = "failed"
	// StatusIgnored: the primary pull request itself, or a duplicate.
	StatusIgnored = "ignored"
	// StatusNoUseFound: a repository item was searched and no use of the
	// changed identifiers was found (v2.5 spec V-13b §1.3 item 2). It is
	// declared for the schema; the search that sets it is WP-13b's.
	StatusNoUseFound = "no_use_found"
)

// KindRepository is coverage.related[].kind of a repository item; a pull
// request item has no kind (V-13b §1.3 item 1).
const KindRepository = string(provider.ItemRepository)

// SourceRepoConfig is coverage.related[].source of an item read from the
// repository config (V-13b §3.2); declared for the schema.
const SourceRepoConfig = "repo_config"

// Reason values (coverage.related[].reason).
const (
	ReasonURLNotConfigured   = "url_not_configured"
	ReasonURLMalformed       = "url_malformed"
	ReasonCredentialsMissing = "credentials_missing"
	ReasonNotFound           = "not_found"
	ReasonForbidden          = "forbidden"
	ReasonUnauthorized       = "unauthorized"
	ReasonUnreadable         = "unreadable"
	ReasonDuplicate          = "duplicate"
	ReasonPrimary            = "primary"
	ReasonDidNotFit          = "did_not_fit"
	ReasonReviewInParts      = "review_in_parts"
	// ReasonOff: related.max_tokens is 0, so nothing was read.
	//
	// DESIGN-QUESTION (WP-11a DQ-1): the spec turns related pull requests
	// off with related.max_tokens = 0 ("the argument is accepted, nothing is
	// read, one note") but names no status for the items, and every item
	// must be accounted for in coverage.related. They get not_placed with
	// this reason. Recommendation: keep; an item that is in the argument
	// but was not read or shown is not_placed, and the reason says why.
	ReasonOff = "off"

	// Reasons of repository items (v2.5 spec V-13b §1.3 item 2).

	// ReasonRepoContextOff: context.repo.enabled is not set (always so in
	// serve mode), so the repository was not fetched; no git command ran.
	ReasonRepoContextOff = "repo_context_off"
	// ReasonBranchNotFound: the repository has no such branch.
	ReasonBranchNotFound = gitctx.ReasonBranchNotFound
	// ReasonPrimaryRepository: the item names the reviewed pull request's own
	// repository, whose contents are already repository context.
	ReasonPrimaryRepository = "primary_repository"
	// ReasonNoContractChange: the primary diff gave no identifier, so the
	// repository was not searched. Declared for the schema; WP-13b derives
	// the identifiers.
	ReasonNoContractChange = "no_contract_change"
)

// GitReasons are the failed reasons a repository item takes from gitctx
// (besides branch_not_found and not_found, and auth, which is reported as
// unauthorized): the fetch's fixed reasons, passed on as they are.
var GitReasons = []string{gitctx.ReasonTimeout, gitctx.ReasonTooLarge, gitctx.ReasonRedirect, gitctx.ReasonGitFailed,
	gitctx.ReasonGitUnavailable, gitctx.ReasonBusy, gitctx.ReasonCache, gitctx.ReasonUnsupported}

// Fixed argument errors (spec §1.1 item 2). They never repeat a value.
const (
	errTooMany     = "related must have at most 4 items"
	errCombination = "related cannot be combined with focus or findings"
)

// ArgumentError is an invalid related argument; its text is fixed.
type ArgumentError struct{ msg string }

func (e *ArgumentError) Error() string { return e.msg }

// Normalize returns the URL as the run uses it: an absolute http(s) URL
// with a host and no credentials, at most MaxURLBytes long, without its
// query and fragment, as net/url writes it. ok is false for anything else.
func Normalize(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxURLBytes {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return "", false
	}
	u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
	return u.String(), true
}

// Validate checks the related argument before any network call, in the
// spec's order: at most MaxItems items, each a URL Normalize accepts (the
// error names its 0-based index), and no focus or findings with it
// (combined is true when the call has either). It returns the normalised
// URLs, each with the Bitbucket Server branch query kept (keepAt); an empty
// list is not given and returns nil. The error is an *ArgumentError.
func Validate(items []string, combined bool) ([]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > MaxItems {
		return nil, &ArgumentError{errTooMany}
	}
	out := make([]string, len(items))
	for i, raw := range items {
		u, ok := Normalize(raw)
		if !ok {
			return nil, &ArgumentError{fmt.Sprintf("related[%d] must be an http or https pull request or repository URL without credentials", i)}
		}
		out[i] = keepAt(raw, u)
	}
	if combined {
		return nil, &ArgumentError{errCombination}
	}
	return out, nil
}

// atKey is the one query key a related URL keeps: a Bitbucket Server
// repository URL names its branch with it ("…/browse?at=refs/heads/x",
// v2.5 spec V-13b §1.1 item 4).
const atKey = "at"

// keepAt returns the normalised URL norm of raw with raw's "at" query
// values put back (only that key, in their order), when raw's path has the
// shape of a Bitbucket Server repository URL: it ends in
// /repos/{slug} or /repos/{slug}/browse. Any other URL is norm unchanged, so
// a pull request URL stays exactly as V-13a normalised it. Whether the
// value names a branch is the provider's ParseRepoPath's decision.
//
// DESIGN-QUESTION (WP-13a DQ-2): the related argument is normalised before
// the URL is resolved, so the provider is not known yet. The query is kept
// by the path's shape rather than for every URL, so that no other URL's
// normalised form (the URL coverage.related reports) changes from 2.4.
// Recommendation: keep.
func keepAt(raw, norm string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return norm
	}
	vals, ok := u.Query()[atKey]
	if !ok || !bitbucketRepoPath(u.Path) {
		return norm
	}
	// "/" is legal in a query value; it is kept readable.
	return norm + "?" + strings.ReplaceAll(url.Values{atKey: vals}.Encode(), "%2F", "/")
}

// bitbucketRepoPath reports whether p ends in /repos/{slug} or
// /repos/{slug}/browse.
func bitbucketRepoPath(p string) bool {
	segs := strings.Split(strings.TrimSuffix(p, "/"), "/")
	n := len(segs)
	if n >= 1 && segs[n-1] == "browse" {
		n--
	}
	return n >= 3 && segs[n-2] == "repos" && segs[n-1] != ""
}

// Resolver maps a pull request URL to a reference and a provider: the run's
// resolver (provider.Resolver, wrapped in serve mode by the credential
// check). It performs no network I/O.
type Resolver interface {
	Resolve(rawURL string) (provider.PRRef, provider.Provider, error)
}

// ItemResolver is a Resolver that also resolves repository URLs
// (provider.Resolver.ResolveItem, kept by the tools' wrappers). Read uses it
// for repository items; a resolver without it resolves pull request URLs
// only.
type ItemResolver interface {
	ResolveItem(rawURL string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error)
}

// BranchFetcher fetches a branch of a repository at the commit the fetch
// brings (gitctx.Runner.EnsureBranch).
type BranchFetcher interface {
	EnsureBranch(ctx context.Context, repo gitctx.Repo, branch string) (gitctx.Checkout, error)
}

// Repositories is what Read needs for repository items (v2.5 spec V-13b).
// A nil *Repositories resolves every URL as a pull request URL, as in
// V-13a (pr_info).
type Repositories struct {
	// Enabled is context.repo.enabled (never set in serve mode). Without it
	// every repository item is failed with repo_context_off and no git
	// command runs.
	Enabled bool
	// Config is the run's configuration, for the provider base URL, token
	// and TLS settings of each repository (gitctx.RepoFor).
	Config *config.Config
	// Fetcher fetches the branches. Nil with Enabled set fails the items
	// with unsupported (a test backend without branches).
	Fetcher BranchFetcher
}

// RepositoriesFor returns the Repositories of a run with configuration cfg
// and repository-context backend (pipeline Deps.RepoContext): nil selects
// the real git runner, as repoctx.Open does; a backend that cannot fetch a
// branch leaves Fetcher nil. Nothing is fetched here.
func RepositoriesFor(cfg *config.Config, backend any) *Repositories {
	r := &Repositories{Enabled: cfg.Context.Repo.Enabled, Config: cfg}
	if !r.Enabled {
		return r
	}
	switch b := backend.(type) {
	case nil:
		r.Fetcher = gitctx.New(gitctx.OptionsFromConfig(cfg.Context.Repo))
	case BranchFetcher:
		r.Fetcher = b
	}
	return r
}

// Item is one related item of a run, in argument order: a pull request or,
// with Kind ItemRepository, a repository.
type Item struct {
	// URL is the normalised URL the caller gave.
	URL string
	// Status and Reason are the item's outcome so far: StatusFailed and
	// StatusIgnored are final; a read item has Read set and gets its status
	// when the prompt is built (full, partial or not_placed); a fetched
	// repository item has Fetched set.
	Status, Reason string
	// Read is set when the pull request and its diff were read.
	Read     bool
	Ref      provider.PRRef
	Provider provider.Provider
	PR       *provider.PullRequest
	Diff     *provider.Diff

	// Kind is provider.ItemRepository for a repository item, "" for a pull
	// request item or an item that was not resolved (off).
	Kind provider.ItemKind
	// Repo is the repository item's reference.
	Repo provider.RepoRef
	// Fetched is set when the repository was fetched; Checkout holds it at
	// its pinned commit (Checkout.HeadSHA) for the whole run.
	Fetched  bool
	Checkout gitctx.Checkout
	// Identifiers, Matches and MatchesShown are the repository item's
	// search counts (V-13b §1.3 item 1).
	Identifiers, Matches, MatchesShown int
	// Source is SourceRepoConfig for an item from the repository config.
	Source string
}

// IsRepository reports whether it is a repository item.
func (it *Item) IsRepository() bool { return it.Kind == provider.ItemRepository }

// Read resolves and reads the related items urls (normalised, in argument
// order) of the primary pull request primary (its normalised URL and its
// resolved reference). A URL equal to the primary's, or resolving to the
// same pull request, is ignored with ReasonPrimary; a repeat of an earlier
// item (by URL or by the pull request it resolves to) with
// ReasonDuplicate. Each other item is resolved through r, then read with
// GetPullRequest and GetDiff (include filters the files, as for the
// primary; nil keeps them all), sequentially, under ctx. A failure is the
// item's status and reason, never the run's error. With off set nothing is
// resolved or read: every item that is not ignored is not_placed with
// ReasonOff.
//
// With repos set and r an ItemResolver, an item may also be a repository
// (v2.5 spec V-13b): one naming the primary's own repository is ignored with
// ReasonPrimaryRepository, a repeat of the same repository and branch with
// ReasonDuplicate. The others are fetched after the pull request items,
// sequentially in argument order, each at the commit its branch points to
// now (fetchRepositories); without repos.Enabled none is, and each is
// failed with ReasonRepoContextOff.
//
// DESIGN-QUESTION (WP-11a DQ-2): the spec ignores the primary URL and
// duplicates "after normalisation". Two different URLs can name the same
// pull request (".../pulls/7" and ".../pulls/7/files"), so an item is also
// ignored when it resolves to the primary's reference or to an earlier
// item's, before anything is read. Recommendation: keep; the same pull
// request is never read twice nor shown as its own context.
func Read(ctx context.Context, r Resolver, primaryURL string, primary provider.PRRef, urls []string,
	include func(string) bool, off bool, repos *Repositories, log *slog.Logger) []*Item {
	return read(ctx, r, primaryURL, primary, urls, include, off, true, repos, log)
}

// ReadPullRequests is Read without the diffs (pr_info, spec V-13a §3.2):
// each related pull request is resolved and read with GetPullRequest only;
// related.max_tokens does not apply, since nothing goes into a prompt.
// Repository URLs are resolved as pull request URLs only (url_malformed),
// as in V-13a, until pr_info lists repository items (V-13b §4).
//
// DESIGN-QUESTION (WP-11c DQ-2): related.max_tokens 0 turns related pull
// requests off for the prompts of pr_review and pr_ask. pr_info has no
// prompt and reads no diff, so it reads them whatever the budget.
// Recommendation: keep.
func ReadPullRequests(ctx context.Context, r Resolver, primaryURL string, primary provider.PRRef, urls []string,
	log *slog.Logger) []*Item {
	return read(ctx, r, primaryURL, primary, urls, nil, false, false, nil, log)
}

func read(ctx context.Context, r Resolver, primaryURL string, primary provider.PRRef, urls []string,
	include func(string) bool, off, withDiff bool, repos *Repositories, log *slog.Logger) []*Item {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	resolve := func(u string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error) {
		ref, p, err := r.Resolve(u)
		return provider.ItemPullRequest, ref, provider.RepoRef{}, p, err
	}
	if ir, ok := r.(ItemResolver); ok && repos != nil {
		resolve = ir.ResolveItem
	}
	items := make([]*Item, 0, len(urls))
	firstByURL := map[string]*Item{}
	var seenRefs []provider.PRRef
	var seenRepos []provider.RepoRef
	var pending []int // the repository items to fetch, by index
	for i, u := range urls {
		it := &Item{URL: u}
		items = append(items, it)
		switch first := firstByURL[u]; {
		case u == primaryURL:
			it.Status, it.Reason = StatusIgnored, ReasonPrimary
		case first != nil:
			it.Status, it.Reason = StatusIgnored, ReasonDuplicate
			it.Kind, it.Repo = first.Kind, first.Repo
		case off:
			it.Status, it.Reason = StatusNotPlaced, ReasonOff
		}
		if firstByURL[u] == nil {
			firstByURL[u] = it
		}
		if it.Status != "" {
			log.Debug("related pull request", "index", i, "status", it.Status, "reason", it.Reason)
			continue
		}
		kind, ref, repo, p, err := resolve(u)
		if err != nil {
			it.Status, it.Reason = StatusFailed, reasonOf(err)
			log.Debug("related pull request", "index", i, "status", it.Status, "reason", it.Reason)
			continue
		}
		if kind == provider.ItemRepository {
			it.Kind, it.Repo, it.Provider = kind, repo, p
			switch {
			case sameRepo(repo, primary):
				it.Status, it.Reason = StatusIgnored, ReasonPrimaryRepository
			case containsRepo(seenRepos, repo):
				it.Status, it.Reason = StatusIgnored, ReasonDuplicate
			default:
				seenRepos = append(seenRepos, repo)
				pending = append(pending, i)
				continue
			}
			logRepository(log, i, it)
			continue
		}
		switch {
		case sameRef(ref, primary):
			it.Status, it.Reason = StatusIgnored, ReasonPrimary
		case containsRef(seenRefs, ref):
			it.Status, it.Reason = StatusIgnored, ReasonDuplicate
		}
		if it.Status != "" {
			log.Debug("related pull request", "index", i, "kind", string(ref.Kind), "status", it.Status, "reason", it.Reason)
			continue
		}
		seenRefs = append(seenRefs, ref)
		it.Ref, it.Provider = ref, p
		pr, err := p.GetPullRequest(ctx, ref)
		d := &provider.Diff{}
		if err == nil && withDiff {
			d, err = p.GetDiff(ctx, ref, pr, provider.DiffOptions{Include: include})
		}
		if err != nil {
			it.Status, it.Reason = StatusFailed, reasonOf(err)
			log.Debug("related pull request", "index", i, "kind", string(ref.Kind), "status", it.Status, "reason", it.Reason)
			continue
		}
		it.Read, it.PR, it.Diff = true, pr, d
		log.Debug("related pull request", "index", i, "kind", string(ref.Kind), "status", "read",
			"files", len(d.Files), "skipped", len(d.Skipped))
	}
	for _, i := range pending {
		fetchRepository(ctx, items[i], repos, log, i)
	}
	return items
}

// fetchRepository fetches the repository item it (index i) at the commit
// its branch, or its default branch, points to now (V-13b §1.2): only with
// repos.Enabled (otherwise repo_context_off, and no git command runs), with
// the configured provider's base URL and token, under ctx. A failure is the
// item's status and gitctx's fixed reason (auth reported as unauthorized).
//
// For WP-13a the fetched item gets no status here: the identifiers of the
// primary diff are not derived yet (WP-13b, spec §2.1), so nothing is
// searched and Identifiers, Matches and MatchesShown stay 0. Mark may still
// set the run's not_placed reasons (review_in_parts, nothing_to_review), and
// Coverage reports an item without one as not_placed without a reason.
func fetchRepository(ctx context.Context, it *Item, repos *Repositories, log *slog.Logger, i int) {
	switch {
	case !repos.Enabled:
		it.Status, it.Reason = StatusFailed, ReasonRepoContextOff
	case repos.Fetcher == nil:
		it.Status, it.Reason = StatusFailed, gitctx.ReasonUnsupported
	}
	if it.Status != "" {
		logRepository(log, i, it)
		return
	}
	repo, ok := gitctx.RepoFor(repos.Config,
		provider.PRRef{Kind: it.Repo.Kind, Namespace: it.Repo.Namespace, Repo: it.Repo.Repo}, it.Provider)
	if !ok {
		it.Status, it.Reason = StatusFailed, gitctx.ReasonUnsupported
		logRepository(log, i, it)
		return
	}
	co, err := repos.Fetcher.EnsureBranch(ctx, repo, it.Repo.Branch)
	if err != nil {
		it.Status, it.Reason = StatusFailed, gitReason(err)
		logRepository(log, i, it)
		return
	}
	it.Fetched, it.Checkout = true, co
	logRepository(log, i, it)
}

// logRepository logs a repository item's outcome: its index, item kind,
// provider kind, status (fetched when it has none yet), reason and counts;
// never its URL, branch, commit or anything found in it.
func logRepository(log *slog.Logger, i int, it *Item) {
	status := it.Status
	if status == "" && it.Fetched {
		status = "fetched"
	}
	log.Debug("related repository", "index", i, "item", KindRepository, "kind", string(it.Repo.Kind),
		"status", status, "reason", it.Reason, "identifiers", it.Identifiers, "matches", it.Matches,
		"matches_shown", it.MatchesShown)
}

// gitReason is the failed reason of a repository fetch: gitctx's fixed
// reason, with auth (git cannot tell 401 from 403 apart once both schemes
// were refused) reported as unauthorized.
func gitReason(err error) string {
	if r := gitctx.ReasonOf(err); r != gitctx.ReasonAuth {
		return r
	}
	return ReasonUnauthorized
}

// sameRepo reports whether repo names the primary pull request's own
// repository (any branch).
func sameRepo(repo provider.RepoRef, primary provider.PRRef) bool {
	return repo.Kind == primary.Kind && strings.EqualFold(repo.Namespace, primary.Namespace) &&
		strings.EqualFold(repo.Repo, primary.Repo) && repo.Kind != ""
}

// containsRepo reports whether repos holds the same repository and branch
// as repo (branch names are case-sensitive; "" is the default branch).
func containsRepo(repos []provider.RepoRef, repo provider.RepoRef) bool {
	for _, r := range repos {
		if r.Kind == repo.Kind && strings.EqualFold(r.Namespace, repo.Namespace) && strings.EqualFold(r.Repo, repo.Repo) &&
			r.Branch == repo.Branch {
			return true
		}
	}
	return false
}

// ReadItems are the items of items that were read, in order.
func ReadItems(items []*Item) []*Item {
	var out []*Item
	for _, it := range items {
		if it.Read {
			out = append(out, it)
		}
	}
	return out
}

func sameRef(a, b provider.PRRef) bool {
	return a.Kind == b.Kind && strings.EqualFold(a.Namespace, b.Namespace) && strings.EqualFold(a.Repo, b.Repo) &&
		a.Number == b.Number && a.Kind != ""
}

func containsRef(refs []provider.PRRef, ref provider.PRRef) bool {
	for _, r := range refs {
		if sameRef(r, ref) {
			return true
		}
	}
	return false
}

// classed is an error that names its X-6 class (tools.RequestError: the
// serve-mode credential check).
type classed interface{ ErrorClass() string }

// reasonOf maps a resolution or read error to a fixed reason (spec §1.2
// item 4): the URL classes, a missing serve-mode credential, not found, an
// authentication answer (403 forbidden, otherwise unauthorized), and
// unreadable for everything else.
func reasonOf(err error) string {
	var c classed
	if errors.As(err, &c) && c.ErrorClass() == ReasonCredentialsMissing {
		return ReasonCredentialsMissing
	}
	var pe *provider.Error
	if !errors.As(err, &pe) {
		return ReasonUnreadable
	}
	switch pe.Class {
	case provider.ClassURLNotConfigured:
		return ReasonURLNotConfigured
	case provider.ClassURLMalformed:
		return ReasonURLMalformed
	case provider.ClassNotFound:
		return ReasonNotFound
	case provider.ClassAuth:
		if pe.Status == 403 {
			return ReasonForbidden
		}
		return ReasonUnauthorized
	}
	return ReasonUnreadable
}

// Notes (spec §1.5 item 2, verbatim); url is the normalised URL, which the
// user gave.

// NoteFailed: the item could not be read.
func NoteFailed(url, reason string) string {
	return "Related pull request " + url + " could not be read (" + reason + ") and was not used."
}

// NoteIgnored: the item is the primary pull request or a duplicate.
func NoteIgnored(url string) string {
	return "Related pull request " + url + " was ignored: it is the reviewed pull request itself or a duplicate."
}

// NoteRepositoryFailed: the repository item could not be fetched.
//
// DESIGN-QUESTION (WP-13a DQ-4): the spec gives notes for searches
// (§2.4) but none for a repository item that is failed or ignored; the
// pull request notes would call it a pull request. These two follow their
// wording with "repository". Recommendation: keep, and list them in the
// troubleshooting docs (WP-13d).
func NoteRepositoryFailed(url, reason string) string {
	return "Related repository " + url + " could not be read (" + reason + ") and was not used."
}

// NoteRepositoryIgnored: the repository item is the reviewed pull request's
// own repository or a duplicate.
func NoteRepositoryIgnored(url string) string {
	return "Related repository " + url + " was ignored: it is the reviewed pull request's own repository or a duplicate."
}

// NotePartial: n of the item's files were listed by path only.
func NotePartial(url string, n int) string {
	return fmt.Sprintf("Related pull request %s did not fit in full; %d of its files are listed by path only.", url, n)
}

// Notes of the whole set.
const (
	NoteNotPlaced = "The related pull requests did not fit the context window next to the diff and were not used."
	NoteInParts   = "The pull request was reviewed in parts; related pull requests need the whole change at once and were not used."
	NoteOff       = "related.max_tokens is 0, so related pull requests were not read."
)
