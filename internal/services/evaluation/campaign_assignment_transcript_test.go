// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

package evaluation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestAssignmentPublicationIncludesPersistedAgentTranscript(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)
	controller := NewCampaignController(store, &stubCampaignExecutor{}, nil, nil)
	req := testCampaignInitRequest(t)
	run, err := controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(ctx, run.GetRunId())
	require.NoError(t, err)
	assignments, err := store.ListAssignments(ctx, run.GetRunId())
	require.NoError(t, err)
	assignment := assignments[0]
	trace := &AgentTrace{RunID: run.GetRunId(), AssignmentID: assignment.GetAssignmentId(), EvaluationAttemptID: "attempt-1", DesignatedRole: "primary", Input: testAgentTraceInput(), FinalOutput: "Checkout failures are caused by upstream payment timeouts.", ModelCalls: []AgentModelCall{{Purpose: agentModelCallPurposeTurn, LoopTurn: 1, Text: "I will inspect the evidence.", FinishReason: "tool_call"}, {Purpose: agentModelCallPurposeTurn, LoopTurn: 2, Text: "Checkout failures are caused by upstream payment timeouts.", FinishReason: "stop"}}, ToolCalls: []AgentToolCall{{ToolName: "inspect", ArgumentsJSON: `{"target":"checkout"}`, ResultJSON: `{"timeout_rate":18}`, Success: true, LoopTurn: 1}}}
	body, err := SealAgentTrace(trace)
	require.NoError(t, err)
	require.NoError(t, store.SaveAssignmentAgentTrace(ctx, run.GetRunId(), assignment.GetAssignmentId(), body))
	result := &evalv1.EvaluationAssignmentResult{RunId: run.GetRunId(), AssignmentId: assignment.GetAssignmentId(), LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED}
	category, err := ScenarioCategoryForAssignment(req.Catalog, assignment)
	require.NoError(t, err)
	request, err := coordinator.buildAssignmentResultPublishRequest(ctx, run, req.Catalog, req.ScenarioArtifacts, assignment, result, category, "unverified", "transcript-regression", nil)
	require.NoError(t, err)
	var envelope struct {
		Record struct {
			Transcripts []struct {
				Response  string `json:"response"`
				ToolCalls []struct {
					Result string `json:"result_json"`
				} `json:"tool_calls"`
			} `json:"role_transcripts"`
		} `json:"record"`
	}
	require.NoError(t, json.Unmarshal(request.Body, &envelope))
	require.Len(t, envelope.Record.Transcripts, 1, "saved responses and tool results must reach the public assignment record")
	require.Equal(t, trace.FinalOutput, envelope.Record.Transcripts[0].Response)
	require.Equal(t, trace.ToolCalls[0].ResultJSON, envelope.Record.Transcripts[0].ToolCalls[0].Result)
	release, err := store.LoadCampaignRelease(ctx, assignment.GetCampaignId())
	require.NoError(t, err)
	exported, err := NewCampaignExporter(nil).buildAssignmentExportRecord(ctx, store, run, req.Catalog, assignment, result, category, nil, false, nil, release)
	require.NoError(t, err)
	encoded, err := json.Marshal(exported)
	require.NoError(t, err)
	var roundTrip CampaignExportAssignmentRecord
	require.NoError(t, json.Unmarshal(encoded, &roundTrip))
	require.Len(t, roundTrip.RoleTranscripts, 1)
	require.Equal(t, trace.FinalOutput, roundTrip.RoleTranscripts[0].Response)
	require.Equal(t, trace.ToolCalls[0].ResultJSON, roundTrip.RoleTranscripts[0].ToolCalls[0].ResultJSON)
}

func TestPublicAssignmentTranscriptFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		tamper         bool
		want           error
	}{
		{name: "oversized", response: strings.Repeat("a", 256*1024), want: constants.ErrEvidenceArtifactTooLarge},
		{name: "tampered", response: "captured response", tamper: true, want: constants.ErrChecksumMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := NewStore(newCampaignRuntimeFileService(t))
			assignment := &evalv1.EvaluationAssignment{RunId: "run-transcript", AssignmentId: "assignment-transcript"}
			result := &evalv1.EvaluationAssignmentResult{RunId: assignment.GetRunId(), AssignmentId: assignment.GetAssignmentId()}
			trace := &AgentTrace{RunID: assignment.GetRunId(), AssignmentID: assignment.GetAssignmentId(), EvaluationAttemptID: "attempt-transcript", DesignatedRole: "primary", FinalOutput: tc.response, Input: testAgentTraceInput()}
			body, err := SealAgentTrace(trace)
			require.NoError(t, err)
			if tc.tamper {
				trace.FinalOutput = "modified response"
				body, err = MarshalCanonicalJSONObject(trace)
				require.NoError(t, err)
			}
			require.NoError(t, store.SaveAssignmentAgentTrace(ctx, assignment.GetRunId(), assignment.GetAssignmentId(), body))
			transcripts, err := BuildPublicAssignmentTranscripts(ctx, store, assignment, result)
			require.ErrorIs(t, err, tc.want)
			require.Nil(t, transcripts)
		})
	}
}

func TestPublicAssignmentTranscriptPublishesModelAndToolTextVerbatim(t *testing.T) {
	ctx := context.Background()
	store := NewStore(newCampaignRuntimeFileService(t))
	assignment := &evalv1.EvaluationAssignment{RunId: "run-transcript", AssignmentId: "assignment-transcript"}
	result := &evalv1.EvaluationAssignmentResult{RunId: assignment.GetRunId(), AssignmentId: assignment.GetAssignmentId()}
	response := "The workload identity is spiffe://ztd.local/operator/x."
	toolResult := `{"stdout":"-----BEGIN PRIVATE KEY-----\nMIIfixture\n-----END PRIVATE KEY-----"}`
	trace := &AgentTrace{RunID: assignment.GetRunId(), AssignmentID: assignment.GetAssignmentId(), EvaluationAttemptID: "attempt-transcript", DesignatedRole: "primary", Input: testAgentTraceInput(), FinalOutput: response, ToolCalls: []AgentToolCall{{ToolName: "inspect", ArgumentsJSON: `{"path":"key.pem"}`, ResultJSON: toolResult, Success: true, LoopTurn: 1}}}
	body, err := SealAgentTrace(trace)
	require.NoError(t, err)
	require.NoError(t, store.SaveAssignmentAgentTrace(ctx, assignment.GetRunId(), assignment.GetAssignmentId(), body))

	transcripts, err := BuildPublicAssignmentTranscripts(ctx, store, assignment, result)

	require.NoError(t, err)
	require.Len(t, transcripts, 1)
	require.Equal(t, response, transcripts[0].Response)
	require.Equal(t, toolResult, transcripts[0].ToolCalls[0].ResultJSON)
}

func TestPublicAssignmentTranscriptDistinguishesMissingSourceFromMissingReferencedEvidence(t *testing.T) {
	ctx := context.Background()
	store := NewStore(newCampaignRuntimeFileService(t))
	assignment := &evalv1.EvaluationAssignment{RunId: "run-transcript", AssignmentId: "assignment-transcript"}
	result := &evalv1.EvaluationAssignmentResult{RunId: assignment.GetRunId(), AssignmentId: assignment.GetAssignmentId()}
	transcripts, err := BuildPublicAssignmentTranscripts(ctx, store, assignment, result)
	require.NoError(t, err)
	require.Empty(t, transcripts)
	result.EvidenceRefs = []*compliancev1.ComplianceEvidenceReference{{ArtifactType: string(complianceevidence.ArtifactTypeEvaluationAssignmentTrace)}}
	_, err = BuildPublicAssignmentTranscripts(ctx, store, assignment, result)
	require.ErrorIs(t, err, constants.ErrNotFound)
}
