// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type testEvidenceLaneClient struct {
	ledger   *evidenceLedger
	requests []client.DispatchCommandRequest
}

func (c *testEvidenceLaneClient) DispatchCommand(_ context.Context, _ client.Persona, request client.DispatchCommandRequest) (*client.GovernedOutcome, error) {
	c.requests = append(c.requests, request)
	txID := fmt.Sprintf("tx-dispatch-%d", len(c.requests))
	receipt := c.ledger.executed(txID, request.ActionType)
	var result proto.Message
	switch request.ActionType {
	case string(constants.ActionTypeExecuteBash):
		result = &operatorv1.CommandResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, Stdout: "HEALTHY\n"}
	case string(constants.ActionTypeFileEdit):
		result = &operatorv1.FileEditResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, FilePath: request.TargetResource, Operation: string(constants.FileOperationWrite)}
	default:
		return nil, fmt.Errorf("test evidence client: unscripted action %s", request.ActionType)
	}
	payload, err := proto.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, Receipt: receipt},
		Dispatch:    &operatorv1.GovernedDispatchResult{TransactionId: txID, Receipt: receipt, ResultPayload: payload},
	}, nil
}

type testEvidenceInferenceFake struct {
	ledger         *evidenceLedger
	turns          [][]*operatorv1.InferenceResponsePart
	requests       []*operatorv1.InferenceDispatchRequest
	failTurn       int
	foreignTurn    int
	digestMismatch bool
}

func (f *testEvidenceInferenceFake) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	f.requests = append(f.requests, req)
	turnNum := len(f.requests)
	turnIndex := (turnNum - 1) % len(f.turns)
	if turnNum == f.foreignTurn {
		f.ledger.executed(fmt.Sprintf("tx-foreign-%d", turnNum), string(constants.ActionTypeExecuteBash))
	}
	txID := fmt.Sprintf("tx-inference-%d", turnNum)
	if turnNum == f.failTurn {
		return nil, governance.WithFailedReceipt(f.ledger.inferenceFailed(txID), errors.New("test evidence inference: provider unreachable"))
	}
	result := &operatorv1.InferenceResult{
		ProviderAttemptId:    req.GetProviderAttemptId(),
		RequestedModel:       req.GetModel(),
		RequestedModelDigest: req.GetModelDigest(),
		ServedModelDigest:    req.GetModelDigest(),
		Model:                req.GetModel(),
		CampaignId:           req.GetCampaignId(),
		RunId:                req.GetRunId(),
		AssignmentId:         req.GetAssignmentId(),
		EvaluationAttemptId:  req.GetEvaluationAttemptId(),
		ScenarioId:           req.GetScenarioId(),
		ModelRegistryDigest:  req.GetModelRegistryDigest(),
		FinishReason:         "stop",
		Parts:                f.turns[turnIndex],
		LoadState:            operatorv1.InferenceLoadState_INFERENCE_LOAD_STATE_UNSPECIFIED,
		RetryClassification:  operatorv1.InferenceRetryClassification_INFERENCE_RETRY_CLASSIFICATION_UNSPECIFIED,
	}
	resultDigest, err := models.ComputeInferenceResultDigest(result)
	if err != nil {
		return nil, err
	}
	result.ResultDigest = resultDigest
	if f.digestMismatch {
		result.ResultDigest = flipFirstHexDigit(resultDigest)
	}
	return &operatorv1.InferenceDispatchResponse{
		TransactionId: txID,
		Result:        result,
		Receipt:       f.ledger.inferenceWithResultDigest(txID, resultDigest),
	}, nil
}

type testEvidenceSentinelFake struct {
	ledger *evidenceLedger
	reads  int
}

func (s *testEvidenceSentinelFake) Read(_ context.Context, _ cases.Executor, _ string) (*operatorv1.ActionReceipt, error) {
	s.reads++
	return s.ledger.executed(fmt.Sprintf("tx-sentinel-%d", s.reads), string(constants.ActionTypeFsRead)), nil
}

type testEvidenceEnv struct {
	rt              *sealedRuntime
	store           *Store
	dataLedger      *evidenceLedger
	inferenceLedger *evidenceLedger
	laneClient      *testEvidenceLaneClient
	inferenceFake   *testEvidenceInferenceFake
	sentinelFake    *testEvidenceSentinelFake
	controller      *CampaignController
	req             CampaignInitRequest
	binding         CampaignExecutionBinding
	scheduledCount  int
	clock           func() time.Time
}

func setupTestEvidenceEnv(t *testing.T, shared bool, runSuffix string, singleRole bool) *testEvidenceEnv {
	t.Helper()
	rt := newSealedRuntime(t)
	store := NewStore(rt.files)

	dataSigner := oracletest.NewSigner(t)
	dataLedger := newEvidenceLedger(t, dataSigner)
	inferenceLedger := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
	if shared {
		inferenceLedger = dataLedger
	}

	laneClient := &testEvidenceLaneClient{ledger: dataLedger}
	inferenceFake := &testEvidenceInferenceFake{ledger: inferenceLedger, turns: runCommandTurns(agentTestCommand)}
	sentinelFake := &testEvidenceSentinelFake{ledger: dataLedger}

	clock := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	seq := 0
	newID := func(prefix string) string {
		seq++
		return fmt.Sprintf("%s-%d", prefix, seq)
	}

	commandLane := NewCommandLane(laneClient, client.Persona{CLISessionID: "cli-session-1"})
	agentExecutor, err := NewCampaignAgentExecutor(inferenceFake, commandLane, sentinelFake, store, clock, newID)
	require.NoError(t, err)

	controller := NewCampaignController(store, agentExecutor, clock, newID)

	req := testCampaignInitRequest(t)
	if runSuffix != "" {
		req.RunID = req.RunID + "-" + runSuffix
	}

	var originalScenario *evalv1.EvaluationScenarioDefinition
	for _, s := range req.Catalog.GetScenarios() {
		if s.GetScenarioId() == agentTestScenarioID {
			originalScenario = s
			break
		}
	}
	require.NotNil(t, originalScenario, "scenario %s must exist in catalog", agentTestScenarioID)

	targetScenario := proto.Clone(originalScenario).(*evalv1.EvaluationScenarioDefinition)
	if singleRole {
		targetScenario.EligibleRoles = []evalv1.ModelCampaignRole{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY}
	}

	truncatedCatalog := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: req.Catalog.GetSchemaVersion(),
		CatalogRef:    req.Catalog.GetCatalogRef(),
		Scenarios:     []*evalv1.EvaluationScenarioDefinition{targetScenario},
	}
	catalogDigest, err := ComputeScenarioCatalogDigest(truncatedCatalog)
	require.NoError(t, err)
	truncatedCatalog.CatalogDigest = catalogDigest
	req.Catalog = truncatedCatalog

	_, err = controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)

	scheduledCount, err := controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)
	if singleRole {
		require.Equal(t, 1, scheduledCount)
	} else {
		require.Equal(t, 2, scheduledCount)
	}

	binding := CampaignExecutionBinding{
		Topology: CampaignOperatorTopology{
			Inference:            inferenceLedger.campaignRuntime(),
			Data:                 dataLedger.campaignRuntime(),
			DataWorkingDirectory: agentTestWorkingDirectory,
		},
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       req.Inventory.ToModelRegistryFreeze().Variants,
	}

	return &testEvidenceEnv{
		rt:              rt,
		store:           store,
		dataLedger:      dataLedger,
		inferenceLedger: inferenceLedger,
		laneClient:      laneClient,
		inferenceFake:   inferenceFake,
		sentinelFake:    sentinelFake,
		controller:      controller,
		req:             req,
		binding:         binding,
		scheduledCount:  scheduledCount,
		clock:           clock,
	}
}

func TestAgentEvidenceIntegration_SharedRuntimeEmbeddedOperator(t *testing.T) {
	ctx := context.Background()
	env := setupTestEvidenceEnv(t, true, "shared", false)

	for i := 0; i < env.scheduledCount; i++ {
		res, ok, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, env.binding, env.req.ScenarioArtifacts)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus())

		trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
		require.NoError(t, err)
		assert.Equal(t, agentTraceSchemaVersion, trace.SchemaVersion)
		require.NotNil(t, trace.Input, "a v4 trace records the exact model input")
		assert.NotEmpty(t, trace.Input.Messages)
		assert.True(t, trace.SharedRuntime(), "shared runtime trace must report SharedRuntime() == true")
		require.NotEmpty(t, trace.ModelCalls)
		for _, mc := range trace.ModelCalls {
			assert.NotNil(t, mc.Receipt, "every model call must carry a receipt")
		}
	}

	verifier := NewCampaignRunVerifier(env.clock)
	report, err := verifier.VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus())

	exportReport, err := NewCampaignExporter(env.clock).ExportRun(ctx, env.store, env.rt.files, env.req.RunID, constants.EvaluationCampaignExportDirname)
	require.NoError(t, err)
	require.NotEmpty(t, exportReport.Files)
}

func TestAgentEvidenceIntegration_DistinctRuntimes(t *testing.T) {
	ctx := context.Background()
	env := setupTestEvidenceEnv(t, false, "distinct", false)

	for i := 0; i < env.scheduledCount; i++ {
		res, ok, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, env.binding, env.req.ScenarioArtifacts)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus())

		trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
		require.NoError(t, err)
		assert.Equal(t, agentTraceSchemaVersion, trace.SchemaVersion)
		require.NotNil(t, trace.Input, "a v4 trace records the exact model input")
		assert.NotEmpty(t, trace.Input.Messages)
		assert.False(t, trace.SharedRuntime(), "distinct runtimes must report SharedRuntime() == false")
		require.NotEmpty(t, trace.ModelCalls)
		for _, mc := range trace.ModelCalls {
			assert.NotNil(t, mc.Receipt)
		}
	}

	verifier := NewCampaignRunVerifier(env.clock)
	report, err := verifier.VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus())

	exportReport, err := NewCampaignExporter(env.clock).ExportRun(ctx, env.store, env.rt.files, env.req.RunID, constants.EvaluationCampaignExportDirname)
	require.NoError(t, err)
	require.NotEmpty(t, exportReport.Files)
}

func TestAgentEvidenceIntegration_ProviderFailureAfterExecution(t *testing.T) {
	ctx := context.Background()
	env := setupTestEvidenceEnv(t, true, "provider-fail", true)
	env.inferenceFake.failTurn = 1

	res, ok, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, env.binding, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, res.GetLifecycleStatus())
	assert.Empty(t, res.GetInvalidEvidenceDetail())

	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	assert.NotEmpty(t, trace.ProviderFailure)
	require.NotEmpty(t, trace.ModelCalls)
	require.NotNil(t, trace.ModelCalls[0].Receipt)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, trace.ModelCalls[0].Receipt.Status)

	exportReport, err := NewCampaignExporter(env.clock).ExportRun(ctx, env.store, env.rt.files, env.req.RunID, constants.EvaluationCampaignExportDirname)
	require.NoError(t, err)
	require.NotEmpty(t, exportReport.Files)

	verifier := NewCampaignRunVerifier(env.clock)
	report, err := verifier.VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus())
}

func TestAgentEvidenceIntegration_ForeignUnclaimedExecutionSharedRuntime(t *testing.T) {
	ctx := context.Background()
	env := setupTestEvidenceEnv(t, true, "foreign", true)
	env.inferenceFake.foreignTurn = 1

	res, ok, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, env.binding, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, res.GetLifecycleStatus())
	assert.Contains(t, res.GetInvalidEvidenceDetail(), "unclaimed execution")
	assert.Empty(t, res.GetDeterministicGrades())
	assert.Empty(t, res.GetSemanticGrades())
}

func TestAgentEvidenceIntegration_DigestMismatch(t *testing.T) {
	ctx := context.Background()
	env := setupTestEvidenceEnv(t, true, "digest-mismatch", true)
	env.inferenceFake.digestMismatch = true

	res, ok, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, env.binding, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, res.GetLifecycleStatus())
	assert.NotEmpty(t, res.GetInvalidEvidenceDetail())

	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	assert.NotEmpty(t, trace.ProviderFailure)
	assert.Equal(t, string(ExecutionFailureInternal), trace.ProviderFailure)
	require.NotEmpty(t, trace.ModelCalls)
	assert.NotNil(t, trace.ModelCalls[0].Receipt)

	err = VerifyAgentTraceEvidence(trace)
	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid, "completed receipt without its signed result fails closed")
}

func TestAgentEvidenceIntegration_AnchorFailClosed(t *testing.T) {
	ctx := context.Background()
	env := setupTestEvidenceEnv(t, true, "anchor-fail", true)

	badBinding := env.binding
	badBinding.Topology.Inference.Anchor = &evalv1.ConformanceAnchor{RuntimeId: "wrong-operator"}

	_, _, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, badBinding, env.req.ScenarioArtifacts)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrConformanceAnchorMissing)
	assert.Equal(t, 0, len(env.laneClient.requests), "nothing is dispatched when anchor validation fails")
}
