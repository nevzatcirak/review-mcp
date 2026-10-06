#!/bin/sh
# Verify that the linux/amd64 binary built by "goreleaser release --snapshot"
# reports the snapshot version in its "version" output. Catches a broken
# -X ldflags variable path, which would silently leave the "dev" default.
#
# Usage: scripts/check-snapshot-version.sh [dist-dir]   (default: dist)
set -eu

dist=${1:-dist}

if [ ! -f "$dist/metadata.json" ]; then
	echo "check-snapshot-version: $dist/metadata.json not found" >&2
	exit 1
fi
# metadata.json holds {"project_name":...,"tag":...,"version":"<snapshot version>",...}.
version=$(sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p' "$dist/metadata.json" | head -n 1)
if [ -z "$version" ]; then
	echo "check-snapshot-version: no version in $dist/metadata.json" >&2
	exit 1
fi

bin=$(find "$dist" -type f -path '*linux_amd64*' -name review-mcp | head -n 1)
if [ -z "$bin" ]; then
	echo "check-snapshot-version: no linux/amd64 binary under $dist" >&2
	exit 1
fi

out=$("$bin" version)
echo "$out"
case $out in
*"$version"*) echo "ok: version output contains $version" ;;
*)
	echo "check-snapshot-version: version output does not contain $version" >&2
	exit 1
	;;
esac
