package review

import (
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/related"
	"github.com/nevzatcirak/review-mcp/internal/repocfg"
)

// Related repositories declared in the repository config (v2.5 spec V-13b
// §3.2): read only without a related argument and with the operator key
// related.from_repo_config, from the config at the base revision.

// unconfiguredURL is a repository URL on a host no provider is configured
// for.
const unconfiguredURL = "https://unknown.example/team/web-ui"

// declResolver is repoResolver that answers unconfiguredURL as the real
// resolver does (url_not_configured, no request).
type declResolver struct{ repoResolver }

func (r declResolver) ResolveItem(u string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error) {
	if strings.HasPrefix(u, "https://unknown.example/") {
		return "", provider.PRRef{}, provider.RepoRef{}, nil, &provider.Error{Class: provider.ClassURLNotConfigured}
	}
	return r.repoResolver.ResolveItem(u)
}

// declaredHarness is consumerHarness with the base revision's repository
// config holding config, the operator switch as on says, a primary diff
// that renames userName, and a consumer hit.
func declaredHarness(on bool, config string) (*harness, *consumerRepo) {
	h, b := consumerHarness(true, goodAnswer)
	h.deps.Resolver = declResolver{h.deps.Resolver.(repoResolver)}
	h.deps.Config.Related.FromRepoConfig = on
	h.prov.files = append(h.prov.files, contractFile())
	b.hits = map[string][]gitctx.Hit{"web-ui@feature/x": {consumerHit}}
	withRepoConfig(h, config)
	return h, b
}

// declaredConsumer declares the consumer branch, confined to src/client/.
const declaredConsumer = "[[related.repos]]\nurl = \"" + consumerBranchURL + "\"\npaths = [\"src/client/\"]\n"

// TestDeclaredRelationsOff [canary 2]: with related.from_repo_config false
// (the default) the declared repositories are not read (no resolution, no
// fetch, no coverage entry): related.repos is listed in repo_config.ignored
// with the fixed note; the result validates.
func TestDeclaredRelationsOff(t *testing.T) {
	h, b := declaredHarness(false, declaredConsumer)
	res := runRelated(t, h, Args{})
	if len(res.Coverage.Related) != 0 || len(b.branches) != 0 || len(b.searches) != 0 {
		t.Fatalf("related %+v, fetches %q, searches %d", res.Coverage.Related, b.branches, len(b.searches))
	}
	if rc := res.RepoConfig; !slices.Equal(rc.Ignored, []string{"related.repos"}) || len(rc.Applied) != 0 {
		t.Fatalf("repo_config %+v", rc)
	}
	if !slices.Contains(res.Notes, repocfg.NoteRelatedOff) {
		t.Fatalf("notes %q", res.Notes)
	}
}

// TestDeclaredRelationsOn: with related.from_repo_config and no related
// argument, the declared repository is read like an argument's, on its
// declared branch, searched below its declared paths, with source
// repo_config in coverage.related; the key is applied; the result
// validates (the overview's mark is TestDeclaredRepositoryRendering's).
func TestDeclaredRelationsOn(t *testing.T) {
	h, b := declaredHarness(true, declaredConsumer)
	res := runRelated(t, h, Args{})
	want := []RelatedItem{{URL: consumerBranchURL, Status: "full", Kind: "repository", Branch: strp("feature/x"),
		Commit: consumerCommit, Identifiers: intp(1), Matches: intp(1), MatchesShown: intp(1), Source: "repo_config"}}
	if got := mustJSON(t, res.Coverage.Related); string(got) != string(mustJSON(t, want)) {
		t.Fatalf("related %s\nwant    %s", got, mustJSON(t, want))
	}
	if !slices.Equal(b.branches, []string{"web-ui@feature/x"}) || len(b.searches) != 1 ||
		!slices.Equal(b.searches[0].Paths, []string{"src/client/"}) {
		t.Fatalf("fetches %q, searches %+v", b.branches, b.searches)
	}
	if rc := res.RepoConfig; !slices.Equal(rc.Applied, []string{"related.repos"}) || len(rc.Ignored) != 0 {
		t.Fatalf("repo_config %+v", rc)
	}
	if !strings.Contains(h.llm.calls[0].user, "R1 — repository grp/web-ui, branch feature/x") {
		t.Error("the declared repository is not in the prompt")
	}
}

// TestDeclaredRelationsCallWins: a call's own related argument wins over the
// declarations, which are not resolved or read; the argument is always
// allowed, also with the operator switch off.
func TestDeclaredRelationsCallWins(t *testing.T) {
	for _, on := range []bool{true, false} {
		h, b := declaredHarness(on, declaredConsumer)
		res := runRelated(t, h, Args{Related: []string{consumerURL}})
		if len(res.Coverage.Related) != 1 || res.Coverage.Related[0].URL != consumerURL || res.Coverage.Related[0].Source != "" ||
			!slices.Equal(b.branches, []string{"web-ui@"}) {
			t.Errorf("on %v: related %+v, fetches %q", on, res.Coverage.Related, b.branches)
		}
	}
}

// TestDeclaredRelationsUnconfiguredHost: a declared URL that matches no
// configured provider is failed with url_not_configured, and nothing is
// requested or fetched for it; a declaration naming a pull request is
// url_malformed; the result validates.
func TestDeclaredRelationsUnconfiguredHost(t *testing.T) {
	h, b := declaredHarness(true, "[[related.repos]]\nurl = \""+unconfiguredURL+"\"\n\n[[related.repos]]\nurl = \""+relatedURL+"\"\n")
	res := runRelated(t, h, Args{})
	if len(res.Coverage.Related) != 2 {
		t.Fatalf("related %+v", res.Coverage.Related)
	}
	for i, reason := range []string{"url_not_configured", "url_malformed"} {
		if e := res.Coverage.Related[i]; e.Status != "failed" || e.Reason != reason || e.Kind != "repository" || e.Source != "repo_config" ||
			e.Commit != "" {
			t.Errorf("entry %d %+v", i, e)
		}
	}
	if len(b.branches) != 0 || len(b.searches) != 0 {
		t.Fatalf("fetches %q, searches %d", b.branches, len(b.searches))
	}
	if n := related.NoteRepositoryFailed(unconfiguredURL, "url_not_configured"); !slices.Contains(res.Notes, n) {
		t.Errorf("notes %q lack %q", res.Notes, n)
	}
}

// TestDeclaredRelationsBaseRevision: the declarations are read from the
// repository config at the base revision only: a pull request that adds
// [[related.repos]] to its own config (head revision) adds no relation.
func TestDeclaredRelationsBaseRevision(t *testing.T) {
	h, b := declaredHarness(true, "[review]\nrequire_tests = false\n")
	head := h.prov.pr.HeadSHA
	if h.prov.revs[head] == nil {
		h.prov.revs[head] = map[string]string{}
	}
	h.prov.revs[head][repoConfigPath] = declaredConsumer
	res := runRelated(t, h, Args{})
	if len(res.Coverage.Related) != 0 || len(b.branches) != 0 {
		t.Fatalf("related %+v, fetches %q", res.Coverage.Related, b.branches)
	}
	if !slices.Equal(h.prov.configReads, []string{repoConfigPath + "@base0"}) {
		t.Fatalf("config reads %q", h.prov.configReads)
	}
}
