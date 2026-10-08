package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{"debug", slog.LevelDebug, false},
		{"INFO", slog.LevelInfo, false},
		{"Warn", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"", 0, true},
		{"trace", 0, true},
		{"warning", 0, true},
	}
	for _, tc := range tests {
		got, err := ParseLevel(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseLevel(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNewWritesToWriterAtLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelWarn)
	l.Info("hidden")
	l.Warn("shown")
	out := buf.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, "shown") {
		t.Fatalf("unexpected log output: %q", out)
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "https://example.com/org/repo/pulls/1", "https://example.com/org/repo/pulls/1"},
		{"canary userinfo", "https://user:s3cret@example.com/repo.git", "https://example.com/repo.git"},
		{"canary userinfo token only", "https://tok3n@example.com/x", "https://example.com/x"},
		{"canary query access_token", "https://example.com/api?access_token=abc123", "https://example.com/api?access_token=REDACTED"},
		{"multiple query params", "https://example.com/a?x=1&y=2", "https://example.com/a?x=REDACTED&y=REDACTED"},
		{"userinfo and query", "https://u:p@example.com/a?token=zzz", "https://example.com/a?token=REDACTED"},
		{"fragment dropped", "https://example.com/a#access_token=zzz", "https://example.com/a"},
		{"canary opaque userinfo", "user:s3cret@your-gitea.example", "user:REDACTED"},
		{"canary mailto like", "mailto:someone:secret@example.com", "mailto:REDACTED"},
		{"canary bare at sign", "s3cret@your-gitea.example/path", "REDACTED"},
		{"canary bare query value", "https://your-gitea.example/api?ghp_S3CRETVALUE", "https://your-gitea.example/api?REDACTED"},
		{"mixed bare and keyed", "https://example.com/a?a=1&tok3n&b=2", "https://example.com/a?a=REDACTED&REDACTED&b=REDACTED"},
		{"unparseable", "http://[::1", "<unparseable URL>"},
		{"control char", "http://exa\x7fmple.com/", "<unparseable URL>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactURL(tc.in)
			if got != tc.want {
				t.Errorf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for _, secret := range []string{"s3cret", "tok3n", "abc123", "zzz", "secret", "someone", "S3CRETVALUE"} {
				if strings.Contains(got, secret) {
					t.Errorf("RedactURL(%q) leaked %q: %q", tc.in, secret, got)
				}
			}
		})
	}
}

func TestRedactText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no secrets", "fetching https://example.com/a", "fetching https://example.com/a"},
		{"canary bearer", "Authorization: Bearer abc.def.ghi", "Authorization: REDACTED"},
		{"canary gitea token scheme", "Authorization: token 0123456789abcdef", "Authorization: REDACTED"},
		{"canary basic", "Authorization: Basic dXNlcjpwYXNz", "Authorization: REDACTED"},
		{"canary url userinfo", "clone https://user:pass@your-gitea.example/org/repo.git failed", "clone https://REDACTED@your-gitea.example/org/repo.git failed"},
		{"bare value", "Authorization: rawsecretvalue", "Authorization: REDACTED"},
		{"equals form", "Authorization=Bearer abc123", "Authorization=REDACTED"},
		{"lowercase header", "authorization: bearer abc123", "authorization: REDACTED"},
		{"json quoted", `{"Authorization": "Bearer abc123", "x": 1}`, `{"Authorization": "REDACTED", "x": 1}`},
		{"password with at sign", "https://u:p@ss@bitbucket.example.com/r", "https://REDACTED@bitbucket.example.com/r"},
		{"both in one line", "GET https://a:b@example.com/x Authorization: Bearer tkn", "GET https://REDACTED@example.com/x Authorization: REDACTED"},
		{"multiline headers", "Accept: json\nAuthorization: Bearer tkn\nHost: example.com", "Accept: json\nAuthorization: REDACTED\nHost: example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactText(tc.in)
			if got != tc.want {
				t.Errorf("RedactText(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for _, secret := range []string{"abc.def.ghi", "0123456789abcdef", "dXNlcjpwYXNz", "pass@", "rawsecretvalue", "abc123", "tkn"} {
				if strings.Contains(got, secret) {
					t.Errorf("RedactText(%q) leaked %q: %q", tc.in, secret, got)
				}
			}
		})
	}
}

// TestRedactURLKeepsNestedNamespace: a path with several namespace segments
// (nested groups) comes back with its "/" separators, and only the userinfo,
// the fragment and the query values change.
func TestRedactURLKeepsNestedNamespace(t *testing.T) {
	in := "https://u:FAKE-pw@gitlab.example.com/group/sub/team/repo/-/merge_requests/7?private_token=FAKE-q#frag" //nolint:gosec // synthetic fake credentials used to test redaction
	want := "https://gitlab.example.com/group/sub/team/repo/-/merge_requests/7?private_token=REDACTED"
	got := RedactURL(in)
	if got != want {
		t.Errorf("RedactURL = %q, want %q", got, want)
	}
	if strings.Contains(got, "%2F") {
		t.Errorf("RedactURL escaped a path separator: %q", got)
	}
}
