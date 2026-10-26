// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

type tribunalAuthoringInference struct {
	intentCalls int
	probeCalls  int
	intent      *tribunalv1.TribunalIntentRequest
	result      *tribunalv1.TribunalIntentResult
	probeErr    error
}

func (f *tribunalAuthoringInference) DispatchInference(context.Context, *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	f.probeCalls++
	return nil, f.probeErr
}

func (f *tribunalAuthoringInference) DispatchIntent(_ context.Context, req *tribunalv1.TribunalIntentRequest) (*tribunalv1.TribunalIntentResult, error) {
	f.intentCalls++
	f.intent = req
	return f.result, nil
}

type tribunalAuthoringTools struct {
	dispatches int
	action     constants.ActionType
	resource   string
	payload    []byte
	outcome    *client.GovernedOutcome
}

func (f *tribunalAuthoringTools) WriteWorkspaceFile(context.Context, Target, string, string, string, string, string) error {
	return nil
}

func (f *tribunalAuthoringTools) DispatchAgentTool(_ context.Context, _ Target, _, _, _, _ string, action constants.ActionType, resource string, payload []byte) (GovernedToolDispatch, error) {
	f.dispatches++
	f.action, f.resource, f.payload = action, resource, append([]byte(nil), payload...)
	receipt := f.outcome.Observation.GetReceipt()
	return GovernedToolDispatch{
		Outcome: f.outcome, TransactionID: receipt.GetTransactionId(),
		ResultPayload: f.outcome.Dispatch.GetResultPayload(),
	}, nil
}

func newTribunalAuthoringRun(t *testing.T, useTribunal bool, inference *tribunalAuthoringInference, tools *tribunalAuthoringTools) *agentRun {
	t.Helper()
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	scope, err := catalog.Scope(ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolRunShellCommand}, TribunalCommands: useTribunal})
	require.NoError(t, err)
	assignment := &evalv1.EvaluationAssignment{
		RunId: "run-1", AssignmentId: "assignment-1", CampaignId: "campaign-1", ScenarioId: "scenario-1",
	}
	variant := &evalv1.ModelVariant{VariantId: "variant-1", ServedModelTag: "model:tag", ModelDigest: "digest"}
	return &agentRun{
		executor: &CampaignAgentExecutor{
			inference: inference, tools: tools,
			newID: func(prefix string) string { return "id-" + prefix },
		},
		req: AssignmentExecutionRequest{
			Assignment: assignment, AttemptID: "attempt-1",
			ScenarioTools: ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolRunShellCommand}, TribunalCommands: useTribunal},
		},
		designated: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
		variants:   map[evalv1.ModelCampaignRole]*evalv1.ModelVariant{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY: variant},
		registry:   nil,
		workspace:  ScenarioWorkspace{OperatorWorkingDirectory: "/operator/workspace"},
		tools:      scope,
		target:     Target{OperatorID: "operator-1", SessionID: "operator-session-1"},
		trace:      &AgentTrace{RunID: "run-1", AssignmentID: "assignment-1", ScenarioID: "scenario-1", EvaluationAttemptID: "attempt-1", InvestigationID: "investigation-1", ModelRegistryDigest: "", Inference: AgentTraceRuntime{SessionID: "inference-session-1"}},
	}
}

func TestRecordMemberCallsClassifiesOnlyReceiptBackedL2DenialAsPolicyOutcome(t *testing.T) {
	receipt := &operatorv1.EventReceipt{
		TransactionId: "tx-denied",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:   operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		ActionType:    string(constants.ActionTypeInference),
		RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET,
		L2Status:      operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		SignerKeyId:   "operator-key",
		Signature:     "signed-receipt",
	}
	member := &tribunalv1.TribunalMemberCall{
		ProviderAttemptId: "attempt-member",
		Receipt:           receipt,
		Failure: &operatorv1.InferenceDispatchFailure{
			Code: operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_GOVERNANCE_REJECTED,
		},
	}
	newRun := func() *agentRun {
		return newTribunalAuthoringRun(t, true, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
	}

	t.Run("signed L2 rejection is retained as governance outcome", func(t *testing.T) {
		run := newRun()
		failure := run.recordMemberCalls(AgentToolCall{CallID: "tool-1"}, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, []*tribunalv1.TribunalMemberCall{member})
		assert.Equal(t, tribunalGovernanceRejected, failure)
		assert.Empty(t, run.trace.ProviderFailure)
		assert.Empty(t, run.trace.ProviderFailureClass)
		require.Len(t, run.trace.ModelCalls, 1)
		assert.Equal(t, modelCallGovernanceRejected, run.trace.ModelCalls[0].Error)
		assert.Equal(t, "tx-denied", run.trace.ModelCalls[0].TransactionID)
		require.NotNil(t, run.trace.ModelCalls[0].Receipt)
		assert.Same(t, receipt, run.trace.ModelCalls[0].Receipt.EventReceipt)
	})

	t.Run("missing receipt remains an internal failure", func(t *testing.T) {
		run := newRun()
		withoutReceipt := proto.Clone(member).(*tribunalv1.TribunalMemberCall)
		withoutReceipt.Receipt = nil
		failure := run.recordMemberCalls(AgentToolCall{CallID: "tool-1"}, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, []*tribunalv1.TribunalMemberCall{withoutReceipt})
		assert.Equal(t, string(ExecutionFailureInternal), failure)
		assert.Equal(t, string(ExecutionFailureInternal), run.trace.ProviderFailure)
		assert.Empty(t, run.trace.ProviderFailureClass)
		assert.Empty(t, run.trace.ModelCalls)
	})
}

func TestRecordMemberCallsPreservesProviderToolCallsInTranscript(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("%d tool calls", count), func(t *testing.T) {
			run := newTribunalAuthoringRun(t, true, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
			result := &operatorv1.InferenceResult{
				ProviderAttemptId: "attempt-member", RequestedModel: "model:tag",
				Parts: []*operatorv1.InferenceResponsePart{
					{Part: &operatorv1.InferenceResponsePart_Text{Text: " answer "}},
					{Part: &operatorv1.InferenceResponsePart_Thinking{Thinking: "reasoning"}},
				},
			}
			for i := 0; i < count; i++ {
				result.Parts = append(result.Parts, &operatorv1.InferenceResponsePart{
					Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
						CallId: fmt.Sprintf("call-%d", i), Name: constants.MCPToolRunShellCommand,
						ArgumentsJson: `{"command":"echo","args":["hello"]}`,
					}},
				})
			}
			digest, err := models.ComputeInferenceResultDigest(result)
			require.NoError(t, err)
			result.ResultDigest = digest
			member := &tribunalv1.TribunalMemberCall{
				ProviderAttemptId: result.GetProviderAttemptId(), Result: result,
				Receipt: &operatorv1.EventReceipt{ResultSummary: digest},
			}

			failure := run.recordMemberCalls(AgentToolCall{CallID: "tool-1"}, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, []*tribunalv1.TribunalMemberCall{member})

			require.Empty(t, failure)
			require.Len(t, run.trace.ModelCalls, 1)
			call := run.trace.ModelCalls[0]
			assert.Equal(t, "answer", call.Text)
			assert.Equal(t, "reasoning", call.Thinking)
			assert.Equal(t, count, call.ToolCallCount)
			assert.NoError(t, verifyV7ModelCallTranscript(call, call.InferenceResult.InferenceResult))
		})
	}
}

func TestRunTribunalRetainsVotesAndObservationsOnReviewFailure(t *testing.T) {
	vote := &operatorv1.ConsensusVoteInference{SubjectTransactionId: "command", MemberAppId: "tribunal-axiom"}
	observation := &operatorv1.EventReceipt{TransactionId: "observation"}
	result := &tribunalv1.TribunalIntentResult{
		Outcome:             tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED,
		ConsensusInferences: []*operatorv1.ConsensusVoteInference{vote},
		MemberCalls: []*tribunalv1.TribunalMemberCall{{
			Stage: tribunalv1.TribunalStage_TRIBUNAL_STAGE_AUDIT, ProviderAttemptId: "attempt-member",
			Receipt:             &operatorv1.EventReceipt{TransactionId: "failed-auditor"},
			Failure:             &operatorv1.InferenceDispatchFailure{Code: operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_GENERATE_FAILED},
			ObservationReceipts: []*operatorv1.EventReceipt{observation},
		}},
	}
	run := newTribunalAuthoringRun(t, true, &tribunalAuthoringInference{result: result}, &tribunalAuthoringTools{})
	call, err := run.runTribunal(context.Background(), AgentToolCall{CallID: "tool-1", ToolName: constants.MCPToolRunShellCommand}, models.RunShellCommandRequest{Command: "pwd"})
	require.NoError(t, err)
	require.Len(t, call.ConsensusInferences, 1)
	assert.True(t, proto.Equal(vote, call.ConsensusInferences[0].ConsensusVoteInference))
	require.Len(t, run.trace.ModelCalls, 1)
	require.Len(t, run.trace.ModelCalls[0].ObservationReceipts, 1)
	assert.True(t, proto.Equal(observation, run.trace.ModelCalls[0].ObservationReceipts[0].EventReceipt))
}

func TestRunToolRoutesNativeAndTribunalShellCalls(t *testing.T) {
	const proposal = `{"command":"echo","args":["hello"]}`
	t.Run("native dispatches MCP directly", func(t *testing.T) {
		tools := &tribunalAuthoringTools{outcome: shellToolOutcome(`{"exit_code":0,"stdout":"hello\n"}`)}
		inference := &tribunalAuthoringInference{result: &tribunalv1.TribunalIntentResult{Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED}}
		run := newTribunalAuthoringRun(t, false, inference, tools)
		call, err := run.runTool(context.Background(), 1, "model-call-1", &operatorv1.InferenceToolCall{CallId: "call-1", Name: constants.MCPToolRunShellCommand, ArgumentsJson: proposal})
		require.NoError(t, err)
		assert.True(t, call.Success)
		assert.Equal(t, 1, tools.dispatches)
		assert.Equal(t, constants.ActionTypeMcpCall, tools.action)
		assert.Equal(t, constants.MCPToolRunShellCommand, tools.resource)
		assert.Zero(t, inference.intentCalls)
		assert.Zero(t, inference.probeCalls)
		var payload operatorv1.McpCallRequested
		require.NoError(t, proto.Unmarshal(tools.payload, &payload))
		assert.Equal(t, constants.MCPToolRunShellCommand, payload.GetToolName())
		assert.JSONEq(t, `{"command":"echo","args":["hello"],"working_dir":"/operator/workspace"}`, payload.GetArgumentsJson())
	})

	t.Run("Tribunal uses intent and does not dispatch native MCP", func(t *testing.T) {
		tools := &tribunalAuthoringTools{}
		inference := &tribunalAuthoringInference{result: &tribunalv1.TribunalIntentResult{Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED}}
		run := newTribunalAuthoringRun(t, true, inference, tools)
		call, err := run.runTool(context.Background(), 1, "model-call-1", &operatorv1.InferenceToolCall{CallId: "call-1", Name: constants.MCPToolRunShellCommand, ArgumentsJson: proposal})
		require.NoError(t, err)
		assert.False(t, call.Success)
		assert.Equal(t, 1, inference.intentCalls)
		assert.Equal(t, `{"args":["hello"],"command":"echo"}`, inference.intent.GetRequest(), "the intent carries the proposal in canonical sorted-key form") //nolint:testifylint // exact bytes, not JSON equivalence
		assert.Equal(t, "/operator/workspace", inference.intent.GetWorkingDirectory())
		assert.Equal(t, int32(constants.DefaultShellCommandTimeout), inference.intent.GetTimeoutSeconds())
		assert.Zero(t, tools.dispatches)
		assert.Zero(t, inference.probeCalls)
	})
}

func TestRunToolRejectsHostnamesBeforeTribunalIntent(t *testing.T) {
	t.Run("Tribunal rejects hostnames", func(t *testing.T) {
		tools := &tribunalAuthoringTools{outcome: shellToolOutcome(`{"exit_code":0}`)}
		inference := &tribunalAuthoringInference{result: &tribunalv1.TribunalIntentResult{Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED}}
		run := newTribunalAuthoringRun(t, true, inference, tools)
		call, err := run.runTool(context.Background(), 1, "model-call-1", &operatorv1.InferenceToolCall{
			CallId: "call-1", Name: constants.MCPToolRunShellCommand,
			ArgumentsJson: `{"command":"echo","args":["hello"],"hostnames":["web-1"]}`,
		})
		require.NoError(t, err)
		assert.False(t, call.Success)
		assert.Contains(t, call.Error, "does not accept hostnames")
		assert.Zero(t, tools.dispatches)
		assert.Zero(t, inference.intentCalls)
		assert.Zero(t, inference.probeCalls)
	})
}

func TestRunApprovedShellCommandShowsRevisedArgumentsOnSuccessAndFailure(t *testing.T) {
	proposal := `{"command":"echo","args":["before"]}`
	approved := models.RunShellCommandRequest{Command: "printf", Args: []string{"%s", "approved"}, Timeout: 30, WorkingDir: "/operator/workspace"}
	approvedJSON := approved.CanonicalJSON()
	for _, tc := range []struct {
		name        string
		summary     string
		wantSuccess bool
	}{
		{name: "success", summary: `{"exit_code":0,"stdout":"approved"}`, wantSuccess: true},
		{name: "execution failure", summary: `{"exit_code":2,"stderr":"failed"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &agentRun{}
			record, err := run.runApprovedShellCommand(AgentToolCall{
				CallID: "call-1", ToolName: constants.MCPToolRunShellCommand, ArgumentsJSON: proposal,
			}, approvedDispatchResult(approvedJSON, tc.summary))
			require.NoError(t, err)
			assert.Equal(t, approvedJSON, record.ApprovedArgumentsJSON) //nolint:testifylint // exact bytes, not JSON equivalence
			assert.Equal(t, tc.wantSuccess, record.Success)
			if tc.wantSuccess {
				// Success is a typed result carrying the approved arguments.
				assert.Contains(t, record.ResultJSON, `"approved_arguments"`)
				assert.Contains(t, record.ResultJSON, `"command":"printf"`)
				assert.Contains(t, record.ResultJSON, `"working_dir":"/operator/workspace"`)
				return
			}
			// Failure is the standard error result; its text names the approved arguments.
			assert.Contains(t, record.Error, "failed")
			assert.Contains(t, record.Error, "Approved arguments: ")
			assert.Contains(t, record.ResultJSON, "Approved arguments:")
			assert.Contains(t, record.ResultJSON, "printf")
		})
	}
}

func shellToolOutcome(summary string) *client.GovernedOutcome {
	receipt := &operatorv1.EventReceipt{TransactionId: "txn-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, ResultSummary: summary}
	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, Receipt: receipt},
		Dispatch:    &operatorv1.GovernedDispatchResult{TransactionId: "txn-1", Receipt: receipt},
	}
}

func approvedDispatchResult(argumentsJSON, summary string) *tribunalv1.TribunalIntentResult {
	receipt := &operatorv1.EventReceipt{TransactionId: "txn-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, ResultSummary: summary}
	return &tribunalv1.TribunalIntentResult{
		Outcome:        tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED,
		ArgumentsJson:  argumentsJSON,
		CommandOutcome: &tribunalv1.TribunalIntentResult_Dispatched{Dispatched: &operatorv1.GovernedDispatchResult{TransactionId: "txn-1", Receipt: receipt}},
	}
}
