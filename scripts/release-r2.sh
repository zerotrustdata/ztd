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
#   upload <release-dir> <version>   Write the verified set under v<version>/. Refuses
#                                    any key that already exists (versioned sets are
#                                    immutable) and reads every object back by SHA-256.
#                                    ztd-binaries.json is written last: the website lists
#                                    a version on /releases/ only once its manifest exists,
#                                    so the manifest's presence means the set is complete.
#   promote <release-dir> <version>  Require every v<version>/ object to match the set,
#                                    then write latest.json and read it back.
#
# Remote storage authenticates with CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID.
# ZTD_R2_PERSIST_TO=<dir> targets wrangler local storage instead (release rehearsal).

set -euo pipefail

BUCKET="ztd-releases"
MANIFEST="ztd-binaries.json"

die() {
	echo "release-r2: $*" >&2
	exit 1
}

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
[ -x "$ROOT/website/node_modules/.bin/wrangler" ] || die "wrangler not installed; run npm ci in website/"

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
	r2 get "$BUCKET/$1" --pipe | sha256sum | cut -d' ' -f1
}

require_absent() {
	local err
	if err=$(r2 get "$BUCKET/$1" --pipe 2>&1 >/dev/null); then
		die "$BUCKET/$1 already exists; versioned release sets are immutable"
	fi
	grep -qF 'The specified key does not exist' <<<"$err" || die "cannot confirm $BUCKET/$1 is absent: $err"
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
	local version=$2 artifact key want got
	release_set "$1"
	for artifact in "${ARTIFACTS[@]}"; do
		require_absent "v$version/$(basename "$artifact")"
	done
	for artifact in "${ARTIFACTS[@]}"; do
		key="v$version/$(basename "$artifact")"
		want=$(sha256sum "$artifact" | cut -d' ' -f1)
		r2 put "$BUCKET/$key" --file "$artifact" --content-type application/octet-stream >/dev/null
		got=$(remote_digest "$key")
		[ "$got" = "$want" ] || die "$BUCKET/$key does not match the verified release set"
	done
	echo "uploaded ${#ARTIFACTS[@]} objects to $BUCKET/v$version/"
}

cmd_promote() {
	local version=$2 artifact key pointer want got
	release_set "$1"
	for artifact in "${ARTIFACTS[@]}"; do
		key="v$version/$(basename "$artifact")"
		got=$(remote_digest "$key") || die "$BUCKET/$key is missing; not promoting"
		[ "$got" = "$(sha256sum "$artifact" | cut -d' ' -f1)" ] || die "$BUCKET/$key does not match the release set; not promoting"
	done
	pointer=$(mktemp)
	printf '{"version": "%s"}\n' "$version" >"$pointer"
	want=$(sha256sum "$pointer" | cut -d' ' -f1)
	r2 put "$BUCKET/latest.json" --file "$pointer" --content-type application/json >/dev/null
	rm -f "$pointer"
	got=$(remote_digest latest.json)
	[ "$got" = "$want" ] || die "$BUCKET/latest.json does not match the promoted version"
	echo "promoted $BUCKET/latest.json to $version"
}

usage() {
	cat >&2 <<'EOF'
usage: release-r2.sh <subcommand> [args]
  upload <release-dir> <version>
  promote <release-dir> <version>
EOF
	exit 2
}

main() {
	local sub=${1:-}
	[ -n "$sub" ] || usage
	shift
	case "$sub" in
		upload) [ $# -eq 2 ] || usage; cmd_upload "$1" "$2" ;;
		promote) [ $# -eq 2 ] || usage; cmd_promote "$1" "$2" ;;
		*) usage ;;
	esac
}

main "$@"
