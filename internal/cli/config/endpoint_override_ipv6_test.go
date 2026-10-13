// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOperatorURLs_IPv6Overrides(t *testing.T) {
	tests := []struct {
		name      string
		override  string
		wantHTTP  string
		wantHTTPS string
	}{
		{"bare ipv6 gets brackets and default port", "fd00::1", "http://[fd00::1]:8080", "https://[fd00::1]:8443"},
		{"bracketed ipv6 gets default port", "[fd00::1]", "http://[fd00::1]:8080", "https://[fd00::1]:8443"},
		{"bracketed ipv6 keeps explicit port", "[fd00::1]:9000", "http://[fd00::1]:9000", "https://[fd00::1]:9000"},
		{"hostname gets default port", "gateway.local", "http://gateway.local:8080", "https://gateway.local:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetHTTPEndpointOverride(tt.override)
			SetHTTPSEndpointOverride(tt.override)
			t.Cleanup(func() {
				SetHTTPEndpointOverride("")
				SetHTTPSEndpointOverride("")
			})
			cfg := &Config{}
			assert.Equal(t, tt.wantHTTP, cfg.OperatorDiscoveryURL())
			assert.Equal(t, tt.wantHTTPS, cfg.OperatorHTTPURL())
		})
	}
}
