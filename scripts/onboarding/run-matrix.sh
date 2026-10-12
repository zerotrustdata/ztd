#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# Clean-room onboarding matrix: replays a new user's first run of the current
# working tree on fresh Linux hosts in Docker and prints a distro x scenario
# result table. The snapshot is what a clone of this tree would contain
# (tracked plus untracked, non-ignored files), so no local build output,
# runtime state, or toolchain leaks in. Run via `make test-onboarding-docker`.
#
# usage: run-matrix.sh [--distros "ubuntu debian fedora arch"]
#                      [--scenarios "build-only rerun quickstart"]
#                      [--out <dir>] [--keep]
# A distro is a known name (ubuntu, debian, fedora, arch, opensuse) or
# name=image for any other base image. --keep leaves the containers for
# inspection. Scenarios are documented in scripts/onboarding/in-container.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=../lib/dev-setup-common.sh
source "${SCRIPT_DIR}/../lib/dev-setup-common.sh"
ztd_ui_init

DISTROS="ubuntu debian fedora arch"
SCENARIOS="build-only rerun quickstart"
OUT=""
KEEP=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        --distros) DISTROS="$2"; shift 2 ;;
        --scenarios) SCENARIOS="$2"; shift 2 ;;
        --out) OUT="$2"; shift 2 ;;
        --keep) KEEP=true; shift ;;
        -h|--help) sed -n '9,21p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
        *) echo "Unknown option: $1" >&2; exit 2 ;;
    esac
done

ztd_onboarding_image() {
    case "$1" in
        *=*) echo "${1#*=}" ;;
        ubuntu) echo "ubuntu:24.04" ;;
        debian) echo "debian:12" ;;
        fedora) echo "fedora:42" ;;
        arch) echo "archlinux:latest" ;;
        opensuse) echo "opensuse/tumbleweed:latest" ;;
        *) return 1 ;;
    esac
}

if ! docker info >/dev/null 2>&1; then
    ztd_ui_fatal "Docker is not available. Start the Docker daemon, or run the hermetic tests: make test-onboarding"
    exit 1
fi

OUT="${OUT:-$REPO_ROOT/.local.dev/onboarding/$(date -u +%Y%m%dT%H%M%SZ)}"
if [[ -e "$OUT" ]]; then
    ztd_ui_fatal "output directory already exists: $OUT"
    exit 1
fi
mkdir -p "$OUT"
SNAPSHOT="$OUT/src.tar"

ztd_ui_title "ztd onboarding matrix"
ztd_ui_info "distros:   $DISTROS"
ztd_ui_info "scenarios: $SCENARIOS"
ztd_ui_info "evidence:  $OUT"

# Snapshot what a fresh clone of the working tree contains. Tracked files
# deleted in the working tree are skipped, matching a commit of this tree.
(
    cd "$REPO_ROOT"
    git ls-files -z --cached --others --exclude-standard |
        while IFS= read -r -d '' f; do
            [[ -e "$f" || -L "$f" ]] && printf '%s\0' "$f"
        done |
        tar --null -T - -cf "$SNAPSHOT"
)
chmod 0644 "$SNAPSHOT"
ztd_ui_ok "source snapshot $(du -h "$SNAPSHOT" | awk '{print $1}')"

# shellcheck disable=SC2086 # word splitting of the lists is intended
read -r -a SCENARIO_LIST <<<"$SCENARIOS"
read -r -a DISTRO_LIST <<<"$DISTROS"

OVERALL=0
for distro in "${DISTRO_LIST[@]}"; do
    name="${distro%%=*}"
    if ! image="$(ztd_onboarding_image "$distro")"; then
        ztd_ui_fail "$name: unknown distro (use one of ubuntu debian fedora arch opensuse, or name=image)"
        OVERALL=1
        continue
    fi
    tag="ztd-onboarding:$name"
    dest="$OUT/$name"
    mkdir -p "$dest"

    ztd_ui_title "$name ${ZTD_UI_DIM}($image)${ZTD_UI_RESET}"
    if ! docker build --quiet --build-arg "BASE=$image" -t "$tag" "$SCRIPT_DIR" >"$dest/image-build.log" 2>&1; then
        ztd_ui_fail "image build failed (see $dest/image-build.log)"
        OVERALL=1
        continue
    fi

    container="ztd-onboarding-$name-$$"
    started=$SECONDS
    docker run --name "$container" \
        -v "$SNAPSHOT:/src.tar:ro" \
        -v "$SCRIPT_DIR/in-container.sh:/usr/local/bin/ztd-onboarding:ro" \
        "$tag" bash /usr/local/bin/ztd-onboarding "${SCENARIO_LIST[@]}" >"$dest/container.log" 2>&1 || true
    docker cp "$container:/home/newcomer/out/." "$dest/" >/dev/null 2>&1 || true
    if [[ "$KEEP" != true ]]; then
        docker rm -f "$container" >/dev/null 2>&1 || true
    fi

    if [[ ! -s "$dest/results.tsv" ]]; then
        ztd_ui_fail "no results after $((SECONDS - started))s (see $dest/container.log)"
        OVERALL=1
        continue
    fi
    while IFS=$'\t' read -r scenario status seconds detail; do
        if [[ "$status" == PASS ]]; then
            ztd_ui_ok "$(printf '%-14s' "$scenario") ${ZTD_UI_DIM}${seconds}s${ZTD_UI_RESET}"
        else
            ztd_ui_fail "$(printf '%-14s' "$scenario") ${ZTD_UI_DIM}${seconds}s${ZTD_UI_RESET} $detail"
            OVERALL=1
        fi
    done <"$dest/results.tsv"
done

rm -f "$SNAPSHOT"
echo
if [[ "$OVERALL" -eq 0 ]]; then
    echo -e "${ZTD_UI_GREEN}${ZTD_UI_BOLD}${ZTD_UI_OK} onboarding matrix passed${ZTD_UI_RESET} ${ZTD_UI_DIM}($(ztd_ui_elapsed), logs in $OUT)${ZTD_UI_RESET}"
else
    ztd_ui_fatal "onboarding matrix failed. Per-scenario logs: $OUT/<distro>/"
fi
exit "$OVERALL"
