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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	agentTestWorkingDirectory = "/srv/ztd-operator"
	agentTestCampaignID       = "campaign-1"
	agentTestScenarioID       = "tool-arg-run-commands"
	agentTestModelTag         = "agent-model:latest"
	agentTestModelDigest      = "1111111111111111111111111111111111111111111111111111111111111111"
	agentTestCommand          = "cat status/health.txt"
	agentTestRunCommandsName  = "run_commands_with_operator"
)

// agentAnswer replaces the governed answer for EXECUTE_BASH dispatches.
type agentAnswer struct {
	outcome *client.GovernedOutcome
	err     error
}

// agentExecutorFixture wires the real executor, the real command lane, and the
// evidence ledger double. Only the Gateway client, the inference Operator, the
// sentinel, and the trace store are fakes, and each fake records into one log.
type agentExecutorFixture struct {
	t         *testing.T
	ledger    *evidenceLedger
	log       []string
	client    *agentLaneClient
	inference *agentInferenceFake
	sentinel  *agentSentinelFake
	traces    *agentTraceStoreFake
	executor  *CampaignAgentExecutor
	req       AssignmentExecutionRequest
	workspace ScenarioWorkspace
}

func newAgentExecutorFixture(t *testing.T, turns ...[]*operatorv1.InferenceResponsePart) *agentExecutorFixture {
	t.Helper()
	signer := oracletest.NewSigner(t)
	f := &agentExecutorFixture{t: t, ledger: newEvidenceLedger(t, signer, evidenceOperatorID)}
	f.client = &agentLaneClient{fixture: f}
	f.inference = &agentInferenceFake{fixture: f, turns: turns}
	f.sentinel = &agentSentinelFake{fixture: f}
	f.traces = &agentTraceStoreFake{}

	registry := []*operatorv1.InferenceModelVariant{{Model: agentTestModelTag, Digest: agentTestModelDigest}}
	registryDigest, err := models.ComputeInferenceModelRegistryDigest(agentTestCampaignID, registry)
	require.NoError(t, err)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	seq := 0
	newID := func(prefix string) string {
		seq++
		return fmt.Sprintf("%s-%d", prefix, seq)
	}
	lane := NewCommandLane(f.client, client.Persona{CLISessionID: "cli-session-1"})
	f.executor, err = NewCampaignAgentExecutor(f.inference, lane, f.sentinel, f.traces, func() time.Time { return now }, newID)
	require.NoError(t, err)

	f.req = AssignmentExecutionRequest{
		Assignment: &evalv1.EvaluationAssignment{
			AssignmentId: "assignment-1",
			RunId:        "run-1",
			CampaignId:   agentTestCampaignID,
			ScenarioId:   agentTestScenarioID,
			Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: &evalv1.ModelVariant{ServedModelTag: agentTestModelTag, ModelDigest: agentTestModelDigest},
			}},
		},
		AttemptID: "attempt-1",
		ScenarioInput: ScenarioInputFixture{
			ScenarioID:     agentTestScenarioID,
			UserPrompt:     "Run one read-only command that prints " + ScenarioWorkspaceToken + "/status/health.txt.",
			WorkspaceFiles: []ScenarioWorkspaceFile{{Label: "health", RelPath: "status/health.txt", Content: "HEALTHY"}},
		},
		GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC,
		Binding: CampaignExecutionBinding{
			InferenceOperatorSessionID:   "infer-session-1",
			DataOperatorID:               evidenceOperatorID,
			DataOperatorSessionID:        evidenceSessionID,
			DataOperatorWorkingDirectory: agentTestWorkingDirectory,
			ModelRegistryDigest:          registryDigest,
			ModelRegistry:                registry,
			DataOperatorAnchor:           signer.Anchor(evidenceOperatorID),
		},
	}
	f.workspace, err = NewScenarioWorkspace(agentTestWorkingDirectory, "run-1", "attempt-1")
	require.NoError(t, err)
	return f
}

func (f *agentExecutorFixture) execute() (*evalv1.EvaluationAssignmentResult, error) {
	return f.executor.ExecuteAssignment(context.Background(), f.req)
}

// storedTrace decodes the sealed trace the executor persisted.
func (f *agentExecutorFixture) storedTrace() *AgentTrace {
	f.t.Helper()
	require.NotEmpty(f.t, f.traces.body, "the executor persists the sealed trace before it imports")
	trace := &AgentTrace{}
	require.NoError(f.t, json.Unmarshal(f.traces.body, trace))
	return trace
}

// agentLaneClient is the Gateway client under the command lane. Every governed
// dispatch is one ledger link and a receipt, unless answer replaces EXECUTE_BASH.
type agentLaneClient struct {
	fixture  *agentExecutorFixture
	answer   *agentAnswer
	requests []client.DispatchCommandRequest
}

func (c *agentLaneClient) DispatchCommand(_ context.Context, _ client.Persona, request client.DispatchCommandRequest) (*client.GovernedOutcome, error) {
	c.requests = append(c.requests, request)
	c.fixture.log = append(c.fixture.log, "dispatch:"+request.ActionType+":"+request.TargetResource)
	if c.answer != nil && request.ActionType == string(constants.ActionTypeExecuteBash) {
		return c.answer.outcome, c.answer.err
	}
	txID := fmt.Sprintf("tx-dispatch-%d", len(c.requests))
	receipt := c.fixture.ledger.executed(txID, request.ActionType)
	var result proto.Message
	switch request.ActionType {
	case string(constants.ActionTypeExecuteBash):
		result = &operatorv1.CommandResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, Stdout: "HEALTHY\n"}
	case string(constants.ActionTypeFileEdit):
		result = &operatorv1.FileEditResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, FilePath: request.TargetResource, Operation: string(constants.FileOperationWrite)}
	default:
		return nil, fmt.Errorf("agent test client: unscripted action %s", request.ActionType)
	}
	payload, err := proto.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, Receipt: receipt},
		Dispatch:    &operatorv1.GovernedDispatchResult{TransactionId: txID, Receipt: receipt, ResultPayload: payload},
	}, nil
}

// agentInferenceFake answers each governed inference with the next scripted turn.
type agentInferenceFake struct {
	fixture  *agentExecutorFixture
	turns    [][]*operatorv1.InferenceResponsePart
	requests []*operatorv1.InferenceDispatchRequest
}

func (f *agentInferenceFake) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	f.requests = append(f.requests, req)
	f.fixture.log = append(f.fixture.log, "infer")
	turn := len(f.requests) - 1
	if turn >= len(f.turns) {
		return nil, fmt.Errorf("agent test inference: unscripted turn %d", turn)
	}
	digest := fmt.Sprintf("digest-%d", turn)
	return &operatorv1.InferenceDispatchResponse{
		Result: &operatorv1.InferenceResult{
			ProviderAttemptId:   req.GetProviderAttemptId(),
			RequestedModel:      req.GetModel(),
			CampaignId:          req.GetCampaignId(),
			ModelRegistryDigest: req.GetModelRegistryDigest(),
			ResultDigest:        digest,
			Parts:               f.turns[turn],
		},
		Receipt: &operatorv1.ActionReceipt{ResultSummary: digest},
	}, nil
}

// agentSentinelFake reads the ledger as a harness sentinel. failOnRead, when
// set, makes that numbered read (1 opens the bracket, 2 closes it) fail.
type agentSentinelFake struct {
	fixture    *agentExecutorFixture
	reads      int
	failOnRead int
}

func (s *agentSentinelFake) Read(_ context.Context, _ cases.Executor, readAbs string) (*operatorv1.ActionReceipt, error) {
	s.reads++
	s.fixture.log = append(s.fixture.log, "sentinel:"+readAbs)
	if s.reads == s.failOnRead {
		return nil, errors.New("agent test sentinel: read failed")
	}
	return s.fixture.ledger.executed(fmt.Sprintf("tx-sentinel-%d", s.reads), string(constants.ActionTypeFsRead)), nil
}

// agentTraceStoreFake keeps the last persisted trace body.
type agentTraceStoreFake struct {
	body  []byte
	saves int
}

func (s *agentTraceStoreFake) SaveAssignmentAgentTrace(_ context.Context, _, _ string, body []byte) error {
	s.saves++
	s.body = body
	return nil
}

func runCommandTurns(command string) [][]*operatorv1.InferenceResponsePart {
	arguments := fmt.Sprintf(`{"command":%q,"request":"print the health file"}`, command)
	return [][]*operatorv1.InferenceResponsePart{
		{{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "call-1", Name: agentTestRunCommandsName, ArgumentsJson: arguments}}}},
		{{Part: &operatorv1.InferenceResponsePart_Text{Text: "HEALTHY"}}},
	}
}

func TestCampaignAgentExecutor_RunCommandsDispatchesModelWrittenCommandVerbatim(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)

	result, err := f.execute()

	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
	require.Len(t, f.inference.requests, 2, "the tool loop makes one inference per turn and no command-generation call")
	for _, request := range f.inference.requests {
		for _, message := range request.GetMessages() {
			for _, part := range message.GetParts() {
				assert.NotContains(t, part.GetText(), "Translate the operator request", "no second inference writes the command")
			}
		}
	}
	var shell *client.DispatchCommandRequest
	for i := range f.client.requests {
		if f.client.requests[i].ActionType == string(constants.ActionTypeExecuteBash) {
			shell = &f.client.requests[i]
		}
	}
	require.NotNil(t, shell, "the run dispatches one governed EXECUTE_BASH")
	command := &operatorv1.CommandRequested{}
	require.NoError(t, proto.Unmarshal(shell.Payload, command))
	assert.Equal(t, agentTestCommand, command.GetCommand())
	assert.Equal(t, "print the health file", command.GetJustification())
	trace := f.storedTrace()
	require.Len(t, trace.ToolCalls, 1)
	assert.Equal(t, agentTestCommand, trace.ToolCalls[0].Command)
}

func TestCampaignAgentExecutor_EmptyCommandFailsCallWithoutDispatch(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns("   ")...)

	_, err := f.execute()

	require.NoError(t, err, "a model-visible tool failure is a scored answer, not a harness failure")
	for _, request := range f.client.requests {
		assert.NotEqual(t, string(constants.ActionTypeExecuteBash), request.ActionType, "an empty command dispatches nothing")
	}
	trace := f.storedTrace()
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.False(t, call.Success)
	assert.Equal(t, agentToolErrorExecution, call.ErrorType)
	assert.Equal(t, "ZTD-1607: Tool execution failed for run_commands_with_operator: command is required", call.Error)
}

func TestCampaignAgentExecutor_BracketsToolLoopWithSentinelReadsInOrder(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	sentinelPath := f.workspace.SentinelPath()
	healthPath, err := f.workspace.FilePath("status/health.txt")
	require.NoError(t, err)

	_, err = f.execute()

	require.NoError(t, err)
	assert.Equal(t, []string{
		"dispatch:FILE_EDIT:" + sentinelPath,
		"dispatch:FILE_EDIT:" + healthPath,
		"sentinel:" + sentinelPath,
		"infer",
		"dispatch:EXECUTE_BASH:" + agentTestWorkingDirectory,
		"infer",
		"sentinel:" + sentinelPath,
	}, f.log)
}

func TestCampaignAgentExecutor_SentinelReadFailureIsAssignmentExecutionErrorNeverGraded(t *testing.T) {
	for _, failOnRead := range []int{1, 2} {
		t.Run(fmt.Sprintf("read %d", failOnRead), func(t *testing.T) {
			f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
			f.sentinel.failOnRead = failOnRead

			result, err := f.execute()

			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrEvaluationAssignmentExecutionFailed)
			assert.Nil(t, result)
			assert.Zero(t, f.traces.saves, "a harness failure persists no trace and grades nothing")
		})
	}
}

func TestCampaignAgentExecutor_HarnessDispatchFailuresFailTheAssignment(t *testing.T) {
	tests := []struct {
		name   string
		answer agentAnswer
	}{
		{name: "suspension for approval", answer: agentAnswer{outcome: &client.GovernedOutcome{
			Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED, SuspendedTxHash: "hash-1"},
		}}},
		{name: "untyped answer", answer: agentAnswer{outcome: &client.GovernedOutcome{
			Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED},
		}}},
		{name: "transport error", answer: agentAnswer{err: errors.New("gateway unreachable")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
			f.client.answer = &tt.answer

			result, err := f.execute()

			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
			assert.Nil(t, result)
			assert.Zero(t, f.traces.saves)
		})
	}
}

func TestCampaignAgentExecutor_RejectionCompletesCallAsSecurityViolationDeny(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns("rm -rf status")...)
	rejected := f.ledger.rejected("tx-rejected", string(constants.ActionTypeExecuteBash))
	f.client.answer = &agentAnswer{outcome: &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{
			Kind:      evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
			Rejection: &operatorv1.GovernanceRejection{Error: "policy blocks recursive delete", Receipt: rejected},
		},
	}}

	result, err := f.execute()

	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
	call := f.storedTrace().ToolCalls[0]
	assert.False(t, call.Success)
	assert.Equal(t, agentToolErrorSecurity, call.ErrorType)
	assert.Equal(t, "SECURITY VIOLATION: policy blocks recursive delete", call.Error)
	assert.Equal(t, agentPolicyDecisionDeny, call.PolicyDecision)
	assert.Equal(t, "tx-rejected", call.TransactionID)
	require.NotNil(t, call.Receipt, "a rejection keeps its signed receipt in the trace")
}

func TestCampaignAgentExecutor_RefusalCompletesCallAsSecurityViolationWithTypedMessage(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	f.client.answer = &agentAnswer{outcome: &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{
			Kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
			Refusal: &operatorv1.IngressRefusal{Code: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED, Error: "method not allowed on this route"},
		},
	}}

	result, err := f.execute()

	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
	call := f.storedTrace().ToolCalls[0]
	assert.Equal(t, agentToolErrorSecurity, call.ErrorType)
	assert.Equal(t, "SECURITY VIOLATION: "+operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED.String()+": method not allowed on this route", call.Error)
	assert.NotContains(t, call.Error, "HTTP")
	assert.Equal(t, agentPolicyDecisionDeny, call.PolicyDecision)
	assert.Nil(t, call.Receipt, "a refusal has no receipt")
}

func TestCampaignAgentExecutor_VerifiedToolCallCarriesReceiptReference(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)

	result, err := f.execute()

	require.NoError(t, err)
	require.Len(t, result.GetEvidenceRefs(), 1)
	require.Len(t, result.GetGovernedActions(), 1)
	trace := f.storedTrace()
	receiptRef := result.GetGovernedActions()[0].GetReceiptRef()
	assert.Equal(t, result.GetEvidenceRefs()[0].GetArtifactId(), receiptRef.GetArtifactId(), "the receipt lives inside the sealed trace")
	assert.Equal(t, "ztd.operator.v1.ActionReceipt", receiptRef.GetSchemaRef())
	assert.Equal(t, trace.ToolCalls[0].TransactionID, receiptRef.GetTransactionId())
	assert.Equal(t, trace.RunID, receiptRef.GetRunId())
	assert.Len(t, receiptRef.GetSha256(), 64)
	assert.Equal(t, receiptRef.GetSha256(), result.GetToolCalls()[0].GetGovernedBindingRef().GetSha256())
}

func TestCampaignAgentExecutor_TamperedReceiptImportsAsInvalidEvidenceWithoutGrades(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	_, err := f.execute()
	require.NoError(t, err)
	trace := f.storedTrace()
	receipt := trace.ToolCalls[0].Receipt.ActionReceipt
	receipt.Signature = flipFirstHexDigit(receipt.Signature)
	// Re-seal so the trace digest matches: the failure must come from the signed
	// evidence, not from the digest check that runs before it.
	body, err := SealAgentTrace(trace)
	require.NoError(t, err)
	ref, err := BuildAssignmentAgentTraceEvidenceReference(f.req.Assignment.GetRunId(), f.req.AttemptID, body, time.Now().UTC())
	require.NoError(t, err)

	result, err := ImportAssignmentResultFromAgentTrace(f.req, trace, ref, time.Now().UTC(), func(prefix string) string { return prefix })

	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, result.GetLifecycleStatus())
	assert.NotEmpty(t, result.GetInvalidEvidenceDetail())
	assert.Contains(t, result.GetInvalidEvidenceDetail(), "tool call "+trace.ToolCalls[0].CallID)
	assert.Contains(t, result.GetInvalidEvidenceDetail(), "signature invalid")
	assert.Empty(t, result.GetDeterministicGrades())
	assert.Empty(t, result.GetSemanticGrades())
	assert.Equal(t, evalv1.EvaluationTrajectoryOutcome_EVALUATION_TRAJECTORY_OUTCOME_UNSPECIFIED, result.GetTrajectoryOutcome())
	assert.Empty(t, result.GetFailureReason())
	assert.Empty(t, result.GetGovernedActions(), "no governed action is claimed from an unverified trace")
	assert.Len(t, result.GetToolCalls(), 1, "the tool calls are still recorded")
}

func TestCampaignAgentExecutor_RequiresLedgerSentinelToConstruct(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)

	_, err := NewCampaignAgentExecutor(f.inference, NewCommandLane(f.client, client.Persona{}), nil, f.traces, time.Now, func(prefix string) string { return prefix })

	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestCampaignAgentExecutor_RequiresDataOperatorAnchorToStart(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	f.req.Binding.DataOperatorAnchor = nil

	_, err := f.execute()

	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
	assert.Empty(t, f.client.requests, "no governed action runs without a pinned Data Operator key")
}
