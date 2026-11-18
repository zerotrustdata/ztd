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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
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
	model := inference.inference("tx-m1")
	beforeA := first.executed("tx-a-before", string(constants.ActionTypeFsRead))
	executed := first.executed("tx-1", string(constants.ActionTypeExecuteBash))
	afterA := first.executed("tx-a-after", string(constants.ActionTypeFsRead))
	beforeB := second.executed("tx-b-before", string(constants.ActionTypeFsRead))
	trace := evidenceTrace(inference.runtime(), first.runtime())
	trace.Roster = append(trace.Roster, AgentTraceRosterMember{Index: 2, Runtime: second.runtime()})
	trace.Roster[0].Bracket = bracketOf(beforeA, afterA)
	trace.ModelCalls = []AgentModelCall{completedModelCall("tx-m1", model)}
	trace.ToolCalls = []AgentToolCall{allowedToolCall("tx-1", executed)}
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
