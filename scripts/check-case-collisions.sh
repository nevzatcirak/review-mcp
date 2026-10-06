#!/bin/sh
# Fail when two tracked paths are equal under case folding. Such paths cannot
# coexist on case-insensitive filesystems (macOS, Windows) and make the module
# zip invalid for the Go module proxy.
#
# Folding the full path is sufficient: every path component folds the same way.
#
# Usage: scripts/check-case-collisions.sh   (run from anywhere inside the repo)
set -eu

export LC_ALL=C

dups=$(git ls-files | tr "A-Z" "a-z" | sort | uniq -d)
if [ -z "$dups" ]; then
	echo "ok: no case-insensitive path collisions"
	exit 0
fi

echo "check-case-collisions: tracked paths that collide when case is ignored:" >&2
# Print every tracked path whose folded form is duplicated.
git ls-files | while IFS= read -r path; do
	folded=$(printf "%s\n" "$path" | tr "A-Z" "a-z")
	if printf "%s\n" "$dups" | grep -qxF -- "$folded"; then
		echo "  $path" >&2
	fi
done
exit 1
