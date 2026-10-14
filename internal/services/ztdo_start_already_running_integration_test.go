//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestZtdoService_Start_AlreadyRunning(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()

	service, err := NewZtdoService(cfg, logger, newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestCanonicalDB(t), newTestPubSubClientFactory())
	require.NoError(t, err)

	service.mu.Lock()
	service.running = true
	service.mu.Unlock()

	err = service.Start(context.Background())
	require.Error(t, err)
	assert.Error(t, err)
}
