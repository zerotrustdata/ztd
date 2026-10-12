//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway_test

import (
	"testing"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/paths"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// TestGatewayRuntimeRoot_OneDatabaseAndLedger verifies INV-STOR-ONE-01 on a
// running Gateway with its embedded Operator: after startup and one governed
// submission (rejected at L4, which still reserves replay state and audits),
// the runtime data directory holds only ztd.db (plus SQLite sidecars) and the
// file ledger.
func TestGatewayRuntimeRoot_OneDatabaseAndLedger(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "runtime-root-one-db",
		Posture:  config.PostureConsensus,
	})
	f.WaitForReady(t)
	client := seedUnprovenAppWorkload(t, f)
	assertUnprovenEnvelopeRejectedBeforeExecution(t, f, client, constants.ErrTxL2SignatureMissing)

	testutil.AssertOneDatabaseAndLedger(t, paths.Infra.DataDir)
}
