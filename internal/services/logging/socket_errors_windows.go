// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build windows

package logging

import (
	"errors"

	"golang.org/x/sys/windows"
)

func isPlatformConnectionRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED)
}

func isPlatformConnectionReset(err error) bool {
	return errors.Is(err, windows.WSAECONNRESET) || errors.Is(err, windows.WSAECONNABORTED)
}

