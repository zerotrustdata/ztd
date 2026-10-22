// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type envelopeSuspender interface {
	SuspendGovernanceEnvelope(context.Context, *commonv1.GovernanceEnvelope) (models.PlatformEnrollmentL3SuspensionResponse, error)
}

// DispatchApprovalCoordinator joins a pending human decision to its original
// caller. It retains no execution results after that request finishes.
type DispatchApprovalCoordinator struct {
	suspender envelopeSuspender
	mu        sync.Mutex
	pending   map[string]*pendingDispatchApproval
}

type approvalOutcome struct {
	eventReceipt *operatorv1.EventReceipt
	err          error
}
type pendingDispatchApproval struct {
	ctx      context.Context
	envelope *commonv1.GovernanceEnvelope
	decision chan *commonv1.GovernanceEnvelope
	outcome  chan approvalOutcome
	done     chan struct{}
}

func NewDispatchApprovalCoordinator(suspender envelopeSuspender) (*DispatchApprovalCoordinator, error) {
	if suspender == nil {
		return nil, constants.ErrSuspendedStoreRequired
	}
	return &DispatchApprovalCoordinator{suspender: suspender, pending: make(map[string]*pendingDispatchApproval)}, nil
}

func (c *DispatchApprovalCoordinator) await(ctx context.Context, env *commonv1.GovernanceEnvelope) (*commonv1.GovernanceEnvelope, *pendingDispatchApproval, error) {
	env = proto.Clone(env).(*commonv1.GovernanceEnvelope)
	if env.Governance == nil {
		env.Governance = &commonv1.GovernanceMetadata{}
	}
	env.Governance.L3 = &commonv1.L3Metadata{PendingDispatch: true}
	p := &pendingDispatchApproval{ctx: ctx, envelope: env, decision: make(chan *commonv1.GovernanceEnvelope), outcome: make(chan approvalOutcome, 1), done: make(chan struct{})}
	c.mu.Lock()
	if _, exists := c.pending[env.Id]; exists {
		c.mu.Unlock()
		return nil, nil, constants.ErrSuspendedTransactionClaimed
	}
	c.pending[env.Id] = p
	c.mu.Unlock()
	if _, err := c.suspender.SuspendGovernanceEnvelope(ctx, env); err != nil {
		c.finish(env.Id, p, nil, err)
		return nil, nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, mcp.ApprovalRequestTTL)
	defer cancel()
	select {
	case resumed := <-p.decision:
		if err := waitCtx.Err(); err != nil {
			c.finish(env.Id, p, nil, err)
			return nil, nil, err
		}
		return resumed, p, nil
	case <-waitCtx.Done():
		err := ctx.Err()
		if err == nil {
			err = constants.ErrTransactionExpired
		}
		c.finish(env.Id, p, nil, err)
		return nil, nil, fmt.Errorf("dispatch: wait for human approval: %w", err)
	}
}

// ResumePendingDispatch never falls back to execution when the original
// request disappeared, including after cancellation or a Gateway restart.
// handedOff reports that the original request took the decision; from then on
// that request owns the outcome, so the decision can never be retried.
func (c *DispatchApprovalCoordinator) ResumePendingDispatch(ctx context.Context, env *commonv1.GovernanceEnvelope) (eventReceipt *operatorv1.EventReceipt, handedOff bool, err error) {
	c.mu.Lock()
	p := c.pending[env.GetId()]
	c.mu.Unlock()
	if p == nil {
		return nil, false, constants.ErrTransactionExpired
	}
	expected := proto.Clone(p.envelope).(*commonv1.GovernanceEnvelope)
	expected.Governance.L3 = env.GetGovernance().GetL3()
	if !proto.Equal(expected, env) {
		return nil, false, constants.ErrTxInvalidEnvelope
	}
	select {
	case p.decision <- env:
	case <-p.done:
		return nil, false, constants.ErrTransactionExpired
	case <-p.ctx.Done():
		return nil, false, p.ctx.Err()
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	select {
	case outcome := <-p.outcome:
		return outcome.eventReceipt, true, outcome.err
	case <-p.ctx.Done():
		return nil, true, p.ctx.Err()
	case <-ctx.Done():
		return nil, true, ctx.Err()
	}
}

func (c *DispatchApprovalCoordinator) finish(txHash string, p *pendingDispatchApproval, eventReceipt *operatorv1.EventReceipt, err error) {
	c.mu.Lock()
	if c.pending[txHash] != p {
		c.mu.Unlock()
		return
	}
	delete(c.pending, txHash)
	close(p.done)
	c.mu.Unlock()
	select {
	case p.outcome <- approvalOutcome{eventReceipt: eventReceipt, err: err}:
	default:
	}
}
