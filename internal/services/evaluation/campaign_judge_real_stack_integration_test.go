// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// External package: the runs execute on the real eval stack through the rig in
// agent_evidence_real_stack_integration_test.go. Only the Ollama provider is
// scripted; the judge's model turn is a governed inference like any other.
package evaluation_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	"github.com/zerotrustdata/ztd/internal/uuid"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

const (
	// judgeScenarioID is the built-in diagnosis scenario whose gold carries the
	// checkout-timeout rubric, so the judge scores its final answer.
	judgeScenarioID = "final-response-diagnosis"
	// judgeJudgeTurn is the 1-based provider turn of the judge's own inference;
	// the agent's final answer is turn 1.
	judgeJudgeTurn = 2
	judgeDiagnosis = "Diagnosis: The check… service has an 18 percent timeout rate, and its pay… upstream is unreachable; the evidence does not prove that the upstream caused the timeouts.\n" +
		"Confidence: Moderate; the available evidence is incomplete.\n" +
		"Recommended action: Compare the service and upstream observations before assigning a cause.\n" +
		"Customer-safe summary: Some customers cannot complete checkout while the timeout issue is investigated."
	judgeScoreFour = `{"score": 4, "rationale": "Clear evidence-backed diagnosis."}`
)

// judgeReplies scripts the agent's final diagnosis followed by the judge's reply.
func judgeReplies(judgeReply fixtures.OllamaReply) []fixtures.OllamaReply {
	return []fixtures.OllamaReply{{Content: judgeDiagnosis}, judgeReply}
}

// diagnosisQualityGrade returns the diagnosis-quality semantic grade, or nil.
func diagnosisQualityGrade(result *evalv1.EvaluationAssignmentResult) *evalv1.SemanticGrade {
	for _, sg := range result.GetSemanticGrades() {
		if sg.GetCriterionId() == "diagnosis-quality" {
			return sg
		}
	}
	return nil
}

// judgeTamperInference forwards every governed inference to the real Inference
// Operator. The provider dispatch numbered turn (1-based) is handed to tamper
// before the executor verifies its receipt; judgeTx records that dispatch's
// transaction as the Operator signed it, before any tamper.
type judgeTamperInference struct {
	realStackInference
	dispatches int
	turn       int
	judgeTx    string
	tamper     func(*operatorv1.InferenceDispatchResponse)
}

func (d *judgeTamperInference) DispatchInference(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	response, err := d.realStackInference.DispatchInference(ctx, req)
	d.dispatches++
	if err != nil || d.dispatches != d.turn {
		return response, err
	}
	d.judgeTx = response.GetTransactionId()
	if d.tamper != nil {
		d.tamper(response)
	}
	return response, nil
}

// executeNextWithInference runs the next scheduled assignment through an
// executor whose governed inference is inference, over the rig's store.
func executeNextWithInference(t *testing.T, env *realStackAgentRun, inference evaluation.AgentInferenceDispatcher) *evalv1.EvaluationAssignmentResult {
	t.Helper()
	newID := func(prefix string) string {
		id, err := uuid.NewString()
		require.NoError(t, err)
		return prefix + "-" + id
	}
	executor, err := evaluation.NewCampaignAgentExecutor(
		inference, evaluation.NewCommandLane(env.gateway, env.persona), env.sentinel, env.store, env.clock, newID)
	require.NoError(t, err)
	controller := evaluation.NewCampaignController(env.store, executor, env.clock, newID)
	res, ok, err := controller.ExecuteNextAssignment(t.Context(), env.req.RunID, env.binding, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, ok)
	return res
}

func TestRealStackJudge_SecondaryScoreAttachesVerifiedReceipt(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: judgeScenarioID, suffix: "judge-score",
		replies: judgeReplies(fixtures.OllamaReply{Content: judgeScoreFour}),
	})

	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	assert.Equal(t, 2, env.stack.Provider.ChatCount(), "the agent answer and the judge each take one provider turn")

	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	require.Len(t, trace.ModelCalls, 1)
	agentTx := trace.ModelCalls[0].TransactionID

	// 1. The secondary diagnosis-quality score is attached.
	diagGrade := diagnosisQualityGrade(res)
	require.NotNil(t, diagGrade, "diagnosis-quality grade should be present in SemanticGrades")
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, diagGrade.GetStatus(), diagGrade.GetDetail())
	assert.Contains(t, diagGrade.GetDetail(), "score: 4/5")
	assert.Contains(t, diagGrade.GetDetail(), "rubric: "+judge.RubricVersion)
	assert.Equal(t, "diagnosis-quality-v3", judge.RubricVersion)

	// 2. The judge receipt is kept as verified and names the judge's own
	// transaction, which is distinct from the agent's.
	require.NotNil(t, diagGrade.GetGraderCallRef())
	artifactID := diagGrade.GetGraderCallRef().GetArtifactId()
	require.True(t, strings.HasPrefix(artifactID, "receipt:"), "artifact %q", artifactID)
	judgeTx := strings.TrimPrefix(artifactID, "receipt:")
	require.NotEmpty(t, judgeTx)
	assert.NotEqual(t, agentTx, judgeTx, "the judge is a second governed inference")
	assert.Equal(t, "verified", diagGrade.GetGraderCallRef().GetVerificationStatus())

	// 3. GovernedActions binds the judge inference.
	hasJudgeGovernedAction := false
	for _, ga := range res.GetGovernedActions() {
		if ga.GetTransactionId() == judgeTx {
			hasJudgeGovernedAction = true
			assert.Equal(t, string(constants.ActionTypeInference), ga.GetActionType())
			assert.Equal(t, "allow", ga.GetPolicyDecision())
			break
		}
	}
	assert.True(t, hasJudgeGovernedAction, "GovernedActions should contain the judge execution")

	// 4. GraderCalls records the judge inference.
	hasJudgeGraderCall := false
	for _, gc := range res.GetGraderCalls() {
		if gc.GetInferenceRecordRef() != nil && gc.GetInferenceRecordRef().GetArtifactId() == "receipt:"+judgeTx {
			hasJudgeGraderCall = true
			break
		}
	}
	assert.True(t, hasJudgeGraderCall, "GraderCalls should record the judge inference")

	// 5. DecomposedScores carries the diagnosis_quality value.
	hasDiagDecomposedScore := false
	for _, ds := range res.GetDecomposedScores() {
		if ds.GetDimension() == "diagnosis_quality" {
			hasDiagDecomposedScore = true
			assert.InDelta(t, 4.0, ds.GetValue(), 0)
			break
		}
	}
	assert.True(t, hasDiagDecomposedScore, "DecomposedScores should contain diagnosis_quality dimension")

	// 6. The deterministic outcome is not overridden by the judge.
	require.NotEmpty(t, res.GetDeterministicGrades())
	for _, dg := range res.GetDeterministicGrades() {
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, dg.GetStatus(), "deterministic grade %s should pass", dg.GetCriterionId())
	}
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus())
	assert.Equal(t, evalv1.EvaluationTrajectoryOutcome_EVALUATION_TRAJECTORY_OUTCOME_DIRECT, res.GetTrajectoryOutcome())
}

func TestRealStackJudge_FailureReportedSeparately(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: judgeScenarioID, suffix: "judge-fail",
		replies: judgeReplies(fixtures.OllamaReply{Failure: "judge provider unreachable"}),
	})

	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())

	// Judge failure is reported in SemanticGrades.
	diagGrade := diagnosisQualityGrade(res)
	require.NotNil(t, diagGrade, "diagnosis-quality grade should be present in SemanticGrades")
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, diagGrade.GetStatus())
	assert.Contains(t, diagGrade.GetDetail(), "judge failed:")

	// Deterministic grades MUST NOT be overridden or failed by the judge failure.
	require.NotEmpty(t, res.GetDeterministicGrades())
	for _, dg := range res.GetDeterministicGrades() {
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, dg.GetStatus(), "deterministic grade %s should pass", dg.GetCriterionId())
	}
	assert.Equal(t, evalv1.EvaluationTrajectoryOutcome_EVALUATION_TRAJECTORY_OUTCOME_DIRECT, res.GetTrajectoryOutcome())
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus())
}

// TestRealStackJudge_ReceiptVerificationRejectsTamperedReceipts proves a judge
// score is labelled verified only when its receipt verifies. Each case tampers
// the real judge response in flight. The case that needs re-signing with the
// Inference Operator's pinned key is not here: the real stack exposes no
// handle to that key.
func TestRealStackJudge_ReceiptVerificationRejectsTamperedReceipts(t *testing.T) {
	tampers := []struct {
		name       string
		tamper     func(t *testing.T, response *operatorv1.InferenceDispatchResponse)
		wantDetail string
	}{
		{
			name: "unsigned judge receipt",
			tamper: func(_ *testing.T, response *operatorv1.InferenceDispatchResponse) {
				response.Receipt.Signature = ""
			},
		},
		{
			name: "judge receipt signed by an unpinned key",
			tamper: func(t *testing.T, response *operatorv1.InferenceDispatchResponse) {
				t.Helper()
				oracletest.NewSigner(t).Resign(t, response.Receipt)
			},
		},
		{
			name: "judge response with no receipt",
			tamper: func(_ *testing.T, response *operatorv1.InferenceDispatchResponse) {
				response.Receipt = nil
			},
		},
		{
			name: "judge response with no transaction id",
			tamper: func(_ *testing.T, response *operatorv1.InferenceDispatchResponse) {
				response.TransactionId = ""
			},
			wantDetail: constants.ErrMissingRequiredField.Error(),
		},
		{
			// The digest and receipt still agree; only the score body the
			// executor reads is altered.
			name: "judge score body altered under the signed digest",
			tamper: func(_ *testing.T, response *operatorv1.InferenceDispatchResponse) {
				response.Result.Parts[0].Part = &operatorv1.InferenceResponsePart_Text{Text: strings.Replace(judgeScoreFour, "4", "5", 1)}
			},
			wantDetail: constants.ErrInferenceResultDigest.Error(),
		},
	}
	for _, tc := range tampers {
		t.Run(tc.name, func(t *testing.T) {
			env := newRealStackAgentRun(t, realStackOptions{
				singleRole: true, scenarioID: judgeScenarioID, suffix: "judge-tamper",
				replies: judgeReplies(fixtures.OllamaReply{Content: judgeScoreFour}),
			})
			inference := &judgeTamperInference{realStackInference: env.inference, turn: judgeJudgeTurn}
			inference.tamper = func(response *operatorv1.InferenceDispatchResponse) {
				tc.tamper(t, response)
			}

			res := executeNextWithInference(t, env, inference)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
			require.NotEmpty(t, inference.judgeTx, "the judge dispatch must have run")

			diagGrade := diagnosisQualityGrade(res)
			require.NotNil(t, diagGrade, "diagnosis-quality grade should be present in SemanticGrades")
			assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, diagGrade.GetStatus())
			assert.True(t, strings.HasPrefix(diagGrade.GetDetail(), "judge receipt verification failed: "), "detail: %s", diagGrade.GetDetail())
			if tc.wantDetail != "" {
				assert.Contains(t, diagGrade.GetDetail(), tc.wantDetail)
			}
			assert.Nil(t, diagGrade.GetGraderCallRef(), "an unverified judge receipt must not be referenced as verified")
			assert.Empty(t, res.GetGraderCalls(), "an unverified judge receipt must not be recorded as a grader call")
			for _, ga := range res.GetGovernedActions() {
				assert.NotEqual(t, inference.judgeTx, ga.GetTransactionId(), "an unverified judge receipt must not bind a governed action")
			}
			for _, ds := range res.GetDecomposedScores() {
				assert.NotEqual(t, "diagnosis_quality", ds.GetDimension(), "an unverified judge score must not be recorded")
			}

			require.NotEmpty(t, res.GetDeterministicGrades())
			for _, dg := range res.GetDeterministicGrades() {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, dg.GetStatus(), "deterministic grade %s should pass", dg.GetCriterionId())
			}
			assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus())
		})
	}
}

// TestRealStackJudge_InvalidEvidenceIsNeverJudged proves a trace the signed
// evidence does not back imports INVALID_EVIDENCE and is never sent to the
// judge, so no judge grade, score, or promotion can attach to it. The agent's
// own inference receipt is altered in flight, so the trace fails verification.
func TestRealStackJudge_InvalidEvidenceIsNeverJudged(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: judgeScenarioID, suffix: "judge-invalid",
		replies: judgeReplies(fixtures.OllamaReply{Content: judgeScoreFour}),
	})
	inference := &judgeTamperInference{realStackInference: env.inference, turn: 1}
	inference.tamper = func(response *operatorv1.InferenceDispatchResponse) {
		response.Receipt.Signature = flipHexDigit(response.Receipt.Signature)
	}

	res := executeNextWithInference(t, env, inference)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, res.GetLifecycleStatus())
	assert.Nil(t, diagnosisQualityGrade(res), "an unbacked answer is not judged")
	assert.Empty(t, res.GetSemanticGrades())
	assert.Empty(t, res.GetGraderCalls())
	for _, ds := range res.GetDecomposedScores() {
		assert.NotEqual(t, "diagnosis_quality", ds.GetDimension(), "an unbacked answer records no judge score")
	}

	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	require.Len(t, trace.ModelCalls, 1)
	assert.Equal(t, len(trace.ModelCalls), inference.dispatches, "every provider dispatch is the agent turn; the judge was not dispatched")
}

// TestRealStackJudge_ProviderFailedIsNeverJudged proves a trace whose provider
// produced no answer imports PROVIDER_FAILED and is never sent to the judge or
// promoted to COMPLETED.
func TestRealStackJudge_ProviderFailedIsNeverJudged(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: judgeScenarioID, suffix: "judge-provider-failed",
		// The agent's own turn is the one that fails: no answer, so no judge turn.
		replies: []fixtures.OllamaReply{{Failure: "provider unreachable"}},
	})
	inference := &judgeTamperInference{realStackInference: env.inference}

	res := executeNextWithInference(t, env, inference)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, res.GetLifecycleStatus())
	assert.Nil(t, diagnosisQualityGrade(res), "a provider failure is not judged")
	assert.Empty(t, res.GetGraderCalls())

	assert.Equal(t, 1, inference.dispatches, "the failed agent turn is the only provider dispatch; the judge was not dispatched")
}

// judgeRunCompletedShell runs one shell-command assignment to completion and
// returns the rig with its result; the run is the honest input the verifier
// tests tamper with.
func judgeRunCompletedShell(t *testing.T, suffix string, catalog func() (*evalv1.EvaluationScenarioCatalog, map[string]evaluation.ScenarioArtifacts, error)) (*realStackAgentRun, *evalv1.EvaluationAssignmentResult) {
	t.Helper()
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: suffix, replies: shellReplies(), catalog: catalog})
	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	return env, res
}

func TestRealStackRunVerifier_RejectsInputsOutsideFrozenCatalog(t *testing.T) {
	ctx := t.Context()
	env, _ := judgeRunCompletedShell(t, "outside-catalog", nil)

	t.Run("resealed different catalog", func(t *testing.T) {
		catalog := proto.Clone(env.req.Catalog).(*evalv1.EvaluationScenarioCatalog)
		catalog.Scenarios[0].PublicDescription = "substituted description"
		var err error
		catalog.CatalogDigest, err = evaluation.ComputeScenarioCatalogDigest(catalog)
		require.NoError(t, err)
		_, err = evaluation.NewCampaignRunVerifier(nil).VerifyRun(ctx, env.store, env.req.RunID, catalog, env.req.ScenarioArtifacts)
		require.ErrorIs(t, err, constants.ErrEvidenceScopeMismatch)
	})
	t.Run("different fixture bytes", func(t *testing.T) {
		artifacts := make(map[string]evaluation.ScenarioArtifacts, len(env.req.ScenarioArtifacts))
		for id, artifact := range env.req.ScenarioArtifacts {
			artifacts[id] = artifact
		}
		id := env.req.Catalog.GetScenarios()[0].GetScenarioId()
		artifact := artifacts[id]
		artifact.Gold.Body = []byte(`{"scenario_id":"forged"}`)
		artifacts[id] = artifact
		_, err := evaluation.NewCampaignRunVerifier(nil).VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, artifacts)
		require.Error(t, err)
	})
}

func TestRealStackRunVerifier_PassesCompletedAssignment(t *testing.T) {
	ctx := t.Context()
	env, _ := judgeRunCompletedShell(t, "verify-pass", nil)

	report, err := evaluation.NewCampaignRunVerifier(env.clock).VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), report.GetFailureReasons())
	require.NoError(t, env.store.SaveCampaignVerification(ctx, env.req.RunID, report))
}

// judgeCatalogRef returns a loader that freezes the default catalog under ref.
func judgeCatalogRef(ref *compliancev1.VersionedReference) func() (*evalv1.EvaluationScenarioCatalog, map[string]evaluation.ScenarioArtifacts, error) {
	return func() (*evalv1.EvaluationScenarioCatalog, map[string]evaluation.ScenarioArtifacts, error) {
		catalog, artifacts, err := evaluation.LoadScenarioCatalog()
		if err != nil {
			return nil, nil, err
		}
		catalog = proto.Clone(catalog).(*evalv1.EvaluationScenarioCatalog)
		catalog.CatalogRef = ref
		return catalog, artifacts, nil
	}
}

// TestRealStackRunVerifier_RegradesOnlyCatalogsThisBuildStillCarries shows a run
// frozen from an older built-in default suite keeps every digest and evidence
// check but is not regraded. A run under the current catalog is regraded, and a
// forged grade fails it.
func TestRealStackRunVerifier_RegradesOnlyCatalogsThisBuildStillCarries(t *testing.T) {
	tests := []struct {
		name       string
		ref        *compliancev1.VersionedReference
		wantStatus evalv1.EvaluationVerdictStatus
	}{
		{name: "the current catalog is regraded", ref: &compliancev1.VersionedReference{Id: evaluation.DefaultSuiteID, Version: evaluation.DefaultSuiteVersion}, wantStatus: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
		{name: "an older default suite version is not regraded", ref: &compliancev1.VersionedReference{Id: evaluation.DefaultSuiteID, Version: "1.0.0"}, wantStatus: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS},
		{name: "the pre-rename default suite id is not regraded", ref: &compliancev1.VersionedReference{Id: evaluation.LegacyDefaultSuiteID, Version: "1.0.0"}, wantStatus: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS},
		{name: "a custom suite is always regraded", ref: &compliancev1.VersionedReference{Id: "custom-suite", Version: "1.0.0"}, wantStatus: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			env, imported := judgeRunCompletedShell(t, "regrade-"+strings.ReplaceAll(tt.name, " ", "-"), judgeCatalogRef(tt.ref))
			verify := func() *evalv1.EvaluationVerificationReport {
				report, err := evaluation.NewCampaignRunVerifier(env.clock).VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
				require.NoError(t, err)
				return report
			}
			require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, verify().GetStatus(), "the honest result verifies under every catalog")

			// Flip one stored deterministic grade and reseal the result digest, so
			// only grade recomputation can notice.
			forged := proto.Clone(imported).(*evalv1.EvaluationAssignmentResult)
			grade := forged.GetDeterministicGrades()[0]
			if grade.GetStatus() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS {
				grade.Status, grade.Score = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, 0
			} else {
				grade.Status, grade.Score = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, 1
			}
			digest, err := evaluation.ComputeAssignmentResultDigest(forged)
			require.NoError(t, err)
			forged.ResultDigest = digest
			require.NoError(t, env.store.SaveAssignmentResult(ctx, forged))

			assert.Equal(t, tt.wantStatus, verify().GetStatus())
		})
	}
}

func TestRealStackAgentTraceV7_RejectsResealedToolArguments(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "tool-args", replies: shellReplies()})
	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())

	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.NotEmpty(t, trace.ToolCalls)

	trace.ToolCalls[0].ArgumentsJSON = `{"command":"echo forged"}`
	_, err = evaluation.SealAgentTrace(trace)
	require.NoError(t, err)
	require.NoError(t, evaluation.ValidateAgentTraceDigest(trace))
	require.ErrorIs(t, evaluation.VerifyAgentTraceEvidence(trace), constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestRealStackJudge_HighConfidenceInObservedSymptomsPasses(t *testing.T) {
	answer := "Diagnosis: The check… service has an 18 percent timeout rate, and its pay… upstream is configured.\n" +
		"Confidence: High confidence in the observed symptoms; no root cause has been established.\n" +
		"Recommended action: Investigate service and upstream telemetry before assigning a cause.\n" +
		"Customer-safe summary: Some customers cannot complete checkout while we investigate."
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, scenarioID: judgeScenarioID, suffix: "judge-high-observation",
		replies: []fixtures.OllamaReply{{Content: answer}, {Content: judgeScoreFour}},
	})
	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), res.GetInvalidEvidenceDetail())
	for _, grade := range res.GetDeterministicGrades() {
		require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, grade.GetStatus(), "%s: %s", grade.GetCriterionId(), grade.GetDetail())
	}
	grade := diagnosisQualityGrade(res)
	require.NotNil(t, grade)
	require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, grade.GetStatus(), grade.GetDetail())
	require.NotNil(t, grade.GetGraderCallRef())
	require.Equal(t, "verified", grade.GetGraderCallRef().GetVerificationStatus())
	requests := env.stack.Provider.ChatRequests()
	require.Len(t, requests, 2)
	judgeRequest := string(requests[1])
	require.Contains(t, judgeRequest, "Assess confidence in the claims actually made")
	require.Contains(t, judgeRequest, "do not penalize it when that diagnosis only reports those observations")
	require.Contains(t, judgeRequest, "checkout-api timeout rate 18 percent")
	require.Contains(t, judgeRequest, "final response contains a populated diagnosis supported by the frozen timeout and customer-impact evidence")
}

func TestRealStackJudge_ContradictoryHighScoreIsUnavailable(t *testing.T) {
	overclaim := strings.Replace(judgeDiagnosis, "Moderate; the available evidence is incomplete.", "High. The failing dependency establishes a clear causal link.", 1)
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, scenarioID: judgeScenarioID, suffix: "judge-contradiction",
		replies: []fixtures.OllamaReply{{Content: overclaim}, {Content: `{"score":5,"rationale":"Appropriately qualified uncertainty."}`}},
	})
	res := env.executeNext(t)
	grade := diagnosisQualityGrade(res)
	require.NotNil(t, grade)
	require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE, grade.GetStatus())
	require.Contains(t, grade.GetDetail(), "contradicts")
	require.Contains(t, grade.GetDetail(), "5/5")
	require.NotNil(t, grade.GetGraderCallRef(), "retain the judge's verified receipt and raw assessment")
	for _, score := range res.GetDecomposedScores() {
		require.NotEqual(t, "diagnosis_quality", score.GetDimension())
	}
	require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, evaluation.DerivePublicSummaryStatus(res))
}
