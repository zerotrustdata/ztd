// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration || e2e

package fixtures

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// EvalStackOptions configures the production-shaped eval fixture.
type EvalStackOptions struct {
	Posture               constants.Posture
	Replies               []OllamaReply
	EnablePublicSpectator bool
}

// EvalStack holds the real Gateway and outbound Operators used by eval
// integration tests. Only the model provider is scripted.
type EvalStack struct {
	Gateway       *GatewayFixture
	Identity      *ClientIdentity
	Owner         *OwnerConsole
	Witness       *OperatorWireWitness
	Data          *OperatorProcess
	Inference     *OperatorProcess
	DataHost      *OperatorHost
	InferenceHost *OperatorHost
	Provider      *ScriptedOllama
}

// NewEvalStack builds an isolated Gateway, bootstrap owner, wire witness, and
// distinct real Data and Inference Operators. All component cleanup is owned
// by the underlying fixtures' t.Cleanup registrations.
func NewEvalStack(t *testing.T, opts EvalStackOptions) *EvalStack {
	t.Helper()
	posture := opts.Posture
	if posture == "" {
		posture = constants.PostureDoctrine
	}
	gatewayOpts := GatewayFixtureOptions{
		TestName: "eval-stack", Posture: posture, ProductionConsensus: true,
		EnablePublicSpectator: opts.EnablePublicSpectator,
	}
	if posture == constants.PostureNotary {
		gatewayOpts.ConsensusID = "eval-stack-consensus"
		gatewayOpts.ConsensusNMembers = 3
		gatewayOpts.ConsensusQuorum = 2
		gatewayOpts.ConsensusNServiceMembers = 2
	}
	gateway := NewGatewayFixture(t, gatewayOpts)
	gateway.WaitForReady(t)
	identity := BootstrapCLIIdentity(t, gateway, "eval-stack-owner")
	owner := AttachOwnerConsole(t, gateway, identity.UserID, identity.CLISessionID, OwnerApprovesEach)
	if posture.RequiresL3() {
		owner.ApproveTransactionsWithCLI(t, identity)
	}
	witness := NewOperatorWireWitness(t, gateway)
	provider := NewScriptedOllama(t, opts.Replies)

	dataHost := NewOperatorHost(t)
	inferenceHost := NewOperatorHost(t)
	data := witness.LaunchOperator(t, dataHost, OperatorProcessOptions{
		Capabilities: constants.OperatorCapabilities{constants.OperatorCapabilityData},
	})
	inference := witness.LaunchOperator(t, inferenceHost, OperatorProcessOptions{
		Capabilities:            constants.OperatorCapabilities{constants.OperatorCapabilityInference, constants.OperatorCapabilityObserver},
		InferenceOllamaEndpoint: provider.Endpoint(),
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, data.AwaitStarted(ctx))
	_, err := data.AwaitHeartbeat(ctx)
	require.NoError(t, err)
	require.NoError(t, inference.AwaitStarted(ctx))
	_, err = inference.AwaitHeartbeat(ctx)
	require.NoError(t, err)

	return &EvalStack{
		Gateway: gateway, Identity: identity, Owner: owner, Witness: witness,
		Data: data, Inference: inference, DataHost: dataHost,
		InferenceHost: inferenceHost, Provider: provider,
	}
}

// RequireOutboundMTLS verifies that the witness observed this Operator's
// enrollment-bound certificate on its TLS listener.
func (s *EvalStack) RequireOutboundMTLS(t *testing.T, operator *OperatorProcess) {
	t.Helper()
	leaf, err := operator.Leaf()
	require.NoError(t, err)
	serial := leaf.SerialNumber.String()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err = s.Witness.AwaitExchangeAfter(ctx, 0, func(exchange WireExchange) bool {
		return exchange.Listener == WireListenerTLS && exchange.ClientCertSerial == serial
	})
	require.NoError(t, err, "operator certificate serial %s was not observed over mTLS", serial)
}
