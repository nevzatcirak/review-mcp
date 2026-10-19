package review

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
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

// consumerRepo is the review's fakeRepo with branch fetches: it pins every
// branch at consumerCommit, fails the branch "nope" with branch_not_found
// and the repository "slow" with timeout, and counts the fetches.
type consumerRepo struct {
	fakeRepo
	branches []string
}

func (f *consumerRepo) EnsureBranch(_ context.Context, repo gitctx.Repo, branch string) (gitctx.Checkout, error) {
	f.branches = append(f.branches, repo.Name+"@"+branch)
	switch {
	case branch == "nope":
		return gitctx.Checkout{}, &gitctx.Error{Reason: gitctx.ReasonBranchNotFound}
	case repo.Name == "slow":
		return gitctx.Checkout{}, &gitctx.Error{Reason: gitctx.ReasonTimeout}
	}
	return gitctx.Checkout{GitDir: "/cache/git", HeadSHA: consumerCommit, Ref: "refs/review-mcp/branch/x"}, nil
}

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
// fetched (not_placed, its commit, zero counts), branch_not_found, a git
// reason, the primary's own repository, a duplicate, the run's not_placed
// reasons (review_in_parts, nothing_to_review, off), and pull request items
// next to them unchanged. The branches are fetched after the pull request.
func TestRelatedRepositoryCoverageValidates(t *testing.T) {
	t.Run("fetched, failed and ignored", func(t *testing.T) {
		h, b := consumerHarness(true, goodAnswer)
		res := runRelated(t, h, Args{Related: []string{consumerBranchURL, relatedURL, missingBranchURL, slowRepoURL,
			ownRepoURL}})
		want := []RelatedItem{
			{URL: consumerBranchURL, Status: "not_placed", Kind: "repository", Branch: strp("feature/x"), Commit: consumerCommit,
				Identifiers: intp(0), Matches: intp(0), MatchesShown: intp(0)},
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
		if strings.Contains(h.logs.String(), consumerCommit) || strings.Contains(h.logs.String(), "feature/x") {
			t.Error("the logs carry the commit or the branch")
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		h, b := consumerHarness(true, goodAnswer)
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
		h, _ := consumerHarness(true, goodAnswer)
		h.prov.files = nil
		res := runRelated(t, h, Args{Related: []string{consumerURL}})
		if e := res.Coverage.Related[0]; e.Status != "not_placed" || e.Reason != ReasonNothingToReview || e.Commit != consumerCommit {
			t.Errorf("entry %+v", e)
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
		h.deps.RepoContext = &consumerRepo{}
		res := runChunked(t, h, Args{Related: []string{relatedURL, consumerBranchURL}})
		validateResult(t, res)
		for i, e := range res.Coverage.Related {
			if e.Status != "not_placed" || e.Reason != "review_in_parts" {
				t.Errorf("entry %d %+v", i, e)
			}
		}
		if e := res.Coverage.Related[1]; e.Kind != "repository" || e.Commit != consumerCommit {
			t.Errorf("repository entry %+v", e)
		}
	})
}
