// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package httpclient

import (
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConstants(t *testing.T) {
	assert.Equal(t, 30*time.Second, DefaultTimeout)
	assert.Equal(t, 10*time.Second, DefaultDialTimeout)
	assert.Equal(t, 10*time.Second, DefaultTLSTimeout)
	assert.Equal(t, 90*time.Second, DefaultIdleConnTimeout)
}

func TestWebSocketDialerWithTLS(t *testing.T) {
	customTLS := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}

	dialer := WebSocketDialerWithTLS(customTLS)
	require.NotNil(t, dialer)

	assert.Equal(t, customTLS, dialer.TLSClientConfig)
	assert.Equal(t, DefaultTLSTimeout, dialer.HandshakeTimeout)
}

// TestIPv4DialContext_RejectsLiteralIPv6Address proves a literal IPv6
// address (e.g. "[::1]") cannot be dialed through the IPv4 dialer — the
// ip4 lookup fails and returns an error, never an IPv6 connection.
func TestIPv4DialContext_RejectsLiteralIPv6Address(t *testing.T) {
	_, err := IPv4DialContext(context.Background(), "tcp", "[::1]:8443")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ipv4 dial: resolve")
}

// TestIPv4DialContext_SplitHostPortError confirms a malformed address
// surfaces the split error rather than panicking.
func TestIPv4DialContext_SplitHostPortError(t *testing.T) {
	_, err := IPv4DialContext(context.Background(), "tcp", "no-port-here")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ipv4 dial: split host/port")
}
