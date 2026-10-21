// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cases

import (
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance/doctrinecorpus"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// PostureBinding pairs a governance posture with its evaluation wire enum.
type PostureBinding struct {
	Posture constants.Posture
	Wire    evalv1.EvaluationGovernancePosture
}

// Postures binds every governance posture to its wire enum, in
// constants.AllPostures order (weakest to strictest). It is the only mapping
// between the two: the name and the L2 and L3 requirements of a case's posture
// come from constants.Posture, never from a second copy of the posture table.
var Postures = []PostureBinding{
	{Posture: constants.PostureDoctrine, Wire: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE},
	{Posture: constants.PostureConsensus, Wire: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS},
	{Posture: constants.PostureRatify, Wire: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY},
	{Posture: constants.PostureNotary, Wire: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY},
}

// GovernancePosture returns the governance posture a wire posture runs under.
// UNSPECIFIED or an unknown value is ErrConformancePostureUnknown.
func GovernancePosture(wire evalv1.EvaluationGovernancePosture) (constants.Posture, error) {
	for _, binding := range Postures {
		if binding.Wire == wire {
			return binding.Posture, nil
		}
	}
	return "", fmt.Errorf("%w: %s", constants.ErrConformancePostureUnknown, wire)
}

// WirePosture returns the wire enum of a governance posture. An unknown
// governance posture is ErrConformancePostureUnknown with UNSPECIFIED.
func WirePosture(posture constants.Posture) (evalv1.EvaluationGovernancePosture, error) {
	for _, binding := range Postures {
		if binding.Posture == posture {
			return binding.Wire, nil
		}
	}
	return evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED, fmt.Errorf("%w: %q", constants.ErrConformancePostureUnknown, string(posture))
}

// Ingress and posture sets used by the row table.
var (
	ingressAll      = []Ingress{IngressDirect, IngressDispatchEmbedded, IngressDispatchOutbound, IngressMCP, IngressA2A, IngressCLI}
	ingressDispatch = []Ingress{IngressDispatchEmbedded, IngressDispatchOutbound}
	// ingressApproval are the ingresses a human approval can resume under ratify and notary.
	ingressApproval = []Ingress{IngressDispatchEmbedded, IngressDispatchOutbound, IngressMCP, IngressA2A, IngressCLI}

	postureAll = []evalv1.EvaluationGovernancePosture{
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY,
	}
	postureDoctrine = []evalv1.EvaluationGovernancePosture{
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
	}
	postureDoctrineConsensus = []evalv1.EvaluationGovernancePosture{
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
	}
	postureRatify = []evalv1.EvaluationGovernancePosture{
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY,
	}
	postureRatifyNotary = []evalv1.EvaluationGovernancePosture{
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY,
	}
	postureConsensusNotary = []evalv1.EvaluationGovernancePosture{
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY,
	}
)

// corpusSet selects which doctrine corpus entries a row expands over.
type corpusSet int

const (
	corpusNone             corpusSet = iota
	corpusEveryAttack                // one case per attack entry
	corpusFirstPerCategory           // the first attack entry of each category
)

// outcome is one submission's expected result. An EXECUTED outcome is completed
// from the posture and action by executedExpect when the row is expanded.
type outcome struct {
	kind      ExpectKind
	rejection operatorv1.GovernanceRejectionCode
	refusal   operatorv1.IngressRefusalCode
}

func executed() outcome  { return outcome{kind: ExpectExecuted} }
func suspended() outcome { return outcome{kind: ExpectSuspended} }

func rejected(code operatorv1.GovernanceRejectionCode) outcome {
	return outcome{kind: ExpectRejected, rejection: code}
}

func refused(code operatorv1.IngressRefusalCode) outcome {
	return outcome{kind: ExpectRefused, refusal: code}
}

// rowSpec is one block of the catalogue table. It expands to one case for every
// fault, ingress, posture and selected corpus entry it names, in that order.
// Rows whose action or outcome differs by posture are split into several specs.
type rowSpec struct {
	row       int
	faults    []Fault
	ingresses []Ingress
	postures  []evalv1.EvaluationGovernancePosture
	action    Action
	corpus    corpusSet
	attended  bool
	outcomes  []outcome // one per submission
}

// gapSpec is one block of cells the ingress cannot express.
type gapSpec struct {
	row       int
	fault     Fault
	ingresses []Ingress
	reason    string
}

// rowSpecs is the catalogue table, rows 1 to 24. Row 25 is a gap only (gapSpecs).
var rowSpecs = []rowSpec{
	// Row 1: the clean mutation executes under the postures that do not suspend it.
	{row: 1, faults: []Fault{FaultNone}, ingresses: ingressAll, postures: postureDoctrineConsensus,
		action: ActionMutation, outcomes: []outcome{executed()}},

	// Row 2: the read baseline. The MCP read tool suspends under ratify and notary.
	{row: 2, faults: []Fault{FaultReadBaseline}, ingresses: []Ingress{IngressDirect, IngressDispatchEmbedded, IngressDispatchOutbound, IngressMCP},
		postures: postureDoctrineConsensus, action: ActionRead, outcomes: []outcome{executed()}},
	{row: 2, faults: []Fault{FaultReadBaseline}, ingresses: []Ingress{IngressDirect, IngressDispatchEmbedded, IngressDispatchOutbound},
		postures: postureRatifyNotary, action: ActionRead, outcomes: []outcome{executed()}},
	{row: 2, faults: []Fault{FaultReadBaseline}, ingresses: []Ingress{IngressMCP},
		postures: postureRatifyNotary, action: ActionRead, outcomes: []outcome{suspended()}},

	// Rows 3 to 12: DIRECT faults. Under ratify and notary an unattended DIRECT
	// mutation cannot carry L3, so these rows use a read to keep the fault the
	// first failing check.
	{row: 3, faults: []Fault{FaultPayloadTampered}, ingresses: []Ingress{IngressDirect},
		postures: postureDoctrineConsensus, action: ActionMutation,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TRANSACTION_HASH_MISMATCH)}},
	{row: 3, faults: []Fault{FaultPayloadTampered}, ingresses: []Ingress{IngressDirect},
		postures: postureRatifyNotary, action: ActionRead,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TRANSACTION_HASH_MISMATCH)}},

	{row: 4, faults: []Fault{FaultIDMismatch}, ingresses: []Ingress{IngressDirect},
		postures: postureDoctrineConsensus, action: ActionMutation,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TRANSACTION_ID_MISMATCH)}},
	{row: 4, faults: []Fault{FaultIDMismatch}, ingresses: []Ingress{IngressDirect},
		postures: postureRatifyNotary, action: ActionRead,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TRANSACTION_ID_MISMATCH)}},

	// Row 5: the first submission executes, the replay is rejected.
	{row: 5, faults: []Fault{FaultReplay}, ingresses: []Ingress{IngressDirect},
		postures: postureDoctrineConsensus, action: ActionMutation,
		outcomes: []outcome{executed(), rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY)}},
	{row: 5, faults: []Fault{FaultReplay}, ingresses: []Ingress{IngressDirect},
		postures: postureRatifyNotary, action: ActionRead,
		outcomes: []outcome{executed(), rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY)}},

	{row: 6, faults: []Fault{FaultExpired}, ingresses: []Ingress{IngressDirect},
		postures: postureDoctrineConsensus, action: ActionMutation,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EXPIRED)}},
	{row: 6, faults: []Fault{FaultExpired}, ingresses: []Ingress{IngressDirect},
		postures: postureRatifyNotary, action: ActionRead,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EXPIRED)}},

	{row: 7, faults: []Fault{FaultProtocolVersion}, ingresses: []Ingress{IngressDirect},
		postures: postureDoctrineConsensus, action: ActionMutation,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_PROTOCOL_VERSION_UNSUPPORTED)}},
	{row: 7, faults: []Fault{FaultProtocolVersion}, ingresses: []Ingress{IngressDirect},
		postures: postureRatifyNotary, action: ActionRead,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_PROTOCOL_VERSION_UNSUPPORTED)}},

	{row: 8, faults: []Fault{FaultEventActionMismatch}, ingresses: []Ingress{IngressDirect},
		postures: postureDoctrineConsensus, action: ActionMutation,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EVENT_ACTION_MISMATCH)}},
	{row: 8, faults: []Fault{FaultEventActionMismatch}, ingresses: []Ingress{IngressDirect},
		postures: postureRatifyNotary, action: ActionRead,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EVENT_ACTION_MISMATCH)}},

	// Row 9 is pinned from the T15 probe: the CLI certificate binds the
	// cli_session_id, so the wrong operator_session_id reaches L4, which signs
	// TARGET_SESSION_MISMATCH under every posture.
	{row: 9, faults: []Fault{FaultWrongTarget}, ingresses: []Ingress{IngressDirect},
		postures: postureDoctrineConsensus, action: ActionMutation,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TARGET_SESSION_MISMATCH)}},
	{row: 9, faults: []Fault{FaultWrongTarget}, ingresses: []Ingress{IngressDirect},
		postures: postureRatifyNotary, action: ActionRead,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TARGET_SESSION_MISMATCH)}},

	{row: 10, faults: []Fault{FaultStaleStateRoot}, ingresses: []Ingress{IngressDirect},
		postures: postureDoctrineConsensus, action: ActionMutation,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_STATE_ROOT_MISMATCH)}},
	{row: 10, faults: []Fault{FaultStaleStateRoot}, ingresses: []Ingress{IngressDirect},
		postures: postureRatifyNotary, action: ActionRead,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_STATE_ROOT_MISMATCH)}},

	{row: 11, faults: []Fault{FaultCallerPosture}, ingresses: []Ingress{IngressDirect},
		postures: postureDoctrineConsensus, action: ActionMutation,
		outcomes: []outcome{refused(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_CALLER_SUPPLIED_POSTURE)}},
	{row: 11, faults: []Fault{FaultCallerPosture}, ingresses: []Ingress{IngressDirect},
		postures: postureRatifyNotary, action: ActionRead,
		outcomes: []outcome{refused(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_CALLER_SUPPLIED_POSTURE)}},

	{row: 12, faults: []Fault{FaultRequestorMismatch}, ingresses: []Ingress{IngressDirect},
		postures: postureDoctrineConsensus, action: ActionMutation,
		outcomes: []outcome{refused(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_IDENTITY_BINDING_FAILED)}},
	{row: 12, faults: []Fault{FaultRequestorMismatch}, ingresses: []Ingress{IngressDirect},
		postures: postureRatifyNotary, action: ActionRead,
		outcomes: []outcome{refused(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_IDENTITY_BINDING_FAILED)}},

	// Row 13: every corpus attack entry, DIRECT. L1 rejects before any L3 check.
	{row: 13, faults: []Fault{FaultDoctrine}, ingresses: []Ingress{IngressDirect}, postures: postureAll,
		action: ActionMutation, corpus: corpusEveryAttack,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE)}},

	// Row 14: the first attack entry of each category. A forbidden command never reaches a human.
	{row: 14, faults: []Fault{FaultDoctrine}, ingresses: []Ingress{IngressDispatchEmbedded, IngressDispatchOutbound, IngressCLI},
		postures: postureAll, action: ActionMutation, corpus: corpusFirstPerCategory,
		outcomes: []outcome{refused(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE)}},
	{row: 14, faults: []Fault{FaultDoctrine}, ingresses: []Ingress{IngressMCP, IngressA2A},
		postures: postureAll, action: ActionMutation, corpus: corpusFirstPerCategory,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE)}},

	{row: 15, faults: []Fault{FaultUngovernedCollection}, ingresses: []Ingress{IngressDirect}, postures: postureAll,
		action:   ActionMutation,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_DOCUMENT_COLLECTION_NOT_GOVERNED)}},

	{row: 16, faults: []Fault{FaultUnknownSession}, ingresses: ingressDispatch, postures: postureAll,
		action:   ActionMutation,
		outcomes: []outcome{refused(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_OPERATOR_SESSION_NOT_FOUND)}},

	// Row 17: the driver sends a broken body, so no action runs. ActionMutation is a placeholder.
	{row: 17, faults: []Fault{FaultMalformedJSON}, ingresses: []Ingress{IngressDirect, IngressDispatchEmbedded, IngressDispatchOutbound},
		postures: postureDoctrine, action: ActionMutation,
		outcomes: []outcome{refused(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST)}},
	{row: 17, faults: []Fault{FaultUnknownEventType}, ingresses: []Ingress{IngressDirect, IngressDispatchEmbedded, IngressDispatchOutbound},
		postures: postureDoctrine, action: ActionMutation,
		outcomes: []outcome{refused(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNKNOWN_EVENT_TYPE)}},
	// Row 17 missing_field: DIRECT decodes the envelope, so the L4 payload-decode check
	// rejects it with a signed receipt. Governed dispatch builds the envelope itself and
	// refuses before one exists.
	{row: 17, faults: []Fault{FaultMissingField}, ingresses: []Ingress{IngressDirect},
		postures: postureDoctrine, action: ActionMutation,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED)}},
	{row: 17, faults: []Fault{FaultMissingField}, ingresses: []Ingress{IngressDispatchEmbedded, IngressDispatchOutbound},
		postures: postureDoctrine, action: ActionMutation,
		outcomes: []outcome{refused(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD)}},

	// Row 18: L2 faults under the postures that require L2.
	{row: 18, faults: []Fault{FaultL2Missing}, ingresses: []Ingress{IngressDirect}, postures: postureConsensusNotary,
		action: ActionMutation, outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_PROOF_MISSING)}},
	{row: 18, faults: []Fault{FaultL2BelowQuorum}, ingresses: []Ingress{IngressDirect}, postures: postureConsensusNotary,
		action: ActionMutation, outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET)}},
	{row: 18, faults: []Fault{FaultL2DuplicateSigner}, ingresses: []Ingress{IngressDirect}, postures: postureConsensusNotary,
		action: ActionMutation, outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_DUPLICATE_SIGNER)}},
	{row: 18, faults: []Fault{FaultL2SignatureFlipped}, ingresses: []Ingress{IngressDirect}, postures: postureConsensusNotary,
		action: ActionMutation, outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_SIGNATURE_INVALID)}},
	{row: 18, faults: []Fault{FaultL2NonMember}, ingresses: []Ingress{IngressDirect}, postures: postureConsensusNotary,
		action: ActionMutation, outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET)}},
	{row: 18, faults: []Fault{FaultL2UnknownSet}, ingresses: []Ingress{IngressDirect}, postures: postureConsensusNotary,
		action: ActionMutation, outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_CONSENSUS_SET_UNKNOWN)}},

	// Row 19: the same L2 faults where L2 does not gate. Doctrine runs a mutation, ratify a read.
	{row: 19, faults: l2Faults, ingresses: []Ingress{IngressDirect}, postures: postureDoctrine,
		action: ActionMutation, outcomes: []outcome{executed()}},
	{row: 19, faults: l2Faults, ingresses: []Ingress{IngressDirect}, postures: postureRatify,
		action: ActionRead, outcomes: []outcome{executed()}},

	// Row 20: L3 faults under the postures that require L3.
	{row: 20, faults: []Fault{FaultL3Missing}, ingresses: []Ingress{IngressDirect}, postures: postureRatifyNotary,
		action: ActionMutation, outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_PROOF_MISSING)}},
	{row: 20, faults: []Fault{FaultL3Invalid}, ingresses: []Ingress{IngressDirect}, postures: postureRatifyNotary,
		action: ActionMutation, outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_PROOF_INVALID)}},

	// Row 21: a garbage L3 proof is ignored where L3 does not gate.
	{row: 21, faults: []Fault{FaultL3Garbage}, ingresses: []Ingress{IngressDirect}, postures: postureDoctrineConsensus,
		action: ActionMutation, outcomes: []outcome{executed()}},

	// Rows 22 to 24: the human approval path under ratify and notary. MCP and A2A
	// answer a mutation with a suspension, so an unattended run observes it (row 22).
	// Governed dispatch and the CLI do not answer until a human decides: the request
	// blocks for the approval TTL, so a suspension is not observable there and those
	// ingresses carry only the attended rows. An approve executes (row 23). A deny is
	// the Operator-signed L3_DENIED rejection (row 24), a rejection receipt on MCP and
	// A2A and the same signed rejection in the dispatch answer.
	{row: 22, faults: []Fault{FaultUnattendedMutation}, ingresses: []Ingress{IngressMCP, IngressA2A}, postures: postureRatifyNotary,
		action: ActionMutation, outcomes: []outcome{suspended()}},
	{row: 23, faults: []Fault{FaultAttendedApprove}, ingresses: ingressApproval, postures: postureRatifyNotary,
		action: ActionMutation, attended: true, outcomes: []outcome{executed()}},
	{row: 24, faults: []Fault{FaultAttendedDeny}, ingresses: ingressApproval, postures: postureRatifyNotary,
		action: ActionMutation, attended: true,
		outcomes: []outcome{rejected(operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED)}},
}

// l2Faults is the six L2 faults of rows 18 and 19.
var l2Faults = []Fault{FaultL2Missing, FaultL2BelowQuorum, FaultL2DuplicateSigner, FaultL2SignatureFlipped, FaultL2NonMember, FaultL2UnknownSet}

// gapSpecs records the cells the ingresses cannot express. Row 25 follows the T06
// handoff answer: no tool or skill argument can carry an L3 proof.
var gapSpecs = []gapSpec{
	{row: 2, fault: FaultReadBaseline, ingresses: []Ingress{IngressA2A}, reason: "every A2A call is a mutation"},
	{row: 2, fault: FaultReadBaseline, ingresses: []Ingress{IngressCLI}, reason: "`ztd operator run` sends EXECUTE_BASH only"},
	{row: 25, fault: FaultL3ViaArguments, ingresses: []Ingress{IngressMCP, IngressA2A},
		reason: "no tool or skill argument can carry an L3 proof; the only attachment is ResumeWithL3Proof, fed by the WebAuthn assertion (T06 handoff, answer a)"},
}

// Catalogue returns every case and every gap of the suite, in a stable order.
func Catalogue() ([]Case, []Gap, error) {
	corpus, err := doctrinecorpus.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("catalogue: load corpus: %w", err)
	}

	var cases []Case
	for _, spec := range rowSpecs {
		entries := selectEntries(spec.corpus, corpus)
		for _, fault := range spec.faults {
			for _, ingress := range spec.ingresses {
				for _, wire := range spec.postures {
					posture, err := GovernancePosture(wire)
					if err != nil {
						return nil, nil, fmt.Errorf("catalogue: row %d: %w", spec.row, err)
					}
					for _, entry := range entries {
						cases = append(cases, newCase(spec, fault, ingress, PostureBinding{Posture: posture, Wire: wire}, entry))
					}
				}
			}
		}
	}

	var gaps []Gap
	for _, spec := range gapSpecs {
		for _, ingress := range spec.ingresses {
			gaps = append(gaps, Gap{Row: spec.row, Ingress: ingress, Fault: spec.fault, Reason: spec.reason})
		}
	}
	return cases, gaps, nil
}

// Applicable returns the cases for one posture, in catalogue order. A2A is
// opt-in at the Gateway: without A2A ingress its cases are not applicable.
func Applicable(posture evalv1.EvaluationGovernancePosture, a2aIngress bool) ([]Case, error) {
	if _, err := GovernancePosture(posture); err != nil {
		return nil, fmt.Errorf("catalogue: applicable: %w", err)
	}
	all, _, err := Catalogue()
	if err != nil {
		return nil, fmt.Errorf("catalogue: applicable: %w", err)
	}
	var out []Case
	for _, c := range all {
		if c.Posture != posture || (c.Ingress == IngressA2A && !a2aIngress) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// selectEntries returns the corpus entries a row expands over. A nil entry means
// the row carries no corpus command.
func selectEntries(set corpusSet, corpus *doctrinecorpus.Corpus) []*doctrinecorpus.Entry {
	if set == corpusNone {
		return []*doctrinecorpus.Entry{nil}
	}
	attacks := corpus.Attacks()
	var entries []*doctrinecorpus.Entry
	if set == corpusEveryAttack {
		for i := range attacks {
			entries = append(entries, &attacks[i])
		}
		return entries
	}
	for _, category := range doctrinecorpus.Categories() {
		for i := range attacks {
			if attacks[i].Category == category {
				entries = append(entries, &attacks[i])
				break
			}
		}
	}
	return entries
}

func newCase(spec rowSpec, fault Fault, ingress Ingress, posture PostureBinding, entry *doctrinecorpus.Entry) Case {
	c := Case{
		Row:      spec.row,
		Ingress:  ingress,
		Fault:    fault,
		Action:   spec.action,
		Posture:  posture.Wire,
		Attended: spec.attended,
	}
	entryID := ""
	if entry != nil {
		entryID = entry.ID
		c.CorpusEntryID = entry.ID
		c.Command = entry.Command
	}
	c.ID = caseID(spec.row, fault, ingress, posture.Posture, entryID)

	effect := effectFor(ingress, spec.action)
	for _, o := range spec.outcomes {
		c.Expect = append(c.Expect, o.expect(posture.Posture, spec.action, effect))
	}
	return c
}

// expect completes an outcome for one posture, action and effect.
func (o outcome) expect(posture constants.Posture, action Action, effect Effect) Expect {
	switch o.kind {
	case ExpectExecuted:
		return executedExpect(posture, action, effect)
	case ExpectRejected:
		return Expect{Kind: ExpectRejected, RejectionCode: o.rejection, Effect: EffectNone}
	case ExpectRefused:
		return Expect{Kind: ExpectRefused, RefusalCode: o.refusal, Effect: EffectNone}
	default:
		return Expect{Kind: ExpectSuspended, Effect: EffectNone}
	}
}

// executedExpect is the one expected outcome of an EXECUTED submission. L2 is
// REQUIRED_VALID under consensus and notary (every action), else NOT_REQUIRED.
// L3 is REQUIRED_VALID for a mutation under ratify and notary, else NOT_REQUIRED.
func executedExpect(posture constants.Posture, action Action, effect Effect) Expect {
	l2 := operatorv1.L2Status_L2_STATUS_NOT_REQUIRED
	if posture.RequiresL2() {
		l2 = operatorv1.L2Status_L2_STATUS_REQUIRED_VALID
	}
	l3 := operatorv1.L3Status_L3_STATUS_NOT_REQUIRED
	if action == ActionMutation && posture.RequiresL3() {
		l3 = operatorv1.L3Status_L3_STATUS_REQUIRED_VALID
	}
	return Expect{Kind: ExpectExecuted, L2Status: l2, L3Status: l3, Effect: effect}
}

// effectFor is the effect an executed case must show: a read leaves the canary
// untouched, an A2A mutation reaches the stub, every other mutation writes the target.
func effectFor(ingress Ingress, action Action) Effect {
	switch {
	case action == ActionRead:
		return EffectNone
	case ingress == IngressA2A:
		return EffectA2AStubCall
	default:
		return EffectTargetMarker
	}
}

// caseID builds the lower-case case id, with the corpus entry id appended for doctrine rows.
func caseID(row int, fault Fault, ingress Ingress, posture constants.Posture, entryID string) string {
	id := fmt.Sprintf("r%02d-%s-%s-%s", row, fault, ingress, posture)
	if entryID != "" {
		id += "-" + entryID
	}
	return strings.ToLower(id)
}
