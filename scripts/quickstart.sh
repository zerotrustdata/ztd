#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# ztd quickstart: from a checkout to a running Gateway, a signed-in CLI, and a
# first governed action in one command. Every step reuses what already exists
# (binary, running Gateway, CLI identity), so rerunning it is safe and fast.
# It only composes the public CLI; it never touches .ztd/ runtime state (its command log
# is scratch under .ztd/tmp).
# Run via `make quickstart`. See docs/guides/getting_started.md.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/dev-setup-common.sh
source "${SCRIPT_DIR}/lib/dev-setup-common.sh"

ztd_quickstart_usage() {
    cat <<EOF
Usage: bash scripts/quickstart.sh [options] [-- <extra ./ztd gw start flags>]

Starts a local Gateway (embedded Operator, doctrine posture), signs in the CLI
headless, and runs a first governed action. Safe to rerun.

Options:
  -h, --help                Show this help
EOF
}

GW_EXTRA=()
while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help) ztd_quickstart_usage; exit 0 ;;
        --) shift; GW_EXTRA=("$@"); break ;;
        *) echo "Unknown option: $1"; ztd_quickstart_usage; exit 2 ;;
    esac
done

ztd_ui_init
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "$REPO_ROOT"
ZTD=./ztd
# shellcheck source=lib/scratch.sh
. "${SCRIPT_DIR}/lib/scratch.sh"
SCRATCH="$(ztd_scratch_dir "$REPO_ROOT" ztd-quickstart)"
LOG="$SCRATCH/commands.log"
trap 'rm -rf "$SCRATCH"' EXIT

# usage: ztd_cli <ztd arguments...>
ztd_cli() {
    "$ZTD" "$@"
}

# Runs a command with its output captured; on failure shows the tail and exits.
# usage: ztd_quickstart_run <what> <command...>
ztd_quickstart_run() {
    local what="$1"
    shift
    if ! "$@" >"$LOG" 2>&1; then
        ztd_ui_fail "$what failed"
        sed 's/^/    /' "$LOG" | tail -n 25 >&2
        return 1
    fi
}

# Captures before matching: `grep -q` exits on the first match, and under
# pipefail the writer's SIGPIPE would turn an online Gateway into "stopped".
ztd_gateway_online() {
    local status
    status="$(ztd_cli gw status 2>/dev/null)" || return 1
    grep -Eq '^Gateway[[:space:]]+(online|running)' <<<"$status"
}

ztd_ui_title "ztd quickstart"

# 1. Binary ---------------------------------------------------------------------
ztd_ui_step 1 4 "ztd binary"
if [[ ! -x "$ZTD" ]]; then
    if ! ztd_have go; then
        ztd_ui_fatal "Go is not installed. Run: bash scripts/$( [[ "$(uname -s)" == Darwin ]] && echo macos || echo linux )-setup.sh --build-only"
        exit 1
    fi
    ztd_ui_info "building ./ztd (first run only)..."
    ztd_quickstart_run "make build" make build || exit 1
fi
ztd_ui_ok "$("$ZTD" --version 2>/dev/null | head -n 1)"

# 2. Gateway --------------------------------------------------------------------
ztd_ui_step 2 4 "Gateway"
if ztd_gateway_online; then
    ztd_ui_ok "already running — reusing it"
else
    ztd_ui_info "starting Gateway (doctrine posture, embedded data Operator)..."
    if ! ztd_quickstart_run "Gateway start" ztd_cli gw start --capabilities data ${GW_EXTRA[@]+"${GW_EXTRA[@]}"}; then
        if grep -qiE 'keyring|keystore|secret service|ssh-agent|master key' "$LOG"; then
            echo >&2
            if [[ "$(uname -s)" == Darwin ]]; then
                ztd_ui_warn "macOS Keychain did not release the vault master key." >&2
                echo "    Rerun in a GUI terminal session and approve the Keychain prompt for ztd (Always Allow)." >&2
                echo "    Then rerun make quickstart." >&2
            else
                ztd_ui_warn "No usable key store for the vault master key. Start an ssh-agent with a key loaded:" >&2
                echo "      ssh-keygen -t ed25519                # skip if you already have an Ed25519 key" >&2
                echo "      eval \"\$(ssh-agent)\" && ssh-add" >&2
                echo "    Then rerun make quickstart." >&2
            fi
        elif grep -qiE 'address already in use|port unavailable' "$LOG"; then
            echo >&2
            ztd_ui_warn "A port the Gateway needs is in use, usually by a Gateway started from another directory." >&2
            echo "    Stop it from its own directory with ./ztd gw stop, then rerun make quickstart." >&2
        fi
        exit 1
    fi
    grep -E 'Waiting for access|listens on HTTP|Port [0-9]+ is held' "$LOG" 2>/dev/null | sed 's/^/  /' || true
    ztd_ui_ok "Gateway online"
fi

# 3. Sign in --------------------------------------------------------------------
ztd_ui_step 3 4 "Sign in"
if ztd_cli auth context >/dev/null 2>&1; then
    ztd_ui_ok "CLI already signed in — reusing the identity"
else
    ztd_quickstart_run "ztd login" ztd_cli login || exit 1
    ztd_ui_ok "signed in as the Gateway owner (headless CLI identity)"
fi

# 4. First governed action ------------------------------------------------------
ztd_ui_step 4 4 "First governed action"
ztd_ui_info "./ztd operator run --all-active --cmd 'echo hello from ztd'"
ztd_quickstart_run "governed action" ztd_cli operator run --all-active --cmd 'echo hello from ztd' || exit 1
sed 's/^/    /' "$LOG"
session="$(awk '$1 == "Operator" && $2 == "session" {print $3; exit}' "$LOG")"
ztd_ui_ok "admitted by the Gateway, executed by the Operator, signed and recorded"

console="$(ztd_cli gw status 2>/dev/null | awk '$1 == "Console" {print $2}')"
echo -e "\n${ZTD_UI_GREEN}${ZTD_UI_BOLD}${ZTD_UI_OK} You're running ztd${ZTD_UI_RESET} ${ZTD_UI_DIM}($(ztd_ui_elapsed))${ZTD_UI_RESET}\n"
echo -e "  ${ZTD_UI_BOLD}Watch${ZTD_UI_RESET}     ./ztd tui                ${ZTD_UI_DIM}live governance console in the terminal${ZTD_UI_RESET}"
echo -e "  ${ZTD_UI_BOLD}Browser${ZTD_UI_RESET}   ./ztd login web          ${ZTD_UI_DIM}add a passkey, then open ${console:-https://localhost:8443/console/}${ZTD_UI_RESET}"
echo -e "  ${ZTD_UI_BOLD}Evidence${ZTD_UI_RESET}  ./ztd gw data audit list --operator-session-id ${session:-<id>}"
echo -e "  ${ZTD_UI_BOLD}Agents${ZTD_UI_RESET}    ./ztd mcp agent run claude"
echo -e "  ${ZTD_UI_BOLD}Stop${ZTD_UI_RESET}      make down                ${ZTD_UI_DIM}state in .ztd/ is kept${ZTD_UI_RESET}"
