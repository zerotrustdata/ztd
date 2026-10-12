// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type directFormationFixture struct {
	req      AssignmentExecutionRequest
	result   *evalv1.EvaluationAssignmentResult
	evidence *FormationRunEvidence
}

// newDirectFormationFixture builds a completed formation whose three roles
// ran through the direct formation runner, grades it on the write path, and round-trips its formation
// run evidence through the store exactly as the verifier will read it.
func newDirectFormationFixture(t *testing.T) directFormationFixture {
	t.Helper()
	variants := testHeterogeneousVariants()
	stack := mustHeterogeneousStack(t)
	formation, err := BindHeterogeneousStack(FormationBindingRequest{Stack: stack, Variants: variants})
	require.NoError(t, err)
	_, artifacts, err := BuildScenarioCatalog()
	require.NoError(t, err)
	req := heterogeneousAssignmentExecutionRequest(t, stack, variants)
	require.NoError(t, json.Unmarshal(artifacts["instruction-exact-format"].Gold.Body, &req.ScenarioGold))

	formationResult := &FormationRunResult{
		FormationID: formation.ID,
		Passed:      true,
		Roles: []FormationRoleTelemetry{
			{Role: FormationRoleLite, Model: formation.Lite, ProviderAttemptID: "lite-attempt"},
			{Role: FormationRoleAssistant, Model: formation.Assistant, ProviderAttemptID: "assistant-attempt"},
			{Role: FormationRolePrimary, Model: formation.Primary, ProviderAttemptID: "primary-attempt"},
		},
	}
	result, err := ImportAssignmentResultFromFormationRun(req, formationResult, time.Unix(1_700_000_000, 0).UTC(), func(prefix string) string { return prefix + "-1" })
	require.NoError(t, err)

	runContext := FormationRunContext{
		CampaignID:          req.Assignment.GetCampaignId(),
		RunID:               req.Assignment.GetRunId(),
		AssignmentID:        req.Assignment.GetAssignmentId(),
		EvaluationAttemptID: req.AttemptID,
		ScenarioID:          req.Assignment.GetScenarioId(),
		ModelRegistryDigest: req.Binding.ModelRegistryDigest,
		InferenceSessionID:  req.Binding.InferenceOperatorSessionID,
		DataSessionID:       req.Binding.DataOperatorSessionID,
	}
	body, _, err := BuildFormationRunEvidence(req, runContext, formationResult)
	require.NoError(t, err)
	store := NewStore(newCampaignMemoryFileService())
	require.NoError(t, store.SaveAssignmentFormationRun(context.Background(), req.Assignment.GetRunId(), req.Assignment.GetAssignmentId(), body))
	evidence, err := store.LoadAssignmentFormationRun(context.Background(), req.Assignment.GetRunId(), req.Assignment.GetAssignmentId())
	require.NoError(t, err)
	return directFormationFixture{req: req, result: result, evidence: evidence}
}

func (f directFormationFixture) verify(t *testing.T, result *evalv1.EvaluationAssignmentResult, evidence *FormationRunEvidence) *evalv1.EvaluationVerificationReport {
	t.Helper()
	report, err := NewCampaignAssignmentVerifier(func() time.Time { return time.Unix(1_700_000_100, 0) }).Verify(context.Background(), CampaignAssignmentVerificationRequest{
		Assignment:           f.req.Assignment,
		Result:               result,
		ScenarioInput:        f.req.ScenarioInput,
		ScenarioGold:         f.req.ScenarioGold,
		ScenarioTools:        f.req.ScenarioTools,
		GradingMethod:        f.req.GradingMethod,
		FormationRunEvidence: evidence,
	})
	require.NoError(t, err)
	return report
}

func TestCampaignAssignmentVerifier_AcceptsDirectFormationResult(t *testing.T) {
	fixture := newDirectFormationFixture(t)

	report := fixture.verify(t, fixture.result, fixture.evidence)

	assert.Empty(t, report.GetFailureReasons())
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus())
}

func TestCampaignAssignmentVerifier_RejectsTamperedFormationGrade(t *testing.T) {
	fixture := newDirectFormationFixture(t)
	tampered := proto.Clone(fixture.result).(*evalv1.EvaluationAssignmentResult)
	// The direct formation runner emits a single deterministic grade,
	// "<assignment>:formation-roles", which the verifier recomputes from evidence.
	grade := findDeterministicGradeByID(tampered.GetDeterministicGrades(), fixture.req.Assignment.GetAssignmentId()+":formation-roles")
	require.NotNil(t, grade)
	grade.Status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	digest, err := ComputeAssignmentResultDigest(tampered)
	require.NoError(t, err)
	tampered.ResultDigest = digest

	report := fixture.verify(t, tampered, fixture.evidence)

	assert.Contains(t, report.GetFailureReasons(), "stored deterministic grades do not match formation recomputation")
}

func TestCampaignAssignmentVerifier_StrictWitnessRejectsDirectFormationWithoutWitnessEvidence(t *testing.T) {
	fixture := newDirectFormationFixture(t)

	report, err := NewCampaignAssignmentVerifier(func() time.Time { return time.Unix(1_700_000_100, 0) }).Verify(context.Background(), CampaignAssignmentVerificationRequest{
		Assignment:                fixture.req.Assignment,
		Result:                    fixture.result,
		ScenarioInput:             fixture.req.ScenarioInput,
		ScenarioGold:              fixture.req.ScenarioGold,
		ScenarioTools:             fixture.req.ScenarioTools,
		GradingMethod:             fixture.req.GradingMethod,
		FormationRunEvidence:      fixture.evidence,
		ProviderObservationPolicy: ProviderObservationPolicyStrict,
		ModelProvenancePolicy:     ModelProvenancePolicyStrict,
	})
	require.NoError(t, err)

	// A completed formation never waives formation witness requirements.
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
	assert.Contains(t, report.GetFailureReasons(), "formation role lite missing observer observation digest")
}
