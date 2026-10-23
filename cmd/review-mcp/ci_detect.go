package main

import (
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
)

// Names of the pipelines CI mode recognises, as the "no pull request"
// message shows them.
const (
	ciNameGitea  = "Gitea Actions"
	ciNameGitHub = "GitHub Actions"
	ciNameGitLab = "GitLab CI"
	ciNameNone   = "none"
)

// ciMaxEventBytes bounds the GitHub/Gitea event file that is read.
const ciMaxEventBytes = 1 << 20

// detectPullRequest finds the pull request of the pipeline (V-4 §1.2) and
// returns its URL ("" when there is none) and the name of the detected CI
// (ciNameNone when none). It reads only the variables the spec names, and
// from the event file only pull_request.number. Nothing is guessed from a
// branch name or a commit message. The URL is not trusted: it goes through
// the resolver and its pinned hosts like any --pr-url.
func detectPullRequest(env ciEnv) (url, ci string) {
	switch {
	case env("GITEA_ACTIONS") == "true":
		// Gitea Actions also sets GITHUB_ACTIONS; its pull requests are
		// under /pulls/.
		return actionsPullRequest(env, "pulls"), ciNameGitea
	case env("GITHUB_ACTIONS") == "true":
		return actionsPullRequest(env, "pull"), ciNameGitHub
	case env("GITLAB_CI") == "true":
		return gitLabMergeRequest(env), ciNameGitLab
	}
	return "", ciNameNone
}

// actionsPullRequest is {GITHUB_SERVER_URL}/{GITHUB_REPOSITORY}/{segment}/{n}
// for a pull_request or pull_request_target event, with n read from the
// event file; "" otherwise.
func actionsPullRequest(env ciEnv, segment string) string {
	switch env("GITHUB_EVENT_NAME") {
	case "pull_request", "pull_request_target":
	default:
		return ""
	}
	server := strings.TrimRight(env("GITHUB_SERVER_URL"), "/")
	repo := strings.Trim(env("GITHUB_REPOSITORY"), "/")
	path := env("GITHUB_EVENT_PATH")
	if server == "" || repo == "" || path == "" {
		return ""
	}
	n, ok := eventPullRequestNumber(path)
	if !ok {
		return ""
	}
	return server + "/" + repo + "/" + segment + "/" + strconv.FormatInt(n, 10)
}

// eventPullRequestNumber reads pull_request.number from the event file at
// path, which may be at most ciMaxEventBytes long. Every other field is
// ignored.
func eventPullRequestNumber(path string) (int64, bool) {
	f, err := os.Open(path) //nolint:gosec // G304: the path is the runner's own GITHUB_EVENT_PATH
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, ciMaxEventBytes+1))
	if err != nil || len(data) > ciMaxEventBytes {
		return 0, false
	}
	var ev struct {
		PullRequest struct {
			Number int64 `json:"number"`
		} `json:"pull_request"`
	}
	if err := json.Unmarshal(data, &ev); err != nil || ev.PullRequest.Number <= 0 {
		return 0, false
	}
	return ev.PullRequest.Number, true
}

// gitLabMergeRequest is
// {CI_SERVER_URL}/{CI_MERGE_REQUEST_PROJECT_PATH}/-/merge_requests/{CI_MERGE_REQUEST_IID}
// in a merge request pipeline; "" otherwise.
func gitLabMergeRequest(env ciEnv) string {
	iid := env("CI_MERGE_REQUEST_IID")
	n, err := strconv.ParseInt(iid, 10, 64)
	if err != nil || n <= 0 {
		return ""
	}
	server := strings.TrimRight(env("CI_SERVER_URL"), "/")
	project := strings.Trim(env("CI_MERGE_REQUEST_PROJECT_PATH"), "/")
	if server == "" || project == "" {
		return ""
	}
	return server + "/" + project + "/-/merge_requests/" + strconv.FormatInt(n, 10)
}
