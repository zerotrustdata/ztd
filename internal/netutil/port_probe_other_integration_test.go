// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build !windows && integration

package netutil

import (
	"errors"
	"io"
	"net"
	"runtime"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestTCPPortProbe_RejectsWildcardAndLoopbackConflictsAndReleasesPort(t *testing.T) {
	for _, tc := range []struct {
		name, network, address string
	}{
		{"IPv4 loopback", "tcp4", "127.0.0.1:0"},
		{"IPv4 wildcard", "tcp4", "0.0.0.0:0"},
		{"IPv6 loopback", "tcp6", "[::1]:0"},
		{"IPv6 wildcard", "tcp6", "[::]:0"},
		{"default wildcard", "tcp", ":0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen(tc.network, tc.address)
			if tc.network == "tcp6" && (errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EPROTONOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL)) {
				t.Skip("IPv6 is unavailable on this host")
			}
			require.NoError(t, err)
			t.Cleanup(func() { _ = listener.Close() })
			port := listener.Addr().(*net.TCPAddr).Port
			require.ErrorIs(t, CheckTCPPortAvailable(port), constants.ErrPortUnavailable)
			require.NoError(t, listener.Close())

			for range 3 {
				require.NoError(t, CheckTCPPortAvailable(port), "probe must release both address families")
			}
		})
	}
}

// A Gateway that just stopped leaves server-side TIME_WAIT sockets on its
// ports. The Gateway's own listener binds over them, so the probe must agree
// on Linux; otherwise every quick restart shifts off the default ports.
func TestTCPPortProbe_AcceptsTimeWaitOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("TIME_WAIT reuse semantics are asserted on Linux only")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port

	accepted := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			accepted <- acceptErr
			return
		}
		accepted <- conn.Close() // server closes first: TIME_WAIT on the listener port
	}()
	client, err := net.Dial("tcp4", listener.Addr().String())
	require.NoError(t, err)
	require.NoError(t, <-accepted)
	_, err = client.Read(make([]byte, 1))
	require.ErrorIs(t, err, io.EOF)
	require.NoError(t, client.Close())
	require.NoError(t, listener.Close())

	require.NoError(t, CheckTCPPortAvailable(port))
}
