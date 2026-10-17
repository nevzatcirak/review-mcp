package mcpserver

import (
	"log/slog"
	"maps"
	"strings"
	"testing"
)

// TestServerInfoReportsIssueReads: server_info says, per provider, whether
// review-mcp reads the issues of its tracker (X-42): Gitea, GitHub and
// GitLab do, Bitbucket Server has no tracker. A degraded start reports none,
// since no provider can be built.
func TestServerInfoReportsIssueReads(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	res := callServerInfo(t, connect(t, realDeps(validEnv(), logger)))
	got := map[string]bool{}
	for _, p := range structuredOf(t, res).Providers {
		got[p.Kind] = p.Issues
	}
	want := map[string]bool{"gitea": true, "bitbucket_server": false, "github": true, "gitlab": true}
	if !maps.Equal(got, want) {
		t.Errorf("issues per provider = %v, want %v", got, want)
	}
	text := textOf(t, res)
	if n := strings.Count(text, ", reads issues\n"); n != 3 {
		t.Errorf("the markdown names %d providers that read issues, want 3:\n%s", n, text)
	}
	if strings.Contains(text, "bitbucket.example.com/bb`, reads issues") {
		t.Error("the markdown says Bitbucket Server reads issues")
	}

	d := realDeps(invalidEnv(), logger)
	for _, p := range structuredOf(t, callServerInfo(t, connect(t, d))).Providers {
		if p.Issues {
			t.Errorf("degraded start: %s reports issue reads", p.Kind)
		}
	}
}
