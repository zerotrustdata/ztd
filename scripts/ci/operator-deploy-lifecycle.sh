#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# Run the real CLI deployment lifecycle on a fresh, owned Gateway. The test
# owns its Operator processes, including failure cleanup. Preserve Gateway logs
# and a binary hash/timing report; stop the Gateway on every exit.
set -euo pipefail
umask 077
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN="$REPO/ztd"
COUNT=3
ROOT=""
while (($#)); do
    case "$1" in
        --binary) BIN="${2:?--binary needs a path}"; shift 2 ;;
        --count) COUNT="${2:?--count needs a number}"; shift 2 ;;
        --root) ROOT="${2:?--root needs a path}"; shift 2 ;;
        --help) echo "Usage: $0 [--binary PATH] [--count 3] [--root NEW_DIRECTORY]"; exit 0 ;;
        *) echo "Unknown argument: $1" >&2; exit 2 ;;
    esac
done
if [[ ! "$COUNT" =~ ^[1-9][0-9]*$ ]] || ((COUNT > 4997)); then
    echo "--count must be an integer in 1..4997" >&2
    exit 2
fi
BIN="$(realpath "$BIN")"
[[ -x "$BIN" ]] || { echo "Build ztd with make build first" >&2; exit 2; }
# Probe before installing the cleanup trap: an occupied port belongs to someone
# else, so refusing the run must never stop that Gateway.
for port in 8080 8443; do
    if [[ -n "$(ss -Hltn "sport = :$port")" ]]; then
        echo "Port $port is occupied; lifecycle test requires an isolated Gateway" >&2
        exit 1
    fi
done
if [[ -z "$ROOT" ]]; then
    ROOT="$(mktemp -d -t ztd-deploy-lifecycle.XXXXXXXX)"
else
    [[ ! -e "$ROOT" ]] || { echo "--root must be a new directory" >&2; exit 2; }
    mkdir -p "$ROOT"
    ROOT="$(realpath "$ROOT")"
fi
mkdir -p "$ROOT/run" "$ROOT/out" "$ROOT/home" "$ROOT/tmp" "$ROOT/bin" "$ROOT/keys"
# CI runners have neither a Secret Service keyring nor an ssh-agent, so seal
# this owned Gateway and its CLI identity with a provisioned master key file:
# 32 random bytes, base64, 0600, outside the runtime root.
MASTER_KEY="$ROOT/keys/master.key"
(set -C && head -c 32 /dev/urandom | base64 >"$MASTER_KEY")
export ZTD_E2E_MASTER_KEY_FILE="$MASTER_KEY"
# Copy once so rebuilding the workspace binary cannot change this scenario.
cp "$BIN" "$ROOT/bin/ztd"
BIN="$ROOT/bin/ztd"
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
export GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}"
export HOME="$ROOT/home"
export USERPROFILE="$ROOT/home"
export TMPDIR="$ROOT/tmp"

# owned_operator PID SCRATCH reports whether PID is a `ztd operator start`
# worker whose --working-dir resolves inside SCRATCH.
owned_operator() {
    local pid="$1" scratch="$2" i dir
    local -a args
    mapfile -d '' -t args <"/proc/$pid/cmdline" 2>/dev/null || return 1
    ((${#args[@]} >= 3)) || return 1
    [[ "$(basename -- "${args[0]}")" == "ztd" && "${args[1]}" == "operator" && "${args[2]}" == "start" ]] || return 1
    for ((i = 3; i < ${#args[@]} - 1; i++)); do
        if [[ "${args[i]}" == "--working-dir" ]]; then
            dir="$(realpath -m -- "${args[i + 1]}")"
            [[ "$dir" == "$scratch" || "$dir" == "$scratch"/* ]]
            return
        fi
    done
    return 1
}

# owned_operators SCRATCH prints the PIDs of every owned Operator worker.
owned_operators() {
    local entry pid
    for entry in /proc/[0-9]*; do
        pid="${entry#/proc/}"
        if owned_operator "$pid" "$1"; then
            echo "$pid"
        fi
    done
}

# cleanup_owned_operators SCRATCH sends SIGTERM, then SIGKILL, to owned workers
# and fails if any survive.
cleanup_owned_operators() {
    local scratch sig pid deadline
    scratch="$(realpath -m -- "$1")"
    for sig in TERM KILL; do
        for pid in $(owned_operators "$scratch"); do
            # Re-check ownership right before signaling to narrow PID reuse.
            if owned_operator "$pid" "$scratch" && kill "-$sig" "$pid" 2>/dev/null; then
                echo "signaled owned worker $pid: SIG$sig"
            fi
        done
        deadline=$((SECONDS + 5))
        while [[ -n "$(owned_operators "$scratch")" ]] && ((SECONDS < deadline)); do
            sleep 0.05
        done
        if [[ -z "$(owned_operators "$scratch")" ]]; then
            echo "no owned Operator processes remain"
            return 0
        fi
    done
    echo "Operator cleanup failed: $(owned_operators "$scratch" | tr '\n' ' ')" >&2
    return 1
}

SUITE_PID=""
cleanup() {
    local status=$?
    trap - EXIT INT TERM
    if [[ -n "$SUITE_PID" ]]; then
        kill -TERM -- "-$SUITE_PID" 2>/dev/null || true
        wait "$SUITE_PID" 2>/dev/null || true
    fi
    # Go's hard test timeout bypasses t.Cleanup. Match detached workers only
    # inside this runner's scratch tree, including unregistered workers.
    if ! cleanup_owned_operators "$ROOT/tmp" >"$ROOT/out/operator-cleanup.log" 2>&1; then
        echo "Operator teardown failed; see $ROOT/out/operator-cleanup.log" >&2
        status=1
    fi
    if ! (cd "$ROOT/run" && "$BIN" gw stop --master-key-file "$MASTER_KEY") >"$ROOT/out/gateway-stop.log" 2>&1; then
        echo "Gateway teardown failed; see $ROOT/out/gateway-stop.log" >&2
        status=1
    fi
    if ((status != 0)); then
        for log in gateway-start owner-login; do
            [[ -s "$ROOT/out/$log.log" ]] && { echo "--- $log.log (tail)"; tail -n 20 "$ROOT/out/$log.log"; } >&2
        done
    fi
    echo "Deployment lifecycle evidence: $ROOT/out (exit $status)"
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
(cd "$ROOT/run" && "$BIN" gw start --cert-mode localhost --posture doctrine \
    --capabilities data --public-spectator=false --log info --master-key-file "$MASTER_KEY") >"$ROOT/out/gateway-start.log" 2>&1
(cd "$ROOT/run" && "$BIN" login --endpoint localhost --master-key-file "$MASTER_KEY") >"$ROOT/out/owner-login.log" 2>&1
cd "$REPO"
setsid env ZTD_E2E_RUNTIME_ROOT="$ROOT/run" \
ZTD_E2E_FLEET_SIZE="$COUNT" \
ZTD_E2E_FLEET_BIN="$BIN" \
ZTD_E2E_FLEET_REPORT="$ROOT/out/deployment-report.json" \
    "$BIN" test e2e --timeout 10m --run '^TestOperatorDeploy_LocalLifecycle$' \
    > >(tee "$ROOT/out/scenario.log") 2>&1 &
SUITE_PID=$!
wait "$SUITE_PID"
SUITE_PID=""
