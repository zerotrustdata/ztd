// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package fixtures

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestEvalStackUsesDistinctOutboundOperatorRoots verifies that Gateway and
// Operator canonical stores exist below their owner roots and that remote
// Operators actually connect over mTLS through the witness. The replay store
// shares its owner's canonical ztd.db, so distinct canonical DB paths prove
// replay reservations cannot leak between runtimes.
func TestEvalStackUsesDistinctOutboundOperatorRoots(t *testing.T) {
	stack := NewEvalStack(t, EvalStackOptions{})
	_, err := stack.Gateway.FileService.Stat(t.Context(), constants.CanonicalDBRelPath)
	require.NoError(t, err)

	dataFS, err := fs.NewRuntimeFileService(stack.DataHost.WorkingDir, testutil.NewTestLogger())
	require.NoError(t, err)
	_, err = dataFS.Stat(t.Context(), constants.CanonicalDBRelPath)
	require.NoError(t, err)
	inferenceFS, err := fs.NewRuntimeFileService(stack.InferenceHost.WorkingDir, testutil.NewTestLogger())
	require.NoError(t, err)
	_, err = inferenceFS.Stat(t.Context(), constants.CanonicalDBRelPath)
	require.NoError(t, err)

	gatewayDB := stack.Gateway.FileService.Resolve(constants.CanonicalDBRelPath)
	dataDB := dataFS.Resolve(constants.CanonicalDBRelPath)
	inferenceDB := inferenceFS.Resolve(constants.CanonicalDBRelPath)
	require.NotEqual(t, gatewayDB, dataDB)
	require.NotEqual(t, gatewayDB, inferenceDB)
	require.NotEqual(t, dataDB, inferenceDB)
	logger := testutil.NewTestLogger()
	dataSQLDB, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dataDB), logger)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := dataSQLDB.Close(); err != nil {
			t.Errorf("close Data Operator evidence DB connection: %v", err)
		}
	})
	dataReplay, err := storage.NewReplayStore(dataSQLDB, logger)
	require.NoError(t, err)
	probeNonce := "eval-stack-isolation-probe"
	expiresAt := time.Now().Add(time.Minute)
	gatewayReplay := stack.Gateway.Service.GetReplayStore()
	replayed, err := gatewayReplay.ReserveNonce(probeNonce, expiresAt)
	require.NoError(t, err)
	require.False(t, replayed)
	replayed, err = gatewayReplay.ReserveNonce(probeNonce, expiresAt)
	require.NoError(t, err)
	require.True(t, replayed)
	operatorReplay, err := dataReplay.ReserveNonce(probeNonce, expiresAt)
	require.NoError(t, err)
	require.False(t, operatorReplay, "Data Operator replay store must be independent of Gateway replay store")
	t.Cleanup(func() {
		if err := gatewayReplay.ReleaseNonce(probeNonce); err != nil {
			t.Errorf("release Gateway replay evidence nonce: %v", err)
		}
		if err := dataReplay.ReleaseNonce(probeNonce); err != nil {
			t.Errorf("release Data Operator replay evidence nonce: %v", err)
		}
	})
	stack.RequireOutboundMTLS(t, stack.Data)
	stack.RequireOutboundMTLS(t, stack.Inference)
}
