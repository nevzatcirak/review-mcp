package tools

import (
	"testing"

	"github.com/nevzatcirak/review-mcp/internal/config"
)

func TestWaitSeconds(t *testing.T) {
	n := func(v int) *int { return &v }
	cfg := config.Defaults()
	cfg.LLM.WaitSeconds = 12
	for _, c := range []struct {
		name string
		arg  *int
		cfg  *config.Config
		want int
		err  bool
	}{
		{"config", nil, cfg, 12, false},
		{"no config: compiled default", nil, nil, 45, false},
		{"argument wins", n(0), cfg, 0, false},
		{"upper bound", n(600), cfg, 600, false},
		{"too high", n(601), cfg, 0, true},
		{"negative", n(-1), cfg, 0, true},
	} {
		got, err := WaitSeconds(c.arg, c.cfg)
		if c.err {
			if err == nil || UserMessage(err) != InvalidWaitSecondsMessage {
				t.Errorf("%s: error = %v, want %q", c.name, err, InvalidWaitSecondsMessage)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: = %d, %v; want %d", c.name, got, err, c.want)
		}
	}
}
