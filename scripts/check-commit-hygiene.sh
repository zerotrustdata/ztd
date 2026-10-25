#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# Rejects content that must never enter git history: paths matched by
# .gitignore (force-added runtime state, keys, build output), blobs over
# HYGIENE_MAX_BYTES outside vendor/, compiled executables (ELF, Mach-O, PE),
# the committer's own home directory path, gitleaks findings, and git file modes
# that disagree with content outside vendor/ (100755 without a `#!` shebang, or a
# `#!` script that is not 100755).
#
#   scripts/check-commit-hygiene.sh --staged         pre-commit: the index
#   scripts/check-commit-hygiene.sh --range A..B     pre-push / CI: every blob
#                                                    added by each commit in A..B
#   scripts/check-commit-hygiene.sh --range B --not --remotes
#                                                    any rev-list arguments work
#
# Range mode inspects every commit, so a file added and deleted inside the range
# is still caught. Once pushed, it lives in history; that is how the legacy
# binaries and PKI key reached zerotrustdata/ztd.
#
# Bypass a single commit only with `git commit --no-verify`, and only when the
# finding is a reviewed false positive. CI runs the same checks.

set -euo pipefail
export LC_ALL=C

cd "$(git rev-parse --show-toplevel)"

MAX_BYTES="${HYGIENE_MAX_BYTES:-5242880}"
MODE=""
REV_ARGS=()

case "${1:-}" in
    --staged) MODE=staged ;;
    --range)
        MODE=range
        shift
        REV_ARGS=("$@")
        [[ ${#REV_ARGS[@]} -gt 0 ]] || { echo "usage: $0 --range <rev-list args>" >&2; exit 2; }
        ;;
    *) echo "usage: $0 --staged | --range <rev-list args>" >&2; exit 2 ;;
esac

FAIL=0
fail() {
    echo "  ✗ $*" >&2
    FAIL=1
}

# ENTRIES: "<blob sha>\t<path>\t<git mode>" for every added or modified file.
# PATCH (check 4 only): added lines with "+++ b/<path>" headers.
CHECK_HOME=""
if [[ -z ${CI:-} && -n ${HOME:-} && $HOME == /*/* ]]; then
    CHECK_HOME=1
fi
PATCH=""
if [[ $MODE == staged ]]; then
    ENTRIES="$(git diff --cached --raw --no-abbrev --no-renames --diff-filter=ACMT -z \
        | tr '\0' '\n' | paste -d'\t' - - | awk -F'\t' '{split($1, a, " "); print a[4] "\t" $2 "\t" a[2]}')"
    [[ -z $CHECK_HOME ]] || PATCH="$(git diff --cached --no-renames --diff-filter=ACMT --no-color -U0 --no-ext-diff)"
else
    COMMITS="$(git rev-list "${REV_ARGS[@]}")"
    if [[ -z $COMMITS ]]; then
        exit 0
    fi
    ENTRIES="$(git diff-tree --stdin -r --root --raw --no-abbrev --no-renames --diff-filter=ACMT -z --no-commit-id <<<"$COMMITS" \
        | tr '\0' '\n' | paste -d'\t' - - | awk -F'\t' '{split($1, a, " "); print a[4] "\t" $2 "\t" a[2]}' | sort -u)"
    [[ -z $CHECK_HOME ]] || PATCH="$(git diff-tree --stdin -r --root -p --no-renames --diff-filter=ACMT --no-color -U0 --no-ext-diff --no-commit-id <<<"$COMMITS")"
fi

if [[ -z $ENTRIES ]]; then
    exit 0
fi

PATHS="$(cut -f2 <<<"$ENTRIES" | sort -u)"

# 1. Paths the .gitignore set says never to commit. Tracked files edited in place
#    are allowed only if they are not ignored, so `git add -f` is what this catches.
IGNORED="$(git check-ignore --no-index --stdin <<<"$PATHS" || true)"
if [[ -n $IGNORED ]]; then
    while IFS= read -r p; do
        fail "$p: matched by $(git check-ignore --no-index -v "$p" | cut -f1) (ignored path, force-added?)"
    done <<<"$IGNORED"
fi

# 2. Oversized blobs. vendor/ holds generated Go sources (modernc.org/sqlite) that
#    exceed the limit; compiled binaries there are still caught by check 3.
while read -r sha type size path; do
    [[ $type == blob ]] || continue
    [[ $path == vendor/* ]] && continue
    if (( size > MAX_BYTES )); then
        fail "$path: $size bytes exceeds the $MAX_BYTES-byte limit"
    fi
done < <(awk -F'\t' '{print $1 " " $2}' <<<"$ENTRIES" \
    | git cat-file --batch-check='%(objectname) %(objecttype) %(objectsize) %(rest)')

# 3. Compiled executables, detected by magic bytes rather than extension.
while IFS=$'\t' read -r sha path _; do
    magic="$( (git cat-file blob "$sha" | head -c 4 | od -An -tx1 | tr -d ' \n') 2>/dev/null || true)"
    case "$magic" in
        7f454c46) fail "$path: ELF executable" ;;
        feedface | feedfacf | cefaedfe | cffaedfe | cafebabe) fail "$path: Mach-O executable" ;;
        4d5a*) fail "$path: PE (Windows) executable" ;;
    esac
done <<<"$ENTRIES"

# 4. The committer's home directory (for example /home/<user>/). Generic fixture
#    paths are fine; this machine's real path is not. Skipped in CI, where $HOME
#    is the runner's and says nothing about the author.
if [[ -n $CHECK_HOME ]]; then
    HITS="$(LC_ALL=C awk -v home="$HOME/" '
        /^\+\+\+ b\// { file = substr($0, 7); next }
        /^\+/ && index($0, home) { print file }
    ' <<<"$PATCH" | sort -u)"
    if [[ -n $HITS ]]; then
        while IFS= read -r p; do
            fail "$p: contains this machine's home path ($HOME/)"
        done <<<"$HITS"
    fi
fi

# 5. Secrets. CI runs gitleaks over full history (build-and-test.yml secret-scan);
#    locally it runs when installed. .gitleaks.toml uses [[allowlists]], which
#    gitleaks older than 8.25 silently ignores, flagging every allowlisted test
#    fixture; refuse to run such a version rather than report false findings.
GITLEAKS_MIN_VERSION=8.25.0
if command -v gitleaks >/dev/null 2>&1; then
    GITLEAKS_VERSION="$(gitleaks version 2>/dev/null | sed 's/^v//')"
    if [[ "$(printf '%s\n%s\n' "$GITLEAKS_MIN_VERSION" "$GITLEAKS_VERSION" | sort -V | head -1)" != "$GITLEAKS_MIN_VERSION" ]]; then
        fail "gitleaks $GITLEAKS_VERSION is older than $GITLEAKS_MIN_VERSION and ignores .gitleaks.toml [[allowlists]]; upgrade it (CI pins 8.27.2)"
    elif [[ $MODE == staged ]]; then
        gitleaks git --pre-commit --staged --config .gitleaks.toml --redact --no-banner --log-level warn -v . \
            || fail "gitleaks reported secrets in the staged changes (see above)"
    elif [[ -z ${CI:-} ]]; then
        gitleaks git --config .gitleaks.toml --redact --no-banner --log-level warn -v \
            --log-opts="${REV_ARGS[*]}" . \
            || fail "gitleaks reported secrets in the pushed commits (see above)"
    fi
elif [[ -z ${CI:-} ]]; then
    echo "  ! gitleaks not installed; secret scan skipped (CI still runs it)" >&2
fi

# 6. File modes. The executable bit must match the content, read from the index or
#    commit tree rather than the working tree: a blob with git mode 100755 must be
#    a script (start with `#!`), and a `#!` script must be 100755. Symlinks and
#    submodules (other modes) are skipped; vendor/ is upstream's and is exempt.
while IFS=$'\t' read -r sha path mode; do
    [[ $path == vendor/* ]] && continue
    [[ $mode == 100644 || $mode == 100755 ]] || continue
    shebang=""
    [[ "$( (git cat-file blob "$sha" | head -c 2) 2>/dev/null || true)" != '#!' ]] || shebang=1
    if [[ $mode == 100755 && -z $shebang ]]; then
        fail "$path: mode 100755 but not a script (no #! shebang); use 644 (git update-index --chmod=-x)"
    elif [[ $mode == 100644 && -n $shebang ]]; then
        fail "$path: #! script with mode 100644; use 755 (git update-index --chmod=+x)"
    fi
done <<<"$ENTRIES"

if (( FAIL )); then
    echo "commit hygiene: FAILED. Unstage or remove the files above; never commit them, even to delete later." >&2
    exit 1
fi
