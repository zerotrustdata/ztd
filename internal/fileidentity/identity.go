// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package fileidentity reads filesystem identities for cached model-weight hashes.
package fileidentity

import (
	"fmt"
	"os"
	"time"
)

// Identity binds a file hash to its file ID, size, modification time, and
// change time. Change time detects writes followed by restoration of mtime.
type Identity struct {
	Info        os.FileInfo
	changeNanos int64
}

// Equal reports whether both observations refer to the same unchanged file.
func (a Identity) Equal(b Identity) bool {
	return os.SameFile(a.Info, b.Info) && a.Info.Size() == b.Info.Size() && a.Info.ModTime().Equal(b.Info.ModTime()) && a.changeNanos == b.changeNanos
}

// LastChangedAt returns the later of modification and filesystem change time.
func (a Identity) LastChangedAt() time.Time {
	return time.Unix(0, max(a.Info.ModTime().UnixNano(), a.changeNanos))
}

// Read reads the identity from an open file handle.
func Read(file *os.File) (Identity, error) {
	info, err := file.Stat()
	if err != nil {
		return Identity{}, fmt.Errorf("file identity: stat: %w", err)
	}
	changeNanos, err := fileChangeTimeNanos(file, info)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Info: info, changeNanos: changeNanos}, nil
}

// Stat opens path and reads its current identity, following model-store symlinks.
func Stat(path string) (Identity, error) {
	file, err := os.Open(path)
	if err != nil {
		return Identity{}, fmt.Errorf("file identity: open %s: %w", path, err)
	}
	defer file.Close()
	return Read(file)
}
