package data

// The contract-file table of related pull requests (X-41, v2.3 spec V-13a
// §1.3). It is review-mcp's own; nothing here is adapted from PR-Agent.

// ContractExtensions are the extensions of API descriptions (tier 1), lower
// case, without the dot.
var ContractExtensions = []string{"proto", "graphql", "gql", "avsc", "thrift", "wsdl"}

// ContractNamePrefixes start the base name of an API description file
// (tier 1) whose extension is one of ContractNameExtensions: openapi*.yaml,
// swagger*.json, asyncapi*.yml and so on, compared in lower case.
var ContractNamePrefixes = []string{"openapi", "swagger", "asyncapi"}

// ContractNameExtensions are the extensions of ContractNamePrefixes files.
var ContractNameExtensions = []string{"yaml", "yml", "json"}

// ContractSchemaSuffix ends the base name of a JSON Schema file (tier 1),
// compared in lower case.
const ContractSchemaSuffix = ".schema.json"

// APISurfaceTokens are the path tokens of API-surface files (tier 2): a
// path one of whose tokens (split on "/", ".", "_", "-" and camelCase
// boundaries, lower case) is one of them.
var APISurfaceTokens = []string{
	"api", "route", "routes", "router", "controller", "controllers", "handler", "handlers",
	"endpoint", "endpoints", "dto", "dtos", "model", "models", "schema", "schemas",
	"client", "clients", "contract", "contracts",
}
