# shellcheck shell=bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# Shared helpers for the Unix dev-setup scripts and scripts/dev-check.sh. Source
# from linux-setup.sh, macos-setup.sh, or dev-check.sh after setting SCRIPT_DIR.
# Must stay compatible with bash 3.2 (the macOS system bash): no associative
# arrays, no mapfile, no ${var,,}.
#
# Tool versions are NOT defined here. Go comes from go.mod; every other pin
# (buf, protoc plugins, golangci-lint) is read from the root Makefile
# so there is a single place to bump them (see ztd_make_var).

# Node.js lines the frontend toolchains accept. This mirrors the strictest
# frontend engines range (jsdom: ^22.22.2 || ^24.15.0 || >=26.0.0); keep it in
# step with console/package.json "engines".
ZTD_NODE_REQUIREMENT="22.22.2+, 24.15+, or 26+"
ZTD_EXPLORER_DIR="evaluation-explorer"
ZTD_EXPLORER_DIST="${ZTD_EXPLORER_DIR}/dist/index.html"
ZTD_PATH_MARKER="# ztd: add repository root to PATH"

# --- terminal output -------------------------------------------------------------
# Color and glyphs only on an interactive terminal that has not opted out
# (NO_COLOR, TERM=dumb); logs and CI get plain ASCII with the same words.

ztd_ui_init() {
    ZTD_UI_STARTED=$SECONDS
    if [[ -t 1 && -z "${NO_COLOR:-}" && "${TERM:-dumb}" != dumb ]]; then
        ZTD_UI_BOLD=$'\033[1m' ZTD_UI_DIM=$'\033[2m' ZTD_UI_RESET=$'\033[0m'
        ZTD_UI_GREEN=$'\033[32m' ZTD_UI_YELLOW=$'\033[33m' ZTD_UI_RED=$'\033[31m' ZTD_UI_CYAN=$'\033[36m'
        ZTD_UI_OK="✓" ZTD_UI_WARN="!" ZTD_UI_FAIL="✗" ZTD_UI_ARROW="→"
    else
        ZTD_UI_BOLD="" ZTD_UI_DIM="" ZTD_UI_RESET=""
        ZTD_UI_GREEN="" ZTD_UI_YELLOW="" ZTD_UI_RED="" ZTD_UI_CYAN=""
        ZTD_UI_OK="ok" ZTD_UI_WARN="!!" ZTD_UI_FAIL="xx" ZTD_UI_ARROW="->"
    fi
}

ztd_ui_elapsed() {
    local s=$((SECONDS - ${ZTD_UI_STARTED:-$SECONDS}))
    if (( s >= 60 )); then
        echo "$((s / 60))m$((s % 60))s"
    else
        echo "${s}s"
    fi
}

ztd_ui_title() { echo -e "\n${ZTD_UI_BOLD}$*${ZTD_UI_RESET}"; }
# usage: ztd_ui_step <n> <total> <title>
ztd_ui_step() { echo -e "\n${ZTD_UI_CYAN}${ZTD_UI_BOLD}[$1/$2]${ZTD_UI_RESET} ${ZTD_UI_BOLD}$3${ZTD_UI_RESET} ${ZTD_UI_DIM}($(ztd_ui_elapsed))${ZTD_UI_RESET}"; }
ztd_ui_ok() { echo -e "  ${ZTD_UI_GREEN}${ZTD_UI_OK}${ZTD_UI_RESET} $*"; }
ztd_ui_warn() { echo -e "  ${ZTD_UI_YELLOW}${ZTD_UI_WARN}${ZTD_UI_RESET} $*"; }
ztd_ui_fail() { echo -e "  ${ZTD_UI_RED}${ZTD_UI_FAIL}${ZTD_UI_RESET} $*"; }
ztd_ui_info() { echo -e "  ${ZTD_UI_DIM}${ZTD_UI_ARROW}${ZTD_UI_RESET} $*"; }
ztd_ui_fatal() { echo -e "\n${ZTD_UI_RED}${ZTD_UI_BOLD}FATAL:${ZTD_UI_RESET} $*" >&2; }

# Reads `NAME := value` from the root Makefile.
ztd_make_var() {
    sed -n "s/^$1[[:space:]]*:=[[:space:]]*//p" Makefile | head -1 | tr -d '[:space:]'
}

ztd_usage() {
    cat <<EOF
Usage: bash scripts/$(basename "$0") [options]

Installs the toolchain needed to build ztd and, by default, to run 'make ci'
(Go dev tools and Node dependencies).

Options:
  -y, --yes       Install missing tooling without prompting
  --build-only    Only set up what 'make build' needs; skip the contributor toolchain
  -h, --help      Show this help
EOF
}

ztd_setup_init() {
    ztd_ui_init
    REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
    cd "$REPO_ROOT" || return 1

    if [[ ! -f go.mod ]]; then
        echo "FATAL: run this script from a ztd repository clone (go.mod not found)."
        exit 1
    fi

    ZTD_GO_MIN="$(sed -n 's/^go //p' go.mod | head -1 | tr -d '[:space:]')"
    if [[ -z "$ZTD_GO_MIN" ]]; then
        echo "FATAL: could not read the required Go version from go.mod."
        exit 1
    fi

    AUTO_YES=false
    ZTD_BUILD_ONLY=false
    for arg in "$@"; do
        case "$arg" in
            -y|--yes) AUTO_YES=true ;;
            --build-only) ZTD_BUILD_ONLY=true ;;
            -h|--help) ztd_usage; exit 0 ;;
            *) echo "Unknown option: $arg"; ztd_usage; exit 2 ;;
        esac
    done
}

ztd_setup_confirm() {
    local prompt="$1"
    if [[ "$AUTO_YES" == true ]]; then
        return 0
    fi
    if [[ ! -t 0 ]]; then
        echo "ERROR: cannot prompt for confirmation: stdin is not a terminal." >&2
        echo "Re-run with -y/--yes to accept all installs non-interactively," >&2
        echo "or run in an interactive terminal to answer each prompt." >&2
        exit 2
    fi
    read -r -p "$prompt" response
    [[ "$response" =~ ^[Yy]$ ]]
}

# Pulls the first dotted number out of a tool's version banner:
# "go version go1.26.6 linux/amd64" -> 1.26.6, "v22.23.3" -> 22.23.3.
ztd_extract_version() {
    echo "$1" | sed -En 's/^[^0-9]*([0-9]+(\.[0-9]+)*).*/\1/p' | head -1
}

# True when $1 >= $2, comparing up to three dotted components. A missing
# component counts as 0, so "1.26" is older than "1.26.6".
ztd_version_ge() {
    local IFS=.
    local -a have need
    local i h n
    # Splitting on the dots (IFS=.) is the point.
    # shellcheck disable=SC2206
    have=($1)
    # shellcheck disable=SC2206
    need=($2)
    for i in 0 1 2; do
        h="${have[$i]:-0}"
        n="${need[$i]:-0}"
        if (( 10#$h > 10#$n )); then
            return 0
        fi
        if (( 10#$h < 10#$n )); then
            return 1
        fi
    done
    return 0
}

# True when `command` resolves to a native tool. On WSL the Windows PATH is
# appended to the Linux one and exposes extensionless Windows shims (npm, npx);
# those must not count as an installed Linux toolchain.
ztd_have() {
    local resolved
    resolved="$(command -v "$1" 2>/dev/null)" || return 1
    case "$resolved" in
        /mnt/[a-z]/*) return 1 ;;
    esac
    return 0
}

ztd_sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

# --- prerequisite checks: print one line each, return 1 when missing -----------

ztd_check_git() {
    if ztd_have git; then
        ztd_ui_ok "git $(git --version | awk '{print $3}')"
        return 0
    fi
    ztd_ui_fail "git: missing"
    return 1
}

ztd_check_make() {
    if ztd_have make; then
        ztd_ui_ok "make"
        return 0
    fi
    ztd_ui_fail "make: missing"
    return 1
}

ztd_check_curl() {
    if ztd_have curl; then
        ztd_ui_ok "curl"
        return 0
    fi
    ztd_ui_fail "curl: missing (used to download Go and Node.js)"
    return 1
}

ztd_check_go() {
    if ! ztd_have go; then
        ztd_ui_fail "go: missing (need >= $ZTD_GO_MIN from go.mod)"
        return 1
    fi

    local version
    version="$(ztd_extract_version "$(go version)")"
    if [[ -z "$version" ]]; then
        ztd_ui_fail "go: detected but version unknown"
        return 1
    fi
    if ztd_version_ge "$version" "$ZTD_GO_MIN"; then
        ztd_ui_ok "go $version (need >= $ZTD_GO_MIN)"
        return 0
    fi
    ztd_ui_fail "go: $version is older than $ZTD_GO_MIN required by go.mod"
    return 1
}

# ztd_node_version_supported VERSION: true when VERSION is in a supported line.
ztd_node_version_supported() {
    local major="${1%%.*}"
    case "$major" in
        22) ztd_version_ge "$1" "22.22.2" ;;
        24) ztd_version_ge "$1" "24.15.0" ;;
        *) (( 10#$major >= 26 )) ;;
    esac
}

ztd_check_node() {
    if ! ztd_have node; then
        ztd_ui_fail "node: missing (need $ZTD_NODE_REQUIREMENT for the evaluation-explorer and console builds)"
        return 1
    fi
    if ! ztd_have npm; then
        ztd_ui_fail "npm: missing (an npm found only under /mnt/<drive> is a Windows shim and does not count)"
        return 1
    fi

    local version
    version="$(ztd_extract_version "$(node --version)")"
    if [[ -z "$version" ]]; then
        ztd_ui_fail "node: detected but version unknown"
        return 1
    fi
    if ztd_node_version_supported "$version"; then
        ztd_ui_ok "node $version, npm $(npm --version)"
        return 0
    fi
    ztd_ui_fail "node: $version is not supported (need $ZTD_NODE_REQUIREMENT)"
    return 1
}

ztd_check_ripgrep() {
    if ztd_have rg; then
        ztd_ui_ok "rg"
        return 0
    fi
    ztd_ui_fail "rg: missing (ripgrep)"
    return 1
}

ztd_check_bc() {
    if ztd_have bc; then
        ztd_ui_ok "bc"
        return 0
    fi
    case "$(uname -s)" in
        MINGW*|MSYS*|CYGWIN*)
            ztd_ui_ok "bc: not required on Windows (awk fallback used)"
            return 0
            ;;
    esac
    ztd_ui_fail "bc: missing (used by the make ci coverage threshold)"
    return 1
}

# 'go test -race' (make ci, make test-integration) needs cgo, hence a C compiler.
ztd_check_cc() {
    local cc
    for cc in cc gcc clang; do
        if ztd_have "$cc"; then
            ztd_ui_ok "C compiler ($cc; required by the Go race detector)"
            return 0
        fi
    done
    case "$(uname -s)" in
        MINGW*|MSYS*|CYGWIN*)
            ztd_ui_ok "C compiler: not required on Windows (the Go race detector is disabled on Windows)"
            return 0
            ;;
    esac
    ztd_ui_fail "C compiler: missing (required by 'go test -race')"
    return 1
}

# Fills MISSING with the logical names of absent prerequisites. --build-only
# checks only what `make build` needs: it embeds the committed Console and
# Evaluation Explorer assets, so Node.js belongs to the contributor toolchain.
ztd_collect_missing() {
    MISSING=()
    ztd_check_git || MISSING+=("git")
    ztd_check_make || MISSING+=("make")
    ztd_check_curl || MISSING+=("curl")
    ztd_check_go || MISSING+=("go")
    if [[ "$ZTD_BUILD_ONLY" != true ]]; then
        ztd_check_node || MISSING+=("node")
        ztd_check_ripgrep || MISSING+=("rg")
        ztd_check_bc || MISSING+=("bc")
        ztd_check_cc || MISSING+=("cc")
    fi
}

# --- contributor-toolchain checks (used by dev-check.sh and dev-tools-if-needed) -

# Reads the exact module version and Go build version of a Go binary via
# `go version -m`. Returns 1 if the binary cannot be resolved or does not
# expose Go build information.
# Outputs: "<mod_version> <go_version>", e.g. "v1.73.0 1.27.3".
ztd_tool_version() {
    local target="$1" resolved
    if [[ -f "$target" ]]; then
        resolved="$target"
    else
        resolved="$(command -v "$target" 2>/dev/null)" || return 1
    fi
    if [[ "$resolved" != *.exe && -f "${resolved}.exe" ]]; then
        resolved="${resolved}.exe"
    fi
    [[ -f "$resolved" ]] || return 1

    local info line1 go_ver mod_ver
    info="$(go version -m "$resolved" 2>/dev/null)" || return 1
    line1="$(echo "$info" | head -1)"
    go_ver="$(echo "$line1" | sed -En 's/.*:[[:space:]]*(go|go version go)?([0-9]+(\.[0-9]+)*).*/\2/p')"
    mod_ver="$(echo "$info" | awk '$1 == "mod" { print $3; exit }')"
    echo "${mod_ver:-unknown} ${go_ver:-unknown}"
}

# Checks a Go-installed tool, optionally against a pinned version and minimum Go
# build version. A version or build-version mismatch warns rather than fails.
# usage: ztd_check_tool <command> [pinned-version] [version-flag]
ztd_check_tool() {
    local cmd="$1" pinned="${2:-}"
    pinned="${pinned#v}"
    if ! ztd_have "$cmd"; then
        ztd_ui_fail "$cmd: missing (run: make dev-tools)"
        return 1
    fi

    local versions mod_ver go_ver
    if ! versions="$(ztd_tool_version "$cmd")"; then
        local flag="${3:---version}" got
        got="$(ztd_extract_version "$("$cmd" "$flag" 2>&1 | head -1)")"
        if [[ -z "$pinned" ]]; then
            ztd_ui_ok "$cmd"
            return 0
        fi
        if [[ "$got" == "$pinned" ]]; then
            ztd_ui_ok "$cmd $got"
        else
            ztd_ui_warn "$cmd ${got:-unknown} (CI pins v$pinned; run: make dev-tools)"
        fi
        return 0
    fi

    mod_ver="${versions%% *}"
    go_ver="${versions##* }"
    local mod_clean="${mod_ver#v}"

    if [[ -n "$pinned" && "$mod_clean" != "$pinned" ]]; then
        ztd_ui_warn "$cmd ${mod_ver:-unknown} (CI pins v$pinned; run: make dev-tools)"
        return 0
    fi

    local min_go="${ZTD_GO_MIN:-}"
    if [[ -z "$min_go" && -f go.mod ]]; then
        min_go="$(sed -n 's/^go //p' go.mod | head -1 | tr -d '[:space:]')"
    fi
    if [[ -n "$min_go" && "$go_ver" != "unknown" ]] && ! ztd_version_ge "$go_ver" "$min_go"; then
        ztd_ui_warn "$cmd ${mod_ver:-unknown} built with go $go_ver (need >= $min_go; run: make dev-tools)"
        return 0
    fi

    ztd_ui_ok "$cmd ${mod_clean:-$mod_ver}"
    return 0
}

ztd_check_go_tools() {
    local failed=0
    ztd_check_tool buf "$(ztd_make_var BUF_VERSION)" || failed=1
    ztd_check_tool protoc-gen-go "$(ztd_make_var PROTOC_GEN_GO_VERSION)" || failed=1
    ztd_check_tool protoc-gen-doc "$(ztd_make_var PROTOC_GEN_DOC_VERSION)" || failed=1
    ztd_check_tool golangci-lint "$(ztd_make_var GOLANGCI_LINT_VERSION)" || failed=1
    ztd_check_tool govulncheck "$(ztd_make_var GOVULNCHECK_VERSION)" || failed=1
    ztd_check_tool swag "$(ztd_make_var SWAG_VERSION)" || failed=1
    return $failed
}

# Installs or refreshes a Go developer tool if missing, if pinned version
# mismatches, or if built with an older Go version than go.mod requires.
# Fails after install if PATH still resolves a stale copy elsewhere outside GOBIN.
# usage: ztd_dev_tool_if_needed <pkg-with-version>
ztd_dev_tool_if_needed() {
    local pkg="$1"
    local bin="${pkg%@*}"; bin="${bin##*/}"
    local pinned="${pkg##*@}"
    local need_install=false
    local reason=""

    local min_go="${ZTD_GO_MIN:-}"
    if [[ -z "$min_go" && -f go.mod ]]; then
        min_go="$(sed -n 's/^go //p' go.mod | head -1 | tr -d '[:space:]')"
    fi

    if ! ztd_have "$bin"; then
        need_install=true
        reason="missing"
    else
        local versions mod_ver go_ver
        if ! versions="$(ztd_tool_version "$bin")"; then
            need_install=true
            reason="build info unavailable"
        else
            mod_ver="${versions%% *}"
            go_ver="${versions##* }"
            local mod_clean="${mod_ver#v}" pin_clean="${pinned#v}"
            if [[ "$mod_clean" != "$pin_clean" ]]; then
                need_install=true
                reason="version mismatch: got ${mod_ver:-unknown}, pinned $pinned"
            elif [[ -n "$min_go" && "$go_ver" != "unknown" ]] && ! ztd_version_ge "$go_ver" "$min_go"; then
                need_install=true
                reason="built with older Go $go_ver (need >= $min_go)"
            fi
        fi
    fi

    if [[ "$need_install" != true ]]; then
        echo "  $bin: already installed (${mod_ver:-current}) — skipping"
        return 0
    fi

    echo "  installing $bin ($pkg; $reason)..."
    # `go install pkg@version` resolves the toolchain from the tool's go.mod, not
    # ours, so GOTOOLCHAIN=auto would build with an older local Go. Require ours.
    local toolchain="${GOTOOLCHAIN:-auto}"
    if [[ -n "$min_go" ]]; then
        toolchain="go${min_go}+auto"
    fi
    GOTOOLCHAIN="$toolchain" go install "$pkg" || return 1
    hash -d "$bin" 2>/dev/null || hash -r 2>/dev/null || true

    local gobin
    gobin="$(go env GOBIN 2>/dev/null)"
    if [[ -z "$gobin" ]]; then
        gobin="$(go env GOPATH 2>/dev/null)/bin"
    fi
    if command -v cygpath >/dev/null 2>&1 && [[ -n "$gobin" ]]; then
        gobin="$(cygpath -u "$gobin")"
    fi

    local resolved
    resolved="$(command -v "$bin" 2>/dev/null)"
    if [[ -z "$resolved" ]]; then
        echo "FATAL: $bin was installed but cannot be found on PATH." >&2
        return 1
    fi

    local post_versions post_mod post_go
    post_versions="$(ztd_tool_version "$bin" 2>/dev/null)" || post_versions="unknown unknown"
    post_mod="${post_versions%% *}"
    post_go="${post_versions##* }"
    local post_mod_clean="${post_mod#v}" pin_clean="${pinned#v}"

    local is_stale=false
    if [[ "$post_mod_clean" != "$pin_clean" ]]; then
        is_stale=true
    elif [[ -n "$min_go" && "$post_go" != "unknown" ]] && ! ztd_version_ge "$post_go" "$min_go"; then
        is_stale=true
    fi

    if [[ "$is_stale" == true ]]; then
        echo "FATAL: after installing $pkg, PATH resolves $bin to '$resolved' (version ${post_mod:-unknown}, built with go ${post_go:-unknown})." >&2
        echo "The resolved binary is stale (pinned $pinned, need go >= $min_go)." >&2
        shopt -s nocasematch 2>/dev/null
        if [[ "$resolved" != "$gobin"/* ]]; then
            echo "A stale copy outside GOBIN ($gobin) is shadowing the installed tool." >&2
            echo "Remove '$resolved' or ensure GOBIN is placed ahead of it in PATH." >&2
        fi
        return 1
    fi
    return 0
}

# npm's hidden lockfile is written after installation. Reuse the tree only
# while both manifests are older and npm can resolve the declared packages.
ztd_node_deps_current() {
    local dir="$1" marker="$1/node_modules/.package-lock.json"
    [[ -f "$marker" && -f "$dir/package.json" ]] || return 1
    [[ "$dir/package.json" -nt "$marker" ]] && return 1
    [[ -f "$dir/package-lock.json" && "$dir/package-lock.json" -nt "$marker" ]] && return 1
    npm ls --prefix "$dir" --depth=0 --offline >/dev/null 2>&1
}

ztd_check_node_deps() {
    local failed=0 dir
    for dir in console protocol/node ztd-adapter website; do
        if [[ -d "$dir/node_modules" ]]; then
            ztd_ui_ok "$dir/node_modules"
        else
            ztd_ui_fail "$dir/node_modules: missing (run: make dev-node)"
            failed=1
        fi
    done
    if [[ ! -f "$ZTD_EXPLORER_DIST" ]]; then
        ztd_ui_fail "evaluation-explorer dist: missing (run the setup script, or: cd $ZTD_EXPLORER_DIR && npm ci && npm run build)"
        failed=1
    fi
    return $failed
}

# --- installers shared by the Linux and macOS scripts --------------------------

ztd_build_evaluation_explorer() {
    if [[ -f "$ZTD_EXPLORER_DIST" ]]; then
        ztd_ui_ok "evaluation-explorer dist already present — skipping frontend build"
        return 0
    fi

    ztd_ui_info "building evaluation-explorer assets (required by make dev-check)..."
    local deps_current=false
    if ztd_node_deps_current "$ZTD_EXPLORER_DIR"; then
        deps_current=true
    fi
    pushd "$ZTD_EXPLORER_DIR" >/dev/null || return 1
    if [[ "$deps_current" != true ]]; then
        if [[ -f package-lock.json ]]; then
            npm ci
        else
            npm install
        fi
    else
        ztd_ui_ok "evaluation-explorer dependencies are current — skipping npm install"
    fi
    npm run build
    popd >/dev/null || return 1
}

ztd_run_make_build() {
    make build
}

# Creates the owner-only .env that every ./ztd command loads, from the
# commented .env.example template. An existing .env is kept.
ztd_create_env_file() {
    if [[ -e .env ]]; then
        ztd_ui_ok "kept existing .env"
        return
    fi
    (umask 077 && cp .env.example .env)
    ztd_ui_ok "created .env from .env.example (mode 600)"
}

ztd_run_dev_setup() {
    make dev-setup
}

# --- PATH ------------------------------------------------------------------------

ZTD_PROFILE_FILE=""

ztd_select_profile() {
    if [[ -n "${ZSH_VERSION:-}" || "${SHELL:-}" == */zsh ]]; then
        ZTD_PROFILE_FILE="$HOME/.zshrc"
    elif [[ -n "${BASH_VERSION:-}" || "${SHELL:-}" == */bash ]]; then
        ZTD_PROFILE_FILE="$HOME/.bashrc"
    else
        ZTD_PROFILE_FILE="$HOME/.profile"
    fi
}

# PATH entries the profile does not mention yet, as "<mode>|<dir>|<comment>".
ZTD_PATH_PLAN=()
# true once every planned directory is in the profile (added now or earlier).
ZTD_PATH_CONFIGURED=false

# True when the profile already mentions <dir> literally or as $HOME/...,
# ${HOME}/..., or ~/...
ztd_profile_mentions() {
    local dir="$1" rel variant
    local -a variants
    variants=("$dir")
    rel="${dir#"$HOME"/}"
    if [[ "$rel" != "$dir" ]]; then
        # The tilde is matched literally: it is how a profile spells the path.
        # shellcheck disable=SC2088
        variants+=("\$HOME/$rel" "\${HOME}/$rel" "~/$rel")
    fi
    [[ -f "$ZTD_PROFILE_FILE" ]] || return 1
    for variant in "${variants[@]}"; do
        # Whole-path match only: /home/x/ztd-old or /home/x/ztd/bin does not
        # mention /home/x/ztd. ENVIRON avoids awk's -v escape processing.
        if ZTD_VARIANT="$variant" awk '
            BEGIN { v = ENVIRON["ZTD_VARIANT"]; n = length(v) }
            {
                s = $0
                while ((i = index(s, v)) > 0) {
                    c = substr(s, i + n, 1)
                    if (c == "" || index(":\"'"'"'; \t", c) > 0) { found = 1; exit }
                    s = substr(s, i + 1)
                }
            }
            END { exit !found }' "$ZTD_PROFILE_FILE"; then
            return 0
        fi
    done
    return 1
}

# Queues <dir> for the profile unless the profile already mentions it. Pass
# "prepend" when <dir> must win over an older tool already on PATH (the Go
# tarball in /usr/local/go versus a distro golang).
# usage: ztd_plan_path_entry <dir> <comment> [prepend]
ztd_plan_path_entry() {
    if ztd_profile_mentions "$1"; then
        ztd_ui_ok "$1 already on PATH in $ZTD_PROFILE_FILE"
        return 0
    fi
    ZTD_PATH_PLAN+=("${3:-append}|$1|$2")
}

ztd_path_export_line() {
    if [[ "$1" == "prepend" ]]; then
        echo "export PATH=\"$2:\$PATH\""
    else
        echo "export PATH=\"\$PATH:$2\""
    fi
}

# Shows the planned profile lines and appends them only after the user agrees
# (or passed --yes). Profiles are only ever appended to, never rewritten. A
# non-interactive run without --yes leaves the profile unchanged rather than
# failing a setup that already built ztd.
ztd_configure_path_unix() {
    ztd_select_profile
    ZTD_PATH_PLAN=()
    ZTD_PATH_CONFIGURED=false
    ztd_plan_path_entry "$REPO_ROOT" "$ZTD_PATH_MARKER"

    if [[ "$ZTD_BUILD_ONLY" != true ]]; then
        # Directories the contributor toolchain installs into. Only those that
        # exist are added, so an unused custom GOPATH/GOBIN is not written into
        # the profile for nothing.
        local gobin
        if ztd_have go; then
            gobin="$(go env GOBIN)"
            if [[ -z "$gobin" ]]; then
                gobin="$(go env GOPATH)/bin"
            fi
            if [[ -d "$gobin" ]]; then
                ztd_plan_path_entry "$gobin" "# ztd: Go-installed dev tools (buf, protoc plugins, golangci-lint, ...)"
            fi
        fi
    fi
    # Go is a build prerequisite, so the tarball install needs PATH in both
    # modes; without it a new shell (and a setup rerun) cannot find Go.
    if [[ "$(command -v go 2>/dev/null)" == /usr/local/go/bin/go ]]; then
        ztd_plan_path_entry /usr/local/go/bin "# ztd: Go toolchain" prepend
    fi

    if [[ ${#ZTD_PATH_PLAN[@]} -eq 0 ]]; then
        ZTD_PATH_CONFIGURED=true
        return 0
    fi

    local entry mode dir comment
    echo "  Proposed additions to $ZTD_PROFILE_FILE:"
    for entry in "${ZTD_PATH_PLAN[@]}"; do
        IFS='|' read -r mode dir comment <<<"$entry"
        echo "    $(ztd_path_export_line "$mode" "$dir")"
    done

    if [[ "$AUTO_YES" != true && ! -t 0 ]]; then
        echo "  stdin is not a terminal; leaving $ZTD_PROFILE_FILE unchanged (pass --yes to apply)."
        return 0
    fi
    if ! ztd_setup_confirm "Update PATH in $ZTD_PROFILE_FILE? [y/N] "; then
        echo "  Left $ZTD_PROFILE_FILE unchanged."
        return 0
    fi

    for entry in "${ZTD_PATH_PLAN[@]}"; do
        IFS='|' read -r mode dir comment <<<"$entry"
        {
            echo ""
            echo "$comment"
            ztd_path_export_line "$mode" "$dir"
        } >>"$ZTD_PROFILE_FILE"
        ztd_ui_ok "added $dir to PATH in $ZTD_PROFILE_FILE"
    done
    ZTD_PATH_CONFIGURED=true
}

ztd_print_next_steps_unix() {
    local binary="./ztd"
    if [[ ! -f "$REPO_ROOT/ztd" && -f "$REPO_ROOT/ztd.exe" ]]; then
        binary="./ztd.exe"
    fi

    echo -e "\n${ZTD_UI_GREEN}${ZTD_UI_BOLD}${ZTD_UI_OK} ztd is ready${ZTD_UI_RESET} ${ZTD_UI_DIM}($(ztd_ui_elapsed))${ZTD_UI_RESET}\n"
    echo -e "  ${ZTD_UI_BOLD}Next${ZTD_UI_RESET}      make quickstart"
    echo -e "            ${ZTD_UI_DIM}starts a local Gateway, signs you in, and runs your first governed action${ZTD_UI_RESET}"
    echo -e "  ${ZTD_UI_BOLD}Configure${ZTD_UI_RESET} .env     ${ZTD_UI_DIM}optional: remote Ollama endpoint, Cloudflare token; ./ztd loads it at startup${ZTD_UI_RESET}"
    if [[ "$ZTD_BUILD_ONLY" != true ]]; then
        echo -e "  ${ZTD_UI_BOLD}Develop${ZTD_UI_RESET}   make ci      ${ZTD_UI_DIM}full local CI${ZTD_UI_RESET}"
        echo -e "            make help    ${ZTD_UI_DIM}every target${ZTD_UI_RESET}"
    fi
    echo -e "  ${ZTD_UI_BOLD}Binary${ZTD_UI_RESET}    $binary"
    echo -e "  ${ZTD_UI_BOLD}Docs${ZTD_UI_RESET}      docs/guides/getting_started.md"

    if [[ "$ZTD_PATH_CONFIGURED" == true ]]; then
        echo -e "\n  ${ZTD_UI_DIM}Open a new terminal or run 'source $ZTD_PROFILE_FILE' to use ztd from any directory.${ZTD_UI_RESET}"
    else
        echo ""
        echo "  PATH was not changed. Run $binary from $REPO_ROOT, or add these"
        echo "  lines to $ZTD_PROFILE_FILE yourself:"
        local entry mode dir comment
        for entry in "${ZTD_PATH_PLAN[@]}"; do
            IFS='|' read -r mode dir comment <<<"$entry"
            echo "    $(ztd_path_export_line "$mode" "$dir")"
        done
    fi
}

# --- shared main flow ------------------------------------------------------------

# Runs the whole Unix setup after ztd_setup_init. <install-fn> receives the
# logical names in MISSING and installs what it can; the flow re-checks after it.
# --build-only is three steps (prerequisites, build, PATH); the full contributor
# flow adds the explorer build, `make dev-setup`, and `make dev-check`.
# usage: ztd_setup_main <platform-label> <install-fn> <script-path>
ztd_setup_main() {
    local platform="$1" install_fn="$2" script="$3" total step=0 prereqs
    if [[ "$ZTD_BUILD_ONLY" == true ]]; then
        total=3
        prereqs="git, make, curl, go >= ${ZTD_GO_MIN}"
        ztd_ui_title "ztd ${platform} setup ${ZTD_UI_DIM}(build only)${ZTD_UI_RESET}"
    else
        total=6
        prereqs="git, make, curl, go >= ${ZTD_GO_MIN}, node (${ZTD_NODE_REQUIREMENT}), rg, bc, C compiler"
        ztd_ui_title "ztd ${platform} setup ${ZTD_UI_DIM}(contributor toolchain)${ZTD_UI_RESET}"
    fi

    step=$((step + 1)); ztd_ui_step "$step" "$total" "Checking prerequisites ${ZTD_UI_DIM}(${prereqs})${ZTD_UI_RESET}"
    ztd_collect_missing
    if [[ ${#MISSING[@]} -gt 0 ]]; then
        ztd_ui_info "missing or outdated: ${MISSING[*]}"
        "$install_fn" "${MISSING[@]}"
        ztd_ui_info "re-checking prerequisites..."
        ztd_collect_missing
        if [[ ${#MISSING[@]} -gt 0 ]]; then
            ztd_ui_fatal "still missing: ${MISSING[*]}. Install them, then rerun: bash $script"
            exit 1
        fi
    fi

    if [[ "$ZTD_BUILD_ONLY" != true ]]; then
        step=$((step + 1)); ztd_ui_step "$step" "$total" "Building evaluation-explorer assets"
        ztd_build_evaluation_explorer
    fi

    step=$((step + 1)); ztd_ui_step "$step" "$total" "Building ztd"
    ztd_run_make_build
    ztd_ui_ok "built ./ztd"
    ztd_create_env_file

    if [[ "$ZTD_BUILD_ONLY" != true ]]; then
        step=$((step + 1)); ztd_ui_step "$step" "$total" "Installing the contributor toolchain (Go dev tools, Node deps)"
        ztd_run_dev_setup
    fi

    step=$((step + 1)); ztd_ui_step "$step" "$total" "Adding ztd to PATH"
    ztd_configure_path_unix

    if [[ "$ZTD_BUILD_ONLY" != true ]]; then
        step=$((step + 1)); ztd_ui_step "$step" "$total" "Verifying the toolchain (make dev-check)"
        make dev-check
    fi

    ztd_print_next_steps_unix
}
