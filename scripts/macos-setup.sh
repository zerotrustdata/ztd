#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# ztd macOS dev setup: install the toolchain, build evaluation-explorer, make
# build, install the contributor toolchain that `make ci` needs (Go dev tools,
# Node deps), and offer to add the repository root to PATH.
# Pass --build-only to stop after `make build`.
# See docs/architecture/scripts.md and docs/guides/getting_started.md

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/dev-setup-common.sh
source "${SCRIPT_DIR}/lib/dev-setup-common.sh"

ztd_macos_brew_install() {
    local packages=("$@")
    if ! command -v brew >/dev/null 2>&1; then
        echo "FATAL: Homebrew is required on macOS. Install it from https://brew.sh"
        exit 1
    fi
    brew install "${packages[@]}"
}

ztd_macos_install_missing() {
    local missing=("$@")
    local brew_packages=()
    local item

    for item in "${missing[@]}"; do
        case "$item" in
            git) brew_packages+=("git") ;;
            make)
                # brew's make installs as gmake; `make` itself ships with the
                # Xcode Command Line Tools, which ztd_check_make looks for.
                echo "make ships with the Xcode Command Line Tools (brew's make is gmake and does not provide 'make'). Install them:"
                echo "  xcode-select --install"
                echo "Finish that installer, then rerun: bash scripts/macos-setup.sh"
                exit 1
                ;;
            go) brew_packages+=("go") ;;
            go-stale)
                echo "System go ${ZTD_GO_STALE} is older than go.mod's ${ZTD_GO_MIN}."
                if ztd_setup_confirm "Upgrade Go with Homebrew now? [y/N] "; then
                    brew upgrade go || echo "  brew upgrade go failed; upgrade it manually"
                fi
                ;;
            node) brew_packages+=("node") ;;
            rg) brew_packages+=("ripgrep") ;;
            bc) brew_packages+=("bc") ;;
            cc)
                echo "A C compiler is required by the Go race detector. Install the Xcode Command Line Tools:"
                echo "  xcode-select --install"
                echo "Finish that installer, then rerun: bash scripts/macos-setup.sh"
                exit 1
                ;;
            curl) echo "curl ships with macOS; check that /usr/bin is on PATH." ;;
        esac
    done

    if [[ ${#brew_packages[@]} -gt 0 ]]; then
        echo "They can be installed via: brew install ${brew_packages[*]}"
        if ztd_setup_confirm "Install with Homebrew now? [y/N] "; then
            ztd_macos_brew_install "${brew_packages[@]}"
        fi
    fi
}

# Called only after the shared optional-tool prompt has been accepted.
ztd_macos_install_optional() {
    if ! ztd_have brew; then
        ztd_ui_info "Homebrew is unavailable; install $1 manually."
        return 1
    fi
    local cask="$1"
    if [[ "$1" == docker ]]; then
        cask="docker-desktop"
    fi
    brew install --cask "$cask" || return 1
    if [[ "$1" == docker ]]; then
        ztd_ui_info "Open Docker from Applications to finish setup when needed."
    fi
}

ztd_setup_init "$@"
ztd_setup_main macOS ztd_macos_install_missing scripts/macos-setup.sh "" ztd_macos_install_optional
