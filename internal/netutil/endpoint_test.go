// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package netutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestSplitEndpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		endpoint string
		wantHost string
		wantPort int
	}{
		{"hostname", "gateway.local", "gateway.local", 0},
		{"ipv4", "10.0.0.1", "10.0.0.1", 0},
		{"hostname with port", "gateway.local:8443", "gateway.local", 8443},
		{"ipv4 with port", "10.0.0.1:8443", "10.0.0.1", 8443},
		{"bare ipv6", "fe80::1", "fe80::1", 0},
		{"bracketed ipv6", "[fe80::1]", "fe80::1", 0},
		{"bracketed ipv6 with port", "[fe80::1]:8443", "fe80::1", 8443},
		{"loopback ipv6 with port", "[::1]:8080", "::1", 8080},
		{"surrounding whitespace", "  gateway.local:8443 \n", "gateway.local", 8443},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host, port, err := SplitEndpoint(tt.endpoint)
			require.NoError(t, err)
			assert.Equal(t, tt.wantHost, host)
			assert.Equal(t, tt.wantPort, port)
		})
	}
}

func TestSplitEndpoint_Rejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		endpoint string
	}{
		{"empty host", ":8443"},
		{"empty port", "gateway.local:"},
		{"non-numeric port", "gateway.local:https"},
		{"port zero", "gateway.local:0"},
		{"port out of range", "gateway.local:70000"},
		{"unterminated bracket", "[fe80::1"},
		{"bracketed without port but trailing junk", "[fe80::1]x"},
		{"bracketed ipv6 with bad port", "[fe80::1]:abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := SplitEndpoint(tt.endpoint)
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrGatewayEndpointInvalid)
		})
	}
}

func TestIsLoopbackEndpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{"empty", "", false},
		{"localhost", "localhost", true},
		{"localhost with port", "localhost:8443", true},
		{"localhost url", "https://LOCALHOST:8443/x", true},
		{"ipv4 loopback", "127.0.0.1:8443", true},
		{"ipv6 loopback", "[::1]:8443", true},
		{"ipv6 loopback bare", "::1", true},
		{"unspecified", "0.0.0.0", true},
		{"routable ipv4", "10.0.0.1:8443", false},
		{"routable ipv6", "[fd00::1]:8443", false},
		{"hostname", "gateway.example.com", false},
		{"malformed", ":8443", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsLoopbackEndpoint(tt.endpoint))
		})
	}
}
