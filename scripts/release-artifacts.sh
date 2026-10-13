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
		[ "$(awk '{print $2}' "$dir/checksums-sha256.txt" | sort)" = "$(printf '%s\n' "$names" | sort)" ] \
			|| die "checksums-sha256.txt does not list exactly the expected binaries"
		(cd "$dir" && sha256sum -c --status checksums-sha256.txt) || die "checksums-sha256.txt does not verify"
	fi

	# Build metadata is checked by running the binary only when the host can execute it.
	# Read the typed `version --json` contract; platform is stamped as GOOS/GOARCH.
	if [ "$(uname -s)/$(uname -m)" = "Linux/x86_64" ]; then
		local out
		out=$("$dir/ztd-linux-amd64" version --json --fips) || die "ztd-linux-amd64 is not in FIPS 140-3 approved mode"
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

cmd_sign() {
	local dir=$1 names name
	need cosign
	names=$(expected_binaries)
	for name in $names; do
		[ -s "$dir/$name" ] || die "missing binary: $name"
		# Keyless signing: the Fulcio certificate is embedded in the bundle.
		cosign sign-blob --yes "$dir/$name" \
			--bundle "$dir/$name.sigstore.json"
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

cmd_stage() {
	local dir=$1 out=$2 names name
	names=$(expected_binaries)
	rm -rf "$out"
	mkdir -p "$out"
	for name in $names; do
		cp "$dir/$name" "$dir/$name.sha256" "$dir/$name.sigstore.json" "$out/"
	done
	cp "$dir/checksums-sha256.txt" "$dir/ztd-binaries.json" "$out/"
	set -- "$out"/*
	echo "staged $# files in $out"
}

usage() {
	cat >&2 <<'EOF'
usage: release-artifacts.sh <subcommand> [args]
  list-binaries
  checksums <bin-dir>
  verify <bin-dir> <version> <revision>
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
		sign) [ $# -eq 1 ] || usage; cmd_sign "$1" ;;
		verify-signatures) [ $# -eq 1 ] || usage; cmd_verify_signatures "$1" ;;
		stage) [ $# -eq 2 ] || usage; cmd_stage "$1" "$2" ;;
		*) usage ;;
	esac
}

main "$@"
