// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package model_provenance

import (
	"fmt"
	"os"
	"syscall"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func fileChangeTimeNanos(_ *os.File, info os.FileInfo) (int64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("model provenance attestor: %w", constants.ErrModelProvenanceBlobIdentity)
	}
	return stat.Ctim.Nano(), nil
}
