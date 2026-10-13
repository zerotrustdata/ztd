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
	"net"
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
