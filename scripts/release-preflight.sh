#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# Release preflight used by the binary release workflow before any build output
# is trusted. Validates the event/ref, the release tag format, and that the tag,
# VERSION file, and checked-out revision all agree.
#
#   release-preflight.sh --event <name> --ref <ref> --sha <sha> --publish <true|false>
#
# Prints key=value lines on stdout (append to $GITHUB_OUTPUT). Diagnostics go to
# stderr. Exits non-zero on any mismatch, so a failing preflight stops the run.
#
# Rules:
#   push                 ref must be a release tag; publish is always true.
#   workflow_dispatch    ref may be a release tag, or a branch for rehearsal.
#                        publish=true requires a tag ref.
#   tags                 v<VERSION>            (for example v1.0.0)
#   publish=true         requires a clean worktree and the release notes file
#                        for this version.

set -euo pipefail

die() {
	echo "release-preflight: $*" >&2
	exit 1
}

usage() {
	echo "usage: release-preflight.sh --event <name> --ref <ref> --sha <sha> --publish <true|false>" >&2
	exit 2
}

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

event="" ref="" sha="" publish=""
while [ $# -gt 0 ]; do
	case "$1" in
		--event) event=${2:-}; shift 2 ;;
		--ref) ref=${2:-}; shift 2 ;;
		--sha) sha=${2:-}; shift 2 ;;
		--publish) publish=${2:-}; shift 2 ;;
		*) usage ;;
	esac
done

[ -n "$event" ] || usage
case "$publish" in
	true|false|"") ;;
	*) die "publish must be true or false, got '$publish'" ;;
esac

# VERSION is the single source of truth.
version=$(tr -d '[:space:]' < VERSION)
version=${version#v}
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "VERSION must be X.Y.Z, got '$version'"

expected_tag="v${version}"

tag=""
case "$event" in
	push)
		[[ "$ref" == refs/tags/* ]] || die "push event must reference a tag, got '$ref'"
		tag=${ref#refs/tags/}
		publish=true
		;;
	workflow_dispatch)
		case "$ref" in
			refs/tags/*) tag=${ref#refs/tags/} ;;
			refs/heads/*) tag="" ;;
			*) die "unsupported dispatch ref '$ref'" ;;
		esac
		if [ "$publish" = "true" ] && [ -z "$tag" ]; then
			die "publish=true requires a release tag ref (got branch '$ref')"
		fi
		[ "${publish:-false}" = "true" ] || publish=false
		;;
	*)
		die "unsupported event '$event'"
		;;
esac

if [ -n "$tag" ]; then
	[ "$tag" = "$expected_tag" ] || die "tag '$tag' does not match VERSION (expected '$expected_tag')"

	# The checked-out commit must be the commit the tag names.
	head=$(git rev-parse HEAD)
	tag_commit=$(git rev-parse -q --verify "refs/tags/${tag}^{commit}") \
		|| die "tag '$tag' is not present in this checkout (use fetch-depth: 0)"
	[ "$tag_commit" = "$head" ] || die "checked-out revision $head does not match tag '$tag' ($tag_commit)"
	if [ -n "$sha" ] && [ "$sha" != "$head" ]; then
		die "GITHUB_SHA $sha does not match checked-out revision $head"
	fi
fi

if [ "$publish" = "true" ]; then
	if [ -n "$(git status --porcelain)" ]; then
		die "worktree is dirty; publishing requires a clean checkout"
	fi
	major_minor=$(echo "$version" | cut -d. -f1-2)
	notes="docs/release_notes/v${major_minor}.x/v${version}.md"
	[ -f "$notes" ] || die "release notes not found: $notes"
	echo "release_notes=${notes}"
fi

echo "version=${version}"
echo "version_major_minor=$(echo "$version" | cut -d. -f1-2)"
echo "tag=${tag}"
echo "publish=${publish:-false}"
echo "release-preflight: ok (event=${event}, tag=${tag:-<branch>}, publish=${publish:-false})" >&2
