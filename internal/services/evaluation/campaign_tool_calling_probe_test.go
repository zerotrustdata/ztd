// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func toolCallingResult(parts ...*operatorv1.InferenceResponsePart) *operatorv1.InferenceResult {
	return &operatorv1.InferenceResult{Parts: parts}
}

func probeToolCall(name, args string) *operatorv1.InferenceResponsePart {
	return &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{Name: name, ArgumentsJson: args}}}
}

func probeText(text string) *operatorv1.InferenceResponsePart {
	return &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Text{Text: text}}
}

func TestToolCallingObservationRequiresOneExactStructuredProbeCall(t *testing.T) {
	valid := probeToolCall("probe_echo", `{"message":"capability-probe-tool"}`)
	tests := []struct {
		name   string
		result *operatorv1.InferenceResult
		want   evalv1.EvaluationVerdictStatus
	}{
		{name: "exact structured arguments", result: toolCallingResult(valid), want: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS},
		{name: "text only", result: toolCallingResult(probeText(`probe_echo {"message":"capability-probe-tool"}`)), want: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
		{name: "polluted name", result: toolCallingResult(probeToolCall("probe_echo_extra", `{"message":"capability-probe-tool"}`)), want: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
		{name: "decoy tool JSON in text", result: toolCallingResult(probeText(`{"name":"probe_echo","arguments":{"message":"capability-probe-tool"}}`)), want: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
		{name: "wrong arguments", result: toolCallingResult(probeToolCall("probe_echo", `{"message":"other"}`)), want: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
		{name: "unknown argument", result: toolCallingResult(probeToolCall("probe_echo", `{"message":"capability-probe-tool","path":"/tmp"}`)), want: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
		{name: "multiple tool calls", result: toolCallingResult(valid, probeToolCall("probe_echo", `{"message":"capability-probe-tool"}`)), want: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
		{name: "probe call plus decoy", result: toolCallingResult(valid, probeText(`{"tool_calls":[{"name":"probe_echo"}]}`)), want: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
		{name: "no result", result: nil, want: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation := toolCallingObservation(tt.result)
			assert.Equal(t, evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_TOOL_CALLING, observation.GetCapability())
			assert.Equal(t, tt.want, observation.GetOutcome())
		})
	}
}

func TestVerifyToolCallingProbeEvidenceRequiresSignedGovernedReceipt(t *testing.T) {
	variant := toolProbeVariant()
	evidence := validToolCallingProbeEvidence(t, variant, "run-probe", "campaign-probe", "assignment-probe")
	require.NoError(t, verifyToolCallingProbeEvidence("run-probe", evidence.GetRequest().GetModelRegistryDigest(), variant, evidence))

	t.Run("missing receipt", func(t *testing.T) {
		mutated := proto.Clone(evidence).(*evalv1.ModelToolCallingProbeEvidence)
		mutated.Response.Receipt = nil
		require.ErrorIs(t, verifyToolCallingProbeEvidence("run-probe", evidence.GetRequest().GetModelRegistryDigest(), variant, mutated), constants.ErrMissingRequiredField)
	})
	t.Run("fake receipt signature", func(t *testing.T) {
		mutated := proto.Clone(evidence).(*evalv1.ModelToolCallingProbeEvidence)
		mutated.Response.Receipt.Signature = "not-a-signature"
		require.Error(t, verifyToolCallingProbeEvidence("run-probe", evidence.GetRequest().GetModelRegistryDigest(), variant, mutated))
	})
	t.Run("reused under another run", func(t *testing.T) {
		require.ErrorIs(t, verifyToolCallingProbeEvidence("another-run", evidence.GetRequest().GetModelRegistryDigest(), variant, evidence), constants.ErrEvidenceScopeMismatch)
	})
	t.Run("wrong variant", func(t *testing.T) {
		other := proto.Clone(variant).(*evalv1.ModelVariant)
		other.VariantId = "other-variant"
		require.ErrorIs(t, verifyToolCallingProbeEvidence("run-probe", evidence.GetRequest().GetModelRegistryDigest(), other, evidence), constants.ErrMissingRequiredField)
	})
	t.Run("malformed probe prompt", func(t *testing.T) {
		mutated := proto.Clone(evidence).(*evalv1.ModelToolCallingProbeEvidence)
		mutated.Request.Messages[0].Parts[0].Part = &operatorv1.InferenceMessagePart_Text{Text: "unrelated request"}
		require.ErrorIs(t, verifyToolCallingProbeEvidence("run-probe", evidence.GetRequest().GetModelRegistryDigest(), variant, mutated), constants.ErrEvaluationEvidenceTraceMismatch)
	})
	t.Run("result content changed after signed receipt", func(t *testing.T) {
		mutated := proto.Clone(evidence).(*evalv1.ModelToolCallingProbeEvidence)
		mutated.Response.Result.Parts = []*operatorv1.InferenceResponsePart{probeText("capability unsupported")}
		require.ErrorIs(t, verifyToolCallingProbeEvidence("run-probe", evidence.GetRequest().GetModelRegistryDigest(), variant, mutated), constants.ErrInferenceResultDigest)
	})
}

type storedToolCallingProbeFixture struct {
	evidence *evalv1.ModelToolCallingProbeEvidence
}

func (s storedToolCallingProbeFixture) LoadToolCallingProbeEvidence(context.Context, string, *compliancev1.ComplianceEvidenceReference) (*evalv1.ModelToolCallingProbeEvidence, error) {
	return proto.Clone(s.evidence).(*evalv1.ModelToolCallingProbeEvidence), nil
}

func TestVerifyFrozenToolCallingProbeRejectsObservationTampering(t *testing.T) {
	variant := toolProbeVariant()
	evidence := validToolCallingProbeEvidence(t, variant, "run-probe", "campaign-probe", "assignment-probe")
	observation := toolCallingObservation(evidence.GetResponse().GetResult())
	run := &evalv1.EvaluationRun{RunId: "run-probe", CampaignBinding: &evalv1.ModelCampaignBinding{ModelRegistryDigest: evidence.GetRequest().GetModelRegistryDigest()}}
	record := &evalv1.ModelToolCallingProbe{VariantId: variant.GetVariantId(), Observation: observation, EvidenceRef: &compliancev1.ComplianceEvidenceReference{ArtifactType: string(complianceevidence.ArtifactTypeEvalExchange), ScenarioId: toolCallingProbeScenario}}
	store := storedToolCallingProbeFixture{evidence: evidence}
	require.NoError(t, verifyFrozenToolCallingProbe(context.Background(), store, run, variant, record))

	tampered := proto.Clone(record).(*evalv1.ModelToolCallingProbe)
	tampered.Observation.Outcome = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	require.ErrorIs(t, verifyFrozenToolCallingProbe(context.Background(), store, run, variant, tampered), constants.ErrEvaluationEvidenceTraceMismatch)
}

func toolProbeVariant() *evalv1.ModelVariant {
	return &evalv1.ModelVariant{VariantId: "variant-probe", ServedModelTag: "probe-model:latest", ModelDigest: strings.Repeat("d", 64)}
}

func validToolCallingProbeEvidence(t *testing.T, variant *evalv1.ModelVariant, runID, campaignID, assignmentID string) *evalv1.ModelToolCallingProbeEvidence {
	t.Helper()
	return signedToolCallingProbeEvidence(t, variant, runID, campaignID, assignmentID, probeToolCall("probe_echo", `{"message":"capability-probe-tool"}`))
}

func signedToolCallingProbeEvidence(t *testing.T, variant *evalv1.ModelVariant, runID, campaignID, assignmentID string, parts ...*operatorv1.InferenceResponsePart) *evalv1.ModelToolCallingProbeEvidence {
	t.Helper()
	const operatorID, sessionID = "operator-probe", "session-probe"
	registry := []*operatorv1.InferenceModelVariant{{Model: variant.GetServedModelTag(), Digest: variant.GetModelDigest()}}
	registryDigest, err := models.ComputeInferenceModelRegistryDigest(campaignID, registry)
	require.NoError(t, err)
	attemptID, transactionID := "tool-calling-probe-"+assignmentID, "tx-tool-calling-probe-"+assignmentID
	probe := InferenceProbeRequest{
		ProviderAttemptID: attemptID, Role: models.InferenceModelRolePrimary, Model: variant.GetServedModelTag(), ModelDigest: variant.GetModelDigest(),
		TargetOperatorSessionID: sessionID, CampaignID: campaignID, ModelRegistryDigest: registryDigest, ModelRegistry: registry,
	}
	configureToolCallingProbe(&probe)
	request, err := BuildInferenceProbeDispatchRequest(probe)
	require.NoError(t, err)
	request.RunId, request.AssignmentId, request.EvaluationAttemptId, request.ScenarioId = runID, assignmentID, attemptID, toolCallingProbeScenario
	result := &operatorv1.InferenceResult{
		ProviderAttemptId: attemptID, RequestedModel: variant.GetServedModelTag(), Model: variant.GetServedModelTag(),
		RequestedModelDigest: variant.GetModelDigest(), ServedModelDigest: variant.GetModelDigest(),
		CampaignId: campaignID, RunId: runID, AssignmentId: assignmentID, EvaluationAttemptId: attemptID,
		ScenarioId: toolCallingProbeScenario, ModelRegistryDigest: registryDigest,
		Parts: parts,
	}
	result.ResultDigest, err = models.ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	signer := oracletest.NewSigner(t)
	ledger := newRuntimeLedger(t, signer, operatorID, sessionID)
	receipt := ledger.executed(transactionID, string(constants.ActionTypeInference))
	receipt.ResultSummary = result.GetResultDigest()
	signer.Resign(t, receipt)
	response := &operatorv1.InferenceDispatchResponse{TransactionId: transactionID, Result: result, Receipt: receipt}
	return &evalv1.ModelToolCallingProbeEvidence{
		VariantId: variant.GetVariantId(), Request: request, Response: response,
		Anchor: signer.Anchor(operatorID), InferenceOperatorId: operatorID, InferenceSessionId: sessionID,
	}
}

type toolCallingGateExecutor struct {
	t          *testing.T
	probeCalls int
	probeText  bool
	probeErr   error
	cellCalls  int
}

func (e *toolCallingGateExecutor) ExecuteAssignment(_ context.Context, req AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	e.cellCalls++
	return &evalv1.EvaluationAssignmentResult{AssignmentId: req.Assignment.GetAssignmentId()}, nil
}

func (e *toolCallingGateExecutor) ProbeToolCalling(_ context.Context, req AssignmentExecutionRequest) (*evalv1.ModelToolCallingProbeEvidence, error) {
	e.probeCalls++
	if e.probeErr != nil {
		return nil, e.probeErr
	}
	variant, err := assignmentToolCallingVariant(req)
	if err != nil {
		return nil, err
	}
	if !e.probeText {
		return validToolCallingProbeEvidence(e.t, variant, req.Assignment.GetRunId(), req.Assignment.GetCampaignId(), req.Assignment.GetAssignmentId()), nil
	}
	return signedToolCallingProbeEvidence(e.t, variant, req.Assignment.GetRunId(), req.Assignment.GetCampaignId(), req.Assignment.GetAssignmentId(), probeText(`probe_echo {"message":"capability-probe-tool"}`)), nil
}

func TestCampaignControllerProbeDispatchFailureStopsBeforeScoring(t *testing.T) {
	ctx := context.Background()
	store := NewStore(newCampaignMemoryFileService())
	executor := &toolCallingGateExecutor{t: t, probeErr: assignmentExecutionError("probe dispatch", constants.ErrInferenceToolsUnsupported)}
	controller := NewCampaignController(store, executor, nil, nil)
	req := testCampaignInitRequest(t)
	for _, scenario := range req.Catalog.GetScenarios() {
		if scenario.GetScenarioId() == "tool-select-file-read" {
			req.Catalog.Scenarios = []*evalv1.EvaluationScenarioDefinition{scenario}
			break
		}
	}
	req.Catalog.ToolCallingProbeRequired = true
	var err error
	req.Catalog.CatalogDigest, err = ComputeScenarioCatalogDigest(req.Catalog)
	require.NoError(t, err)
	_, err = controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)

	result, executed, err := controller.ExecuteNextAssignment(ctx, req.RunID, CampaignExecutionBinding{
		Topology: testOperatorTopology("inf-session", "data-session"), ModelRegistryDigest: req.Inventory.RegistryDigest,
	}, req.ScenarioArtifacts)
	require.ErrorIs(t, err, constants.ErrInferenceToolsUnsupported, "a failed probe is a run error, not evidence of an unsupported capability")
	assert.Nil(t, result)
	assert.False(t, executed)
	assert.Equal(t, 1, executor.probeCalls)
	assert.Zero(t, executor.cellCalls)
	assignments, err := store.ListAssignments(ctx, req.RunID)
	require.NoError(t, err)
	for _, assignment := range assignments {
		assert.NotEqual(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING, assignment.GetLifecycleStatus(), "a failed preflight must remain resumable")
		exists, err := store.AssignmentResultExists(ctx, req.RunID, assignment.GetAssignmentId())
		require.NoError(t, err)
		assert.False(t, exists, "no scored result may be fabricated from a failed eligibility dispatch")
	}
	run, err := store.LoadRun(ctx, req.RunID)
	require.NoError(t, err)
	assert.Empty(t, run.GetToolCallingProbes(), "a transport error supplies no signed probe evidence")
}

func TestCampaignToolCallingGateReusesOnlyRunAndVariantProbe(t *testing.T) {
	ctx := context.Background()
	variant := toolProbeVariant()
	campaignID := "campaign-probe"
	registry := []*operatorv1.InferenceModelVariant{{Model: variant.GetServedModelTag(), Digest: variant.GetModelDigest()}}
	registryDigest, err := models.ComputeInferenceModelRegistryDigest(campaignID, registry)
	require.NoError(t, err)
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	executor := &toolCallingGateExecutor{t: t}
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-gate" })
	newRun := func(runID string) *evalv1.EvaluationRun {
		run := &evalv1.EvaluationRun{
			SchemaVersion: CampaignSchemaVersion, RunId: runID,
			CampaignBinding: &evalv1.ModelCampaignBinding{CampaignId: campaignID, ModelRegistryDigest: registryDigest},
		}
		require.NoError(t, store.SaveRun(ctx, run))
		return run
	}
	toolReq := func(runID, assignmentID string, candidate *evalv1.ModelVariant) AssignmentExecutionRequest {
		assignment := &evalv1.EvaluationAssignment{
			RunId: runID, CampaignId: campaignID, AssignmentId: assignmentID,
			Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{CandidateVariant: candidate, DesignatedRole: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY}},
		}
		return AssignmentExecutionRequest{Assignment: assignment, ScenarioTools: ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolReadFile}}, Binding: CampaignExecutionBinding{ModelRegistryDigest: registryDigest, ToolCallingProbeRequired: true}}
	}

	run := newRun("run-probe-a")
	first := toolReq(run.GetRunId(), "assignment-1", variant)
	legacyCatalog := first
	legacyCatalog.Binding = CampaignExecutionBinding{ModelRegistryDigest: registryDigest}
	result, err := controller.toolCallingGate(ctx, run, legacyCatalog)
	require.NoError(t, err)
	assert.Nil(t, result, "historical catalogs do not activate the probe gate")
	assert.Zero(t, executor.probeCalls)

	result, err = controller.toolCallingGate(ctx, run, first)
	require.NoError(t, err)
	assert.Nil(t, result)
	assert.Equal(t, 1, executor.probeCalls)

	// A second tool-bearing cell for the same variant reuses the run-scoped probe.
	second := toolReq(run.GetRunId(), "assignment-2", variant)
	result, err = controller.toolCallingGate(ctx, run, second)
	require.NoError(t, err)
	assert.Nil(t, result)
	assert.Equal(t, 1, executor.probeCalls)

	// A distinct variant receives its own probe even within the same run.
	otherVariant := proto.Clone(variant).(*evalv1.ModelVariant)
	otherVariant.VariantId = "variant-probe-other"
	third := toolReq(run.GetRunId(), "assignment-3", otherVariant)
	result, err = controller.toolCallingGate(ctx, run, third)
	require.NoError(t, err)
	assert.Nil(t, result)
	assert.Equal(t, 2, executor.probeCalls)
	assert.Len(t, run.GetToolCallingProbes(), 2)

	// A new run does not inherit the prior run's probe, even for the same variant.
	otherRun := newRun("run-probe-b")
	fourth := toolReq(otherRun.GetRunId(), "assignment-4", variant)
	result, err = controller.toolCallingGate(ctx, otherRun, fourth)
	require.NoError(t, err)
	assert.Nil(t, result)
	assert.Equal(t, 3, executor.probeCalls)
}

func TestCampaignToolCallingGateExcludesUnsupportedToolCellsButSkipsAnswerOnly(t *testing.T) {
	ctx := context.Background()
	variant := toolProbeVariant()
	campaignID := "campaign-probe"
	registry := []*operatorv1.InferenceModelVariant{{Model: variant.GetServedModelTag(), Digest: variant.GetModelDigest()}}
	registryDigest, err := models.ComputeInferenceModelRegistryDigest(campaignID, registry)
	require.NoError(t, err)
	store := NewStore(newCampaignMemoryFileService())
	executor := &toolCallingGateExecutor{t: t, probeText: true}
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, nil)
	run := &evalv1.EvaluationRun{SchemaVersion: CampaignSchemaVersion, RunId: "run-probe-exclusion", CampaignBinding: &evalv1.ModelCampaignBinding{CampaignId: campaignID, ModelRegistryDigest: registryDigest}}
	require.NoError(t, store.SaveRun(ctx, run))
	assignment := &evalv1.EvaluationAssignment{RunId: run.GetRunId(), CampaignId: campaignID, AssignmentId: "tool-cell", Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{CandidateVariant: variant, DesignatedRole: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY}}}
	toolReq := AssignmentExecutionRequest{Assignment: assignment, ScenarioTools: ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolReadFile}}, Binding: CampaignExecutionBinding{ModelRegistryDigest: registryDigest, ToolCallingProbeRequired: true}}
	excluded, err := controller.toolCallingGate(ctx, run, toolReq)
	require.NoError(t, err)
	require.NotNil(t, excluded)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, excluded.GetLifecycleStatus())
	assert.Equal(t, toolCallingProbeFailure, excluded.GetFailureReason())
	assert.Len(t, excluded.GetEvidenceRefs(), 1)

	answerOnly := toolReq
	answerOnly.Assignment = proto.Clone(assignment).(*evalv1.EvaluationAssignment)
	answerOnly.Assignment.AssignmentId = "answer-only-cell"
	answerOnly.ScenarioTools = ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER}
	eligible, err := controller.toolCallingGate(ctx, run, answerOnly)
	require.NoError(t, err)
	assert.Nil(t, eligible, "answer-only cells remain eligible without a tool-call probe")
	assert.Equal(t, 1, executor.probeCalls)
}
