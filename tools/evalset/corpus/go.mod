// The evaluation corpus is text for the reviewer to read; it is never built
// or run (R-1). This file makes tools/evalset/corpus a module of its own, so
// the review-mcp module (go build, go vet, go test and the linters on ./...)
// never compiles the Go sources of the cases. It is not part of any case.
module evalset.example/corpus

go 1.22
