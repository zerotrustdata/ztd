#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# One publication procedure for independent rehearsals and real releases.
# Rehearsals use a temporary GitHub draft and an isolated prefix in the real
# R2 bucket. They never publish a GitHub release or move the public pointer.
set -euo pipefail
shopt -s dotglob

die() { echo "release-publish: $*" >&2; exit 1; }
[ $# -eq 4 ] || die 'usage: release-publish.sh <rehearse|publish> <release-dir> <version> <revision>'
mode=$1 dir=$2 version=$3 revision=$4
case "$mode" in rehearse|publish) ;; *) die "invalid mode: $mode" ;; esac
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "invalid version: $version"
[[ "$revision" =~ ^[0-9a-f]{40}$ ]] || die "invalid revision: $revision"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
dir=$(cd "$dir" && pwd)
notes="$ROOT/docs/release_notes/v${version%.*}.x/v$version.md"
[ -s "$notes" ] || die "release notes not found: $notes"
scripts/release-artifacts.sh verify-set "$dir" "$version" "$revision"

tag="v$version"
namespace=()
tag_options=(--verify-tag)
if [ "$mode" = rehearse ]; then
	[[ "${GITHUB_RUN_ID:-}" =~ ^[0-9]+$ ]] || die 'rehearsal requires GITHUB_RUN_ID'
	[[ "${GITHUB_RUN_ATTEMPT:-}" =~ ^[0-9]+$ ]] || die 'rehearsal requires GITHUB_RUN_ATTEMPT'
	tag="rehearsal-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT"
	namespace=("_rehearsal/$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT")
	tag_options=()
else
	[ "$(git rev-parse "refs/tags/$tag^{commit}")" = "$revision" ] || die "tag $tag does not name $revision"
fi

# shellcheck source=lib/scratch.sh
. "$ROOT/scripts/lib/scratch.sh"
scratch=$(ztd_scratch_dir "$ROOT" ztd-release-publish) || die 'cannot create publication scratch directory'
draft_attempted=false
cleanup() {
	local status=$?
	trap - EXIT
	if [ "$mode" = rehearse ]; then
		if [ "$draft_attempted" = true ]; then
			gh release delete "$tag" --yes || status=1
			# Draft creation may create a temporary tag. Inspect exact refs so a
			# nonexistent tag is not confused with a failed cleanup request.
			local ref
			if ref=$(gh api "repos/{owner}/{repo}/git/matching-refs/tags/$tag" \
				--jq ".[] | select(.ref == \"refs/tags/$tag\") | .ref"); then
				if [ -n "$ref" ]; then
					gh api --method DELETE "repos/{owner}/{repo}/git/refs/tags/$tag" || status=1
				fi
			else
				status=1
			fi
		fi
		scripts/release-r2.sh clean-rehearsal "$dir" "$version" "${namespace[@]}" || status=1
	fi
	rm -rf "$scratch"
	exit "$status"
}
trap cleanup EXIT

# Listing distinguishes an absent release from authentication/transport errors.
# Never turn a failed lookup into permission to create or replace a release.
release=$(gh api --paginate 'repos/{owner}/{repo}/releases?per_page=100' |
	jq -sce --arg tag "$tag" '
		if length == 0 or any(.[]; type != "array") then error("invalid release list")
		else [.[][] | select(.tag_name == $tag)] end |
		if length == 0 then {}
		elif length == 1 then .[0] |
			if (.draft | type) == "boolean" and (.target_commitish | type) == "string" then .
			else error("invalid release record") end
		else error("duplicate release tag") end')
state=$(jq -r 'if .tag_name == null then "absent" elif .draft then "draft" else "public" end' <<<"$release")
if [ "$state" = absent ]; then
	draft_attempted=true
	gh release create "$tag" --target "$revision" --draft --title "ztd $tag" --notes-file "$notes" "${tag_options[@]}"
else
	[ "$(jq -r .target_commitish <<<"$release")" = "$revision" ] || die "existing release targets another revision: $tag"
	[ "$mode/$state" != rehearse/public ] || die 'rehearsal tag is already public'
	draft_attempted=true
fi

download_assets() {
	local file name count
	rm -rf "$scratch/download"
	mkdir "$scratch/download"
	count=$(gh release view "$tag" --json assets --jq '.assets | length')
	[[ "$count" =~ ^[0-9]+$ ]] || die 'invalid GitHub asset count'
	if [ "$count" -gt 0 ]; then
		gh release download "$tag" --dir "$scratch/download"
	fi
	for file in "$scratch/download"/*; do
		[ -e "$file" ] || continue
		name=${file##*/}
		if [ ! -f "$dir/$name" ] || ! cmp -s "$file" "$dir/$name"; then
			die "GitHub asset does not match verified release set: $name"
		fi
	done
}

# Resume only missing assets. Existing bytes are checked before any write, and
# public releases are never changed. A lost upload reply is safe to rerun.
download_assets
for file in "$dir"/*; do
	[ -f "$file" ] || die "non-file in release set: $file"
	if [ ! -f "$scratch/download/${file##*/}" ]; then
		[ "$state" != public ] || die "public release is missing asset: ${file##*/}"
		gh release upload "$tag" "$file"
	fi
done
download_assets
for file in "$dir"/*; do
	cmp -s "$file" "$scratch/download/${file##*/}" || die "GitHub asset read-back failed: ${file##*/}"
done

scripts/release-r2.sh upload "$dir" "$version" "${namespace[@]}"
if [ "$mode" = publish ]; then
	if [ "$state" != public ]; then
		gh release edit "$tag" --draft=false
	fi
	[ "$(gh release view "$tag" --json isDraft --jq .isDraft)" = false ] || die 'GitHub release is still a draft'
else
	[ "$(gh release view "$tag" --json isDraft --jq .isDraft)" = true ] || die 'rehearsal release must remain a draft'
fi
scripts/release-r2.sh promote "$dir" "$version" "${namespace[@]}"
echo "release-publish: $mode completed ($tag, revision $revision)"
