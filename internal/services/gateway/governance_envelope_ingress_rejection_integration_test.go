// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// TestGovernanceEnvelopeIntegration_NotaryPostureRejectsUnprovenMutationAtWarden
// runs the real Gateway under notary posture, where L2 and L3 both gate
// execution. A DOCUMENT_UPDATE with neither proof must fail closed at L4 with
// ErrTxL2SignatureMissing, because L2 is checked before L3, and never reach L5.
func TestGovernanceEnvelopeIntegration_NotaryPostureRejectsUnprovenMutationAtWarden(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "governance-envelope-notary-unproven",
		Posture:  constants.PostureNotary,
	})
	f.WaitForReady(t)
	client := seedUnprovenAppWorkload(t, f)

	assertUnprovenEnvelopeRejectedBeforeExecution(t, f, client, constants.ErrTxL2SignatureMissing)
}

// TestGovernanceEnvelopeIntegration_StateRootMismatchRejectedAtIngress runs the
// real Gateway under doctrine posture with a hash-consistent DOCUMENT_UPDATE
// whose state_merkle_root is stale. L1 passes, the stateful check fails, and the
// Gateway must reject with ErrTxStateRootMismatch (HTTP 403) before any L5 work.
func TestGovernanceEnvelopeIntegration_StateRootMismatchRejectedAtIngress(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "governance-envelope-state-root-mismatch",
		Posture:  constants.PostureDoctrine,
	})
	f.WaitForReady(t)
	client := seedUnprovenAppWorkload(t, f)

	const documentID = "l4-state-root-mismatch-document"
	env := unprovenDocumentUpdateEnvelope(t, f, documentID, "l4-state-root-mismatch-nonce")
	env.StateMerkleRoot = strings.Repeat("0", 64)
	var err error
	env.Id, err = govtypes.GenerateMessageID(env)
	require.NoError(t, err)
	env.TransactionHash = env.Id

	assertEnvelopeRejectedAtIngress(t, f, client, env, documentID, constants.ErrTxStateRootMismatch)
}
