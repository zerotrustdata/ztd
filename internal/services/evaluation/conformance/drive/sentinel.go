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

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	"github.com/zerotrustdata/ztd/internal/uuid"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Sentinel is the harness-owned FS_READ that the runner uses to read the canary
// file back through the executor's own session.
type Sentinel struct {
	client  *client.Client
	persona client.Persona
	targets map[cases.Executor]operator.Target
}

// NewSentinel requires at least one executor target. The conformance runner
// passes both; a campaign reads only its Data Operator. Read refuses an executor
// with no target.
func NewSentinel(c *client.Client, p client.Persona, targets map[cases.Executor]operator.Target) (*Sentinel, error) {
	if c == nil {
		return nil, fmt.Errorf("sentinel: client: %w", constants.ErrMissingRequiredField)
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("sentinel: targets: %w", constants.ErrMissingRequiredField)
	}
	return &Sentinel{client: c, persona: p, targets: targets}, nil
}

// Read dispatches FS_READ of readAbs to the executor's session and returns the
// signed receipt. Any outcome other than RECEIPT is an error.
func (s *Sentinel) Read(ctx context.Context, ex cases.Executor, readAbs string) (*operatorv1.ActionReceipt, error) {
	target, ok := s.targets[ex]
	if !ok {
		return nil, fmt.Errorf("sentinel %s: %w", ex, constants.ErrConformanceIngressUnsupported)
	}
	executionID, err := uuid.NewString()
	if err != nil {
		return nil, fmt.Errorf("sentinel %s: execution id: %w", ex, err)
	}
	a := cases.Attempt{
		AttemptID: executionID,
		ReadAbs:   readAbs,
		Case:      cases.Case{ID: "sentinel-" + string(ex), Action: cases.ActionRead},
	}
	req, err := dispatchRequest(a, target, s.persona)
	if err != nil {
		return nil, fmt.Errorf("sentinel %s: %w", ex, err)
	}
	outcome, err := s.client.DispatchCommand(ctx, s.persona, req)
	if err != nil {
		return nil, fmt.Errorf("sentinel %s: submit: %w", ex, err)
	}
	if err := outcome.Typed(); err != nil {
		return nil, fmt.Errorf("sentinel %s: %w", ex, err)
	}
	obs := Observe(outcome)
	if obs.GetKind() != evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT || obs.GetReceipt() == nil {
		return nil, fmt.Errorf("sentinel %s: outcome %s: %w", ex, obs.GetKind(), constants.ErrActionReceiptMissing)
	}
	return obs.GetReceipt(), nil
}
