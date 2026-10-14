//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestZtdoService_Start_RejectsSecondStartWithoutDisturbingRunningService(t *testing.T) {
	service, err := NewZtdoService(testutil.NewTestConfig(t), testutil.NewTestLogger(), newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestCanonicalDB(t), newTestPubSubClientFactory())
	require.NoError(t, err)
	service.running = true

	err = service.Start(t.Context())

	require.ErrorIs(t, err, constants.ErrServiceUnavailable)
	assert.True(t, service.running, "a rejected second start must leave the service running")
	assert.Nil(t, service.cancel, "a rejected start must not create a new service context")
	assert.Nil(t, service.pubSubClient, "a rejected start must not open a second pub/sub client")
}

func TestZtdoService_Start_RequiresRuntimeFileService(t *testing.T) {
	service, err := NewZtdoService(testutil.NewTestConfig(t), testutil.NewTestLogger(), newTestTLSConfig(t), newTestL2Trust(t), nil, newTestCanonicalDB(t), newTestPubSubClientFactory())
	require.NoError(t, err)

	err = service.Start(t.Context())

	require.ErrorIs(t, err, constants.ErrInternal)
	assert.Contains(t, err.Error(), "fileSvc must be provided")
	assert.False(t, service.running)
	assert.Nil(t, service.pubSubClient, "nothing may be connected before the file service is validated")
	if service.cancel != nil {
		service.cancel()
	}
}
