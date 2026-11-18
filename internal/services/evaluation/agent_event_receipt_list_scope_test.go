// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const scopeTestInvestigationID = "investigation-04a18700-2a5c-4eb9-ae00-f64fd5fea1a3"

// eventReceiptListScopeCases are schema-valid event_receipt_list arguments a
// model can send. The out-of-scope ones previously dispatched, completed, and
// then failed the authoritative context decode with an unclassified
// ErrEvidenceScopeMismatch, which recorded the cell as INVALID_EVIDENCE
// internal_error instead of grading the model's argument mistake.
var eventReceiptListScopeCases = []struct {
	name    string
	args    string
	inScope bool
	field   string
}{
	{"empty arguments take injected defaults", `{}`, true, ""},
	{"current investigation", `{"investigation_id":"` + scopeTestInvestigationID + `"}`, true, ""},
	{"limit at the context bound", `{"limit":50}`, true, ""},
	{"zero limit takes the default", `{"limit":0}`, true, ""},
	{"mangled investigation ID", `{"investigation_id":"investigation-04a18700-2a5c-4eb9-ae00-f64fd5fea1a"}`, false, "investigation_id"},
	{"operator session filter", `{"operator_session_id":"5c35251f-f468-4874-b317-12a3cb6f9c8b"}`, false, "operator_session_id"},
	{"limit above the context bound", `{"limit":51}`, false, "limit"},
}

func eventReceiptListRequest(t *testing.T, raw string) agentToolRequest {
	t.Helper()
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	spec, ok := catalog.Tool(constants.MCPToolEventReceiptList)
	require.True(t, ok)
	request := decodeAgentToolRequest(spec, true, raw)
	require.Nil(t, request.Failure, "case arguments must satisfy the declared schema")
	return request
}

// The pre-dispatch scope check rejects exactly the queries the authoritative
// context decode would reject as out of scope, so no schema-valid model request
// can reach that decode and fail the assignment.
func TestEventReceiptListScopeFailure_MatchesAuthoritativeContextScope(t *testing.T) {
	trace := &AgentTrace{InvestigationID: scopeTestInvestigationID, Roster: rosterOfOne(AgentTraceRuntime{}, AgentTraceWorkspace{})}
	receipt := &AgentTraceReceipt{EventReceipt: &operatorv1.EventReceipt{}}
	for _, tt := range eventReceiptListScopeCases {
		t.Run(tt.name, func(t *testing.T) {
			args := eventReceiptListRequest(t, tt.args).Arguments
			failure := args.EventReceiptListScopeFailure(scopeTestInvestigationID)

			args.SetEventReceiptListDefaults(scopeTestInvestigationID)
			query, ok := args.EventReceiptList()
			require.True(t, ok)
			_, _, decodeErr := decodeInvestigationReceipts(trace, trace.Roster[0], query, receipt)

			if tt.inScope {
				assert.Nil(t, failure)
				assert.NotErrorIs(t, decodeErr, constants.ErrEvidenceScopeMismatch)
				return
			}
			require.NotNil(t, failure)
			assert.Equal(t, agentToolErrorExecution, failure.ErrorType)
			assert.Contains(t, failure.Error, constants.MCPToolEventReceiptList)
			assert.Contains(t, failure.Error, tt.field)
			require.ErrorIs(t, decodeErr, constants.ErrEvidenceScopeMismatch)
			assert.Equal(t, ExecutionFailureInternal, ClassifyExecutionFailure(assignmentExecutionError("scope", decodeErr)),
				"an out-of-scope query that reached the decode would fail the assignment as internal_error")
		})
	}
}

// An out-of-scope query is a model-visible tool failure recorded before any
// governed dispatch: the run has no executor here, so reaching dispatch panics.
func TestRunNativeMCPTool_RejectsOutOfScopeEventReceiptListBeforeDispatch(t *testing.T) {
	run := &agentRun{trace: &AgentTrace{InvestigationID: scopeTestInvestigationID,
		Roster: rosterOfOne(AgentTraceRuntime{OperatorID: "data-1", SessionID: "session-1"}, AgentTraceWorkspace{Root: "/work/case", OperatorWorkingDirectory: "/work"})}}
	spec, err := catalogToolSpec(constants.MCPToolEventReceiptList)
	require.NoError(t, err)
	for _, tt := range eventReceiptListScopeCases {
		if tt.inScope {
			continue
		}
		t.Run(tt.name, func(t *testing.T) {
			request := eventReceiptListRequest(t, tt.args)
			record := onTarget(AgentToolCall{ToolName: constants.MCPToolEventReceiptList, ArgumentsJSON: request.ArgumentsJSON})

			var completed AgentToolCall
			require.NotPanics(t, func() {
				// The pre-dispatch scope check runs in the target's dispatch
				// arguments, before any governed dispatch.
				target := run.runOnTarget(context.Background(), spec, request, record)
				completed, err = target.record, target.err
			})
			require.NoError(t, err)
			assert.False(t, completed.Success)
			assert.Nil(t, completed.Receipt)
			assert.Empty(t, completed.ActionType)
			assert.Equal(t, agentToolErrorExecution, completed.ErrorType)
			assert.Contains(t, completed.Error, tt.field)
			assert.Contains(t, completed.ResultJSON, tt.field)
		})
	}
}
