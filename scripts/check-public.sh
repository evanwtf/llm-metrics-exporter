#!/bin/sh
# Refuse text that names the private network. This repo is public (AGENTS.md):
# no private addresses, no private host names, no home-directory paths.
#
#   scripts/check-public.sh [file ...]    default: every tracked file
#
# Names that are private but look ordinary (a host called "bigbox") go in
# .public-denylist, one per line, which is git-ignored and never committed.
# PUBLIC_DENYLIST overrides its path. Exit 1 when anything matches.
#
# POSIX sh and grep only, so the pre-commit hook works from a bare `git commit`.
set -u

denylist=${PUBLIC_DENYLIST:-.public-denylist}

# RFC 1918 IPv4 addresses, private DNS suffixes, and home directories.
octet='(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])'
patterns="(^|[^0-9.])(10\.$octet|172\.(1[6-9]|2[0-9]|3[01])|192\.168)\.$octet\.$octet([^0-9]|$)
[A-Za-z0-9-]+\.(internal|lan|home\.arpa|localdomain)([^A-Za-z0-9-]|$)
/(Users|home)/[a-z][a-z0-9_-]*/"

if [ "$#" -eq 0 ]; then
	set -- $(git ls-files)
fi

status=0
for f in "$@"; do
	[ -f "$f" ] || continue
	case "$f" in
	# This script and its test name the patterns on purpose.
	scripts/check-public.sh | scripts/checkpublic/*) continue ;;
	esac
	if printf '%s\n' "$patterns" | grep -En -f - "$f" /dev/null; then
		status=1
	fi
	if [ -s "$denylist" ]; then
		if grep -v -e '^#' -e '^[[:space:]]*$' "$denylist" | grep -Fin -f - "$f" /dev/null; then
			status=1
		fi
	fi
done
if [ "$status" -ne 0 ]; then
	echo "check-public: the lines above name the private network; this repo is public" >&2
fi
exit "$status"
