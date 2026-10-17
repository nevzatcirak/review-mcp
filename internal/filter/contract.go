package filter

import (
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/nevzatcirak/review-mcp/internal/filter/data"
)

// Contract tiers of a changed file of a related pull request (X-41, v2.3
// spec V-13a §1.3): inside a related pull request's share of the prompt,
// API descriptions come first, then API-surface files, then the rest.
const (
	// TierOther is every other file.
	TierOther = 0
	// TierDescription is an API description (.proto, OpenAPI, GraphQL, ...)
	// or a path a related.contract_globs glob matches.
	TierDescription = 1
	// TierSurface is a file on the API surface by its path (a route, a
	// controller, a DTO, a client, ...). Documentation files never are.
	TierSurface = 2
)

// ContractTier returns the tier of the slash-separated path p by the fixed
// table of internal/filter/data (contract.go): TierDescription for the
// extensions .proto, .graphql, .gql, .avsc, .thrift and .wsdl, for
// openapi*, swagger* and asyncapi* files with the extension .yaml, .yml or
// .json and for *.schema.json (the base name compared in lower case);
// TierSurface when one of the path's tokens is an API-surface word
// (contractTokens) and the file is no documentation (IsDocumentation);
// TierOther otherwise. Filter.ContractTier adds related.contract_globs.
func ContractTier(p string) int {
	p = strings.ReplaceAll(p, "\\", "/")
	base := strings.ToLower(path.Base(p))
	ext := strings.TrimPrefix(path.Ext(base), ".")
	if slices.Contains(data.ContractExtensions, ext) || strings.HasSuffix(base, data.ContractSchemaSuffix) {
		return TierDescription
	}
	if slices.Contains(data.ContractNameExtensions, ext) {
		for _, pre := range data.ContractNamePrefixes {
			if strings.HasPrefix(base, pre) {
				return TierDescription
			}
		}
	}
	if IsDocumentation(p) {
		return TierOther
	}
	for _, tok := range contractTokens(p) {
		if slices.Contains(data.APISurfaceTokens, tok) {
			return TierSurface
		}
	}
	return TierOther
}

// ContractTier is the package's ContractTier, with the globs of
// related.contract_globs (the operator's and the repository config's)
// making a path TierDescription. The globs match as ignore.glob does: a
// pattern without "/" matches the base name at any depth.
func (f *Filter) ContractTier(p string) int {
	if f != nil && matchAny(f.contractGlobs, p) {
		return TierDescription
	}
	return ContractTier(p)
}

// contractTokens splits a path into lower-case tokens: on "/", ".", "_" and
// "-", and inside each part on camelCase boundaries (a lower-case letter or
// a digit followed by an upper-case one, and the last upper-case letter of
// a run followed by a lower-case one: "APIClient" is "api", "client").
func contractTokens(p string) []string {
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '.' || r == '_' || r == '-' || r == '\\' })
	var out []string
	for _, part := range parts {
		rs := []rune(part)
		start := 0
		for i := 1; i < len(rs); i++ {
			prev, cur := rs[i-1], rs[i]
			next := rune(0)
			if i+1 < len(rs) {
				next = rs[i+1]
			}
			split := (unicode.IsLower(prev) || unicode.IsDigit(prev)) && unicode.IsUpper(cur) ||
				unicode.IsUpper(prev) && unicode.IsUpper(cur) && unicode.IsLower(next)
			if split {
				out = append(out, strings.ToLower(string(rs[start:i])))
				start = i
			}
		}
		out = append(out, strings.ToLower(string(rs[start:])))
	}
	return out
}
