package repocfg

import (
	"context"
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/provider"
)

// ForRun reads the repository config of pr at its base revision and merges
// it into a copy of base: the one read of a run, made after the pull request
// was read and before any prompt is built. The caller continues on
// Outcome.Config. Nothing but the status and the counts is logged. log may be
// nil.
func ForRun(ctx context.Context, log *slog.Logger, base *config.Config, p FileGetter, ref provider.PRRef, pr *provider.PullRequest) *Outcome {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	o := Apply(base, Read(ctx, log, base.RepoConfig, p, ref, pr))
	log.Debug("repo config", "status", string(o.Result.Status), "applied", len(o.Result.Applied), "ignored", len(o.Result.Ignored))
	return o
}

// Statuses lists every value of Result.Status.
func Statuses() []string {
	return []string{string(StatusApplied), string(StatusNothingApplied), string(StatusAbsent), string(StatusInvalid), string(StatusOff), string(StatusUnreadable)}
}

// Schema is the JSON schema of Result, for the output schemas of the tools
// that read the repository config.
func Schema() map[string]any {
	enum := make([]any, 0, 6)
	for _, s := range Statuses() {
		enum = append(enum, s)
	}
	strList := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	return map[string]any{"type": "object", "additionalProperties": false,
		"description": "the repository config (X-40) read at the target branch",
		"required":    []any{"path", "status", "applied", "ignored"},
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "repo_config.path, the file's path in the repository"},
			"status": map[string]any{"type": "string", "enum": enum,
				"description": "applied: at least one setting was applied; nothing_applied: the file was read but nothing in it was applied; absent: there is no file at the target branch; invalid: the file is not TOML or is larger than 16 KiB, and was ignored; off: repo_config.enabled is false, nothing was read; unreadable: the file could not be read, and was ignored"},
			"applied": strList("the keys applied, in the order of the allow-list"),
			"ignored": strList("the keys ignored: keys a repository may not change and allowed keys with an invalid value, at most 10, then \"and N more\""),
		}}
}
