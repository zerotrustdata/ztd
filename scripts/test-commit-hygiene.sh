#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# Exercise the real hygiene hook in disposable repositories. Fixture commits
# never touch the source repository, and secret scanning stays in its own CI job.
set -euo pipefail

SOURCE_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT
CASE_NUMBER=0

fixture_commit() {
    git -c core.hooksPath=/dev/null -c commit.gpgsign=false commit -qm "$1"
}

new_repository() {
    CASE_NUMBER=$((CASE_NUMBER + 1))
    mkdir "$SCRATCH/$CASE_NUMBER"
    cd "$SCRATCH/$CASE_NUMBER"
    git init -q
    git config user.name 'Hygiene Fixture'
    git config user.email 'hygiene@example.invalid'
    mkdir scripts
    cp "$SOURCE_ROOT/scripts/check-commit-hygiene.sh" scripts/
    cp "$SOURCE_ROOT/.gitleaks.toml" .
    printf '.env\n' > .gitignore
    git add scripts .gitignore .gitleaks.toml
    fixture_commit baseline
    BASE="$(git rev-parse HEAD)"
}

expect_rejection() {
    local mode=$1 reason=$2
    shift 2
    if CI=true HYGIENE_MAX_BYTES=32 bash scripts/check-commit-hygiene.sh "$mode" "$@" > "$SCRATCH/result" 2>&1; then
        echo "FAIL: expected hygiene rejection for $reason" >&2
        exit 1
    fi
    if ! grep -qF "$reason" "$SCRATCH/result"; then
        cat "$SCRATCH/result" >&2
        echo "FAIL: expected diagnostic $reason" >&2
        exit 1
    fi
}

new_repository
printf 'ordinary source\n' > source.txt
git add source.txt
CI=true bash scripts/check-commit-hygiene.sh --staged
fixture_commit source
CI=true bash scripts/check-commit-hygiene.sh --range "$BASE..HEAD"
echo 'PASS: ordinary source accepted in staged and range modes'

new_repository
printf 'fixture only\n' > .env
git add -f .env
expect_rejection --staged 'ignored path'
fixture_commit ignored
git rm -q .env
fixture_commit removed
expect_rejection --range 'ignored path' "$BASE..HEAD"
echo 'PASS: force-added ignored file rejected even after deletion'

for magic in '\x7fELF' '\xfe\xed\xfa\xce' 'MZ'; do
    new_repository
    printf '%b' "$magic" > executable
    git add executable
    case "$magic" in
        *ELF) reason='ELF executable' ;;
        MZ) reason='PE (Windows) executable' ;;
        *) reason='Mach-O executable' ;;
    esac
    expect_rejection --staged "$reason"
    fixture_commit executable
    git rm -q executable
    fixture_commit removed
    expect_rejection --range "$reason" "$BASE..HEAD"
    echo "PASS: $reason rejected in staged and add-then-delete history"
done

new_repository
printf '%040d' 0 > oversized.txt
git add oversized.txt
expect_rejection --staged 'exceeds the 32-byte limit'
fixture_commit oversized
git rm -q oversized.txt
fixture_commit removed
expect_rejection --range 'exceeds the 32-byte limit' "$BASE..HEAD"
echo 'PASS: oversized file rejected even after deletion'

new_repository
mkdir vendor
printf '%040d' 0 > vendor/source.txt
git add vendor/source.txt
CI=true HYGIENE_MAX_BYTES=32 bash scripts/check-commit-hygiene.sh --staged
fixture_commit vendor
CI=true HYGIENE_MAX_BYTES=32 bash scripts/check-commit-hygiene.sh --range "$BASE..HEAD"
printf '\x7fELF' > vendor/binary
git add vendor/binary
expect_rejection --staged 'ELF executable'
echo 'PASS: vendor size exception still rejects executable content'

new_repository
printf 'not a script\n' > data.txt
git add --chmod=+x data.txt
expect_rejection --staged 'data.txt: mode 100755 but not a script'
fixture_commit data
expect_rejection --range 'data.txt: mode 100755 but not a script' "$BASE..HEAD"
echo 'PASS: executable non-script rejected in staged and range modes'

new_repository
printf '#!/usr/bin/env bash\necho ok\n' > tool.sh
git add --chmod=-x tool.sh
expect_rejection --staged 'tool.sh: #! script with mode 100644'
fixture_commit tool
expect_rejection --range 'tool.sh: #! script with mode 100644' "$BASE..HEAD"
echo 'PASS: non-executable shebang script rejected in staged and range modes'

new_repository
printf '#!/usr/bin/env bash\necho ok\n' > tool.sh
printf 'not a script\n' > data.txt
git add --chmod=+x tool.sh
git add --chmod=-x data.txt
CI=true bash scripts/check-commit-hygiene.sh --staged
fixture_commit modes
CI=true bash scripts/check-commit-hygiene.sh --range "$BASE..HEAD"
echo 'PASS: executable shebang script and plain data file accepted'

new_repository
mkdir vendor
printf 'not a script\n' > vendor/data.txt
printf '#!/usr/bin/env bash\n' > vendor/tool.sh
git add --chmod=+x vendor/data.txt
git add --chmod=-x vendor/tool.sh
CI=true bash scripts/check-commit-hygiene.sh --staged
fixture_commit vendor
CI=true bash scripts/check-commit-hygiene.sh --range "$BASE..HEAD"
echo 'PASS: vendor/ exempt from file mode check'
