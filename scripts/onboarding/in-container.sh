#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# Replays a new user's first run inside a fresh-host container (see Dockerfile).
# Runs as the unprivileged `newcomer` user against a source snapshot at
# /src.tar and writes logs plus results.tsv (scenario, PASS|FAIL, seconds,
# detail) to ~/out for run-matrix.sh to collect.
#
# Scenarios, run in the order given:
#   build-only     bash scripts/linux-setup.sh --build-only -y from nothing
#   rerun          the same command again: no installs, PATH not duplicated
#   quickstart     make quickstart twice: first run, then reuse of everything
#   release-smoke  ./ztd test release-smoke (governed lifecycle + evidence)
#   contributor    bash scripts/linux-setup.sh -y (full make ci toolchain)
#   operator-deploy real three-Operator CLI lifecycle on an owned Gateway
#
# usage: in-container.sh <scenario>...

set -uo pipefail

OUT="$HOME/out"
REPO="$HOME/ztd"
RESULTS="$OUT/results.tsv"
MARKER="# ztd: add repository root to PATH"
mkdir -p "$OUT"
: >"$RESULTS"

# --- assertions ------------------------------------------------------------------
# A scenario runs in a subshell and appends one line per failed expectation to
# FAILURES_FILE, so a scenario that crashes is still reported, not lost.
FAILURES_FILE="$OUT/.failures"

expect() {
    local what="$1"
    shift
    if ! "$@" >/dev/null 2>&1; then
        echo "$what" >>"$FAILURES_FILE"
    fi
}

expect_log() {
    local log="$1" pattern="$2"
    grep -qE -- "$pattern" "$OUT/$log" 2>/dev/null || echo "$log lacks /$pattern/" >>"$FAILURES_FILE"
}

expect_no_log() {
    local log="$1" pattern="$2"
    if grep -qE -- "$pattern" "$OUT/$log" 2>/dev/null; then
        echo "$log unexpectedly matches /$pattern/" >>"$FAILURES_FILE"
    fi
}

# Runs a command the way a user does after setup: in a new interactive login
# terminal with a clean environment, so only what setup wrote to the shell
# profile is on PATH. SSH_AUTH_SOCK is the one passthrough, so ztd can reach the
# ephemeral ssh-agent that scenario_quickstart starts. Output goes to $OUT/<log>.
# usage: in_new_terminal <log> <command>
in_new_terminal() {
    env -i HOME="$HOME" USER="$(id -un)" SHELL=/bin/bash TERM=dumb LANG=C.UTF-8 PATH=/usr/bin:/bin \
        ${SSH_AUTH_SOCK:+"SSH_AUTH_SOCK=$SSH_AUTH_SOCK"} \
        bash -ic "cd '$REPO' && $2" >"$OUT/$1" 2>&1 </dev/null
}

marker_count() {
    grep -cF -- "$MARKER" "$HOME/.bashrc" 2>/dev/null || true
}

# Runs <scenario-fn> with a fresh failure list and records one results row.
run_scenario() {
    local name="$1" started=$SECONDS status=PASS detail="" rc=0
    : >"$FAILURES_FILE"
    echo "=== scenario: $name"
    ( "scenario_${name//-/_}" ) || rc=$?
    if [[ $rc -ne 0 ]]; then
        echo "scenario aborted (exit $rc; see container.log)" >>"$FAILURES_FILE"
    fi
    if [[ -s "$FAILURES_FILE" ]]; then
        status=FAIL
        detail="$(paste -sd ';' "$FAILURES_FILE" | sed 's/;/; /g')"
    fi
    printf '%s\t%s\t%s\t%s\n' "$name" "$status" "$((SECONDS - started))" "$detail" >>"$RESULTS"
    echo "=== $name: $status ${detail:+($detail)}"
}

# --- scenarios -------------------------------------------------------------------

scenario_build_only() {
    expect "setup exits 0" bash -c "bash scripts/linux-setup.sh --build-only -y >'$OUT/build-only.log' 2>&1"
    expect "./ztd is executable" test -x ./ztd
    expect "./ztd --version runs" ./ztd --version
    expect "Node.js and npm installed for frontend builds" bash -c 'command -v node && command -v npm'
    expect "PATH marker written once" test "$(marker_count)" = 1
    expect_log build-only.log 'ztd is ready'
    expect_log build-only.log 'make quickstart'
    # The archive has no Git metadata. Once setup has installed Git, commit
    # this disposable snapshot so release evidence can name its source revision.
    expect "source snapshot has a Git revision" bash -c '
        git init -q &&
        git add --all &&
        git -c user.name="Onboarding Test" -c user.email="onboarding@example.invalid" \
            commit -qm "Onboarding source snapshot"
    '
}

scenario_rerun() {
    expect "rerun in a new terminal exits 0" in_new_terminal rerun.log "bash scripts/linux-setup.sh --build-only -y"
    expect_no_log rerun.log 'missing or outdated'
    expect_log rerun.log 'already on PATH'
    expect "PATH marker still written once" test "$(marker_count)" = 1
    expect "ztd and go on PATH in a new terminal" in_new_terminal new-terminal.log "command -v ztd && command -v go"
}

scenario_quickstart() {
    # A container has neither a Secret Service keyring nor an ssh-agent. Start an
    # ephemeral agent whose socket and throwaway Ed25519 key live in a private
    # scratch directory under the repo runtime root. Quickstart and every ./ztd call
    # it makes find the agent via SSH_AUTH_SOCK. The trap stops it when this scenario exits.
    # shellcheck source=../lib/scratch.sh
    . "$REPO/scripts/lib/scratch.sh"
    local agent_dir
    agent_dir="$(ztd_scratch_dir "$REPO" onboarding-agent)" || return 1
    trap 'ssh-agent -k >/dev/null 2>&1 || true' EXIT
    eval "$(ssh-agent -s -a "$agent_dir/agent.sock")"
    ssh-keygen -q -t ed25519 -N '' -f "$agent_dir/agent_key"
    ssh-add -q "$agent_dir/agent_key"

    expect "first quickstart exits 0" in_new_terminal quickstart.log "make quickstart"
    expect_log quickstart.log 'hello from ztd'
    expect_log quickstart.log "You're running ztd"
    expect "second quickstart exits 0" in_new_terminal quickstart-rerun.log "make quickstart"
    expect_log quickstart-rerun.log 'already running'
    expect_log quickstart-rerun.log 'already signed in'
    expect_no_log quickstart.log 'command not found'
    local session
    session="$(awk '$1 == "Operator" && $2 == "session" {print $3; exit}' "$OUT/quickstart.log")"
    expect "audit trail for the governed action" bash -c "./ztd gw data audit list --operator-session-id '$session' >'$OUT/audit.log' 2>&1"
    expect "make down exits 0" bash -c "make down >'$OUT/down.log' 2>&1"
}

scenario_release_smoke() {
    expect "release smoke passes" bash -c "./ztd test release-smoke --root '$OUT/release-smoke' --binary '$REPO/ztd' >'$OUT/release-smoke.log' 2>&1"
    expect_log release-smoke.log 'Release smoke PASSED'
}

scenario_contributor() {
    expect "contributor setup exits 0" bash -c "bash scripts/linux-setup.sh -y >'$OUT/contributor.log' 2>&1"
    expect_log contributor.log 'dev-check passed'
    expect "third-party notices match" bash -c "make third-party-notices-check >'$OUT/notices.log' 2>&1"
    expect "env template preserved" cmp .env.example .env
    expect "env file is owner-only" test "$(stat -c %a .env)" = 600
}

scenario_operator_deploy() {
    expect "real Operator deployment lifecycle passes" bash -c \
        "bash scripts/ci/operator-deploy-lifecycle.sh --count 3 --root '$OUT/deployment-lifecycle' >'$OUT/operator-deploy.log' 2>&1"
}

# --- main ------------------------------------------------------------------------

mkdir -p "$REPO"
tar -xf /src.tar -C "$REPO" || { printf 'unpack\tFAIL\t0\tcannot unpack /src.tar\n' >>"$RESULTS"; exit 1; }
cd "$REPO" || exit 1

for scenario in "$@"; do
    if ! declare -F "scenario_${scenario//-/_}" >/dev/null; then
        printf '%s\tFAIL\t0\tunknown scenario\n' "$scenario" >>"$RESULTS"
        continue
    fi
    run_scenario "$scenario"
done

! grep -q $'\tFAIL\t' "$RESULTS"
