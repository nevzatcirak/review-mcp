# Contributing to review-mcp

Thank you for helping. This file is also what review-mcp itself reads as the
project's guidelines when it reviews pull requests to this repository
(`review.guidelines_paths`, see `docs/review.md`).

## Before you open a pull request

- Build, test and lint with Go 1.26 or newer and golangci-lint v2.14.0:
  `make build`, `make test` (`go test -race ./...`), `make lint`, `make fmt`.
- Run `make licenses-check` when you add or update a dependency.

## Conventions

- Commits follow Conventional Commits (`feat(scope): …`, `fix(scope): …`,
  `docs: …`), one logical change per commit, and every commit builds and
  passes the tests on its own.
- A change in behaviour comes with a test that fails without the change.
- Errors shown to users are fixed sentences: they never include tokens,
  secrets or raw server responses.
- Examples, fixtures and documentation use placeholder hosts such as
  `your-gitea.example` or `*.example.com`, never a real organisation's
  systems.
- review-mcp never builds or runs pull request code (decision R-1).
- Every exported helper must document the error cases it names in its doc comment.
- New user-facing behaviour is documented in `docs/` and in `CHANGELOG.md`.
