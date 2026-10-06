# Cutting a release

A release is cut by pushing a version tag (`v*`); `.github/workflows/release.yml`
does the rest:

1. The `release` job runs the license check and the tests, extracts the tag's
   section from `CHANGELOG.md` (the release fails if it is empty), runs
   GoReleaser to build and publish the GitHub release, attests build provenance
   and uploads `dist/` for the next job. A tag with a pre-release suffix (for
   example `v1.0.0-rc.1`) is published as a GitHub pre-release.
2. The `npm-publish` job runs after the `release` job succeeded. It builds the
   npm packages from the tag (`npm/scripts/build-packages.mjs`) and publishes
   them with `npm/scripts/publish-packages.mjs`: the six platform packages
   first, then the main package. A pre-release version is published under the
   dist-tag `next`, any other under `latest`. A version that already exists on
   the registry stops the job; nothing is republished.

## npm trusted publishing

npm publishing uses trusted publishing (GitHub OIDC), not a token. Each of the
seven packages has a trusted publisher configured on npmjs.com:

- user: `nevzatcirak`
- repository: `review-mcp`
- workflow: `release.yml`

The packages are:

- `@nevzatcirak/review-mcp`
- `@nevzatcirak/review-mcp-linux-x64`
- `@nevzatcirak/review-mcp-linux-arm64`
- `@nevzatcirak/review-mcp-darwin-x64`
- `@nevzatcirak/review-mcp-darwin-arm64`
- `@nevzatcirak/review-mcp-win32-x64`
- `@nevzatcirak/review-mcp-win32-arm64`

The job installs a pinned npm (11.21.0; trusted publishing needs 11.5.1 or
later) and publishes with `--provenance`. The `NPM_TOKEN` repository secret is
no longer used and can be deleted.
