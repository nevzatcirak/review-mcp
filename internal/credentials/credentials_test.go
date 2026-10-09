package credentials

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

const marker = "VALUE-MARKER-7f3a"

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestValueChecks(t *testing.T) {
	long := strings.Repeat("a", MaxValueBytes)
	tests := []struct {
		name      string
		h         http.Header
		want      string
		malformed bool
	}{
		{"absent", hdr(), "", false},
		{"empty", hdr(HeaderGiteaToken, ""), "", false},
		{"blank", hdr(HeaderGiteaToken, " \t "), "", false},
		{"trimmed", hdr(HeaderGiteaToken, " \ttok-1 \t"), "tok-1", false},
		{"lower-case header name", hdr("x-review-mcp-gitea-token", "tok-2"), "tok-2", false},
		{"visible ascii", hdr(HeaderGiteaToken, "!~azAZ09-_.=/+"), "!~azAZ09-_.=/+", false},
		{"max length", hdr(HeaderGiteaToken, long), long, false},
		{"too long", hdr(HeaderGiteaToken, long+"a"), "", true},
		{"too long after trim only", hdr(HeaderGiteaToken, " "+long+" "), long, false},
		{"inner space", hdr(HeaderGiteaToken, "tok "+marker), "", true},
		{"control char", hdr(HeaderGiteaToken, marker+"\x01"), "", true},
		{"DEL", hdr(HeaderGiteaToken, marker+"\x7f"), "", true},
		{"non-ascii", hdr(HeaderGiteaToken, marker+"é"), "", true},
		{"sent twice", hdr(HeaderGiteaToken, "a", HeaderGiteaToken, "b"), "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, err := Value(tc.h, HeaderGiteaToken)
			if tc.malformed {
				var me *MalformedError
				if !errors.As(err, &me) {
					t.Fatalf("want MalformedError, got %v (value %q)", err, v)
				}
				if err.Error() != "malformed credential header: X-Review-MCP-Gitea-Token" {
					t.Errorf("message = %q", err.Error())
				}
				if strings.Contains(err.Error(), marker) || v != "" {
					t.Error("malformed value echoed or returned")
				}
				return
			}
			if err != nil || v != tc.want {
				t.Errorf("Value = %q, %v; want %q", v, err, tc.want)
			}
		})
	}
}

func TestCheckNamesTheHeader(t *testing.T) {
	for _, name := range ToolHeaders {
		err := Check(hdr(name, "x\x00"+marker))
		if err == nil || err.Error() != "malformed credential header: "+name {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := Check(hdr(HeaderGiteaToken, "ok", HeaderLLMAPIKey, "ok", HeaderAuthorization, "Bearer x y\x01")); err != nil {
		t.Errorf("well-formed headers rejected: %v", err)
	}
}

func TestSecrets(t *testing.T) {
	h := hdr(HeaderGiteaToken, "g", HeaderBitbucketServerToken, "b", HeaderGitHubToken, "h", HeaderLLMAPIKey, "k")
	access := config.NewSecret("acc")
	s, err := Secrets(h, false, config.NewSecret("server-key"), access)
	if err != nil {
		t.Fatal(err)
	}
	if s.GiteaToken.Reveal() != "g" || s.BitbucketServerToken.Reveal() != "b" || s.GitHubToken.Reveal() != "h" ||
		s.LLMAPIKey.Reveal() != "k" || s.ServeAccessToken.Reveal() != "acc" {
		t.Errorf("header source: %+v", s)
	}
	s, err = Secrets(h, true, config.NewSecret("server-key"), access)
	if err != nil || s.LLMAPIKey.Reveal() != "server-key" {
		t.Errorf("server source: %v", err)
	}
	// With the server source, a malformed LLM header is ignored (never read);
	// a malformed provider header still fails.
	if _, err := Secrets(hdr(HeaderLLMAPIKey, "\x01"), true, config.NewSecret("k"), access); err != nil {
		t.Errorf("server source read the LLM header: %v", err)
	}
	if _, err := Secrets(hdr(HeaderBitbucketServerToken, "\x01"), true, config.NewSecret("k"), access); err == nil {
		t.Error("malformed provider header accepted")
	}
	if _, err := Secrets(hdr(HeaderGitHubToken, "\x01"), true, config.NewSecret("k"), access); err == nil || err.Error() != "malformed credential header: X-Review-MCP-GitHub-Token" {
		t.Errorf("malformed github header: %v", err)
	}
	s, err = Secrets(nil, false, config.Secret{}, config.Secret{})
	if err != nil || s.GiteaToken.IsSet() || s.LLMAPIKey.IsSet() {
		t.Errorf("nil header: %v", err)
	}
}

func TestPresence(t *testing.T) {
	p := Presence(hdr(HeaderGiteaToken, marker, HeaderLLMAPIKey, " ", HeaderBitbucketServerToken, "\x01", HeaderAuthorization, "Bearer "+marker))
	want := map[string]string{HeaderGiteaToken: "set", HeaderLLMAPIKey: "unset", HeaderBitbucketServerToken: "malformed", HeaderGitHubToken: "unset", HeaderAuthorization: "set"}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %q, want %q", k, p[k], v)
		}
	}
	for k, v := range p {
		if strings.Contains(k+v, marker) {
			t.Errorf("value reported for %s", k)
		}
	}
	if Presence(nil)[HeaderGiteaToken] != "unset" {
		t.Error("nil header")
	}
}
