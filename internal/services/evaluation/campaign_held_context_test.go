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

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// heldRepetitionFixture runs a cell's first repetition and returns its sealed
// trace together with a fixture for the second repetition that holds it.
func heldRepetitionFixture(t *testing.T) (*AgentTrace, *agentExecutorFixture) {
	t.Helper()
	first := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	first.req.Assignment.Repetition = 1
	_, err := first.execute()
	require.NoError(t, err)
	previous := first.storedTrace()

	second := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	second.req.Assignment.AssignmentId = "assignment-2"
	second.req.Assignment.Repetition = 2
	second.req.AttemptID = "attempt-2"
	second.req.HeldContext = previous
	return previous, second
}

func TestCampaignAgentExecutor_HeldRepetitionContinuesThePreviousInvestigation(t *testing.T) {
	previous, f := heldRepetitionFixture(t)

	result, err := f.execute()

	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
	held, err := previous.Conversation()
	require.NoError(t, err)
	require.Len(t, held, len(previous.Input.Messages)+3, "input, the tool turn's assistant and tool messages, then the final answer")
	first := f.inference.requests[0].GetMessages()
	require.Len(t, first, len(held)+1, "the model receives the whole held investigation, then the scenario prompt once more")
	assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, first[0].GetRole())
	assert.Equal(t, previous.Input.Messages[0].Text, first[0].GetParts()[0].GetText(), "the held system context is the previous repetition's own")
	assert.Equal(t, "HEALTHY", first[len(held)-1].GetParts()[0].GetText(), "the previous final answer precedes the new prompt")
	prompt := first[len(held)]
	assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, prompt.GetRole())
	assert.Contains(t, prompt.GetParts()[0].GetText(), previous.Workspace.Root+"/status/health.txt", "the prompt renders against the held workspace")

	trace := f.storedTrace()
	assert.Equal(t, agentTraceSchemaVersion, trace.SchemaVersion)
	require.NotNil(t, trace.HeldContext)
	assert.Equal(t, previous.AssignmentID, trace.HeldContext.AssignmentID)
	assert.Equal(t, uint32(1), trace.HeldContext.Repetition)
	assert.Equal(t, previous.EvidenceDigest, trace.HeldContext.EvidenceDigest)
	assert.Equal(t, held, trace.HeldContext.Messages, "the held conversation is recorded verbatim")
	assert.Equal(t, previous.Workspace, trace.Workspace, "the repetition continues in the previous workspace")
	assert.Equal(t, previous.Workspace, trace.HeldContext.Workspace)
	require.Len(t, trace.Input.Messages, 1, "Input holds only what this repetition added")
	sentinel := previous.ScenarioWorkspace().SentinelPath()
	for _, request := range f.client.requests {
		if request.EventType == string(constants.EventOperatorFileEditRequested) && request.TargetResource != sentinel {
			assert.True(t, strings.HasPrefix(request.TargetResource, previous.Workspace.Root+"/"), "fixtures are restored in the held workspace")
		}
	}

	chained, err := trace.Conversation()
	require.NoError(t, err)
	assert.Equal(t, held, chained[:len(held)], "a later repetition chains through this one's held context")
	assert.Len(t, chained, len(held)+4)
}

func TestCampaignAgentExecutor_HeldRepetitionOnAReboundDataOperatorIsAHarnessFailure(t *testing.T) {
	previous, f := heldRepetitionFixture(t)
	previous.Data.OperatorID = "operator-elsewhere"

	_, err := f.execute()

	require.ErrorIs(t, err, constants.ErrEvaluationHeldContextUnavailable)
	assert.True(t, isRecoverableAssignmentExecutionError(err), "the repetition records a harness failure instead of silently starting fresh")
	assert.Empty(t, f.inference.requests, "no inference runs without the held investigation")
}

func TestImportAssignmentResultFromAgentTrace_HeldTraceMustRunInTheHeldWorkspace(t *testing.T) {
	_, f := heldRepetitionFixture(t)
	_, err := f.execute()
	require.NoError(t, err)
	trace := f.storedTrace()
	require.NoError(t, trace.validateWorkspace())

	own, err := NewScenarioWorkspace(agentTestWorkingDirectory, trace.RunID, trace.EvaluationAttemptID)
	require.NoError(t, err)
	trace.Workspace.Root = own.Root
	require.ErrorIs(t, trace.validateWorkspace(), constants.ErrEvaluationWorkspaceUnavailable)

	trace.Workspace = AgentTraceWorkspace{Root: agentTestWorkingDirectory + "/elsewhere", OperatorWorkingDirectory: agentTestWorkingDirectory}
	trace.HeldContext.Workspace = trace.Workspace
	require.ErrorIs(t, trace.validateWorkspace(), constants.ErrEvaluationWorkspaceUnavailable, "a held workspace must still be an attempt-scoped root")
}

func TestAgentTraceConversation_ToolCallCountMismatchIsMalformed(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	_, err := f.execute()
	require.NoError(t, err)
	trace := f.storedTrace()
	trace.ModelCalls[0].ToolCallCount = 2

	_, err = trace.Conversation()

	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}

func TestCampaignController_HeldAssignmentContextLoadsThePreviousRepetition(t *testing.T) {
	ctx := context.Background()
	store := NewStore(newCampaignMemoryFileService())
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	catalog, _, err := LoadScenarioCatalog()
	require.NoError(t, err)
	inventory, err := MaterializeModelRegistry(agentTestCampaignID, []*evalv1.ModelVariant{testModelVariant()})
	require.NoError(t, err)
	spec, err := MaterializeCampaignSpec(agentTestCampaignID, catalog, inventory, 2, true, testPlatform)
	require.NoError(t, err)
	require.NoError(t, store.SaveCampaignSpec(ctx, spec))

	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	assignment := f.req.Assignment
	assignment.Lane = evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE
	assignment.Repetition = 1
	assignment.AssignmentId, err = ComputeAssignmentDeterministicIdentity(assignment)
	require.NoError(t, err)
	next := &evalv1.EvaluationAssignment{
		RunId: assignment.GetRunId(), CampaignId: assignment.GetCampaignId(), ScenarioId: assignment.GetScenarioId(),
		Lane: assignment.GetLane(), Repetition: 2, Target: assignment.GetTarget(),
	}

	first, err := controller.heldAssignmentContext(ctx, agentTestCampaignID, assignment)
	require.NoError(t, err)
	assert.Nil(t, first, "a cell's first repetition starts fresh")

	_, err = controller.heldAssignmentContext(ctx, agentTestCampaignID, next)
	require.ErrorIs(t, err, constants.ErrEvaluationHeldContextUnavailable, "a previous repetition without a trace cannot be continued")
	assert.True(t, isRecoverableAssignmentExecutionError(err))

	_, err = f.execute()
	require.NoError(t, err)
	require.NoError(t, store.SaveAssignmentAgentTrace(ctx, assignment.GetRunId(), assignment.GetAssignmentId(), f.traces.body))
	held, err := controller.heldAssignmentContext(ctx, agentTestCampaignID, next)
	require.NoError(t, err)
	require.NotNil(t, held)
	assert.Equal(t, assignment.GetAssignmentId(), held.AssignmentID)
}
