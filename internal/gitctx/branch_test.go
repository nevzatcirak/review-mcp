package gitctx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// hashedRef is the local ref the spec names for a branch (v2.5 spec V-13b
// §1.2 item 1), computed here independently of branchRef.
func hashedRef(branch string) string {
	sum := sha256.Sum256([]byte(branch))
	return "refs/review-mcp/branch/" + hex.EncodeToString(sum[:])[:16]
}

// TestEnsureBranchRefNames [canary]: the fetch maps refs/heads/{branch}
// (the remote's HEAD for the default branch) to the hashed local ref, never
// to a ref built from the branch name, and the checkout reports that ref
// and the fetched commit. The fetch keeps Ensure's size flags.
func TestEnsureBranchRefNames(t *testing.T) {
	for _, tc := range []struct{ branch, remote, local string }{
		{"", "HEAD", "refs/review-mcp/branch/default"},
		{"main", "refs/heads/main", hashedRef("main")},
		{"feature/x", "refs/heads/feature/x", hashedRef("feature/x")},
		{"default", "refs/heads/default", hashedRef("default")},
	} {
		t.Run(tc.branch, func(t *testing.T) {
			resetSchemes()
			r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
			co, err := r.EnsureBranch(context.Background(), giteaRepo("https://your-gitea.example"), tc.branch)
			if err != nil {
				t.Fatalf("EnsureBranch: %v", err)
			}
			if co.Ref != tc.local || co.HeadSHA != fakeSHA {
				t.Errorf("checkout = %+v, want ref %s at %s", co, tc.local, fakeSHA)
			}
			calls := f.calls(t)
			fetch := " " + strings.Join(callsOf(calls, "fetch")[0].Args, " ") + " "
			for _, want := range []string{
				" --depth=1 ", " --filter=blob:limit=1m ", " --no-tags ", " --no-recurse-submodules ",
				" origin +" + tc.remote + ":" + tc.local + " ",
			} {
				if !strings.Contains(fetch, want) {
					t.Errorf("fetch lacks %q: %s", strings.TrimSpace(want), fetch)
				}
			}
			revParse := strings.Join(callsOf(calls, "rev-parse")[0].Args, " ")
			if !strings.Contains(revParse, tc.local+"^{commit}") {
				t.Errorf("rev-parse args = %s", revParse)
			}
			for _, c := range calls {
				for _, a := range c.Args {
					if tc.branch != "" && strings.Contains(a, "refs/review-mcp/branch/"+tc.branch) {
						t.Errorf("git %s: a local ref is built from the branch name: %s", c.subcommand(), a)
					}
				}
			}
		})
	}
}

// TestEnsureBranchSharesTheEntry: a branch is fetched into the same cache
// entry (one bare repository) as the repository's pull requests, with the
// same pinned remote URL.
func TestEnsureBranchSharesTheEntry(t *testing.T) {
	resetSchemes()
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
	pr, err := r.Ensure(context.Background(), gitlabRepo("https://gitlab.example/gitlab"), PR{Number: 7, HeadSHA: fakeSHA})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	br, err := r.EnsureBranch(context.Background(), gitlabRepo("https://gitlab.example/gitlab"), "feature/x")
	if err != nil {
		t.Fatalf("EnsureBranch: %v", err)
	}
	if pr.GitDir != br.GitDir || !strings.HasSuffix(filepath.ToSlash(br.GitDir), "/gitlab.example/gitlab/group+sub/project/"+gitDirName) {
		t.Errorf("git dirs %s and %s", pr.GitDir, br.GitDir)
	}
	for _, c := range callsOf(f.calls(t), "config") {
		if c.Args[len(c.Args)-2] == "remote.origin.url" && c.Args[len(c.Args)-1] != "https://gitlab.example/gitlab/group/sub/project.git" {
			t.Errorf("remote.origin.url = %s", c.Args[len(c.Args)-1])
		}
	}
}

// TestEnsureBranchTokenNeverInArgv [canary]: as for Ensure, the token
// reaches git only through the environment of the fetch, for every
// provider's header form and for a named and the default branch.
func TestEnsureBranchTokenNeverInArgv(t *testing.T) {
	cases := []struct {
		name   string
		repo   Repo
		header string
	}{
		{"gitea", giteaRepo("https://your-gitea.example"), "Authorization: token " + testToken},
		{"bitbucket", bbsRepo("https://bitbucket.example.com"), "Authorization: Bearer " + testToken},
		{"github", githubRepo("https://github.example.com"), "Authorization: " + basicOf("x-access-token", testToken)},
		{"gitlab", gitlabRepo("https://gitlab.example.com"), "Authorization: " + basicOf("oauth2", testToken)},
	}
	for _, tc := range cases {
		for _, branch := range []string{"", "feature/x"} {
			t.Run(tc.name+"/"+branch, func(t *testing.T) {
				resetSchemes()
				r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
				if _, err := r.EnsureBranch(context.Background(), tc.repo, branch); err != nil {
					t.Fatalf("EnsureBranch: %v", err)
				}
				assertTokenOnlyInFetchEnv(t, f.calls(t), tc.header)
			})
		}
	}
}

// TestEnsureBranchRedirectGuard [canary]: the ls-remote --get-url guard
// stops a branch fetch as it stops a pull request fetch: redirect, no
// fetch, and the credential reaches no git command.
func TestEnsureBranchRedirectGuard(t *testing.T) {
	resetSchemes()
	setExtraConfig(t, [][2]string{{"url.https://elsewhere.example/.insteadOf", "https://your-gitea.example/"}})
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
	_, err := r.EnsureBranch(context.Background(), giteaRepo("https://your-gitea.example"), "main")
	wantReason(t, err, ReasonRedirect)
	calls := f.calls(t)
	if n := len(callsOf(calls, "fetch")); n != 0 {
		t.Errorf("%d fetches ran after the guard", n)
	}
	if n := len(callsOf(calls, "ls-remote")); n != 1 {
		t.Errorf("%d ls-remote calls, want 1", n)
	}
	assertNoCredential(t, calls)
}

// TestEnsureBranchMissingRemoteRef: git's "couldn't find remote ref" is
// branch_not_found for a branch (stderr dropped), while the same answer to
// a pull request fetch stays not_found.
func TestEnsureBranchMissingRemoteRef(t *testing.T) {
	resetSchemes()
	const leak = "SECRET-STDERR-ZQ7X"
	beh := fakeBehavior{FetchStderr: "fatal: couldn't find remote ref refs/heads/nope " + leak + "\n", FetchExit: 128}
	r, _ := fakeRunner(t, beh, Options{})
	_, err := r.EnsureBranch(context.Background(), giteaRepo("https://your-gitea.example"), "nope")
	wantReason(t, err, ReasonBranchNotFound)
	if strings.Contains(err.Error(), leak) || strings.Contains(Note(ReasonOf(err)), leak) {
		t.Errorf("stderr leaked: %q", err.Error())
	}
	_, err = r.Ensure(context.Background(), giteaRepo("https://your-gitea.example"), PR{Number: 7, HeadSHA: fakeSHA})
	wantReason(t, err, ReasonNotFound)
	// A repository that is not there is not_found for a branch too.
	r, _ = fakeRunner(t, fakeBehavior{FetchStderr: "fatal: repository 'x' not found\n", FetchExit: 128}, Options{})
	_, err = r.EnsureBranch(context.Background(), giteaRepo("https://your-gitea.example"), "")
	wantReason(t, err, ReasonNotFound)
}

// TestEnsureBranchRefusesBadNames: a branch that is not a valid branch name
// is unsupported before git runs.
func TestEnsureBranchRefusesBadNames(t *testing.T) {
	r, f := fakeRunner(t, fakeBehavior{SHA: fakeSHA}, Options{})
	for _, b := range []string{"a..b", "-x", "HEAD", "a b", "a:b", "--upload-pack=x", "x.lock", "/x"} {
		_, err := r.EnsureBranch(context.Background(), giteaRepo("https://your-gitea.example"), b)
		wantReason(t, err, ReasonUnsupported)
	}
	if _, err := r.EnsureBranch(context.Background(), giteaRepo("https://user:pw@your-gitea.example"), "main"); ReasonOf(err) != ReasonUnsupported {
		t.Errorf("credentials in base: %v", err)
	}
	if n := len(f.calls(t)); n != 0 {
		t.Errorf("git ran %d times for refused input", n)
	}
}

// TestEnsureBranchRealGit fetches, from the fixture git server, the default
// branch (the remote's HEAD), a named branch with "/" and a missing one
// (branch_not_found), into the same bare repository as pull request 7.
func TestEnsureBranchRealGit(t *testing.T) {
	resetSchemes()
	s := newGitServer(t, provider.KindGitea)
	s.set(acceptExactly("token "+testToken), "")
	feature := s.addBranch(t, "feature/x", map[string]string{"client.go": "package main\n\nvar field = \"userName\"\n"})
	r, _ := realRunner(t, Options{})
	gitPath, _ := realGit(t)

	def, err := r.EnsureBranch(context.Background(), giteaRepo(s.base), "")
	if err != nil {
		t.Fatalf("default branch: %v", err)
	}
	if def.HeadSHA != s.head || def.Ref != "refs/review-mcp/branch/default" {
		t.Errorf("default branch checkout = %+v, want %s", def, s.head)
	}
	named, err := r.EnsureBranch(context.Background(), giteaRepo(s.base), "feature/x")
	if err != nil {
		t.Fatalf("named branch: %v", err)
	}
	if named.HeadSHA != feature || named.Ref != hashedRef("feature/x") || named.GitDir != def.GitDir {
		t.Errorf("named branch checkout = %+v, want %s in %s", named, feature, def.GitDir)
	}
	if got := gitOut(t, "", gitPath, "--git-dir="+named.GitDir, "cat-file", "-p", named.HeadSHA+":client.go"); !strings.Contains(got, "userName") {
		t.Errorf("client.go at the branch = %q", got)
	}
	if got := strings.TrimSpace(gitOut(t, "", gitPath, "--git-dir="+named.GitDir, "rev-parse", named.Ref)); got != feature {
		t.Errorf("the local ref holds %s", got)
	}
	pr, err := r.Ensure(context.Background(), giteaRepo(s.base), PR{Number: 7, HeadSHA: s.head})
	if err != nil || pr.GitDir != named.GitDir {
		t.Errorf("pull request 7 after the branches: %+v %v", pr, err)
	}

	_, err = r.EnsureBranch(context.Background(), giteaRepo(s.base), "nope")
	wantReason(t, err, ReasonBranchNotFound)
	if got := schemesOf(s.requests()); len(got) != 1 || got[0] != "token" {
		t.Errorf("auth schemes = %v, want [token]", got)
	}
}

// TestEnsureBranchTokenNeverOnDisk [canary]: after branch fetches with
// every header scheme, no file in the cache holds the token in any form
// (asserted as for Ensure).
func TestEnsureBranchTokenNeverOnDisk(t *testing.T) {
	resetSchemes()
	g := newGitServer(t, provider.KindGitea)
	g.set(acceptExactly("token "+testToken), "")
	b := newGitServer(t, provider.KindBitbucketServer)
	b.set(acceptExactly(basicOf(testUser, testToken)), "")
	h := newGitServer(t, provider.KindGitHub)
	h.set(acceptExactly(basicOf("x-access-token", testToken)), "")
	l := newGitServer(t, provider.KindGitLab)
	l.set(acceptExactly(basicOf("oauth2", testToken)), "")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	r, _ := realRunner(t, Options{CacheDir: cacheDir})

	if _, err := r.EnsureBranch(context.Background(), githubRepo(h.base), "main"); err != nil {
		t.Fatalf("github EnsureBranch: %v", err)
	}
	if _, err := r.EnsureBranch(context.Background(), giteaRepo(g.base), ""); err != nil {
		t.Fatalf("gitea EnsureBranch: %v", err)
	}
	if _, err := r.EnsureBranch(context.Background(), gitlabRepo(l.base), "main"); err != nil {
		t.Fatalf("gitlab EnsureBranch: %v", err)
	}
	calls := 0
	repo := bbsRepo(b.base)
	repo.Identity = identityOf(testUser, &calls)
	if _, err := r.EnsureBranch(context.Background(), repo, ""); err != nil {
		t.Fatalf("bitbucket EnsureBranch: %v", err)
	}
	assertNoTokenOnDisk(t, cacheDir, 4)
}

// TestEnsureBranchInsteadOfGuardRealGit [canary]: with the real git, an
// insteadOf that would send a branch fetch elsewhere is stopped by the
// guard: redirect, and no request reaches either server.
func TestEnsureBranchInsteadOfGuardRealGit(t *testing.T) {
	resetSchemes()
	s := newGitServer(t, provider.KindGitea)
	s.set(acceptExactly("token "+testToken), "")
	other := newGitServer(t, provider.KindGitea)
	setExtraConfig(t, [][2]string{{"url." + other.base + "/.insteadOf", s.base + "/"}})
	r, _ := realRunner(t, Options{})
	_, err := r.EnsureBranch(context.Background(), giteaRepo(s.base), "main")
	wantReason(t, err, ReasonRedirect)
	if n := len(s.requests()) + len(other.requests()); n != 0 {
		t.Errorf("%d requests reached a server", n)
	}
}
