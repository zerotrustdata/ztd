// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package fs

// runtimeWriteFailureHint suggests a repair for a failed write under .ztd/.
func runtimeWriteFailureHint(dir string) string {
	if hint := runtimeDirRepairHint(dir); hint != "" {
		return hint
	}
	return "check .ztd ownership and permissions"
}
