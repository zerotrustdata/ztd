// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

type capturingTribunalDispatcher struct{ requests []DispatchRequest }

func (d *capturingTribunalDispatcher) Dispatch(_ context.Context, req DispatchRequest) (*DispatchResult, error) {
	d.requests = append(d.requests, req)
	return &DispatchResult{TransactionID: "transaction"}, nil
}

func TestTribunalIntentDispatchUsesApprovedArgumentBytesOnce(t *testing.T) {
	dispatcher := &capturingTribunalDispatcher{}
	controller := &TribunalIntentController{commands: dispatcher}
	// Noncanonical whitespace proves the controller uses the approved bytes directly.
	approved := `{"command":"printf", "args":["%s", "a  b"],"timeout":17,"working_dir":"/workspace"}`
	req := &tribunalv1.TribunalIntentRequest{TargetOperatorSessionId: "operator-session", ExecutionId: "execution", WorkingDirectory: "/workspace", CaseId: "case", InvestigationId: "investigation", TaskId: "task", CliSessionId: "cli"}
	result := &tribunalv1.TribunalIntentResult{Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, ArgumentsJson: approved}
	controller.dispatchCommand(context.Background(), req, result, "app", "user")
	require.Len(t, dispatcher.requests, 1)
	captured := dispatcher.requests[0]
	event, err := constants.RequestEventForAction(constants.ActionTypeMcpCall)
	require.NoError(t, err)
	assert.Equal(t, string(event), captured.EventType)
	assert.Equal(t, constants.MCPToolRunShellCommand, captured.TargetResource)
	assert.Equal(t, "operator-session", captured.TargetOperatorSessionID)
	assert.Equal(t, "app", captured.ActingAppID)
	assert.Equal(t, "user", captured.RequestorUserID)
	assert.Equal(t, "case", captured.CaseID)
	assert.Equal(t, "investigation", captured.InvestigationID)
	assert.Equal(t, "task", captured.TaskID)
	assert.Equal(t, "cli", captured.CliSessionID)
	payload := &operatorv1.McpCallRequested{}
	require.NoError(t, proto.Unmarshal(captured.Payload, payload))
	assert.Equal(t, constants.MCPToolRunShellCommand, payload.GetToolName())
	assert.Equal(t, approved, payload.GetArgumentsJson())
	assert.Equal(t, "execution", payload.GetExecutionId())
	require.NotNil(t, result.GetDispatched())
}

func TestTribunalIntentUnapprovedDoesNotDispatch(t *testing.T) {
	for _, outcome := range []tribunalv1.TribunalOutcome{
		tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_NO_CONSENSUS,
		tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_BLOCKED,
		tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED,
		tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED,
	} {
		t.Run(outcome.String(), func(t *testing.T) {
			dispatcher := &capturingTribunalDispatcher{}
			controller := &TribunalIntentController{commands: dispatcher}
			result := &tribunalv1.TribunalIntentResult{Outcome: outcome}
			controller.dispatchCommand(context.Background(), &tribunalv1.TribunalIntentRequest{}, result, "app", "user")
			assert.Empty(t, dispatcher.requests)
			assert.Nil(t, result.CommandOutcome)
		})
	}
}
