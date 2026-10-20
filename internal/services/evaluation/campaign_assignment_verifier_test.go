// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// A formation's result needs real governed trace evidence before it can verify.
func TestCampaignAssignmentVerifier_FormationRequiresTrace(t *testing.T) {
	t.Parallel()
	req := heterogeneousAssignmentExecutionRequest(t, mustHeterogeneousStack(t), testHeterogeneousVariants())
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    req.Assignment.GetAssignmentId(),
		RunId:           req.Assignment.GetRunId(),
		CampaignId:      req.Assignment.GetCampaignId(),
		Lane:            req.Assignment.GetLane(),
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
	}
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest

	verifier := NewCampaignAssignmentVerifier(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() })
	report, err := verifier.Verify(context.Background(), CampaignAssignmentVerificationRequest{
		Assignment: req.Assignment,
		Result:     result,
	})
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
	assert.Contains(t, report.GetFailureReasons(), "agent trace is required for homogeneous verification")
}

func TestCampaignAssignmentVerifier_FailsClosedOnModelRoleAssignment(t *testing.T) {
	t.Parallel()
	variants := testHeterogeneousVariants()
	stack := mustHeterogeneousStack(t)
	req := heterogeneousAssignmentExecutionRequest(t, stack, variants)
	modelRole := proto.Clone(req.Assignment).(*evalv1.EvaluationAssignment)
	modelRole.Target = nil
	verifier := NewCampaignAssignmentVerifier(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() })
	report, err := verifier.Verify(context.Background(), CampaignAssignmentVerificationRequest{
		Assignment: modelRole,
		Result:     &evalv1.EvaluationAssignmentResult{AssignmentId: modelRole.GetAssignmentId()},
	})
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
	// A model-role assignment is verified against its persisted agent trace, so
	// a result with no trace is rejected rather than accepted as formation evidence.
	assert.Contains(t, report.GetFailureReasons(), "agent trace is required for homogeneous verification")
}

// storedHomogeneousResult imports a trace and seals the result against the
// persisted trace body, as a stored result is.
func storedHomogeneousResult(t *testing.T, req AssignmentExecutionRequest, trace *AgentTrace) *evalv1.EvaluationAssignmentResult {
	t.Helper()
	body, err := MarshalCanonicalJSONObject(trace)
	require.NoError(t, err)
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	ref, err := BuildAssignmentAgentTraceEvidenceReference(trace.RunID, trace.EvaluationAttemptID, body, now)
	require.NoError(t, err)
	result, err := ImportAssignmentResultFromAgentTrace(req, trace, ref, now, nil)
	require.NoError(t, err)
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	return result
}

// verifyStoredHomogeneous verifies a stored result against its persisted trace
// under the given catalog reference.
func verifyStoredHomogeneous(t *testing.T, req AssignmentExecutionRequest, trace *AgentTrace, result *evalv1.EvaluationAssignmentResult, catalog *compliancev1.VersionedReference) *evalv1.EvaluationVerificationReport {
	t.Helper()
	verifier := NewCampaignAssignmentVerifier(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() })
	report, err := verifier.Verify(context.Background(), CampaignAssignmentVerificationRequest{
		Assignment:    req.Assignment,
		Result:        result,
		ScenarioInput: req.ScenarioInput,
		ScenarioGold:  req.ScenarioGold,
		ScenarioTools: req.ScenarioTools,
		GradingMethod: req.GradingMethod,
		AgentTrace:    trace,
		CatalogRef:    catalog,
	})
	require.NoError(t, err)
	return report
}

// A stored INVALID_EVIDENCE that the grader's evidence verdict produced must
// verify: the verifier recomputes the same verdict, under the current catalog
// and under the legacy one, where grades are not recomputed.
func TestCampaignAssignmentVerifier_AcceptsStoredInvalidEvidenceFromGraderVerdict(t *testing.T) {
	triage := &TriageExpectation{
		Complexity: []constants.TriageComplexity{constants.TriageComplexitySimple},
		Intent:     []constants.TriageIntent{constants.TriageIntentAction},
		Posture:    []constants.TriagePosture{constants.TriagePostureNormal},
	}
	trace := verifiedGradingTrace(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
	req := verifiedGradingRequest(trace, &ScenarioPlayerExpectations{
		Triage:  triage,
		Marshal: &MarshalExpectation{Risk: []MarshalRisk{""}},
	})
	result := storedHomogeneousResult(t, req, trace)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, result.GetLifecycleStatus(), "premise: the grader verdict makes the import INVALID_EVIDENCE")
	require.Empty(t, result.GetDeterministicGrades())

	for _, catalog := range []*compliancev1.VersionedReference{nil, {Id: LegacyDefaultSuiteID, Version: "1.0.0"}} {
		report := verifyStoredHomogeneous(t, req, trace, result, catalog)
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), "%v", report.GetFailureReasons())
	}
}

// A stored INVALID_EVIDENCE from the live schema pin must verify offline.
func TestCampaignAssignmentVerifier_AcceptsStoredInvalidEvidenceFromSchemaPin(t *testing.T) {
	triage := &TriageExpectation{
		Complexity: []constants.TriageComplexity{constants.TriageComplexitySimple},
		Intent:     []constants.TriageIntent{constants.TriageIntentAction},
		Posture:    []constants.TriagePosture{constants.TriagePostureNormal},
	}
	trace := verifiedGradingTrace(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
	req := verifiedGradingRequest(trace, &ScenarioPlayerExpectations{Triage: triage})
	relabelAgentTrace(t, trace, agentTraceSchemaVersionV6)
	result := storedHomogeneousResult(t, req, trace)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, result.GetLifecycleStatus(), "premise: the live pin makes the import INVALID_EVIDENCE")

	report := verifyStoredHomogeneous(t, req, trace, result, nil)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), "%v", report.GetFailureReasons())
}

// A campaign assignment whose v7 trace was relabelled to an earlier version
// cannot be stored COMPLETED: the verifier applies the schema pin, so the
// COMPLETED lifecycle is flagged.
func TestCampaignAssignmentVerifier_FlagsStoredCompletedForRelabelledEarlierVersion(t *testing.T) {
	triage := &TriageExpectation{
		Complexity: []constants.TriageComplexity{constants.TriageComplexitySimple},
		Intent:     []constants.TriageIntent{constants.TriageIntentAction},
		Posture:    []constants.TriagePosture{constants.TriagePostureNormal},
	}
	trace := verifiedGradingTrace(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
	req := verifiedGradingRequest(trace, &ScenarioPlayerExpectations{Triage: triage})
	// The v7 trace imports COMPLETED while it is the sealed version.
	result := storedHomogeneousResult(t, req, trace)
	completed := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	require.Equal(t, completed, result.GetLifecycleStatus(), "premise: the v7 trace imports COMPLETED")

	// Relabel the persisted trace and re-bind the stored result to its relabelled body,
	// as a stored result is sealed against the persisted body.
	relabelAgentTrace(t, trace, agentTraceSchemaVersionV6)
	body, err := MarshalCanonicalJSONObject(trace)
	require.NoError(t, err)
	ref, err := BuildAssignmentAgentTraceEvidenceReference(trace.RunID, trace.EvaluationAttemptID, body, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	result.EvidenceRefs = []*compliancev1.ComplianceEvidenceReference{ref}
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest

	report := verifyStoredHomogeneous(t, req, trace, result, nil)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
	assert.Contains(t, report.GetFailureReasons(), "stored lifecycle "+completed.String()+" does not match agent trace lifecycle "+evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE.String())
}
