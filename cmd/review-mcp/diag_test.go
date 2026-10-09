package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

var diagSecrets = []string{diagGiteaToken, diagBBSToken, fakeLLMKey, diagMarker}

// diag runs the diag command in-process and returns code, stdout, stderr.
func diag(env map[string]string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := runWith(append([]string{"diag"}, args...), strings.NewReader(""), &out, &errb, loaderFor(env))
	return code, out.String(), errb.String()
}

func assertNoLeak(t *testing.T, what, s string) {
	t.Helper()
	for _, secret := range diagSecrets {
		if strings.Contains(s, secret) {
			t.Errorf("%s leaks %q", what, secret)
		}
	}
}

func decodeReport(t *testing.T, out string) (map[string]any, string) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("stdout does not start with JSON: %v\n%s", err, out)
	}
	rest := out[dec.InputOffset():]
	return m, rest
}

func asMaps(t *testing.T, v any) []map[string]any {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("not a JSON array: %#v", v)
	}
	out := []map[string]any{}
	for _, e := range arr {
		out = append(out, e.(map[string]any))
	}
	return out
}

func TestDiagPRGitea(t *testing.T) {
	g := newFakeGitea(t)
	code, out, errs := diag(diagEnv(g, nil), "pr", g.giteaPR()+"?token=URLQUERYSECRET")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	t.Logf("sample diag pr output (gitea fake):\n%s", out)
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
	if strings.Contains(out+errs, "URLQUERYSECRET") {
		t.Error("URL query value leaked")
	}
	if !strings.HasSuffix(out, "}\n") || strings.HasSuffix(out, "\n\n") {
		t.Errorf("want exactly one trailing newline, got %q", out[len(out)-3:])
	}
	if !strings.Contains(out, "\n  \"kind\": \"gitea\",\n") {
		t.Errorf("expected 2-space indentation:\n%s", out)
	}

	m, rest := decodeReport(t, out)
	if strings.TrimSpace(rest) != "" {
		t.Errorf("unexpected output after JSON: %q", rest)
	}
	wantKeys := []string{"kind", "ref", "title", "source_branch", "target_branch", "head_sha", "base_sha",
		"base_strategy", "commit_count", "commits", "files", "skipped", "totals", "elapsed_ms"}
	if len(m) != len(wantKeys) {
		t.Errorf("got %d top-level keys, want %d: %v", len(m), len(wantKeys), m)
	}
	for _, k := range wantKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	for k, want := range map[string]any{
		"kind": "gitea", "title": "Add feature", "source_branch": "feature", "target_branch": "main",
		"head_sha": "headsha", "base_sha": "mergesha", "base_strategy": "gitea:merge_base", "commit_count": float64(2),
	} {
		if m[k] != want {
			t.Errorf("%s = %#v, want %#v", k, m[k], want)
		}
	}
	ref := m["ref"].(map[string]any)
	if ref["namespace"] != "octo" || ref["repo"] != "demo" || ref["number"] != float64(7) ||
		ref["url"] != g.giteaPR()+"?token=REDACTED" {
		t.Errorf("ref = %v", ref)
	}
	commits := m["commits"].([]any)
	if len(commits) != 2 || commits[0] != "First commit" || commits[1] != "Second commit" {
		t.Errorf("commits = %v (want oldest first, first lines only)", commits)
	}

	files := asMaps(t, m["files"])
	byPath := map[string]map[string]any{}
	for _, f := range files {
		byPath[f["path"].(string)] = f
		for _, k := range []string{"path", "old_path", "type", "additions", "deletions", "patch_bytes", "base_status", "head_status", "binary"} {
			if _, ok := f[k]; !ok {
				t.Errorf("file %v lacks key %q", f["path"], k)
			}
		}
	}
	if len(files) != 2 {
		t.Fatalf("files = %v", files)
	}
	rn := byPath["src/renamed.go"]
	if rn["old_path"] != "src/old_name.go" || rn["type"] != "renamed" || rn["additions"] != float64(1) ||
		rn["deletions"] != float64(1) || rn["base_status"] != "full" || rn["head_status"] != "full" || rn["binary"] != false {
		t.Errorf("renamed file = %v", rn)
	}
	if rn["patch_bytes"].(float64) <= 0 {
		t.Errorf("patch_bytes = %v", rn["patch_bytes"])
	}
	if byPath["src/app.go"]["type"] != "modified" || byPath["src/app.go"]["old_path"] != "" {
		t.Errorf("modified file = %v", byPath["src/app.go"])
	}
	sk := asMaps(t, m["skipped"])
	if len(sk) != 1 || sk[0]["path"] != "assets/logo.png" || sk[0]["reason"] != "binary" {
		t.Errorf("skipped = %v", sk)
	}
	tot := m["totals"].(map[string]any)
	if tot["files"] != float64(2) || tot["skipped"] != float64(1) || tot["additions"] != float64(2) ||
		tot["deletions"] != float64(2) || tot["patch_bytes"] != rn["patch_bytes"].(float64)+byPath["src/app.go"]["patch_bytes"].(float64) {
		t.Errorf("totals = %v", tot)
	}
}

func TestDiagPRBitbucket(t *testing.T) {
	b := newFakeBBS(t)
	code, out, errs := diag(diagEnv(nil, b), "pr", b.bbsPR())
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
	m, _ := decodeReport(t, out)
	if m["kind"] != "bitbucket_server" || m["base_strategy"] != "bbs:merge_base_endpoint" ||
		m["base_sha"] != "mergesha" || m["head_sha"] != "headsha" || m["target_branch"] != "main" {
		t.Errorf("report = %v", m)
	}
	ref := m["ref"].(map[string]any)
	if ref["namespace"] != "PROJ" || ref["repo"] != "demo" || ref["number"] != float64(7) {
		t.Errorf("ref = %v", ref)
	}
	files := asMaps(t, m["files"])
	if len(files) != 2 || files[0]["path"] != "src/app.go" || files[0]["type"] != "modified" ||
		files[0]["additions"] != float64(1) || files[0]["deletions"] != float64(1) ||
		files[1]["path"] != "src/new.go" || files[1]["type"] != "added" || files[1]["base_status"] != "not_applicable" {
		t.Errorf("files = %v", files)
	}
	// Empty arrays must be [] and never null.
	if !strings.Contains(out, "\"skipped\": []") {
		t.Errorf("skipped must be an empty array:\n%s", out)
	}
	commits := m["commits"].([]any)
	if len(commits) != 2 || commits[0] != "First commit" || commits[1] != "Second commit" {
		t.Errorf("commits = %v", commits)
	}
}

func TestDiagPRArraysNeverNull(t *testing.T) {
	r := buildPRReport(provider.PRRef{}, &provider.PullRequest{}, nil, &provider.Diff{}, 0)
	var buf bytes.Buffer
	if err := writeJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "null") {
		t.Errorf("report contains null:\n%s", buf.String())
	}
}

func TestDiagShowPatch(t *testing.T) {
	g := newFakeGitea(t)
	// Flags after the positional URL must work.
	code, out, errs := diag(diagEnv(g, nil), "pr", g.giteaPR(), "--show-patch", "src/renamed.go")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, errs)
	}
	_, rest := decodeReport(t, out)
	want := "\n--- patch: src/renamed.go ---\n@@ -1,2 +1,2 @@\n package main\n-var r = 1\n+var r = 2\n"
	if rest != want {
		t.Errorf("after the JSON:\n got %q\nwant %q", rest, want)
	}
	assertNoLeak(t, "stdout", out)

	// Flag before the URL gives the same output.
	code2, out2, _ := diag(diagEnv(g, nil), "pr", "--show-patch", "src/renamed.go", g.giteaPR())
	_, rest2 := decodeReport(t, out2)
	if code2 != 0 || rest2 != want {
		t.Errorf("flag-first form: code %d rest %q", code2, rest2)
	}

	// Bitbucket Server: patch is generated, still hunk-only.
	b := newFakeBBS(t)
	code, out, errs = diag(diagEnv(nil, b), "pr", b.bbsPR(), "--show-patch", "src/app.go")
	if code != 0 {
		t.Fatalf("bbs exit %d; stderr:\n%s", code, errs)
	}
	_, rest = decodeReport(t, out)
	if !strings.HasPrefix(rest, "\n--- patch: src/app.go ---\n@@ ") || !strings.Contains(rest, "-var a = 1\n+var a = 2\n") ||
		strings.Contains(rest, "+++") {
		t.Errorf("bbs patch output = %q", rest)
	}
}

func TestDiagShowPatchNoMatch(t *testing.T) {
	g := newFakeGitea(t)
	code, out, errs := diag(diagEnv(g, nil), "pr", g.giteaPR(), "--show-patch", "nope.go")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	m, rest := decodeReport(t, out)
	if m["kind"] != "gitea" || strings.TrimSpace(rest) != "" {
		t.Errorf("JSON must still be printed, nothing after it: %q", rest)
	}
	if !strings.Contains(errs, "--show-patch: no changed file has that path") {
		t.Errorf("stderr = %q", errs)
	}
}

func TestDiagComment(t *testing.T) {
	g, b := newFakeGitea(t), newFakeBBS(t)
	env := diagEnv(g, b)
	const body = "  review-mcp connectivity check\n\nline two ## not trimmed  "

	code, out, errs := diag(env, "comment", g.giteaPR(), "--body", body)
	if code != 0 {
		t.Fatalf("gitea exit %d; stderr:\n%s", code, errs)
	}
	var res map[string]string
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res) != 2 ||
		res["id"] != "55" || res["url"] != "https://your-gitea.example/octo/demo/pulls/7#issuecomment-55" {
		t.Errorf("gitea result = %q (%v)", out, err)
	}
	if p := g.posts(); len(p) != 1 {
		t.Fatalf("gitea posts = %v", p)
	} else {
		var posted map[string]string
		if err := json.Unmarshal([]byte(p[0]), &posted); err != nil || posted["body"] != body {
			t.Errorf("gitea posted body = %q (%v)", p[0], err)
		}
	}

	code, out, errs = diag(env, "comment", "--body", body, b.bbsPR())
	if code != 0 {
		t.Fatalf("bbs exit %d; stderr:\n%s", code, errs)
	}
	res = nil
	if err := json.Unmarshal([]byte(out), &res); err != nil || res["id"] != "77" || res["url"] == "" {
		t.Errorf("bbs result = %q (%v)", out, err)
	}
	if p := b.posts(); len(p) != 1 {
		t.Fatalf("bbs posts = %v", p)
	} else {
		var posted map[string]string
		if err := json.Unmarshal([]byte(p[0]), &posted); err != nil || posted["text"] != body {
			t.Errorf("bbs posted body = %q (%v)", p[0], err)
		}
	}
	assertNoLeak(t, "stdout", out)
	assertNoLeak(t, "stderr", errs)
}

func TestDiagUsageErrors(t *testing.T) {
	g := newFakeGitea(t)
	env := diagEnv(g, nil)
	u := g.giteaPR()
	for name, args := range map[string][]string{
		"no subcommand":      {},
		"unknown subcommand": {"bogus"},
		"pr no url":          {"pr"},
		"pr two urls":        {"pr", u, u},
		"pr bad flag":        {"pr", u, "--bogus"},
		"pr empty show":      {"pr", u, "--show-patch", ""},
		"pr show no value":   {"pr", u, "--show-patch"},
		"comment no url":     {"comment", "--body", "x"},
		"comment no body":    {"comment", u},
		"comment empty body": {"comment", u, "--body", ""},
		"comment bad flag":   {"comment", u, "--body", "x", "--bogus"},
	} {
		t.Run(name, func(t *testing.T) {
			code, out, errs := diag(env, args...)
			if code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if out != "" {
				t.Errorf("stdout must be empty, got %q", out)
			}
			if !strings.Contains(errs, "usage:") {
				t.Errorf("stderr lacks usage: %q", errs)
			}
		})
	}
	if n := g.requests(); n != 0 {
		t.Errorf("usage errors made %d requests", n)
	}
}

func TestTopLevelUsageListsDiag(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"bogus"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	for _, s := range []string{"diag pr <PR_URL>", "diag comment <PR_URL> --body", "diag ask <PR_URL> --question", "diag describe <PR_URL>", "diag improve <PR_URL>"} {
		if !strings.Contains(errb.String(), s) {
			t.Errorf("usage lacks %q:\n%s", s, errb.String())
		}
	}
}

func TestDiagInvalidConfigNoNetwork(t *testing.T) {
	g := newFakeGitea(t)
	env := diagEnv(g, nil)
	env["REVIEW_MCP_LLM_CONTEXT_WINDOW"] = "12"
	env["REVIEW_MCP_BITBUCKET_SERVER_BASE_URL"] = "ftp://user:" + fakeURLUserinfo + "@bitbucket.example.com"
	for _, args := range [][]string{{"pr", g.giteaPR()}, {"comment", g.giteaPR(), "--body", "x"}} {
		code, out, errs := diag(env, args...)
		if code != 1 {
			t.Errorf("%v: exit %d, want 1", args, code)
		}
		if out != "" {
			t.Errorf("stdout = %q", out)
		}
		lines := strings.Split(strings.TrimSpace(errs), "\n")
		if len(lines) < 2 {
			t.Errorf("want one problem per line, got %q", errs)
		}
		if strings.Contains(errs, fakeURLUserinfo) || strings.Contains(errs, diagGiteaToken) {
			t.Errorf("config problems leak a secret: %q", errs)
		}
	}
	if n := g.requests(); n != 0 {
		t.Errorf("invalid config made %d requests", n)
	}
}

func TestDiagForeignHostNoRequests(t *testing.T) {
	g := newFakeGitea(t)
	want := (&provider.Error{Class: provider.ClassURLNotConfigured}).Error()
	for _, u := range []string{
		"https://other.example.net/octo/demo/pulls/7",
		strings.Replace(g.giteaPR(), "127.0.0.1", "localhost", 1), // look-alike host for the same server
		g.srv.URL + "/gitea-other/octo/demo/pulls/7",              // sibling path prefix
	} {
		for _, args := range [][]string{{"pr", u}, {"comment", u, "--body", "x"}} {
			code, out, errs := diag(diagEnv(g, nil), args...)
			if code != 1 || out != "" {
				t.Errorf("%v: exit %d stdout %q", args, code, out)
			}
			if !strings.Contains(errs, want) {
				t.Errorf("%v: stderr %q lacks %q", args, errs, want)
			}
		}
	}
	if n := g.requests(); n != 0 {
		t.Errorf("foreign URLs made %d requests (token must never reach them)", n)
	}
}

func TestDiagProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		class  provider.ErrorClass
	}{
		{401, provider.ClassAuth}, {404, provider.ClassNotFound}, {429, provider.ClassRateLimited}, {500, provider.ClassUpstream},
	} {
		g := newFakeGitea(t)
		g.failStatus.Store(int32(tc.status)) //nolint:gosec // G115: small HTTP status constants
		code, out, errs := diag(diagEnv(g, nil), "pr", g.giteaPR())
		want := (&provider.Error{Class: tc.class, Status: tc.status}).Error()
		if code != 1 || out != "" || !hasLine(errs, want) {
			t.Errorf("status %d: exit %d stdout %q stderr %q (want line %q)", tc.status, code, out, errs, want)
		}
		assertNoLeak(t, "stderr", errs)
	}
}

func hasLine(s, line string) bool {
	for _, l := range strings.Split(s, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

func TestReportErrorGeneric(t *testing.T) {
	var b bytes.Buffer
	err := errors.New("Get \"https://host.example/api?access_token=SECRETQ\": boom")
	if code := reportError(&b, err); code != 1 {
		t.Errorf("code %d", code)
	}
	if got := b.String(); got != "unexpected error; rerun with REVIEW_MCP_LOG_LEVEL=debug for details\n" {
		t.Errorf("stderr = %q", got)
	}
	b.Reset()
	reportError(&b, context.DeadlineExceeded)
	if !strings.Contains(b.String(), "(class: timeout)") {
		t.Errorf("stderr = %q", b.String())
	}
	b.Reset()
	reportError(&b, provider.ErrAuth)
	if b.String() != provider.ErrAuth.Error()+"\n" {
		t.Errorf("stderr = %q", b.String())
	}
}

// TestDiagLeakInProcess [canary]: with debug logging, neither stream carries a
// token or the response-body marker, on success and on every error path.
func TestDiagLeakInProcess(t *testing.T) {
	g, b := newFakeGitea(t), newFakeBBS(t)
	env := diagEnv(g, b)
	bad := newFakeGitea(t)
	bad.failStatus.Store(401)
	badEnv := diagEnv(bad, nil)

	runs := []struct {
		env  map[string]string
		args []string
	}{
		{env, []string{"pr", g.giteaPR(), "--show-patch", "src/app.go"}},
		{env, []string{"pr", b.bbsPR(), "--show-patch", "src/app.go"}},
		{env, []string{"comment", g.giteaPR(), "--body", "hello"}},
		{env, []string{"comment", b.bbsPR(), "--body", "hello"}},
		{badEnv, []string{"pr", bad.giteaPR()}},
		{badEnv, []string{"comment", bad.giteaPR(), "--body", "hello"}},
		{env, []string{"pr", "https://other.example.net/octo/demo/pulls/7"}},
	}
	for _, r := range runs {
		code, out, errs := diag(r.env, r.args...)
		_ = code
		assertNoLeak(t, "stdout of "+r.args[0], out)
		assertNoLeak(t, "stderr of "+r.args[0], errs)
		if r.env["REVIEW_MCP_LOG_LEVEL"] == "debug" && errs == "" {
			t.Errorf("%v: expected debug logs on stderr", r.args)
		}
	}
}
