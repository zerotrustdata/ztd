// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package serve

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseWSLAddresses(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"172.29.183.254"}, parseWSLAddresses("172.29.183.254 \n"))
	assert.Equal(t, []string{"172.29.183.254", "10.0.0.5"}, parseWSLAddresses("172.29.183.254 10.0.0.5 fd00::1\n"))
	assert.Empty(t, parseWSLAddresses("127.0.0.1 ::1 not-an-ip"))
	assert.Empty(t, parseWSLAddresses(""))
}

func TestDiscoverImplicitGatewayHost(t *testing.T) {
	t.Parallel()

	const wslIP = "172.29.183.254"
	reachableOnly := func(hosts ...string) func(context.Context, string, int) bool {
		return func(_ context.Context, host string, _ int) bool {
			for _, h := range hosts {
				if h == host {
					return true
				}
			}
			return false
		}
	}
	wsl := func(context.Context) []string { return []string{"10.255.255.254", wslIP} }

	tests := []struct {
		name     string
		d        gatewayDiscovery
		explicit bool
		want     string
	}{
		{"loopback answers", gatewayDiscovery{"windows", reachableOnly("localhost"), wsl}, false, "localhost"},
		{"loopback silent falls back to WSL address", gatewayDiscovery{"windows", reachableOnly(wslIP), wsl}, false, wslIP},
		{"nothing answers keeps localhost", gatewayDiscovery{"windows", reachableOnly(), wsl}, false, "localhost"},
		{"explicit endpoint is never replaced", gatewayDiscovery{"windows", reachableOnly(wslIP), wsl}, true, "localhost"},
		{"non-windows is untouched", gatewayDiscovery{"linux", reachableOnly(wslIP), wsl}, false, "localhost"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.d.discoverImplicitGatewayHost(context.Background(), tt.explicit, "localhost", 8080, slog.Default())
			assert.Equal(t, tt.want, got)
		})
	}
}
