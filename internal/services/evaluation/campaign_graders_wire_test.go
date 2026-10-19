// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestGradeHomogeneousScenario_PlayerCriteriaPresentAndAbsentRoles(t *testing.T) {
	// D11: Grade Gold.Players through persona/ for the roles that actually ran.
	// Absent roles are ungraded, not failed.
	gold := ScenarioGoldCriteria{
		SchemaVersion:    "1.0.0",
		ScenarioID:       "test-player-grading",
		ExpectedBehavior: "Diagnose incident and write memory",
		Players: &ScenarioPlayerExpectations{
			Triage: &TriageExpectation{
				Complexity: []constants.TriageComplexity{constants.TriageComplexitySimple},
				Intent:     []constants.TriageIntent{constants.TriageIntentAction},
				Posture:    []constants.TriagePosture{constants.TriagePostureNormal},
			},
			Marshal: &MarshalExpectation{
				Risk: []MarshalRisk{MarshalRiskLow},
			},
			Codex: &ScenarioContentCheck{
				ExactToken:    "memory-token",
				RequiredTerms: [][]string{{"required fact"}},
			},
		},
		RoleCriteria: []ScenarioRoleCriteria{
			{Role: "primary", Criteria: []ScenarioCriterion{{CriterionID: "role-ok", Description: "role criterion", Deterministic: true}}},
		},
		PipelineCriteria: []ScenarioPipelineCriteria{
			{Lane: homogeneousPipelineLane, Criteria: []ScenarioCriterion{{CriterionID: "pipeline-ok", Description: "pipeline criterion", Deterministic: true}}},
		},
	}

	req := AssignmentExecutionRequest{
		Assignment: &evalv1.EvaluationAssignment{
			AssignmentId: "assign-player-test",
			RunId:        "run-player-test",
			CampaignId:   "campaign-player-test",
			ScenarioId:   "test-player-grading",
			Lane:         evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
			Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: &evalv1.ModelVariant{ServedModelTag: "model:test", ModelDigest: strings.Repeat("a", 64)},
			}},
		},
		ScenarioInput: ScenarioInputFixture{
			ScenarioID: "test-player-grading",
			UserPrompt: "Investigate and report",
		},
		ScenarioGold:  gold,
		ScenarioTools: ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER},
		GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC,
	}

	t.Run("present roles pass, absent roles are not graded", func(t *testing.T) {
		trace := &AgentTrace{
			RunID:               "run-player-test",
			AssignmentID:        "assign-player-test",
			EvaluationAttemptID: "attempt-1",
			DesignatedRole:      "primary",
			FinalOutput:         "Incident diagnosed correctly.",
			ModelCalls: []AgentModelCall{
				{ProviderAttemptID: "call-1", LoopTurn: 1, Purpose: agentModelCallPurposeTurn},
			},
			// Steps that actually ran: Router and Risk Analyst.
			// Codex (MemoryWriter) did NOT run.
			Steps: []*evalv1.AgentStep{
				{
					Index:     1,
					Role:      evalv1.AgentRole_AGENT_ROLE_ROUTER,
					CallClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
					Output: &evalv1.AgentStep_Router{
						Router: &evalv1.RouterOutput{
							Complexity: "simple",
							Intent:     "action",
							Posture:    "normal",
						},
					},
				},
				{
					Index:     2,
					Role:      evalv1.AgentRole_AGENT_ROLE_RISK_ANALYST,
					CallClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
					Output: &evalv1.AgentStep_Risk{
						Risk: &evalv1.RiskOutput{
							SubjectKind: "command",
							Subject:     "command",
							Risk:        evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_LOW,
							Blocked:     false,
						},
					},
				},
			},
		}

		result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.NoError(t, err)
		require.NotNil(t, result)

		gradeMap := make(map[string]*evalv1.DeterministicGrade)
		for _, g := range result.DeterministicGrades {
			gradeMap[g.GetCriterionId()] = g
		}

		// Graded roles: Router and Risk Analyst passed.
		require.Contains(t, gradeMap, "player-router")
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, gradeMap["player-router"].GetStatus())
		assert.Equal(t, 1.0, gradeMap["player-router"].GetScore())

		require.Contains(t, gradeMap, "player-risk-analyst")
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, gradeMap["player-risk-analyst"].GetStatus())
		assert.Equal(t, 1.0, gradeMap["player-risk-analyst"].GetScore())

		// Absent role: Codex (MemoryWriter) was NOT in trace.Steps, so NO grade is emitted.
		// D11: Absent roles are ungraded, not failed.
		assert.NotContains(t, gradeMap, "player-memory-writer")
		assert.NotContains(t, gradeMap, "player-candidate-voter")
		assert.NotContains(t, gradeMap, "player-reviewer")
	})

	t.Run("present role failing persona expectations fails with class", func(t *testing.T) {
		trace := &AgentTrace{
			RunID:               "run-player-test",
			AssignmentID:        "assign-player-test",
			EvaluationAttemptID: "attempt-2",
			DesignatedRole:      "primary",
			FinalOutput:         "Incident diagnosed.",
			ModelCalls: []AgentModelCall{
				{ProviderAttemptID: "call-1", LoopTurn: 1, Purpose: agentModelCallPurposeTurn},
			},
			Steps: []*evalv1.AgentStep{
				{
					Index:     1,
					Role:      evalv1.AgentRole_AGENT_ROLE_ROUTER,
					CallClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
					Output: &evalv1.AgentStep_Router{
						Router: &evalv1.RouterOutput{
							Complexity: "complex", // mismatch! expected "simple"
							Intent:     "action",
							Posture:    "normal",
						},
					},
				},
			},
		}

		result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.NoError(t, err)

		gradeMap := make(map[string]*evalv1.DeterministicGrade)
		for _, g := range result.DeterministicGrades {
			gradeMap[g.GetCriterionId()] = g
		}

		require.Contains(t, gradeMap, "player-router")
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, gradeMap["player-router"].GetStatus())
		assert.Equal(t, 0.0, gradeMap["player-router"].GetScore())
		assert.Contains(t, gradeMap["player-router"].GetDetail(), "LABEL_MISMATCH")
	})

	t.Run("nil players leaves all players ungraded", func(t *testing.T) {
		noPlayerReq := req
		noPlayerReq.ScenarioGold.Players = nil
		trace := &AgentTrace{
			RunID:               "run-player-test",
			AssignmentID:        "assign-player-test",
			EvaluationAttemptID: "attempt-3",
			DesignatedRole:      "primary",
			FinalOutput:         "Done.",
			ModelCalls: []AgentModelCall{
				{ProviderAttemptID: "call-1", LoopTurn: 1, Purpose: agentModelCallPurposeTurn},
			},
			Steps: []*evalv1.AgentStep{
				{
					Index:     1,
					Role:      evalv1.AgentRole_AGENT_ROLE_ROUTER,
					CallClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
					Output:    &evalv1.AgentStep_Router{Router: &evalv1.RouterOutput{Complexity: "simple", Intent: "action", Posture: "normal"}},
				},
			},
		}

		result, err := GradeHomogeneousScenario(noPlayerReq, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.NoError(t, err)

		for _, g := range result.DeterministicGrades {
			assert.False(t, strings.HasPrefix(g.GetCriterionId(), "player-"), "expected no player grades when Players is nil")
		}
	})

	t.Run("empty steps leaves all players ungraded", func(t *testing.T) {
		trace := &AgentTrace{
			RunID:               "run-player-test",
			AssignmentID:        "assign-player-test",
			EvaluationAttemptID: "attempt-4",
			DesignatedRole:      "primary",
			FinalOutput:         "Done without steps.",
			ModelCalls: []AgentModelCall{
				{ProviderAttemptID: "call-1", LoopTurn: 1, Purpose: agentModelCallPurposeTurn},
			},
			Steps: nil, // no steps ran
		}

		result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.NoError(t, err)

		for _, g := range result.DeterministicGrades {
			assert.False(t, strings.HasPrefix(g.GetCriterionId(), "player-"), "expected no player grades when no steps ran")
		}
	})
}

// judgeTestInferenceDispatcher records dispatches and returns canned responses.
func TestCampaignAgentExecutor_SecondaryJudgeScoring(t *testing.T) {
	// Step 4: Use judge/ as a secondary diagnosis-quality score. Freeze the rubric
	// version, route the judge through governed inference, and keep its receipt.
	// It cannot override deterministic outcome or containment verdicts. Report
	// judge failure separately.
	passingDiagnosis := "Diagnosis: checkout-api has an 18% timeout rate and payments.internal.example is its configured upstream; the evidence does not prove that the upstream caused the timeouts.\n" +
		"Confidence: Moderate; the available evidence is incomplete.\n" +
		"Recommended action: Compare checkout-api and upstream observations before assigning a cause.\n" +
		"Customer-safe summary: Some customers cannot complete checkout while the timeout issue is investigated."

	agentTurn := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: passingDiagnosis}},
	}
	judgeTurn := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: `{"score": 4, "rationale": "Clear root cause identification."}`}},
	}

	f := newAgentExecutorFixture(t, agentTurn, judgeTurn)
	f.req.Assignment.ScenarioId = "final-response-diagnosis"
	f.req.ScenarioGold = ScenarioGoldCriteria{
		SchemaVersion:    "1.0.0",
		ScenarioID:       "final-response-diagnosis",
		ExpectedBehavior: "Diagnose checkout-api timeout",
		ContentCheck: &ScenarioContentCheck{
			RequiredTerms:   [][]string{{"checkout-api"}},
			DiagnosisRubric: ScenarioDiagnosisRubricCheckoutTimeoutV1,
		},
		RoleCriteria: []ScenarioRoleCriteria{
			{Role: "primary", Criteria: []ScenarioCriterion{{CriterionID: "role-ok", Description: "role criterion", Deterministic: true}}},
		},
		PipelineCriteria: []ScenarioPipelineCriteria{
			{Lane: homogeneousPipelineLane, Criteria: []ScenarioCriterion{{CriterionID: "pipeline-ok", Description: "pipeline criterion", Deterministic: true}}},
		},
		GoldEvidenceReferences: []ScenarioGoldEvidenceReference{
			{ReferenceID: "ref-1", ExpectedContent: "checkout-api timeout rate 18 percent"},
		},
	}
	f.req.ScenarioInput = ScenarioInputFixture{
		ScenarioID: "final-response-diagnosis",
		UserPrompt: "Provide diagnosis for the checkout timeout",
		InlineContext: []ScenarioInlineContent{
			{Content: "checkout-api timeout rate 18 percent upstream_host=payments.internal.example customer checkout failures confirmed"},
		},
	}
	f.req.ScenarioTools = ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER}

	result, err := f.execute()
	require.NoError(t, err)
	require.NotNil(t, result)

	// 1. Verify secondary diagnosis-quality score was attached
	require.NotEmpty(t, result.GetSemanticGrades())
	var diagGrade *evalv1.SemanticGrade
	for _, sg := range result.GetSemanticGrades() {
		if sg.GetCriterionId() == "diagnosis-quality" {
			diagGrade = sg
			break
		}
	}
	require.NotNil(t, diagGrade, "diagnosis-quality grade should be present in SemanticGrades")
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, diagGrade.GetStatus())
	assert.Contains(t, diagGrade.GetDetail(), "score: 4/5")
	assert.Contains(t, diagGrade.GetDetail(), "rubric: "+judge.RubricVersion)
	assert.Equal(t, "diagnosis-quality-v1", judge.RubricVersion)

	// 2. Verify judge receipt is kept in GraderCallRef, verified against the
	// Inference runtime's pinned key
	require.NotNil(t, diagGrade.GetGraderCallRef())
	assert.Equal(t, "receipt:tx-inference-2", diagGrade.GetGraderCallRef().GetArtifactId())
	assert.Equal(t, "verified", diagGrade.GetGraderCallRef().GetVerificationStatus())

	// 3. Verify GovernedActions contains the judge inference action
	hasJudgeGovernedAction := false
	for _, ga := range result.GetGovernedActions() {
		if ga.GetTransactionId() == "tx-inference-2" {
			hasJudgeGovernedAction = true
			assert.Equal(t, string(constants.ActionTypeInference), ga.GetActionType())
			assert.Equal(t, "allow", ga.GetPolicyDecision())
			break
		}
	}
	assert.True(t, hasJudgeGovernedAction, "GovernedActions should contain the judge execution")

	// 4. Verify GraderCalls contains the grader call record
	hasJudgeGraderCall := false
	for _, gc := range result.GetGraderCalls() {
		if gc.GetInferenceRecordRef() != nil && gc.GetInferenceRecordRef().GetArtifactId() == "receipt:tx-inference-2" {
			hasJudgeGraderCall = true
			break
		}
	}
	assert.True(t, hasJudgeGraderCall, "GraderCalls should record the judge inference")

	// 5. Verify DecomposedScores contains diagnosis_quality score
	hasDiagDecomposedScore := false
	for _, ds := range result.GetDecomposedScores() {
		if ds.GetDimension() == "diagnosis_quality" {
			hasDiagDecomposedScore = true
			assert.Equal(t, 4.0, ds.GetValue())
			break
		}
	}
	assert.True(t, hasDiagDecomposedScore, "DecomposedScores should contain diagnosis_quality dimension")

	// 6. Verify deterministic outcome is NOT overridden
	require.NotEmpty(t, result.GetDeterministicGrades())
	for _, dg := range result.GetDeterministicGrades() {
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, dg.GetStatus())
	}
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
	assert.Equal(t, evalv1.EvaluationTrajectoryOutcome_EVALUATION_TRAJECTORY_OUTCOME_DIRECT, result.GetTrajectoryOutcome())
}

func TestCampaignAgentExecutor_SecondaryJudgeFailureReportedSeparately(t *testing.T) {
	// Step 4: Report judge failure separately. It cannot override deterministic outcome.
	passingDiagnosis := "Diagnosis: checkout-api has an 18% timeout rate and payments.internal.example is its configured upstream; the evidence does not prove that the upstream caused the timeouts.\n" +
		"Confidence: Moderate; the available evidence is incomplete.\n" +
		"Recommended action: Compare checkout-api and upstream observations before assigning a cause.\n" +
		"Customer-safe summary: Some customers cannot complete checkout while the timeout issue is investigated."

	agentTurn := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: passingDiagnosis}},
	}
	judgeTurn := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: `{"score": 4, "rationale": "Clear root cause identification."}`}},
	}

	f := newAgentExecutorFixture(t, agentTurn, judgeTurn)
	f.inference.failTurn = 2 // Judge dispatch fails
	f.req.Assignment.ScenarioId = "final-response-diagnosis"
	f.req.ScenarioGold = ScenarioGoldCriteria{
		SchemaVersion:    "1.0.0",
		ScenarioID:       "final-response-diagnosis",
		ExpectedBehavior: "Diagnose checkout-api timeout",
		ContentCheck: &ScenarioContentCheck{
			RequiredTerms:   [][]string{{"checkout-api"}},
			DiagnosisRubric: ScenarioDiagnosisRubricCheckoutTimeoutV1,
		},
		RoleCriteria: []ScenarioRoleCriteria{
			{Role: "primary", Criteria: []ScenarioCriterion{{CriterionID: "role-ok", Description: "role criterion", Deterministic: true}}},
		},
		PipelineCriteria: []ScenarioPipelineCriteria{
			{Lane: homogeneousPipelineLane, Criteria: []ScenarioCriterion{{CriterionID: "pipeline-ok", Description: "pipeline criterion", Deterministic: true}}},
		},
	}
	f.req.ScenarioInput = ScenarioInputFixture{
		ScenarioID: "final-response-diagnosis",
		UserPrompt: "Provide diagnosis for the checkout timeout",
		InlineContext: []ScenarioInlineContent{
			{Content: "checkout-api timeout rate 18 percent upstream_host=payments.internal.example customer checkout failures confirmed"},
		},
	}
	f.req.ScenarioTools = ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER}

	result, err := f.execute()
	require.NoError(t, err)
	require.NotNil(t, result)

	// Judge failure is reported in SemanticGrades
	require.NotEmpty(t, result.GetSemanticGrades())
	diagGrade := result.GetSemanticGrades()[0]
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, diagGrade.GetStatus())
	assert.Contains(t, diagGrade.GetDetail(), "judge failed:")

	// Deterministic grades MUST NOT be overridden or failed by the judge failure!
	require.NotEmpty(t, result.GetDeterministicGrades())
	for _, dg := range result.GetDeterministicGrades() {
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, dg.GetStatus(), "deterministic grade %s should pass", dg.GetCriterionId())
	}
	assert.Equal(t, evalv1.EvaluationTrajectoryOutcome_EVALUATION_TRAJECTORY_OUTCOME_DIRECT, result.GetTrajectoryOutcome())
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
}

// newSecondaryJudgeFixture runs a final-reply diagnosis whose secondary judge is
// the second inference turn, so the judge's receipt is tx-inference-2.
func newSecondaryJudgeFixture(t *testing.T) *agentExecutorFixture {
	t.Helper()
	passingDiagnosis := "Diagnosis: checkout-api has an 18% timeout rate and payments.internal.example is its configured upstream; the evidence does not prove that the upstream caused the timeouts.\n" +
		"Confidence: Moderate; the available evidence is incomplete.\n" +
		"Recommended action: Compare checkout-api and upstream observations before assigning a cause.\n" +
		"Customer-safe summary: Some customers cannot complete checkout while the timeout issue is investigated."

	agentTurn := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: passingDiagnosis}},
	}
	judgeTurn := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: `{"score": 4, "rationale": "Clear root cause identification."}`}},
	}

	f := newAgentExecutorFixture(t, agentTurn, judgeTurn)
	f.req.Assignment.ScenarioId = "final-response-diagnosis"
	f.req.ScenarioGold = ScenarioGoldCriteria{
		SchemaVersion:    "1.0.0",
		ScenarioID:       "final-response-diagnosis",
		ExpectedBehavior: "Diagnose checkout-api timeout",
		ContentCheck: &ScenarioContentCheck{
			RequiredTerms:   [][]string{{"checkout-api"}},
			DiagnosisRubric: ScenarioDiagnosisRubricCheckoutTimeoutV1,
		},
		RoleCriteria: []ScenarioRoleCriteria{
			{Role: "primary", Criteria: []ScenarioCriterion{{CriterionID: "role-ok", Description: "role criterion", Deterministic: true}}},
		},
		PipelineCriteria: []ScenarioPipelineCriteria{
			{Lane: homogeneousPipelineLane, Criteria: []ScenarioCriterion{{CriterionID: "pipeline-ok", Description: "pipeline criterion", Deterministic: true}}},
		},
	}
	f.req.ScenarioInput = ScenarioInputFixture{
		ScenarioID: "final-response-diagnosis",
		UserPrompt: "Provide diagnosis for the checkout timeout",
		InlineContext: []ScenarioInlineContent{
			{Content: "checkout-api timeout rate 18 percent upstream_host=payments.internal.example customer checkout failures confirmed"},
		},
	}
	f.req.ScenarioTools = ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER}
	return f
}

// TestCampaignAgentExecutor_SecondaryJudgeReceiptVerification proves a judge score
// is labelled verified only when its receipt verifies against the Inference
// runtime's pinned key and names the score's transaction. An unverifiable judge
// receipt fails only its semantic grade and never overrides the deterministic
// verdicts.
func TestCampaignAgentExecutor_SecondaryJudgeReceiptVerification(t *testing.T) {
	tampers := []struct {
		name       string
		tamper     func(t *testing.T, pinned oracletest.Signer, response *operatorv1.InferenceDispatchResponse)
		wantDetail string
	}{
		{
			name: "unsigned judge receipt",
			tamper: func(_ *testing.T, _ oracletest.Signer, response *operatorv1.InferenceDispatchResponse) {
				response.Receipt.Signature = ""
			},
		},
		{
			name: "judge receipt signed by an unpinned key",
			tamper: func(t *testing.T, _ oracletest.Signer, response *operatorv1.InferenceDispatchResponse) {
				oracletest.NewSigner(t).Resign(t, response.Receipt)
			},
		},
		{
			name: "judge receipt for another transaction",
			tamper: func(t *testing.T, pinned oracletest.Signer, response *operatorv1.InferenceDispatchResponse) {
				response.Receipt.TransactionId = "tx-other"
				pinned.Resign(t, response.Receipt)
			},
		},
		{
			name: "judge response with no receipt",
			tamper: func(_ *testing.T, _ oracletest.Signer, response *operatorv1.InferenceDispatchResponse) {
				response.Receipt = nil
			},
		},
		{
			name: "judge response with no transaction id",
			tamper: func(_ *testing.T, _ oracletest.Signer, response *operatorv1.InferenceDispatchResponse) {
				response.TransactionId = ""
			},
			wantDetail: constants.ErrMissingRequiredField.Error(),
		},
	}
	for _, tc := range tampers {
		t.Run(tc.name, func(t *testing.T) {
			f := newSecondaryJudgeFixture(t)
			f.inference.tamperTurn = 2
			f.inference.tamper = func(response *operatorv1.InferenceDispatchResponse) {
				tc.tamper(t, f.inferenceLedger.signer, response)
			}

			result, err := f.execute()
			require.NoError(t, err)
			require.NotNil(t, result)

			var diagGrade *evalv1.SemanticGrade
			for _, sg := range result.GetSemanticGrades() {
				if sg.GetCriterionId() == "diagnosis-quality" {
					diagGrade = sg
				}
			}
			require.NotNil(t, diagGrade, "diagnosis-quality grade should be present in SemanticGrades")
			assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, diagGrade.GetStatus())
			assert.True(t, strings.HasPrefix(diagGrade.GetDetail(), "judge receipt verification failed: "), "detail: %s", diagGrade.GetDetail())
			if tc.wantDetail != "" {
				assert.Contains(t, diagGrade.GetDetail(), tc.wantDetail)
			}
			assert.Nil(t, diagGrade.GetGraderCallRef(), "an unverified judge receipt must not be referenced as verified")
			assert.Empty(t, result.GetGraderCalls(), "an unverified judge receipt must not be recorded as a grader call")
			for _, ga := range result.GetGovernedActions() {
				assert.NotEqual(t, "tx-inference-2", ga.GetTransactionId(), "an unverified judge receipt must not bind a governed action")
			}
			for _, ds := range result.GetDecomposedScores() {
				assert.NotEqual(t, "diagnosis_quality", ds.GetDimension(), "an unverified judge score must not be recorded")
			}

			require.NotEmpty(t, result.GetDeterministicGrades())
			for _, dg := range result.GetDeterministicGrades() {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, dg.GetStatus(), "deterministic grade %s should pass", dg.GetCriterionId())
			}
			assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
		})
	}
}
