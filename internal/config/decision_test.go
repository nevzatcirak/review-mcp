package config

import (
	"reflect"
	"testing"
)

// The decision.* keys (v2.7 spec V-14 §2.1).

// TestDecisionDefaults: off, with all three decisions allowed; server_info
// shows both keys through Summary with their defaults.
func TestDecisionDefaults(t *testing.T) {
	cfg, rep := mustLoad(t, memSrc(minimalEnv(), nil))
	if cfg.Decision.Enabled || !reflect.DeepEqual(cfg.Decision.Allowed, []string{"approve", "request_changes", "clear"}) {
		t.Fatalf("decision = %+v", cfg.Decision)
	}
	vals := cfg.Summary(rep).Values
	if v := vals["decision.enabled"]; v.Value != false || v.Source != OriginDefault {
		t.Errorf("decision.enabled in config.values = %+v", v)
	}
	if v := vals["decision.allowed"]; !reflect.DeepEqual(v.Value, []string{"approve", "request_changes", "clear"}) || v.Source != OriginDefault {
		t.Errorf("decision.allowed in config.values = %+v", v)
	}
}

// TestDecisionFromEnvAndFile: the env list is comma-separated; the file has
// a [decision] table; both appear in Summary with their sources.
func TestDecisionFromEnvAndFile(t *testing.T) {
	cfg, rep := mustLoad(t, memSrc(envWith(map[string]string{
		"REVIEW_MCP_DECISION_ENABLED": "true",
		"REVIEW_MCP_DECISION_ALLOWED": "request_changes, clear",
	}), nil))
	if !cfg.Decision.Enabled || !reflect.DeepEqual(cfg.Decision.Allowed, []string{"request_changes", "clear"}) {
		t.Fatalf("decision = %+v", cfg.Decision)
	}
	if cfg.Decision.Allows("approve") || !cfg.Decision.Allows("clear") {
		t.Errorf("Allows does not follow the list")
	}
	vals := cfg.Summary(rep).Values
	if v := vals["decision.enabled"]; v.Value != true || v.Source != OriginEnv {
		t.Errorf("decision.enabled = %+v", v)
	}
	file := "[decision]\nenabled = true\nallowed = [\"request_changes\"]\n"
	cfg, rep = mustLoad(t, memSrc(envWith(map[string]string{"REVIEW_MCP_CONFIG": "/c.toml"}), map[string]string{"/c.toml": file}))
	if !cfg.Decision.Enabled || !reflect.DeepEqual(cfg.Decision.Allowed, []string{"request_changes"}) {
		t.Fatalf("decision from the file = %+v", cfg.Decision)
	}
	if v := cfg.Summary(rep).Values["decision.allowed"]; v.Source != OriginFile {
		t.Errorf("decision.allowed source = %q, want file", v.Source)
	}
}

// TestDecisionAllowedProblems: an unknown item, a repeated item and an
// empty list while enabled are configuration problems; an empty list while
// disabled is not. An item is reported by index, never echoed.
func TestDecisionAllowedProblems(t *testing.T) {
	for _, c := range []struct {
		name, enabled, allowed, want string
	}{
		{"unknown", "false", "approve,merge-it", "decision.allowed[1]: must be approve, request_changes or clear"},
		{"case", "true", "Approve", "decision.allowed[0]: must be approve, request_changes or clear"},
		{"repeat", "true", "clear,approve,clear", "decision.allowed[2]: repeats an earlier item"},
		{"empty_while_enabled", "true", "", "decision.allowed: is empty while decision.enabled is true"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Load(memSrc(envWith(map[string]string{
				"REVIEW_MCP_DECISION_ENABLED": c.enabled, "REVIEW_MCP_DECISION_ALLOWED": c.allowed}), nil))
			if err == nil {
				t.Fatal("loaded")
			}
			ps := problemsOf(t, err)
			if !hasProblem(ps, c.want) {
				t.Errorf("problems %q, want %q", ps, c.want)
			}
			if hasProblem(ps, "merge-it") {
				t.Errorf("a problem echoes the item: %q", ps)
			}
		})
	}
	cfg := mustLoadCfg(t, map[string]string{"REVIEW_MCP_DECISION_ALLOWED": ""})
	if cfg.Decision.Enabled || len(cfg.Decision.Allowed) != 0 {
		t.Errorf("decision = %+v", cfg.Decision)
	}
}

func mustLoadCfg(t *testing.T, extra map[string]string) *Config {
	t.Helper()
	cfg, _ := mustLoad(t, memSrc(envWith(extra), nil))
	return cfg
}

// TestServeRefusesDecision: decision.enabled is a startup error in serve
// mode with the fixed sentence, from the environment and from a file;
// serve without it loads.
func TestServeRefusesDecision(t *testing.T) {
	const want = "decision.enabled: review decisions are not available in serve mode; unset REVIEW_MCP_DECISION_ENABLED"
	if ServeDecisionSentence != want {
		t.Fatalf("ServeDecisionSentence = %q", ServeDecisionSentence)
	}
	if ps := serveProblems(t, serveEnv(map[string]string{"REVIEW_MCP_DECISION_ENABLED": "true"}), nil); !hasProblem(ps, want) {
		t.Errorf("decision.enabled in serve mode not refused: %v", ps)
	}
	file := "[decision]\nenabled = true\n"
	if ps := serveProblems(t, serveEnv(map[string]string{"REVIEW_MCP_CONFIG": "/c.toml"}), map[string]string{"/c.toml": file}); !hasProblem(ps, want) {
		t.Errorf("decision.enabled from a file in serve mode not refused: %v", ps)
	}
	if ps := serveProblems(t, serveEnv(map[string]string{"REVIEW_MCP_DECISION_ALLOWED": "approve"}), nil); ps != nil {
		t.Errorf("serve without decision.enabled: %v", ps)
	}
}
