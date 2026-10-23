// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestPrivilegedRouteRegistry_PrivilegedPaths(t *testing.T) {
	t.Parallel()

	registry := NewPrivilegedRouteRegistry()

	privilegedPaths := []string{
		constants.APIPaths.QueryPrefix,
		constants.APIPaths.QueryPrefix + "/some-query",
		constants.APIPaths.QueryPrefix + "/operators",
	}

	for _, path := range privilegedPaths {
		assert.True(t, registry.IsPrivileged(path), "Path %s should be privileged", path)
	}
}

func TestPrivilegedRouteRegistry_NonPrivilegedPaths(t *testing.T) {
	t.Parallel()

	registry := NewPrivilegedRouteRegistry()

	nonPrivilegedPaths := []string{
		constants.APIPaths.GovernanceEnvelopes,
		constants.APIPaths.Health,
		constants.APIPaths.State,
		constants.APIPaths.MCPEndpoint,
		constants.APIPaths.A2ACall,
		constants.APIPaths.AuditEventReceipts,
		constants.APIPaths.Operators,
		constants.APIPaths.DataSettings,
		"/api/v1/pki/apps/enroll",
	}

	for _, path := range nonPrivilegedPaths {
		assert.False(t, registry.IsPrivileged(path), "Path %s should NOT be privileged", path)
	}
}

func TestPrivilegedRouteRegistry_CanonicalCoverage(t *testing.T) {
	t.Parallel()

	registry := NewPrivilegedRouteRegistry()

	assert.False(t, registry.IsPrivileged(constants.APIPaths.GovernanceEnvelopes),
		"Governance envelopes admit policy-authorized app certificates")
	assert.True(t, registry.IsPrivileged(constants.APIPaths.QueryPrefix),
		"Query prefix must be privileged (app certs blocked)")
}
