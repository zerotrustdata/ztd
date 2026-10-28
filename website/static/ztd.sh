#!/bin/sh
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# Installs the ztd binary for this host as ./ztd (or $ZTD_INSTALL_DIR/ztd when that
# directory is set). The version comes from https://zerotrustdata.io/latest.json, or from
# ZTD_VERSION when that is set; the binary and its checksum are then both fetched from that
# one pinned version. Set ZTD_OVERWRITE=1 to replace an existing regular file at that
# path; symbolic links and directories are never replaced. Requires curl, install,
# mktemp, awk, tr, and either sha256sum or shasum. Never requires root and never
# invokes sudo. On Windows, use ztd.ps1 instead.
#
# After the checksum matches, the signature bundle is downloaded. When cosign is
# on PATH the bundle is verified against the release workflow identity for that
# version. ZTD_REQUIRE_SIGSTORE=1 refuses the install when cosign is missing or
# the bundle does not verify. On macOS, com.apple.quarantine is removed only
# after that verification.

set -eu

fail() {
  echo "ztd install: $*" >&2
  exit 1
}

tmp_dir=""
cleanup() {
  if [ -n "$tmp_dir" ]; then
    rm -rf "$tmp_dir"
  fi
}

# Accepts X.Y.Z with an optional prerelease suffix. The candidate is passed through ENVIRON so that
# embedded newlines cannot satisfy the anchored pattern.
valid_version() {
  ZTD_CANDIDATE="$1" awk 'BEGIN { v = ENVIRON["ZTD_CANDIDATE"]; exit !(v ~ /^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$/) }'
}

# The whole install runs from main, which is called on the last line, so a truncated download never executes a
# partial script when piped into sh.
main() {
  for required in curl install mktemp awk tr; do
    command -v "$required" >/dev/null 2>&1 || fail "required command not found: $required"
  done
  if command -v sha256sum >/dev/null 2>&1; then
    sha256_digest() { sha256sum "$1"; }
  elif command -v shasum >/dev/null 2>&1; then
    sha256_digest() { shasum -a 256 "$1"; }
  else
    fail "no SHA-256 tool found (need sha256sum or shasum)"
  fi

  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$os" in
    linux|darwin) ;;
    mingw*|msys*|cygwin*) fail "unsupported OS: $os; on Windows run ztd.ps1 in PowerShell instead" ;;
    *) fail "unsupported OS: $os" ;;
  esac
  case "$arch" in
    x86_64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    i386|i686) arch=386 ;;
    *) fail "unsupported architecture: $arch" ;;
  esac
  case "$os-$arch" in
    linux-amd64|linux-arm64|linux-386|darwin-amd64|darwin-arm64) ;;
    *) fail "no published ztd build for $os/$arch" ;;
  esac

  install_dir="${ZTD_INSTALL_DIR:-.}"
  [ -d "$install_dir" ] || fail "install directory not found: $install_dir"
  [ -w "$install_dir" ] || fail "install directory is not writable: $install_dir (no sudo is attempted; choose a writable ZTD_INSTALL_DIR)"
  target="$install_dir/ztd"
  if [ -L "$target" ]; then
    fail "$target is a symbolic link; remove it first (the installer never writes through links)"
  fi
  if [ -d "$target" ]; then
    fail "$target is a directory; refusing to replace it"
  fi
  replacing=
  if [ -e "$target" ]; then
    [ "${ZTD_OVERWRITE:-}" = 1 ] || fail "$target already exists; set ZTD_OVERWRITE=1 to replace it"
    replacing=1
  fi

  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  tmp_dir=$(mktemp -d) || fail "could not create a temporary directory"

  base_url="https://zerotrustdata.io"
  if [ -n "${ZTD_VERSION:-}" ]; then
    version="$ZTD_VERSION"
    valid_version "$version" || fail "invalid ZTD_VERSION: $version (expected X.Y.Z or X.Y.Z-prerelease)"
  else
    pointer_path="$tmp_dir/latest.json"
    curl -fsSL "$base_url/latest.json" -o "$pointer_path" \
      || fail "download failed: $base_url/latest.json"
    version=$(awk -F'"' '{ for (i = 1; i < NF; i++) if ($i == "version") { print $(i + 2); exit } }' "$pointer_path")
    valid_version "$version" || fail "latest.json does not name a valid release version; nothing was installed"
  fi
  echo "Installing ztd $version"

  binary="ztd-${os}-${arch}"
  binary_path="$tmp_dir/$binary"
  checksum_path="$tmp_dir/$binary.sha256"
  binary_url="$base_url/v/$version/$binary"
  checksum_url="$binary_url.sha256"
  curl -fsSL "$binary_url" -o "$binary_path" \
    || fail "download failed: $binary_url"
  curl -fsSL "$checksum_url" -o "$checksum_path" \
    || fail "download failed: $checksum_url"
  [ -s "$binary_path" ] || fail "downloaded $binary is empty"

  checksum_lines=$(awk 'NF { count++ } END { print count + 0 }' "$checksum_path")
  [ "$checksum_lines" = 1 ] || fail "malformed checksum file for $binary (expected one line)"
  expected=$(awk 'NF { print $1; exit }' "$checksum_path" | tr '[:upper:]' '[:lower:]')
  case "$expected" in
    *[!0-9a-f]*) fail "malformed checksum file for $binary (not hexadecimal)" ;;
  esac
  [ "${#expected}" -eq 64 ] || fail "malformed checksum file for $binary (expected 64 hex characters)"

  digest_output=$(sha256_digest "$binary_path") || fail "could not compute the SHA-256 of $binary"
  actual=$(echo "${digest_output%% *}" | tr '[:upper:]' '[:lower:]')
  if [ "$expected" != "$actual" ]; then
    fail "checksum verification failed for $binary; nothing was installed"
  fi

  # The checksum proves the bytes match what this host published. Sigstore
  # proves the release workflow signed them. Checksum-only is honest about
  # that gap; ZTD_REQUIRE_SIGSTORE=1 closes it.
  verified="SHA-256 verified; signature not checked (install cosign, or set ZTD_REQUIRE_SIGSTORE=1 to require it)"
  sig_path="$tmp_dir/$binary.sigstore.json"
  if curl -fsSL "$binary_url.sigstore.json" -o "$sig_path"; then
    if command -v cosign >/dev/null 2>&1; then
      identity="https://github.com/zerotrustdata/ztd/.github/workflows/release-binary.yml@refs/tags/v${version}"
      if ! cosign verify-blob \
        --bundle "$sig_path" \
        --certificate-identity "$identity" \
        --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
        "$binary_path" >"$tmp_dir/cosign.out" 2>&1; then
        cat "$tmp_dir/cosign.out" >&2
        fail "Sigstore verification failed for $binary; nothing was installed"
      fi
      verified="SHA-256 verified and Sigstore signature verified"
    elif [ "${ZTD_REQUIRE_SIGSTORE:-}" = 1 ]; then
      fail "ZTD_REQUIRE_SIGSTORE=1 but cosign is not installed (macOS: brew install cosign)"
    fi
  elif [ "${ZTD_REQUIRE_SIGSTORE:-}" = 1 ]; then
    fail "ZTD_REQUIRE_SIGSTORE=1 but the signature bundle could not be downloaded: $binary_url.sigstore.json"
  else
    verified="SHA-256 verified; signature bundle unavailable, checksum only"
  fi
  echo "Verified: $verified"

  case "$(uname -s)" in
    Darwin)
      if command -v xattr >/dev/null 2>&1 && xattr -p com.apple.quarantine "$binary_path" >/dev/null 2>&1; then
        xattr -d com.apple.quarantine "$binary_path" \
          || fail "could not clear Gatekeeper quarantine on the verified binary"
        echo "Cleared Gatekeeper quarantine after verification"
      fi
      ;;
  esac

  if [ -n "$replacing" ]; then
    echo "ztd install: replacing existing $target (ZTD_OVERWRITE=1); it was not checked as a previous ztd install" >&2
  fi
  install -m 0755 "$binary_path" "$target" || fail "could not write $target"
  case "$(uname -s)" in
    Darwin)
      if command -v xattr >/dev/null 2>&1; then
        xattr -d com.apple.quarantine "$target" 2>/dev/null || true
      fi
      ;;
  esac
  echo "Installed $target"
}

main "$@"
