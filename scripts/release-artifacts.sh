#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# Release artifact helpers used by .github/workflows/release-binary.yml.
# The expected binary set comes from the ztdbinaries catalog (the same source
# the Makefile build-all target uses), so nothing here hardcodes platforms.
#
#   list-binaries                          Print expected binary filenames.
#   checksums <bin-dir>                    Write checksums-sha256.txt for expected binaries.
#   verify <bin-dir> <version> <revision>  Require the complete, unstale binary set, matching
#                                          .sha256 files, manifest digests, and build metadata.
#   sign <bin-dir>                         cosign keyless sign-blob per binary (.sigstore.json bundle),
#                                          then verify each bundle against the workflow identity.
#   verify-signatures <bin-dir>            Verify every .sigstore.json bundle against the identity.
#   stage <bin-dir> <out-dir>              Copy exactly the publishable file set into out-dir.
#
# Signer identity for verify-signatures comes from GITHUB_WORKFLOW_REF inside
# Actions, or ZTD_COSIGN_IDENTITY (exact certificate identity) elsewhere.
#
# Public verification (documented contract):
#   cosign verify-blob \
#     --bundle ztd-linux-amd64.sigstore.json \
#     --certificate-identity 'https://github.com/zerotrustdata/ztd/.github/workflows/release-binary.yml@refs/tags/v1.0.0' \
#     --certificate-oidc-issuer https://token.actions.githubusercontent.com \
#     ztd-linux-amd64

set -euo pipefail

OIDC_ISSUER="https://token.actions.githubusercontent.com"

die() {
	echo "release-artifacts: $*" >&2
	exit 1
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "required tool not found: $1"
}

repo_root() {
	cd "$(dirname "$0")/.." && pwd
}

ROOT="$(repo_root)"
cd "$ROOT"
# shellcheck source=lib/scratch.sh
. "$ROOT/scripts/lib/scratch.sh"

expected_binaries() {
	local targets token os arch name
	targets=$(go run ./internal/tools/ztdbinaries -list-targets) || die "cannot read ztdbinaries catalog"
	[ -n "$(echo "$targets" | tr -d '[:space:]')" ] || die "ztdbinaries catalog lists no targets"
	for token in $targets; do
		os=${token%/*}
		arch=${token#*/}
		name="ztd-${os}-${arch}"
		if [ "$os" = "windows" ]; then
			name="${name}.exe"
		fi
		printf '%s\n' "$name"
	done
}

cmd_list_binaries() {
	expected_binaries
}

expected_files() {
	local names name
	names=$(expected_binaries)
	for name in $names; do
		printf '%s\n' "$name" "$name.sha256" "$name.sigstore.json"
	done
	printf '%s\n' checksums-sha256.txt ztd-binaries.json
}

cmd_checksums() {
	local dir=$1 names
	need sha256sum
	names=$(expected_binaries)
	(
		cd "$dir"
		for n in $names; do [ -s "$n" ] || die "missing binary: $n"; done
		# shellcheck disable=SC2086
		sha256sum $names > checksums-sha256.txt
	)
	echo "wrote $dir/checksums-sha256.txt"
}

cmd_verify() {
	local dir=$1 version=$2 revision=$3
	local manifest="$dir/ztd-binaries.json" names name want got count_expected count_manifest
	need sha256sum
	need jq
	[ -d "$dir" ] || die "binary directory not found: $dir"
	[ -s "$manifest" ] || die "missing manifest: $manifest"

	[ "$(jq -r '.schema_version' "$manifest")" = "1" ] || die "manifest schema_version is not 1"
	# The Makefile VERSION file carries the v prefix; the manifest and `ztd version` use it.
	[ "$(jq -r '.version' "$manifest")" = "v${version}" ] || die "manifest version does not match v${version}"
	[ "$(jq -r '.source_revision' "$manifest")" = "$revision" ] || die "manifest source_revision does not match $revision"

	names=$(expected_binaries)
	count_expected=$(printf '%s\n' "$names" | wc -l)
	count_manifest=$(jq '.targets | length' "$manifest")
	[ "$count_expected" = "$count_manifest" ] || die "manifest lists $count_manifest targets, catalog expects $count_expected"

	for name in $names; do
		[ -s "$dir/$name" ] || die "missing or empty binary: $name"
		[ -s "$dir/$name.sha256" ] || die "missing checksum file: $name.sha256"
		(cd "$dir" && sha256sum -c --status "$name.sha256") || die "checksum mismatch: $name"
		want=$(jq -r --arg n "$name" '.targets[] | select(.filename == $n) | .sha256' "$manifest")
		got=$(sha256sum "$dir/$name" | cut -d' ' -f1)
		if [ -z "$want" ] || [ "$want" != "$got" ]; then
			die "manifest digest mismatch: $name"
		fi
	done

	# Reject stray binaries (for example a stale artifact from an earlier build).
	for path in "$dir"/ztd-linux-* "$dir"/ztd-darwin-* "$dir"/ztd-windows-*; do
		[ -e "$path" ] || continue
		name=$(basename "$path")
		case "$name" in
			*.sha256|*.sigstore.json) continue ;;
		esac
		grep -qxF "$name" <<<"$names" || die "unexpected binary in release set: $name"
	done

	if [ -s "$dir/checksums-sha256.txt" ]; then
		[ "$(awk '{sub(/^\*/, "", $2); print $2}' "$dir/checksums-sha256.txt" | sort)" = "$(printf '%s\n' "$names" | sort)" ] \
			|| die "checksums-sha256.txt does not list exactly the expected binaries"
		(cd "$dir" && sha256sum -c --status checksums-sha256.txt) || die "checksums-sha256.txt does not verify"
	fi

	# Build metadata is checked by running the binary only when the host can execute it.
	# Read the typed `version --json` contract; platform is stamped as GOOS/GOARCH.
	# Run a private executable copy (digest verified above): artifact transfer
	# drops the executable bit, and verification must not modify the release set.
	if [ "$(uname -s)/$(uname -m)" = "Linux/x86_64" ]; then
		local out scratch exe
		scratch=$(ztd_scratch_dir "$ROOT" ztd-release-verify) || die "cannot create scratch dir"
		exe="$scratch/ztd-linux-amd64"
		install -m 0700 "$dir/ztd-linux-amd64" "$exe"
		out=$("$exe" version --json --fips) || { rm -rf "$scratch"; die "ztd-linux-amd64 version --json --fips failed (FIPS 140-3 approved mode inactive or binary not executable)"; }
		rm -rf "$scratch"
		[ "$(jq -r '.version' <<<"$out")" = "v${version}" ] || die "ztd-linux-amd64 does not report version v${version}"
		[ "$(jq -r '.platform' <<<"$out")" = "linux/amd64" ] || die "ztd-linux-amd64 does not report platform linux/amd64"
		[ "$(jq -r '.source_revision' <<<"$out")" = "$revision" ] || die "ztd-linux-amd64 does not report source revision ${revision}"
		[ "$(jq -r '.source_tree_modified' <<<"$out")" = "false" ] || die "ztd-linux-amd64 was not built from a clean, VCS-stamped source tree"
		[ "$(jq -r '.fips140.enabled' <<<"$out")" = "true" ] || die "ztd-linux-amd64 is not in FIPS 140-3 approved mode"
		echo "host binary metadata verified (linux/amd64, FIPS approved mode)"
	else
		echo "host is $(uname -s)/$(uname -m); runtime metadata checks skipped for non-host binaries"
	fi
	echo "release set verified: $count_expected binaries in $dir"
}

identity() {
	if [ -n "${ZTD_COSIGN_IDENTITY:-}" ]; then
		echo "$ZTD_COSIGN_IDENTITY"
		return
	fi
	[ -n "${GITHUB_WORKFLOW_REF:-}" ] || die "set GITHUB_WORKFLOW_REF or ZTD_COSIGN_IDENTITY"
	echo "https://github.com/${GITHUB_WORKFLOW_REF}"
}

sign_binary() (
	local binary=$1 id=$2 scratch bundle attempt
	scratch=$(ztd_scratch_dir "$ROOT" ztd-release-sign) || die "cannot create signing scratch directory"
	trap 'rm -rf "$scratch"' EXIT
	bundle="$scratch/bundle.sigstore.json"
	# A new process generates a new ephemeral key and certificate. Retrying only
	# the failed Rekor request can produce HTTP 409 after a lost successful reply.
	# Never publish an old or partial bundle left by an unsuccessful attempt.
	rm -f "$binary.sigstore.json"
	for attempt in 1 2 3; do
		rm -f "$bundle"
		if cosign sign-blob --yes --timeout 2m "$binary" --bundle "$bundle"; then
			[ -s "$bundle" ] || die "signing produced no signature bundle: $binary"
			cosign verify-blob --bundle "$bundle" \
				--certificate-identity "$id" \
				--certificate-oidc-issuer "$OIDC_ISSUER" \
				"$binary" >/dev/null || die "signature verification failed: $binary"
			mv "$bundle" "$binary.sigstore.json"
			return
		fi
		echo "release-artifacts: signing $binary failed (attempt $attempt/3)" >&2
		[ "$attempt" -eq 3 ] || sleep "$((attempt * 5))"
	done
	die "signing failed after 3 attempts: $binary"
)

cmd_sign() {
	local dir=$1 names name id
	need cosign
	id=$(identity)
	names=$(expected_binaries)
	for name in $names; do
		[ -s "$dir/$name" ] || die "missing binary: $name"
		sign_binary "$dir/$name" "$id"
	done
	cmd_verify_signatures "$dir"
}

cmd_verify_signatures() {
	local dir=$1 names name id
	need cosign
	id=$(identity)
	names=$(expected_binaries)
	for name in $names; do
		[ -s "$dir/$name.sigstore.json" ] || die "missing signature bundle: $name.sigstore.json"
		cosign verify-blob \
			--bundle "$dir/$name.sigstore.json" \
			--certificate-identity "$id" \
			--certificate-oidc-issuer "$OIDC_ISSUER" \
			"$dir/$name" >/dev/null || die "signature verification failed: $name"
	done
	echo "verified signature bundles for all binaries (identity: $id)"
}

cmd_verify_set() {
	local dir=$1 expected actual file
	expected=$(expected_files | sort)
	actual=$(
		shopt -s dotglob
		for file in "$dir"/*; do
			[ -f "$file" ] && [ ! -L "$file" ] || die "non-regular file in release set: $file"
			printf '%s\n' "${file##*/}"
		done | sort
	)
	[ "$actual" = "$expected" ] || die "release set does not contain exactly the catalog artifacts: $dir"
	cmd_verify "$@"
	cmd_verify_signatures "$dir"
}

cmd_stage() {
	local dir=$1 out=$2 files name
	files=$(expected_files)
	rm -rf "$out"
	mkdir -p "$out"
	for name in $files; do
		cp "$dir/$name" "$out/"
	done
	set -- "$out"/*
	echo "staged $# files in $out"
}

usage() {
	cat >&2 <<'EOF'
usage: release-artifacts.sh <subcommand> [args]
  list-binaries
  checksums <bin-dir>
  verify <bin-dir> <version> <revision>
  verify-set <release-dir> <version> <revision>
  sign <bin-dir>
  verify-signatures <bin-dir>
  stage <bin-dir> <out-dir>
EOF
	exit 2
}

main() {
	local sub=${1:-}
	[ -n "$sub" ] || usage
	shift
	case "$sub" in
		list-binaries) cmd_list_binaries ;;
		checksums) [ $# -eq 1 ] || usage; cmd_checksums "$1" ;;
		verify) [ $# -eq 3 ] || usage; cmd_verify "$1" "$2" "$3" ;;
		verify-set) [ $# -eq 3 ] || usage; cmd_verify_set "$1" "$2" "$3" ;;
		sign) [ $# -eq 1 ] || usage; cmd_sign "$1" ;;
		verify-signatures) [ $# -eq 1 ] || usage; cmd_verify_signatures "$1" ;;
		stage) [ $# -eq 2 ] || usage; cmd_stage "$1" "$2" ;;
		*) usage ;;
	esac
}

main "$@"
