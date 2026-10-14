// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"context"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// approvalStream is one open approval subscription. The driver opens it before the
// attended call is submitted, waits on it once the call suspends, and closes it.
type approvalStream interface {
	// Wait blocks until the human decides on txHash and returns the resumed, signed receipt.
	Wait(ctx context.Context, txHash, userID string) (*operatorv1.ActionReceipt, error)
	// Close ends the subscription.
	Close()
}

// approvalSubscriber opens an approval stream for a persona. The client is the
// production subscriber; tests fake it.
type approvalSubscriber interface {
	Subscribe(ctx context.Context, p client.Persona) (approvalStream, error)
}

// clientApprovals subscribes through the harness client's SubscribeApprovals.
type clientApprovals struct {
	c *client.Client
}

// Subscribe opens the client's live approval subscription.
func (ca clientApprovals) Subscribe(ctx context.Context, p client.Persona) (approvalStream, error) {
	sub, err := ca.c.SubscribeApprovals(ctx, p)
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// Attended wraps the MCP or A2A driver. Cases with Attended=false pass through
// unchanged. T23 wraps the drivers only for an attended run.
//
// The approve or deny decision is the human's. The driver never approves anything
// itself: it waits for the decision over SSE and reports what the Gateway resumed.
type Attended struct {
	inner          cases.Driver
	persona        client.Persona
	approverUserID string
	approvals      approvalSubscriber
}

// NewAttended wraps inner. The approval stream is the client's SubscribeApprovals.
func NewAttended(inner cases.Driver, c *client.Client, p client.Persona, approverUserID string) *Attended {
	return &Attended{
		inner:          inner,
		persona:        p,
		approverUserID: approverUserID,
		approvals:      clientApprovals{c: c},
	}
}

// Drive subscribes to approvals, then submits the attempt through the inner driver.
// For an attended case whose submission is suspended, it waits for the human decision
// and returns the resumed receipt in place of the suspension (row 24, approve). Every
// other answer is returned as the inner driver gave it. Every attended case in the
// catalogue is row 24: row 25 (deny) and row 26 (L3 via arguments) are gaps, so there
// is no deny path here.
func (d *Attended) Drive(ctx context.Context, a cases.Attempt) (cases.Driven, error) {
	if !a.Case.Attended {
		return d.inner.Drive(ctx, a)
	}
	// The stream opens before the call is submitted. The Gateway publishes
	// approval.completed as soon as the human decides, so a stream opened after the
	// suspension can miss that decision.
	stream, err := d.approvals.Subscribe(ctx, d.persona)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("attended %s: subscribe for approval: %w", a.Case.Fault, err)
	}
	defer stream.Close()

	driven, err := d.inner.Drive(ctx, a)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("attended %s: drive: %w", a.Case.Fault, err)
	}
	if !isSuspendedOnly(driven) {
		return driven, nil
	}
	txHash := driven.Observations[0].GetSuspendedTxHash()

	// The inner driver closed its A2A attempt when it returned the suspension. The
	// Gateway calls the stub only after the approval, so the attempt is reopened for
	// the wait and its calls are collected after it.
	a2a, onA2A := d.inner.(*A2A)
	if onA2A {
		a2a.stub.Begin(a.AttemptID)
	}
	receipt, waitErr := stream.Wait(ctx, txHash, d.approverUserID)
	var waitCalls []*evalv1.ConformanceA2ACall
	if onA2A {
		waitCalls = a2a.stub.End(a.AttemptID)
	}
	if waitErr != nil {
		return cases.Driven{}, fmt.Errorf("attended %s: wait for approval: %w", a.Case.Fault, waitErr)
	}

	return cases.Driven{
		Observations: []*evalv1.ConformanceObservation{{
			Kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
			Receipt: receipt,
		}},
		A2ACalls: append(append([]*evalv1.ConformanceA2ACall{}, driven.A2ACalls...), waitCalls...),
	}, nil
}

// isSuspendedOnly reports whether driven is exactly one SUSPENDED observation.
func isSuspendedOnly(driven cases.Driven) bool {
	return len(driven.Observations) == 1 &&
		driven.Observations[0].GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED
}
