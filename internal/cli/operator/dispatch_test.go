// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operator

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestBuildExecuteBashDispatchRequest(t *testing.T) {
	request, err := BuildExecuteBashDispatchRequest(
		"4881d566-90a9-44c9-9e3e-c6bb51e07f5c",
		"echo hello",
		"exec-1",
		"cli-1",
	)
	require.NoError(t, err)
	assert.Equal(t, "4881d566-90a9-44c9-9e3e-c6bb51e07f5c", request.TargetOperatorSessionID)
	assert.Equal(t, string(constants.Event.Operator.Command.Requested), request.EventType)
	assert.Equal(t, "cli-1", request.CLISessionID)

	var payload operatorv1.CommandRequested
	require.NoError(t, proto.Unmarshal(request.Payload, &payload))
	assert.Equal(t, "echo hello", payload.Command)
	assert.Equal(t, "exec-1", payload.ExecutionId)
}

func TestBuildExecuteBashDispatchRequest_MissingFields(t *testing.T) {
	_, err := BuildExecuteBashDispatchRequest("", "echo", "exec-1", "cli-1")
	require.Error(t, err)
}

func TestParseCommandResult_EmptyPayload(t *testing.T) {
	_, err := ParseCommandResult(&operatorv1.GovernedDispatchResult{
		EventType: "ztd.v1.operator.command.status.updated.running",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty command result payload")
}

func TestParseCommandResult_Success(t *testing.T) {
	payload, err := proto.Marshal(&operatorv1.CommandResult{
		Stdout:     "hello\n",
		ReturnCode: 0,
	})
	require.NoError(t, err)

	result, err := ParseCommandResult(&operatorv1.GovernedDispatchResult{
		EventType:     "ztd.v1.operator.command.completed",
		ActionType:    string(constants.ActionTypeExecuteBash),
		ResultPayload: payload,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello\n", result.Stdout)
	assert.Equal(t, int32(0), result.ReturnCode)
}

// truncatedProto is a length-delimited field whose declared length exceeds the
// bytes that follow, so proto.Unmarshal must reject it.
var truncatedProto = []byte{0x0a, 0x05, 'a'}

func TestMarshalExecuteBashPayload_RoundTripsEveryField(t *testing.T) {
	payload, err := MarshalExecuteBashPayload("ls -la /etc", "exec-9", "inspect config")
	require.NoError(t, err)

	var decoded operatorv1.CommandRequested
	require.NoError(t, proto.Unmarshal(payload, &decoded))

	assert.Equal(t, "ls -la /etc", decoded.Command)
	assert.Equal(t, "exec-9", decoded.ExecutionId)
	assert.Equal(t, "inspect config", decoded.Justification)
}

func TestBuildExecuteBashDispatchRequest_TrimsCommandAndStampsGovernedCLIMetadata(t *testing.T) {
	request, err := BuildExecuteBashDispatchRequest("session-1", "  \techo hi \n", "exec-1", "cli-1")
	require.NoError(t, err)

	var payload operatorv1.CommandRequested
	require.NoError(t, proto.Unmarshal(request.Payload, &payload))
	assert.Equal(t, "echo hi", payload.Command, "surrounding whitespace is not part of the command")
	assert.Equal(t, "ztd operator run", payload.Justification)
	assert.Equal(t, "cli", request.TargetResource)
}

func TestBuildExecuteBashDispatchRequest_RejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name        string
		sessionID   string
		command     string
		executionID string
		want        error
	}{
		{name: "empty operator session", sessionID: "", command: "echo", executionID: "exec-1", want: constants.ErrGatewayOperatorSessionIDRequired},
		{name: "empty command", sessionID: "session-1", command: "", executionID: "exec-1", want: constants.ErrMissingRequiredField},
		{name: "whitespace-only command", sessionID: "session-1", command: " \t\n", executionID: "exec-1", want: constants.ErrMissingRequiredField},
		{name: "empty execution id", sessionID: "session-1", command: "echo", executionID: "", want: constants.ErrMissingRequiredField},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request, err := BuildExecuteBashDispatchRequest(tt.sessionID, tt.command, tt.executionID, "cli-1")

			require.ErrorIs(t, err, tt.want)
			assert.Equal(t, models.OperatorCommandRequest{}, request, "no partial request may be returned on failure")
		})
	}
}

func TestBuildExecuteBashDispatchRequest_AllowsAnEmptyCLISessionAndOmitsItFromJSON(t *testing.T) {
	request, err := BuildExecuteBashDispatchRequest("session-1", "echo", "exec-1", "")
	require.NoError(t, err)

	encoded, err := json.Marshal(request)
	require.NoError(t, err)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	assert.NotContains(t, fields, "cli_session_id")
	assert.Equal(t, "session-1", fields["target_operator_session_id"])
	assert.Equal(t, "cli", fields["target_resource"])
	assert.Equal(t, string(constants.Event.Operator.Command.Requested), fields["event_type"])
	assert.NotEmpty(t, fields["payload"], "the protobuf payload travels base64 encoded")
}

// fakeCommandPoster records the request it was given and returns a canned
// response, so RunOnce is exercised without a Gateway.
type fakeCommandPoster struct {
	path string
	body any
	raw  []byte
	err  error
}

func (f *fakeCommandPoster) Post(path string, body any) ([]byte, error) {
	f.path = path
	f.body = body
	return f.raw, f.err
}

func marshalDispatchBody(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	body, err := protojson.Marshal(msg)
	require.NoError(t, err)
	return body
}

func marshalCommandResult(t *testing.T, result *operatorv1.CommandResult) []byte {
	t.Helper()
	payload, err := proto.Marshal(result)
	require.NoError(t, err)
	return payload
}

func TestRunOnce_DecodesEveryOutcome(t *testing.T) {
	signedRejection := &operatorv1.GovernanceRejection{
		Error: "rejected",
		EventReceipt: &operatorv1.EventReceipt{
			TransactionId: "tx-2",
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			FailureCode:   operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
			RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE,
			ResultSummary: "denied by doctrine",
			SignerKeyId:   "key-2",
		},
	}
	tests := []struct {
		name                   string
		raw                    []byte
		err                    error
		wantSuccess            bool
		wantTransaction        string
		wantError              string
		wantEventReceiptStatus string
		wantSignerKey          string
		wantStdout             string
		wantRejection          bool
		wantRejectionCode      string
		wantRefusalCode        string
	}{
		{
			name: "signed final event receipt and completed command",
			raw: marshalDispatchBody(t, &operatorv1.GovernedDispatchResult{
				TransactionId: "tx-1",
				EventType:     "ztd.v1.operator.command.completed",
				ActionType:    string(constants.ActionTypeExecuteBash),
				ResultPayload: marshalCommandResult(t, &operatorv1.CommandResult{
					Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
					Stdout: "hi\n",
				}),
				EventReceipt: &operatorv1.EventReceipt{
					TransactionId: "tx-1",
					Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
					SignerKeyId:   "key-1",
				},
			}),
			wantSuccess:            true,
			wantTransaction:        "tx-1",
			wantEventReceiptStatus: "EXECUTION_STATUS_COMPLETED",
			wantSignerKey:          "key-1",
			wantStdout:             "hi\n",
		},
		{
			name: "200 body without an event receipt fails closed",
			raw: marshalDispatchBody(t, &operatorv1.GovernedDispatchResult{
				TransactionId: "tx-1",
				ResultPayload: marshalCommandResult(t, &operatorv1.CommandResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}),
			}),
			wantTransaction: "tx-1",
			wantError:       "no event receipt",
		},
		{
			name: "completed event receipt with a failed command reports the command error",
			raw: marshalDispatchBody(t, &operatorv1.GovernedDispatchResult{
				TransactionId: "tx-1",
				ResultPayload: marshalCommandResult(t, &operatorv1.CommandResult{
					Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
					Error:  "command exited",
				}),
				EventReceipt: &operatorv1.EventReceipt{TransactionId: "tx-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED},
			}),
			wantTransaction:        "tx-1",
			wantError:              "command exited",
			wantEventReceiptStatus: "EXECUTION_STATUS_COMPLETED",
		},
		{
			name:      "malformed 200 body is an error",
			raw:       []byte("<html>proxy page</html>"),
			wantError: "decode governed dispatch result",
		},
		{
			name:              "signed L4 rejection",
			err:               &api.StatusError{StatusCode: http.StatusForbidden, Body: string(marshalDispatchBody(t, signedRejection))},
			wantTransaction:   "tx-2",
			wantError:         "denied by doctrine",
			wantRejection:     true,
			wantRejectionCode: "GOVERNANCE_REJECTION_CODE_L1_DOCTRINE",
		},
		{
			name:            "typed refusal before L4",
			err:             &api.StatusError{StatusCode: http.StatusForbidden, Body: string(marshalDispatchBody(t, &operatorv1.IngressRefusal{Code: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED, Error: "method refused"}))},
			wantError:       "method refused",
			wantRefusalCode: "INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED",
		},
		{
			name:      "untyped non-2xx body keeps the transport error",
			err:       &api.StatusError{StatusCode: http.StatusBadGateway, Body: "<html>502 Bad Gateway</html>"},
			wantError: "status 502",
		},
		{
			name:      "transport failure without a status",
			err:       errors.New("connection refused"),
			wantError: "connection refused",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			poster := &fakeCommandPoster{raw: tt.raw, err: tt.err}

			result := RunOnce(poster, Target{OperatorSessionID: "session-1", OperatorID: "op-1"}, "echo hi", "exec-1", "cli-1")

			assert.Equal(t, constants.APIPaths.OperatorsCommands, poster.path)
			assert.Equal(t, "session-1", result.OperatorSessionID)
			assert.Equal(t, "op-1", result.OperatorID)
			assert.Equal(t, tt.wantSuccess, result.Success)
			assert.Equal(t, tt.wantTransaction, result.TransactionID)
			if tt.wantError != "" {
				assert.Contains(t, result.Error, tt.wantError)
			} else {
				assert.Empty(t, result.Error)
			}
			assert.Equal(t, tt.wantEventReceiptStatus, result.EventReceiptStatus)
			assert.Equal(t, tt.wantSignerKey, result.SignerKeyID)
			assert.Equal(t, tt.wantStdout, result.Stdout)
			assert.Equal(t, tt.wantRejection, result.Rejection != nil)
			assert.Equal(t, tt.wantRejectionCode, result.RejectionCode)
			assert.Equal(t, tt.wantRefusalCode, result.RefusalCode)
			if tt.wantSuccess {
				assert.NotNil(t, result.EventReceipt, "a success carries the verified final event receipt")
			}
		})
	}
}

func TestParseCommandResult_NilResponseIsAMissingRequiredField(t *testing.T) {
	got, err := ParseCommandResult(nil)

	assert.Nil(t, got)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestParseCommandResult_EmptyPayloadErrorIdentifiesTheOffendingEvent(t *testing.T) {
	_, err := ParseCommandResult(&operatorv1.GovernedDispatchResult{
		EventType:  "ztd.v1.operator.command.status.updated.running",
		ActionType: string(constants.ActionTypeExecuteBash),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "event_type=ztd.v1.operator.command.status.updated.running")
	assert.Contains(t, err.Error(), "action_type="+string(constants.ActionTypeExecuteBash))
}

func TestParseCommandResult_CorruptPayloadWrapsTheProtobufError(t *testing.T) {
	_, err := ParseCommandResult(&operatorv1.GovernedDispatchResult{ResultPayload: truncatedProto})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "operator dispatch: decode command result")
	assert.NotNil(t, errors.Unwrap(err), "the underlying protobuf error must stay reachable")
}

func TestSummarizeRunPercentilesUseSuccessfulDispatchesOnly(t *testing.T) {
	results := make([]RunResult, 0, 102)
	for i := 1; i <= 100; i++ {
		results = append(results, RunResult{Success: true, DurationMs: float64(i)})
	}
	results = append(results, RunResult{Success: false, DurationMs: 1}, RunResult{Success: false, DurationMs: 9000})

	summary := SummarizeRun(results, 2500*time.Millisecond, 16)

	assert.Equal(t, RunSummary{
		Targets:     102,
		Succeeded:   100,
		Failed:      2,
		WallMs:      2500,
		P50Ms:       50,
		P95Ms:       95,
		P99Ms:       99,
		MaxMs:       100,
		Concurrency: 16,
	}, summary)
}

func TestSummarizeRunWithoutSuccessReportsZeroLatency(t *testing.T) {
	summary := SummarizeRun([]RunResult{{DurationMs: 40}}, time.Second, 1)

	assert.Equal(t, 1, summary.Failed)
	assert.Zero(t, summary.Succeeded)
	assert.Zero(t, summary.P99Ms)
	assert.Zero(t, summary.MaxMs)
}
