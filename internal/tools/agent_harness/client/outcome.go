// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

// GovernedOutcome is what one governed ingress call returned. Observation holds
// exactly one typed body matching its kind; Dispatch is set only on a dispatch
// success; Suspension only on an MCP or A2A suspension.
type GovernedOutcome struct {
	ConsensusInferences []*operatorv1.ConsensusVoteInference
	Observation         *evalv1.ConformanceObservation
	Dispatch            *operatorv1.GovernedDispatchResult
	Suspension          *mcp.GovernedSuspension
	Body                []byte
}

// Typed returns ErrIngressResponseUntyped (wrapped) when the ingress answered
// with no typed outcome. Callers that cannot grade an untyped answer call it.
func (o *GovernedOutcome) Typed() error {
	switch o.kind() {
	case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
		evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
		evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED:
		return nil
	default:
		return fmt.Errorf("governed outcome: %w", constants.ErrIngressResponseUntyped)
	}
}

func (o *GovernedOutcome) kind() evalv1.ConformanceObservationKind {
	if o == nil {
		return evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNSPECIFIED
	}
	return o.Observation.GetKind()
}

// decodeHTTPOutcome classifies a DIRECT or dispatch answer. A 2xx body must
// strictly decode into success; any other status must be a governance rejection
// or an ingress refusal.
func decodeHTTPOutcome(status int, body []byte, success proto.Message) *GovernedOutcome {
	return stampOutcome(status, body, decodeHTTPBody(status, body, success))
}

func decodeHTTPBody(status int, body []byte, success proto.Message) *GovernedOutcome {
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		rejection, refusal, err := governance.DecodeIngressFailure(body)
		switch {
		case err != nil:
			return untypedOutcome()
		case rejection != nil:
			return rejectionOutcome(rejection)
		default:
			return refusalOutcome(refusal)
		}
	}
	if err := decodeStrictProtoJSON(body, success); err != nil {
		return untypedOutcome()
	}
	switch msg := success.(type) {
	case *operatorv1.EventReceipt:
		if msg.GetTransactionId() == "" {
			return untypedOutcome()
		}
		return receiptOutcome(msg)
	case *operatorv1.GovernedDispatchResult:
		if msg.GetReceipt() == nil {
			return untypedOutcome()
		}
		outcome := receiptOutcome(msg.GetReceipt())
		outcome.Dispatch = msg
		outcome.ConsensusInferences = msg.GetConsensusInferences()
		return outcome
	default:
		return untypedOutcome()
	}
}

// IntentCommandOutcome classifies the governed MCP_CALL outcome of an approved
// Tribunal run_shell_command intent. An intent without a dispatch result or
// receipt is untyped.
func IntentCommandOutcome(result *tribunalv1.TribunalIntentResult) *GovernedOutcome {
	switch outcome := result.GetCommandOutcome().(type) {
	case *tribunalv1.TribunalIntentResult_Dispatched:
		if outcome.Dispatched.GetReceipt() == nil {
			return untypedOutcome()
		}
		governed := receiptOutcome(outcome.Dispatched.GetReceipt())
		governed.Dispatch = outcome.Dispatched
		governed.ConsensusInferences = outcome.Dispatched.GetConsensusInferences()
		return governed
	case *tribunalv1.TribunalIntentResult_Rejection:
		return rejectionOutcome(outcome.Rejection)
	case *tribunalv1.TribunalIntentResult_Refusal:
		return refusalOutcome(outcome.Refusal)
	default:
		return untypedOutcome()
	}
}

// MCPStdioOutcome classifies one tools/call response line read from the
// `ztd mcp stdio` bridge exactly as an MCP route answer is classified. A stdio
// answer has no HTTP status, so http_status stays 0.
func MCPStdioOutcome(raw []byte) *GovernedOutcome {
	return decodeRPCOutcome(0, raw, decodeMCPResult)
}

// decodeRPCOutcome classifies a JSON-RPC answer from an MCP or A2A route. The
// envelope is decoded strictly; error.data is a governance failure body and
// result is handed to the per-ingress result decoder.
func decodeRPCOutcome(status int, raw []byte, result func(json.RawMessage) *GovernedOutcome) *GovernedOutcome {
	return stampOutcome(status, raw, decodeRPCBody(raw, result))
}

func decodeRPCBody(raw []byte, result func(json.RawMessage) *GovernedOutcome) *GovernedOutcome {
	var envelope mcp.JSONRPCResponse
	if err := decodeStrictJSON(raw, &envelope); err != nil {
		return untypedOutcome()
	}
	switch {
	case envelope.Error != nil && envelope.Result != nil:
		return untypedOutcome()
	case envelope.Error != nil:
		if len(envelope.Error.Data) == 0 {
			return untypedOutcome()
		}
		rejection, refusal, err := governance.DecodeIngressFailure(envelope.Error.Data)
		switch {
		case err != nil:
			return untypedOutcome()
		case rejection != nil:
			return rejectionOutcome(rejection)
		default:
			return refusalOutcome(refusal)
		}
	case envelope.Result != nil:
		return result(envelope.Result)
	default:
		return untypedOutcome()
	}
}

// decodeMCPResult classifies a tools/call result: a typed suspension, or a
// signed receipt.
func decodeMCPResult(result json.RawMessage) *GovernedOutcome {
	var call mcp.CallToolResult
	if err := decodeStrictJSON(result, &call); err != nil {
		return untypedOutcome()
	}
	if call.Suspension != nil && call.Suspension.TxHash != "" {
		return suspendedOutcome(call.Suspension)
	}
	outcome := decodeReceiptJSON(call.Receipt)
	for _, inference := range call.ConsensusInferences {
		outcome.ConsensusInferences = append(outcome.ConsensusInferences, inference.ConsensusVoteInference)
	}
	return outcome
}

// decodeA2AResult classifies an a2a/call result: a typed suspension, or the
// success wrapper around a signed receipt.
func decodeA2AResult(result json.RawMessage) *GovernedOutcome {
	var suspension mcp.GovernedSuspension
	if err := decodeStrictJSON(result, &suspension); err == nil && suspension.Status != "" && suspension.TxHash != "" {
		return suspendedOutcome(&suspension)
	}
	var success mcp.A2ASuccessResponse
	if err := decodeStrictJSON(result, &success); err != nil {
		return untypedOutcome()
	}
	outcome := decodeReceiptJSON(success.Result)
	for _, inference := range success.ConsensusInferences {
		outcome.ConsensusInferences = append(outcome.ConsensusInferences, inference.ConsensusVoteInference)
	}
	return outcome
}

func decodeReceiptJSON(raw json.RawMessage) *GovernedOutcome {
	if len(raw) == 0 {
		return untypedOutcome()
	}
	receipt := &operatorv1.EventReceipt{}
	if err := decodeStrictProtoJSON(raw, receipt); err != nil {
		return untypedOutcome()
	}
	return receiptOutcome(receipt)
}

// decodeStrictJSON decodes exactly one JSON value into v, rejecting unknown
// fields and trailing data.
func decodeStrictJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data after JSON value", constants.ErrInvalidJSONResponse)
	}
	return nil
}

// decodeStrictProtoJSON decodes canonical protojson into msg without discarding
// unknown fields.
func decodeStrictProtoJSON(raw []byte, msg proto.Message) error {
	if err := (protojson.UnmarshalOptions{}).Unmarshal(raw, msg); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInvalidJSONResponse, err)
	}
	return nil
}

func untypedOutcome() *GovernedOutcome {
	return &GovernedOutcome{Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED}}
}

func receiptOutcome(receipt *operatorv1.EventReceipt) *GovernedOutcome {
	return &GovernedOutcome{Observation: &evalv1.ConformanceObservation{
		Kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		Receipt: receipt,
	}}
}

func rejectionOutcome(rejection *operatorv1.GovernanceRejection) *GovernedOutcome {
	return &GovernedOutcome{ConsensusInferences: rejection.GetConsensusInferences(), Observation: &evalv1.ConformanceObservation{
		Kind:      evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
		Rejection: rejection,
	}}
}

func refusalOutcome(refusal *operatorv1.IngressRefusal) *GovernedOutcome {
	return &GovernedOutcome{ConsensusInferences: refusal.GetConsensusInferences(), Observation: &evalv1.ConformanceObservation{
		Kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
		Refusal: refusal,
	}}
}

func suspendedOutcome(suspension *mcp.GovernedSuspension) *GovernedOutcome {
	return &GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{
			Kind:            evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED,
			SuspendedTxHash: suspension.TxHash,
		},
		Suspension: suspension,
	}
}

// stampOutcome records the HTTP status and raw body on every outcome. An
// untyped outcome keeps the raw body as its untyped_body evidence.
func stampOutcome(status int, body []byte, outcome *GovernedOutcome) *GovernedOutcome {
	outcome.Observation.HttpStatus = int32(status)
	outcome.Body = body
	if outcome.Observation.GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED {
		outcome.Observation.UntypedBody = body
	}
	return outcome
}
