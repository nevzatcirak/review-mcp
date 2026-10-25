package main

import (
	"strings"
	"testing"
)

// TestCIHasNoDecision (v2.7 spec V-14 §2.1, X-47 item 14): ci makes no
// review decision. "ci review --help" names none, and --decision is an
// unknown flag: a usage error (exit 2) that loads nothing and sends
// nothing. Automated gating is the exit code of ci review --fail-on.
func TestCIHasNoDecision(t *testing.T) {
	g, l := newFakeGitea(t), newFakeLLM(t, 200, reviewAnswer)
	env := reviewDiagEnv(g, l)
	pr := prURLOf(g, 7)
	for _, cmd := range []string{ciReview, ciDescribe, ciImprove} {
		r := ciCall(t, env, nil, cmd, "--help")
		if r.code != 2 || r.loads != 0 || !strings.Contains(r.errs, "review-mcp ci review") {
			t.Fatalf("ci %s --help: exit %d, loads %d, stderr %q", cmd, r.code, r.loads, r.errs)
		}
		if strings.Contains(strings.ToLower(r.errs), "decision") || strings.Contains(r.errs, "approve") {
			t.Errorf("ci %s --help names a decision:\n%s", cmd, r.errs)
		}
		r = ciCall(t, env, nil, cmd, "--pr-url", pr, "--decision", "approve")
		if r.code != 2 || r.out != "" || r.loads != 0 || !strings.Contains(r.errs, "flag provided but not defined: -decision") {
			t.Errorf("ci %s --decision: exit %d, loads %d, stdout %q, stderr %q", cmd, r.code, r.loads, r.out, r.errs)
		}
		assertCIClean(t, r)
	}
	if g.requests() != 0 || l.hits.Load() != 0 {
		t.Errorf("requests were sent: provider %d, llm %d", g.requests(), l.hits.Load())
	}
}
