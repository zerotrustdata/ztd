#!/bin/sh
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# Live smoke test for the deployed site, run after every production deploy. Checks the Releases pages, then runs the
# published one-command onboarding exactly as a user would, with a provisioned master key file because CI runners
# have no OS key store, and stops the Gateway it started. ZTD_SMOKE_ORIGIN overrides the origin (e.g. a preview).

set -eu

origin="${ZTD_SMOKE_ORIGIN:-https://zerotrustdata.io}"
work=$(mktemp -d)
dir="$work/ztd"

cleanup() {
  if [ -x "$dir/ztd" ]; then
    (cd "$dir" && ./ztd gw stop --master-key-file "$work/master.key" </dev/null >/dev/null 2>&1) || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

fail() {
  echo "smoke: $*" >&2
  exit 1
}

page() {
  curl -fsS "$origin$1" -o "$work/page" || fail "GET $1 failed"
  grep -qF "$2" "$work/page" || fail "GET $1 does not contain: $2"
}

version=$(curl -fsS "$origin/latest.json" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
[ -n "$version" ] || fail "latest.json does not name a version"
echo "smoke: latest release v$version"

page /releases/ "/releases/v$version/"
page /releases/ "/start.sh | sh"
page "/releases/v$version/" "/v/$version/ztd-binaries.json"
location=$(curl -fsS -o /dev/null -w '%{redirect_url}' "$origin/releases/latest/")
[ "$location" = "$origin/releases/v$version/" ] || fail "/releases/latest/ redirects to '$location'"
echo "smoke: releases pages ok"

(umask 077 && head -c 32 /dev/urandom | base64 >"$work/master.key")
curl -fsSL "$origin/start.sh" | ZTD_DIR="$dir" ZTD_MASTER_KEY_FILE="$work/master.key" sh
"$dir/ztd" --version </dev/null | grep -qF "$version" || fail "installed ztd is not v$version"
echo "smoke: start.sh onboarding ok"
