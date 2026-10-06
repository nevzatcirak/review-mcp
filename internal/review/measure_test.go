package review

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/tokens"
)

// measureExtra is a representative short extra instruction for the
// entry-criterion measurement.
const measureExtra = "Focus on error handling and concurrency. Ignore formatting-only changes."

// TestScaffoldingTokens is the measurement of entry criterion P4 §0.3: it
// renders the prompt scaffolding with an empty diff and empty PR fields
// (title, branch, description) for every combination of the three field
// toggles, en-US and one non-English language, with and without extra
// instructions, and logs the token figures (run with -v to see the
// table). It asserts only invariants; the figures go into the report and
// WP-PR-4e sets the diag diff --prompt-tokens default from the maximum.
//
// Columns: raw = tokens.Raw(system) + tokens.Raw(user); est = the same
// with tokens.Estimate at the default factor; request =
// ScaffoldingTokens (est plus the 48-token framing allowance), the
// PromptTokens the pipeline uses.
func TestScaffoldingTokens(t *testing.T) {
	factor := config.Defaults().LLM.TokenEstimateFactor
	var b strings.Builder
	fmt.Fprintf(&b, "\n| effort | tests | security | language | extra | raw | est (f=%.1f) | request |\n|---|---|---|---|---|---|---|---|\n", factor)
	maxReq, minReq := 0, int(^uint(0)>>1)
	for mask := range 8 {
		tg := Toggles{EffortEstimate: mask&1 != 0, Tests: mask&2 != 0, Security: mask&4 != 0}
		for _, lang := range []string{"en-US", "tr-TR"} {
			for _, extra := range []string{"", measureExtra} {
				in := PromptInput{Toggles: tg, MaxFindings: config.Defaults().Review.MaxFindings,
					ExtraInstructions: extra, Language: lang, Date: "2026-10-06"}
				p, err := RenderPrompts(in)
				if err != nil {
					t.Fatal(err)
				}
				raw := tokens.Raw(p.System) + tokens.Raw(p.User)
				est := tokens.Estimate(p.System, factor) + tokens.Estimate(p.User, factor)
				req, err := ScaffoldingTokens(in, factor)
				if err != nil {
					t.Fatal(err)
				}
				if req != est+48 {
					t.Errorf("request %d != est %d + 48", req, est)
				}
				maxReq, minReq = max(maxReq, req), min(minReq, req)
				fmt.Fprintf(&b, "| %v | %v | %v | %s | %v | %d | %d | %d |\n",
					tg.EffortEstimate, tg.Tests, tg.Security, lang, extra != "", raw, est, req)
			}
		}
	}
	fmt.Fprintf(&b, "\nrequest tokens: min %d, max %d\n", minReq, maxReq)
	t.Log(b.String())
	if maxReq <= minReq || maxReq > 4096 {
		t.Errorf("implausible scaffolding figures: min %d max %d", minReq, maxReq)
	}
}
