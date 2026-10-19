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
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/governance"
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
// evidence ledger doubles. Only the Gateway client, the inference Operator, the
// sentinel, and the trace store are fakes, and each fake records into one log.
// ledger is the Data runtime's ledger; inferenceLedger is the Inference
// runtime's, and is ledger itself when both roles share one runtime.
type agentExecutorFixture struct {
	t               *testing.T
	ledger          *evidenceLedger
	inferenceLedger *evidenceLedger
	log             []string
	client          *agentLaneClient
	inference       *agentInferenceFake
	sentinel        *agentSentinelFake
	traces          *agentTraceStoreFake
	executor        *CampaignAgentExecutor
	req             AssignmentExecutionRequest
	workspace       ScenarioWorkspace
}

// newAgentExecutorFixture runs the Inference and Data roles on distinct runtimes.
func newAgentExecutorFixture(t *testing.T, turns ...[]*operatorv1.InferenceResponsePart) *agentExecutorFixture {
	t.Helper()
	return newAgentExecutorFixtureOnRuntimes(t, false, turns...)
}

// newAgentExecutorFixtureOnRuntimes runs the Inference and Data roles on
// distinct runtimes, or, when shared, on one runtime whose ledger records both
// roles' executions, as the Gateway's embedded data,inference Operator does.
func newAgentExecutorFixtureOnRuntimes(t *testing.T, shared bool, turns ...[]*operatorv1.InferenceResponsePart) *agentExecutorFixture {
	t.Helper()
	f := &agentExecutorFixture{t: t, ledger: newEvidenceLedger(t, oracletest.NewSigner(t))}
	f.inferenceLedger = newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
	if shared {
		f.inferenceLedger = f.ledger
	}
	f.client = &agentLaneClient{fixture: f}
	f.inference = &agentInferenceFake{fixture: f, turns: turns}
	f.sentinel = &agentSentinelFake{fixture: f}
	f.traces = &agentTraceStoreFake{}

	registry := []*operatorv1.InferenceModelVariant{{Model: agentTestModelTag, Digest: agentTestModelDigest}}
	registryDigest, err := models.ComputeInferenceModelRegistryDigest(agentTestCampaignID, registry)
	require.NoError(t, err)
	frozenVariant := &evalv1.ModelVariant{
		VariantId:      "variant-a",
		ServedModelTag: agentTestModelTag,
		ModelDigest:    agentTestModelDigest,
	}
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
				CandidateVariant: frozenVariant,
			}},
		},
		AttemptID: "attempt-1",
		ScenarioInput: ScenarioInputFixture{
			ScenarioID:     agentTestScenarioID,
			UserPrompt:     "Run one read-only command that prints " + ScenarioWorkspaceToken + "/status/health.txt.",
			WorkspaceFiles: []ScenarioWorkspaceFile{{Label: "health", RelPath: "status/health.txt", Content: "HEALTHY"}},
		},
		ScenarioTools: ScenarioToolExpectations{AllowedTools: agentTestCatalogToolNames()},
		GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC,
		Binding: CampaignExecutionBinding{
			Topology: CampaignOperatorTopology{
				Inference:            f.inferenceLedger.campaignRuntime(),
				Data:                 f.ledger.campaignRuntime(),
				DataWorkingDirectory: agentTestWorkingDirectory,
			},
			ModelRegistryDigest: registryDigest,
			ModelRegistry:       registry,
			ModelVariants:       []*evalv1.ModelVariant{proto.Clone(frozenVariant).(*evalv1.ModelVariant)},
		},
	}
	f.workspace, err = NewScenarioWorkspace(agentTestWorkingDirectory, "run-1", "attempt-1")
	require.NoError(t, err)
	return f
}

// agentTestCatalogToolNames scopes the fixture scenario to every catalog tool,
// so each test can drive whichever tool it exercises.
func agentTestCatalogToolNames() []string {
	names := make([]string, 0, len(agentToolSpecs))
	for _, spec := range agentToolSpecs {
		names = append(names, spec.Name)
	}
	return names
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
	requests []models.OperatorCommandRequest
}

func (c *agentLaneClient) DispatchCommand(_ context.Context, _ client.Persona, request models.OperatorCommandRequest) (*client.GovernedOutcome, error) {
	c.requests = append(c.requests, request)
	action, err := constants.ValidateGovernedRequest(constants.EventType(request.EventType))
	if err != nil {
		return nil, err
	}
	c.fixture.log = append(c.fixture.log, "dispatch:"+string(action)+":"+request.TargetResource)
	if c.answer != nil && action == constants.ActionTypeExecuteBash {
		return c.answer.outcome, c.answer.err
	}
	txID := fmt.Sprintf("tx-dispatch-%d", len(c.requests))
	receipt := c.fixture.ledger.executed(txID, string(action))
	var result proto.Message
	switch action {
	case constants.ActionTypeExecuteBash:
		result = &operatorv1.CommandResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, Stdout: "HEALTHY\n"}
	case constants.ActionTypeFileEdit:
		result = &operatorv1.FileEditResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, FilePath: request.TargetResource, Operation: string(constants.FileOperationWrite)}
	default:
		return nil, fmt.Errorf("agent test client: unscripted action %s", action)
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

// agentInferenceFake is the Inference runtime: each governed inference appends
// one commitment to the fixture's inference ledger and answers with the next
// scripted turn and its signed receipt. Turns are numbered from 1.
type agentInferenceFake struct {
	fixture  *agentExecutorFixture
	turns    [][]*operatorv1.InferenceResponsePart
	requests []*operatorv1.InferenceDispatchRequest
	// failTurn, when set, makes that turn's provider fail after the runtime
	// executed it, so the error carries the runtime's FAILED receipt.
	failTurn int
	// foreignTurn, when set, has another caller execute on the Inference
	// runtime during that turn, a commitment the trace cannot claim.
	foreignTurn int
	// tamperTurn, when set, has tamper rewrite that turn's response after the
	// runtime signs its receipt, so the response or its receipt no longer verifies.
	tamperTurn int
	tamper     func(response *operatorv1.InferenceDispatchResponse)
}

func (f *agentInferenceFake) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	f.requests = append(f.requests, req)
	f.fixture.log = append(f.fixture.log, "infer")
	turn := len(f.requests)
	if turn > len(f.turns) {
		return nil, fmt.Errorf("agent test inference: unscripted turn %d", turn)
	}
	ledger := f.fixture.inferenceLedger
	if turn == f.foreignTurn {
		ledger.executed(fmt.Sprintf("tx-foreign-%d", turn), string(constants.ActionTypeExecuteBash))
	}
	txID := fmt.Sprintf("tx-inference-%d", turn)
	if turn == f.failTurn {
		return nil, governance.WithFailedReceipt(ledger.inferenceFailed(txID), errors.New("agent test inference: provider unreachable"))
	}
	result := oracletest.InferenceResult(f.fixture.t, req, f.turns[turn-1])
	response := &operatorv1.InferenceDispatchResponse{
		TransactionId: txID,
		Result:        result,
		Receipt:       ledger.inferenceWithResultDigest(txID, result.GetResultDigest()),
	}
	if turn == f.tamperTurn && f.tamper != nil {
		f.tamper(response)
	}
	return response, nil
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
	var shell *models.OperatorCommandRequest
	for i := range f.client.requests {
		if f.client.requests[i].EventType == string(constants.EventOperatorCommandRequested) {
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

func TestCampaignAgentExecutor_DispatchesToolsDespiteRecordedProbeRejection(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	f.req.Assignment.GetHomogeneous().CandidateVariant.CapabilityObservations = []*evalv1.ModelCapabilityObservation{
		capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_TOOL_CALLING, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSUPPORTED, "provider rejected tool-call probe"),
	}

	result, err := f.execute()

	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
	require.Len(t, f.inference.requests, 2, "past probe results must not suppress current provider requests")
	for _, request := range f.inference.requests {
		assert.Len(t, request.GetTools(), len(agentToolSpecs), "the provider receives the scenario's full tool surface")
		require.NotNil(t, request.GetToolChoice())
		assert.Equal(t, operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO, request.GetToolChoice().GetMode())
	}
	assert.Empty(t, f.storedTrace().ProviderFailure)
}

func TestCampaignAgentExecutor_DeclaresScenarioAllowedAndForbiddenToolsInCatalogOrder(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	f.req.ScenarioTools = ScenarioToolExpectations{
		AllowedTools:   []string{agentToolRunCommands, agentToolFileRead},
		ForbiddenTools: []string{agentToolFileWrite},
	}

	_, err := f.execute()

	require.NoError(t, err)
	want := []string{agentToolFileRead, agentToolFileWrite, agentToolRunCommands}
	for _, request := range f.inference.requests {
		var names []string
		for _, tool := range request.GetTools() {
			names = append(names, tool.GetName())
		}
		assert.Equal(t, want, names, "forbidden tools stay declared so restraint is observable")
	}
	trace := f.storedTrace()
	require.NotNil(t, trace.Input)
	require.Len(t, trace.Input.Tools, len(want))
	assert.Equal(t, want, trace.ModelCalls[0].ToolsDeclared)
}

func TestCampaignAgentExecutor_AnswerScenarioDeclaresNoTools(t *testing.T) {
	f := newAgentExecutorFixture(t, []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: "HEALTHY"}}})
	f.req.ScenarioTools = ScenarioToolExpectations{}

	_, err := f.execute()

	require.NoError(t, err)
	require.Len(t, f.inference.requests, 1)
	assert.Empty(t, f.inference.requests[0].GetTools())
	assert.Nil(t, f.inference.requests[0].GetToolChoice(), "no tool choice without declared tools")
	trace := f.storedTrace()
	assert.Empty(t, trace.Input.Tools)
	assert.Empty(t, trace.ModelCalls[0].ToolsDeclared)
	assert.Equal(t, "HEALTHY", trace.FinalOutput)
}

func TestCampaignAgentExecutor_UndeclaredToolCallFailsWithoutDispatch(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	f.req.ScenarioTools = ScenarioToolExpectations{AllowedTools: []string{agentToolFileRead}}

	_, err := f.execute()

	require.NoError(t, err, "an undeclared tool call is a scored answer, not a harness failure")
	for _, request := range f.client.requests {
		assert.NotEqual(t, string(constants.EventOperatorCommandRequested), request.EventType, "an undeclared tool dispatches nothing")
	}
	trace := f.storedTrace()
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.False(t, call.Success)
	assert.Equal(t, agentToolErrorExecution, call.ErrorType)
	assert.Equal(t, `ZTD-1607: Unknown tool "run_commands_with_operator"; use one of: file_read_on_operator`, call.Error)
}

func TestCampaignAgentExecutor_UncatalogedScenarioToolFailsClosed(t *testing.T) {
	f := newAgentExecutorFixture(t)
	f.req.ScenarioTools = ScenarioToolExpectations{AllowedTools: []string{"not_a_catalog_tool"}}

	_, err := f.execute()

	require.ErrorIs(t, err, constants.ErrEvaluationScenarioContractInvalid)
	assert.Empty(t, f.inference.requests, "no model call before the tool surface resolves")
}

func TestCampaignAgentExecutor_TraceRecordsExactInputThinkingAndProviderCallIDs(t *testing.T) {
	turns := runCommandTurns(agentTestCommand)
	turns[0] = append([]*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Thinking{Thinking: "  Read the health file first.\n"}}}, turns[0]...)
	f := newAgentExecutorFixture(t, turns...)

	_, err := f.execute()

	require.NoError(t, err)
	trace := f.storedTrace()
	assert.Equal(t, agentTraceSchemaVersion, trace.SchemaVersion)
	require.NotNil(t, trace.Input)
	first := f.inference.requests[0]
	require.Len(t, trace.Input.Messages, len(first.GetMessages()), "the trace records the first turn's rendered messages")
	for i, message := range first.GetMessages() {
		assert.Equal(t, message.GetRole().String(), trace.Input.Messages[i].Role)
		assert.Equal(t, message.GetParts()[0].GetText(), trace.Input.Messages[i].Text)
	}
	require.Len(t, trace.Input.Tools, len(first.GetTools()), "the trace records the declared tool schemas")
	for i, tool := range first.GetTools() {
		assert.Equal(t, tool.GetName(), trace.Input.Tools[i].Name)
		assert.Equal(t, tool.GetJsonSchema(), trace.Input.Tools[i].JSONSchema)
	}
	require.Len(t, trace.ModelCalls, 2)
	assert.Equal(t, "  Read the health file first.\n", trace.ModelCalls[0].Thinking, "thinking is published verbatim")
	assert.Empty(t, trace.ModelCalls[1].Thinking)
	require.Len(t, trace.ToolCalls, 1)
	assert.Equal(t, "call-1", trace.ToolCalls[0].ProviderCallID)
	require.NoError(t, ValidateAgentTraceDigest(trace))
}

func TestCampaignAgentExecutor_EmptyCommandFailsCallWithoutDispatch(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns("   ")...)

	_, err := f.execute()

	require.NoError(t, err, "a model-visible tool failure is a scored answer, not a harness failure")
	for _, request := range f.client.requests {
		assert.NotEqual(t, string(constants.EventOperatorCommandRequested), request.EventType, "an empty command dispatches nothing")
	}
	trace := f.storedTrace()
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.False(t, call.Success)
	assert.Equal(t, agentToolErrorExecution, call.ErrorType)
	assert.Equal(t, "ZTD-1607: Tool execution failed for run_commands_with_operator: command is required", call.Error)
}

func TestCampaignAgentExecutor_EmptyFileContentFailsValidationWithoutDispatch(t *testing.T) {
	for _, tool := range []string{agentToolFileWrite, agentToolFileCreate} {
		t.Run(tool, func(t *testing.T) {
			turns := [][]*operatorv1.InferenceResponsePart{
				{{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
					CallId: "empty-write", Name: tool,
					ArgumentsJson: `{"file_path":"notes.txt","content":"","justification":"update notes","target_operators":[]}`,
				}}}},
				{{Part: &operatorv1.InferenceResponsePart_Text{Text: "No file written."}}},
			}
			f := newAgentExecutorFixture(t, turns...)
			_, err := f.execute()
			require.NoError(t, err)
			trace := f.storedTrace()
			require.Len(t, trace.ToolCalls, 1)
			assert.False(t, trace.ToolCalls[0].Success)
			assert.Contains(t, trace.ToolCalls[0].Error, "content")
			for _, request := range f.client.requests {
				if request.EventType == string(constants.EventOperatorFileEditRequested) {
					edit := &operatorv1.FileEditRequested{}
					require.NoError(t, proto.Unmarshal(request.Payload, edit))
					assert.NotEqual(t, "notes.txt", filepath.Base(edit.GetFilePath()), "invalid tool call must not reach governed dispatch")
				}
			}
			for _, declaration := range f.inference.requests[0].GetTools() {
				if declaration.GetName() == tool {
					assert.Contains(t, declaration.GetJsonSchema(), `"minLength":1`)
				}
			}
		})
	}
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

func TestCampaignAgentExecutor_IngressRefusalIsNotPolicyDenial(t *testing.T) {
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
	assert.Equal(t, agentToolErrorExecution, call.ErrorType)
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED.String()+": method not allowed on this route", call.Error)
	assert.Empty(t, call.PolicyDecision)
	assert.Nil(t, call.Receipt, "a refusal has no receipt")
	assert.NotEqual(t, AgentTerminalClassificationPolicyDenial, f.storedTrace().TerminalClassification)
}

func TestCampaignAgentExecutor_VerifiedToolCallCarriesReceiptReference(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)

	result, err := f.execute()

	require.NoError(t, err)
	require.Len(t, result.GetEvidenceRefs(), 1)
	trace := f.storedTrace()
	require.Len(t, trace.ModelCalls, 2)
	actions := result.GetGovernedActions()
	require.Len(t, actions, 3, "each governed inference, then the tool call it returned, in execution order")
	assert.Equal(t, trace.ModelCalls[0].TransactionID, actions[0].GetTransactionId())
	assert.Equal(t, trace.ToolCalls[0].TransactionID, actions[1].GetTransactionId())
	assert.Equal(t, trace.ModelCalls[1].TransactionID, actions[2].GetTransactionId())
	for _, i := range []int{0, 2} {
		assert.Equal(t, string(constants.ActionTypeInference), actions[i].GetActionType())
		assert.Equal(t, trace.Inference.OperatorID, actions[i].GetOperatorId())
		assert.Equal(t, agentPolicyDecisionAllow, actions[i].GetPolicyDecision())
	}
	assert.Equal(t, trace.ToolCalls[0].Receipt.GetActionType(), actions[1].GetActionType())
	assert.NotEmpty(t, actions[1].GetActionType())
	assert.Equal(t, trace.Data.OperatorID, actions[1].GetOperatorId())
	receiptRef := actions[1].GetReceiptRef()
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

func TestCampaignAgentExecutor_RequiresEachRoleAnchorToStart(t *testing.T) {
	for _, role := range []string{"inference", "data"} {
		t.Run(role, func(t *testing.T) {
			f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
			if role == "inference" {
				f.req.Binding.Topology.Inference.Anchor = nil
			} else {
				f.req.Binding.Topology.Data.Anchor = nil
			}

			_, err := f.execute()

			assert.ErrorIs(t, err, constants.ErrConformanceAnchorMissing)
			assert.Empty(t, f.client.requests, "no governed action runs without both runtimes' pinned keys")
			assert.Empty(t, f.inference.requests)
		})
	}
}

func TestCampaignAgentExecutor_SharedRuntimeMustPinOneKeyToStart(t *testing.T) {
	f := newAgentExecutorFixtureOnRuntimes(t, true, runCommandTurns(agentTestCommand)...)
	f.req.Binding.Topology.Inference.Anchor = oracletest.NewSigner(t).Anchor(evidenceOperatorID)

	_, err := f.execute()

	assert.ErrorIs(t, err, constants.ErrConformanceAnchorInvalid)
	assert.Empty(t, f.client.requests)
}

// The run that motivated per-role runtimes: the embedded Operator held both
// capabilities, so every inference committed inside the Data sentinel bracket.
// On either topology the trace claims every commitment its bracket holds.
func TestCampaignAgentExecutor_EvidenceVerifiesOnEachOperatorTopology(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared runtime %t", shared), func(t *testing.T) {
			f := newAgentExecutorFixtureOnRuntimes(t, shared, runCommandTurns(agentTestCommand)...)

			result, err := f.execute()

			require.NoError(t, err)
			assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
			assert.Empty(t, result.GetInvalidEvidenceDetail())
			assert.NotEmpty(t, result.GetDeterministicGrades())
			trace := f.storedTrace()
			assert.Equal(t, shared, trace.SharedRuntime())
			assert.Equal(t, f.inferenceLedger.operatorID, trace.Inference.OperatorID)
			assert.Equal(t, f.ledger.operatorID, trace.Data.OperatorID)
			require.Len(t, trace.ModelCalls, 2)
			for _, call := range trace.ModelCalls {
				require.NotNil(t, call.Receipt, "every executed model turn keeps its signed receipt")
				assert.Equal(t, call.TransactionID, call.Receipt.GetTransactionId())
				assert.True(t, call.Completed())
			}
			require.NoError(t, VerifyAgentTraceEvidence(trace))
		})
	}
}

func TestCampaignAgentExecutor_ProviderFailureKeepsFailedReceiptAndVerifies(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared runtime %t", shared), func(t *testing.T) {
			f := newAgentExecutorFixtureOnRuntimes(t, shared, runCommandTurns(agentTestCommand)...)
			f.inference.failTurn = 2

			result, err := f.execute()

			require.NoError(t, err, "a provider failure is a scored outcome, not a harness failure")
			assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, result.GetLifecycleStatus())
			assert.Empty(t, result.GetInvalidEvidenceDetail())
			trace := f.storedTrace()
			require.Len(t, trace.ModelCalls, 2, "the failed turn is recorded because its runtime committed it")
			failed := trace.ModelCalls[1]
			assert.False(t, failed.Completed())
			assert.NotEmpty(t, failed.Error)
			assert.Equal(t, "tx-inference-2", failed.TransactionID)
			require.NotNil(t, failed.Receipt)
			assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, failed.Receipt.GetStatus())
			require.NoError(t, VerifyAgentTraceEvidence(trace))
		})
	}
}

func TestCampaignAgentExecutor_SharedRuntimeExecutionTheTraceDoesNotClaimIsInvalidEvidence(t *testing.T) {
	f := newAgentExecutorFixtureOnRuntimes(t, true, runCommandTurns(agentTestCommand)...)
	f.inference.foreignTurn = 2

	result, err := f.execute()

	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, result.GetLifecycleStatus())
	assert.NotEmpty(t, result.GetInvalidEvidenceDetail())
	assert.Empty(t, result.GetDeterministicGrades())
	assert.ErrorIs(t, VerifyAgentTraceEvidence(f.storedTrace()), constants.ErrConformanceUnclaimedExecution)
}

func TestCampaignAgentExecutor_DistinctRuntimesKeepInferenceLedgerOutOfDataBracket(t *testing.T) {
	f := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	f.inference.foreignTurn = 2

	result, err := f.execute()

	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(),
		"another caller on the Inference runtime is outside the Data bracket")
}
