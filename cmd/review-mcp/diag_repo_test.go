package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nevzatcirak/review-mcp/internal/ask"
	"github.com/nevzatcirak/review-mcp/internal/repoctx"
	"github.com/nevzatcirak/review-mcp/internal/review"
)

// TestDryRunReportsRepositoryContext: the dry run shows the block's tokens
// and the symbol list (RC-9) when repository context is on, and has no such
// field when it is off, so the report of a run without the feature is
// unchanged.
func TestDryRunReportsRepositoryContext(t *testing.T) {
	rep := &repoctx.Report{Tokens: 321, Symbols: []string{"Alpha", "Beta"}}

	pl := &review.Plan{Result: &review.Result{}, RepoReport: rep}
	raw, err := json.Marshal(buildDryRunReport(pl, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"repo_context":{"tokens":321,"symbols":["Alpha","Beta"]}`) {
		t.Errorf("pr_review dry run: %s", raw)
	}
	pl.RepoReport = nil
	raw, _ = json.Marshal(buildDryRunReport(pl, time.Second))
	if strings.Contains(string(raw), `"repo_context":{"tokens"`) {
		t.Errorf("pr_review dry run with the feature off: %s", raw)
	}

	apl := &ask.Plan{Result: &ask.Result{}, RepoReport: rep}
	raw, _ = json.Marshal(buildAskDryRunReport(apl, time.Second))
	if !strings.Contains(string(raw), `"repo_context":{"tokens":321,"symbols":["Alpha","Beta"]}`) {
		t.Errorf("pr_ask dry run: %s", raw)
	}
	apl.RepoReport = nil
	raw, _ = json.Marshal(buildAskDryRunReport(apl, time.Second))
	if strings.Contains(string(raw), `"repo_context":{"tokens"`) {
		t.Errorf("pr_ask dry run with the feature off: %s", raw)
	}
}
