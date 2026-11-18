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
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

// fleetRoster is a three-member roster whose first two members share a
// hostname, as fixture Operators on one host do.
func fleetRoster() []AgentTraceRosterMember {
	member := func(index int, id, host string) AgentTraceRosterMember {
		return AgentTraceRosterMember{
			Index: index, Hostname: host,
			Runtime:   AgentTraceRuntime{OperatorID: id, SessionID: id + "-session"},
			Workspace: AgentTraceWorkspace{Root: "/srv/" + id + "/workspaces/ws-1", OperatorWorkingDirectory: "/srv/" + id},
		}
	}
	return []AgentTraceRosterMember{member(1, "op-a", "shared"), member(2, "op-b", "shared"), member(3, "op-c", "db-host")}
}

func TestResolveRosterTargets(t *testing.T) {
	roster := fleetRoster()
	tests := []struct {
		name      string
		roster    []AgentTraceRosterMember
		selectors []string
		present   bool
		want      []int
		err       error
	}{
		{name: "absent on a roster of one", roster: roster[:1], want: []int{1}},
		{name: "absent on a fleet fails closed", roster: roster, err: constants.ErrHarnessTargetRequired},
		{name: "all", roster: roster, selectors: []string{"all"}, present: true, want: []int{1, 2, 3}},
		{name: "all is case-insensitive", roster: roster, selectors: []string{"ALL"}, present: true, want: []int{1, 2, 3}},
		{name: "indexes resolve in roster order", roster: roster, selectors: []string{"3", "1"}, present: true, want: []int{1, 3}},
		{name: "operator id", roster: roster, selectors: []string{"op-b"}, present: true, want: []int{2}},
		{name: "unique hostname", roster: roster, selectors: []string{"db-host"}, present: true, want: []int{3}},
		{name: "shared hostname is ambiguous", roster: roster, selectors: []string{"shared"}, present: true, err: constants.ErrHarnessTargetAmbiguous},
		{name: "index past the roster", roster: roster, selectors: []string{"4"}, present: true, err: constants.ErrHarnessTargetUnknown},
		{name: "index zero", roster: roster, selectors: []string{"0"}, present: true, err: constants.ErrHarnessTargetUnknown},
		{name: "unknown name never falls back", roster: roster, selectors: []string{"web-9"}, present: true, err: constants.ErrHarnessTargetUnknown},
		{name: "one member named twice", roster: roster, selectors: []string{"1", "op-a"}, present: true, want: []int{1}},
		{name: "empty roster", err: constants.ErrHarnessRosterEmpty},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveRosterTargets(test.roster, test.selectors, test.present)
			if test.err != nil {
				require.ErrorIs(t, err, test.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestSplitTargetOperators(t *testing.T) {
	rest, selectors, present, problem := splitTargetOperators(`{"path":"a.txt","target_operators":["1","op-b"]}`)
	assert.JSONEq(t, `{"path":"a.txt"}`, rest)
	assert.Equal(t, []string{"1", "op-b"}, selectors)
	assert.True(t, present)
	assert.Empty(t, problem)

	rest, _, present, problem = splitTargetOperators(`{"path":"a.txt","target_operators":null}`)
	assert.JSONEq(t, `{"path":"a.txt"}`, rest)
	assert.False(t, present, "an explicit null is absent")
	assert.Empty(t, problem)

	for _, invalid := range []string{`"all"`, `[1]`, `[]`, `[""]`, `{"a":1}`} {
		_, _, present, problem := splitTargetOperators(`{"path":"a.txt","target_operators":` + invalid + `}`)
		assert.True(t, present, invalid)
		assert.Equal(t, constants.ErrHarnessTargetsInvalid.Error(), problem, invalid)
	}

	rest, selectors, present, problem = splitTargetOperators(`not json`)
	assert.Equal(t, `not json`, rest, "the contract decoder rejects it")
	assert.Nil(t, selectors)
	assert.False(t, present)
	assert.Empty(t, problem)
}

func TestDecodeAgentToolRequest_RoutesTargetsOutsideTheToolContract(t *testing.T) {
	spec, err := catalogToolSpec(constants.MCPToolReadFile)
	require.NoError(t, err)
	require.Contains(t, spec.Arguments(), harnessTargetOperatorsArgument, "every Operator tool declares target_operators")

	request := decodeAgentToolRequest(spec, true, `{"path":"app/config.yaml","target_operators":["all"]}`)
	require.Nil(t, request.Failure)
	assert.JSONEq(t, `{"path":"app/config.yaml"}`, request.ArgumentsJSON, "the tool never receives the routing argument")
	assert.Equal(t, []string{"all"}, request.TargetSelectors)
	assert.True(t, request.TargetsPresent)

	request = decodeAgentToolRequest(spec, true, `{"path":"app/config.yaml","target_operators":"all"}`)
	require.NotNil(t, request.Failure)
	assert.Contains(t, request.Failure.Error, harnessTargetOperatorsArgument)
}

func TestDecodeAgentToolRequest_RejectsTrailingJSONAfterTargets(t *testing.T) {
	spec, err := catalogToolSpec(constants.MCPToolReadFile)
	require.NoError(t, err)
	for _, trailing := range []string{`{}`, `null`, `true`, `invalid`} {
		t.Run(trailing, func(t *testing.T) {
			request := decodeAgentToolRequest(spec, true, `{"path":"app/config.yaml","target_operators":["all"]} `+trailing)
			require.NotNil(t, request.Failure, "removing routing arguments must not hide malformed JSON")
			assert.Equal(t, agentToolErrorExecution, request.Failure.ErrorType)
			assert.Contains(t, request.Failure.Error, "Invalid JSON object")
		})
	}
}

func TestFanOutResultJSON(t *testing.T) {
	roster := fleetRoster()
	single := []AgentToolCall{{TargetIndex: 2, ResultJSON: `{"ok":true}`}}
	got, err := fanOutResultJSON(roster, single)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, got, "one target answers with its own result")

	group := []AgentToolCall{{TargetIndex: 1, ResultJSON: `{"exit_code":0}`}, {TargetIndex: 3, ResultJSON: `{"error":"x"}`}}
	got, err = fanOutResultJSON(roster, group)
	require.NoError(t, err)
	var decoded fanOutResult
	require.NoError(t, json.Unmarshal([]byte(got), &decoded))
	require.Len(t, decoded.Targets, 2)
	assert.Equal(t, 1, decoded.Targets[0].RosterIndex)
	assert.Equal(t, "op-a", decoded.Targets[0].OperatorID)
	assert.Equal(t, "db-host", decoded.Targets[1].Hostname)
	assert.JSONEq(t, `{"error":"x"}`, string(decoded.Targets[1].Result))

	_, err = fanOutResultJSON(roster, []AgentToolCall{{TargetIndex: 1}, {TargetIndex: 9}})
	require.ErrorIs(t, err, constants.ErrHarnessFanOutMismatch)
}

func TestHarnessDispatchArguments_ResolvesPathsInsideEachTarget(t *testing.T) {
	roster := fleetRoster()
	spec, err := catalogToolSpec(constants.MCPToolReadFile)
	require.NoError(t, err)
	for _, member := range roster {
		got, failure, err := harnessDispatchArguments(spec, `{"path":"./app/config.yaml"}`, member, "case-1")
		require.NoError(t, err)
		require.Nil(t, failure)
		assert.JSONEq(t, `{"path":"`+member.Workspace.Root+`/app/config.yaml"}`, got, "one relative path names the file on every target")
	}
	_, failure, err := harnessDispatchArguments(spec, `{"path":"`+roster[0].Workspace.Root+`/app/config.yaml"}`, roster[1], "case-1")
	require.NoError(t, err)
	require.NotNil(t, failure, "member 1's absolute path is outside member 2's workspace")

	shell, err := catalogToolSpec(constants.MCPToolRunShellCommand)
	require.NoError(t, err)
	got, failure, err := harnessDispatchArguments(shell, models.RunShellCommandRequest{Command: "ls", WorkingDir: "logs"}.CanonicalJSON(), roster[2], "case-1")
	require.NoError(t, err)
	require.Nil(t, failure)
	var request models.RunShellCommandRequest
	require.NoError(t, json.Unmarshal([]byte(got), &request))
	assert.Equal(t, roster[2].Workspace.Root+"/logs", request.WorkingDir)
}

// fleetTools records each governed dispatch's target; fan-out dispatches
// arrive concurrently.
type fleetTools struct {
	tribunalAuthoringTools
	mu      sync.Mutex
	targets []string
	args    map[string]string
}

func (f *fleetTools) DispatchAgentTool(_ context.Context, target Target, _, _, _, _ string, _ constants.ActionType, _ string, payload []byte) (GovernedToolDispatch, error) {
	var call operatorv1.McpCallRequested
	if err := proto.Unmarshal(payload, &call); err != nil {
		return GovernedToolDispatch{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targets = append(f.targets, target.OperatorID)
	if f.args == nil {
		f.args = make(map[string]string)
	}
	f.args[target.OperatorID] = call.GetArgumentsJson()
	outcome := shellToolOutcome(`{"exit_code":0,"stdout":"` + target.OperatorID + `"}`)
	return GovernedToolDispatch{Outcome: outcome, TransactionID: outcome.Observation.GetReceipt().GetTransactionId()}, nil
}

func TestRunTool_FansOneCallOutAcrossTheNamedTargets(t *testing.T) {
	tools := &fleetTools{}
	run := newTribunalAuthoringRun(t, false, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
	run.executor.tools = tools
	ids := 0
	run.executor.newID = func(prefix string) string { ids++; return prefix + "-" + string(rune('0'+ids)) }
	run.trace.Roster = fleetRoster()

	group, err := run.runTool(t.Context(), 1, "model-call-1", &operatorv1.InferenceToolCall{
		CallId: "call-1", Name: constants.MCPToolRunShellCommand, ArgumentsJson: `{"command":"echo","target_operators":["3","1"]}`,
	})
	require.NoError(t, err)
	require.Len(t, group, 2)
	assert.Equal(t, []int{1, 3}, []int{group[0].TargetIndex, group[1].TargetIndex})
	assert.Equal(t, group[0].CallID, group[0].GroupID)
	assert.Equal(t, group[0].GroupID, group[1].GroupID)
	assert.NotEqual(t, group[0].CallID, group[1].CallID)
	assert.Equal(t, []string{"3", "1"}, group[0].TargetOperators)
	assert.Equal(t, models.RunShellCommandRequest{Command: "echo"}.CanonicalJSON(), group[0].ArgumentsJSON)

	sort.Strings(tools.targets)
	assert.Equal(t, []string{"op-a", "op-c"}, tools.targets, "the untargeted member is never dispatched to")
	var dispatched models.RunShellCommandRequest
	require.NoError(t, json.Unmarshal([]byte(tools.args["op-c"]), &dispatched))
	assert.Equal(t, run.trace.Roster[2].Workspace.Root, dispatched.WorkingDir, "each target runs in its own workspace")

	result, err := fanOutResultJSON(run.trace.Roster, group)
	require.NoError(t, err)
	assert.Contains(t, result, `"roster_index":3`)
}

func TestRunTool_FleetCallWithoutTargetsFailsClosedBeforeDispatch(t *testing.T) {
	tools := &fleetTools{}
	run := newTribunalAuthoringRun(t, false, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
	run.executor.tools = tools
	run.trace.Roster = fleetRoster()

	group, err := run.runTool(t.Context(), 1, "model-call-1", &operatorv1.InferenceToolCall{
		CallId: "call-1", Name: constants.MCPToolRunShellCommand, ArgumentsJson: `{"command":"echo"}`,
	})
	require.NoError(t, err)
	require.Len(t, group, 1)
	assert.Zero(t, group[0].TargetIndex)
	assert.False(t, group[0].Success)
	assert.Contains(t, group[0].Error, harnessTargetOperatorsArgument)
	assert.Empty(t, tools.targets, "a call that names no target dispatches nothing")
}

// fleetEvidenceTrace returns a trace on two Data runtimes: member 1 executes
// one tool call between its sentinels, member 2 is bracketed with nothing
// between them.
func fleetEvidenceTrace(t *testing.T) (*AgentTrace, *evidenceLedger, *operatorv1.EventReceipt) {
	t.Helper()
	first := newRuntimeLedger(t, oracletest.NewSigner(t), "operator-data-a", "session-a")
	second := newRuntimeLedger(t, oracletest.NewSigner(t), "operator-data-b", "session-b")
	inference := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
	trace := evidenceTrace(inference.runtime(), first.runtime())
	trace.Roster = append(trace.Roster, AgentTraceRosterMember{Index: 2, Runtime: second.runtime()})
	trace.Input = testAgentTraceInput()
	withRosterPrompt(trace)
	req1, call1 := prepareShellToolCall(t, trace, "tx-m1", "call-1", "echo")
	m1 := sealModelTurn(t, inference, trace, "tx-m1", 1, req1)
	beforeA := first.executed("tx-a-before", string(constants.ActionTypeFsRead))
	call1 = commitShellToolCall(t, first, call1, "tx-1")
	afterA := first.executed("tx-a-after", string(constants.ActionTypeFsRead))
	beforeB := second.executed("tx-b-before", string(constants.ActionTypeFsRead))
	trace.Roster[0].Bracket = bracketOf(beforeA, afterA)
	trace.ModelCalls = []AgentModelCall{m1}
	trace.ToolCalls = []AgentToolCall{call1}
	return trace, second, beforeB
}

func TestVerifyAgentTraceEvidence_FleetBracketsEveryMember(t *testing.T) {
	t.Run("an untargeted member with an empty bracket verifies", func(t *testing.T) {
		trace, second, beforeB := fleetEvidenceTrace(t)
		trace.Roster[1].Bracket = bracketOf(beforeB, second.executed("tx-b-after", string(constants.ActionTypeFsRead)))
		require.NoError(t, VerifyAgentTraceEvidence(trace))
	})
	t.Run("an execution on an untargeted member is unclaimed", func(t *testing.T) {
		trace, second, beforeB := fleetEvidenceTrace(t)
		_ = second.executed("tx-stray", string(constants.ActionTypeExecuteBash))
		trace.Roster[1].Bracket = bracketOf(beforeB, second.executed("tx-b-after", string(constants.ActionTypeFsRead)))
		requireEvidenceError(t, trace, constants.ErrConformanceUnclaimedExecution)
	})
	t.Run("a member left unbracketed fails", func(t *testing.T) {
		trace, _, _ := fleetEvidenceTrace(t)
		trace.Roster[1].Bracket = nil
		requireEvidenceError(t, trace, constants.ErrMissingRequiredField)
	})
	t.Run("a receipt claimed on the wrong member fails", func(t *testing.T) {
		trace, second, beforeB := fleetEvidenceTrace(t)
		trace.Roster[1].Bracket = bracketOf(beforeB, second.executed("tx-b-after", string(constants.ActionTypeFsRead)))
		trace.ToolCalls[0].TargetIndex = 2
		err := VerifyAgentTraceEvidence(trace)
		require.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	})
}

func TestRenderRosterUpdateMessage(t *testing.T) {
	msg := renderRosterUpdateMessage(AgentTraceRosterMember{
		Index:        3,
		Runtime:      AgentTraceRuntime{OperatorID: "op-c", SessionID: "op-c-session"},
		Hostname:     "app-03",
		OS:           "linux",
		Architecture: "amd64",
		Workspace:    AgentTraceWorkspace{Root: "/srv/op-c/workspaces/ws-1"},
	})
	want := "<operator_roster_update>\n" +
		"Added Operator [3] operator_id=op-c hostname=app-03 os=linux arch=amd64 workspace=/srv/op-c/workspaces/ws-1\n" +
		"Use target_operators (roster indexes, Operator IDs, hostnames, or [\"all\"]) to target it.\n" +
		"</operator_roster_update>\n"
	assert.Equal(t, want, msg)
}

func TestAgentTrace_InitialRosterJoinsAndConversation(t *testing.T) {
	roster := fleetRoster()
	joined := roster[2]
	trace := &AgentTrace{
		Roster: roster,
		RosterJoins: []AgentTraceRosterJoin{{
			Index:           3,
			Runtime:         joined.Runtime,
			JoinedAfterCall: "tc-deploy",
		}},
		Input: &AgentTraceInput{
			Messages: []AgentTraceMessage{
				{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM.String(), Text: renderRosterPrompt(roster[:2])},
				{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String(), Text: "Deploy app-03 and read it."},
			},
		},
		ModelCalls: []AgentModelCall{
			{
				ProviderAttemptID: "m-1",
				TransactionID:     "tx-m1",
				Purpose:           agentModelCallPurposeTurn,
				LoopTurn:          1,
				ToolCallCount:     1,
				Text:              "Deploying app-03.",
			},
			{
				ProviderAttemptID: "m-2",
				TransactionID:     "tx-m2",
				Purpose:           agentModelCallPurposeTurn,
				LoopTurn:          2,
				ToolCallCount:     0,
				Text:              "Done.",
			},
		},
		ToolCalls: []AgentToolCall{
			{
				CallID:          "tc-deploy",
				GroupID:         "tc-deploy",
				ModelCallID:     "m-1",
				ProviderCallID:  "call-1",
				ToolName:        constants.MCPToolOperatorDeploy,
				TargetOperators: []string{"1"},
				TargetIndex:     1,
				ArgumentsJSON:   `{"hostnames":["app-03"]}`,
				ResultJSON:      `{"execution_id":"tc-deploy","status":"EXECUTION_STATUS_COMPLETED","deployments":[{"hostname":"app-03","deployment_id":"dep-1","success":true}]}`,
				Success:         true,
				LoopTurn:        1,
			},
		},
	}

	assert.Len(t, trace.InitialRoster(), 2)
	assert.Equal(t, []AgentTraceRosterMember{roster[0], roster[1]}, trace.InitialRoster())
	require.NoError(t, validateAgentTraceRosterJoins(trace))

	conv, err := trace.Conversation()
	require.NoError(t, err)
	require.Len(t, conv, 6)
	assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT.String(), conv[2].Role)
	assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL.String(), conv[3].Role)
	assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String(), conv[4].Role)
	assert.Equal(t, renderRosterUpdateMessage(joined), conv[4].Text)
	assert.Equal(t, "Done.", conv[5].Text)

	t.Run("rejects non-contiguous index", func(t *testing.T) {
		bad := *trace
		bad.RosterJoins = []AgentTraceRosterJoin{{Index: 4, Runtime: joined.Runtime, JoinedAfterCall: "tc-deploy"}}
		require.ErrorIs(t, validateAgentTraceRosterJoins(&bad), constants.ErrHarnessRosterMismatch)
	})
	t.Run("rejects runtime mismatch", func(t *testing.T) {
		bad := *trace
		bad.RosterJoins = []AgentTraceRosterJoin{{Index: 3, Runtime: AgentTraceRuntime{OperatorID: "other", SessionID: "s"}, JoinedAfterCall: "tc-deploy"}}
		require.ErrorIs(t, validateAgentTraceRosterJoins(&bad), constants.ErrHarnessRosterMismatch)
	})
	t.Run("rejects empty joined_after_call", func(t *testing.T) {
		bad := *trace
		bad.RosterJoins = []AgentTraceRosterJoin{{Index: 3, Runtime: joined.Runtime, JoinedAfterCall: ""}}
		require.ErrorIs(t, validateAgentTraceRosterJoins(&bad), constants.ErrMissingRequiredField)
	})
	t.Run("rejects non-deploy or failed call", func(t *testing.T) {
		bad := *trace
		bad.ToolCalls = []AgentToolCall{{CallID: "tc-deploy", ToolName: constants.MCPToolReadFile, Success: true}}
		require.ErrorIs(t, validateAgentTraceRosterJoins(&bad), constants.ErrHarnessRosterMismatch)

		bad.ToolCalls = []AgentToolCall{{CallID: "tc-deploy", ToolName: constants.MCPToolOperatorDeploy, Success: false}}
		require.ErrorIs(t, validateAgentTraceRosterJoins(&bad), constants.ErrHarnessRosterMismatch)
	})
}

type fakeDeployJoiner struct {
	mu       sync.Mutex
	watched  []string
	awaited  []string
	closed   bool
	operator CampaignDataOperator
	events   *[]string
}

func (f *fakeDeployJoiner) WatchDeployment(_ context.Context, deploymentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watched = append(f.watched, deploymentID)
	if f.events != nil {
		*f.events = append(*f.events, "watch:"+deploymentID)
	}
	return nil
}

func (f *fakeDeployJoiner) AwaitJoinedOperator(_ context.Context, deploymentID, hostname string) (CampaignDataOperator, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.awaited = append(f.awaited, deploymentID+":"+hostname)
	if f.events != nil {
		*f.events = append(*f.events, "await:"+deploymentID)
	}
	return f.operator, nil
}

func (f *fakeDeployJoiner) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
}

type deployJoinTestInference struct {
	mu       sync.Mutex
	turn     int
	requests [][]string
}

func (d *deployJoinTestInference) DispatchIntent(_ context.Context, _ *tribunalv1.TribunalIntentRequest) (*tribunalv1.TribunalIntentResult, error) {
	return nil, nil
}

func (d *deployJoinTestInference) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.turn++
	var msgs []string
	for _, m := range req.GetMessages() {
		var text strings.Builder
		for _, p := range m.GetParts() {
			text.WriteString(p.GetText())
		}
		msgs = append(msgs, m.GetRole().String()+":"+text.String())
	}
	d.requests = append(d.requests, msgs)

	var result *operatorv1.InferenceResult
	switch d.turn {
	case 1:
		result = &operatorv1.InferenceResult{
			ProviderAttemptId:    req.GetProviderAttemptId(),
			CampaignId:           req.GetCampaignId(),
			RunId:                req.GetRunId(),
			AssignmentId:         req.GetAssignmentId(),
			EvaluationAttemptId:  req.GetEvaluationAttemptId(),
			ScenarioId:           req.GetScenarioId(),
			ModelRegistryDigest:  req.GetModelRegistryDigest(),
			RequestedModel:       req.GetModel(),
			Model:                req.GetModel(),
			RequestedModelDigest: req.GetModelDigest(),
			ServedModelDigest:    req.GetModelDigest(),
			FinishReason:         "tool_calls",
			Parts: []*operatorv1.InferenceResponsePart{{
				Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
					CallId:        "call-deploy",
					Name:          constants.MCPToolOperatorDeploy,
					ArgumentsJson: `{"hostnames":["app-03"],"target_operators":["1"]}`,
				}},
			}},
		}
	case 2:
		result = &operatorv1.InferenceResult{
			ProviderAttemptId:    req.GetProviderAttemptId(),
			CampaignId:           req.GetCampaignId(),
			RunId:                req.GetRunId(),
			AssignmentId:         req.GetAssignmentId(),
			EvaluationAttemptId:  req.GetEvaluationAttemptId(),
			ScenarioId:           req.GetScenarioId(),
			ModelRegistryDigest:  req.GetModelRegistryDigest(),
			RequestedModel:       req.GetModel(),
			Model:                req.GetModel(),
			RequestedModelDigest: req.GetModelDigest(),
			ServedModelDigest:    req.GetModelDigest(),
			FinishReason:         "tool_calls",
			Parts: []*operatorv1.InferenceResponsePart{{
				Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
					CallId:        "call-read-joined",
					Name:          constants.MCPToolReadFile,
					ArgumentsJson: `{"path":"./app/config.yaml","target_operators":["3"]}`,
				}},
			}},
		}
	default:
		result = &operatorv1.InferenceResult{
			ProviderAttemptId:    req.GetProviderAttemptId(),
			CampaignId:           req.GetCampaignId(),
			RunId:                req.GetRunId(),
			AssignmentId:         req.GetAssignmentId(),
			EvaluationAttemptId:  req.GetEvaluationAttemptId(),
			ScenarioId:           req.GetScenarioId(),
			ModelRegistryDigest:  req.GetModelRegistryDigest(),
			RequestedModel:       req.GetModel(),
			Model:                req.GetModel(),
			RequestedModelDigest: req.GetModelDigest(),
			ServedModelDigest:    req.GetModelDigest(),
			FinishReason:         "stop",
			Parts: []*operatorv1.InferenceResponsePart{{
				Part: &operatorv1.InferenceResponsePart_Text{Text: "Deployed app-03 and verified config."},
			}},
		}
	}
	digest, err := models.ComputeInferenceResultDigest(result)
	if err != nil {
		return nil, err
	}
	result.ResultDigest = digest
	return &operatorv1.InferenceDispatchResponse{
		TransactionId: "tx-inf-" + req.GetProviderAttemptId(),
		Receipt:       &operatorv1.EventReceipt{TransactionId: "tx-inf-" + req.GetProviderAttemptId(), Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, ResultSummary: digest},
		Result:        result,
	}, nil
}

type deployJoinTestTools struct {
	mu           sync.Mutex
	writtenFiles []string
	dispatched   []string
	events       *[]string
	deployCallID string
}

func (t *deployJoinTestTools) WriteWorkspaceFile(_ context.Context, target Target, _, _, _, path, _ string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writtenFiles = append(t.writtenFiles, target.OperatorID+":"+path)
	return nil
}

func (t *deployJoinTestTools) DispatchAgentTool(_ context.Context, target Target, _, _, _, _ string, action constants.ActionType, _ string, payload []byte) (GovernedToolDispatch, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.dispatched = append(t.dispatched, target.OperatorID+":"+string(action))
	if action == constants.ActionTypeOperatorDeploy {
		var req operatorv1.OperatorDeployRequested
		if err := proto.Unmarshal(payload, &req); err != nil {
			return GovernedToolDispatch{}, err
		}
		t.deployCallID = req.GetExecutionId()
		if t.events != nil {
			*t.events = append(*t.events, "dispatch:"+req.GetExecutionId())
		}
		depID := operatordeploy.DeploymentIDForExecution(req.GetExecutionId(), 0)
		summaryBytes, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(&operatorv1.OperatorDeployResult{
			ExecutionId: req.GetExecutionId(),
			Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			Deployments: []*operatorv1.OperatorDeployedHost{{
				Hostname:     "app-03",
				DeploymentId: depID,
				Phase:        string(models.OperatorDeploymentPhasePendingApproval),
				WorkingDir:   "/srv/op-c",
				Success:      true,
			}},
		})
		if err != nil {
			return GovernedToolDispatch{}, err
		}
		receipt := &operatorv1.EventReceipt{
			TransactionId: "tx-deploy",
			ActionType:    string(constants.ActionTypeOperatorDeploy),
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			ResultSummary: string(summaryBytes),
		}
		return GovernedToolDispatch{
			TransactionID: "tx-deploy",
			Outcome: &client.GovernedOutcome{
				Observation: &evalv1.ConformanceObservation{
					Kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
					Receipt: receipt,
				},
				Dispatch: &operatorv1.GovernedDispatchResult{TransactionId: "tx-deploy", Receipt: receipt},
			},
		}, nil
	}
	receipt := &operatorv1.EventReceipt{
		TransactionId: "tx-read",
		ActionType:    string(constants.ActionTypeFsRead),
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary: `{"path":"/srv/op-c/app/config.yaml","content":"log_level: debug\n"}`,
	}
	return GovernedToolDispatch{
		TransactionID: "tx-read",
		Outcome: &client.GovernedOutcome{
			Observation: &evalv1.ConformanceObservation{
				Kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
				Receipt: receipt,
			},
			Dispatch: &operatorv1.GovernedDispatchResult{TransactionId: "tx-read", Receipt: receipt},
		},
	}, nil
}

type deployJoinTestSentinel struct {
	mu    sync.Mutex
	reads []string
}

func (s *deployJoinTestSentinel) ReadSession(_ context.Context, operatorID, sessionID, readAbs string) (*operatorv1.EventReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, operatorID+":"+sessionID+":"+readAbs)
	return &operatorv1.EventReceipt{TransactionId: "tx-sentinel-" + operatorID, Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}, nil
}

func TestExecutor_DeployJoinerJoinsAndMaterializesOperator(t *testing.T) {
	var events []string
	joiner := &fakeDeployJoiner{
		events: &events,
		operator: CampaignDataOperator{
			CampaignOperatorRuntime: CampaignOperatorRuntime{OperatorID: "op-c", OperatorSessionID: "op-c-session"},
			WorkingDirectory:        "/srv/op-c",
			Hostname:                "app-03",
			OS:                      "linux",
			Architecture:            "amd64",
		},
	}
	tools := &deployJoinTestTools{events: &events}
	sentinel := &deployJoinTestSentinel{}
	inference := &deployJoinTestInference{}
	ids := 0
	exec, err := NewCampaignAgentExecutor(inference, &recordingModelResidency{}, tools, sentinel, unusedTraceStore{}, nil, func(prefix string) string {
		ids++
		return prefix + "-" + string(rune('a'+ids-1))
	})
	require.NoError(t, err)
	exec.WithDeployJoinerFactory(func(context.Context) (RosterDeployJoiner, error) {
		return joiner, nil
	})

	run := newTribunalAuthoringRun(t, false, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
	run.executor = exec
	run.tools, err = exec.catalog.Scope(ScenarioToolExpectations{
		AllowedTools: []string{constants.MCPToolOperatorDeploy, constants.MCPToolReadFile},
	})
	require.NoError(t, err)
	run.req.ScenarioInput = ScenarioInputFixture{
		UserPrompt: "Deploy app-03 and read config.",
		WorkspaceFiles: []ScenarioWorkspaceFile{
			workspaceFile("app-config", "app/config.yaml", "log_level: debug\n", false),
			onMembers(workspaceFile("pending", "inventory/pending.yaml", "host: app-03\n", false), 1, 2),
		},
	}
	run.trace.Roster = fleetRoster()[:2]

	require.NoError(t, run.loop(t.Context()))
	assert.True(t, joiner.closed)
	require.Len(t, run.trace.Roster, 3)
	require.Len(t, run.trace.RosterJoins, 1)
	assert.Equal(t, 3, run.trace.RosterJoins[0].Index)
	assert.Equal(t, "op-c", run.trace.RosterJoins[0].Runtime.OperatorID)
	assert.Equal(t, tools.deployCallID, run.trace.RosterJoins[0].JoinedAfterCall)

	expectedDepID := operatordeploy.DeploymentIDForExecution(tools.deployCallID, 0)
	require.Equal(t, []string{"watch:" + expectedDepID, "dispatch:" + tools.deployCallID, "await:" + expectedDepID}, events, "WatchDeployment must run before DispatchAgentTool")
	assert.Contains(t, tools.dispatched, "op-c:"+string(constants.ActionTypeMcpCall))
	assert.Contains(t, sentinel.reads, "op-c:op-c-session:"+run.trace.Roster[2].ScenarioWorkspace().SentinelPath())

	var joinedConfigWritten, joinedPendingWritten bool
	for _, w := range tools.writtenFiles {
		if strings.HasPrefix(w, "op-c:") && strings.HasSuffix(w, "/app/config.yaml") {
			joinedConfigWritten = true
		}
		if strings.HasPrefix(w, "op-c:") && strings.HasSuffix(w, "/inventory/pending.yaml") {
			joinedPendingWritten = true
		}
	}
	assert.True(t, joinedConfigWritten, "shared workspace file should materialize on joined member [3]")
	assert.False(t, joinedPendingWritten, "member [1,2]-only file must not materialize on [3]")

	require.GreaterOrEqual(t, len(inference.requests), 2)
	assert.Contains(t, inference.requests[1], operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String()+":"+renderRosterUpdateMessage(run.trace.Roster[2]))

	t.Run("nil joiner fails closed when operator_deploy returns pending deployments", func(t *testing.T) {
		nilJoinerTools := &deployJoinTestTools{}
		nilJoinerInf := &deployJoinTestInference{}
		nilExec, err := NewCampaignAgentExecutor(nilJoinerInf, &recordingModelResidency{}, nilJoinerTools, &deployJoinTestSentinel{}, unusedTraceStore{}, nil, func(prefix string) string {
			ids++
			return prefix + "-" + string(rune('a'+ids-1))
		})
		require.NoError(t, err)
		nilRun := newTribunalAuthoringRun(t, false, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
		nilRun.executor = nilExec
		nilRun.tools, err = nilExec.catalog.Scope(ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolOperatorDeploy}})
		require.NoError(t, err)
		nilRun.req.ScenarioInput = ScenarioInputFixture{UserPrompt: "Deploy app-03."}
		nilRun.trace.Roster = fleetRoster()[:2]

		err = nilRun.loop(t.Context())
		require.ErrorIs(t, err, constants.ErrEvaluationAssignmentExecutionFailed)
	})
}
