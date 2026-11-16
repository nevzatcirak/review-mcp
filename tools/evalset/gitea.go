package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// gitea is the small part of the Gitea API (v1) that setup uses. The token
// goes only into the Authorization header; no error carries it, the URL or
// a response body.
type gitea struct {
	base   string // the instance URL without a trailing slash
	token  string
	client *http.Client
}

func newGitea(base, token string) *gitea {
	return &gitea{base: base, token: token, client: &http.Client{Timeout: 60 * time.Second}}
}

// errStatus is a response status outside 2xx.
type errStatus struct{ code int }

func (e *errStatus) Error() string { return fmt.Sprintf("the Gitea API answered %d", e.code) }

func isStatus(err error, code int) bool {
	var es *errStatus
	return errors.As(err, &es) && es.code == code
}

// do sends one API request and decodes a 2xx JSON answer into out (when not
// nil).
func (g *gitea) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+"/api/v1"+path, body)
	if err != nil {
		return errors.New("cannot build a Gitea API request")
	}
	req.Header.Set("Authorization", "token "+g.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return errors.New("the Gitea API cannot be reached")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return &errStatus{resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return errors.New("the Gitea API answered with unreadable JSON")
	}
	return nil
}

// user returns the login of the token's user.
func (g *gitea) user(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := g.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", errors.New("the Gitea API did not name the token's user")
	}
	return u.Login, nil
}

func repoPath(owner, name string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

type repoState int

const (
	repoCreated repoState = iota
	repoReused
)

// ensureRepo creates the repository, or reuses it when it exists and its
// main is absent or is the base commit of the case; otherwise it refuses.
// A repository under the token's own user is created through /user/repos,
// one under an organization through /orgs/{owner}/repos.
//
// §2.1 compares the base tree. The Gitea API (1.22) reports a commit's own
// id as its tree's, so setup compares the commit id instead: the commits
// are reproducible, and the same commit id pins the same tree.
func (g *gitea) ensureRepo(ctx context.Context, owner, name, login string, private bool, baseCommit string) (repoState, error) {
	err := g.do(ctx, http.MethodGet, repoPath(owner, name), nil, nil)
	switch {
	case err == nil:
		id, found, err := g.branchCommit(ctx, owner, name, baseBranch)
		if err != nil {
			return 0, err
		}
		if found && id != baseCommit {
			return 0, fmt.Errorf("the repository %s exists and its %s is not the base commit of the case; remove it or choose another -prefix", name, baseBranch)
		}
		return repoReused, nil
	case !isStatus(err, http.StatusNotFound):
		return 0, err
	}
	in := map[string]any{"name": name, "private": private, "auto_init": false, "default_branch": baseBranch}
	path := "/user/repos"
	if owner != login {
		path = "/orgs/" + url.PathEscape(owner) + "/repos"
	}
	if err := g.do(ctx, http.MethodPost, path, in, nil); err != nil {
		return 0, fmt.Errorf("cannot create the repository %s: %w", name, err)
	}
	return repoCreated, nil
}

// branchCommit returns the commit id of a branch and whether the branch
// exists.
func (g *gitea) branchCommit(ctx context.Context, owner, name, branch string) (string, bool, error) {
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	err := g.do(ctx, http.MethodGet, repoPath(owner, name)+"/branches/"+url.PathEscape(branch), nil, &b)
	if isStatus(err, http.StatusNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return b.Commit.ID, true, nil
}

// pull is the part of a Gitea pull request setup reads.
type pull struct {
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// waitBranch waits until the API shows the branch at the commit sha: Gitea
// updates its branch list after a push asynchronously, and a pull request
// from a branch it does not list yet is refused with 404.
func (g *gitea) waitBranch(ctx context.Context, owner, name, branch, sha string) error {
	for i := 0; ; i++ {
		id, _, err := g.branchCommit(ctx, owner, name, branch)
		if err != nil {
			return err
		}
		if id == sha {
			return nil
		}
		if i == branchPolls {
			return fmt.Errorf("the Gitea API does not show the pushed %s branch", branch)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(branchPollWait):
		}
	}
}

// branchPolls and branchPollWait bound waitBranch to about 30 seconds.
const (
	branchPolls    = 60
	branchPollWait = 500 * time.Millisecond
)

// ensurePR returns the URL of the open pull request change → main, opening
// it with the case's title and description when there is none. An open one
// whose head is not the case's head commit is refused.
func (g *gitea) ensurePR(ctx context.Context, owner, name, title, body, head string) (string, bool, error) {
	var open []pull
	if err := g.do(ctx, http.MethodGet, repoPath(owner, name)+"/pulls?state=open&limit=50", nil, &open); err != nil {
		return "", false, err
	}
	for _, p := range open {
		if p.Head.Ref == headBranch && p.Base.Ref == baseBranch {
			if p.Head.SHA != head {
				return "", false, errors.New("an open pull request from change to main has another head commit")
			}
			return p.HTMLURL, false, nil
		}
	}
	var p pull
	in := map[string]any{"head": headBranch, "base": baseBranch, "title": title, "body": body}
	if err := g.do(ctx, http.MethodPost, repoPath(owner, name)+"/pulls", in, &p); err != nil {
		return "", false, fmt.Errorf("cannot open the pull request: %w", err)
	}
	if p.HTMLURL == "" {
		return "", false, errors.New("the Gitea API did not return the pull request URL")
	}
	return p.HTMLURL, true, nil
}
