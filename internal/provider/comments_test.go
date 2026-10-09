package provider

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestValidateReply(t *testing.T) {
	cases := []struct {
		id, body string
		hint     string // "" means valid
	}{
		{"12", "ok", ""},
		{"007", "ok", ""},
		{"1", "  text  ", ""},
		{"12", "", "empty body"},
		{"12", " \t\r\n ", "empty body"},
		{"", "ok", "invalid comment id"},
		{"0", "ok", "invalid comment id"},
		{"000", "ok", "invalid comment id"},
		{"-1", "ok", "invalid comment id"},
		{"+1", "ok", "invalid comment id"},
		{"1.5", "ok", "invalid comment id"},
		{"abc", "ok", "invalid comment id"},
		{" 1", "ok", "invalid comment id"},
		{"1 ", "ok", "invalid comment id"},
		{"١٢", "ok", "invalid comment id"}, // non-ASCII digits
		{"99999999999999999999", "ok", "invalid comment id"},
		{"1/../2", "ok", "invalid comment id"},
		// The body is checked first.
		{"abc", "", "empty body"},
	}
	for _, c := range cases {
		err := ValidateReply(c.id, c.body)
		if c.hint == "" {
			if err != nil {
				t.Errorf("(%q,%q): unexpected error %v", c.id, c.body, err)
			}
			continue
		}
		var pe *Error
		if !errors.As(err, &pe) || pe.Class != ClassProtocol || pe.Hint != c.hint || pe.Status != 0 {
			t.Errorf("(%q,%q): err = %v, want protocol/%q", c.id, c.body, err, c.hint)
		}
		if !errors.Is(err, ErrProtocol) {
			t.Errorf("(%q,%q): not errors.Is ErrProtocol", c.id, c.body)
		}
	}
}

func th(id string, kind ThreadKind, path string, line int, at int) Thread {
	return Thread{ID: id, Kind: kind, Path: path, Line: line,
		Comments: []CommentItem{{ID: id, CreatedAt: time.Unix(int64(at), 0)}}}
}

func ids(ts []Thread) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func TestSortThreads(t *testing.T) {
	ts := []Thread{
		th("i-b-3", ThreadInline, "b.go", 3, 1),
		th("i-a-9-late", ThreadInline, "a.go", 9, 50),
		th("g-late", ThreadGeneral, "", 0, 90),
		th("i-a-9-early", ThreadInline, "a.go", 9, 5),
		th("i-a-2", ThreadInline, "a.go", 2, 100),
		th("g-early", ThreadGeneral, "", 0, 10),
		th("11", ThreadGeneral, "", 0, 70),
		th("9", ThreadGeneral, "", 0, 70), // same time: numeric ID tie-break, 9 < 11
		th("i-a-0", ThreadInline, "a.go", 0, 7),
		{ID: "empty", Kind: ThreadGeneral, Comments: []CommentItem{}}, // zero time sorts first
	}
	SortThreads(ts)
	want := []string{"empty", "g-early", "9", "11", "g-late", "i-a-0", "i-a-2", "i-a-9-early", "i-a-9-late", "i-b-3"}
	if got := ids(ts); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v\nwant    %v", got, want)
	}
	// Idempotent and input-order independent.
	rev := make([]Thread, len(ts))
	for i := range ts {
		rev[len(ts)-1-i] = ts[i]
	}
	SortThreads(rev)
	if got := ids(rev); !reflect.DeepEqual(got, want) {
		t.Fatalf("reversed input order = %v", got)
	}
}

func TestThreadJSONShape(t *testing.T) {
	b, err := json.Marshal(Thread{ID: "1", Kind: ThreadInline, Comments: []CommentItem{}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "kind", "path", "line", "outdated", "resolved", "comments", "reply_in_thread"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %s", k, b)
		}
	}
	if m["resolved"] != nil {
		t.Errorf("resolved = %v, want null", m["resolved"])
	}
	if c, ok := m["comments"].([]any); !ok || c == nil {
		t.Errorf("comments = %v, want []", m["comments"])
	}
	b, _ = json.Marshal(CommentItem{ID: "1"})
	for _, k := range []string{`"id"`, `"author"`, `"body"`, `"created_at"`, `"updated_at"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("CommentItem JSON lacks %s: %s", k, b)
		}
	}
}

func TestValidateUpdatePR(t *testing.T) {
	s := func(v string) *string { return &v }
	for name, tc := range map[string]struct {
		up   UpdatePR
		hint string
	}{
		"title":            {UpdatePR{Title: s("T")}, ""},
		"description":      {UpdatePR{Description: s("")}, ""},
		"version 0":        {UpdatePR{Title: s("T"), Version: "0"}, ""},
		"version 12":       {UpdatePR{Title: s("T"), Version: "12"}, ""},
		"emoji and fence":  {UpdatePR{Title: s("Fix \U0001F680"), Description: s("a\r\n```\r\nb  \r\n```")}, ""},
		"nothing":          {UpdatePR{Version: "1"}, "nothing to update"},
		"empty title":      {UpdatePR{Title: s("")}, "empty title"},
		"blank title":      {UpdatePR{Title: s(" \t")}, "empty title"},
		"multi-line title": {UpdatePR{Title: s("a\nb")}, "invalid title"},
		"NUL":              {UpdatePR{Description: s("a\x00")}, "invalid description"},
		"version text":     {UpdatePR{Title: s("T"), Version: "x"}, "invalid pull request version"},
		"negative version": {UpdatePR{Title: s("T"), Version: "-1"}, "invalid pull request version"},
	} {
		err := ValidateUpdatePR(tc.up)
		var pe *Error
		switch {
		case tc.hint == "" && err != nil:
			t.Errorf("%s: unexpected error %v", name, err)
		case tc.hint != "" && (!errors.As(err, &pe) || pe.Class != ClassProtocol || pe.Hint != tc.hint):
			t.Errorf("%s: err = %v, want protocol %q", name, err, tc.hint)
		}
	}
}
