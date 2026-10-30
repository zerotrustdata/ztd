#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# Cloudflare R2 publication used by .github/workflows/release-binary.yml.
# Writes to the ztd-releases bucket that the website Worker binds as RELEASES
# (website/wrangler.jsonc), through the website's pinned wrangler.
#
#   upload <release-dir> <version>   Resume the verified set under v<version>/.
#                                    Existing objects must match byte for byte;
#                                    missing objects are written and read back.
#                                    ztd-binaries.json is written last: the website lists
#                                    a version on /releases/ only once its manifest exists,
#                                    so the manifest's presence means the set is complete.
#   promote <release-dir> <version>  Require every v<version>/ object to match the set,
#                                    then write latest.json and read it back.
#   check                            Prove the credentials can write, read back, and delete
#                                    an object under _preflight/ (a prefix the website
#                                    Worker never serves). Every release run, rehearsals
#                                    included, runs it before building.
#
# Remote storage authenticates with CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID.
# upload/promote accept an optional _rehearsal/<run>-<attempt> namespace.
# clean-rehearsal deletes only that isolated namespace's known release objects.
# ZTD_R2_PERSIST_TO=<dir> targets wrangler local storage for offline tests.

set -euo pipefail

BUCKET="ztd-releases"
MANIFEST="ztd-binaries.json"

die() {
	echo "release-r2: $*" >&2
	exit 1
}

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=lib/scratch.sh
. "$ROOT/scripts/lib/scratch.sh"
[ -x "$ROOT/website/node_modules/.bin/wrangler" ] || die "wrangler not installed; run npm ci in website/"
SCRATCH=$(ztd_scratch_dir "$ROOT" ztd-release-r2) || die "cannot create scratch directory"
PREFIX=""
PROBE_KEY=""
cleanup() {
	local status=$?
	trap - EXIT
	if [ -n "$PROBE_KEY" ]; then
		r2 delete "$BUCKET/$PROBE_KEY" >/dev/null || status=1
	fi
	rm -rf "$SCRATCH"
	exit "$status"
}
trap cleanup EXIT

if [ -n "${ZTD_R2_PERSIST_TO:-}" ]; then
	STORAGE=(--local --persist-to "$ZTD_R2_PERSIST_TO")
else
	[ -n "${CLOUDFLARE_API_TOKEN:-}" ] || die "CLOUDFLARE_API_TOKEN is not set"
	[ -n "${CLOUDFLARE_ACCOUNT_ID:-}" ] || die "CLOUDFLARE_ACCOUNT_ID is not set"
	STORAGE=(--remote)
fi

r2() {
	(cd "$ROOT/website" && node_modules/.bin/wrangler r2 object "$@" "${STORAGE[@]}")
}

remote_digest() {
	local digest
	if digest=$(r2 get "$BUCKET/$1" --pipe 2>"$SCRATCH/read-error" | sha256sum); then
		printf '%s\n' "${digest%% *}"
		return
	fi
	# Wrangler exposes missing keys only through this diagnostic. Other read
	# errors must never be interpreted as absence, especially authorization errors.
	if grep -qF 'The specified key does not exist' "$SCRATCH/read-error"; then
		return 3
	fi
	cat "$SCRATCH/read-error" >&2
	return 1
}

release_set() {
	local dir=$1 artifact
	[ -d "$dir" ] || die "release directory not found: $dir"
	dir=$(cd "$dir" && pwd)
	[ -f "$dir/$MANIFEST" ] || die "release set has no $MANIFEST: $dir"
	ARTIFACTS=()
	for artifact in "$dir"/*; do
		[ "${artifact##*/}" = "$MANIFEST" ] || ARTIFACTS+=("$artifact")
	done
	ARTIFACTS+=("$dir/$MANIFEST")
}

cmd_upload() {
	local version=$2 artifact key want got status manifest_present=false
	local missing=()
	release_set "$1"
	# Check the whole set before writing anything. A matching existing object
	# belongs to this upload; a mismatch is immutable and requires investigation.
	for artifact in "${ARTIFACTS[@]}"; do
		key="${PREFIX}v$version/$(basename "$artifact")"
		want=$(sha256sum "$artifact" | cut -d' ' -f1)
		if got=$(remote_digest "$key"); then
			[ "$got" = "$want" ] || die "$BUCKET/$key does not match the verified release set; refusing to overwrite"
			[ "${artifact##*/}" != "$MANIFEST" ] || manifest_present=true
		else
			status=$?
			[ "$status" -eq 3 ] || die "cannot read $BUCKET/$key"
			missing+=("$artifact")
		fi
	done
	if [ "$manifest_present" = true ] && [ "${#missing[@]}" -gt 0 ]; then
		die "published manifest has missing objects in $BUCKET/${PREFIX}v$version/"
	fi
	for artifact in "${missing[@]}"; do
		key="${PREFIX}v$version/$(basename "$artifact")"
		want=$(sha256sum "$artifact" | cut -d' ' -f1)
		r2 put "$BUCKET/$key" --file "$artifact" --content-type application/octet-stream >/dev/null
		got=$(remote_digest "$key")
		[ "$got" = "$want" ] || die "$BUCKET/$key does not match the verified release set"
	done
	echo "verified ${#ARTIFACTS[@]} objects in $BUCKET/${PREFIX}v$version/ (uploaded ${#missing[@]})"
}

cmd_promote() {
	local version=$2 artifact key pointer want got
	release_set "$1"
	for artifact in "${ARTIFACTS[@]}"; do
		key="${PREFIX}v$version/$(basename "$artifact")"
		got=$(remote_digest "$key") || die "$BUCKET/$key is missing; not promoting"
		[ "$got" = "$(sha256sum "$artifact" | cut -d' ' -f1)" ] || die "$BUCKET/$key does not match the release set; not promoting"
	done
	pointer="$SCRATCH/latest.json"
	printf '{"version": "%s"}\n' "$version" >"$pointer"
	want=$(sha256sum "$pointer" | cut -d' ' -f1)
	r2 put "$BUCKET/${PREFIX}latest.json" --file "$pointer" --content-type application/json >/dev/null
	got=$(remote_digest "${PREFIX}latest.json")
	[ "$got" = "$want" ] || die "$BUCKET/latest.json does not match the promoted version"
	echo "promoted $BUCKET/${PREFIX}latest.json to $version"
}

cmd_clean_rehearsal() {
	local version=$2 artifact status=0
	[ -n "$PREFIX" ] || die "cleanup requires an isolated rehearsal namespace"
	release_set "$1"
	for artifact in "${ARTIFACTS[@]}"; do
		r2 delete "$BUCKET/${PREFIX}v$version/${artifact##*/}" >/dev/null || status=1
	done
	r2 delete "$BUCKET/${PREFIX}latest.json" >/dev/null || status=1
	return "$status"
}

cmd_check() {
	local probe="$SCRATCH/probe.txt" want got
	PROBE_KEY="_preflight/${GITHUB_RUN_ID:-local}-$$-$(date +%s)"
	printf 'release credential check %s\n' "$PROBE_KEY" >"$probe"
	want=$(sha256sum "$probe" | cut -d' ' -f1)
	r2 put "$BUCKET/$PROBE_KEY" --file "$probe" --content-type text/plain >/dev/null \
		|| die "cannot write $BUCKET/$PROBE_KEY; check CLOUDFLARE_API_TOKEN has R2 object read/write on $BUCKET"
	got=$(remote_digest "$PROBE_KEY") || die "cannot read back $BUCKET/$PROBE_KEY"
	[ "$got" = "$want" ] || die "$BUCKET/$PROBE_KEY read back a different object"
	r2 delete "$BUCKET/$PROBE_KEY" >/dev/null || die "cannot delete $BUCKET/$PROBE_KEY"
	PROBE_KEY=""
	echo "R2 credentials can write, read, and delete in $BUCKET"
}

usage() {
	cat >&2 <<'EOF'
usage: release-r2.sh <subcommand> [args]
  upload <release-dir> <version> [_rehearsal/<run>-<attempt>]
  promote <release-dir> <version> [_rehearsal/<run>-<attempt>]
  clean-rehearsal <release-dir> <version> <_rehearsal/<run>-<attempt>>
  check
EOF
	exit 2
}

main() {
	local sub=${1:-}
	[ -n "$sub" ] || usage
	shift
	case "$sub" in
		check) [ $# -eq 0 ] || usage; cmd_check ;;
		upload|promote|clean-rehearsal)
			[ $# -eq 2 ] || [ $# -eq 3 ] || usage
			[[ "$2" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "invalid release version: $2"
			if [ -n "${3:-}" ]; then
				[[ "$3" =~ ^_rehearsal/[0-9]+-[0-9]+$ ]] || die "invalid rehearsal namespace: $3"
				PREFIX="$3/"
			fi
			case "$sub" in
				upload) cmd_upload "$1" "$2" ;;
				promote) cmd_promote "$1" "$2" ;;
				clean-rehearsal) cmd_clean_rehearsal "$1" "$2" ;;
			esac
			;;
		*) usage ;;
	esac
}

main "$@"
