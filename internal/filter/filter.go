// Package filter decides which changed files are reviewed and classifies
// files by language. The embedded tables are adapted from PR-Agent (see
// NOTICE); the matching code is original.
package filter

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/nevzatcirak/review-mcp/internal/config"
	"github.com/nevzatcirak/review-mcp/internal/filter/data"
)

// Exclusion reasons reported by Explain.
const (
	ReasonLockfileOrMinified = "lockfile_or_minified"
	ReasonBadExtension       = "bad_extension"
	ReasonGeneratedPrefix    = "generated:"
	ReasonIgnoreGlob         = "ignore_glob"
	ReasonIgnoreRegex        = "ignore_regex"
	// ReasonEmptyPath is a sixth reason beyond the spec's five: upstream drops
	// empty names, and an explicit value is clearer than a misleading one.
	ReasonEmptyPath = "empty_path"
)

type generatedSet struct {
	framework string
	globs     []string
}

// Filter is an immutable file filter. It is safe for concurrent use.
type Filter struct {
	badExt    map[string]struct{}
	lockfiles map[string]struct{}
	generated []generatedSet
	globs     []string
	regexes   []*regexp.Regexp
}

// New builds a Filter from the validated configuration. It returns an error
// for an invalid glob, regex or framework name instead of failing open.
func New(cfg *config.Config) (*Filter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("filter: nil config")
	}
	f := &Filter{
		badExt:    make(map[string]struct{}, len(data.BadExtensions)),
		lockfiles: make(map[string]struct{}, len(data.LockfileNames)),
	}
	for _, e := range data.BadExtensions {
		f.badExt[strings.ToLower(e)] = struct{}{}
	}
	for _, n := range data.LockfileNames {
		f.lockfiles[n] = struct{}{}
	}
	for i, name := range cfg.Diff.IgnoreGeneratedFrameworks {
		globs, ok := data.GeneratedCode[name]
		if !ok {
			return nil, fmt.Errorf("diff.ignore_generated_frameworks[%d]: unknown framework (valid: %s)",
				i, strings.Join(data.FrameworkNames(), ", "))
		}
		for _, g := range globs {
			if !doublestar.ValidatePattern(g) {
				return nil, fmt.Errorf("generated-code glob for framework %q is invalid", name)
			}
		}
		f.generated = append(f.generated, generatedSet{framework: name, globs: globs})
	}
	for i, g := range cfg.Ignore.Glob {
		if !doublestar.ValidatePattern(g) {
			return nil, fmt.Errorf("ignore.glob[%d]: invalid glob pattern", i)
		}
		f.globs = append(f.globs, g)
	}
	for i, r := range cfg.Ignore.Regex {
		// ^(?:...) keeps Python re.match semantics: anchored at the start only.
		re, err := regexp.Compile("^(?:" + r + ")")
		if err != nil {
			return nil, fmt.Errorf("ignore.regex[%d]: pattern does not compile as RE2", i)
		}
		f.regexes = append(f.regexes, re)
	}
	return f, nil
}

// Include reports whether the file at the slash-separated path is reviewed.
func (f *Filter) Include(p string) bool {
	ok, _ := f.Explain(p)
	return ok
}

// Explain reports whether the path is included and, when it is not, the
// first matching reason. Check order: lockfile_or_minified, bad_extension,
// generated:<framework> (in configured order), ignore_glob, ignore_regex.
func (f *Filter) Explain(p string) (included bool, reason string) {
	if p == "" {
		return false, ReasonEmptyPath
	}
	base := path.Base(p)
	if _, ok := f.lockfiles[base]; ok {
		return false, ReasonLockfileOrMinified
	}
	lowerBase := strings.ToLower(base)
	for _, s := range data.MinifiedSuffixes {
		if strings.HasSuffix(lowerBase, s) {
			return false, ReasonLockfileOrMinified
		}
	}
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		if _, ok := f.badExt[strings.ToLower(base[i+1:])]; ok {
			return false, ReasonBadExtension
		}
	}
	for _, gs := range f.generated {
		if matchAny(gs.globs, p) {
			return false, ReasonGeneratedPrefix + gs.framework
		}
	}
	if matchAny(f.globs, p) {
		return false, ReasonIgnoreGlob
	}
	for _, re := range f.regexes {
		if re.MatchString(p) {
			return false, ReasonIgnoreRegex
		}
	}
	return true, ""
}

// matchAny reports whether p matches any of the globs.
func matchAny(globs []string, p string) bool {
	for _, g := range globs {
		if matchGlob(g, p) {
			return true
		}
	}
	return false
}

// matchGlob applies the one glob semantics used for ignore.glob and the
// generated-code table. A pattern without "/" matches the basename at any
// depth, which is the same as an implicit "**/" prefix, so PR-Agent patterns
// such as "*.min.js" also reach nested files. A pattern containing "/" keeps
// doublestar semantics against the full path, anchored at the repository root
// ("*" stays within one segment, "**" spans directories). doublestar treats a
// leading "**/" as zero or more directories, so "**/x.pb.go" covers a
// root-level "x.pb.go" too.
func matchGlob(g, p string) bool {
	if !strings.Contains(g, "/") {
		p = path.Base(p)
	}
	ok, err := doublestar.Match(g, p)
	return err == nil && ok
}
