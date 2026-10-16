package repocfg

// allowList is the §1.2 allow-list of V-5, in the order of its table. The
// order is that of the applied keys in the result and the note.
//
// A key not in this list is ignored, whatever its name or its value: llm.*,
// the provider sections, context.*, serve.*, log.*, repo_config.*, the other
// diff.* limits, the *_tokens budgets and every secret-looking key.
var allowList = []string{
	"output.language",
	"ignore.glob",
	"ignore.regex",
	"diff.ignore_generated_frameworks",
	"review.guidelines_paths",
	"review.require_tests",
	"review.require_security",
	"review.require_performance",
	"review.require_effort_estimate",
	"review.inline_findings",
	"review.persistent_overview",
	"review.ci_status",
	"review.file_tree",
	"review.incremental",
	"review.max_findings",
	"review.max_total_findings",
	"improve.max_suggestions",
	"improve.min_score",
	"review.extra_instructions",
	"ask.extra_instructions",
	"review.focuses",
}

var allowed = func() map[string]bool {
	m := make(map[string]bool, len(allowList))
	for _, k := range allowList {
		m[k] = true
	}
	return m
}()
