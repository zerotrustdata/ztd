// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package fileidentity

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// fileBasicInfo mirrors FILE_BASIC_INFO; times are 100ns FILETIME ticks.
type fileBasicInfo struct {
	CreationTime   int64
	LastAccessTime int64
	LastWriteTime  int64
	ChangeTime     int64
	FileAttributes uint32
	_              uint32
}

// FILETIME ticks between 1601-01-01 and the Unix epoch.
const fileTimeUnixEpochTicks = 116444736000000000

func fileChangeTimeNanos(file *os.File, _ os.FileInfo) (int64, error) {
	var info fileBasicInfo
	if err := windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileBasicInfo, (*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, fmt.Errorf("file identity: %w: %w", constants.ErrFileIdentityUnavailable, err)
	}
	return (info.ChangeTime - fileTimeUnixEpochTicks) * 100, nil
}
