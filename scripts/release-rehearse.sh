#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

set -euo pipefail

if [ "$(git rev-parse --abbrev-ref HEAD)" != main ] || [ -n "$(git status --porcelain)" ]; then
	echo 'Error: make release-rehearse runs on a clean main checkout.' >&2
	exit 1
fi
git fetch -q origin main
HEAD_SHA="$(git rev-parse HEAD)"
if [ "$HEAD_SHA" != "$(git rev-parse origin/main)" ]; then
	echo 'Error: HEAD is not origin/main. Push or pull, then retry.' >&2
	exit 1
fi

latest_rehearsal() {
	gh run list --workflow release-binary.yml --event workflow_dispatch --branch main --commit "$HEAD_SHA" \
		--limit 1 --json databaseId --jq '.[0].databaseId // empty'
}

# Dispatch success is the command's exit status. CLI output is intended for
# humans and may be empty, including with gh 2.45 in a noninteractive shell.
PREVIOUS_RUN_ID="$(latest_rehearsal)"
if ! gh workflow run release-binary.yml --ref main -f publish=false; then
	echo 'Error: could not dispatch release-binary.yml' >&2
	exit 1
fi

# Actions may take a few seconds to expose the run. Never mistake a prior
# rehearsal of this commit for the one just dispatched.
for ((attempt = 1; attempt <= 12; attempt++)); do
	RUN_ID="$(latest_rehearsal)"
	if [ -n "$RUN_ID" ] && [ "$RUN_ID" != "$PREVIOUS_RUN_ID" ]; then
		echo "Rehearsal: $RUN_ID (commit ${HEAD_SHA:0:8})"
		exec gh run watch "$RUN_ID" --exit-status
	fi
	if [ "$attempt" -lt 12 ]; then
		sleep 5
	fi
done

echo 'Error: release-binary.yml was dispatched but its run is not visible yet. Check existing runs before dispatching again:' >&2
echo "gh run list --workflow release-binary.yml --event workflow_dispatch --branch main --commit $HEAD_SHA" >&2
exit 1
