package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// Declared related repositories in pr_info (v2.5 spec V-13b §3.2 item 4).

const declaredConfig = "[[related.repos]]\nurl = \"https://gitlab.example/grp/web-ui\"\nbranch = \"main\"\n\n" +
	"[[related.repos]]\nurl = \"https://unknown.example/team/web-ui\"\n"

// declInfoProvider is infoProvider whose repository config, at the base
// revision only, declares related repositories; it records the files read.
type declInfoProvider struct {
	*infoProvider
	reads []string
}

func (f *declInfoProvider) GetFileAt(_ context.Context, _ provider.PRRef, path, rev string) ([]byte, error) {
	f.reads = append(f.reads, path+"@"+rev)
	if path == ".review-mcp.toml" && rev == f.pr.BaseSHA {
		return []byte(declaredConfig), nil
	}
	return nil, provider.ErrNotFound
}

// declInfoResolver resolves gitlab.example repository URLs (no request) and
// nothing else; a related pull request URL is the primary.
type declInfoResolver struct{ p *declInfoProvider }

func (r declInfoResolver) Resolve(u string) (provider.PRRef, provider.Provider, error) {
	return provider.PRRef{Kind: provider.KindGitea, Namespace: "octo", Repo: "demo", Number: 7, URL: u}, r.p, nil
}

func (r declInfoResolver) ResolveItem(u string) (provider.ItemKind, provider.PRRef, provider.RepoRef, provider.Provider, error) {
	if strings.HasPrefix(u, "https://gitlab.example/") {
		return provider.ItemRepository, provider.PRRef{}, provider.RepoRef{Kind: provider.KindGitLab, Namespace: "grp",
			Repo: "web-ui", URL: u}, r.p, nil
	}
	return "", provider.PRRef{}, provider.RepoRef{}, nil, &provider.Error{Class: provider.ClassURLNotConfigured}
}

// TestPRInfoDeclaredRelations: with related.from_repo_config pr_info reads
// the repository config at the base revision and lists the declared
// repositories without reading them (not_read, or failed with the reason a
// review would give), with kind, branch and source; with the switch off, or
// with a related argument (which wins), it reads no file and lists none, as
// in 2.4.
func TestPRInfoDeclaredRelations(t *testing.T) {
	run := func(on bool, rel []string) (PRInfoResult, *declInfoProvider) {
		t.Helper()
		f := &declInfoProvider{infoProvider: newInfoProvider()}
		cfg := config.Defaults()
		cfg.Related.FromRepoConfig = on
		res, err := PRInfoFor(context.Background(), cfg, declInfoResolver{f}, testPRURL, rel, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res, f
	}
	res, f := run(true, nil)
	if len(f.reads) != 1 || f.reads[0] != ".review-mcp.toml@basesha" {
		t.Fatalf("reads %q", f.reads)
	}
	got, _ := json.Marshal(res.Related)
	want := `[{"url":"https://gitlab.example/grp/web-ui","status":"not_read","kind":"repository","branch":"main","source":"repo_config"},` +
		`{"url":"https://unknown.example/team/web-ui","status":"failed","reason":"url_not_configured","kind":"repository","branch":"","source":"repo_config"}]`
	if string(got) != want {
		t.Fatalf("related %s\nwant    %s", got, want)
	}
	md := RenderPRInfoMarkdown(res)
	for _, s := range []string{"Related pull requests and repositories:", "`not_read`, repository declared in the repository config",
		"`failed (url_not_configured)`, repository declared in the repository config"} {
		if !strings.Contains(md, s) {
			t.Errorf("the markdown lacks %q:\n%s", s, md)
		}
	}
	for name, c := range map[string]struct {
		on  bool
		rel []string
	}{"off": {false, nil}, "related argument": {true, []string{"https://your-gitea.example/octo/demo/pulls/8"}}} {
		res, f := run(c.on, c.rel)
		if len(f.reads) != 0 {
			t.Errorf("%s: reads %q", name, f.reads)
		}
		for _, it := range res.Related {
			if it.Source != "" || it.Kind != "" {
				t.Errorf("%s: a declared relation is listed: %+v", name, it)
			}
		}
	}
}
