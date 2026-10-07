package review

// ResultSchema is the JSON schema (draft 2020-12) of Result, for the
// pr_review tool's MCP output schema (DQ-6; WP-PR-4e attaches it). The
// review part comes from the descriptor table (ReviewSchema).
//
// It is a plain map rather than a github.com/google/jsonschema-go value:
// the MCP SDK's Tool.OutputSchema accepts any value that marshals to a JSON
// schema, and that module is only an indirect dependency of this one.
func ResultSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	boolean := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	strList := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	object := func(desc string, props map[string]any, required ...string) map[string]any {
		req := []any{}
		for _, r := range required {
			req = append(req, r)
		}
		return map[string]any{"type": "object", "description": desc, "properties": props,
			"required": req, "additionalProperties": false}
	}
	skipped := func(desc string) map[string]any {
		return map[string]any{"type": "array", "description": desc, "items": object("a file and the reason",
			map[string]any{"path": str("file path"), "reason": str("reason code or filter rule")}, "path", "reason")}
	}
	review := ReviewSchema()
	review["description"] = "the validated review; the optional fields appear only when enabled and usable"

	return object("pr_review result", map[string]any{
		"pr": object("the reviewed pull request", map[string]any{
			"kind":     str("provider kind: gitea or bitbucket_server"),
			"url":      str("pull request URL with credentials and query values removed"),
			"number":   integer("pull request number"),
			"title":    str("pull request title"),
			"head_sha": str("head commit the review describes"),
		}, "kind", "url", "number", "title"),
		"enabled_fields": strList("the review fields asked for, in order"),
		"review":         review,
		"coverage": object("what the model saw (X-3)", map[string]any{
			"included":       strList("files whose diff was sent in full"),
			"clipped":        strList("files whose diff was sent in part"),
			"deleted_listed": strList("deleted files whose patch was left out by design and whose names were sent in the deleted-files list; they count as reviewed (X-20)"),
			"omitted": object("files left out for the token budget, by change type", map[string]any{
				"added":    strList("added files"),
				"modified": strList("modified and renamed files"),
				"deleted":  strList("deleted files whose names were not sent either"),
			}, "added", "modified", "deleted"),
			"skipped":            skipped("files skipped for other reasons (binary, limits, fetch failures, empty or unparseable diffs)"),
			"filtered":           skipped("files excluded by the ignore rules, with the matching rule"),
			"partial":            boolean("true when at least one reviewable changed file was not fully reviewed: omitted, clipped, skipped by the provider for size or limit, or unreadable (X-18); files filtered on purpose do not count"),
			"reviewed_files":     integer("changed files whose diff was sent in full, plus deleted files listed by name"),
			"total_files":        integer("changed files except those filtered on purpose (and binary or empty-diff files): reviewed_files + not_reviewed_files"),
			"not_reviewed_files": integer("changed files the model did not fully see: omitted, clipped, skipped for size or limit, or unreadable"),
		}, "included", "clipped", "deleted_listed", "omitted", "skipped", "filtered", "partial", "reviewed_files",
			"total_files", "not_reviewed_files"),
		"notes": strList("notes about truncation, dropped findings, clipped or trimmed files and the re-ask"),
		"metadata": object("run metadata", map[string]any{
			"model":             str("llm.model"),
			"context_window":    integer("llm.context_window"),
			"prompt_tokens":     integer("estimated tokens of the prompt scaffolding"),
			"diff_tokens":       integer("estimated tokens of the diff sent"),
			"request_tokens":    integer("estimated tokens of the request; 0 without a model call"),
			"fast_path":         boolean("whether the whole extended diff fit"),
			"llm_calls":         integer("number of model calls (0 to 2)"),
			"repair_tactic":     str("how the answer was parsed: direct or a YAML repair tactic; empty without a model call"),
			"reasked":           boolean("whether the model was asked a second time after an unparseable answer"),
			"truncated":         boolean("whether the answer was cut off by the output limit"),
			"diff_trimmed":      boolean("whether the request-size guard shortened the diff"),
			"reviewed_at":       str("run time, RFC 3339 in UTC"),
			"already_discussed": integer("comment threads of the existing pull request discussion that were shown to the model (X-13); absent when 0"),
		}, "model", "context_window", "prompt_tokens", "diff_tokens", "request_tokens", "fast_path", "llm_calls",
			"repair_tactic", "reasked", "truncated", "diff_trimmed", "reviewed_at"),
		"publish": object("publishing outcome; present when publish was requested", map[string]any{
			"published":  boolean("whether the overview was posted or updated"),
			"comment_id": str("id of the overview comment"),
			"url":        str("URL of the overview comment"),
			"error":      str("why publishing failed; the review is still returned"),
			"updated":    boolean("true when the overview of an earlier run was edited in place instead of posting a new one"),
			"inline": object("inline comments; present when inline findings were on and the overview was posted or found", map[string]any{
				"posted":            integer("findings posted as inline comments"),
				"skipped_duplicate": integer("findings already posted on the pull request, not repeated"),
				"unanchorable":      integer("findings not on a line of the diff, listed in the overview only"),
				"failed":            integer("findings whose inline comment could not be posted, listed in the overview only"),
			}, "posted", "skipped_duplicate", "unanchorable", "failed"),
		}, "published"),
	}, "pr", "enabled_fields", "review", "coverage", "notes", "metadata")
}
