// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"testing"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// WithGatewayHealthCheck stubs gateway publication health checks for t.
func WithGatewayHealthCheck(t *testing.T, healthy bool) {
	t.Helper()
	original := GatewayHealthCheck
	GatewayHealthCheck = func() bool { return healthy }
	t.Cleanup(func() { GatewayHealthCheck = original })
}

// WithConfiguredGatewayHealthCheck stubs the configured-Gateway health check
// used by provider observation and campaign witness capture for t.
func WithConfiguredGatewayHealthCheck(t *testing.T, healthy bool) {
	t.Helper()
	original := ConfiguredGatewayHealthCheck
	ConfiguredGatewayHealthCheck = func(fs.RuntimeFileService, *config.Config) bool { return healthy }
	t.Cleanup(func() { ConfiguredGatewayHealthCheck = original })
}
