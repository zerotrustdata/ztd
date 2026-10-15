#!/usr/bin/env bash
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# Exercise first-time Linux contributor setup followed by a governed release
# smoke. This runs only on a fresh CI runner; release-smoke owns its processes.
set -euo pipefail
umask 077
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

if [[ -e .ztd || -e .env ]]; then
    echo "ERROR: onboarding smoke requires a fresh checkout without .ztd or .env" >&2
    exit 1
fi

# Invoke the documented contributor setup, not the build-only shortcut. This
# installs the contributor toolchain and builds the native ztd binary.
bash scripts/linux-setup.sh -y
make third-party-notices-check

# Setup creates the owner-only .env from the template byte-for-byte. Every
# entry is commented, so ./ztd loads no keys from it.
cmp .env.example .env
if [[ "$(stat -c %a .env)" != 600 ]]; then
    echo "ERROR: setup did not create .env with mode 600" >&2
    exit 1
fi

if [[ ! -x ./ztd ]]; then
    echo "ERROR: setup completed without producing ./ztd" >&2
    exit 1
fi

smoke_root="${RUNNER_TEMP:-$PWD/.ztd/tmp}/ztd-onboarding-smoke-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-0}"
if [[ -e "$smoke_root" ]]; then
    echo "ERROR: smoke evidence path already exists: $smoke_root" >&2
    exit 1
fi
mkdir -p "$(dirname "$smoke_root")"

# The maintained runner creates and tears down an isolated Gateway and data
# Operator, performs a governed file mutation, verifies read-back, and exports
# the local evidence bundle under <root>/evidence and <root>/reports.
./ztd test release-smoke --root "$smoke_root" --binary "$PWD/ztd"
echo "Onboarding smoke passed. Evidence: $smoke_root/evidence and $smoke_root/reports"
