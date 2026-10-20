package ask

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/gitctx"
	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/repocfg"
	"github.com/nevzatcirak/review-mcp/internal/repotools"
)

// Repository tools in related repositories in pr_ask (v2.5 spec V-13b
// §3.1): the related block is placed before the tools are planned, so the
// loop knows the labels and the checkouts.

const (
	askConsumerURL    = "https://gitlab.example/grp/web-ui"
	askConsumerCommit = "0fedcba9876543210fedcba9876543210fedcba9"
)

// askRepoResolver is relResolver that also resolves askConsumerURL as a
// repository (related.ItemResolver).
type askRepoResolver struct{ relResolver }

func (r askRepoResolver) ResolveItem(u string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error) {
	if u == askConsumerURL {
		return provider.ItemRepository, provider.PRRef{},
			provider.RepoRef{Kind: provider.KindGitLab, Namespace: "grp", Repo: "web-ui", URL: u}, r.rel, nil
	}
	ref, p, err := r.Resolve(u)
	return provider.ItemPullRequest, ref, provider.RepoRef{}, p, err
}

// askConsumer is toolRepo with a consumer: its branch fetch pins
// askConsumerCommit, its search finds one use, and a read at that commit is
// answered from consumer.
type askConsumer struct {
	*toolRepo
	consumer      map[string]string
	consumerReads []string
}

func (a *askConsumer) EnsureBranch(context.Context, gitctx.Repo, string) (gitctx.Checkout, error) {
	return gitctx.Checkout{GitDir: "/cache/web-ui", HeadSHA: askConsumerCommit}, nil
}

func (a *askConsumer) SearchTerms(context.Context, gitctx.Checkout, gitctx.TermQuery) (gitctx.TermResult, error) {
	h := gitctx.Hit{Symbol: "userName", Path: "src/client/profile.ts", Line: 2, StartLine: 2, Snippet: "p.userName"}
	return gitctx.TermResult{Hits: []gitctx.Hit{h}, Found: 1, Searched: 1}, nil
}

func (a *askConsumer) FileAt(ctx context.Context, c gitctx.Checkout, rev, path string, n int) ([]byte, error) {
	if c.HeadSHA != askConsumerCommit {
		return a.toolRepo.FileAt(ctx, c, rev, path, n)
	}
	a.consumerReads = append(a.consumerReads, rev+":"+path)
	s, ok := a.consumer[path]
	if !ok {
		return nil, &provider.Error{Class: provider.ClassNotFound}
	}
	return []byte(s), nil
}

// TestAskToolsReadRelatedRepository: pr_ask's tools take repo with the label
// of the placed repository item (R2; R1 is a related pull request) and read
// its checkout at the pinned commit; R1 is invalid_arguments; the
// instruction names the repository.
func TestAskToolsReadRelatedRepository(t *testing.T) {
	h, tr, tl := toolHarness(3,
		callTools(
			toolCall("c1", repotools.ReadFile, `{"path":"src/client/profile.ts","repo":"R2"}`),
			toolCall("c2", repotools.ReadFile, `{"path":"src/client/profile.ts","repo":"R1"}`)),
		answerWith("The client reads userName."))
	h.prov.files = append(h.prov.files, provider.FilePatch{Path: "api/openapi.yaml", Type: provider.ChangeModified,
		Patch:      "@@ -10,3 +10,3 @@ components:\n       properties:\n-        userName:\n+        displayName:\n           type: string\n",
		BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap})
	h.deps.Resolver = askRepoResolver{relResolver{primary: h.prov, rel: &relProvider{files: []provider.FilePatch{
		addedPatch("proto/user.proto", "PROTO-MARKER", 4)}}}}
	h.deps.Config.GitLab.BaseURL, h.deps.Config.Secrets.GitLabToken = "https://gitlab.example", config.NewSecret("gitlab-token")
	ac := &askConsumer{toolRepo: tr, consumer: map[string]string{"src/client/profile.ts": "const who = p.userName\n"}}
	h.deps.RepoContext = ac
	args := h.args()
	args.Related = []string{relatedURL, askConsumerURL}
	res, err := Run(context.Background(), h.deps, args)
	if err != nil {
		t.Fatal(err)
	}
	if e := res.Coverage.Related[1]; e.Kind != "repository" || e.Commit != askConsumerCommit || e.Status != "full" {
		t.Fatalf("the repository item %+v", e)
	}
	if len(tl.convs) == 0 || !strings.Contains(tl.convs[0].System, "R2 for grp/web-ui") {
		t.Fatal("the instruction does not name the repository")
	}
	if got, want := mustJSONText(t, tl.convs[0].Tools), mustJSONText(t, repotools.DefinitionsFor([]repotools.Repo{{Label: "R2"}})); got != want {
		t.Errorf("tools %s", got)
	}
	if !slices.Equal(ac.consumerReads, []string{askConsumerCommit + ":src/client/profile.ts"}) || len(tr.reads) != 0 {
		t.Errorf("consumer reads %q, head reads %q", ac.consumerReads, tr.reads)
	}
	u := toolsOf(res)
	if len(u.Calls) != 2 || u.Calls[0].Status != repotools.StatusOK || u.Calls[0].Repo != "R2" ||
		u.Calls[1].Status != repotools.StatusInvalidArguments || u.Calls[1].Repo != "R1" {
		t.Fatalf("tool use %+v", u)
	}
}

func mustJSONText(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAskDeclaredRelations: pr_ask reads the related repositories the
// repository config declares at the base revision only with
// related.from_repo_config and without a related argument (source
// repo_config); with the switch off the key is ignored with the note and
// nothing is fetched.
func TestAskDeclaredRelations(t *testing.T) {
	for _, on := range []bool{false, true} {
		h, tr, _ := toolHarness(0)
		h.deps.LLM = h.llm
		h.prov.files = append(h.prov.files, provider.FilePatch{Path: "api/openapi.yaml", Type: provider.ChangeModified,
			Patch:      "@@ -10,3 +10,3 @@ components:\n       properties:\n-        userName:\n+        displayName:\n           type: string\n",
			BaseStatus: provider.ContentNotFetchedSizeCap, HeadStatus: provider.ContentNotFetchedSizeCap})
		h.prov.repoFiles = map[string]string{".review-mcp.toml@" + h.prov.pr.BaseSHA: "[[related.repos]]\nurl = \"" + askConsumerURL + "\"\n"}
		h.deps.Resolver = askRepoResolver{relResolver{primary: h.prov, rel: &relProvider{}}}
		h.deps.Config.GitLab.BaseURL, h.deps.Config.Secrets.GitLabToken = "https://gitlab.example", config.NewSecret("gitlab-token")
		h.deps.Config.Related.FromRepoConfig = on
		h.deps.RepoContext = &askConsumer{toolRepo: tr}
		res, err := Run(context.Background(), h.deps, h.args())
		if err != nil {
			t.Fatal(err)
		}
		if !on {
			if len(res.Coverage.Related) != 0 || !slices.Equal(res.RepoConfig.Ignored, []string{"related.repos"}) ||
				!slices.Contains(res.Notes, repocfg.NoteRelatedOff) {
				t.Errorf("off: related %+v, repo_config %+v, notes %q", res.Coverage.Related, res.RepoConfig, res.Notes)
			}
			continue
		}
		if len(res.Coverage.Related) != 1 || res.Coverage.Related[0].Source != "repo_config" ||
			res.Coverage.Related[0].Commit != askConsumerCommit {
			t.Errorf("on: related %+v", res.Coverage.Related)
		}
	}
}

// TestAskDeclaredRelationsOffNote: with the operator switch off, the note
// is given only when the call passes no related argument (WP-13f); with
// the call's related, related.repos is still in repo_config.ignored and no
// note is given.
func TestAskDeclaredRelationsOffNote(t *testing.T) {
	for _, withRelated := range []bool{false, true} {
		h, tr, _ := toolHarness(0)
		h.deps.LLM = h.llm
		h.prov.repoFiles = map[string]string{".review-mcp.toml@" + h.prov.pr.BaseSHA: "[[related.repos]]\nurl = \"" + askConsumerURL + "\"\n"}
		h.deps.Resolver = askRepoResolver{relResolver{primary: h.prov, rel: &relProvider{}}}
		h.deps.Config.GitLab.BaseURL, h.deps.Config.Secrets.GitLabToken = "https://gitlab.example", config.NewSecret("gitlab-token")
		h.deps.RepoContext = &askConsumer{toolRepo: tr}
		args := h.args()
		if withRelated {
			args.Related = []string{relatedURL}
		}
		res, err := Run(context.Background(), h.deps, args)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(res.RepoConfig.Ignored, []string{"related.repos"}) {
			t.Errorf("related %v: repo_config %+v", withRelated, res.RepoConfig)
		}
		if got := slices.Contains(res.Notes, repocfg.NoteRelatedOff); got == withRelated {
			t.Errorf("related %v: the note given %v: %q", withRelated, got, res.Notes)
		}
	}
}
