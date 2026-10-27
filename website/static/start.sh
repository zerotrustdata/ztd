#!/bin/sh
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# One-command ztd onboarding for Linux and macOS:
#
#   curl -fsSL https://zerotrustdata.io/start.sh | sh
#
# 1. Installs ztd into $ZTD_DIR (default ~/ztd) with the checksum-verified installer, ztd.sh.
# 2. Starts a Gateway there on https://localhost:8443 (doctrine posture, embedded data Operator, loopback only).
# 3. Signs the CLI in headless as the Gateway owner (ztd login).
# 4. Runs a first governed action through the Gateway and the Operator.
#
# Every step reuses what already exists (binary, running Gateway, CLI identity), so rerunning is safe. Runtime
# state lives in $ZTD_DIR/.ztd/. Never requires root and never invokes sudo.
#
# Environment:
#   ZTD_DIR              Install and runtime directory (default: ~/ztd).
#   ZTD_VERSION          Install this X.Y.Z release instead of the latest.
#   ZTD_OVERWRITE=1      Replace an existing $ZTD_DIR/ztd binary (upgrade). A running Gateway keeps its version
#                        until it is restarted (./ztd gw restart).

set -eu

base_url="https://zerotrustdata.io"
tmp_dir=""
log=""

cleanup() {
  if [ -n "$tmp_dir" ]; then
    rm -rf "$tmp_dir"
  fi
}

ui_init() {
  if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != dumb ]; then
    bold=$(printf '\033[1m') dim=$(printf '\033[2m') green=$(printf '\033[32m') red=$(printf '\033[31m')
    yellow=$(printf '\033[33m') cyan=$(printf '\033[36m') reset=$(printf '\033[0m')
  else
    bold="" dim="" green="" red="" yellow="" cyan="" reset=""
  fi
}

# A next-step command and its description, aligned; a command too long for the column gets the description below it.
next_step() {
  if [ "${#1}" -gt 30 ]; then
    printf '  %s\n    %s%s%s\n' "$1" "$dim" "$2" "$reset"
  else
    printf '  %-30s %s%s%s\n' "$1" "$dim" "$2" "$reset"
  fi
}
step() { printf '\n%s[%s/4]%s %s%s%s\n' "$cyan" "$1" "$reset" "$bold" "$2" "$reset"; }
ok() { printf '  %s✓%s %s\n' "$green" "$reset" "$*"; }
info() { printf '  %s%s%s\n' "$dim" "$*" "$reset"; }
warn() { printf '  %s!%s %s\n' "$yellow" "$reset" "$*" >&2; }

fail() {
  printf '%sztd start:%s %s\n' "$red" "$reset" "$*" >&2
  exit 1
}

# Shows the captured output of the failed step, indented.
show_log() {
  if [ -s "$log" ]; then
    tail -n 25 "$log" | sed 's/^/    /' >&2
  fi
}

# Runs ./ztd with stdin on /dev/null: under `curl | sh` the script itself arrives on stdin, and no ztd command may
# read it.
ztd_cli() {
  ./ztd "$@" </dev/null
}

# Runs a ztd command with its output captured in $log.
captured() {
  ztd_cli "$@" >"$log" 2>&1
}

gateway_online() {
  captured gw status || return 1
  # "online" once the health API answers (signed-in CLI); "running" when only
  # the PID is alive, e.g. a Gateway left by a run that stopped before sign-in.
  awk '$1 == "Gateway" && ($2 == "online" || $2 == "running") { found = 1 } END { exit !found }' "$log"
}

# Explains the two failures a first Gateway start commonly hits on a new host.
gateway_start_hint() {
  if grep -qiE 'key ?store|keyring|secret service|ssh-agent|master key' "$log"; then
    echo >&2
    case "$(uname -s)" in
      Darwin)
        warn "macOS Keychain did not release the Gateway vault master key."
        echo "    Rerun in a GUI terminal session and approve the Keychain prompt for ztd (Always Allow)." >&2
        echo "    No GUI session (SSH/container)? The Keychain prompt cannot be shown there; sign in from the GUI session first." >&2
        ;;
      *)
        warn "No usable OS key store for the Gateway vault master key. Start an ssh-agent with a key loaded:"
        echo "      eval \"\$(ssh-agent)\" && ssh-add    # then rerun the start command" >&2
        echo "    No SSH key yet? Create one first with: ssh-keygen -t ed25519" >&2
        ;;
    esac
  elif grep -qiE 'address already in use|port unavailable|port [0-9]+ .*in use' "$log"; then
    echo >&2
    warn "A port the Gateway needs is in use, usually by a Gateway started from another directory."
    echo "    Stop it from its own directory with ./ztd gw stop, then rerun the start command." >&2
    echo "    On macOS, find the holder with: lsof -nP -iTCP:8080 -sTCP:LISTEN" >&2
  fi
}

# Surfaces Gateway notices (Keychain wait, shifted ports) that a captured
# start would otherwise swallow on the success path.
gateway_start_notices() {
  grep -E 'Waiting for access|listens on HTTP|Port [0-9]+ is held' "$log" 2>/dev/null | sed 's/^/  /' || true
}

main() {
  ui_init
  started=$(date +%s)
  for required in curl mktemp awk sed grep tail uname date; do
    command -v "$required" >/dev/null 2>&1 || fail "required command not found: $required"
  done
  case "$(uname -s | tr '[:upper:]' '[:lower:]')" in
    linux|darwin) ;;
    mingw*|msys*|cygwin*) fail "on Windows, run from PowerShell: irm $base_url/start.ps1 | iex" ;;
    *) fail "unsupported OS: $(uname -s)" ;;
  esac
  if [ -n "${ZTD_DIR:-}" ]; then
    dir=$ZTD_DIR
  else
    [ -n "${HOME:-}" ] || fail "HOME is not set; set ZTD_DIR to choose an install directory"
    dir="$HOME/ztd"
  fi
  mkdir -p "$dir" || fail "could not create $dir"
  cd "$dir" || fail "could not enter $dir"
  dir=$(pwd)
  display_dir=$dir
  case "$dir" in
    "${HOME:-/nonexistent}"/*) display_dir="~${dir#"$HOME"}" ;;
  esac

  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  tmp_dir=$(mktemp -d) || fail "could not create a temporary directory"
  log="$tmp_dir/step.log"

  printf '%sztd%s — zero-trust execution for AI agents. Installing into %s\n' "$bold" "$reset" "$display_dir"

  # 1. Binary ----------------------------------------------------------------
  step 1 "ztd binary"
  if [ -f ./ztd ] && [ ! -L ./ztd ] && [ "${ZTD_OVERWRITE:-}" != 1 ]; then
    [ -x ./ztd ] || fail "$dir/ztd exists but is not executable; rerun with ZTD_OVERWRITE=1 to replace it"
    ok "reusing $display_dir/ztd (rerun with ZTD_OVERWRITE=1 to upgrade)"
  else
    curl -fsSL "$base_url/ztd.sh" -o "$tmp_dir/ztd.sh" || fail "download failed: $base_url/ztd.sh"
    if ! ZTD_INSTALL_DIR="$dir" sh "$tmp_dir/ztd.sh" </dev/null >"$log" 2>&1; then
      show_log
      fail "installing the ztd binary failed; nothing else was changed"
    fi
    ok "$(awk '/^Installed /' "$log") — SHA-256 verified"
  fi
  if ./ztd --version </dev/null >"$log" 2>&1; then
    info "$(head -n 1 "$log")"
  else
    show_log
    fail "$dir/ztd does not run on this host"
  fi

  # 2. Gateway ---------------------------------------------------------------
  step 2 "Gateway"
  if gateway_online; then
    ok "already running — reusing it"
  else
    info "starting Gateway (doctrine posture, embedded data Operator, loopback only)..."
    if ! captured gw start; then
      show_log
      gateway_start_hint
      fail "the Gateway did not start"
    fi
    gateway_start_notices
    ok "Gateway online"
  fi

  # 3. Sign in ---------------------------------------------------------------
  step 3 "Sign in"
  if captured auth context; then
    ok "CLI already signed in — reusing the identity"
  elif captured login; then
    ok "signed in as the Gateway owner (headless CLI identity)"
  else
    show_log
    fail "ztd login failed"
  fi

  # 4. First governed action -------------------------------------------------
  step 4 "First governed action"
  info "./ztd operator run --all-active --cmd 'echo hello from ztd'"
  if ! captured operator run --all-active --cmd 'echo hello from ztd'; then
    show_log
    fail "the first governed action failed"
  fi
  sed -e '/^[[:space:]]*$/d' -e 's/^/    /' "$log"
  session=$(awk '$1 == "Operator" && $2 == "session" { print $3; exit }' "$log")
  ok "admitted by the Gateway, executed by the Operator, signed and recorded"

  console=""
  if captured gw status; then
    console=$(awk '$1 == "Console" { print $2; exit }' "$log")
  fi
  printf '\n%s%s✓ ztd is running%s %s(%ss)%s\n\n' "$green" "$bold" "$reset" "$dim" "$(($(date +%s) - started))" "$reset"
  case "$display_dir" in
    *[!A-Za-z0-9_./~-]*) cd_target="'$dir'" ;;
    *) cd_target=$display_dir ;;
  esac
  printf '  %scd %s%s\n' "$bold" "$cd_target" "$reset"
  next_step "./ztd tui" "live governance console in the terminal"
  next_step "./ztd login web" "add a passkey, then open ${console:-https://localhost:8443/console/}"
  printf '  ./ztd gw data audit list --operator-session-id %s\n' "${session:-<id>}"
  next_step "./ztd mcp agent run claude" "put an AI agent behind the Gateway"
  next_step "./ztd gw stop" "stop the Gateway; state in .ztd/ is kept"
  echo
  printf '  %sDocs: https://github.com/zerotrustdata/ztd/blob/main/docs/guides/getting_started.md%s\n' "$dim" "$reset"
}

# Called on the last line, so a truncated download never executes a partial script when piped into sh.
main "$@"
