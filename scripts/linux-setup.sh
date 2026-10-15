#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# ztd Linux dev setup: install the toolchain, build evaluation-explorer, make
# build, install the contributor toolchain that `make ci` needs (Go dev tools,
# Node deps), and offer to add the repository root to PATH.
# Pass --build-only to stop after `make build`.
# See docs/architecture/scripts.md and docs/guides/getting_started.md

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/dev-setup-common.sh
source "${SCRIPT_DIR}/lib/dev-setup-common.sh"
# shellcheck source=lib/scratch.sh
source "${SCRIPT_DIR}/lib/scratch.sh"

ZTD_PKG=""
SUDO=""

ztd_linux_detect_pkg_manager() {
    local pm
    for pm in apt-get dnf pacman zypper; do
        if command -v "$pm" >/dev/null 2>&1; then
            ZTD_PKG="${pm%-get}"
            return 0
        fi
    done
    return 1
}

# Sets SUDO to "" for root and "sudo" otherwise; fails when neither applies.
ztd_linux_init_sudo() {
    if [[ "$(id -u)" -eq 0 ]]; then
        SUDO=""
        return 0
    fi
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
        return 0
    fi
    return 1
}

# Maps a logical prerequisite name to the package that provides it.
ztd_linux_pkg_name() {
    case "$ZTD_PKG:$1" in
        apt:cc) echo "build-essential" ;;
        dnf:cc|zypper:cc) echo "gcc" ;;
        pacman:cc) echo "base-devel" ;;
        *:rg) echo "ripgrep" ;;
        *) echo "$1" ;;
    esac
}

ztd_linux_pkg_install() {
    case "$ZTD_PKG" in
        apt) $SUDO apt-get update && $SUDO apt-get install -y "$@" ;;
        dnf) $SUDO dnf install -y "$@" ;;
        pacman) $SUDO pacman -S --noconfirm --needed "$@" ;;
        zypper) $SUDO zypper install -y "$@" ;;
        *) return 1 ;;
    esac
}

# Installs the official Go tarball matching go.mod into /usr/local/go. Works on
# every distro and on WSL; the snap alternative needs a running snapd, which WSL
# often lacks, and distro golang packages are usually too old.
ztd_linux_install_go() {
    local goarch tarball scratch tmp want got
    case "$(uname -m)" in
        x86_64) goarch="amd64" ;;
        aarch64|arm64) goarch="arm64" ;;
        *) echo "  no official Go tarball flow for $(uname -m); install Go from https://go.dev/dl/"; return 1 ;;
    esac
    tarball="go${ZTD_GO_MIN}.linux-${goarch}.tar.gz"
    scratch="$(ztd_scratch_dir "$REPO_ROOT" ztd-go)" || return 1
    tmp="$scratch/${tarball}"

    echo "Downloading ${tarball}..."
    if ! curl -fSL "https://go.dev/dl/${tarball}" -o "$tmp" ||
        ! want="$(curl -fsSL "https://dl.google.com/go/${tarball}.sha256")"; then
        rm -rf "$scratch"
        return 1
    fi
    got="$(ztd_sha256 "$tmp")"
    if [[ "$got" != "$want" ]]; then
        echo "  checksum mismatch for ${tarball} (expected ${want}, got ${got})"
        rm -rf "$scratch"
        return 1
    fi

    echo "Installing Go ${ZTD_GO_MIN} to /usr/local/go (replaces any existing /usr/local/go)..."
    $SUDO rm -rf /usr/local/go
    $SUDO tar -C /usr/local -xzf "$tmp"
    rm -rf "$scratch"
    # Ahead of any older distro Go (e.g. /usr/bin/go) so the re-check sees this one.
    export PATH="/usr/local/go/bin:$PATH"
    hash -r
}

# Node.js release line installed when node is missing; its latest release must
# satisfy ZTD_NODE_REQUIREMENT.
ZTD_NODE_LTS_MAJOR=22

# Installs the newest Node ${ZTD_NODE_LTS_MAJOR}.x LTS tarball from nodejs.org
# into /usr/local/lib/nodejs and links node/npm/npx into /usr/local/bin.
# Ubuntu's apt nodejs is older than the supported lines.
ztd_linux_install_node() {
    local nodearch sums tarball scratch tmp want got dest
    case "$(uname -m)" in
        x86_64) nodearch="x64" ;;
        aarch64|arm64) nodearch="arm64" ;;
        *) echo "  no official Node.js tarball flow for $(uname -m); install Node.js ${ZTD_NODE_REQUIREMENT} from https://nodejs.org/"; return 1 ;;
    esac
    local base="https://nodejs.org/dist/latest-v${ZTD_NODE_LTS_MAJOR}.x"
    sums="$(curl -fsSL "${base}/SHASUMS256.txt")" || return 1
    tarball="$(echo "$sums" | awk -v arch="linux-${nodearch}.tar.gz" '$2 ~ arch"$" {print $2; exit}')"
    want="$(echo "$sums" | awk -v t="$tarball" '$2 == t {print $1}')"
    if [[ -z "$tarball" || -z "$want" ]]; then
        echo "  could not find a Node ${ZTD_NODE_LTS_MAJOR}.x linux-${nodearch} tarball in ${base}/SHASUMS256.txt"
        return 1
    fi

    scratch="$(ztd_scratch_dir "$REPO_ROOT" ztd-node)" || return 1
    tmp="$scratch/${tarball}"
    echo "Downloading ${tarball}..."
    if ! curl -fSL "${base}/${tarball}" -o "$tmp"; then
        rm -rf "$scratch"
        return 1
    fi
    got="$(ztd_sha256 "$tmp")"
    if [[ "$got" != "$want" ]]; then
        echo "  checksum mismatch for ${tarball} (expected ${want}, got ${got})"
        rm -rf "$scratch"
        return 1
    fi

    dest="/usr/local/lib/nodejs"
    echo "Installing ${tarball%.tar.gz} to ${dest}..."
    $SUDO mkdir -p "$dest"
    $SUDO tar -C "$dest" -xzf "$tmp"
    rm -rf "$scratch"
    for bin in node npm npx corepack; do
        $SUDO ln -sf "${dest}/${tarball%.tar.gz}/bin/${bin}" "/usr/local/bin/${bin}"
    done
    hash -r
}

ztd_linux_install_missing() {
    local item
    local -a pkgs
    pkgs=()

    for item in "$@"; do
        case "$item" in
            git|make|curl|rg|bc|cc) pkgs+=("$(ztd_linux_pkg_name "$item")") ;;
        esac
    done

    if [[ ${#pkgs[@]} -gt 0 ]]; then
        if ! ztd_linux_detect_pkg_manager; then
            echo "No supported package manager (apt, dnf, pacman, zypper) found."
            echo "Install manually: ${pkgs[*]}"
        elif ! ztd_linux_init_sudo; then
            echo "Need root to install packages but 'sudo' is not available."
            echo "As root, run: ${ZTD_PKG} install ${pkgs[*]}"
        elif ztd_setup_confirm "Install ${pkgs[*]} with ${ZTD_PKG} now? [y/N] "; then
            ztd_linux_pkg_install "${pkgs[@]}" || echo "  package install failed; install ${pkgs[*]} manually"
        fi
    fi

    for item in "$@"; do
        case "$item" in
            go)
                echo "Go must satisfy go.mod (currently $ZTD_GO_MIN)."
                if ztd_linux_init_sudo && ztd_have curl &&
                    ztd_setup_confirm "Download the official Go ${ZTD_GO_MIN} tarball to /usr/local/go now? [y/N] "; then
                    ztd_linux_install_go || echo "  Go install failed; install it manually from https://go.dev/dl/"
                else
                    echo "  Install Go ${ZTD_GO_MIN}+ from https://go.dev/dl/ and rerun this script."
                fi
                ;;
            node)
                echo "Node.js ${ZTD_NODE_REQUIREMENT} is required (evaluation explorer, console, protocol/node)."
                if ztd_linux_init_sudo && ztd_have curl &&
                    ztd_setup_confirm "Download the official Node.js ${ZTD_NODE_LTS_MAJOR}.x LTS tarball to /usr/local/lib/nodejs now? [y/N] "; then
                    ztd_linux_install_node || echo "  Node.js install failed; install Node.js ${ZTD_NODE_REQUIREMENT} manually from https://nodejs.org/"
                else
                    echo "  Install Node.js ${ZTD_NODE_REQUIREMENT} from https://nodejs.org/ and rerun this script."
                fi
                ;;
        esac
    done
}

ztd_setup_init "$@"
ztd_setup_main Linux ztd_linux_install_missing scripts/linux-setup.sh
