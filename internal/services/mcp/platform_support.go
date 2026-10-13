// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"fmt"
	"runtime"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// unsupportedPlatformError reports that a tool's OS data source is absent, so
// callers get a typed failure instead of an empty success.
func unsupportedPlatformError(tool, requirement string) error {
	return fmt.Errorf("%s: %w: requires %s (host is %s)", tool, constants.ErrMCPToolUnsupportedPlatform, requirement, runtime.GOOS)
}
