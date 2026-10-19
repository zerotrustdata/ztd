// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cases

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/governance/doctrinecorpus"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// catalogueRows is the number of plan catalogue rows every catalogue must cover.
const catalogueRows = 25

// doctrineIngresses are the ingresses that must carry every attack category.
var doctrineIngresses = []Ingress{IngressDispatchEmbedded, IngressDispatchOutbound, IngressMCP, IngressA2A, IngressCLI}

// cellKey is one (ingress, fault) cell of the catalogue.
type cellKey struct {
	ingress Ingress
	fault   Fault
}

// doctrineKey identifies one doctrine case by where it enters and which corpus entry it sends.
type doctrineKey struct {
	ingress Ingress
	posture evalv1.EvaluationGovernancePosture
	entryID string
}

// categoryKey identifies one doctrine category on one ingress in one posture.
type categoryKey struct {
	ingress  Ingress
	posture  evalv1.EvaluationGovernancePosture
	category doctrinecorpus.Category
}

// Validate checks a catalogue. Every failure wraps ErrConformanceCatalogueInvalid.
func Validate(cases []Case, gaps []Gap, corpus *doctrinecorpus.Corpus) error {
	if corpus == nil {
		return invalid("corpus is nil")
	}
	attacks := make(map[string]doctrinecorpus.Entry)
	for _, e := range corpus.Attacks() {
		attacks[e.ID] = e
	}
	offline := governance.NewL1Doctrine()

	ids := make(map[string]struct{}, len(cases))
	cells := make(map[cellKey]struct{}, len(cases))
	rows := make(map[int]bool, catalogueRows)
	for _, c := range cases {
		if _, dup := ids[c.ID]; dup {
			return invalid("duplicate case id %q", c.ID)
		}
		ids[c.ID] = struct{}{}
		if err := validateCase(c); err != nil {
			return err
		}
		if c.Fault == FaultDoctrine {
			if err := validateDoctrineCase(c, attacks, offline); err != nil {
				return err
			}
		}
		cells[cellKey{c.Ingress, c.Fault}] = struct{}{}
		rows[c.Row] = true
	}

	for _, g := range gaps {
		if g.Row < 1 || g.Row > catalogueRows {
			return invalid("gap row %d is outside 1 to %d", g.Row, catalogueRows)
		}
		if g.Reason == "" {
			return invalid("gap row %d %s %s has no reason", g.Row, g.Ingress, g.Fault)
		}
		if _, shared := cells[cellKey{g.Ingress, g.Fault}]; shared {
			return invalid("gap row %d %s %s shares a cell with a case", g.Row, g.Ingress, g.Fault)
		}
		rows[g.Row] = true
	}

	for row := 1; row <= catalogueRows; row++ {
		if !rows[row] {
			return invalid("row %d has no case and no gap", row)
		}
	}
	return validateDoctrineCoverage(cases, attacks)
}

// validateCase checks one case's own fields and each of its expectations.
func validateCase(c Case) error {
	posture, err := GovernancePosture(c.Posture)
	if err != nil {
		return invalid("case %s: posture %s is not one of the four postures", c.ID, c.Posture)
	}
	if !knownIngress(c.Ingress) {
		return invalid("case %s: ingress %q is unknown", c.ID, c.Ingress)
	}
	if c.Action != ActionMutation && c.Action != ActionRead {
		return invalid("case %s: action %q is unknown", c.ID, c.Action)
	}
	want := 1
	if c.Fault == FaultReplay {
		want = 2
	}
	if len(c.Expect) != want {
		return invalid("case %s: has %d expectations, want %d", c.ID, len(c.Expect), want)
	}
	for _, e := range c.Expect {
		if err := validateExpect(c, posture, e); err != nil {
			return err
		}
	}
	return nil
}

// validateExpect checks one expectation against the case it belongs to, which
// runs under posture.
func validateExpect(c Case, posture constants.Posture, e Expect) error {
	switch e.Kind {
	case ExpectExecuted:
		if e.RejectionCode != operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED ||
			e.RefusalCode != operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNSPECIFIED {
			return invalid("case %s: executed expectation carries a failure code", c.ID)
		}
		if e.L2Status == operatorv1.L2Status_L2_STATUS_UNSPECIFIED || e.L3Status == operatorv1.L3Status_L3_STATUS_UNSPECIFIED {
			return invalid("case %s: executed expectation has no L2 or L3 status", c.ID)
		}
		want := executedExpect(posture, c.Action, effectFor(c.Ingress, c.Action))
		if e != want {
			return invalid("case %s: executed expectation %+v does not match %+v", c.ID, e, want)
		}
		return nil
	case ExpectRejected:
		if !definedRejection(e.RejectionCode) {
			return invalid("case %s: rejection code %d is not a defined rejection", c.ID, e.RejectionCode)
		}
		if e.RefusalCode != operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNSPECIFIED {
			return invalid("case %s: rejection carries a refusal code", c.ID)
		}
	case ExpectRefused:
		if !definedRefusal(e.RefusalCode) {
			return invalid("case %s: refusal code %d is not a defined refusal", c.ID, e.RefusalCode)
		}
		if e.RejectionCode != operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED {
			return invalid("case %s: refusal carries a rejection code", c.ID)
		}
	case ExpectSuspended:
		if e.RejectionCode != operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED ||
			e.RefusalCode != operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNSPECIFIED {
			return invalid("case %s: suspension carries a failure code", c.ID)
		}
	default:
		return invalid("case %s: expectation kind %q is unknown", c.ID, e.Kind)
	}
	if e.L2Status != operatorv1.L2Status_L2_STATUS_UNSPECIFIED || e.L3Status != operatorv1.L3Status_L3_STATUS_UNSPECIFIED {
		return invalid("case %s: %s expectation carries an L2 or L3 status", c.ID, e.Kind)
	}
	if e.Effect != EffectNone {
		return invalid("case %s: %s expectation has effect %q, want %q", c.ID, e.Kind, e.Effect, EffectNone)
	}
	return nil
}

// validateDoctrineCase checks that a doctrine case sends its corpus attack command and that L1 blocks it offline.
func validateDoctrineCase(c Case, attacks map[string]doctrinecorpus.Entry, offline *governance.L1Doctrine) error {
	entry, ok := attacks[c.CorpusEntryID]
	if !ok {
		return invalid("case %s: doctrine case names no attack entry %q", c.ID, c.CorpusEntryID)
	}
	if c.Command != entry.Command {
		return invalid("case %s: command does not match corpus entry %q", c.ID, entry.ID)
	}
	if violations := offline.ValidatePayload(&operatorv1.CommandRequested{Command: c.Command}); len(violations) == 0 {
		return invalid("case %s: command is not blocked offline and would execute live", c.ID)
	}
	return nil
}

// validateDoctrineCoverage checks that every attack entry runs on DIRECT in every
// posture, and that every attack category runs on every doctrine ingress in every posture.
func validateDoctrineCoverage(cases []Case, attacks map[string]doctrinecorpus.Entry) error {
	have := make(map[doctrineKey]struct{})
	categories := make(map[categoryKey]struct{})
	for _, c := range cases {
		if c.Fault != FaultDoctrine {
			continue
		}
		entry := attacks[c.CorpusEntryID]
		have[doctrineKey{c.Ingress, c.Posture, c.CorpusEntryID}] = struct{}{}
		categories[categoryKey{c.Ingress, c.Posture, entry.Category}] = struct{}{}
	}

	for _, entry := range attacks {
		for _, posture := range Postures {
			if _, ok := have[doctrineKey{IngressDirect, posture.Wire, entry.ID}]; !ok {
				return invalid("attack entry %s has no DIRECT doctrine case in posture %s", entry.ID, posture.Posture)
			}
		}
	}
	for _, category := range doctrinecorpus.Categories() {
		for _, ingress := range doctrineIngresses {
			for _, posture := range Postures {
				if _, ok := categories[categoryKey{ingress, posture.Wire, category}]; !ok {
					return invalid("category %s has no %s doctrine case in posture %s", category, ingress, posture.Posture)
				}
			}
		}
	}
	return nil
}

// definedRejection reports whether code is a rejection a case may expect.
func definedRejection(code operatorv1.GovernanceRejectionCode) bool {
	if code == operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED ||
		code == operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED {
		return false
	}
	_, ok := operatorv1.GovernanceRejectionCode_name[int32(code)]
	return ok
}

// definedRefusal reports whether code is a refusal a case may expect.
func definedRefusal(code operatorv1.IngressRefusalCode) bool {
	if code == operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNSPECIFIED ||
		code == operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED {
		return false
	}
	_, ok := operatorv1.IngressRefusalCode_name[int32(code)]
	return ok
}

// knownIngress reports whether i is one of the six ingresses.
func knownIngress(i Ingress) bool {
	for _, known := range ingressAll {
		if i == known {
			return true
		}
	}
	return false
}

// invalid wraps ErrConformanceCatalogueInvalid with the failing check's detail.
func invalid(format string, args ...any) error {
	return fmt.Errorf("catalogue: validate: %w: %s", constants.ErrConformanceCatalogueInvalid, fmt.Sprintf(format, args...))
}
