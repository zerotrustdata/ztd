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

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// ConformanceA2ASkill is the only skill the conformance A2A stub accepts as a
// case skill. The verifier decides on the recorded calls.
const ConformanceA2ASkill = "ztd-conformance"

// A2A submits attempts to the Gateway's A2A route. The executing Operator is the
// embedded one, and the Gateway calls the stub as its A2A downstream.
type A2A struct {
	client  *client.Client
	persona client.Persona
	stub    *A2AStub
}

// NewA2A builds the A2A driver. stub is the downstream the Gateway was started with.
func NewA2A(c *client.Client, p client.Persona, stub *A2AStub) *A2A {
	return &A2A{client: c, persona: p, stub: stub}
}

// Drive submits one attempt as an A2A skill call. The stub records the calls the
// Gateway makes during the call; the answer is one observation and those calls.
func (d *A2A) Drive(ctx context.Context, a cases.Attempt) (cases.Driven, error) {
	payload, err := a2aPayload(a)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("a2a %s: %w", a.Case.Fault, err)
	}
	d.stub.Begin(a.AttemptID)
	outcome, callErr := d.client.A2ACall(ctx, d.persona, mcp.A2ACallRequest{SkillName: ConformanceA2ASkill, Payload: payload, ExecutionID: a.AttemptID})
	// End runs on every path so the attempt is always closed, even when the call failed.
	calls := d.stub.End(a.AttemptID)
	if callErr != nil {
		return cases.Driven{}, fmt.Errorf("a2a %s: call: %w", a.Case.Fault, callErr)
	}
	return cases.Driven{
		Observations: []*evalv1.ConformanceObservation{Observe(outcome)},
		A2ACalls:     calls,
	}, nil
}

// a2aPayload maps a fault to the skill payload that submits it, as the raw JSON
// the A2A request carries.
func a2aPayload(a cases.Attempt) (json.RawMessage, error) {
	var payload any
	switch a.Case.Fault {
	case cases.FaultNone, cases.FaultUnattendedMutation, cases.FaultAttendedApprove, cases.FaultAttendedDeny:
		payload = client.MarkerSkillPayload{Marker: a.Marker}
	case cases.FaultDoctrine:
		payload = client.CommandSkillPayload{Cmd: a.Case.Command}
	default:
		return nil, constants.ErrConformanceIngressUnsupported
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("a2a: marshal payload: %w", err)
	}
	return raw, nil
}
