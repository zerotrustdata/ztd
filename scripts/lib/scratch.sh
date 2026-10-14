# shellcheck shell=sh
# Copyright (c) 2026 Lateralus Labs, LLC.
# Use of this source code is governed by the Business Source License
# included in the LICENSE file.
#
# As of the Change Date listed in the LICENSE file, this software is
# released under the Apache License, Version 2.0.

# Runtime scratch for repo-run scripts (INV-FS-11): throwaway dirs and files live
# under <repo>/.ztd/tmp, never the OS temp dir or $HOME. POSIX sh so the release,
# website, and dev-setup scripts can all source it.

# usage: ztd_scratch_dir <repo_root> <prefix>
# Creates a private directory under <repo_root>/.ztd/tmp and prints its path.
# Scripts that run git inside it export GIT_CEILING_DIRECTORIES=<repo_root>/.ztd/tmp
# so fixture repositories can never fall through to the source repository.
ztd_scratch_dir() {
    _ztd_runtime="$1/.ztd"
    mkdir -p "$_ztd_runtime/tmp" &&
        chmod 700 "$_ztd_runtime" "$_ztd_runtime/tmp" &&
        mktemp -d "$_ztd_runtime/tmp/$2.XXXXXX"
}
