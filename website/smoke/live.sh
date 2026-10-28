#!/bin/sh
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# Live smoke test for the deployed site, run after every production deploy. Checks the Releases pages, then runs the
# published one-command onboarding exactly as a user would, under an ephemeral ssh-agent with a throwaway Ed25519 key
# because CI runners have no OS key store, and stops the Gateway and the agent it started. ZTD_SMOKE_ORIGIN overrides
# the origin (e.g. a preview); ZTD_SMOKE_WORKER_VERSION names the Worker version the checks must run against.

set -eu

origin="${ZTD_SMOKE_ORIGIN:-https://zerotrustdata.io}"
root=$(cd "$(dirname "$0")/../.." && pwd)
# shellcheck source=../../scripts/lib/scratch.sh
. "$root/scripts/lib/scratch.sh"
work=$(ztd_scratch_dir "$root" ztd-website-smoke)
dir="$work/ztd"

cleanup() {
  if [ -x "$dir/ztd" ]; then
    (cd "$dir" && ./ztd gw stop </dev/null >/dev/null 2>&1) || true
  fi
  if [ -n "${SSH_AGENT_PID:-}" ]; then
    ssh-agent -k >/dev/null 2>&1 || true
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

# wrangler deploy returns before every edge serves the new version, so wait until the site answers from the Worker
# version just deployed (ZTD_SMOKE_WORKER_VERSION) rather than smoke-testing the previous one.
if [ -n "${ZTD_SMOKE_WORKER_VERSION:-}" ]; then
  tries=0
  until curl -fsS -o /dev/null -D - "$origin/" | tr -d '\r' | grep -qixF "x-ztd-worker-version: $ZTD_SMOKE_WORKER_VERSION"; do
    tries=$((tries + 1))
    [ "$tries" -lt 60 ] || fail "worker version $ZTD_SMOKE_WORKER_VERSION is not live after 120s"
    sleep 2
  done
  echo "smoke: worker version $ZTD_SMOKE_WORKER_VERSION live"
fi

version=$(curl -fsS "$origin/latest.json" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
[ -n "$version" ] || fail "latest.json does not name a version"
echo "smoke: latest release v$version"

page /releases/ "/releases/v$version/"
page /releases/ "/start.sh | sh"
page /releases/ "/start.ps1 | iex"
page /start.ps1 "irm https://zerotrustdata.io/start.ps1 | iex"
page "/releases/v$version/" "/v/$version/ztd-binaries.json"
location=$(curl -fsS -o /dev/null -w '%{redirect_url}' "$origin/releases/latest/")
[ "$location" = "$origin/releases/v$version/" ] || fail "/releases/latest/ redirects to '$location'"
echo "smoke: releases pages ok"

page /terms/ "Commercial License and Services Terms"
page /terms/ 'href="/terms/oem/"'
page /terms/oem/ "ztd Commercial OEM License Agreement"
curl -fsS "$origin/" -o "$work/home" || fail "GET / failed"
if grep -q 'href="/buy/' "$work/home"; then
  fail "homepage still links to /buy/"
fi
grep -q 'id="proofs"' "$work/home" || fail "homepage has no proofs section"
echo "smoke: terms pages ok and homepage has no /buy/ links"

eval "$(ssh-agent -s -a "$work/agent.sock")"
ssh-keygen -q -t ed25519 -N '' -f "$work/agent_key"
ssh-add -q "$work/agent_key"
curl -fsSL "$origin/start.sh" | ZTD_DIR="$dir" sh
"$dir/ztd" --version </dev/null | grep -qF "$version" || fail "installed ztd is not v$version"
echo "smoke: start.sh onboarding ok"
