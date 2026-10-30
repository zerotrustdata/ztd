#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.
#
# Exercise the release gates offline in disposable repositories. Git commits
# and tags, synthetic binaries, and placeholder signature files stay under .ztd/tmp.
# Placeholder signatures test staging only; this script makes no claim about
# cosign/Fulcio authenticity.
set -euo pipefail

SOURCE_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=lib/scratch.sh
. "$SOURCE_ROOT/scripts/lib/scratch.sh"
SCRATCH="$(ztd_scratch_dir "$SOURCE_ROOT" ztd-release-scripts)"
export GIT_CEILING_DIRECTORIES="$SOURCE_ROOT/.ztd/tmp"
trap 'rm -rf "$SCRATCH"' EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

command -v jq >/dev/null 2>&1 || fail "jq is required to run release script tests (install via 'choco install jq' on Windows or 'apt install jq' on Debian/Ubuntu)"

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
cp "$SOURCE_ROOT/Makefile" "$REPO/"
cp "$SOURCE_ROOT/scripts/release-rehearse.sh" "$REPO/scripts/"
cp "$SOURCE_ROOT/scripts/release-r2.sh" "$REPO/scripts/"
cp -R "$SOURCE_ROOT/scripts/lib" "$REPO/scripts/"
cp "$SOURCE_ROOT/go.mod" "$SOURCE_ROOT/go.sum" "$REPO/"
ln -s "$SOURCE_ROOT/vendor" "$REPO/vendor"
ln -s "$SOURCE_ROOT/internal/constants" "$REPO/internal/constants"
ln -s "$SOURCE_ROOT/internal/timesvc" "$REPO/internal/timesvc"
ln -s "$SOURCE_ROOT/internal/services/ztdbinaries" "$REPO/internal/services/ztdbinaries"
cp -R "$SOURCE_ROOT/internal/tools/ztdbinaries" "$REPO/internal/tools/"
printf 'v1.0.0\n' > "$REPO/VERSION"
printf 'Synthetic release notes for script tests.\n' > "$REPO/docs/release_notes/v1.0.x/v1.0.0.md"
cd "$REPO"
git init -q --initial-branch=main
git config user.name 'Release Fixture'
git config user.email 'release-fixture@example.invalid'
git add VERSION Makefile docs scripts go.mod go.sum internal vendor
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

# Exercise the real Make target with a local origin and a fake GitHub CLI. A
# successful dispatch may print nothing; run discovery must use structured data.
git switch -q main
git clone -q --bare . "$SCRATCH/origin.git"
git remote add origin "$SCRATCH/origin.git"
MOCK_BIN="$SCRATCH/mock-bin"
mkdir "$MOCK_BIN"
cat > "$MOCK_BIN/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$REHEARSAL_CASE/calls"
case "$1 $2" in
	'workflow run')
		if [ "$REHEARSAL_MODE" = dispatch-error ]; then
			echo 'HTTP 403: dispatch denied (https://github.com/fixture/repo/actions/runs/999)' >&2
			exit 1
		fi
		if [ "$REHEARSAL_MODE" = dispatch-url ]; then
			echo 'https://github.com/fixture/repo/actions/runs/101'
		fi
		;;
	'run list')
		count=0
		if [ -f "$REHEARSAL_CASE/list-count" ]; then
			read -r count < "$REHEARSAL_CASE/list-count"
		fi
		printf '%s\n' "$((count + 1))" > "$REHEARSAL_CASE/list-count"
		if [ "$REHEARSAL_MODE" = list-error ] || { [ "$REHEARSAL_MODE" = poll-error ] && [ "$count" -gt 0 ]; }; then
			echo 'HTTP 503: run discovery unavailable' >&2
			exit 1
		fi
		case "$*" in
			*'--event workflow_dispatch --branch main --commit '*'--json databaseId --jq '*) ;;
			*) echo 'missing workflow/ref/revision filters or structured run lookup' >&2; exit 2 ;;
		esac
		if [ "$REHEARSAL_MODE" = timeout ] || { [ "$REHEARSAL_MODE" = delayed ] && [ "$count" -lt 2 ]; }; then
			echo 100
		elif [ "$count" -gt 0 ]; then
			echo 101
		fi
		;;
	'run watch')
		[ "$3" = 101 ] && [ "$4" = --exit-status ] || exit 2
		[ "$REHEARSAL_MODE" != watch-error ] || exit 1
		;;
	*) echo "unexpected gh call: $*" >&2; exit 2 ;;
esac
EOF
cat > "$MOCK_BIN/sleep" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$REHEARSAL_CASE/sleeps"
EOF
chmod +x "$MOCK_BIN/gh" "$MOCK_BIN/sleep"

rehearse() {
	REHEARSAL_MODE=$1
	REHEARSAL_CASE="$SCRATCH/rehearsal-$1"
	export REHEARSAL_MODE REHEARSAL_CASE
	mkdir "$REHEARSAL_CASE"
	PATH="$MOCK_BIN:$PATH" make --no-print-directory -f "$REPO/Makefile" \
		"GO_BIN_DIR=$MOCK_BIN" TOOLCHAIN_FREE_GOALS=release-rehearse release-rehearse
}

for mode in silent dispatch-url delayed; do
	if ! rehearse "$mode" > "$SCRATCH/result" 2>&1; then
		cat "$SCRATCH/result" >&2
		fail "release rehearsal rejected successful $mode dispatch"
	fi
	grep -qx 'run watch 101 --exit-status' "$SCRATCH/rehearsal-$mode/calls" || fail 'did not watch the newly dispatched rehearsal'
done
[ "$(wc -l < "$SCRATCH/rehearsal-delayed/sleeps")" -eq 1 ] || fail 'did not wait for the new run to become visible'
for mode in dispatch-error list-error poll-error timeout; do
	case "$mode" in
		dispatch-error) reason='HTTP 403: dispatch denied' ;;
		list-error|poll-error) reason='HTTP 503: run discovery unavailable' ;;
		timeout) reason='dispatched but its run is not visible' ;;
	esac
	expect_rejection "$reason" rehearse "$mode"
	if grep -q '^run watch ' "$SCRATCH/rehearsal-$mode/calls"; then
		fail "watched a run after $mode"
	fi
done
expect_rejection 'Error' rehearse watch-error
[ "$(wc -l < "$SCRATCH/rehearsal-timeout/sleeps")" -le 12 ] || fail 'run discovery exceeded its retry budget'
echo 'PASS: rehearsal silent/URL dispatch, delayed discovery, dispatch/list/watch failures, and bounded polling'

BIN="$SCRATCH/bin"
mkdir -p "$BIN"
mapfile -t NAMES < <(scripts/release-artifacts.sh list-binaries)
[ "${#NAMES[@]}" -eq 7 ] || fail "catalog lists ${#NAMES[@]} binaries; expected 7"
printf '%s\n' "${NAMES[@]}" | grep -qx 'ztd-windows-amd64.exe' || fail 'Windows executable suffix missing from catalog output'
for name in "${NAMES[@]}"; do
	if [ "$name" = ztd-linux-amd64 ]; then
		# Static fixture for the script's `version --json --fips` reader, not a
		# release binary. Left non-executable: artifact transfer drops the mode.
		cat > "$BIN/$name" <<'EOF'
#!/usr/bin/env bash
[ "${1:-}" = version ] && [ "${2:-}" = --json ] && [ "${3:-}" = --fips ] || exit 2
cat <<'META'
{"version":"v1.0.0","platform":"linux/amd64","source_revision":"REVISION_PLACEHOLDER","source_tree_modified":false,"fips140":{"enabled":true}}
META
EOF
		sed -i "s/REVISION_PLACEHOLDER/$HEAD_SHA/" "$BIN/$name"
		chmod -x "$BIN/$name"
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
scripts/release-artifacts.sh verify "$BIN" 1.0.0 "$HEAD_SHA" > "$SCRATCH/verify.out"
if [ "$(uname -s)/$(uname -m)" = "Linux/x86_64" ]; then
	grep -qF 'host binary metadata verified' "$SCRATCH/verify.out" || fail 'host binary metadata was not verified'
fi
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

# A successful rehearsal cannot predict a later Sigstore outage. Exercise the
# actual signer against a transport double: a failed attempt may leave a partial
# bundle, and retries must generate fresh signatures rather than trust that file.
cat > "$MOCK_BIN/cosign" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
command=$1
shift
bundle='' artifact=''
while [ $# -gt 0 ]; do
	case "$1" in
		--bundle) bundle=$2; shift 2 ;;
		--certificate-identity|--certificate-oidc-issuer|--timeout) shift 2 ;;
		--yes) shift ;;
		*) artifact=$1; shift ;;
	esac
done
name=${artifact##*/}
printf '%s %s\n' "$command" "$name" >> "$SIGN_CASE/calls"
case "$command" in
	sign-blob)
		count=0
		[ ! -f "$SIGN_CASE/$name.count" ] || read -r count < "$SIGN_CASE/$name.count"
		count=$((count + 1))
		printf '%s\n' "$count" > "$SIGN_CASE/$name.count"
		[ ! -e "$bundle" ] || { echo 'attempt reused a stale bundle' >&2; exit 2; }
		if [ "$SIGN_MODE" = unavailable ] || { [ "$SIGN_MODE" = conflict ] && [ "$count" -eq 1 ]; }; then
			printf partial > "$bundle"
			echo 'createLogEntryConflict: an equivalent entry already exists in the transparency log' >&2
			exit 1
		fi
		sha256sum "$artifact" | cut -d' ' -f1 > "$bundle"
		;;
	verify-blob)
		[ "$SIGN_MODE" != invalid ] || exit 1
		[ "$(cat "$bundle")" = "$(sha256sum "$artifact" | cut -d' ' -f1)" ]
		;;
	*) exit 2 ;;
esac
EOF
cat > "$MOCK_BIN/sleep" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$SIGN_CASE/sleeps"
EOF
chmod +x "$MOCK_BIN/cosign" "$MOCK_BIN/sleep"

sign_case() {
	SIGN_MODE=$1
	SIGN_CASE="$SCRATCH/sign-$1"
	export SIGN_MODE SIGN_CASE
	mkdir "$SIGN_CASE"
	PATH="$MOCK_BIN:$PATH" ZTD_COSIGN_IDENTITY=https://example.invalid/release \
		scripts/release-artifacts.sh sign "$BIN"
}

if ! sign_case conflict > "$SCRATCH/result" 2>&1; then
	cat "$SCRATCH/result" >&2
	fail 'signing did not recover from a Rekor conflict'
fi
for name in "${NAMES[@]}"; do
	[ "$(cat "$SIGN_CASE/$name.count")" -eq 2 ] || fail "signing did not retry $name independently"
done
expect_rejection 'signing failed after 3 attempts' sign_case unavailable
[ "$(wc -l < "$SIGN_CASE/calls")" -eq 3 ] || fail 'signing exceeded its retry budget or verified a partial bundle'
expect_rejection 'signature verification failed' sign_case invalid
[ "$(wc -l < "$SIGN_CASE/calls")" -eq 2 ] || fail 'verification failure was retried or ignored'
echo 'PASS: signing conflict recovery, fresh bundles, bounded failure, and fail-closed verification'

# Exercise real R2 orchestration with a local object-transport double. Lost
# replies must be recoverable without deleting or overwriting immutable bytes.
mkdir -p "$REPO/website/node_modules/.bin"
cat > "$REPO/website/node_modules/.bin/wrangler" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[ "$1 $2" = 'r2 object' ] || exit 2
operation=$3 key=$4 file=''
shift 4
while [ $# -gt 0 ]; do
	case "$1" in
		--file) file=$2; shift 2 ;;
		--content-type|--persist-to) shift 2 ;;
		--pipe|--local|--remote) shift ;;
		*) exit 2 ;;
	esac
done
printf '%s %s\n' "$operation" "$key" >> "$R2_STORE/calls"
object="$R2_STORE/$key"
case "$operation" in
	get)
		[ "${R2_READ_ERROR:-false}" = false ] || { echo 'HTTP 403: forbidden' >&2; exit 1; }
		[ -f "$object" ] || { echo 'The specified key does not exist' >&2; exit 1; }
		cat "$object"
		;;
	put)
		mkdir -p "${object%/*}"
		cp "$file" "$object"
		if [ "${R2_LOST_REPLY:-}" = "$key" ]; then
			echo 'connection reset after upload' >&2
			exit 1
		fi
		;;
	delete) rm -f "$object" ;;
	*) exit 2 ;;
esac
EOF
chmod +x "$REPO/website/node_modules/.bin/wrangler"
R2_STORE="$SCRATCH/r2-store"
export R2_STORE
mkdir "$R2_STORE"
export ZTD_R2_PERSIST_TO="$R2_STORE"
expect_rejection 'connection reset after upload' env R2_LOST_REPLY="ztd-releases/v1.0.0/${NAMES[0]}" \
	scripts/release-r2.sh upload "$STAGED" 1.0.0
[ ! -e "$R2_STORE/ztd-releases/v1.0.0/ztd-binaries.json" ] || fail 'partial upload published a manifest'
if ! scripts/release-r2.sh upload "$STAGED" 1.0.0 > "$SCRATCH/result" 2>&1; then
	cat "$SCRATCH/result" >&2
	fail 'R2 upload could not resume after a lost successful reply'
fi
[ "$(grep -c "^put ztd-releases/v1.0.0/${NAMES[0]}$" "$R2_STORE/calls")" -eq 1 ] || fail 'resume overwrote an existing object'
scripts/release-r2.sh promote "$STAGED" 1.0.0 >/dev/null
scripts/release-r2.sh upload "$STAGED" 1.0.0 >/dev/null
[ "$(grep -c '^put ztd-releases/v1.0.0/' "$R2_STORE/calls")" -eq 23 ] || fail 'repeat upload overwrote immutable objects'
printf tampered > "$R2_STORE/ztd-releases/v1.0.0/${NAMES[1]}"
expect_rejection 'does not match' scripts/release-r2.sh upload "$STAGED" 1.0.0
expect_rejection 'not promoting' scripts/release-r2.sh promote "$STAGED" 1.0.0
expect_rejection 'HTTP 403: forbidden' env R2_READ_ERROR=true scripts/release-r2.sh upload "$STAGED" 1.0.0
scripts/release-r2.sh upload "$STAGED" 1.0.0 _rehearsal/123-1 >/dev/null
scripts/release-r2.sh promote "$STAGED" 1.0.0 _rehearsal/123-1 >/dev/null
[ "$(jq -r .version "$R2_STORE/ztd-releases/latest.json")" = 1.0.0 ] || fail 'rehearsal changed the public pointer'
scripts/release-r2.sh clean-rehearsal "$STAGED" 1.0.0 _rehearsal/123-1 >/dev/null
[ -z "$(find "$R2_STORE/ztd-releases/_rehearsal" -type f -print -quit)" ] || fail 'rehearsal cleanup left objects'
expect_rejection 'requires an isolated rehearsal namespace' scripts/release-r2.sh clean-rehearsal "$STAGED" 1.0.0
scripts/release-r2.sh check >/dev/null
expect_rejection 'cannot read back' env R2_READ_ERROR=true scripts/release-r2.sh check
[ -z "$(find "$R2_STORE/ztd-releases/_preflight" -type f -print -quit)" ] || fail 'failed credential probe left an object'
echo 'PASS: R2 interrupted/repeat upload, immutable conflict, read failure, isolated rehearsal, and cleanup'

# Both modes execute the same publication script. Only the transport is fake;
# verification, recovery, publication ordering, and cleanup are production code.
cp "$SOURCE_ROOT/scripts/release-publish.sh" "$REPO/scripts/"
sign_case healthy >/dev/null
scripts/release-artifacts.sh stage "$BIN" "$STAGED" >/dev/null
cat > "$MOCK_BIN/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$PUB_CASE/calls"
case "$1 $2" in
	'api --paginate')
		[ "${PUB_FAIL:-}" != api ] || { echo 'HTTP 403: forbidden' >&2; exit 1; }
		if [ "${PUB_FAIL:-}" = malformed ]; then echo '{}'; exit; fi
		if [ -f "$PUB_CASE/release.json" ]; then jq -s '.' "$PUB_CASE/release.json"; else echo '[]'; fi
		;;
	'api repos/{owner}/{repo}/git/matching-refs/tags/'*)
		[ ! -f "$PUB_CASE/tag-ref" ] || cat "$PUB_CASE/tag-ref"
		;;
	'api --method')
		[ "$3" = DELETE ] && [[ "$4" == */tags/rehearsal-* ]] || exit 2
		rm "$PUB_CASE/tag-ref"
		;;
	'release create')
		tag=$3 target=''
		shift 3
		while [ $# -gt 0 ]; do
			case "$1" in
				--target) target=$2; shift 2 ;;
				--draft|--verify-tag) shift ;;
				--title|--notes-file) shift 2 ;;
				*) exit 2 ;;
			esac
		done
		jq -n --arg tag "$tag" --arg revision "$target" \
			'{tag_name:$tag,target_commitish:$revision,draft:true}' > "$PUB_CASE/release.json"
		mkdir -p "$PUB_CASE/assets"
		if [[ "$tag" == rehearsal-* ]]; then printf 'refs/tags/%s\n' "$tag" > "$PUB_CASE/tag-ref"; fi
		;;
	'release view')
		[ "${PUB_FAIL:-}" != view ] || { echo 'HTTP 503: asset lookup unavailable' >&2; exit 1; }
		case "$5" in
			assets) find "$PUB_CASE/assets" -type f | wc -l ;;
			isDraft) jq -r .draft "$PUB_CASE/release.json" ;;
			*) exit 2 ;;
		esac
		;;
	'release upload')
		cp "$4" "$PUB_CASE/assets/"
		if [ "${PUB_FAIL:-}" = upload ]; then echo 'upload reply lost' >&2; exit 1; fi
		;;
	'release download')
		[ "$4" = --dir ] || exit 2
		cp "$PUB_CASE/assets/"* "$5/"
		if [ "${PUB_FAIL:-}" = readback ]; then printf corrupt > "$5/checksums-sha256.txt"; fi
		;;
	'release edit')
		[ "$4" = --draft=false ] || exit 2
		[ "${PUB_FAIL:-}" != publish ] || { echo 'publication unavailable' >&2; exit 1; }
		jq '.draft = false' "$PUB_CASE/release.json" > "$PUB_CASE/updated.json"
		mv "$PUB_CASE/updated.json" "$PUB_CASE/release.json"
		;;
	'release delete')
		[[ "$3" == rehearsal-* ]] || exit 2
		rm -rf "$PUB_CASE/release.json" "$PUB_CASE/assets"
		;;
	*) echo "unexpected gh call: $*" >&2; exit 2 ;;
esac
EOF
chmod +x "$MOCK_BIN/gh"
publication() {
	local mode=$1 scenario=$2
	PUB_CASE="$SCRATCH/publication-$scenario"
	PUB_FAIL=${3:-}
	R2_STORE="$PUB_CASE/r2"
	export PUB_CASE PUB_FAIL R2_STORE
	mkdir -p "$R2_STORE"
	PATH="$MOCK_BIN:$PATH" ZTD_COSIGN_IDENTITY=https://example.invalid/release \
		GITHUB_RUN_ID=456 GITHUB_RUN_ATTEMPT=1 \
		scripts/release-publish.sh "$mode" "$STAGED" 1.0.0 "$HEAD_SHA"
}
publication rehearse healthy >/dev/null
[ ! -f "$PUB_CASE/release.json" ] || fail 'successful rehearsal left a GitHub draft'
[ ! -f "$PUB_CASE/tag-ref" ] || fail 'successful rehearsal left a temporary tag'
[ ! -f "$R2_STORE/ztd-releases/latest.json" ] || fail 'rehearsal wrote the public pointer'
[ -z "$(find "$R2_STORE/ztd-releases" -type f -print -quit)" ] || fail 'rehearsal left R2 objects'
if grep -q '^release edit ' "$PUB_CASE/calls"; then fail 'rehearsal published its GitHub draft'; fi
printf unexpected > "$STAGED/.unexpected"
expect_rejection 'exactly the catalog artifacts' publication rehearse extra-file
[ ! -e "$PUB_CASE/calls" ] || fail 'invalid local set reached GitHub'
rm "$STAGED/.unexpected"
expect_rejection 'GitHub asset does not match' publication rehearse corrupt readback
[ ! -f "$PUB_CASE/release.json" ] || fail 'failed rehearsal left a GitHub draft'
[ ! -f "$PUB_CASE/tag-ref" ] || fail 'failed rehearsal left a temporary tag'
expect_rejection 'HTTP 403: forbidden' publication publish auth api
if grep -q '^release create ' "$PUB_CASE/calls"; then fail 'authorization failure was treated as an absent release'; fi
expect_rejection 'invalid release list' publication publish malformed malformed
if grep -q '^release create ' "$PUB_CASE/calls"; then fail 'malformed release list was treated as an absent release'; fi
expect_rejection 'HTTP 503: asset lookup unavailable' publication publish view-error view
if grep -q '^release upload ' "$PUB_CASE/calls"; then fail 'failed asset lookup was treated as an empty release'; fi
expect_rejection 'upload reply lost' publication publish retry-upload upload
[ ! -f "$R2_STORE/ztd-releases/latest.json" ] || fail 'failed upload moved the public pointer'
publication publish retry-upload >/dev/null
[ "$(jq -r .draft "$PUB_CASE/release.json")" = false ] || fail 'release was not published'
publication publish retry-upload >/dev/null
[ "$(grep -c '^release upload ' "$PUB_CASE/calls")" -eq 23 ] || fail 'resume re-uploaded existing GitHub assets'
[ "$(grep -c '^release edit ' "$PUB_CASE/calls")" -eq 1 ] || fail 'resume repeated GitHub publication'
expect_rejection 'publication unavailable' publication publish retry-publication publish
[ ! -f "$R2_STORE/ztd-releases/latest.json" ] || fail 'pointer moved before GitHub publication'
publication publish retry-publication >/dev/null
[ "$(jq -r .version "$R2_STORE/ztd-releases/latest.json")" = 1.0.0 ] || fail 'resumed release was not promoted'
printf corrupt > "$PUB_CASE/assets/checksums-sha256.txt"
expect_rejection 'GitHub asset does not match' publication publish retry-publication
echo 'PASS: shared publication rehearsal, cleanup, asset read-back, authorization failure, interrupted upload, and publication recovery'
