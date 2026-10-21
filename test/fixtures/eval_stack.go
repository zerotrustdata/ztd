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
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// EvalStackOptions configures the production-shaped eval fixture.
type EvalStackOptions struct {
	Posture               constants.Posture
	Replies               []OllamaReply
	EnablePublicSpectator bool
	// SharedRuntime serves the Data and Inference roles from one Operator
	// process, so both roles append to one commitment ledger.
	SharedRuntime bool
	// Provenance launches a Provenance Operator over a model store holding the
	// served model. The Gateway records attestation windows only through that
	// Operator, and the provider serves the store's digest for the model.
	Provenance bool
	// ModelTags are the tags the Provenance Operator's store holds and the
	// provider serves, each at ScriptedModelDigest(tag). The default model tag
	// is always included.
	ModelTags []string
	// A2ADownstreamURL opts the Gateway into A2A ingress, forwarding to this
	// downstream. Empty leaves the Gateway with no A2A ingress.
	A2ADownstreamURL string
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
	// Provenance is the Provenance Operator, nil unless EvalStackOptions.Provenance.
	Provenance *OperatorProcess
}

// NewEvalStack builds an isolated Gateway, bootstrap owner, wire witness, and
// distinct real Data and Inference Operators. All component cleanup is owned
// by the underlying fixtures' t.Cleanup registrations. Outside the evalstack
// build tag the test is skipped: real eval stacks are not part of CI.
func NewEvalStack(t *testing.T, opts EvalStackOptions) *EvalStack {
	t.Helper()
	if !evalStackEnabled {
		t.Skip("real eval stack tests run only under the evalstack build tag: ./ztd test integration --eval-stack")
	}
	posture := opts.Posture
	if posture == "" {
		posture = constants.PostureDoctrine
	}
	gatewayOpts := GatewayFixtureOptions{
		TestName: "eval-stack", Posture: posture, ProductionConsensus: true,
		EnablePublicSpectator: opts.EnablePublicSpectator,
		NoDownstream:          true,
		A2ADownstreamURL:      opts.A2ADownstreamURL,
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

	var dataHost, inferenceHost *OperatorHost
	var data, inference *OperatorProcess
	if opts.SharedRuntime {
		dataHost = NewOperatorHost(t)
		inferenceHost = dataHost
		data = witness.LaunchOperator(t, dataHost, OperatorProcessOptions{
			Capabilities: constants.OperatorCapabilities{
				constants.OperatorCapabilityData, constants.OperatorCapabilityInference, constants.OperatorCapabilityObserver,
			},
			InferenceOllamaEndpoint: provider.Endpoint(),
		})
		inference = data
	} else {
		dataHost = NewOperatorHost(t)
		inferenceHost = NewOperatorHost(t)
		data = witness.LaunchOperator(t, dataHost, OperatorProcessOptions{
			Capabilities: constants.OperatorCapabilities{constants.OperatorCapabilityData},
		})
		inference = witness.LaunchOperator(t, inferenceHost, OperatorProcessOptions{
			Capabilities:            constants.OperatorCapabilities{constants.OperatorCapabilityInference, constants.OperatorCapabilityObserver},
			InferenceOllamaEndpoint: provider.Endpoint(),
		})
	}
	var provenance *OperatorProcess
	if opts.Provenance {
		tags := append([]string{evalModelTag}, opts.ModelTags...)
		for _, tag := range tags {
			provider.ServeModel(tag, ScriptedModelDigest(t, tag))
		}
		provenance = witness.LaunchOperator(t, NewOperatorHost(t), OperatorProcessOptions{
			Capabilities:               constants.OperatorCapabilities{constants.OperatorCapabilityProvenance},
			ProvenanceModelStorageRoot: NewOllamaModelStore(t, tags),
		})
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, data.AwaitStarted(ctx))
	_, err := data.AwaitHeartbeat(ctx)
	require.NoError(t, err)
	if inference != data {
		require.NoError(t, inference.AwaitStarted(ctx))
		_, err = inference.AwaitHeartbeat(ctx)
		require.NoError(t, err)
	}
	if provenance != nil {
		require.NoError(t, provenance.AwaitStarted(ctx))
		_, err = provenance.AwaitHeartbeat(ctx)
		require.NoError(t, err)
	}

	return &EvalStack{
		Gateway: gateway, Identity: identity, Owner: owner, Witness: witness,
		Data: data, Inference: inference, DataHost: dataHost,
		InferenceHost: inferenceHost, Provider: provider,
		Provenance: provenance,
	}
}

// EnrollEvalAppIdentity enrolls the ztd-eval application identity through the
// production app enrollment client. The authenticated owner's CLI identity
// approves the request, the same way the eval runner's own enrollment does.
// The CLI runtime must already be installed in fileSvc/cfg
// (ClientIdentity.InstallCLIRuntime).
func (s *EvalStack) EnrollEvalAppIdentity(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config) {
	t.Helper()
	ownerClient, err := api.NewClient(fileSvc, cfg)
	require.NoError(t, err)
	enroller, err := auth.NewAppPlatformEnrollmentClient(constants.EvaluationAppName, fileSvc, cfg, slog.Default(), auth.AppEnrollmentOptions{
		Approver: auth.OwnerAppEnrollmentApprover(ownerClient, "ztd eval: enrolled by the authenticated owner"),
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	_, err = enroller.Enroll(ctx, io.Discard)
	require.NoError(t, err)
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
