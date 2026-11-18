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
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
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
	// BindConsensus, when set, rebinds the Gateway's consensus before any
	// Operator enrolls. Outbound Operators receive the binding's L2 trust only
	// in their enrollment and renewal bundles.
	BindConsensus func(t *testing.T, gateway *GatewayFixture, identity *ClientIdentity)
	// DataOperators is the size of the Data fleet, the harness roster a run
	// may freeze. Zero or one launches only Data. Every further member is a
	// separately rooted Data-only Operator on its own host directory.
	DataOperators int
	// DeployTargetHostname, when non-empty, provisions an isolated deploy
	// target host and configures local deploy overrides on initial Data
	// Operators so operatordeploy stages into it without SSH.
	DeployTargetHostname string
}

// EvalStack holds the real Gateway and outbound Operators used by eval
// integration tests. Only the model provider is scripted.
type EvalStack struct {
	Gateway  *GatewayFixture
	Identity *ClientIdentity
	Owner    *OwnerConsole
	Witness  *OperatorWireWitness
	Data     *OperatorProcess
	// Fleet is every Data Operator in launch order; Fleet[0] is Data.
	Fleet []*OperatorProcess
	// FleetHosts are the Fleet's host directories, in the same order.
	FleetHosts    []*OperatorHost
	Inference     *OperatorProcess
	DataHost      *OperatorHost
	InferenceHost *OperatorHost
	// DeployHost is the provisioned target host for DeployTargetHostname, nil
	// unless EvalStackOptions.DeployTargetHostname is set.
	DeployHost *OperatorHost
	// DeployTargetHostname is the configured local deploy target hostname.
	DeployTargetHostname string
	Provider             *ScriptedOllama
	// Provenance is the Provenance Operator, nil unless EvalStackOptions.Provenance.
	Provenance *OperatorProcess
	// Posture is the posture the Gateway runs under.
	Posture constants.Posture
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
	if opts.BindConsensus != nil {
		opts.BindConsensus(t, gateway, identity)
	}
	witness := NewOperatorWireWitness(t, gateway)
	provider := NewScriptedOllama(t, opts.Replies)

	var deployHost *OperatorHost
	var deployOverrides map[string]models.OperatorDeployLocalOverride
	if opts.DeployTargetHostname != "" {
		deployHost = NewOperatorHost(t)
		var deployMu sync.Mutex
		var deployLaunched bool
		deployOverrides = map[string]models.OperatorDeployLocalOverride{
			opts.DeployTargetHostname: {
				DestDir: deployHost.WorkingDir,
				StartFunc: func(_ context.Context, _, _ string, args []string) (int, error) {
					deployMu.Lock()
					defer deployMu.Unlock()
					if deployLaunched {
						return os.Getpid(), nil
					}
					deployLaunched = true
					deploymentID := operatordeploy.OperatorArgValue(args, "--deployment-id")
					witness.LaunchOperator(t, deployHost, OperatorProcessOptions{
						DeploymentID: deploymentID,
						Capabilities: constants.OperatorCapabilities{constants.OperatorCapabilityData},
					})
					return os.Getpid(), nil
				},
			},
		}
	}

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
			OperatorDeployOverrides: deployOverrides,
		})
		inference = data
	} else {
		dataHost = NewOperatorHost(t)
		inferenceHost = NewOperatorHost(t)
		data = witness.LaunchOperator(t, dataHost, OperatorProcessOptions{
			Capabilities:            constants.OperatorCapabilities{constants.OperatorCapabilityData},
			OperatorDeployOverrides: deployOverrides,
		})
		inference = witness.LaunchOperator(t, inferenceHost, OperatorProcessOptions{
			Capabilities:            constants.OperatorCapabilities{constants.OperatorCapabilityInference, constants.OperatorCapabilityObserver},
			InferenceOllamaEndpoint: provider.Endpoint(),
		})
	}
	fleet, fleetHosts := []*OperatorProcess{data}, []*OperatorHost{dataHost}
	for len(fleet) < opts.DataOperators {
		host := NewOperatorHost(t)
		fleet = append(fleet, witness.LaunchOperator(t, host, OperatorProcessOptions{
			Capabilities:            constants.OperatorCapabilities{constants.OperatorCapabilityData},
			OperatorDeployOverrides: deployOverrides,
		}))
		fleetHosts = append(fleetHosts, host)
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
	for _, member := range fleet[1:] {
		require.NoError(t, member.AwaitStarted(ctx))
		_, err = member.AwaitHeartbeat(ctx)
		require.NoError(t, err)
	}
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
		Data: data, Fleet: fleet, FleetHosts: fleetHosts, Inference: inference, DataHost: dataHost,
		InferenceHost: inferenceHost, DeployHost: deployHost, DeployTargetHostname: opts.DeployTargetHostname,
		Provider: provider, Provenance: provenance, Posture: posture,
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
