package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// ciEventMarker is in the event file fixtures, outside pull_request.number.
// It must never reach the output.
const ciEventMarker = "FAKE-EVENT-BODY-QX42"

func eventFile(t *testing.T, number string) string {
	t.Helper()
	return writeFile(t, "event.json", `{"action":"opened","pull_request":{"number":`+number+
		`,"title":"`+ciEventMarker+`","head":{"ref":"feature"}},"repository":{"full_name":"x/y"}}`)
}

func actionsVars(t *testing.T, event string) map[string]string {
	t.Helper()
	return map[string]string{
		"GITHUB_ACTIONS":    "true",
		"GITHUB_SERVER_URL": "https://github.example.com/",
		"GITHUB_REPOSITORY": "octo/demo",
		"GITHUB_EVENT_NAME": event,
		"GITHUB_EVENT_PATH": eventFile(t, "7"),
	}
}

func with(m map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func gitLabVars() map[string]string {
	return map[string]string{
		"GITLAB_CI":                     "true",
		"CI_SERVER_URL":                 "https://gitlab.example.com",
		"CI_MERGE_REQUEST_PROJECT_PATH": "group/sub/demo",
		"CI_MERGE_REQUEST_IID":          "12",
	}
}

func TestCIDetectPullRequest(t *testing.T) {
	gh := actionsVars(t, "pull_request")
	gitea := with(gh, "GITEA_ACTIONS", "true", "GITHUB_SERVER_URL", "https://your-gitea.example")
	big := writeFile(t, "big.json", `{"pull_request":{"number":7},"pad":"`+strings.Repeat("x", ciMaxEventBytes)+`"}`)
	for _, tc := range []struct {
		name    string
		vars    map[string]string
		url, ci string
	}{
		{"github pull_request", gh, "https://github.example.com/octo/demo/pull/7", ciNameGitHub},
		{"github pull_request_target", with(gh, "GITHUB_EVENT_NAME", "pull_request_target"), "https://github.example.com/octo/demo/pull/7", ciNameGitHub},
		{"github push", with(gh, "GITHUB_EVENT_NAME", "push"), "", ciNameGitHub},
		{"github workflow_dispatch", with(gh, "GITHUB_EVENT_NAME", "workflow_dispatch"), "", ciNameGitHub},
		{"github no event name", with(gh, "GITHUB_EVENT_NAME", ""), "", ciNameGitHub},
		{"github missing event file", with(gh, "GITHUB_EVENT_PATH", t.TempDir()+"/none.json"), "", ciNameGitHub},
		{"github no event path", with(gh, "GITHUB_EVENT_PATH", ""), "", ciNameGitHub},
		{"github event over 1 MiB", with(gh, "GITHUB_EVENT_PATH", big), "", ciNameGitHub},
		{"github event not JSON", with(gh, "GITHUB_EVENT_PATH", writeFile(t, "n.json", "not json")), "", ciNameGitHub},
		{"github missing number", with(gh, "GITHUB_EVENT_PATH", writeFile(t, "m.json", `{"pull_request":{"title":"x"}}`)), "", ciNameGitHub},
		{"github zero number", with(gh, "GITHUB_EVENT_PATH", eventFile(t, "0")), "", ciNameGitHub},
		{"github string number", with(gh, "GITHUB_EVENT_PATH", eventFile(t, `"7"`)), "", ciNameGitHub},
		{"github no server", with(gh, "GITHUB_SERVER_URL", ""), "", ciNameGitHub},
		{"github no repository", with(gh, "GITHUB_REPOSITORY", ""), "", ciNameGitHub},
		{"github actions not true", with(gh, "GITHUB_ACTIONS", "1"), "", ciNameNone},
		{"gitea pull_request", gitea, "https://your-gitea.example/octo/demo/pulls/7", ciNameGitea},
		{"gitea push", with(gitea, "GITHUB_EVENT_NAME", "push"), "", ciNameGitea},
		{"gitlab merge request", gitLabVars(), "https://gitlab.example.com/group/sub/demo/-/merge_requests/12", ciNameGitLab},
		{"gitlab branch pipeline", with(gitLabVars(), "CI_MERGE_REQUEST_IID", ""), "", ciNameGitLab},
		{"gitlab bad iid", with(gitLabVars(), "CI_MERGE_REQUEST_IID", "12abc"), "", ciNameGitLab},
		{"gitlab no project", with(gitLabVars(), "CI_MERGE_REQUEST_PROJECT_PATH", ""), "", ciNameGitLab},
		{"gitlab no server", with(gitLabVars(), "CI_SERVER_URL", ""), "", ciNameGitLab},
		{"no ci", map[string]string{"CI": "true", "BUILD_NUMBER": "3"}, "", ciNameNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newCIVars(tc.vars)
			url, ci := detectPullRequest(v.get)
			if url != tc.url || ci != tc.ci {
				t.Errorf("detected (%q, %q), want (%q, %q)", url, ci, tc.url, tc.ci)
			}
			for _, k := range v.keys() {
				if !strings.HasPrefix(k, "GIT") && !strings.HasPrefix(k, "CI_") {
					t.Errorf("detection read %s", k)
				}
			}
		})
	}
}

// TestCIDetectedReview runs a review of the pull request a Gitea Actions
// pipeline names, without --pr-url.
func TestCIDetectedReview(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	vars := with(actionsVars(t, "pull_request"), "GITEA_ACTIONS", "true", "GITHUB_SERVER_URL", g.giteaBase())
	r := ciCall(t, reviewDiagEnv(g, l), vars, "review", "--json")
	if r.code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", r.code, r.errs)
	}
	var res review.Result
	if err := json.Unmarshal([]byte(r.out), &res); err != nil || res.PR.Number != 7 {
		t.Errorf("reviewed %v %+v", err, res.PR)
	}
	if !strings.Contains(r.errs, `detected="Gitea Actions"`) {
		t.Errorf("the log does not say where the pull request came from:\n%s", r.errs)
	}
	if strings.Contains(r.out+r.errs, ciEventMarker) {
		t.Error("the event file reached the output")
	}
	assertCIClean(t, r)

	// --pr-url wins over detection.
	r = ciCall(t, reviewDiagEnv(g, l), vars, "review", "--json", "--pr-url", prURLOf(g, 8))
	if r.code != 0 {
		t.Fatalf("--pr-url: exit %d; stderr:\n%s", r.code, r.errs)
	}
	if err := json.Unmarshal([]byte(r.out), &res); err != nil || res.PR.Number != 8 {
		t.Errorf("--pr-url: reviewed %v %+v", err, res.PR)
	}
	for _, k := range r.variables {
		if strings.HasPrefix(k, "GITHUB_EVENT") {
			t.Errorf("--pr-url given, yet detection read %s", k)
		}
	}
}

func TestCIDetectionNoPullRequest(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	for _, tc := range []struct {
		vars map[string]string
		ci   string
	}{
		{with(actionsVars(t, "push")), ciNameGitHub},
		{with(gitLabVars(), "CI_MERGE_REQUEST_IID", ""), ciNameGitLab},
		{nil, ciNameNone},
	} {
		for _, cmd := range []string{"review", "describe", "improve"} {
			r := ciCall(t, reviewDiagEnv(g, l), tc.vars, cmd)
			want := "no pull request: pass --pr-url (detected: " + tc.ci + ")"
			if r.code != 2 || r.out != "" || r.lastStderr != want || r.loads != 0 {
				t.Errorf("%s %s: exit %d, loads %d, final line %q, want %q", tc.ci, cmd, r.code, r.loads, r.lastStderr, want)
			}
		}
	}
	if g.requests() != 0 || l.hits.Load() != 0 {
		t.Errorf("requests sent: provider %d, llm %d", g.requests(), l.hits.Load())
	}
}

// TestCIDetectedURLIsPinned: a detected URL on a host that is not
// configured is refused by the resolver, like a tool argument; nothing is
// sent.
func TestCIDetectedURLIsPinned(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	r := ciCall(t, reviewDiagEnv(g, l), gitLabVars(), "review", "--publish")
	want := (&provider.Error{Class: provider.ClassURLNotConfigured}).Error()
	if r.code != 1 || !strings.HasPrefix(r.lastStderr, "ci review: "+want) {
		t.Errorf("exit %d, final line %q", r.code, r.lastStderr)
	}
	if g.requests() != 0 || l.hits.Load() != 0 {
		t.Errorf("requests sent: provider %d, llm %d", g.requests(), l.hits.Load())
	}
	assertCIClean(t, r)
}
