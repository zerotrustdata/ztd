// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux || darwin

package keystore

import (
	"os"
	"syscall"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Nonblocking open prevents an invalid FIFO from hanging startup. The path is
// already symlink-resolved, so O_NOFOLLOW refuses a final component swapped
// for a symlink after that check. Metadata is checked on this same descriptor
// before reading; trusted directories remain required.
func openExternalKey(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY|syscall.O_NOFOLLOW, 0)
}

func validateExternalKeyMetadata(info os.FileInfo) error {
	mode := info.Mode()
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) ||
		(mode.Perm() != 0400 && mode.Perm() != constants.PermFilePrivate) ||
		mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return constants.ErrKeyStoreExternalFileInvalid
	}
	return nil
}
