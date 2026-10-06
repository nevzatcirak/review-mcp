// Source: hardcoded lists in is_valid_file (pr_agent/algo/language_handler.py)
// at PR-Agent commit 8e5a9295973b24af4b70cafd0b660a230811ef9e (8e5a929). This
// is MIT-licensed material adapted from PR-Agent; see NOTICE.
//
// Entry counts: lockfiles=14 minified_suffixes=5

package data

// LockfileNames are exact basenames of lockfiles that are never reviewed.
var LockfileNames = []string{
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	"composer.lock",
	"Gemfile.lock",
	"poetry.lock",
	"go.sum",
	".terraform.lock.hcl",
	"uv.lock",
	"Cargo.lock",
	"Pipfile.lock",
	"mix.lock",
	"pubspec.lock",
	"bun.lockb",
}

// MinifiedSuffixes are lowercase filename suffixes of minified or map files
// that are never reviewed.
var MinifiedSuffixes = []string{
	".min.js",
	".min.css",
	".js.map",
	".ts.map",
	".css.map",
}
