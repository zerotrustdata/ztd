// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux || darwin

package config

import (
	"os"
	"syscall"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Nonblocking open prevents a FIFO from hanging startup; O_NOFOLLOW refuses a
// symlinked env file. Metadata is checked on this same descriptor.
func openEnvFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY|syscall.O_NOFOLLOW, 0)
}

func validateEnvFileMetadata(info os.FileInfo) error {
	mode := info.Mode()
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) ||
		(mode.Perm() != constants.PermFileReadOnly && mode.Perm() != constants.PermFilePrivate) {
		return constants.ErrEnvFileInvalid
	}
	return nil
}
