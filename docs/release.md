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
   the registry stops the job; nothing is republished. After the last publish
   the job looks every `<name>@<version>` up again with `npm view`, retrying
   for up to 30 minutes (10 s, 20 s, 40 s, 80 s, 150 s, then 300 s per wait);
   a version that is still missing, or reported as staged, fails the job.

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

## If a platform package is missing after release

The `npm-publish` job fails with "is not fully available on the registry" when,
after publishing, a package version is still not visible on the registry after
30 minutes of retries, or when `npm publish` reported it as staged. The message
names the packages: "Missing" (the registry answered 404 or nothing), "Staged"
(npm said the version was staged) or "Not verified" (the lookup itself kept
failing, for example a network error, so nothing is known about that version).

What to do:

1. Check the package page on npmjs.com for each named package.
   - A staged publish waits for a maintainer to approve it (`npm stage list`,
     then `npm stage approve <stage-id>`, which asks for 2FA).
   - A package-level setting may require 2FA or staging for publishes; the
     package settings page shows it.
2. Wait. The registry can lag: on `v1.0.0-rc.3` the publish job succeeded, but
   `@nevzatcirak/review-mcp-darwin-x64` appeared only about 25 minutes later,
   far beyond the former 5-minute window. The 30-minute window covers that
   observed lag of about 25 minutes. If it still runs out, run
   `npm view <name>@<version> version` again later.
3. Never republish the same version. npm does not accept it, and a second
   attempt can hide the real cause. Either wait or approve the staged publish,
   or, if the version cannot be completed, cut the next release candidate (for
   example `v1.0.0-rc.5`) with a new tag.
