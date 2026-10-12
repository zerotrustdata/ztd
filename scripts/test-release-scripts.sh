#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# Exercise the release gates offline in disposable repositories. Git commits
# and tags, synthetic binaries, and placeholder signature files stay in /tmp.
# Placeholder signatures test staging only; this script makes no claim about
# cosign/Fulcio authenticity.
set -euo pipefail

SOURCE_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

fixture_commit() {
	git -c core.hooksPath=/dev/null -c commit.gpgsign=false commit -qm "$1"
}

expect_rejection() {
	local reason=$1
	shift
	if "$@" >"$SCRATCH/result" 2>&1; then
		fail "expected rejection: $reason"
	fi
	if ! grep -qF "$reason" "$SCRATCH/result"; then
		cat "$SCRATCH/result" >&2
		fail "expected diagnostic: $reason"
	fi
}

# Keep the real production catalog and its vendored dependencies available to
# the copied artifact script, while all release state lives in this fixture.
REPO="$SCRATCH/repo"
mkdir -p "$REPO/scripts" "$REPO/docs/release_notes/v1.0.x" \
	"$REPO/internal/tools" "$REPO/internal/services"
cp "$SOURCE_ROOT/scripts/release-preflight.sh" "$REPO/scripts/"
cp "$SOURCE_ROOT/scripts/release-artifacts.sh" "$REPO/scripts/"
cp "$SOURCE_ROOT/go.mod" "$SOURCE_ROOT/go.sum" "$REPO/"
ln -s "$SOURCE_ROOT/vendor" "$REPO/vendor"
ln -s "$SOURCE_ROOT/internal/constants" "$REPO/internal/constants"
ln -s "$SOURCE_ROOT/internal/timesvc" "$REPO/internal/timesvc"
ln -s "$SOURCE_ROOT/internal/services/ztdbinaries" "$REPO/internal/services/ztdbinaries"
cp -R "$SOURCE_ROOT/internal/tools/ztdbinaries" "$REPO/internal/tools/"
printf 'v1.0.0\n' > "$REPO/VERSION"
printf 'Synthetic release notes for script tests.\n' > "$REPO/docs/release_notes/v1.0.x/v1.0.0.md"
cd "$REPO"
git init -q
git config user.name 'Release Fixture'
git config user.email 'release-fixture@example.invalid'
git add VERSION docs scripts go.mod go.sum internal vendor
fixture_commit baseline
git tag v1.0.0
HEAD_SHA="$(git rev-parse HEAD)"

scripts/release-preflight.sh --event workflow_dispatch --ref refs/tags/v1.0.0 --sha "$HEAD_SHA" --publish true > "$SCRATCH/preflight-tag"
grep -qx 'tag=v1.0.0' "$SCRATCH/preflight-tag" || fail 'tag preflight did not report release tag'
grep -qx 'publish=true' "$SCRATCH/preflight-tag" || fail 'tag preflight did not enable requested publication'
expect_rejection "does not match VERSION" scripts/release-preflight.sh --event workflow_dispatch --ref refs/tags/v2.0.0 --sha "$HEAD_SHA" --publish false
expect_rejection "does not match checked-out revision" scripts/release-preflight.sh --event workflow_dispatch --ref refs/tags/v1.0.0 --sha "$(printf '0%.0s' {1..40})" --publish false
expect_rejection "unsupported dispatch ref" scripts/release-preflight.sh --event workflow_dispatch --ref refs/pull/123/merge --sha "$HEAD_SHA" --publish false

git switch -q -c rehearsal
scripts/release-preflight.sh --event workflow_dispatch --ref refs/heads/rehearsal --sha "$HEAD_SHA" --publish false > "$SCRATCH/preflight-branch"
grep -qx 'tag=' "$SCRATCH/preflight-branch" || fail 'branch rehearsal reported a tag'
grep -qx 'publish=false' "$SCRATCH/preflight-branch" || fail 'branch rehearsal did not remain nonpublishing'
expect_rejection "publish=true requires a release tag" scripts/release-preflight.sh --event workflow_dispatch --ref refs/heads/rehearsal --sha "$HEAD_SHA" --publish true
printf 'dirty\n' > "$REPO/dirty-marker"
expect_rejection 'worktree is dirty' scripts/release-preflight.sh --event workflow_dispatch --ref refs/tags/v1.0.0 --sha "$HEAD_SHA" --publish true
rm "$REPO/dirty-marker"

printf 'v1.0.1\n' > VERSION
expect_rejection "does not match VERSION" scripts/release-preflight.sh --event workflow_dispatch --ref refs/tags/v1.0.0 --sha "$HEAD_SHA" --publish false
git checkout -q -- VERSION
printf 'later commit\n' > later.txt
git add later.txt
fixture_commit 'move checkout after tag'
expect_rejection "does not match tag" scripts/release-preflight.sh --event workflow_dispatch --ref refs/tags/v1.0.0 --sha "$(git rev-parse HEAD)" --publish false
echo 'PASS: release preflight tag, revision, rehearsal, and dirty-publication cases'

BIN="$SCRATCH/bin"
mkdir -p "$BIN"
mapfile -t NAMES < <(scripts/release-artifacts.sh list-binaries)
[ "${#NAMES[@]}" -eq 7 ] || fail "catalog lists ${#NAMES[@]} binaries; expected 7"
printf '%s\n' "${NAMES[@]}" | grep -qx 'ztd-windows-amd64.exe' || fail 'Windows executable suffix missing from catalog output'
for name in "${NAMES[@]}"; do
	if [ "$name" = ztd-linux-amd64 ]; then
		# Static fixture for the script's metadata parser, not a release binary.
		cat > "$BIN/$name" <<'EOF'
#!/usr/bin/env bash
if [ "${1:-}" = version ] && [ "${2:-}" = --fips ]; then exit 0; fi
cat <<'META'
ztd version v1.0.0
platform: linux_amd64
source rev: REVISION_PLACEHOLDER
source state: clean
META
EOF
		sed -i "s/REVISION_PLACEHOLDER/$HEAD_SHA/" "$BIN/$name"
		chmod +x "$BIN/$name"
	else
		printf 'synthetic fixture artifact for %s\n' "$name" > "$BIN/$name"
	fi
	sha256sum "$BIN/$name" > "$BIN/$name.sha256"
done

TARGETS='[]'
for name in "${NAMES[@]}"; do
	digest="$(sha256sum "$BIN/$name" | cut -d' ' -f1)"
	TARGETS="$(jq -c --arg filename "$name" --arg sha "$digest" '. + [{filename:$filename,sha256:$sha}]' <<<"$TARGETS")"
done
jq -n --arg version v1.0.0 --arg revision "$HEAD_SHA" --argjson targets "$TARGETS" \
	'{schema_version:1,version:$version,source_revision:$revision,targets:$targets}' > "$BIN/ztd-binaries.json"
scripts/release-artifacts.sh checksums "$BIN" >/dev/null
scripts/release-artifacts.sh verify "$BIN" 1.0.0 "$HEAD_SHA" >/dev/null
expect_rejection 'source_revision does not match' scripts/release-artifacts.sh verify "$BIN" 1.0.0 "$(printf '0%.0s' {1..40})"
mv "$BIN/${NAMES[2]}" "$SCRATCH/missing-artifact"
expect_rejection "missing or empty binary: ${NAMES[2]}" scripts/release-artifacts.sh verify "$BIN" 1.0.0 "$HEAD_SHA"
mv "$SCRATCH/missing-artifact" "$BIN/${NAMES[2]}"

cp "$BIN/ztd-binaries.json" "$SCRATCH/manifest.good"
cp "$BIN/${NAMES[1]}" "$SCRATCH/artifact.good"
jq '.targets[0].sha256 = "0000000000000000000000000000000000000000000000000000000000000000"' \
	"$BIN/ztd-binaries.json" > "$SCRATCH/manifest.tampered"
cp "$SCRATCH/manifest.tampered" "$BIN/ztd-binaries.json"
expect_rejection 'manifest digest mismatch' scripts/release-artifacts.sh verify "$BIN" 1.0.0 "$HEAD_SHA"
cp "$SCRATCH/manifest.good" "$BIN/ztd-binaries.json"

printf 'tampered bytes\n' >> "$BIN/${NAMES[1]}"
expect_rejection 'checksum mismatch' scripts/release-artifacts.sh verify "$BIN" 1.0.0 "$HEAD_SHA"
cp "$BIN/${NAMES[1]}.sha256" "$SCRATCH/checksum.good"
cp "$SCRATCH/artifact.good" "$BIN/${NAMES[1]}"
sha256sum "$BIN/${NAMES[1]}" > "$BIN/${NAMES[1]}.sha256"
scripts/release-artifacts.sh checksums "$BIN" >/dev/null
scripts/release-artifacts.sh verify "$BIN" 1.0.0 "$HEAD_SHA" >/dev/null
cp "$SCRATCH/checksum.good" "$BIN/${NAMES[1]}.sha256"
scripts/release-artifacts.sh checksums "$BIN" >/dev/null
printf stray > "$BIN/ztd-linux-mips"
scripts/release-artifacts.sh verify "$BIN" 1.0.0 "$HEAD_SHA" >/dev/null 2>&1 && fail 'stray artifact was accepted'
rm "$BIN/ztd-linux-mips"

# Stage exercises exact file selection only. Placeholder bundles are never
# passed to cosign and do not represent verified signatures.
for name in "${NAMES[@]}"; do
	printf '{"fixture":"placeholder"}\n' > "$BIN/$name.sigstore.json"
done
STAGED="$SCRATCH/staged"
scripts/release-artifacts.sh stage "$BIN" "$STAGED" >/dev/null
EXPECTED_FILES=()
for name in "${NAMES[@]}"; do
	EXPECTED_FILES+=("$name" "$name.sha256" "$name.sigstore.json")
done
EXPECTED_FILES+=(checksums-sha256.txt ztd-binaries.json)
printf '%s\n' "${EXPECTED_FILES[@]}" | sort > "$SCRATCH/expected-files"
find "$STAGED" -maxdepth 1 -type f -printf '%f\n' | sort > "$SCRATCH/actual-files"
diff -u "$SCRATCH/expected-files" "$SCRATCH/actual-files" || fail 'staged release set does not contain exactly the catalog artifacts'
[ "$(wc -l < "$SCRATCH/actual-files")" -eq 23 ] || fail 'staged release set does not contain 23 files'
echo 'PASS: catalog artifact completeness, checksums, revision/manifest/tamper/stray rejection, and staging names'
