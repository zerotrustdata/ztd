//go:build unix

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package fs

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// lockFile takes a non-blocking exclusive or shared flock. flock locks belong
// to the open file description, so two opens in one process also conflict.
func lockFile(f *os.File, exclusive bool) error {
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	err := unix.Flock(int(f.Fd()), how|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return constants.ErrRuntimeFileLocked
	}
	return err
}
