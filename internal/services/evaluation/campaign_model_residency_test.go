// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// residencyStep is one recorded executor side effect, in call order.
type residencyStep struct {
	kind   string
	retain []string
}

// recordingModelResidency records every release the executor requests and
// answers each with err.
type recordingModelResidency struct {
	steps    *[]residencyStep
	releases []ModelResidencyRelease
	err      error
}

func (r *recordingModelResidency) ReleaseAllExcept(_ context.Context, release ModelResidencyRelease) ([]string, error) {
	r.releases = append(r.releases, release)
	if r.steps != nil {
		*r.steps = append(*r.steps, residencyStep{kind: "release", retain: release.Retain})
	}
	return nil, r.err
}

// failingWorkspaceWriter records the first workspace write and fails it, so an
// assignment stops right after the residency step that precedes the write.
type failingWorkspaceWriter struct {
	AgentToolDispatcher
	steps *[]residencyStep
}

func (w *failingWorkspaceWriter) WriteWorkspaceFile(context.Context, Target, string, string, string, string, string) error {
	*w.steps = append(*w.steps, residencyStep{kind: "write"})
	return errors.New("test: workspace unavailable")
}

type unusedSentinel struct{}

func (unusedSentinel) Read(context.Context, cases.Executor, string) (*operatorv1.EventReceipt, error) {
	return nil, errors.New("test: sentinel must not be read")
}

type unusedTraceStore struct{}

func (unusedTraceStore) SaveAssignmentAgentTrace(context.Context, string, string, []byte) error {
	return errors.New("test: trace must not be saved")
}

func residencyTestJudge() *evalv1.ModelVariant {
	return &evalv1.ModelVariant{
		VariantId: "judge", ProviderClass: constants.InferenceProviderOllama, ServedModelTag: DefaultJudgeServedModelTag,
		ModelDigest: "abababababababababababababababababababababababababababababababab",
	}
}

// formationTags is the sorted, de-duplicated served tags of the request's
// formation slots, resolved through its frozen variants.
func formationTags(req AssignmentExecutionRequest) []string {
	stack := req.Assignment.GetHeterogeneous().GetStack()
	tags := []string{}
	for _, slot := range []*evalv1.RoleAssignment{stack.GetPrimarySlot(), stack.GetAssistantSlot(), stack.GetLiteSlot()} {
		for _, variant := range req.Binding.ModelVariants {
			if variant.GetVariantId() == slot.GetVariantId() {
				tags = append(tags, variant.GetServedModelTag())
			}
		}
	}
	slices.Sort(tags)
	return slices.Compact(tags)
}

func TestNewCampaignAgentExecutor_RequiresModelResidency(t *testing.T) {
	newID := func(prefix string) string { return prefix }
	inference := &inferenceUnavailable{}
	tools := NewCommandLane(nil, client.Persona{})

	_, err := NewCampaignAgentExecutor(inference, nil, tools, unusedSentinel{}, unusedTraceStore{}, time.Now, newID)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField, "the provider must never silently hold two phases' models")

	executor, err := NewCampaignAgentExecutor(inference, &recordingModelResidency{}, tools, unusedSentinel{}, unusedTraceStore{}, time.Now, newID)
	require.NoError(t, err)
	assert.NotNil(t, executor)
}

func TestAgentRunModelTags_KeepsEveryFormationSlotOnce(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	executor := &CampaignAgentExecutor{catalog: catalog, newID: func(prefix string) string { return prefix + "-test" }}
	req := heterogeneousLiteRequest(t)

	run, err := executor.newRun(req)
	require.NoError(t, err)

	assert.Equal(t, formationTags(req), run.modelTags())
}

func TestCampaignAgentExecutor_ReleasesOtherModelsBeforeTheAssignmentOpens(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	var steps []residencyStep
	residency := &recordingModelResidency{steps: &steps}
	executor := &CampaignAgentExecutor{
		inference: &inferenceUnavailable{}, residency: residency, tools: &failingWorkspaceWriter{steps: &steps},
		sentinel: unusedSentinel{}, traceStore: unusedTraceStore{}, catalog: catalog, now: time.Now,
		newID: func(prefix string) string { return prefix + "-test" },
	}
	req := heterogeneousLiteRequest(t)

	_, err = executor.ExecuteAssignment(t.Context(), req)

	require.ErrorIs(t, err, constants.ErrEvaluationAssignmentExecutionFailed)
	require.Equal(t, []residencyStep{
		{kind: "release", retain: formationTags(req)},
		{kind: "write"},
	}, steps, "the provider is set for the assignment before its ledger sentinel is written")
	require.Len(t, residency.releases, 1)
	assert.Equal(t, req.Assignment.GetRunId(), residency.releases[0].RunID)
}

func TestCampaignAgentExecutor_ReleaseFailureStopsTheAssignmentBeforeAnyDispatch(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	var steps []residencyStep
	inference := &inferenceUnavailable{}
	residency := &recordingModelResidency{steps: &steps, err: constants.ErrInferenceModelReleaseFailed}
	executor := &CampaignAgentExecutor{
		inference: inference, residency: residency, tools: &failingWorkspaceWriter{steps: &steps},
		sentinel: unusedSentinel{}, traceStore: unusedTraceStore{}, catalog: catalog, now: time.Now,
		newID: func(prefix string) string { return prefix + "-test" },
	}

	_, err = executor.ExecuteAssignment(t.Context(), heterogeneousLiteRequest(t))

	require.ErrorIs(t, err, constants.ErrEvaluationAssignmentExecutionFailed, "a residency failure is a harness failure, never graded against the model")
	require.ErrorIs(t, err, constants.ErrInferenceModelReleaseFailed)
	assert.Len(t, steps, 1, "nothing is written after a failed release")
	assert.Zero(t, inference.dispatched)
}

func TestCampaignAgentExecutor_ToolCallingProbeReleasesOtherModelsFirst(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	inference := &inferenceUnavailable{}
	residency := &recordingModelResidency{err: constants.ErrInferenceModelReleaseFailed}
	executor := &CampaignAgentExecutor{inference: inference, residency: residency, catalog: catalog, newID: func(prefix string) string { return prefix + "-test" }}
	req := heterogeneousLiteRequest(t)

	_, err = executor.ProbeToolCalling(t.Context(), req)

	require.ErrorIs(t, err, constants.ErrInferenceModelReleaseFailed)
	require.ErrorIs(t, err, constants.ErrEvaluationToolCallingProbeFailed)
	assert.NotErrorIs(t, err, constants.ErrEvaluationAssignmentExecutionFailed, "a failed probe release stops the run; it is never recoverable")
	require.Len(t, residency.releases, 1)
	assert.Equal(t, formationTags(req), residency.releases[0].Retain)
	assert.Zero(t, inference.dispatched, "the probe never dispatches while other models hold the provider")
}

func TestCampaignAgentExecutor_JudgeReleaseRetainsOnlyTheFrozenJudge(t *testing.T) {
	residency := &recordingModelResidency{}
	executor := &CampaignAgentExecutor{residency: residency}
	req := AssignmentExecutionRequest{
		Assignment: &evalv1.EvaluationAssignment{RunId: "run-1"},
		Binding:    CampaignExecutionBinding{JudgeVariant: residencyTestJudge()},
	}

	require.NoError(t, executor.releaseAllExceptJudge(t.Context(), req))

	assert.Equal(t, []ModelResidencyRelease{{RunID: "run-1", Retain: []string{DefaultJudgeServedModelTag}}}, residency.releases)
}

func TestCampaignAgentExecutor_JudgeReleaseFailsClosed(t *testing.T) {
	t.Run("without a frozen judge", func(t *testing.T) {
		residency := &recordingModelResidency{}
		executor := &CampaignAgentExecutor{residency: residency}

		err := executor.releaseAllExceptJudge(t.Context(), AssignmentExecutionRequest{Assignment: &evalv1.EvaluationAssignment{RunId: "run-1"}})

		require.ErrorIs(t, err, constants.ErrEvaluationCampaignJudgeInvalid)
		assert.Empty(t, residency.releases, "no release is sent for a judge that cannot be named")
	})
	t.Run("when the release fails", func(t *testing.T) {
		executor := &CampaignAgentExecutor{residency: &recordingModelResidency{err: constants.ErrEvaluationDispatchFailed}}
		req := AssignmentExecutionRequest{
			Assignment: &evalv1.EvaluationAssignment{RunId: "run-1"},
			Binding:    CampaignExecutionBinding{JudgeVariant: residencyTestJudge()},
		}

		err := executor.releaseAllExceptJudge(t.Context(), req)

		require.ErrorIs(t, err, constants.ErrEvaluationAssignmentExecutionFailed)
		require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
	})
}

func TestCampaignAgentExecutor_QualityJudgeRequiredMatchesScoreWithJudge(t *testing.T) {
	executor := &CampaignAgentExecutor{}
	semantic := AssignmentExecutionRequest{GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_SEMANTIC_JUDGE}
	deterministic := AssignmentExecutionRequest{GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC}
	rubric := AssignmentExecutionRequest{
		GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC,
		ScenarioGold:  ScenarioGoldCriteria{ContentCheck: &ScenarioContentCheck{DiagnosisRubric: "checkout-timeout"}},
	}

	assert.True(t, executor.qualityJudgeRequired(semantic, &AgentTrace{FinalOutput: "answer"}))
	assert.True(t, executor.qualityJudgeRequired(rubric, &AgentTrace{FinalOutput: "answer"}))
	assert.False(t, executor.qualityJudgeRequired(deterministic, &AgentTrace{FinalOutput: "answer"}), "a deterministic cell never loads the judge")
}
