#!/bin/sh
# Fail when two tracked paths, or two of their directory prefixes, are equal
# under case folding. Such paths cannot coexist on case-insensitive
# filesystems (macOS, Windows) and make the module zip invalid for the Go
# module proxy.
#
# Folding whole file paths is not enough: Foo/a.go and foo/b.go fold to
# different paths, yet Foo and foo still merge into one directory. So every
# prefix of every tracked path (each directory and the file itself) is listed
# once, and two distinct prefixes that fold to the same string collide.
#
# Usage: scripts/check-case-collisions.sh   (run from anywhere inside the repo)
set -eu

export LC_ALL=C

prefixes=$(git ls-files | awk '{
	n = split($0, part, "/")
	p = part[1]
	print p
	for (i = 2; i <= n; i++) {
		p = p "/" part[i]
		print p
	}
}' | sort -u)

dups=$(printf "%s\n" "$prefixes" | tr "A-Z" "a-z" | sort | uniq -d)
if [ -z "$dups" ]; then
	echo "ok: no case-insensitive path collisions"
	exit 0
fi

echo "check-case-collisions: tracked paths that collide when case is ignored:" >&2
# Print every distinct prefix whose folded form is duplicated.
printf "%s\n" "$prefixes" | while IFS= read -r path; do
	folded=$(printf "%s\n" "$path" | tr "A-Z" "a-z")
	if printf "%s\n" "$dups" | grep -qxF -- "$folded"; then
		echo "  $path" >&2
	fi
done
exit 1
