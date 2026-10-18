package llm

import (
	"context"
	"net/http"
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

// goldenComplete is the request body Complete sent before tool calling was
// added (V-8 spec §1 item 2), captured from that code. The prompt holds
// characters the encoder escapes, so a change of encoder settings shows.
const goldenComplete = `{"model":"test-model","messages":[{"role":"system","content":"sys \u003cb\u003e\u0026\u003c/b\u003e \u2028 \"q\""},{"role":"user","content":"usr\tline\nü 🙂 \\"}],"temperature":0.2,"seed":7,"reasoning_effort":"low","max_tokens":512}`

// [canary] Complete stays byte-identical on the wire (V-8 spec §1 item 6):
// tool calling adds fields that must never reach a single-pass request.
func TestCompleteGoldenRequest(t *testing.T) {
	f := newFake(t, func(_ int, w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, okJSON("x")) })
	cfg := baseCfg(f.srv.URL)
	temp, seed, mt, re := 0.2, int64(7), 512, "low"
	cfg.Temperature, cfg.Seed, cfg.MaxOutputTokens, cfg.ReasoningEffort = &temp, &seed, &mt, &re
	for _, tc := range []struct {
		name string
		cfg  config.LLM
		want string
	}{
		{"sampling", cfg, goldenComplete},
		{"plain", baseCfg(f.srv.URL), goldenPlain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := f.calls()
			c := newClient(t, tc.cfg, nil, nil)
			if _, err := c.Complete(context.Background(), "sys \u003cb\u003e\u0026\u003c/b\u003e \u2028 \"q\"", "usr\tline\nü 🙂 \\"); err != nil {
				t.Fatal(err)
			}
			if got := string(f.body(n)); got != tc.want {
				t.Fatalf("request body changed:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// goldenPlain is the same without sampling parameters.
const goldenPlain = `{"model":"test-model","messages":[{"role":"system","content":"sys \u003cb\u003e\u0026\u003c/b\u003e \u2028 \"q\""},{"role":"user","content":"usr\tline\nü 🙂 \\"}]}`
