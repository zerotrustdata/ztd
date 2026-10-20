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
	"strings"
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
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

const (
	agentTestWorkingDirectory = "/srv/ztd-operator"
	agentTestCampaignID       = "campaign-1"
	agentTestScenarioID       = "tool-arg-shell-command"
	agentTestModelTag         = "agent-model:latest"
	agentTestModelDigest      = "1111111111111111111111111111111111111111111111111111111111111111"
	agentTestCommand          = "cat status/health.txt"
	agentTestRunCommandsName  = "run_commands_with_operator"
)

// agentTestPersona is the persona the fixture's command lane dispatches as, and
// the persona an approved Tribunal command dispatches as.
var agentTestPersona = client.Persona{CLISessionID: "cli-session-1"}

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
	lane := NewCommandLane(f.client, agentTestPersona)
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
		ScenarioTools: ScenarioToolExpectations{AllowedTools: agentTestCatalogToolNames(), TribunalCommands: true},
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
	catalog, err := newAgentToolCatalog()
	if err != nil {
		panic(err)
	}
	return catalog.ToolNames()
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

// agentShellResultSummary is the run_shell_command result the Operator signs
// into the receipt of a completed shell call.
const agentShellResultSummary = `{"exit_code":0,"stdout":"HEALTHY\n","stderr":"","timed_out":false}`

// agentLaneClient is the Gateway client under the command lane. Every governed
// dispatch is one ledger link and a receipt, unless answer replaces the tool
// call's governed answer (EXECUTE_BASH or MCP_CALL).
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
	if c.answer != nil && (action == constants.ActionTypeExecuteBash || action == constants.ActionTypeMcpCall) {
		return c.answer.outcome, c.answer.err
	}
	txID := fmt.Sprintf("tx-dispatch-%d", len(c.requests))
	receipt := c.fixture.ledger.executed(txID, string(action))
	var result proto.Message
	switch action {
	case constants.ActionTypeExecuteBash:
		result = &operatorv1.CommandResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, Stdout: "HEALTHY\n"}
	case constants.ActionTypeMcpCall:
		// A native tool's answer is its result summary; the MCP payload carries none.
		receipt.ResultSummary = agentShellResultSummary
		c.fixture.ledger.signer.Resign(c.fixture.t, receipt)
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
	// intents records each Tribunal intent the executor submitted, in order.
	intents []*tribunalv1.TribunalIntentRequest
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

// DispatchIntent answers a Tribunal intent as the Gateway's intent controller
// does: the production Tribunal deliberates over this fake's scripted member
// inferences, and an approved command dispatches as one governed EXECUTE_BASH
// on the lane client. A transport failure of that dispatch is returned as an
// error, which the executor classifies as a harness failure.
func (f *agentInferenceFake) DispatchIntent(ctx context.Context, req *tribunalv1.TribunalIntentRequest) (*tribunalv1.TribunalIntentResult, error) {
	f.intents = append(f.intents, req)
	result, err := tribunal.NewService(agentTribunalMembers{inference: f}).Deliberate(ctx, req)
	if err != nil {
		return nil, err
	}
	if result.GetOutcome() != tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED {
		return result, nil
	}
	payload, err := proto.Marshal(&operatorv1.CommandRequested{
		Command:          result.GetCommand(),
		ExecutionId:      req.GetExecutionId(),
		Justification:    req.GetRequest(),
		WorkingDirectory: req.GetWorkingDirectory(),
	})
	if err != nil {
		return nil, err
	}
	outcome, err := f.fixture.client.DispatchCommand(ctx, agentTestPersona, models.OperatorCommandRequest{
		TargetOperatorSessionID: req.GetTargetOperatorSessionId(),
		EventType:               string(constants.EventOperatorCommandRequested),
		Payload:                 payload,
		TargetResource:          req.GetWorkingDirectory(),
		InvestigationID:         req.GetInvestigationId(),
		TaskID:                  req.GetTaskId(),
	})
	if err != nil {
		return nil, err
	}
	switch {
	case outcome.Dispatch != nil:
		result.CommandOutcome = &tribunalv1.TribunalIntentResult_Dispatched{Dispatched: outcome.Dispatch}
	case outcome.Observation.GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION:
		result.CommandOutcome = &tribunalv1.TribunalIntentResult_Rejection{Rejection: outcome.Observation.GetRejection()}
	case outcome.Observation.GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL:
		result.CommandOutcome = &tribunalv1.TribunalIntentResult_Refusal{Refusal: outcome.Observation.GetRefusal()}
	}
	return result, nil
}

// agentTribunalMembers is the Tribunal's member dispatcher: each member call is
// one scripted governed inference on the fake. A failed call answers with the
// typed failure the Gateway's member dispatcher returns, here a provider that
// is unavailable, carrying the runtime's FAILED receipt.
type agentTribunalMembers struct {
	inference *agentInferenceFake
}

func (m agentTribunalMembers) DispatchMember(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, *operatorv1.InferenceDispatchFailure) {
	response, err := m.inference.DispatchInference(ctx, req)
	if err != nil {
		return nil, &operatorv1.InferenceDispatchFailure{
			Error:   err.Error(),
			Code:    operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_BACKEND_UNAVAILABLE,
			Receipt: governance.FailedReceipt(err),
		}
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

// shellCommandArguments encodes one command line as run_shell_command arguments:
// the program, then its arguments.
func shellCommandArguments(commandLine string) string {
	request := models.RunShellCommandRequest{}
	if fields := strings.Fields(commandLine); len(fields) > 0 {
		request.Command, request.Args = fields[0], fields[1:]
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// runCommandTurns scripts one native run_shell_command call for commandLine,
// then the answer that reports its output.
func runCommandTurns(commandLine string) [][]*operatorv1.InferenceResponsePart {
	return [][]*operatorv1.InferenceResponsePart{
		{{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "call-1", Name: constants.MCPToolRunShellCommand, ArgumentsJson: shellCommandArguments(commandLine)}}}},
		{{Part: &operatorv1.InferenceResponsePart_Text{Text: "HEALTHY"}}},
	}
}

// TestCampaignAgentExecutor_RequiresLedgerSentinelToConstruct needs no fixture:
// the constructor's nil check rejects every missing dependency with the typed
// sentinel before any of them is used.
func TestCampaignAgentExecutor_RequiresLedgerSentinelToConstruct(t *testing.T) {
	_, err := NewCampaignAgentExecutor(nil, NewCommandLane(nil, client.Persona{}), nil, nil, time.Now, func(prefix string) string { return prefix })

	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}
