// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import "fmt"

// Posture is the Gateway's governance posture: which of L2 consensus and L3
// notary gate execution as fail-closed layers rather than being audited. L1
// doctrine, L4 warden, and L5 actuator run in every posture.
//
//	doctrine  — L1 enforced, L2/L3 audited (minimum, default)
//	consensus — L1+L2 enforced, L3 audited
//	ratify    — L1+L3 enforced, L2 audited
//	notary    — L1+L2+L3 strictly enforced (maximum)
//
// The Gateway fixes its posture at startup and stamps it onto every
// GovernanceEnvelope it builds; it is immutable at runtime. This type is the
// single owner of posture names, requirements, and wording. Every caller
// derives L2/L3 requirements from it, never from a second table.
type Posture string

// Governance postures.
const (
	PostureDoctrine  Posture = "doctrine"
	PostureConsensus Posture = "consensus"
	PostureRatify    Posture = "ratify"
	PostureNotary    Posture = "notary"
)

// DefaultPosture is the posture a Gateway starts under when none is given. It
// is a var, not a const, so the Posture enum lists only the four postures.
var DefaultPosture = PostureDoctrine

// AllPostures lists every posture from weakest to strictest.
var AllPostures = []Posture{PostureDoctrine, PostureConsensus, PostureRatify, PostureNotary}

// ParsePosture returns the posture named name. An unrecognized name wraps
// ErrInvalidPosture; callers propagate it so a misconfigured deployment fails
// fast instead of running under a weaker posture.
func ParsePosture(name string) (Posture, error) {
	p := Posture(name)
	if !p.Valid() {
		return "", fmt.Errorf("%w: %q (must be one of: doctrine, consensus, ratify, notary)", ErrInvalidPosture, name)
	}
	return p, nil
}

// Valid reports whether p is one of the defined postures.
func (p Posture) Valid() bool {
	switch p {
	case PostureDoctrine, PostureConsensus, PostureRatify, PostureNotary:
		return true
	default:
		return false
	}
}

// RequiresL2 reports whether L2 consensus votes gate execution. An
// unrecognized posture fails closed and requires it.
func (p Posture) RequiresL2() bool {
	switch p {
	case PostureDoctrine, PostureRatify:
		return false
	default:
		return true
	}
}

// RequiresL3 reports whether an L3 human (passkey) proof gates mutations. An
// unrecognized posture fails closed and requires it.
func (p Posture) RequiresL3() bool {
	switch p {
	case PostureDoctrine, PostureConsensus:
		return false
	default:
		return true
	}
}

// Description summarizes what p enforces, for startup output and logs.
func (p Posture) Description() string {
	switch p {
	case PostureDoctrine:
		return "doctrine (L1 enforced, L2/L3 audited)"
	case PostureConsensus:
		return "consensus (L1/L2 enforced, L3 audited)"
	case PostureRatify:
		return "ratify (L1/L3 enforced, L2 audited)"
	case PostureNotary:
		return "notary (L1/L2/L3 strictly enforced)"
	default:
		return fmt.Sprintf("unrecognized posture %q", string(p))
	}
}

// HumanApprovalReason tells a person or agent why a governed mutation waits for
// the owner's passkey under p. It is empty when p requires no L3 proof. CLI,
// MCP, and console output all use this wording.
func (p Posture) HumanApprovalReason() string {
	if !p.RequiresL3() {
		return ""
	}
	return fmt.Sprintf("The Gateway runs under the %s posture, which requires the owner's passkey approval (L3) for every governed mutation.", string(p))
}
