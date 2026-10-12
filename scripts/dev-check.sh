#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# Preflight for `make ci`: reports every missing or mismatched tool in one pass
# instead of letting the pipeline die on the first one. Run via `make dev-check`;
# the ci targets depend on it. See docs/architecture/scripts.md.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/dev-setup-common.sh
source "${SCRIPT_DIR}/lib/dev-setup-common.sh"

ztd_setup_init

# When invoked from make, PATH already carries the Go bin dir and ~/.local/bin.
# When run directly, add them so a fresh install is found before the profile is
# sourced.
GO_BIN_DIR="$(go env GOBIN 2>/dev/null)"
if [[ -z "$GO_BIN_DIR" ]] && command -v go >/dev/null 2>&1; then
    GO_BIN_DIR="$(go env GOPATH)/bin"
fi
if command -v cygpath >/dev/null 2>&1 && [[ -n "$GO_BIN_DIR" ]]; then
    GO_BIN_DIR="$(cygpath -u "$GO_BIN_DIR")"
fi
USER_HOME="${HOME:-${USERPROFILE:-}}"
if command -v cygpath >/dev/null 2>&1 && [[ -n "$USER_HOME" ]]; then
    USER_HOME="$(cygpath -u "$USER_HOME")"
fi
case "$(uname -s):${OS:-}" in
    MINGW*|MSYS*|CYGWIN*|*:Windows_NT)
        export PATH="${GO_BIN_DIR:-$USER_HOME/go/bin}:$USER_HOME/.local/bin:/usr/bin:$PATH"
        ;;
    *)
        export PATH="${GO_BIN_DIR:-$USER_HOME/go/bin}:$USER_HOME/.local/bin:$PATH"
        ;;
esac

FAILED=0

ztd_ui_title "Prerequisites ${ZTD_UI_DIM}(git, make, curl, go >= ${ZTD_GO_MIN}, node >= ${ZTD_NODE_MIN_MAJOR}, rg, bc, C compiler)${ZTD_UI_RESET}"
ztd_collect_missing
if [[ ${#MISSING[@]} -gt 0 ]]; then
    FAILED=1
fi

ztd_ui_title "Go dev tools"
ztd_check_go_tools || FAILED=1

ztd_ui_title "Node dependencies"
ztd_check_node_deps || FAILED=1

echo
if [[ "$FAILED" -ne 0 ]]; then
    case "$(uname -s)" in
        Darwin) setup="bash scripts/macos-setup.sh" ;;
        Linux) setup="bash scripts/linux-setup.sh" ;;
        *) setup="pwsh scripts/windows-setup.ps1 (or run linux-setup.sh inside WSL)" ;;
    esac
    ztd_ui_fatal "the toolchain 'make ci' needs is incomplete."
    echo "  Fix everything above in one step with: $setup" >&2
    echo "  (Go tools and Node deps alone: make dev-setup)" >&2
    exit 1
fi
echo -e "${ZTD_UI_GREEN}${ZTD_UI_BOLD}${ZTD_UI_OK} dev-check passed:${ZTD_UI_RESET} everything 'make ci' needs is installed."
