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
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// malformedBody is the truncated JSON that row 17 submits as a malformed request.
const malformedBody = `{"id":`

// unknownEventType is the event_type that row 17 submits as unknown.
const unknownEventType = "ztd.v1.conformance.unknown"

// L2Source supplies a valid L2 proof for a final envelope (T18). Nil under
// postures that do not require L2.
type L2Source interface {
	Proof(ctx context.Context, env *commonv1.GovernanceEnvelope) (*commonv1.L2Metadata, error)
}

// Direct submits attempts to the direct envelope ingress.
type Direct struct {
	client   *client.Client
	id       Identity
	l2       L2Source
	mutators MutatorSet
}

// NewDirect merges the mutator sets. A fault present in two sets is an error.
func NewDirect(c *client.Client, p client.Persona, embedded operator.Target, l2 L2Source, sets ...MutatorSet) (*Direct, error) {
	if c == nil {
		return nil, fmt.Errorf("direct: client: %w", constants.ErrMissingRequiredField)
	}
	merged := MutatorSet{}
	for _, set := range sets {
		for fault, m := range set {
			if _, dup := merged[fault]; dup {
				return nil, fmt.Errorf("direct: fault %q has a mutator in two sets", fault)
			}
			merged[fault] = m
		}
	}
	return &Direct{client: c, id: Identity{Persona: p, Target: embedded}, l2: l2, mutators: merged}, nil
}

// Drive submits one attempt and returns one observation per HTTP answer. A
// transport error (no HTTP answer) is returned as an error.
func (d *Direct) Drive(ctx context.Context, a cases.Attempt) (cases.Driven, error) {
	if a.Case.Fault == cases.FaultMalformedJSON {
		return d.submitBodies(ctx, a, []byte(malformedBody))
	}

	stateRoot, err := d.client.StateRoot(ctx)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("direct %s: state root: %w", a.Case.Fault, err)
	}
	env, err := Baseline(a, d.id, stateRoot, time.Now())
	if err != nil {
		return cases.Driven{}, fmt.Errorf("direct %s: %w", a.Case.Fault, err)
	}

	if applied, err := applyBodyFault(env, a.Case.Fault); err != nil {
		return cases.Driven{}, fmt.Errorf("direct %s: %w", a.Case.Fault, err)
	} else if applied {
		if err := d.attachL2(ctx, env, false); err != nil {
			return cases.Driven{}, fmt.Errorf("direct %s: %w", a.Case.Fault, err)
		}
		wire, err := protojson.Marshal(env)
		if err != nil {
			return cases.Driven{}, fmt.Errorf("direct %s: marshal envelope: %w", a.Case.Fault, err)
		}
		return d.submitBodies(ctx, a, wire)
	}

	m, ok := d.mutators[a.Case.Fault]
	if !ok {
		return cases.Driven{}, fmt.Errorf("direct %s: %w", a.Case.Fault, constants.ErrConformanceIngressUnsupported)
	}
	if err := m.Apply(ctx, env, a); err != nil {
		return cases.Driven{}, fmt.Errorf("direct %s: mutate: %w", a.Case.Fault, err)
	}
	if err := d.attachL2(ctx, env, m.SetsL2); err != nil {
		return cases.Driven{}, fmt.Errorf("direct %s: %w", a.Case.Fault, err)
	}

	count := 1
	if a.Case.Fault == cases.FaultReplay {
		count = 2
	}
	obs := make([]*evalv1.ConformanceObservation, 0, count)
	for range count {
		outcome, err := d.client.SubmitEnvelope(ctx, d.id.Persona, env)
		if err != nil {
			return cases.Driven{}, fmt.Errorf("direct %s: submit: %w", a.Case.Fault, err)
		}
		obs = append(obs, Observe(outcome))
	}
	return cases.Driven{Observations: obs}, nil
}

// submitBodies posts one raw body and returns its observation.
func (d *Direct) submitBodies(ctx context.Context, a cases.Attempt, body []byte) (cases.Driven, error) {
	outcome, err := d.client.SubmitEnvelopeBody(ctx, d.id.Persona, body)
	if err != nil {
		return cases.Driven{}, fmt.Errorf("direct %s: submit: %w", a.Case.Fault, err)
	}
	return cases.Driven{Observations: []*evalv1.ConformanceObservation{Observe(outcome)}}, nil
}

// attachL2 writes governance.l2 from the L2 source over the final hash. It is
// skipped when the mutator wrote L2 itself or when no source is configured.
func (d *Direct) attachL2(ctx context.Context, env *commonv1.GovernanceEnvelope, setsL2 bool) error {
	if d.l2 == nil || setsL2 {
		return nil
	}
	proof, err := d.l2.Proof(ctx, env)
	if err != nil {
		return fmt.Errorf("l2 proof: %w", err)
	}
	if env.Governance == nil {
		env.Governance = &commonv1.GovernanceMetadata{}
	}
	env.Governance.L2 = proof
	return nil
}

// applyBodyFault applies a row 17 body fault that changes the envelope rather
// than one property of it. It reports whether the fault is a body fault.
func applyBodyFault(env *commonv1.GovernanceEnvelope, fault cases.Fault) (bool, error) {
	switch fault {
	case cases.FaultUnknownEventType:
		env.EventType = unknownEventType
		return true, Rehash(env)
	case cases.FaultMissingField:
		env.Payload = nil
		return true, Rehash(env)
	default:
		return false, nil
	}
}
