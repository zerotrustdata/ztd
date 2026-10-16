// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	"github.com/zerotrustdata/ztd/internal/uuid"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// malformedDispatchBody is the truncated JSON that dispatch row 17 submits as a
// malformed request.
const malformedDispatchBody = `{"target_operator_session_id":`

// Dispatch submits attempts to the governed HTTP dispatch ingress
// (POST /api/v1/operators/commands) for the embedded and outbound executors.
type Dispatch struct {
	client  *client.Client
	persona client.Persona
	targets map[cases.Executor]operator.Target
}

// NewDispatch requires at least one target. Drive refuses an executor with no target.
func NewDispatch(c *client.Client, p client.Persona, targets map[cases.Executor]operator.Target) (*Dispatch, error) {
	if c == nil {
		return nil, fmt.Errorf("dispatch: client: %w", constants.ErrMissingRequiredField)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("dispatch: targets: %w", constants.ErrMissingRequiredField)
	}
	return &Dispatch{client: c, persona: p, targets: targets}, nil
}

// Drive submits one attempt and returns its one observation. A transport error
// (no HTTP answer) is returned as an error.
func (d *Dispatch) Drive(ctx context.Context, a cases.Attempt) (cases.Driven, error) {
	if a.Case.Ingress != cases.IngressDispatchEmbedded && a.Case.Ingress != cases.IngressDispatchOutbound {
		return cases.Driven{}, fmt.Errorf("dispatch %s: ingress %s: %w", a.Case.Fault, a.Case.Ingress, constants.ErrConformanceIngressUnsupported)
	}
	target, ok := d.targets[a.Case.Ingress.Executor()]
	if !ok {
		return cases.Driven{}, fmt.Errorf("dispatch %s: executor %s: %w", a.Case.Fault, a.Case.Ingress.Executor(), constants.ErrConformanceIngressUnsupported)
	}

	if a.Case.Fault == cases.FaultMalformedJSON {
		outcome, err := d.client.DispatchCommandBody(ctx, d.persona, []byte(malformedDispatchBody))
		return dispatchDriven(a, outcome, err)
	}

	req, err := dispatchRequest(a, target, d.persona)
	if err != nil {
		return cases.Driven{}, err
	}

	switch a.Case.Fault {
	case cases.FaultNone, cases.FaultReadBaseline, cases.FaultDoctrine, cases.FaultNoL3Path:
		outcome, err := d.client.DispatchCommand(ctx, d.persona, req)
		return dispatchDriven(a, outcome, err)
	case cases.FaultUnknownSession:
		session, err := uuid.NewString()
		if err != nil {
			return cases.Driven{}, fmt.Errorf("dispatch %s: session id: %w", a.Case.Fault, err)
		}
		req.TargetOperatorSessionID = session
		outcome, err := d.client.DispatchCommand(ctx, d.persona, req)
		return dispatchDriven(a, outcome, err)
	case cases.FaultUnknownEventType:
		req.EventType = unknownEventType
		return d.submitRaw(ctx, a, req)
	case cases.FaultMissingField:
		req.Payload = nil
		return d.submitRaw(ctx, a, req)
	default:
		return cases.Driven{}, fmt.Errorf("dispatch %s: %w", a.Case.Fault, constants.ErrConformanceIngressUnsupported)
	}
}

// submitRaw posts req unvalidated. The request is the fault itself, so the
// client-side checks in DispatchCommand must not run.
func (d *Dispatch) submitRaw(ctx context.Context, a cases.Attempt, req client.DispatchCommandRequest) (cases.Driven, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("dispatch %s: marshal request: %w", a.Case.Fault, err)
	}
	outcome, err := d.client.DispatchCommandBody(ctx, d.persona, body)
	return dispatchDriven(a, outcome, err)
}

// dispatchRequest builds the governed dispatch request for one attempt. Payload
// is the one owner of the action, event and payload for each case.
func dispatchRequest(a cases.Attempt, target operator.Target, p client.Persona) (client.DispatchCommandRequest, error) {
	action, event, msg, err := Payload(a)
	if err != nil {
		return client.DispatchCommandRequest{}, fmt.Errorf("dispatch %s: %w", a.Case.Fault, err)
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		return client.DispatchCommandRequest{}, fmt.Errorf("dispatch %s: marshal payload: %w", a.Case.Fault, err)
	}
	return client.DispatchCommandRequest{
		TargetOperatorSessionID: target.OperatorSessionID,
		ActionType:              string(action),
		EventType:               string(event),
		Payload:                 payload,
		CLISessionID:            p.CLISessionID,
	}, nil
}

// dispatchDriven wraps the one answer of a dispatch submission as an observation.
func dispatchDriven(a cases.Attempt, outcome *client.GovernedOutcome, err error) (cases.Driven, error) {
	if err != nil {
		return cases.Driven{}, fmt.Errorf("dispatch %s: submit: %w", a.Case.Fault, err)
	}
	return cases.Driven{Observations: []*evalv1.ConformanceObservation{Observe(outcome)}}, nil
}
