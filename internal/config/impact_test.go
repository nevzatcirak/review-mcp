package config

import (
	"slices"
	"strings"
	"testing"
)

func TestImpactDefaults(t *testing.T) {
	cfg, rep := mustLoad(t, MemSource{Env: minimalEnv()})
	got := cfg.Context.Impact
	if got.Enabled || got.Depth != 2 || got.MaxCallersPerNode != 5 || got.MaxLinks != 40 || got.MaxTokens != 4000 ||
		len(got.Paths) != 0 || len(got.ExcludePaths) != 0 || len(got.EntryPoints) != 0 {
		t.Fatalf("defaults: %+v", got)
	}
	if len(rep.Warnings) != 0 {
		t.Fatalf("warnings: %v", rep.Warnings)
	}
	sum := cfg.Summary(rep)
	for _, k := range []string{"context.impact.enabled", "context.impact.depth", "context.impact.max_callers_per_node",
		"context.impact.max_links", "context.impact.max_tokens", "context.impact.paths", "context.impact.exclude_paths", "context.impact.entry_points"} {
		if _, ok := sum.Values[k]; !ok {
			t.Errorf("server_info config.values lacks %s", k)
		}
	}
}

func TestImpactFromEnvAndFile(t *testing.T) {
	env := envWith(map[string]string{
		"REVIEW_MCP_CONTEXT_REPO_ENABLED":                "true",
		"REVIEW_MCP_CONTEXT_IMPACT_ENABLED":              "true",
		"REVIEW_MCP_CONTEXT_IMPACT_DEPTH":                "3",
		"REVIEW_MCP_CONTEXT_IMPACT_MAX_CALLERS_PER_NODE": "8",
		"REVIEW_MCP_CONTEXT_IMPACT_PATHS":                "services,billing/",
		"REVIEW_MCP_CONTEXT_IMPACT_ENTRY_POINTS":         "api/**",
	})
	cfg, rep := mustLoad(t, MemSource{Env: env})
	got := cfg.Context.Impact
	if !got.Enabled || got.Depth != 3 || got.MaxCallersPerNode != 8 || !slices.Equal(got.Paths, []string{"services", "billing/"}) ||
		!slices.Equal(got.EntryPoints, []string{"api/**"}) || len(rep.Warnings) != 0 {
		t.Fatalf("env: %+v %v", got, rep.Warnings)
	}
}

func TestImpactValidation(t *testing.T) {
	cases := map[string]string{
		"REVIEW_MCP_CONTEXT_IMPACT_DEPTH":                "4",
		"REVIEW_MCP_CONTEXT_IMPACT_MAX_CALLERS_PER_NODE": "0",
		"REVIEW_MCP_CONTEXT_IMPACT_MAX_LINKS":            "201",
		"REVIEW_MCP_CONTEXT_IMPACT_MAX_TOKENS":           "499",
		"REVIEW_MCP_CONTEXT_IMPACT_PATHS":                "/abs",
		"REVIEW_MCP_CONTEXT_IMPACT_EXCLUDE_PATHS":        "a/../b",
		"REVIEW_MCP_CONTEXT_IMPACT_ENTRY_POINTS":         "zz[",
	}
	for env, v := range cases {
		_, _, err := Load(MemSource{Env: envWith(map[string]string{env: v})})
		if err == nil {
			t.Errorf("%s=%s accepted", env, v)
			continue
		}
		key := envToKey(t, env)
		if !hasProblem(problemsOf(t, err), key) {
			t.Errorf("%s: problems %v", env, problemsOf(t, err))
		}
		for _, p := range problemsOf(t, err) {
			// Numbers are echoed like every range problem; a path or a glob
			// is reported by its index only.
			if strings.Contains(env, "PATHS") || strings.Contains(env, "ENTRY") {
				if strings.Contains(p, v) {
					t.Errorf("%s: problem echoes the value: %q", env, p)
				}
			}
		}
	}
	many := strings.TrimSuffix(strings.Repeat("a,", 21), ",")
	if _, _, err := Load(MemSource{Env: envWith(map[string]string{"REVIEW_MCP_CONTEXT_IMPACT_PATHS": many})}); err == nil || !hasProblem(problemsOf(t, err), "at most 20") {
		t.Errorf("21 paths: %v", err)
	}
}

func envToKey(t *testing.T, env string) string {
	t.Helper()
	for _, e := range table {
		if e.env == env {
			return e.key
		}
	}
	t.Fatalf("no key for %s", env)
	return ""
}

func TestImpactNotes(t *testing.T) {
	_, rep := mustLoad(t, MemSource{Env: envWith(map[string]string{"REVIEW_MCP_CONTEXT_IMPACT_ENABLED": "true"})})
	if !slices.Contains(rep.Warnings, ImpactWithoutRepoSentence) {
		t.Fatalf("no startup note without repository context: %v", rep.Warnings)
	}
	cfg, rep, err := loadServe(t, memSrc(serveEnv(map[string]string{"REVIEW_MCP_CONTEXT_IMPACT_ENABLED": "true", "REVIEW_MCP_CONTEXT_IMPACT_DEPTH": "1"}), nil))
	if err != nil {
		t.Fatalf("serve refused the impact keys: %v", err)
	}
	if !slices.Contains(rep.Warnings, ServeImpactSentence) || slices.Contains(rep.Warnings, ImpactWithoutRepoSentence) {
		t.Fatalf("serve warnings: %v", rep.Warnings)
	}
	if !slices.Contains(cfg.Summary(rep).Warnings, ServeImpactSentence) {
		t.Fatal("server_info warnings lack the serve note")
	}
}

func TestImpactClone(t *testing.T) {
	c := Defaults()
	c.Context.Impact.Paths = []string{"a"}
	c.Context.Impact.ExcludePaths = []string{"b"}
	c.Context.Impact.EntryPoints = []string{"c/**"}
	cp := c.Clone()
	cp.Context.Impact.Paths[0], cp.Context.Impact.ExcludePaths[0], cp.Context.Impact.EntryPoints[0] = "x", "y", "z"
	if c.Context.Impact.Paths[0] != "a" || c.Context.Impact.ExcludePaths[0] != "b" || c.Context.Impact.EntryPoints[0] != "c/**" {
		t.Fatal("Clone shares the impact lists")
	}
}
