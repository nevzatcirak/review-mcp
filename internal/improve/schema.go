package improve

// ResultSchema is the JSON schema (draft 2020-12) of Result, for the
// pr_improve tool's MCP output schema (v2 spec §1.7). It is a plain map, as
// review.ResultSchema and describe.ResultSchema are. Every property is
// required and no other is allowed, so a result matches exactly one branch
// of job_result's oneOf.
func ResultSchema() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	nullInt := func(desc string, minimum int) map[string]any {
		return map[string]any{"type": []any{"integer", "null"}, "minimum": minimum, "description": desc}
	}
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
	score := nullInt("the self-review score, 0 to 10; null when the suggestion is unscored (its self-review call failed, or the self-review gave it no usable score)", 0)
	score["maximum"] = MaxScore

	return object("pr_improve result", map[string]any{
		"suggestions": map[string]any{"type": "array",
			"description": "the code suggestions: part order, then score descending with the unscored ones last in their part; without duplicates; at most improve.max_suggestions",
			"items": object("one code suggestion", map[string]any{
				"file":          str("file path, one of the files whose diff the suggestion's model call was shown"),
				"language":      str("programming language of the file, as the model named it"),
				"label":         str("free-text label of the kind of suggestion, one line, at most 40 characters"),
				"summary":       str("one-line summary of the suggestion"),
				"content":       str("the suggestion, in prose"),
				"existing_code": str("the code the suggestion replaces, as the model quoted it from the diff"),
				"improved_code": str("the replacement for existing_code"),
				"start_line":    nullInt("first new-file line of existing_code as the self-review gave it; null when it gave none", 1),
				"end_line":      nullInt("last new-file line of existing_code as the self-review gave it; null when it gave none", 1),
				"score":         score,
				"why":           str("the self-review's reason for the score; empty when unscored"),
				"verified":      boolean("whether existing_code was found in the head file at start_line to end_line; not checked yet, always false"),
				"anchor": map[string]any{"type": []any{"object", "null"}, "properties": map[string]any{}, "required": []any{},
					"additionalProperties": false, "description": "where the suggestion is posted inline; not available yet, always null"},
			}, "file", "language", "label", "summary", "content", "existing_code", "improved_code", "start_line", "end_line",
				"score", "why", "verified", "anchor")},
		"coverage": object("what the model saw (X-3, X-18)", map[string]any{
			"included":       strList("files whose diff was sent in full"),
			"clipped":        strList("files whose diff was sent in part; not fully reviewed"),
			"deleted_listed": strList("deleted files whose patch was left out by design and whose names were sent in the deleted-files list (X-20)"),
			"omitted": object("files left out for the token budget, by change type", map[string]any{
				"added":    strList("added files"),
				"modified": strList("modified and renamed files"),
				"deleted":  strList("deleted files whose names were not sent either"),
			}, "added", "modified", "deleted"),
			"skipped":            skipped("files not reviewed for other reasons (binary, limits, fetch failures, empty or unparseable diffs, too_large, model_call_failed)"),
			"filtered":           skipped("files excluded by the ignore rules, with the matching rule"),
			"partial":            boolean("true when at least one changed file that is not filtered on purpose was not fully reviewed (X-18)"),
			"reviewed_files":     integer("files reviewed: included plus deleted_listed"),
			"total_files":        integer("changed files except those filtered on purpose (and binary or empty-diff files): reviewed_files + not_reviewed_files"),
			"not_reviewed_files": integer("changed files that were not reviewed: omitted, clipped, skipped for size or limit, unreadable, too large for a part of their own, or in a part whose model call failed"),
			"model_calls":        integer("suggestion calls with a diff (X-19): 1 for a run in one call, N for a run in N parts, 0 without a model call; re-asks and the self-review calls are not counted"),
			"failed_parts":       integer("parts whose suggestion call failed; their files are skipped with reason model_call_failed"),
			"repo_context": object("repository context in the suggestion prompts (RC-9)", map[string]any{
				"status":     map[string]any{"type": "string", "enum": []any{"used", "skipped", "off"}, "description": "used, skipped (see reason), or off (disabled)"},
				"reason":     str("why repository context was skipped; empty otherwise"),
				"symbols":    integer("symbols searched"),
				"references": integer("uses shown in the prompts"),
				"files":      integer("distinct files of the uses shown, summed over the parts"),
			}, "status", "reason", "symbols", "references", "files"),
		}, "included", "clipped", "deleted_listed", "omitted", "skipped", "filtered", "partial", "reviewed_files",
			"total_files", "not_reviewed_files", "model_calls", "failed_parts", "repo_context"),
		"notes": strList("notes about failed parts, self-review failures and scores, dropped or unscored suggestions, duplicates, the total cap, the discussion, truncation, clipped or trimmed files and the re-ask"),
		"metadata": object("run metadata", map[string]any{
			"model":             str("llm.model"),
			"context_window":    integer("the context window budgeted for"),
			"prompt_tokens":     integer("estimated tokens the diff budget reserves for the prompts: the larger of the suggestion scaffolding and the self-review scaffolding with room for the suggestions"),
			"diff_tokens":       integer("estimated tokens of the diff sent, summed over the parts"),
			"request_tokens":    integer("estimated tokens of the largest suggestion request; 0 without a model call"),
			"fast_path":         boolean("whether the whole extended diff fit one call"),
			"llm_calls":         integer("number of chat completions: suggestion calls, self-review calls and re-asks"),
			"self_review_calls": integer("chat completions of the self-review calls, re-asks included (part of llm_calls)"),
			"repair_tactic":     str("how the first accepted answer was parsed: direct or a YAML repair tactic; empty without a model call"),
			"reasked":           boolean("whether a call was asked a second time after an unparseable answer"),
			"truncated":         boolean("whether an answer was cut off by the output limit"),
			"diff_trimmed":      boolean("whether the request-size guard shortened the diff"),
			"already_discussed": integer("discussion threads shown to the model (X-13)"),
		}, "model", "context_window", "prompt_tokens", "diff_tokens", "request_tokens", "fast_path", "llm_calls",
			"self_review_calls", "repair_tactic", "reasked", "truncated", "diff_trimmed", "already_discussed"),
	}, "suggestions", "coverage", "notes", "metadata")
}
