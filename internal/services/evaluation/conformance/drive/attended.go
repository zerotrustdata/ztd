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

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// approvalStream is one open approval subscription. The driver opens it before the
// attended call is submitted, waits on it once the call suspends, and closes it.
type approvalStream interface {
	// Wait asks the human for decision on txHash, blocks until the human decides, and
	// returns the signed receipt: resumed on approve, L3_DENIED rejection on deny.
	Wait(ctx context.Context, txHash, userID string, decision client.ApprovalDecision) (*operatorv1.ActionReceipt, error)
	// Close ends the subscription.
	Close()
}

// approvalSubscriber opens an approval stream for a persona. The client is the
// production subscriber; tests fake it.
type approvalSubscriber interface {
	Subscribe(ctx context.Context, p client.Persona) (approvalStream, error)
	// Announce asks the human for decision on a transaction that does not exist yet.
	Announce(decision client.ApprovalDecision)
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
	return bannerStream{c: ca.c, sub: sub}, nil
}

// Announce prints the approval banner without a transaction.
func (ca clientApprovals) Announce(decision client.ApprovalDecision) {
	ca.c.AnnounceApproval("", decision)
}

// bannerStream is the harness approval stream. Its Wait prints the approval banner
// before it waits on the shared subscription.
type bannerStream struct {
	c   *client.Client
	sub *auth.ApprovalSubscription
}

// Wait prints the banner for txHash and waits for the human decision.
func (s bannerStream) Wait(ctx context.Context, txHash, userID string, decision client.ApprovalDecision) (*operatorv1.ActionReceipt, error) {
	return s.c.WaitForApproval(ctx, s.sub, txHash, userID, decision)
}

// Close ends the subscription.
func (s bannerStream) Close() {
	s.sub.Close()
}

// Attended wraps the MCP, A2A, governed dispatch or CLI driver. Cases with
// Attended=false pass through unchanged. The run wraps the drivers only for an
// attended run.
//
// The approve or deny decision is the human's. The driver never approves or denies
// anything itself: the banner asks for the decision the case names. MCP and A2A
// answer with a suspension, so the driver waits for the decision over SSE and
// reports the signed receipt it produced. Governed dispatch and the CLI hold the
// request open until the decision, so the inner driver's answer already is the
// signed receipt or L3_DENIED rejection and no second wait is needed.
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
// For an attended MCP or A2A case whose submission is suspended, it waits for the human decision
// and returns it in place of the suspension: the resumed receipt on approve (row 23),
// or the Operator-signed L3_DENIED rejection on deny (row 24). Every other answer is
// returned as the inner driver gave it.
//
// A blocking ingress (governed dispatch, CLI) is announced before the call and
// returns the inner answer: the Gateway answers it only after the human decides.
func (d *Attended) Drive(ctx context.Context, a cases.Attempt) (cases.Driven, error) {
	if !a.Case.Attended {
		return d.inner.Drive(ctx, a)
	}
	decision := client.ApprovalDecisionApprove
	if a.Case.Fault == cases.FaultAttendedDeny {
		decision = client.ApprovalDecisionDeny
	}
	if blocksUntilDecision(a.Case.Ingress) {
		d.approvals.Announce(decision)
		driven, err := d.inner.Drive(ctx, a)
		if err != nil {
			return cases.Driven{}, fmt.Errorf("attended %s: drive: %w", a.Case.Fault, err)
		}
		return driven, nil
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
	receipt, waitErr := stream.Wait(ctx, txHash, d.approverUserID, decision)
	var waitCalls []*evalv1.ConformanceA2ACall
	if onA2A {
		waitCalls = a2a.stub.End(a.AttemptID)
	}
	if waitErr != nil {
		return cases.Driven{}, fmt.Errorf("attended %s: wait for approval: %w", a.Case.Fault, waitErr)
	}

	return cases.Driven{
		Observations: []*evalv1.ConformanceObservation{decisionObservation(receipt)},
		A2ACalls:     append(append([]*evalv1.ConformanceA2ACall{}, driven.A2ACalls...), waitCalls...),
	}, nil
}

// blocksUntilDecision reports whether the ingress holds a suspended mutation open
// until a human decides, instead of answering with a suspension: governed HTTP
// dispatch (both executors) and the CLI, which posts to the same route.
func blocksUntilDecision(ingress cases.Ingress) bool {
	switch ingress {
	case cases.IngressDispatchEmbedded, cases.IngressDispatchOutbound, cases.IngressCLI:
		return true
	default:
		return false
	}
}

// decisionObservation records the signed receipt of a human decision. A governance
// rejection (the L3_DENIED deny) is a REJECTION, as every ingress reports one; any
// other receipt is a RECEIPT.
func decisionObservation(receipt *operatorv1.ActionReceipt) *evalv1.ConformanceObservation {
	if governance.IsGovernanceRejectionReceipt(receipt) {
		return &evalv1.ConformanceObservation{
			Kind:      evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
			Rejection: &operatorv1.GovernanceRejection{Error: receipt.GetRejectionCode().String(), Receipt: receipt},
		}
	}
	return &evalv1.ConformanceObservation{
		Kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		Receipt: receipt,
	}
}

// isSuspendedOnly reports whether driven is exactly one SUSPENDED observation.
func isSuspendedOnly(driven cases.Driven) bool {
	return len(driven.Observations) == 1 &&
		driven.Observations[0].GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED
}
