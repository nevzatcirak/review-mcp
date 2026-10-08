#!/bin/sh
# Tests for scripts/release-notes.sh. Run with: sh scripts/release-notes_test.sh
set -eu

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/CHANGELOG.md" <<'EOF'
# Changelog

## [Unreleased]

## [1.1.0-rc.1] - Unreleased

- Parts.

## [1.0.0-rc.1] - 2026-01-02

### Added

- First tool surface.

## [1.0.0-rc.10] - 2026-01-03

- Tenth candidate.

## [0.9.0]

## [0.1.0] - 2025-12-01

- Old.

[1.0.0-rc.1]: https://example.com/compare/a...b
EOF

fail() { echo "FAIL: $1" >&2; exit 1; }

out=$(sh "$here/release-notes.sh" v1.0.0-rc.1 "$tmp/CHANGELOG.md")
want=$(printf '### Added\n\n- First tool surface.')
[ "$out" = "$want" ] || fail "rc.1 section mismatch: $out"

out=$(sh "$here/release-notes.sh" v1.0.0-rc.10 "$tmp/CHANGELOG.md")
[ "$out" = "- Tenth candidate." ] || fail "rc.10 must not match rc.1: $out"

# A section marked "- Unreleased" (until it is tagged) is found by its version.
out=$(sh "$here/release-notes.sh" v1.1.0-rc.1 "$tmp/CHANGELOG.md")
[ "$out" = "- Parts." ] || fail "unreleased-marked section mismatch: $out"

if sh "$here/release-notes.sh" v0.9.0 "$tmp/CHANGELOG.md" 2>/dev/null; then fail "empty section must fail"; fi
if sh "$here/release-notes.sh" v2.0.0 "$tmp/CHANGELOG.md" 2>/dev/null; then fail "missing section must fail"; fi
if sh "$here/release-notes.sh" v1.0.0 "$tmp/CHANGELOG.md" 2>/dev/null; then fail "v1.0.0 must not match rc sections"; fi
if sh "$here/release-notes.sh" v1.0.0-rc.1 "$tmp/missing.md" 2>/dev/null; then fail "missing file must fail"; fi

echo "release-notes tests passed"
