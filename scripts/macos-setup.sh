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
# Pass --build-only to compile ./ztd with `make build-go` from the committed
# frontend embeds. That mode does not install Node.js.
# See docs/architecture/scripts.md and docs/guides/getting_started.md

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/dev-setup-common.sh
source "${SCRIPT_DIR}/lib/dev-setup-common.sh"

ztd_macos_brew_install() {
    local packages=("$@")
    if ! command -v brew >/dev/null 2>&1; then
        echo "FATAL: Homebrew is not installed. ztd will not download or run the Homebrew installer."
        echo "Install it yourself from https://brew.sh, then rerun: bash scripts/macos-setup.sh"
        echo "Or install these yourself and rerun: ${packages[*]}"
        exit 1
    fi
    brew install "${packages[@]}"
}

# Homebrew stopped publishing bottles for Intel (x86_64), including under
# Rosetta where uname -m is also x86_64. Apple Silicon (arm64) still has bottles.
ztd_macos_intel() {
    case "$(uname -m)" in
        x86_64|amd64) return 0 ;;
        *) return 1 ;;
    esac
}

# Node and Go on Intel come from the official tarballs. `brew install node`
# builds from source there, and that build dies in sphinx-doc because
# Homebrew's Python has no pip.
ztd_macos_install_node_tarball() {
    echo "Node.js ${ZTD_NODE_REQUIREMENT} is required (evaluation explorer, console, protocol/node)."
    echo "  Homebrew no longer ships bottles for Intel macOS. Building node from source pulls sphinx-doc, and that build fails because Homebrew's Python has no pip."
    echo "  This downloads the official Node.js ${ZTD_NODE_LTS_MAJOR}.x LTS tarball (darwin-x64) into /usr/local/lib/nodejs and links node, npm, npx, and corepack into /usr/local/bin."
    if ztd_usr_local_needs_sudo; then
        echo "  /usr/local is not writable by you, so the install uses sudo."
    fi
    if ztd_have curl &&
        ztd_setup_confirm "Download the official Node.js ${ZTD_NODE_LTS_MAJOR}.x LTS tarball now? [y/N] "; then
        ztd_install_official_node darwin || echo "  Node.js install failed; install Node.js ${ZTD_NODE_REQUIREMENT} manually from https://nodejs.org/"
    else
        echo "  Install Node.js ${ZTD_NODE_REQUIREMENT} from https://nodejs.org/ and rerun this script."
    fi
}

ztd_macos_install_go_tarball() {
    local why="$1"
    echo "$why"
    echo "  Homebrew no longer ships bottles for Intel macOS, so this uses the official Go tarball instead of brew."
    if ztd_usr_local_needs_sudo; then
        echo "  /usr/local is not writable by you, so the install uses sudo."
    fi
    if ztd_have curl &&
        ztd_setup_confirm "Download the official Go ${ZTD_GO_MIN} tarball to /usr/local/go now? [y/N] "; then
        ztd_install_official_go darwin || echo "  Go install failed; install it manually from https://go.dev/dl/"
    else
        echo "  Install Go ${ZTD_GO_MIN}+ from https://go.dev/dl/ and rerun this script."
    fi
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
            go)
                if ztd_macos_intel; then
                    ztd_macos_install_go_tarball "Go must satisfy go.mod (currently ${ZTD_GO_MIN})."
                else
                    brew_packages+=("go")
                fi
                ;;
            go-stale)
                if ztd_macos_intel; then
                    ztd_macos_install_go_tarball "System go ${ZTD_GO_STALE} is older than go.mod's ${ZTD_GO_MIN}; every shell and tool outside this repo still sees the old one."
                else
                    echo "System go ${ZTD_GO_STALE} is older than go.mod's ${ZTD_GO_MIN}."
                    if ztd_setup_confirm "Upgrade Go with Homebrew now? [y/N] "; then
                        brew upgrade go || echo "  brew upgrade go failed; upgrade it manually"
                    fi
                fi
                ;;
            node)
                if ztd_macos_intel; then
                    ztd_macos_install_node_tarball
                else
                    brew_packages+=("node")
                fi
                ;;
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
        echo "  Homebrew would install: ${brew_packages[*]}"
        if ztd_macos_intel; then
            echo "  Homebrew no longer ships bottles for Intel macOS. ${brew_packages[*]} may compile from source, and that build can fail."
        fi
        echo "  That writes only under the Homebrew prefix. Your shell profile, Keychain, and trust settings are separate steps, and each asks first."
        if ! command -v brew >/dev/null 2>&1; then
            echo "  Homebrew is not installed. ztd will not download or run its installer."
            echo "  Install Homebrew yourself from https://brew.sh, or install ${brew_packages[*]} another way, then rerun."
            return 0
        fi
        if ztd_setup_confirm "Install with Homebrew now? [y/N] "; then
            ztd_macos_brew_install "${brew_packages[@]}" || echo "  Homebrew install failed; install ${brew_packages[*]} manually and rerun."
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
        ztd_ui_info "Docker Desktop installs into /Applications and adds a privileged helper. Decline anything in its own installer you do not want."
    elif [[ "$1" == chromium ]]; then
        ztd_ui_info "This is the Chromium cask in /Applications. Playwright's browser is separate and stays in your user cache:"
        ztd_ui_info "  (cd evaluation-explorer && npx playwright install chromium)"
    fi
    brew install --cask "$cask" || return 1
    if [[ "$1" == docker ]]; then
        ztd_ui_info "Open Docker from Applications to finish setup when needed."
    fi
}

ztd_setup_init "$@"
ztd_setup_main macOS ztd_macos_install_missing scripts/macos-setup.sh "" ztd_macos_install_optional
