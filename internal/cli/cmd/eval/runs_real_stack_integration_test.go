// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	"github.com/zerotrustdata/ztd/internal/testutil"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	harnessconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	"github.com/zerotrustdata/ztd/internal/uuid"
	"github.com/zerotrustdata/ztd/protocol"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

type runTribunalMemberSigner struct {
	keys map[string]ed25519.PrivateKey
}

func (s *runTribunalMemberSigner) HasKey(appID string) bool {
	_, ok := s.keys[appID]
	return ok
}

func (s *runTribunalMemberSigner) Sign(appID string, payload []byte) ([]byte, error) {
	key, ok := s.keys[appID]
	if !ok {
		return nil, constants.ErrConsensusMemberKeyUnavailable
	}
	return ed25519.Sign(key, payload), nil
}

func bindRunTribunalConsensus(t *testing.T, gateway *fixtures.GatewayFixture, identity *fixtures.ClientIdentity) {
	t.Helper()
	ctx := t.Context()
	gatewayService := gateway.Service
	store := gatewayService.GetConsensusStore()
	fixtureBinding, err := store.GetBoundConsensusApp(ctx)
	require.NoError(t, err)
	require.NotNil(t, fixtureBinding)
	require.NoError(t, store.ClearConsensusBinding(ctx, ""))

	signer := &runTribunalMemberSigner{keys: make(map[string]ed25519.PrivateKey, len(tribunal.AllMembers))}
	memberAppIDs := make([]string, 0, len(tribunal.AllMembers))
	for _, member := range tribunal.AllMembers {
		appID := tribunal.ConsensusMemberAppID(member)
		memberAppIDs = append(memberAppIDs, appID)
		publicKey, privateKey, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		signer.keys[appID] = privateKey
		require.NoError(t, gatewayService.GetSignerStore().AddTrustedSigner(ctx, models.TrustedSigner{
			ID: appID, PublicKey: hex.EncodeToString(publicKey), AddedAt: time.Now().UTC(), Enabled: true,
		}))
		spiffeID := protocol.NewWorkloadIdentity().AppSPIFFEID(appID)
		policy, err := json.Marshal(models.AppPolicy{AppID: spiffeID, OwnerUserID: identity.UserID})
		require.NoError(t, err)
		require.NoError(t, gatewayService.GetDocStore().DocSet(ctx, marshaler.CollectionName(constants.CollectionAppPolicies), spiffeID, policy))
	}
	gatewayService.SetConsensusMemberSigner(signer)
	require.NoError(t, store.AddConsensus(ctx, models.ConsensusPolicy{
		ID:              string(constants.DocIDConsensusBinding),
		Provider:        string(constants.ConsensusProviderTribunal),
		EndpointURL:     fixtureBinding.EndpointURL,
		MemberAppIDs:    memberAppIDs,
		Quorum:          3,
		RequireDistinct: true,
		ModelTag:        testQwenVariant().GetServedModelTag(),
		ModelDigest:     testQwenVariant().GetModelDigest(),
		Enabled:         true,
	}))
}

// realStackRun is a real-stack eval ready to run: the stack is up, the CLI
// runtime is installed against it, and the campaign is created on it.
type realStackRun struct {
	ctx     context.Context
	root    string
	stack   *fixtures.EvalStack
	deps    nativeEvalDeps
	cfg     *config.Config
	fileSvc fs.RuntimeFileService
}

// newRealStackRun starts the real stack under posture and creates campaignID on
// it: one answer-only scenario repeated repetitions times, each assignment one
// real model turn that the scripted provider answers READY. runControl is the
// run's process boundary.
func newRealStackRun(t *testing.T, posture constants.Posture, campaignID string, repetitions uint32, runControl runControlDeps) *realStackRun {
	t.Helper()
	// The version context is what the root command attaches; run start reads the
	// platform release from it.
	ctx, cancel := context.WithTimeout(testVersionContext(), 3*time.Minute)
	t.Cleanup(cancel)
	replies := make([]fixtures.OllamaReply, repetitions)
	for i := range replies {
		replies[i] = fixtures.OllamaReply{Content: "READY"}
	}
	run := newRealStackEnvironment(t, fixtures.EvalStackOptions{Posture: posture, Replies: replies}, runControl)
	deps, stack, evidenceFiles := run.deps, run.stack, run.fileSvc
	dataWorkload, err := stack.Data.Workload()
	require.NoError(t, err)

	// Keep the live run to the answer-only Lite scenario: each assignment is one
	// real model turn with no synthetic tools or event receipts.
	suite := evaluation.DefaultScenarioSuite()
	suite.ID = "real-stack-suite"
	suite.Version = evaluation.SuiteSchemaVersion
	for _, scenario := range evaluation.DefaultScenarioSuite().Scenarios {
		if scenario.ScenarioID == "instruction-exact-format" {
			suite.Scenarios = []evaluation.SuiteScenario{scenario}
			break
		}
	}
	require.Len(t, suite.Scenarios, 1)
	require.NoError(t, evaluation.NewStore(evidenceFiles).CreateSuite(ctx, suite))
	_, err = createCampaign(ctx, deps, evidenceFiles, campaignCreateSpec{
		Platform:    testPlatform,
		CampaignID:  campaignID,
		Variants:    []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions: repetitions,
		SuiteID:     suite.ID,
		Investigation: &evalv1.EvaluationInvestigation{
			Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
			InvestigationId: "real-stack-investigation",
			DataOperatorId:  dataWorkload.OperatorID,
		},
	})
	require.NoError(t, err)
	return run
}

// newRealStackEnvironment retains every production factory; only Ollama replies
// and the process control boundary are supplied by the test.
func newRealStackEnvironment(t *testing.T, opts fixtures.EvalStackOptions, runControl runControlDeps) *realStackRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(testVersionContext(), 3*time.Minute)
	t.Cleanup(cancel)
	root := testutil.TempDir(t)
	keyring := keystoretest.NewMemoryKeyring()
	cfgLoader := func(projectRoot string) (*config.Config, error) {
		cfg, err := config.Load(projectRoot)
		if err != nil {
			return nil, err
		}
		cfg.Keyring = keyring
		return cfg, nil
	}
	deps := nativeEvalDeps{
		configLoader:      cfgLoader,
		fileSvcFactory:    shared.EvidenceFileSvcFactory(keystore.Options{Keyring: keyring}),
		createRuntimeTree: func(ctx context.Context, fileSvc fs.RuntimeFileService) error { return fileSvc.CreateRuntimeTree(ctx) },
		clientFactory:     func(cfg harnessconfig.Config) (*harnessclient.Client, error) { return harnessclient.New(cfg) },
		authLoader:        auth.LoadClientAuthContext,
		bindClientFactory: defaultEvalBindClient,
		campaignPublicationFactory: func(cmd *cobra.Command, fileSvc fs.RuntimeFileService, cfg *config.Config) (campaignVerificationPublication, error) {
			return NewCampaignPublicationCoordinator(cmd, fileSvc, cfg)
		},
		agentExecutorFactory: newProductionCampaignAgentExecutor,
		appEnroller:          enrollEvalAppIdentity,
		runControl:           runControl,
		conformance:          defaultConformanceDeps(),
		now:                  time.Now,
		newID:                uuid.NewString,
	}

	stack := fixtures.NewEvalStack(t, opts)

	fileSvc, err := fs.NewRuntimeFileService(root, slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(ctx))
	keyStore, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, keyStore.Initialize())
	require.NoError(t, keyStore.InitVault())
	cfg, err := cfgLoader(root)
	require.NoError(t, err)
	stack.Identity.InstallCLIRuntime(t, stack.Gateway, fileSvc, cfg)
	writeTestFrozenInventory(t, root, testQwenVariant())
	evidenceFiles, err := deps.fileSvcFactory(root, slog.Default())
	require.NoError(t, err)
	return &realStackRun{ctx: ctx, root: root, stack: stack, deps: deps, cfg: cfg, fileSvc: evidenceFiles}
}

// newGatewayRunEnv is the CLI test environment backed by the real eval stack.
func newGatewayRunEnv(t *testing.T) *runEnv {
	t.Helper()
	env, _ := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	return env
}

func newGatewayRunEnvWithPosture(t *testing.T, posture constants.Posture) (*runEnv, *realStackRun) {
	t.Helper()
	return newGatewayRunEnvWithOptions(t, fixtures.EvalStackOptions{Posture: posture})
}

func newPublicationRunEnv(t *testing.T) *runEnv {
	t.Helper()
	env, run := newGatewayRunEnvWithOptions(t, fixtures.EvalStackOptions{Posture: constants.PostureDoctrine, EnablePublicSpectator: true})
	previous := gwremote.PublicMirrorOrigin
	gwremote.PublicMirrorOrigin = run.stack.Gateway.PublicMirrorOrigin
	t.Cleanup(func() { gwremote.PublicMirrorOrigin = previous })
	return env
}

func newGatewayRunEnvWithOptions(t *testing.T, opts fixtures.EvalStackOptions) (*runEnv, *realStackRun) {
	t.Helper()
	control := newFakeProcessControl()
	replies := make([]fixtures.OllamaReply, 16)
	for i := range replies {
		replies[i] = fixtures.OllamaReply{Content: "READY"}
	}
	opts.Replies = replies
	run := newRealStackEnvironment(t, opts, testRunControl(control))
	cmd := &cobra.Command{}
	cmd.SetContext(run.ctx)
	cmd.Flags().String("project-root", run.root, "")
	data, err := run.stack.Data.Workload()
	require.NoError(t, err)
	inference, err := run.stack.Inference.Workload()
	require.NoError(t, err)
	authContext, err := run.deps.authLoader(run.fileSvc, run.cfg)
	require.NoError(t, err)
	_, err = evalBindSessions(cmd, run.cfg, run.fileSvc, defaultEvalBindClient(run.cfg), authContext, []string{data.OperatorSessionID})
	require.NoError(t, err)
	return &runEnv{root: run.root, deps: run.deps, cmd: cmd, control: control,
		dataOperatorID: data.OperatorID, dataSession: data.OperatorSessionID, inferenceSession: inference.OperatorSessionID}, run
}

func TestRunsExecute_RealStack(t *testing.T) {
	tests := []struct {
		name    string
		posture constants.Posture
	}{
		{name: "doctrine", posture: constants.PostureDoctrine},
		{name: "ratify", posture: constants.PostureRatify},
		{name: "notary", posture: constants.PostureNotary},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := newRealStackRun(t, tt.posture, "real-stack-campaign", 1, defaultRunControlDeps())

			cmd := evalCmdWithConfig(run.deps)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"runs", "start", "real-stack-campaign", "--daemon=false", "--publish=false", "--no-backup", "--project-root", run.root})
			cmdCtx, cancelCmd := context.WithTimeout(testVersionContext(), 2*time.Minute)
			t.Cleanup(cancelCmd)
			require.NoError(t, cmd.ExecuteContext(cmdCtx), out.String())
			assert.Contains(t, out.String(), "Executed 1 assignment")
			assert.Equal(t, 1, run.stack.Provider.ChatCount())

			store := evaluation.NewStore(run.fileSvc)
			runIDs, err := store.ListRunIDs(run.ctx)
			require.NoError(t, err)
			require.Len(t, runIDs, 1)
			assignments, err := store.ListAssignments(run.ctx, runIDs[0])
			require.NoError(t, err)
			require.Len(t, assignments, 1)
			assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, assignments[0].GetLifecycleStatus())
			trace, err := store.LoadAssignmentAgentTrace(run.ctx, runIDs[0], assignments[0].GetAssignmentId())
			require.NoError(t, err)
			require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
			require.Len(t, trace.ModelCalls, 1)
			assert.Equal(t, "READY", trace.FinalOutput)
			require.NotNil(t, trace.ModelCalls[0].EventReceipt)
			eventReceipt := trace.ModelCalls[0].EventReceipt.EventReceipt
			workload, err := run.stack.Inference.Workload()
			require.NoError(t, err)
			assert.Equal(t, workload.OperatorID, trace.Inference.OperatorID)
			assert.Equal(t, workload.OperatorSessionID, trace.Inference.SessionID)
			dataWorkload, err := run.stack.Data.Workload()
			require.NoError(t, err)
			assert.Equal(t, dataWorkload.OperatorID, trace.Data.OperatorID)
			assert.NotEqual(t, trace.Data.OperatorID, trace.Inference.OperatorID)

			operatorFiles, err := fs.NewRuntimeFileService(run.stack.InferenceHost.WorkingDir, slog.Default())
			require.NoError(t, err)
			local, err := storage.ReadLocalEventReceipt(operatorFiles, slog.Default(), keystore.Options{Keyring: run.stack.InferenceHost.Keyring}, eventReceipt.GetTransactionId())
			require.NoError(t, err)
			require.NotNil(t, local)
			assert.True(t, proto.Equal(eventReceipt, local.EventReceipt), "the imported trace event receipt must match the Inference Operator's authoritative local record")
		})
	}
}

func TestReleaseProviderModels_RunsNoTribunalVoteRound(t *testing.T) {
	run := newRealStackEnvironment(t, fixtures.EvalStackOptions{
		Posture: constants.PostureConsensus, BindConsensus: bindRunTribunalConsensus,
		Replies: []fixtures.OllamaReply{{Content: "ready"}},
	}, runControlDeps{})

	cmd := &cobra.Command{}
	cmd.SetContext(run.ctx)
	cmd.Flags().String("project-root", run.root, "")
	authContext, err := run.deps.authLoader(run.fileSvc, run.cfg)
	require.NoError(t, err)
	inferenceWorkload, err := run.stack.Inference.Workload()
	require.NoError(t, err)

	// Warm the provider through a governed no-tool inference so the typed
	// residency read finds one resident model for the production release path.
	appClient, err := inferenceEvalAppClient(run.ctx, io.Discard, run.deps, run.fileSvc, run.cfg, authContext)
	require.NoError(t, err)
	inferenceResult, _, err := appClient.DispatchInference(run.ctx, &operatorv1.InferenceDispatchRequest{
		RequestSchemaVersion: constants.InferenceRequestSchemaVersion,
		Role:                 operatorv1.ModelRole_MODEL_ROLE_PRIMARY,
		Model:                testQwenVariant().GetServedModelTag(),
		Messages: []*operatorv1.InferenceMessage{{
			Role:  operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
			Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "warm provider"}}},
		}},
		ProviderAttemptId:       "tribunal-release-warmup-attempt",
		TargetOperatorSessionId: inferenceWorkload.OperatorSessionID,
		ActingAppId:             protocol.NewWorkloadIdentity().AppSPIFFEID(constants.EvaluationAppName),
	})
	require.NoError(t, err)
	require.NotNil(t, inferenceResult)
	require.NotNil(t, inferenceResult.GetEventReceipt())
	assert.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, inferenceResult.GetEventReceipt().GetL2Status())
	assert.Equal(t, 1, run.stack.Provider.ChatCount(), "warm-up inference is not voted on")

	// Neither the strict-run preflight residency read nor the typed release is
	// a state change, so under consensus neither runs a Tribunal vote round.
	require.NoError(t, releaseProviderModels(cmd, run.deps, runExecuteOptions{RunID: "tribunal-release"}, run.cfg, authContext,
		inferenceWorkload.OperatorSessionID, run.stack.Provider.Endpoint(), "resident", nil))
	assert.Equal(t, 1, run.stack.Provider.ChatCount(), "residency and release must not add Tribunal vote inferences")

	provider, err := inference.NewOllamaBackend(run.stack.Provider.Endpoint(), slog.Default())
	require.NoError(t, err)
	residency, err := provider.ReadResidency(run.ctx)
	require.NoError(t, err)
	assert.Empty(t, residency.Models, "release verifies absence inside its own execution")

	eventReceipts, err := run.stack.Gateway.Service.GetAuditStore().ListEventReceipts(models.AuditScope{
		OperatorSessionID: inferenceWorkload.OperatorSessionID,
		InvestigationID:   "ollama-model-residency",
	}, 10, 0)
	require.NoError(t, err)
	assert.Len(t, eventReceipts, 1, "the caller must not dispatch a second governed residency query after release")

	releaseEventReceipts, err := run.stack.Gateway.Service.GetAuditStore().ListEventReceipts(models.AuditScope{
		OperatorSessionID: inferenceWorkload.OperatorSessionID,
		InvestigationID:   "ollama-model-release",
	}, 10, 0)
	require.NoError(t, err)
	require.Len(t, releaseEventReceipts, 1)
	assert.Equal(t, constants.ActionTypeOllamaModelRelease, releaseEventReceipts[0].ActionType)
	assert.NotEmpty(t, releaseEventReceipts[0].TransactionID)
	operatorFiles, err := fs.NewRuntimeFileService(run.stack.InferenceHost.WorkingDir, testutil.NewTestLogger())
	require.NoError(t, err)
	local, err := storage.ReadLocalEventReceipt(operatorFiles, testutil.NewTestLogger(), keystore.Options{Keyring: run.stack.InferenceHost.Keyring}, releaseEventReceipts[0].TransactionID)
	require.NoError(t, err)
	require.NotNil(t, local)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, local.EventReceipt.GetStatus())
	assert.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, local.EventReceipt.GetL2Status())
}
