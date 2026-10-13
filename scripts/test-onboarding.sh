#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# Offline regression tests for the Unix onboarding scripts (lib/dev-setup-common.sh,
# quickstart.sh, linux-setup.sh). Nothing here touches the network, Docker, the
# real toolchain, or a real shell profile: every test uses a private HOME, and
# the "missing tool" cases use a PATH that holds only stub commands plus symlinks
# to the coreutils the code calls.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

# The library has no side effects at load time, so it is sourced once here.
# ztd_setup_init (which cds to the repo root) is only ever called in subshells.
# shellcheck source=lib/dev-setup-common.sh
source "$SCRIPT_DIR/lib/dev-setup-common.sh"

GO_MIN="$(sed -n 's/^go //p' "$SOURCE_ROOT/go.mod" | head -1 | tr -d '[:space:]')"
[[ -n "$GO_MIN" ]] || fail "could not read the Go version from go.mod"

# Coreutils the code under test calls, wrapped so "missing" tools stay missing.
# 'ln -s' on Windows/Git Bash creates .exe copies that fail to load msys-2.0.dll
# under restricted PATH, so we use /bin/sh exec wrappers.
CORE="$SCRATCH/core-bin"
mkdir -p "$CORE"
for tool in sed awk grep head tail tr cat mktemp basename dirname uname id rm touch cmp cp wc cut mkdir; do
	real="$(command -v "$tool" || true)"
	if [[ -n "$real" ]]; then
		printf '#!/bin/sh\nexec "%s" "$@"\n' "$real" >"$CORE/$tool"
		chmod 755 "$CORE/$tool"
	fi
done

# stub <bindir> <name> <sh-body>: writes an executable /bin/sh script.
stub() {
	mkdir -p "$1"
	printf '#!/bin/sh\n%s\n' "$3" >"$1/$2"
	chmod 755 "$1/$2"
}

# Stubs for the build-only prerequisites (git make curl go), go at a given version.
toolchain_stubs() {
	local bin=$1 go_version=$2
	stub "$bin" git "echo 'git version 2.43.0'"
	stub "$bin" make "exit 0"
	stub "$bin" curl "exit 0"
	stub "$bin" go "echo 'go version go${go_version} linux/amd64'"
}

# collect_missing <home-dir-name> <bindir> [ztd_setup_init args...]
# Prints the MISSING list from ztd_collect_missing, space-separated.
collect_missing() {
	local home_name=$1 bin=$2
	shift 2
	(
		HOME="$SCRATCH/$home_name"
		mkdir -p "$HOME"
		PATH="$bin:$CORE"
		export HOME PATH
		# shellcheck source=lib/dev-setup-common.sh
		source "$SCRIPT_DIR/lib/dev-setup-common.sh"
		ztd_setup_init "$@" >/dev/null
		ztd_collect_missing >/dev/null
		echo "${MISSING[*]:-}"
	)
}

# expect_version_ge <true|false> <have> <need>
expect_version_ge() {
	local got=false
	if ztd_version_ge "$2" "$3"; then
		got=true
	fi
	[[ "$got" == "$1" ]] || fail "ztd_version_ge $2 $3: want $1, got $got"
}

test_version_ge() {
	expect_version_ge true 1.26.9 1.26.9
	expect_version_ge true 1.26.10 1.26.9
	expect_version_ge false 1.26 1.26.9
	expect_version_ge true 1.27.0 1.26.9
	expect_version_ge true 22.0 22.0
	expect_version_ge false 21.9.9 22.0
}

test_extract_version() {
	local got
	got="$(ztd_extract_version "go version go1.26.6 linux/amd64")"
	[[ "$got" == 1.26.6 ]] || fail "extract go banner: want 1.26.6, got '$got'"
	got="$(ztd_extract_version "v22.23.3")"
	[[ "$got" == 22.23.3 ]] || fail "extract node banner: want 22.23.3, got '$got'"
}

test_build_only_does_not_require_node() {
	local bin="$SCRATCH/bin-build-only"
	toolchain_stubs "$bin" "$GO_MIN"
	local missing
	missing="$(collect_missing home-build-only "$bin" --build-only)" ||
		fail "collect_missing --build-only failed"
	[[ -z "$missing" ]] || fail "--build-only: want no missing tools, got '$missing'"
}

test_full_mode_requires_node() {
	local bin="$SCRATCH/bin-full"
	toolchain_stubs "$bin" "$GO_MIN"
	local missing
	missing="$(collect_missing home-full "$bin")" || fail "collect_missing (full) failed"
	case " $missing " in
	*" node "*) ;;
	*) fail "full mode: want node in MISSING, got '$missing'" ;;
	esac
}

test_go_too_old_is_missing() {
	local bin="$SCRATCH/bin-old-go"
	toolchain_stubs "$bin" "1.20.1"
	local missing
	missing="$(collect_missing home-old-go "$bin" --build-only)" ||
		fail "collect_missing (old go) failed"
	[[ "$missing" == "go" ]] || fail "old go: want MISSING 'go', got '$missing'"
}

# The WSL rule (a node reachable only under /mnt/<letter>/ does not count) needs
# a real /mnt/<letter> directory, which a hermetic test cannot create. This only
# covers the positive path of ztd_have; the /mnt rejection is left unexercised.
test_ztd_have_accepts_native_tool() {
	local bin="$SCRATCH/bin-have"
	stub "$bin" fakecmd "exit 0"
	(
		HOME="$SCRATCH/home-have"
		PATH="$bin:$CORE"
		export HOME PATH
		# shellcheck source=lib/dev-setup-common.sh
		source "$SCRIPT_DIR/lib/dev-setup-common.sh"
		ztd_have fakecmd || exit 1
		if ztd_have definitely-not-installed; then
			exit 1
		fi
	) || fail "ztd_have: native stub should be found and absent tool should not"
}

test_ui_plain_when_not_tty() {
	local out="$SCRATCH/ui-plain.out" got
	(
		unset NO_COLOR
		ztd_ui_init
		ztd_ui_ok hello
	) >"$out" 2>&1
	got="$(cat "$out")"
	[[ "$got" == "  ok hello" ]] || fail "plain ok line: want '  ok hello', got '$got'"
	if grep -q $'\033' "$out"; then
		fail "plain output contains escape bytes"
	fi
}

test_ui_no_color_clears_palette() {
	local out="$SCRATCH/ui-nocolor.out" got
	(
		NO_COLOR=1
		export NO_COLOR
		ztd_ui_init
		# The palette is set inside this same subshell, so the read is local to it.
		# shellcheck disable=SC2031
		printf 'green=[%s] ok=[%s]\n' "$ZTD_UI_GREEN" "$ZTD_UI_OK" >"$out"
	)
	got="$(cat "$out")"
	[[ "$got" == "green=[] ok=[ok]" ]] || fail "NO_COLOR: got '$got'"
}

test_ui_elapsed_format() {
	local got
	got="$(ZTD_UI_STARTED=$((SECONDS - 75)); ztd_ui_elapsed)"
	[[ "$got" == 1m15s ]] || fail "elapsed 75s: want 1m15s, got '$got'"
	got="$(ZTD_UI_STARTED=$((SECONDS - 5)); ztd_ui_elapsed)"
	[[ "$got" == 5s ]] || fail "elapsed 5s: want 5s, got '$got'"
}

# Profile tests run with a stub-only PATH: the library also plans the Go
# toolchain directory (/usr/local/go/bin) when go lives there, which would
# make the expected profile contents depend on the host.
profile_path() {
	local bin="$SCRATCH/bin-profile"
	if [[ ! -x "$bin/go" ]]; then
		toolchain_stubs "$bin" "$GO_MIN"
	fi
	echo "$bin:$CORE"
}

test_path_profile_idempotent() {
	local home="$SCRATCH/home-path-idem" profile count
	mkdir -p "$home"
	profile="$home/.bashrc"
	(
		HOME="$home" PATH="$(profile_path)" SHELL=/bin/bash AUTO_YES=true ZTD_BUILD_ONLY=true
		REPO_ROOT="$SOURCE_ROOT"
		export HOME PATH SHELL AUTO_YES ZTD_BUILD_ONLY REPO_ROOT
		ztd_ui_init
		ztd_configure_path_unix >/dev/null
		ztd_configure_path_unix >/dev/null
	) || fail "ztd_configure_path_unix failed"
	[[ -f "$profile" ]] || fail "no profile written at $profile"
	count="$(grep -cxF '# ztd: add repository root to PATH' "$profile" || true)"
	[[ "$count" == 1 ]] || fail "marker lines: want 1, got $count"
	count="$(grep -cF "$SOURCE_ROOT" "$profile" || true)"
	[[ "$count" == 1 ]] || fail "lines mentioning repo root: want 1, got $count"
}

test_path_profile_respects_home_form() {
	local home="$SCRATCH/home-path-form" profile before
	mkdir -p "$home/src/ztd"
	profile="$home/.bashrc"
	printf '%s\n' "export PATH=\"\$PATH:\$HOME/src/ztd\"" >"$profile"
	before="$SCRATCH/bashrc-home-form.before"
	cp "$profile" "$before"
	(
		HOME="$home" PATH="$(profile_path)" SHELL=/bin/bash AUTO_YES=true ZTD_BUILD_ONLY=true
		REPO_ROOT="$home/src/ztd"
		export HOME PATH SHELL AUTO_YES ZTD_BUILD_ONLY REPO_ROOT
		ztd_ui_init
		ztd_configure_path_unix >/dev/null
	) || fail "ztd_configure_path_unix failed"
	cmp -s "$before" "$profile" || fail "profile changed although \$HOME/src/ztd was already present"
}

test_path_profile_matches_whole_path() {
	local home="$SCRATCH/home-path-prefix" profile count
	mkdir -p "$home/src/ztd"
	profile="$home/.bashrc"
	printf '%s\n' "export PATH=\"\$PATH:$home/src/ztd-old:$home/src/ztd/bin\"" >"$profile"
	(
		HOME="$home" PATH="$(profile_path)" SHELL=/bin/bash AUTO_YES=true ZTD_BUILD_ONLY=true
		REPO_ROOT="$home/src/ztd"
		export HOME PATH SHELL AUTO_YES ZTD_BUILD_ONLY REPO_ROOT
		ztd_ui_init
		ztd_configure_path_unix >/dev/null
	) || fail "ztd_configure_path_unix failed"
	count="$(grep -cxF '# ztd: add repository root to PATH' "$profile" || true)"
	[[ "$count" == 1 ]] || fail "sibling and child paths masked the repo root: marker lines want 1, got $count"
}

test_create_env_file_owner_only_and_kept() {
	local repo="$SCRATCH/repo-env"
	mkdir -p "$repo"
	printf '# template\n' >"$repo/.env.example"
	(
		cd "$repo" || exit 1
		ztd_ui_init
		ztd_create_env_file >/dev/null
	) || fail "ztd_create_env_file failed"
	case "$(uname -s)" in
	MINGW* | MSYS* | CYGWIN*)
		echo "SKIP: NTFS has no POSIX mode 600"
		;;
	*)
		[[ -n "$(find "$repo/.env" -perm 600)" ]] || fail ".env mode: want 600"
		;;
	esac

	printf 'kept\n' >"$repo/.env"
	(
		cd "$repo" || exit 1
		ztd_ui_init
		ztd_create_env_file >/dev/null
	) || fail "ztd_create_env_file rerun failed"
	[[ "$(cat "$repo/.env")" == kept ]] || fail "rerun overwrote an existing .env"
}

test_confirm_noninteractive_exits_2() {
	local rc=0
	(
		AUTO_YES=false
		ztd_setup_confirm "x? "
	) </dev/null >/dev/null 2>&1 || rc=$?
	[[ $rc -eq 2 ]] || fail "non-tty confirm without -y: want exit 2, got $rc"
}

test_linux_setup_rejects_unknown_option() {
	local out="$SCRATCH/linux-bogus.out" rc=0
	HOME="$SCRATCH/home-linux-bogus" bash "$SCRIPT_DIR/linux-setup.sh" --bogus \
		</dev/null >"$out" 2>&1 || rc=$?
	[[ $rc -eq 2 ]] || { cat "$out" >&2; fail "linux-setup --bogus: want exit 2, got $rc"; }
	grep -qF 'Unknown option' "$out" || { cat "$out" >&2; fail "linux-setup --bogus: no 'Unknown option' message"; }
}

test_quickstart_help_and_unknown_option() {
	local out="$SCRATCH/qs-help.out" rc=0
	HOME="$SCRATCH/home-qs-help" bash "$SCRIPT_DIR/quickstart.sh" --help >"$out" 2>&1 || rc=$?
	[[ $rc -eq 0 ]] || { cat "$out" >&2; fail "quickstart --help: want exit 0, got $rc"; }
	grep -qF -- '--master-key-file' "$out" || fail "quickstart --help: missing --master-key-file"

	rc=0
	HOME="$SCRATCH/home-qs-help" bash "$SCRIPT_DIR/quickstart.sh" --bogus >"$out" 2>&1 || rc=$?
	[[ $rc -eq 2 ]] || { cat "$out" >&2; fail "quickstart --bogus: want exit 2, got $rc"; }
}

# make_fake_repo <repo-dir> <state-dir>
# A copy of quickstart.sh and the library beside a stub ./ztd. The stub logs each
# call to $ZTD_STUB_STATE/calls.log and keeps Gateway and sign-in state there.
make_fake_repo() {
	local repo=$1 state=$2
	mkdir -p "$repo/scripts/lib" "$state"
	cp "$SCRIPT_DIR/quickstart.sh" "$repo/scripts/"
	cp "$SCRIPT_DIR/lib/dev-setup-common.sh" "$repo/scripts/lib/"
	cat >"$repo/ztd" <<'STUB'
#!/bin/sh
STATE="${ZTD_STUB_STATE:?}"
echo "$*" >>"$STATE/calls.log"
cmd="$*"
case "${cmd% --master-key-file *}" in
--version) echo "ztd version v1.0.0" ;;
"gw status")
	if [ -e "$STATE/gw.on" ]; then
		echo "Gateway  online (PID 1)"
		echo "Console  https://localhost:8443/console/"
	else
		echo "Gateway  stopped"
	fi
	;;
"gw start"*)
	if [ -e "$STATE/keyfail" ]; then
		echo "Error: no Secret Service keyring available" >&2
		exit 1
	fi
	touch "$STATE/gw.on"
	;;
"auth context") [ -e "$STATE/authed" ] ;;
login) touch "$STATE/authed" ;;
"operator run"*) echo "hello from ztd" ;;
esac
STUB
	chmod 755 "$repo/ztd"
}

# count_calls <state-dir> <grep-pattern>
count_calls() {
	local log="$1/calls.log"
	[[ -f "$log" ]] || { echo 0; return 0; }
	grep -c -e "$2" "$log" || true
}

test_quickstart_end_to_end_is_idempotent() {
	local repo="$SCRATCH/repo" state="$SCRATCH" home="$SCRATCH/home-qs-e2e"
	local out rc=0 starts logins
	make_fake_repo "$repo" "$state"
	mkdir -p "$home"

	out="$SCRATCH/qs-e2e-first.out"
	ZTD_STUB_STATE="$state" HOME="$home" bash "$repo/scripts/quickstart.sh" \
		--master-key-file /tmp/k.key >"$out" 2>&1 || rc=$?
	[[ $rc -eq 0 ]] || { cat "$out" >&2; fail "quickstart first run: want exit 0, got $rc"; }
	grep -qF 'hello from ztd' "$out" || { cat "$out" >&2; fail "quickstart first run: no governed-action output"; }
	grep -qF "You're running ztd" "$out" || fail "quickstart first run: no success banner"
	grep -qxF 'gw start --capabilities data --master-key-file /tmp/k.key' "$state/calls.log" ||
		fail "quickstart first run: missing gw start call with --master-key-file"
	grep -qxF 'login --master-key-file /tmp/k.key' "$state/calls.log" ||
		fail "quickstart first run: missing login call with --master-key-file"
	grep -qxF "operator run --all-active --cmd echo hello from ztd --master-key-file /tmp/k.key" "$state/calls.log" ||
		fail "quickstart first run: missing operator run call with --master-key-file"
	if grep -v -e '^--version$' -e ' --master-key-file /tmp/k.key$' "$state/calls.log" >&2; then
		fail "quickstart first run: ztd call without --master-key-file"
	fi

	starts="$(count_calls "$state" '^gw start')"
	logins="$(count_calls "$state" '^login ')"

	rc=0
	out="$SCRATCH/qs-e2e-second.out"
	ZTD_STUB_STATE="$state" HOME="$home" bash "$repo/scripts/quickstart.sh" \
		--master-key-file /tmp/k.key >"$out" 2>&1 || rc=$?
	[[ $rc -eq 0 ]] || { cat "$out" >&2; fail "quickstart second run: want exit 0, got $rc"; }
	grep -qF 'already running' "$out" || { cat "$out" >&2; fail "quickstart second run: Gateway not reported as already running"; }
	grep -qF 'already signed in' "$out" || { cat "$out" >&2; fail "quickstart second run: CLI not reported as already signed in"; }
	[[ "$(count_calls "$state" '^gw start')" == "$starts" ]] ||
		fail "quickstart second run: issued a new gw start"
	[[ "$(count_calls "$state" '^login ')" == "$logins" ]] ||
		fail "quickstart second run: issued a new login"
}

test_quickstart_surfaces_key_store_remedy() {
	local repo="$SCRATCH/repo-keyring" state="$SCRATCH/t-keyring" home="$SCRATCH/home-qs-key"
	local out rc=0
	make_fake_repo "$repo" "$state"
	touch "$state/keyfail"
	mkdir -p "$home"

	out="$SCRATCH/qs-keyring.out"
	ZTD_STUB_STATE="$state" HOME="$home" bash "$repo/scripts/quickstart.sh" \
		--master-key-file /tmp/k.key >"$out" 2>&1 || rc=$?
	[[ $rc -ne 0 ]] || { cat "$out" >&2; fail "quickstart with no key store: want non-zero exit"; }
	grep -qF 'ssh-agent' "$out" || { cat "$out" >&2; fail "quickstart key-store failure: no ssh-agent remedy"; }
	grep -qF -- '--master-key-file' "$out" || { cat "$out" >&2; fail "quickstart key-store failure: no --master-key-file remedy"; }
}

run_test() {
	"$1"
	echo "ok ${1#test_}"
}

main() {
	run_test test_version_ge
	run_test test_extract_version
	run_test test_build_only_does_not_require_node
	run_test test_full_mode_requires_node
	run_test test_go_too_old_is_missing
	run_test test_ztd_have_accepts_native_tool
	run_test test_ui_plain_when_not_tty
	run_test test_ui_no_color_clears_palette
	run_test test_ui_elapsed_format
	run_test test_path_profile_idempotent
	run_test test_path_profile_respects_home_form
	run_test test_path_profile_matches_whole_path
	run_test test_create_env_file_owner_only_and_kept
	run_test test_confirm_noninteractive_exits_2
	run_test test_linux_setup_rejects_unknown_option
	run_test test_quickstart_help_and_unknown_option
	run_test test_quickstart_end_to_end_is_idempotent
	run_test test_quickstart_surfaces_key_store_remedy
	echo "all onboarding tests passed"
}

main "$@"
