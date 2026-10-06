#!/bin/sh
# Print the CHANGELOG.md section for a release tag.
#
# Usage: scripts/release-notes.sh <tag> [changelog-file]
#
# <tag> is a git tag such as v1.0.0-rc.1; the leading "v" is dropped. The
# section starts at a level-2 heading "## [1.0.0-rc.1]" (Keep a Changelog) or
# "## 1.0.0-rc.1", optionally followed by " - <date>", and ends before the next
# level-2 heading. The heading itself is not printed. Link-reference lines at
# the end of the file ("[1.0.0]: https://...") are not part of any section.
#
# Fails (exit 1) when the file or the section is missing or the section is
# empty, so that a release can never go out with empty notes.
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
	echo "usage: $0 <tag> [changelog-file]" >&2
	exit 2
fi

tag=$1
file=${2:-CHANGELOG.md}
version=${tag#v}

if [ ! -f "$file" ]; then
	echo "release-notes: $file not found" >&2
	exit 1
fi

notes=$(awk -v ver="$version" '
	/^## / {
		if (found) { exit }
		h = $0
		sub(/^## +/, "", h)
		sub(/^\[/, "", h)
		n = length(ver)
		if (substr(h, 1, n) == ver) {
			rest = substr(h, n + 1)
			if (rest == "" || rest ~ /^(\]| )/) { found = 1 }
		}
		next
	}
	found && /^\[[^]]+\]: / { next }
	found { print }
' "$file")

# Trim leading and trailing blank lines.
notes=$(printf '%s\n' "$notes" | sed -e '/./,$!d' | sed -e :a -e '/^\n*$/{$d;N;ba' -e '}')

if [ -z "$notes" ]; then
	echo "release-notes: no non-empty section for version $version in $file" >&2
	exit 1
fi
printf '%s\n' "$notes"
