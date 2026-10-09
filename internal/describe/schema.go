package describe

// ResultSchema is the JSON schema (draft 2020-12) of Result, for the
// pr_describe tool's MCP output schema (v2 spec §3.6). It is a plain map, as
// review.ResultSchema is. Every property is required and no other is
// allowed, so a result matches exactly one branch of job_result's oneOf.
func ResultSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	nullStr := func(desc string) map[string]any {
		return map[string]any{"type": []any{"string", "null"}, "description": desc}
	}
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
	typeEnum := []any{}
	for _, t := range AllowedTypes {
		typeEnum = append(typeEnum, t)
	}

	return object("pr_describe result", map[string]any{
		"title": nullStr("the generated title; null when it was not generated (the summary call of a description in parts failed, or the answer had none)"),
		"type": map[string]any{
			"type":        []any{"array", "null"},
			"items":       map[string]any{"type": "string", "enum": typeEnum},
			"description": "the PR types; values outside the list are dropped with a note; null when they were not generated",
		},
		"description": nullStr("the generated summary, up to four bullets; when the summary call of a description in parts failed, the described files' titles, one per line; null when nothing was generated"),
		"files": map[string]any{"type": "array", "description": "the walkthrough: one entry per described file, in the order the model returned them, parts in order",
			"items": object("one described file", map[string]any{
				"path":    str("file path, one of the files shown to the model"),
				"title":   str("one-line summary of the file's changes"),
				"summary": str("summary of the file's changes, in bullet points"),
				"label":   str("free-text label of the kind of change, one line, at most 40 characters"),
			}, "path", "title", "summary", "label")},
		"coverage": object("what the model saw and what was described (X-3, X-18); \"reviewed\" in the field names reads \"described\"", map[string]any{
			"included":       strList("files whose diff was sent in full and that are described"),
			"clipped":        strList("files whose diff was sent in part; not described"),
			"deleted_listed": strList("deleted files whose patch was left out by design and whose names were sent in the deleted-files list, and that are described (X-20)"),
			"omitted": object("files left out for the token budget, by change type; not described", map[string]any{
				"added":    strList("added files"),
				"modified": strList("modified and renamed files"),
				"deleted":  strList("deleted files whose names were not sent either"),
			}, "added", "modified", "deleted"),
			"skipped":            skipped("files not described for other reasons (binary, limits, fetch failures, empty or unparseable diffs, too_large, model_call_failed, not_returned: shown to the model but no walkthrough entry came back)"),
			"filtered":           skipped("files excluded by the ignore rules, with the matching rule"),
			"partial":            boolean("true when at least one changed file that is not filtered on purpose was not described (X-18)"),
			"reviewed_files":     integer("described files: included plus deleted_listed"),
			"total_files":        integer("changed files except those filtered on purpose (and binary or empty-diff files): reviewed_files + not_reviewed_files"),
			"not_reviewed_files": integer("changed files that were not described: omitted, clipped, skipped for size or limit, unreadable, too large for a part of their own, in a part whose model call failed, or not returned by the model"),
			"model_calls":        integer("calls with a diff (X-19): 1 for a description in one call, N for a description in N parts, 0 without a model call; re-asks and the summary call of a description in parts are not counted"),
			"failed_parts":       integer("parts whose model call failed; their files are skipped with reason model_call_failed and are not described"),
			"repo_context": object("repository context in the prompt (RC-9); pr_describe does not use it, so it is always off", map[string]any{
				"status":     map[string]any{"type": "string", "enum": []any{"used", "skipped", "off"}, "description": "always off for pr_describe"},
				"reason":     str("always empty for pr_describe"),
				"symbols":    integer("always 0 for pr_describe"),
				"references": integer("always 0 for pr_describe"),
				"files":      integer("always 0 for pr_describe"),
			}, "status", "reason", "symbols", "references", "files"),
		}, "included", "clipped", "deleted_listed", "omitted", "skipped", "filtered", "partial", "reviewed_files",
			"total_files", "not_reviewed_files", "model_calls", "failed_parts", "repo_context"),
		"notes": strList("notes about failed parts, the summary call, dropped walkthrough entries or types, files not described, truncation, clipped or trimmed files and the re-ask"),
		"metadata": object("run metadata", map[string]any{
			"model":           str("llm.model"),
			"context_window":  integer("the context window budgeted for"),
			"prompt_tokens":   integer("estimated tokens of the prompt scaffolding (with the part line for a description in parts)"),
			"diff_tokens":     integer("estimated tokens of the diff sent, summed over the parts"),
			"request_tokens":  integer("estimated tokens of the largest request with a diff; 0 without a model call"),
			"fast_path":       boolean("whether the whole extended diff fit one call"),
			"llm_calls":       integer("number of chat completions, re-asks and the summary call included"),
			"commit_messages": integer("commit messages read from the provider, before diff.max_commits_tokens clips them"),
			"repair_tactic":   str("how the first accepted answer was parsed: direct or a YAML repair tactic; empty without a model call"),
			"reasked":         boolean("whether a call was asked a second time after an unparseable answer"),
			"truncated":       boolean("whether an answer was cut off by the output limit"),
			"diff_trimmed":    boolean("whether the request-size guard shortened the diff"),
		}, "model", "context_window", "prompt_tokens", "diff_tokens", "request_tokens", "fast_path", "llm_calls",
			"commit_messages", "repair_tactic", "reasked", "truncated", "diff_trimmed"),
	}, "title", "type", "description", "files", "coverage", "notes", "metadata")
}
