package gitlab

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// TestPositionRefused: only a 400 whose structured error names the
// position or the line code is a refused position, in each form GitLab
// sends it.
func TestPositionRefused(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"ruby hash, line code", 400, `{"message":"400 Bad request - Note {:line_code=>[\"can't be blank\", \"must be a valid line code\"]}"}`, true},
		{"ruby hash, position second", 400, `{"message":"400 Bad request - Note {:note=>[\"x\"], :position=>[\"is incomplete\"]}"}`, true},
		{"ruby 3.4 hash", 400, `{"message":"400 Bad request - Note {line_code: [\"can't be blank\"]}"}`, true},
		{"ruby hash, string keys", 400, `{"message":"400 Bad request - Note {\"position\"=>[\"is invalid\"]}"}`, true},
		{"object", 400, `{"message":{"line_code":["can't be blank"]}}`, true},
		{"object, position", 400, `{"message":{"position":["is incomplete"],"note":["x"]}}`, true},
		{"object, other field", 400, `{"message":{"note":["can't be blank"]}}`, false},
		{"other note error", 400, `{"message":"400 Bad request - Note {:note=>[\"can't be blank\"]}"}`, false},
		{"field name inside a value", 400, `{"message":"400 Bad request - Note {:note=>[\"bad line_code: x\"]}"}`, false},
		{"missing parameter", 400, `{"error":"position[new_line] is invalid"}`, false},
		{"not about a note", 400, `{"message":"400 Bad request - line_code: x"}`, false},
		{"not json", 400, `line_code`, false},
		{"422", 422, `{"message":{"line_code":["can't be blank"]}}`, false},
		{"500", 500, `{"message":{"position":["x"]}}`, false},
	} {
		if got := positionRefused(c.status, []byte(c.body)); got != c.want {
			t.Errorf("%s: positionRefused = %v, want %v", c.name, got, c.want)
		}
	}
}

// inlinePR is the merge request the unit tests post on.
func inlinePR() *provider.PullRequest {
	return &provider.PullRequest{HeadSHA: testHead, BaseSHA: testBase, StartSHA: testBase,
		WebURL: "https://gitlab.example.com/group/sub/project/-/merge_requests/7"}
}

// TestInlineResultsByStatus: each item is one request; a refused position
// is unanchorable, any other failure failed, with the fixed sentence of
// its class only; an auth failure stops the batch.
func TestInlineResultsByStatus(t *testing.T) {
	f := newFake(t)
	answers := []struct {
		status int
		body   string
	}{
		{400, `{"message":{"line_code":["can't be blank ` + testSentinel + `"]}}`},
		{400, `{"message":"400 Bad request - Note {:note=>[\"` + testSentinel + `\"]}"}`},
		{422, `{"message":{"position":["` + testSentinel + `"]}}`},
		{404, `{"message":"404 Not found ` + testSentinel + `"}`},
		{500, `{"message":"` + testSentinel + `"}`},
		{201, `{"id":"ab12","individual_note":false,"notes":[{"id":77,"type":"DiffNote","author":{"id":900,"username":"review-bot"}}]}`},
		{401, `{"message":"401 Unauthorized ` + testSentinel + `"}`},
	}
	calls := 0
	f.handle(http.MethodPost, fakeMR+"/discussions", func(w http.ResponseWriter, _ *http.Request) {
		a := answers[min(calls, len(answers)-1)]
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	})
	p, _ := f.provider(f.config(), time.Now())
	items := make([]provider.InlineComment, len(answers)+1)
	for i := range items {
		items[i] = provider.InlineComment{Path: "src/app.go", Line: i + 1, LineType: provider.LineAdded, Body: "Inline."}
	}
	res, err := p.PostInlineComments(t.Context(), testRef(), inlinePR(), items)
	if err != nil {
		t.Fatalf("PostInlineComments: %v", err)
	}
	want := []provider.InlineReason{
		provider.InlineReasonUnanchorable, provider.InlineReasonFailed, provider.InlineReasonFailed, provider.InlineReasonFailed,
		provider.InlineReasonFailed, provider.InlineReasonPosted, provider.InlineReasonFailed, provider.InlineReasonFailed,
	}
	for i, r := range res {
		if r.Reason != want[i] || r.Posted != (want[i] == provider.InlineReasonPosted) {
			t.Errorf("item %d: %+v, want reason %q", i, r, want[i])
		}
		if strings.Contains(r.Error, testSentinel) || strings.Contains(r.Error, testToken) {
			t.Errorf("item %d: the error carries server text: %q", i, r.Error)
		}
	}
	if res[5].ID != "77" || res[5].URL != inlinePR().WebURL+"#note_77" {
		t.Errorf("posted item: %+v", res[5])
	}
	if !strings.HasPrefix(res[7].Error, provider.ErrAuth.Error()) || res[7].Error != res[6].Error {
		t.Errorf("after the auth failure: %q, %q", res[6].Error, res[7].Error)
	}
	if calls != len(answers) {
		t.Errorf("%d requests, want %d (none after the auth failure)", calls, len(answers))
	}
}

// TestInlineRequestShape: the body and the position of an added line, an
// unchanged line and a renamed file's line; a range is posted on its first
// line.
func TestInlineRequestShape(t *testing.T) {
	f := newFake(t)
	var got []map[string]any
	f.handle(http.MethodPost, fakeMR+"/discussions", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Errorf("decode: %v", err)
		}
		got = append(got, in)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"id": "ab", "notes": []any{map[string]any{"id": 5, "type": "DiffNote"}}})
	})
	p, _ := f.provider(f.config(), time.Now())
	pr := inlinePR()
	pr.StartSHA = "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3"
	items := []provider.InlineComment{
		{Path: "src/app.go", Line: 4, LineType: provider.LineAdded, Body: "Added."},
		{Path: "src/app.go", Line: 5, EndLine: 7, LineType: provider.LineContext, OldLine: 4, Body: "Unchanged."},
		{Path: "pkg/new.go", OldPath: "pkg/old.go", Line: 2, LineType: provider.LineContext, OldLine: 3, Body: "Renamed."},
	}
	if _, err := p.PostInlineComments(t.Context(), testRef(), pr, items); err != nil {
		t.Fatal(err)
	}
	pos := func(newPath, oldPath string, newLine, oldLine float64) map[string]any {
		m := map[string]any{"position_type": "text", "base_sha": testBase, "start_sha": pr.StartSHA, "head_sha": testHead,
			"new_path": newPath, "old_path": oldPath, "new_line": newLine}
		if oldLine != 0 {
			m["old_line"] = oldLine
		}
		return m
	}
	want := []map[string]any{
		{"body": "Added.", "position": pos("src/app.go", "src/app.go", 4, 0)},
		{"body": "Unchanged.", "position": pos("src/app.go", "src/app.go", 5, 4)},
		{"body": "Renamed.", "position": pos("pkg/new.go", "pkg/old.go", 2, 3)},
	}
	if len(got) != len(want) {
		t.Fatalf("%d requests, want %d", len(got), len(want))
	}
	for i := range want {
		g, _ := json.Marshal(got[i])
		w, _ := json.Marshal(want[i])
		if string(g) != string(w) {
			t.Errorf("request %d:\n got %s\nwant %s", i, g, w)
		}
	}
}

// TestInlineValidationSendsNothing: an invalid old line is refused before
// any request.
func TestInlineValidationSendsNothing(t *testing.T) {
	f := newFake(t)
	p, _ := f.provider(f.config(), time.Now())
	for _, it := range []provider.InlineComment{
		{Path: "a.go", Line: 2, LineType: provider.LineAdded, OldLine: 1, Body: "x"},
		{Path: "a.go", Line: 2, LineType: provider.LineContext, OldLine: -1, Body: "x"},
	} {
		_, err := p.PostInlineComments(t.Context(), testRef(), inlinePR(), []provider.InlineComment{it})
		if !errors.Is(err, provider.ErrProtocol) {
			t.Errorf("%+v: error %v, want protocol", it, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
}

// TestInlineWithoutStartSHA reads the merge request for its diff_refs
// when the caller's pull request has no start_sha, and refuses when the
// head moved.
func TestInlineWithoutStartSHA(t *testing.T) {
	f := newFake(t)
	f.json(fakeMR, mrJSON(true))
	var start string
	f.handle(http.MethodPost, fakeMR+"/discussions", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Position struct {
				StartSHA string `json:"start_sha"`
			} `json:"position"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		start = in.Position.StartSHA
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, map[string]any{"notes": []any{map[string]any{"id": 5, "type": "DiffNote"}}})
	})
	p, _ := f.provider(f.config(), time.Now())
	items := []provider.InlineComment{{Path: "a.go", Line: 1, LineType: provider.LineAdded, Body: "x"}}
	res, err := p.PostInlineComments(t.Context(), testRef(), &provider.PullRequest{HeadSHA: testHead, BaseSHA: testBase}, items)
	if err != nil || len(res) != 1 || !res[0].Posted || start != testBase {
		t.Fatalf("res %+v, err %v, start_sha %q", res, err, start)
	}
	_, err = p.PostInlineComments(t.Context(), testRef(), &provider.PullRequest{HeadSHA: testPushed}, items)
	if !errors.Is(err, provider.ErrConflict) {
		t.Errorf("a moved head: error %v, want conflict", err)
	}
}
