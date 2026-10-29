// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build windows

package keystore

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

func fdIsTerminal(fd uintptr) bool {
	var mode uint32
	return windows.GetConsoleMode(windows.Handle(fd), &mode) == nil
}

// ReaderIsTerminal reports whether in is an open terminal. Pipes, files, and
// in-memory readers are not terminals, so a recovery key is never written there.
func ReaderIsTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok || f == nil {
		return false
	}
	return fdIsTerminal(f.Fd())
}
