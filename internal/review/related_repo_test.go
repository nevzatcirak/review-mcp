package review

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/related"
)

// Repository items of the tests (v2.5 spec V-13b): a consumer on another
// provider, on a branch and on its default branch, and the primary's own
// repository.
const (
	consumerBranchURL = "https://gitlab.example/grp/web-ui/-/tree/feature/x"
	consumerURL       = "https://gitlab.example/grp/web-ui"
	missingBranchURL  = "https://gitlab.example/grp/web-ui/-/tree/nope"
	slowRepoURL       = "https://gitlab.example/grp/slow"
	ownRepoURL        = "https://your-gitea.example/octo/demo"
)

// consumerCommit is the commit the fake backend pins every branch at.
const consumerCommit = "0fedcba9876543210fedcba9876543210fedcba9"

// repoResolver is relResolver that also resolves the repository URLs of
// the tests (related.ItemResolver).
type repoResolver struct{ relResolver }

func (r repoResolver) ResolveItem(u string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error) {
	switch u {
	case consumerBranchURL, consumerURL, missingBranchURL, slowRepoURL:
		name, branch := "web-ui", ""
		switch u {
		case consumerBranchURL:
			branch = "feature/x"
		case missingBranchURL:
			branch = "nope"
		case slowRepoURL:
			name = "slow"
		}
		return provider.ItemRepository, provider.PRRef{},
			provider.RepoRef{Kind: provider.KindGitLab, Namespace: "grp", Repo: name, Branch: branch, URL: u}, r.rel, nil
	case ownRepoURL:
		return provider.ItemRepository, provider.PRRef{},
			provider.RepoRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", URL: u}, r.primary, nil
	}
	ref, p, err := r.Resolve(u)
	return provider.ItemPullRequest, ref, provider.RepoRef{}, p, err
}

// consumerRepo is the review's fakeRepo with branch fetches and consumer
// searches: it pins every branch at commit (consumerCommit when unset),
// fails the branch "nope" with branch_not_found and the repository "slow"
// with timeout, and counts the fetches. A search of "<name>@<branch>"
// returns hits[that key] and records its query.
type consumerRepo struct {
	fakeRepo
	commit   string
	branches []string
	searches []gitctx.TermQuery
	hits     map[string][]gitctx.Hit
}

func (f *consumerRepo) EnsureBranch(_ context.Context, repo gitctx.Repo, branch string) (gitctx.Checkout, error) {
	f.branches = append(f.branches, repo.Name+"@"+branch)
	switch {
	case branch == "nope":
		return gitctx.Checkout{}, &gitctx.Error{Reason: gitctx.ReasonBranchNotFound}
	case repo.Name == "slow":
		return gitctx.Checkout{}, &gitctx.Error{Reason: gitctx.ReasonTimeout}
	}
	commit := f.commit
	if commit == "" {
		commit = consumerCommit
	}
	return gitctx.Checkout{GitDir: "/cache/" + repo.Name + "@" + branch, HeadSHA: commit, Ref: "refs/review-mcp/branch/x"}, nil
}

func (f *consumerRepo) SearchTerms(_ context.Context, c gitctx.Checkout, q gitctx.TermQuery) (gitctx.TermResult, error) {
	f.searches = append(f.searches, q)
	hits := f.hits[strings.TrimPrefix(c.GitDir, "/cache/")]
	return gitctx.TermResult{Hits: hits, Found: len(hits), Searched: len(q.Terms)}, nil
}

// contractFile is a primary change of an API description: the property
// userName is renamed to displayName.
func contractFile() provider.FilePatch {
	return provider.FilePatch{Path: "api/openapi.yaml", Type: provider.ChangeModified, Additions: 1, Deletions: 1,
		Patch: "@@ -10,4 +10,4 @@ components:\n       properties:\n-        userName:\n+        displayName:\n" +
			"           type: string\n           format: text\n",
		BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap}
}

// consumerHit is a use of userName in the consumer's client.
var consumerHit = gitctx.Hit{Symbol: "userName", Path: "src/client/profile.ts", Line: 42, StartLine: 40,
	Snippet: "const p = await load()\nconst who = p.userName\nrender(who)\nCONSUMER-HIT-MARKER"}

// consumerHarness is relatedHarness with repository items: GitLab configured
// with a token, the backend b, and repository context as enabled says.
func consumerHarness(enabled bool, answers ...string) (*harness, *consumerRepo) {
	h, rel := relatedHarness(answers...)
	h.deps.Resolver = repoResolver{relResolver{primary: h.prov, rel: rel}}
	h.deps.Config.GitLab.BaseURL, h.deps.Config.Secrets.GitLabToken = "https://gitlab.example", config.NewSecret("gitlab-token")
	h.deps.Config.Context.Repo.Enabled = enabled
	b := &consumerRepo{}
	h.deps.RepoContext = b
	return h, b
}

func intp(n int) *int       { return &n }
func strp(s string) *string { return &s }

// TestRelatedRepositoryContextOff: without context.repo.enabled (the case of
// every serve-mode run) a repository item is failed with repo_context_off,
// no git command runs (no branch fetch and no fetch of the primary's head),
// the pull request items are read as before, the note names the item, and
// the result validates against the declared schema.
func TestRelatedRepositoryContextOff(t *testing.T) {
	h, b := consumerHarness(false, goodAnswer)
	res := runRelated(t, h, Args{Related: []string{consumerBranchURL, relatedURL, consumerURL}})
	want := []RelatedItem{
		{URL: consumerBranchURL, Status: "failed", Reason: "repo_context_off", Kind: "repository", Branch: strp("feature/x"),
			Identifiers: intp(0), Matches: intp(0), MatchesShown: intp(0)},
		{URL: relatedURL, Status: "full", HeadSHA: "1234567890abcdef1234567890abcdef12345678", FilesIncluded: 3},
		{URL: consumerURL, Status: "failed", Reason: "repo_context_off", Kind: "repository", Branch: strp(""),
			Identifiers: intp(0), Matches: intp(0), MatchesShown: intp(0)},
	}
	if got, _ := json.Marshal(res.Coverage.Related); string(got) != string(mustJSON(t, want)) {
		t.Errorf("related %s\nwant    %s", got, mustJSON(t, want))
	}
	if len(b.branches) != 0 || b.ensures != 0 {
		t.Errorf("git ran: branches %q, ensures %d", b.branches, b.ensures)
	}
	if n := "Related repository " + consumerBranchURL + " could not be read (repo_context_off) and was not used."; !slices.Contains(res.Notes, n) {
		t.Errorf("notes %q lack %q", res.Notes, n)
	}
	for _, leak := range []string{"web-ui", "feature/x", "gitlab.example/grp"} {
		if strings.Contains(h.logs.String(), leak) {
			t.Errorf("the logs carry %q", leak)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRelatedRepositoryCoverageValidates: every coverage case of a
// repository item validates against the declared schema (the WP-10d rule):
// searched with hits (full) and without (no_use_found), branch_not_found, a
// git reason, the primary's own repository, a duplicate, no contract change
// (nothing fetched), the run's not_placed reasons (review_in_parts,
// nothing_to_review, off: nothing fetched), and pull request items next to
// them unchanged. The branches are fetched after the pull request, when the
// related block is placed.
func TestRelatedRepositoryCoverageValidates(t *testing.T) {
	t.Run("searched, failed and ignored", func(t *testing.T) {
		h, b := consumerHarness(true, goodAnswer)
		h.prov.files = append(h.prov.files, contractFile())
		b.hits = map[string][]gitctx.Hit{"web-ui@feature/x": {consumerHit}}
		res := runRelated(t, h, Args{Related: []string{consumerBranchURL, relatedURL, missingBranchURL, slowRepoURL,
			ownRepoURL}})
		want := []RelatedItem{
			{URL: consumerBranchURL, Status: "full", Kind: "repository", Branch: strp("feature/x"), Commit: consumerCommit,
				Identifiers: intp(1), Matches: intp(1), MatchesShown: intp(1)},
			{URL: relatedURL, Status: "full", HeadSHA: "1234567890abcdef1234567890abcdef12345678", FilesIncluded: 3},
			{URL: missingBranchURL, Status: "failed", Reason: "branch_not_found", Kind: "repository", Branch: strp("nope"),
				Identifiers: intp(0), Matches: intp(0), MatchesShown: intp(0)},
			{URL: slowRepoURL, Status: "failed", Reason: "timeout", Kind: "repository", Branch: strp(""),
				Identifiers: intp(0), Matches: intp(0), MatchesShown: intp(0)},
			{URL: ownRepoURL, Status: "ignored", Reason: "primary_repository", Kind: "repository", Branch: strp(""),
				Identifiers: intp(0), Matches: intp(0), MatchesShown: intp(0)},
		}
		if got := mustJSON(t, res.Coverage.Related); string(got) != string(mustJSON(t, want)) {
			t.Errorf("related %s\nwant    %s", got, mustJSON(t, want))
		}
		if !slices.Equal(b.branches, []string{"web-ui@feature/x", "web-ui@nope", "slow@"}) {
			t.Errorf("branch fetches %q", b.branches)
		}
		for _, n := range []string{
			"Related repository " + missingBranchURL + " could not be read (branch_not_found) and was not used.",
			"Related repository " + ownRepoURL + " was ignored: it is the reviewed pull request's own repository or a duplicate.",
		} {
			if !slices.Contains(res.Notes, n) {
				t.Errorf("notes %q lack %q", res.Notes, n)
			}
		}
		if q := b.searches[0]; len(q.Terms) != 1 || q.Terms[0] != (gitctx.Term{Text: "userName", Word: true}) || q.Filter == nil {
			t.Errorf("search %+v", q)
		}
		for _, leak := range []string{consumerCommit, "feature/x", "userName", "src/client", "CONSUMER-HIT-MARKER"} {
			if strings.Contains(h.logs.String(), leak) {
				t.Errorf("the logs carry %q", leak)
			}
		}
	})
	t.Run("no use found", func(t *testing.T) {
		h, b := consumerHarness(true, goodAnswer)
		h.prov.files = append(h.prov.files, contractFile())
		res := runRelated(t, h, Args{Related: []string{consumerURL}})
		want := []RelatedItem{{URL: consumerURL, Status: "no_use_found", Kind: "repository", Branch: strp(""), Commit: consumerCommit,
			Identifiers: intp(1), Matches: intp(0), MatchesShown: intp(0)}}
		if got := mustJSON(t, res.Coverage.Related); string(got) != string(mustJSON(t, want)) {
			t.Errorf("related %s\nwant    %s", got, mustJSON(t, want))
		}
		note := "No use of the changed identifiers was found in grp/web-ui at " + consumerCommit[:12] + ". Text search does " +
			"not see dynamic or generated uses, so this is not a statement that nothing is affected."
		if !slices.Contains(res.Notes, note) || len(b.searches) != 1 {
			t.Errorf("notes %q, searches %d", res.Notes, len(b.searches))
		}
	})
	t.Run("related.max_identifiers", func(t *testing.T) {
		h, b := consumerHarness(true, goodAnswer)
		removed := contractFile()
		removed.Patch = "@@ -10,3 +10,1 @@ components:\n       properties:\n-        userName:\n-        legacyId:\n"
		h.prov.files = append(h.prov.files, removed)
		h.deps.Config.Related.MaxIdentifiers = 1
		res := runRelated(t, h, Args{Related: []string{consumerURL}})
		if len(b.searches) != 1 || len(b.searches[0].Terms) != 1 || b.searches[0].Terms[0].Text != "userName" {
			t.Errorf("searches %+v", b.searches)
		}
		if n := "1 of the changed identifiers were not searched (limit 1)."; !slices.Contains(res.Notes, n) {
			t.Errorf("notes %q lack %q", res.Notes, n)
		}
	})
	t.Run("no contract change", func(t *testing.T) {
		h, b := consumerHarness(true, goodAnswer)
		res := runRelated(t, h, Args{Related: []string{consumerURL, relatedURL, consumerBranchURL}})
		for _, i := range []int{0, 2} {
			if e := res.Coverage.Related[i]; e.Status != "not_placed" || e.Reason != "no_contract_change" || e.Commit != "" ||
				*e.Identifiers != 0 {
				t.Errorf("entry %d %+v", i, e)
			}
		}
		if e := res.Coverage.Related[1]; e.Status != "full" {
			t.Errorf("pull request entry %+v", e)
		}
		if len(b.branches) != 0 || len(b.searches) != 0 {
			t.Errorf("fetched %q, searched %d", b.branches, len(b.searches))
		}
		n := 0
		for _, note := range res.Notes {
			if note == "The pull request changes no contract file or exported symbol, so the related repositories were not searched." {
				n++
			}
		}
		if n != 1 {
			t.Errorf("the no-contract note comes %d times: %q", n, res.Notes)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		h, b := consumerHarness(true, goodAnswer)
		h.prov.files = append(h.prov.files, contractFile())
		res := runRelated(t, h, Args{Related: []string{consumerURL, consumerURL}})
		if e := res.Coverage.Related[1]; e.Status != "ignored" || e.Reason != "duplicate" || e.Kind != "repository" ||
			e.Branch == nil || *e.Branch != "" || e.Commit != "" {
			t.Errorf("duplicate %+v", e)
		}
		if len(b.branches) != 1 {
			t.Errorf("branch fetches %q", b.branches)
		}
	})
	t.Run("nothing to review", func(t *testing.T) {
		h, b := consumerHarness(true, goodAnswer)
		h.prov.files = nil
		res := runRelated(t, h, Args{Related: []string{consumerURL}})
		if e := res.Coverage.Related[0]; e.Status != "not_placed" || e.Reason != ReasonNothingToReview || e.Commit != "" ||
			len(b.branches) != 0 {
			t.Errorf("entry %+v, fetches %q", e, b.branches)
		}
	})
	t.Run("off", func(t *testing.T) {
		h, b := consumerHarness(true, goodAnswer)
		h.deps.Config.Related.MaxTokens = 0
		res := runRelated(t, h, Args{Related: []string{consumerURL}})
		if e := res.Coverage.Related[0]; e.Status != "not_placed" || e.Reason != "off" || len(b.branches) != 0 {
			t.Errorf("entry %+v, fetches %q", e, b.branches)
		}
	})
	t.Run("in parts", func(t *testing.T) {
		h, _ := chunkHarness(t, nil)
		rel := &relProvider{files: []provider.FilePatch{patchOf("proto/user.proto", "PROTO-MARKER", 6)}}
		h.deps.Resolver = repoResolver{relResolver{primary: h.prov, rel: rel}}
		h.deps.Config.GitLab.BaseURL, h.deps.Config.Secrets.GitLabToken = "https://gitlab.example", config.NewSecret("gitlab-token")
		h.deps.Config.Context.Repo.Enabled = true
		b := &consumerRepo{}
		h.deps.RepoContext = b
		res := runChunked(t, h, Args{Related: []string{relatedURL, consumerBranchURL}})
		validateResult(t, res)
		for i, e := range res.Coverage.Related {
			if e.Status != "not_placed" || e.Reason != "review_in_parts" {
				t.Errorf("entry %d %+v", i, e)
			}
		}
		if e := res.Coverage.Related[1]; e.Kind != "repository" || e.Commit != "" || len(b.branches) != 0 {
			t.Errorf("repository entry %+v, fetches %q", e, b.branches)
		}
	})
}

// consumerHits are n uses of userName in the consumer, each with context.
func consumerHits(n int) []gitctx.Hit {
	out := make([]gitctx.Hit, n)
	for i := range out {
		out[i] = gitctx.Hit{Symbol: "userName", Path: fmt.Sprintf("src/views/v%02d.ts", i), Line: 10, StartLine: 8,
			Snippet: "const a = 1\nconst b = 2\nshow(user.userName)\nconst c = 3\nconst d = 4"}
	}
	return out
}

// TestRelatedRepositoryMixedBlockSweep: a related block with a pull request
// and a repository, across context windows: whenever the block is placed
// the primary diff is whole and both sections are in it, the repository
// section after the pull request's; when it is not, both items are
// not_placed with did_not_fit; the repository's status follows the hits
// shown; every result validates against the declared schema.
func TestRelatedRepositoryMixedBlockSweep(t *testing.T) {
	statuses := map[string]int{}
	for window := 4000; window <= 20000; window += 500 {
		h, b := consumerHarness(true, goodAnswer)
		h.prov.files = append(h.prov.files, contractFile(), patchOf("src/big.go", "BIG-PRIMARY-MARKER", 120))
		b.hits = map[string][]gitctx.Hit{"web-ui@feature/x": consumerHits(20)}
		h.deps.Config.Related.MaxTokens = 2400
		h.deps.Config.LLM.ContextWindow = window
		h.deps.Config.Review.MaxChunks = 1
		res, err := Run(context.Background(), h.deps, Args{PRURL: testPRURL, Related: []string{relatedURL, consumerBranchURL}})
		if err != nil {
			continue // the primary diff alone does not fit this window
		}
		validateResult(t, res)
		user := h.llm.calls[0].user
		pr, repo := res.Coverage.Related[0], res.Coverage.Related[1]
		statuses[repo.Status]++
		if !strings.Contains(user, related.HeaderWithRepositories) {
			if pr.Reason != "did_not_fit" || repo.Reason != "did_not_fit" || *repo.MatchesShown != 0 {
				t.Errorf("window %d: block left out, entries %+v %+v", window, pr, repo)
			}
			continue
		}
		// The primary diff is the one prepared without the related block.
		hb, _ := consumerHarness(true, goodAnswer)
		hb.prov.files = append(hb.prov.files, contractFile(), patchOf("src/big.go", "BIG-PRIMARY-MARKER", 120))
		hb.deps.Config.LLM.ContextWindow, hb.deps.Config.Review.MaxChunks = window, 1
		base := prepareWith(t, hb, Args{}).Prompts.User
		diff := base[strings.Index(base, "The PR code diff:"):]
		if res.Metadata.DiffTrimmed || !strings.Contains(user, diff) {
			t.Fatalf("window %d: the related block was placed and the primary diff trimmed", window)
		}
		at := strings.Index(user, "R2 — repository grp/web-ui, branch feature/x, commit "+consumerCommit[:12])
		if at < 0 || at < strings.Index(user, "Related pull request R1: ") {
			t.Errorf("window %d: the repository section is missing or before the pull request's", window)
		}
		switch shown := *repo.MatchesShown; {
		case shown == 20 && repo.Status != "full", shown < 20 && repo.Status != "partial":
			t.Errorf("window %d: %d of 20 hits shown, status %q", window, shown, repo.Status)
		}
		if n := strings.Count(user[at:], "] uses userName"); n != *repo.MatchesShown {
			t.Errorf("window %d: %d hits in the prompt, matches_shown %d", window, n, *repo.MatchesShown)
		}
	}
	if statuses["full"] == 0 || statuses["not_placed"] == 0 || statuses["partial"] == 0 {
		t.Errorf("the sweep gave %v; want full, partial and not_placed", statuses)
	}
}

// TestRelatedRepositoryInstruction: the related instruction gets the
// repository paragraph only when a repository item is placed; a block of
// pull requests only keeps the 2.4 instruction and header.
func TestRelatedRepositoryInstruction(t *testing.T) {
	h, b := consumerHarness(true, goodAnswer)
	h.prov.files = append(h.prov.files, contractFile())
	b.hits = map[string][]gitctx.Hit{"web-ui@feature/x": {consumerHit}}
	pl := prepareWith(t, h, Args{Related: []string{relatedURL, consumerBranchURL}})
	if !strings.Contains(pl.Prompts.System, RelatedInstruction+"\n\n"+RelatedRepositoryInstruction) {
		t.Error("the system prompt lacks the repository paragraph")
	}
	if !strings.Contains(pl.Prompts.User, related.HeaderWithRepositories) {
		t.Error("the block lacks the header naming repositories")
	}

	h, _ = consumerHarness(true, goodAnswer)
	h.prov.files = append(h.prov.files, contractFile())
	pl = prepareWith(t, h, Args{Related: []string{relatedURL}})
	if strings.Contains(pl.Prompts.System, RelatedRepositoryInstruction) || !strings.Contains(pl.Prompts.System, RelatedInstruction) ||
		strings.Contains(pl.Prompts.User, related.HeaderWithRepositories) || !strings.Contains(pl.Prompts.User, RelatedHeader) {
		t.Error("a block of pull requests only changed")
	}

	// A repository item that is not placed (no contract change) adds nothing.
	h, _ = consumerHarness(true, goodAnswer)
	pl = prepareWith(t, h, Args{Related: []string{relatedURL, consumerBranchURL}})
	if strings.Contains(pl.Prompts.System, RelatedRepositoryInstruction) || strings.Contains(pl.Prompts.User, "R2 — repository") {
		t.Error("an unplaced repository item reached the prompt")
	}
}
