// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/investigation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const deniedObservationError = "SECURITY VIOLATION: GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET"

// deniedProbeObservation is the harness probe L2 refused, as the executor
// records it: a signed rejection receipt and no request envelope, because the
// Gateway attaches the admitted envelope only to a dispatch that executed.
func deniedProbeObservation(t *testing.T, receipt *operatorv1.EventReceipt) AgentScenarioObservation {
	t.Helper()
	result, err := agentToolFailureJSON(agentToolFailure{ErrorType: agentToolErrorSecurity, Error: deniedObservationError})
	require.NoError(t, err)
	return AgentScenarioObservation{Kind: scenarioObservationProbe, Call: AgentToolCall{
		CallID: "scenario-observation-1", ToolName: constants.MCPToolRunShellCommand,
		ArgumentsJSON: (models.RunShellCommandRequest{Command: "sh", Args: []string{"./check.sh"}, WorkingDir: "/work/case"}).CanonicalJSON(),
		ActionType:    string(constants.ActionTypeMcpCall), TransactionID: receipt.GetTransactionId(), PolicyDecision: agentPolicyDecisionDeny,
		ErrorType: agentToolErrorSecurity, Error: deniedObservationError, ResultJSON: result, Receipt: &AgentTraceReceipt{EventReceipt: receipt},
	}}
}

func TestScenarioObservationEvidence_GovernanceDenialVerifiesFromItsSignedReceipt(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	before := ledger.executed("before", string(constants.ActionTypeFsRead))
	model := ledger.inference("agent-turn")
	denied := ledger.rejected("probe-tx", string(constants.ActionTypeMcpCall))
	trace.ModelCalls = []AgentModelCall{completedModelCall("agent-turn", model)}
	trace.Observations = []AgentScenarioObservation{deniedProbeObservation(t, denied)}
	trace.Bracket = bracketOf(before, ledger.executed("after", string(constants.ActionTypeFsRead)))

	require.NoError(t, VerifyAgentTraceEvidence(trace))
}

func TestScenarioObservationEvidence_GovernanceDenialRejectsAClaimItsReceiptDoesNotBack(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	denied := ledger.rejected("probe-tx", string(constants.ActionTypeMcpCall))
	tests := []struct {
		name   string
		mutate func(*AgentToolCall)
	}{
		{name: "claims success", mutate: func(call *AgentToolCall) { call.Success = true }},
		{name: "not recorded as a policy denial", mutate: func(call *AgentToolCall) { call.PolicyDecision = agentPolicyDecisionAllow }},
		{name: "not a security failure", mutate: func(call *AgentToolCall) { call.ErrorType = agentToolErrorExecution }},
		{name: "text outside the denial form", mutate: func(call *AgentToolCall) { call.Error = "tool execution failed" }},
		{name: "result differs from the denial", mutate: func(call *AgentToolCall) { call.ResultJSON = `{"exit_code":0}` }},
		{name: "action differs from the receipt", mutate: func(call *AgentToolCall) { call.ActionType = string(constants.ActionTypeFileEdit) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation := deniedProbeObservation(t, denied)
			tt.mutate(&observation.Call)
			require.ErrorIs(t, verifyScenarioObservation(trace, observation), constants.ErrEvaluationEvidenceTraceMismatch)
		})
	}
}

// The relaxed rule is the signed denial's alone: an executed receipt still has
// to carry the request envelope that binds its arguments.
func TestScenarioObservationEvidence_ExecutedReceiptStillRequiresItsRequestEnvelope(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	observation := deniedProbeObservation(t, ledger.executed("probe-tx", string(constants.ActionTypeMcpCall)))
	require.ErrorIs(t, verifyScenarioObservation(trace, observation), constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestVerifyV7ModelCallIdentity_BindsEachCallToItsFormationSlot(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	trace := &AgentTrace{SchemaVersion: agentTraceSchemaVersion, CampaignID: "campaign-1", RunID: "run-1", AssignmentID: "assignment-1",
		EvaluationAttemptID: "attempt-1", ScenarioID: "scenario-1", DesignatedRole: "primary", ServedModelTag: "primary:1", ModelDigest: digest, ModelRegistryDigest: digest}
	lite := operatorv1.ModelRole_MODEL_ROLE_LITE.String()
	assistant := operatorv1.ModelRole_MODEL_ROLE_ASSISTANT.String()
	tests := []struct {
		name    string
		purpose string
		role    string
		wantErr bool
	}{
		{name: "interrogation on the Lite slot", purpose: agentModelCallPurposeInterrogation, role: lite},
		{name: "interrogation on any other slot", purpose: agentModelCallPurposeInterrogation, role: assistant, wantErr: true},
		{name: "tribunal generate on the Lite slot", purpose: agentModelCallPurposeTribunalPrefix + "generate", role: lite},
		{name: "tribunal marshal on the Assistant slot", purpose: agentModelCallPurposeTribunalPrefix + "marshal", role: assistant},
		{name: "tribunal marshal on the Lite slot", purpose: agentModelCallPurposeTribunalPrefix + "marshal", role: lite, wantErr: true},
		{name: "scored turn off the designated slot", purpose: agentModelCallPurposeTurn, role: lite, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A provider failure carries no result, so the slot rule decides alone.
			call := AgentModelCall{Purpose: tt.purpose, ProviderAttemptID: "attempt", ModelVariantID: "variant", ModelRole: tt.role, ServedModelTag: "other:1", ModelDigest: digest, Error: "provider_failure"}
			err := verifyV7ModelCallIdentity(trace, call, oracle.Outcome{Kind: oracle.OutcomeExecutionFailed})
			if tt.wantErr {
				require.ErrorIs(t, err, constants.ErrEvaluationEvidenceTraceMismatch)
				return
			}
			require.NoError(t, err)
		})
	}
}

// deniedMeasurementRequest is a scenario whose answer and trajectory pass, so
// the final checks alone decide the verdict.
func deniedMeasurementRequest(gold ScenarioGoldCriteria) AssignmentExecutionRequest {
	gold.ContentCheck = &ScenarioContentCheck{RequiredTerms: [][]string{{"diagnosed"}}}
	gold.RoleCriteria = []ScenarioRoleCriteria{{Role: "primary", Criteria: []ScenarioCriterion{{CriterionID: "primary-responsibility"}}}}
	return AssignmentExecutionRequest{
		Assignment:    &evalv1.EvaluationAssignment{AssignmentId: "assign-1", RunId: "run-1"},
		ScenarioInput: ScenarioInputFixture{ScenarioID: "scenario-1"},
		ScenarioGold:  gold,
	}
}

func deniedMeasurementTrace(observations ...AgentScenarioObservation) *AgentTrace {
	return &AgentTrace{RunID: "run-1", AssignmentID: "assign-1", DesignatedRole: "primary", FinalOutput: "diagnosed",
		Workspace:    AgentTraceWorkspace{Root: "/work/case", OperatorWorkingDirectory: "/work"},
		ModelCalls:   []AgentModelCall{{Purpose: agentModelCallPurposeTurn, TransactionID: "tx-turn", ResultDigest: "digest-turn"}},
		Observations: observations}
}

func gradeOf(t *testing.T, grades []*evalv1.DeterministicGrade, criterion string) *evalv1.DeterministicGrade {
	t.Helper()
	for _, grade := range grades {
		if grade.GetCriterionId() == criterion {
			return grade
		}
	}
	require.Failf(t, "grade not found", "criterion %s", criterion)
	return nil
}

func TestGradeHomogeneousScenario_DeniedFinalCheckIsUnmeasuredNotAModelFailure(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	observation := deniedProbeObservation(t, ledger.rejected("probe-tx", string(constants.ActionTypeMcpCall)))
	req := deniedMeasurementRequest(ScenarioGoldCriteria{GovernedProbe: &investigation.GovernedProbe{Command: "sh", Args: []string{"./check.sh"}}})
	lifecycle := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED

	grading, err := GradeHomogeneousScenario(req, deniedMeasurementTrace(observation), lifecycle)
	require.NoError(t, err)

	unavailable := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE
	assert.Equal(t, unavailable, gradeOf(t, grading.DeterministicGrades, "governed-probe").GetStatus())
	assert.Equal(t, unavailable, gradeOf(t, grading.DeterministicGrades, "primary-responsibility").GetStatus())
	assert.Equal(t, unavailable, verdictFromGrades(grading.DeterministicGrades), "an unmeasured outcome is not a model verdict")
	assert.Equal(t, scenarioFinalCheckDeniedReason, grading.Trajectory.PublicFailureReason)
}

func TestGradeHomogeneousScenario_ExecutedProbeFailureStaysAModelFailure(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	prior, hash := ledger.advance()
	observation := deniedProbeObservation(t, ledger.signer.ExecutionFailed(t, ledger.opts("probe-tx", string(constants.ActionTypeMcpCall)), prior, hash))
	req := deniedMeasurementRequest(ScenarioGoldCriteria{GovernedProbe: &investigation.GovernedProbe{Command: "sh", Args: []string{"./check.sh"}}})

	grading, err := GradeHomogeneousScenario(req, deniedMeasurementTrace(observation), evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, err)

	fail := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	assert.Equal(t, fail, gradeOf(t, grading.DeterministicGrades, "governed-probe").GetStatus())
	assert.Equal(t, fail, verdictFromGrades(grading.DeterministicGrades))
}

// An observed mismatch is the model's whichever order the checks ran in; a
// denial on another check does not excuse it.
func TestGradeHomogeneousScenario_DeniedCheckDoesNotHideAnObservedFileStateFailure(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	absence := deniedProbeObservation(t, ledger.rejected("absent-tx", string(constants.ActionTypeMcpCall)))
	absence.Kind = scenarioObservationFile
	absence.Call.ArgumentsJSON = (models.RunShellCommandRequest{Command: "test", Args: []string{"!", "-e", "/work/case/gone.txt"}, WorkingDir: "/work/case"}).CanonicalJSON()
	req := deniedMeasurementRequest(ScenarioGoldCriteria{FileState: []investigation.FileState{{Path: "gone.txt", Absent: true}, {Path: "kept.txt", Contains: []string{"expected"}}}})

	grading, err := GradeHomogeneousScenario(req, deniedMeasurementTrace(absence), evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, err)

	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, gradeOf(t, grading.DeterministicGrades, "file-state").GetStatus())
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, verdictFromGrades(grading.DeterministicGrades))
}

func TestInvalidEvidenceReason_NamesTheStageWithFixedSentences(t *testing.T) {
	cause := constants.ErrEvaluationEvidenceTraceMismatch
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "model call", err: agentEvidenceError(agentEvidenceStageModelCall, "model call agent-inference-1", cause), want: "A model call's signed evidence does not match the trace."},
		{name: "tool call", err: agentEvidenceError(agentEvidenceStageToolCall, "tool call call-1 request", cause), want: "A tool call's signed evidence does not match the trace."},
		{name: "harness check", err: agentEvidenceError(agentEvidenceStageObservation, "scenario observation probe run_shell_command", cause), want: "A harness check's signed evidence does not match the trace."},
		{name: "ledger", err: agentEvidenceError(agentEvidenceStageLedger, "ledger bracket", cause), want: "The Operator ledger does not bracket the recorded activity."},
		{name: "runtime", err: agentEvidenceError(agentEvidenceStageRuntime, "runtime anchors", cause), want: "The trace's runtime identity could not be verified."},
		{name: "frozen model binding", err: fmt.Errorf("verify: %w", constants.ErrFormationRegistryBinding), want: invalidEvidenceReasonModelBinding},
		{name: "joined with another failure", err: errors.Join(nil, agentEvidenceError(agentEvidenceStageLedger, "ledger bracket", cause), constants.ErrFormationRegistryBinding), want: "The Operator ledger does not bracket the recorded activity."},
		{name: "unclassified", err: constants.ErrEvidenceArtifactMalformed, want: invalidEvidenceReasonUnclassified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := invalidEvidenceReason(tt.err)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "agent-inference-1", "a call identifier stays in the local detail")
		})
	}
}

func TestAgentEvidenceError_KeepsCauseAndSentinelIdentity(t *testing.T) {
	err := agentEvidenceError(agentEvidenceStageObservation, "scenario observation probe run_shell_command", constants.ErrEvaluationEvidenceTraceMismatch)
	require.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	require.ErrorIs(t, err, constants.ErrEvaluationEvidenceTraceMismatch)
	assert.Contains(t, err.Error(), "scenario observation probe run_shell_command")
}

func TestAssignmentOutcomeSummary_InvalidEvidenceStatesItsReason(t *testing.T) {
	result := &evalv1.EvaluationAssignmentResult{
		LifecycleStatus:     evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE,
		PublicFailureReason: "A harness check's signed evidence does not match the trace.",
	}
	assert.Equal(t, `INVALID_EVIDENCE verdict=INVALID_EVIDENCE reason="A harness check's signed evidence does not match the trace."`, AssignmentOutcomeSummary(result))
}

// regradeFixtureTrace seals a trace with no ledger evidence, which imports as
// INVALID_EVIDENCE, for one scheduled assignment.
func regradeFixtureTrace(t *testing.T, store *Store, assignment *evalv1.EvaluationAssignment) {
	t.Helper()
	workspace, err := NewScenarioWorkspace(agentTestWorkingDirectory, assignment.GetRunId(), "attempt-1")
	require.NoError(t, err)
	body, err := SealAgentTrace(&AgentTrace{RunID: assignment.GetRunId(), AssignmentID: assignment.GetAssignmentId(), EvaluationAttemptID: "attempt-1", DesignatedRole: "primary", Workspace: AgentTraceWorkspace(workspace), Input: testAgentTraceInput(), FinalOutput: "Checkout fails on payment timeouts."})
	require.NoError(t, err)
	require.NoError(t, store.SaveAssignmentAgentTrace(t.Context(), assignment.GetRunId(), assignment.GetAssignmentId(), body))
}

// Repair has to reach a formation's results: its Interrogator and denied-check
// verdicts are what a since-fixed verifier recorded as INVALID_EVIDENCE.
func TestRegradeAssignmentsFromAgentTraces_ReachesAFormationResult(t *testing.T) {
	fixture := newArchiveFixture(t)
	createBoundSystemCampaign(t, fixture, "formation-regrade")
	_, err := fixture.controller.StartRun(t.Context(), RunStartRequest{Governance: testGovernance, ProviderObservationPolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM, ModelProvenancePolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
		PlatformRelease: testPlatform.Release, CampaignID: "formation-regrade", RunID: "formation-regrade-run", Lane: evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM})
	require.NoError(t, err)
	_, err = fixture.controller.ScheduleRun(t.Context(), "formation-regrade-run")
	require.NoError(t, err)
	assignments, err := fixture.store.ListAssignments(t.Context(), "formation-regrade-run")
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	formation := assignments[0]
	require.True(t, IsHeterogeneousAssignment(formation))

	// A result the earlier verifier stored: INVALID_EVIDENCE with no reason.
	stored := &evalv1.EvaluationAssignmentResult{SchemaVersion: CampaignSchemaVersion, AssignmentId: formation.GetAssignmentId(), RunId: formation.GetRunId(), CampaignId: formation.GetCampaignId(), Lane: formation.GetLane(),
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, CompletedAt: timestamppb.New(time.Unix(1_800_000_000, 0))}
	stored.ResultDigest, err = ComputeAssignmentResultDigest(stored)
	require.NoError(t, err)
	require.NoError(t, fixture.controller.persistTerminalAssignment(t.Context(), formation, stored))
	regradeFixtureTrace(t, fixture.store, formation)

	regraded, err := fixture.controller.RegradeAssignmentsFromAgentTraces(t.Context(), "formation-regrade-run")
	require.NoError(t, err)
	assert.Equal(t, 1, regraded)
	result, err := fixture.store.LoadAssignmentResult(t.Context(), "formation-regrade-run", formation.GetAssignmentId())
	require.NoError(t, err)
	assert.NotEmpty(t, result.GetPublicFailureReason())
	assert.True(t, proto.Equal(stored.GetCompletedAt(), result.GetCompletedAt()))

	regraded, err = fixture.controller.RegradeAssignmentsFromAgentTraces(t.Context(), "formation-regrade-run")
	require.NoError(t, err)
	assert.Zero(t, regraded, "a result that states its reason is not imported again")
}
