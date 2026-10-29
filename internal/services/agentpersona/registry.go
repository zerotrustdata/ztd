// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package agentpersona

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Persona IDs. Tiers (owner direction): Lite runs Triage, the Interrogator,
// and the Tribunal members; Assistant runs Dash and Marshal; Primary runs Sage
// and the Auditor. Triage runs once at initial ingest; the Interrogator asks
// clarifying questions inside the agent loop.
const (
	IDSage         = "sage"
	IDDash         = "dash"
	IDTriage       = "triage"
	IDInterrogator = "interrogator"
	IDAxiom        = "axiom"
	IDConcord      = "concord"
	IDVariance     = "variance"
	IDPragma       = "pragma"
	IDNemesis      = "nemesis"
	IDMarshal      = "marshal"
	IDAuditor      = "auditor"
)

// Registry holds every persona by ID in registration order.
type Registry struct {
	byID  map[string]Persona
	order []string
}

// NewRegistry registers every persona explicitly and fails closed on a
// duplicate ID or a persona missing a required section.
func NewRegistry() (*Registry, error) {
	r := &Registry{byID: make(map[string]Persona)}
	for _, p := range []Persona{sage(), dash(), triage(), interrogator(), axiom(), concord(), variance(), pragma(), nemesis(), marshal(), auditor()} {
		if err := p.validate(); err != nil {
			return nil, err
		}
		if _, duplicate := r.byID[p.ID]; duplicate {
			return nil, fmt.Errorf("agentpersona: duplicate persona %q: %w", p.ID, constants.ErrPersonaDuplicate)
		}
		r.byID[p.ID] = p
		r.order = append(r.order, p.ID)
	}
	return r, nil
}

// Get returns the persona registered under id.
func (r *Registry) Get(id string) (Persona, error) {
	p, ok := r.byID[id]
	if !ok {
		return Persona{}, fmt.Errorf("agentpersona: persona %q: %w", id, constants.ErrNotFound)
	}
	return p, nil
}

// VoteSystemPrompt returns the registered persona's L2 prompt and fails when
// the ID is unknown or does not identify a voting persona.
func (r *Registry) VoteSystemPrompt(id string) (string, error) {
	p, err := r.Get(id)
	if err != nil {
		return "", err
	}
	if prompt := p.VoteSystemPrompt(); prompt != "" {
		return prompt, nil
	}
	return "", fmt.Errorf("agentpersona: persona %q has no vote rendering: %w", id, constants.ErrPersonaInvalid)
}

// All returns every persona in registration order.
func (r *Registry) All() []Persona {
	out := make([]Persona, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id])
	}
	return out
}
