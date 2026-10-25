package repocfg

import (
	"slices"
	"testing"
)

// TestDecisionKeysIgnored (v2.7 spec V-14 §2.1): neither decision key is in
// the allow-list, so a repository config with a [decision] table lists both
// under ignored and changes nothing: a pull request can never turn review
// decisions on for itself, nor widen what is allowed.
func TestDecisionKeysIgnored(t *testing.T) {
	base, o := applyText(t, "[decision]\nenabled = true\nallowed = [\"approve\"]\n")
	ignored := slices.Sorted(slices.Values(o.Result.Ignored))
	if !slices.Equal(ignored, []string{"decision.allowed", "decision.enabled"}) || len(o.Result.Applied) != 0 ||
		o.Result.Status != StatusNothingApplied {
		t.Fatalf("result %+v", o.Result)
	}
	if o.Config.Decision.Enabled || !slices.Equal(o.Config.Decision.Allowed, base.Decision.Allowed) {
		t.Errorf("the repository config changed decision: %+v", o.Config.Decision)
	}
}
