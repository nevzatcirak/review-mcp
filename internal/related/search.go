package related

import (
	"context"
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/filter"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// The consumer search of related repositories (v2.5 spec V-13b §2.1a and
// §2.2): the changed contract surface of the primary diff is derived first;
// only when it names something is each readable repository item fetched at
// the commit its branch points to now and searched there for those names.

// Caps of the search of one repository (spec §0 item 4, §2.2 item 3).
const (
	// MaxHitsPerIdentifier is the most hits kept per identifier, the first
	// by path, then line.
	MaxHitsPerIdentifier = 5
	// MaxHitsPerRepository is the most hits kept per repository.
	MaxHitsPerRepository = 30
	// HitContextLines is the number of lines kept before and after a hit.
	HitContextLines = 2
)

// Pending are the repository items of items that Search would fetch and
// search: readable, with no status yet, in argument order.
func Pending(items []*Item) []*Item {
	var out []*Item
	for _, it := range items {
		if it.IsRepository() && it.repos != nil && it.Status == "" && !it.Searched {
			out = append(out, it)
		}
	}
	return out
}

// SearchFiles derives the changed contract surface of files (the files the
// run reviews; Derive with related.max_identifiers limit and the run's
// filter, which names the contract files) and searches the pending
// repository items of items for it (Search). With no pending item nothing
// is derived. With possible false (the prompt has no room for the related
// block: the primary diff is already trimmed) a surface that names
// something is not searched either, and Place reports the pending items as
// did_not_fit; an empty surface is still no_contract_change.
func SearchFiles(ctx context.Context, items []*Item, files []provider.FilePatch, flt *filter.Filter, limit int,
	possible bool, log *slog.Logger) {
	if len(Pending(items)) == 0 {
		return
	}
	ids, dropped := Derive(files, flt, limit)
	if len(ids) > 0 && !possible {
		return
	}
	Search(ctx, items, ids, dropped, limit, log)
}

// Search fetches and searches the pending repository items of items for the
// identifiers ids (Surface; dropped is how many related.max_identifiers,
// limit, left out). The surface comes first (spec §2.1a): when ids is empty
// nothing is fetched, and each pending item is not_placed with
// ReasonNoContractChange, without a commit. Otherwise each item is fetched,
// sequentially in argument order, under ctx, with the configured provider's
// base URL and token, at the commit its branch (or its default branch)
// points to now (V-13b §1.2), and searched at that commit: fixed strings,
// word-bounded except routes, offline, with the operator's filter, below
// its Paths when it has them, MaxHitsPerIdentifier, MaxHitsPerRepository
// and HitContextLines. A failed fetch or search is the item's status and
// gitctx's fixed reason (auth reported as unauthorized), never the run's
// error. A searched item keeps no status: the prompt gives it one (full,
// partial, no_use_found or not_placed).
func Search(ctx context.Context, items []*Item, ids []Identifier, dropped, limit int, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	terms := make([]gitctx.Term, len(ids))
	for i, id := range ids {
		terms[i] = gitctx.Term{Text: id.Text, Word: id.Word()}
	}
	for i, it := range items {
		if !it.IsRepository() || it.repos == nil || it.Status != "" || it.Searched {
			continue
		}
		if len(ids) == 0 {
			it.Status, it.Reason = StatusNotPlaced, ReasonNoContractChange
			logRepository(log, i, it)
			continue
		}
		if !fetch(ctx, it) {
			logRepository(log, i, it)
			continue
		}
		res, err := it.repos.Searcher.SearchTerms(ctx, it.Checkout, gitctx.TermQuery{Terms: terms, Filter: it.repos.Filter,
			Paths: it.Paths, MaxPerTerm: MaxHitsPerIdentifier, MaxTotal: MaxHitsPerRepository, ContextLines: HitContextLines})
		if err != nil {
			it.Status, it.Reason = StatusFailed, gitReason(err)
			logRepository(log, i, it)
			continue
		}
		it.Searched, it.Surface, it.Hits = true, ids, res.Hits
		it.Identifiers, it.Matches = res.Searched, max(res.Found, len(res.Hits))
		it.Cut = it.Matches - len(res.Hits)
		it.unsearched, it.limit = dropped, limit
		logRepository(log, i, it)
	}
}

// fetch fetches the repository item it at its pinned commit; a failure is
// its status.
func fetch(ctx context.Context, it *Item) bool {
	repos := it.repos
	repo, ok := gitctx.RepoFor(repos.Config,
		provider.PRRef{Kind: it.Repo.Kind, Namespace: it.Repo.Namespace, Repo: it.Repo.Repo}, it.Provider)
	if !ok {
		it.Status, it.Reason = StatusFailed, gitctx.ReasonUnsupported
		return false
	}
	co, err := repos.Fetcher.EnsureBranch(ctx, repo, it.Repo.Branch)
	if err != nil {
		it.Status, it.Reason = StatusFailed, gitReason(err)
		return false
	}
	it.Fetched, it.Checkout = true, co
	return true
}
