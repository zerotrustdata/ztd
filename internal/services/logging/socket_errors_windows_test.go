// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build windows

package logging

import (
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/windows"
)

func TestErrorKindAndClientDisconnect_WindowsSocketCodes(t *testing.T) {
	for _, tc := range []struct {
		name           string
		err            error
		wantKind       string
		wantDisconnect bool
	}{
		{"refused", windows.WSAECONNREFUSED, "connection_refused", false},
		{"reset", windows.WSAECONNRESET, "connection_reset", true},
		{"aborted", windows.WSAECONNABORTED, "connection_reset", true},
		{"permission denied", windows.WSAEACCES, "other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &net.OpError{Op: "write", Net: "tcp", Err: &os.SyscallError{Syscall: "wsasend", Err: tc.err}}
			assert.Equal(t, tc.wantKind, ErrorKind(err).Value.String())
			assert.Equal(t, tc.wantDisconnect, IsClientDisconnect(err))
		})
	}
}

