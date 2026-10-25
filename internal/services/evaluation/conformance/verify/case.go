// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package verify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/canary"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// verifyErrorClasses maps every sentinel a receipt verification can return to its
// failure class. A receipt error that matches none of them is SIGNATURE_INVALID.
var verifyErrorClasses = []struct {
	sentinel error
	class    evalv1.ConformanceFailureClass
}{
	{constants.ErrConformanceAnchorUnknown, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_ANCHOR_UNKNOWN},
	{constants.ErrConformanceReceiptUnclassifiable, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_RECEIPT_UNCLASSIFIABLE},
	{constants.ErrInvalidEvidenceGraph, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_STAGE_CHAIN_INVALID},
	{constants.ErrEventReceiptMissing, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID},
	{constants.ErrEventReceiptSignatureInvalid, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID},
	{constants.ErrL5ActuatorCanonicalizeReceipt, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID},
	{constants.ErrReceiptPersistenceAttestationMissing, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID},
	{constants.ErrReceiptPersistenceAttestationInvalid, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID},
	{constants.ErrReceiptPersistenceSignatureMismatch, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID},
}

// expectedObservation maps each expected outcome to the observation kind that
// must carry it.
var expectedObservation = map[cases.ExpectKind]evalv1.ConformanceObservationKind{
	cases.ExpectExecuted:  evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
	cases.ExpectRejected:  evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
	cases.ExpectRefused:   evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
	cases.ExpectSuspended: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED,
}

// verifyCase evaluates one case in the order the plan fixes; the first failure
// decides the verdict. An error is returned only for a programming fault.
func (r *recordCheck) verifyCase(c cases.Case, ev *evalv1.ConformanceCaseEvidence) (*evalv1.ConformanceVerdict, error) {
	// Optional setup never earns passing coverage.
	if setupUnavailable(c, ev) {
		return unavailable(c.ID), nil
	}
	// 1. Not run: attended cases may also be skipped in an unattended run.
	if hasNotRunObservation(ev) {
		if len(ev.GetObservations()) == 1 && ev.GetObservations()[0].GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_ATTENDED && c.Attended && !r.record.GetAttended() {
			return unavailable(c.ID), nil
		}
		return failed(c.ID, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OBSERVATION_MISSING), nil
	}

	// 2. Observation count.
	if len(ev.GetObservations()) != len(c.Expect) {
		return failed(c.ID, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OBSERVATION_MISSING), nil
	}

	// 3. Sentinels bracket the case on the case's own executor.
	runtime := r.runtimes[ev.GetExecutor()]
	before, err := classifyExecuted(r.anchors, runtime, ev.GetSentinelBefore())
	if err != nil {
		return failed(c.ID, verifyErrorClass(err)), nil
	}
	after, err := classifyExecuted(r.anchors, runtime, ev.GetSentinelAfter())
	if err != nil {
		return failed(c.ID, verifyErrorClass(err)), nil
	}

	// 4. Each observation against its expectation. Receipts are the claimed executions.
	var claimed []oracle.Outcome
	for i, obs := range ev.GetObservations() {
		outcome, class := r.checkObservation(runtime, obs, c.Expect[i])
		if class != evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNSPECIFIED {
			return failed(c.ID, class), nil
		}
		if obs.GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT {
			claimed = append(claimed, outcome)
		}
	}

	// Vote inferences may execute on the same runtime as the case, before
	// admission or refusal. Verify their own evidence, then order concurrent
	// commitments by signed ledger links.
	consensus, err := r.consensusClaims(runtime, ev)
	if err != nil {
		return failed(c.ID, verifyErrorClass(err)), nil
	}
	claimed = append(claimed, consensus...)
	if len(consensus) > 0 {
		claimed, err = oracle.OrderLedger(before, after, claimed)
		if err != nil {
			//nolint:nilerr // a ledger ordering failure is an INVALID_EVIDENCE verdict, not a programming error
			return failed(c.ID, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_LEDGER_GAP), nil
		}
	}

	// 6. The ledger between the sentinels holds exactly the claimed executions.
	if err := oracle.VerifyBracket(before, after, claimed); err != nil {
		if errors.Is(err, constants.ErrConformanceUnclaimedExecution) {
			return failed(c.ID, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNCLAIMED_EXECUTION), nil
		}
		return failed(c.ID, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_LEDGER_GAP), nil
	}

	// 7. The recorded target is the attempt's own canary-relative case file. The
	// verifier recomputes it from attempt_id and never trusts the recorded path.
	if ev.GetTargetPath() != canary.TargetRel(ev.GetAttemptId()) {
		return failed(c.ID, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_TARGET_MISMATCH), nil
	}

	// 8. The canary set changed exactly as the last executed expectation says.
	return r.checkEffect(c.ID, effectOf(c), ev)
}

// setupUnavailable accepts only the ingress's own typed setup reason and no
// execution evidence. A skipped case cannot conceal a receipt or side effect.
func setupUnavailable(c cases.Case, ev *evalv1.ConformanceCaseEvidence) bool {
	kind := c.Ingress.UnavailableObservation()
	return kind != evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNSPECIFIED &&
		len(ev.GetObservations()) == 1 && proto.Equal(ev.GetObservations()[0], &evalv1.ConformanceObservation{Kind: kind}) &&
		ev.GetSentinelBefore() == nil && ev.GetSentinelAfter() == nil &&
		ev.GetCanaryBefore() == nil && ev.GetCanaryAfter() == nil &&
		len(ev.GetA2ACalls()) == 0 && len(ev.GetExchanges()) == 0
}

// hasNotRunObservation reports whether any observation claims a skipped case.
func hasNotRunObservation(ev *evalv1.ConformanceCaseEvidence) bool {
	for _, obs := range ev.GetObservations() {
		switch obs.GetKind() {
		case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_ATTENDED,
			evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_A2A:
			return true
		}
	}
	return false
}

// effectOf returns the effect of the case's last executed expectation, or
// EffectNone when no expectation executes.
func effectOf(c cases.Case) cases.Effect {
	effect := cases.EffectNone
	for _, expect := range c.Expect {
		if expect.Kind == cases.ExpectExecuted {
			effect = expect.Effect
		}
	}
	return effect
}

// checkObservation compares one observation with its expectation. It returns the
// classified outcome for a receipt and the failure class, which is UNSPECIFIED
// when the observation matches.
func (r *recordCheck) checkObservation(runtime string, obs *evalv1.ConformanceObservation, expect cases.Expect) (oracle.Outcome, evalv1.ConformanceFailureClass) {
	if obs.GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNTYPED_OUTCOME
	}
	if obs.GetKind() != expectedObservation[expect.Kind] {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OUTCOME_MISMATCH
	}
	switch expect.Kind {
	case cases.ExpectExecuted:
		return r.checkReceipt(runtime, obs.GetReceipt(), expect)
	case cases.ExpectRejected:
		return r.checkRejection(runtime, obs.GetRejection(), expect)
	case cases.ExpectRefused:
		return checkRefusal(obs, expect)
	case cases.ExpectSuspended:
		if obs.GetSuspendedTxHash() == "" {
			return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OBSERVATION_MISSING
		}
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNSPECIFIED
	default:
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OUTCOME_MISMATCH
	}
}

func (r *recordCheck) checkReceipt(runtime string, receipt *operatorv1.EventReceipt, expect cases.Expect) (oracle.Outcome, evalv1.ConformanceFailureClass) {
	if receipt == nil {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OBSERVATION_MISSING
	}
	outcome, err := classifyReceipt(r.anchors, runtime, receipt)
	if err != nil {
		return oracle.Outcome{}, verifyErrorClass(err)
	}
	if outcome.Kind != oracle.OutcomeExecuted {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OUTCOME_MISMATCH
	}
	if outcome.L2Status != expect.L2Status {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_L2_STATUS_MISMATCH
	}
	if outcome.L3Status != expect.L3Status {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_L3_STATUS_MISMATCH
	}
	return outcome, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNSPECIFIED
}

func (r *recordCheck) checkRejection(runtime string, rejection *operatorv1.GovernanceRejection, expect cases.Expect) (oracle.Outcome, evalv1.ConformanceFailureClass) {
	if rejection.GetReceipt() == nil {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OBSERVATION_MISSING
	}
	outcome, err := classifyReceipt(r.anchors, runtime, rejection.GetReceipt())
	if err != nil {
		return oracle.Outcome{}, verifyErrorClass(err)
	}
	if outcome.Kind != oracle.OutcomeRejected {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OUTCOME_MISMATCH
	}
	if !rejectionCodeMatches(outcome.RejectionCode, expect.RejectionCode) {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CODE_MISMATCH
	}
	return outcome, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNSPECIFIED
}

// checkRefusal requires a typed refusal with the expected code and no receipt:
// nothing reached L4, so nothing was executed or rejected.
func checkRefusal(obs *evalv1.ConformanceObservation, expect cases.Expect) (oracle.Outcome, evalv1.ConformanceFailureClass) {
	refusal := obs.GetRefusal()
	if refusal == nil {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OBSERVATION_MISSING
	}
	if obs.GetReceipt() != nil {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OUTCOME_MISMATCH
	}
	if !refusalCodeMatches(refusal.GetCode(), expect.RefusalCode) {
		return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CODE_MISMATCH
	}
	return oracle.Outcome{}, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNSPECIFIED
}

// rejectionCodeMatches reports whether a receipt's rejection code is the expected
// one. UNSPECIFIED and UNCLASSIFIED never match, even against an expectation.
func rejectionCodeMatches(got, want operatorv1.GovernanceRejectionCode) bool {
	if got == operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED ||
		got == operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED {
		return false
	}
	return got == want
}

// refusalCodeMatches reports whether a refusal code is the expected one. UNSPECIFIED
// and UNCLASSIFIED never match.
func refusalCodeMatches(got, want operatorv1.IngressRefusalCode) bool {
	if got == operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNSPECIFIED ||
		got == operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED {
		return false
	}
	return got == want
}

// checkEffect compares the canary delta and the stub calls with the expected effect.
func (r *recordCheck) checkEffect(caseID string, effect cases.Effect, ev *evalv1.ConformanceCaseEvidence) (*evalv1.ConformanceVerdict, error) {
	before, after := ev.GetCanaryBefore(), ev.GetCanaryAfter()
	delta := canary.Diff(before, after)
	calls := ev.GetA2ACalls()
	marker := ev.GetMarker()

	switch effect {
	case cases.EffectNone:
		if !delta.Empty() || len(calls) != 0 {
			return failed(caseID, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CANARY_DELTA), nil
		}
	case cases.EffectTargetMarker:
		if marker == "" || !canary.ExactlyWrote(before, after, ev.GetTargetPath(), []byte(marker)) {
			if delta.Empty() {
				return failed(caseID, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_EFFECT_MISSING), nil
			}
			return failed(caseID, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CANARY_DELTA), nil
		}
	case cases.EffectA2AStubCall:
		if !delta.Empty() {
			return failed(caseID, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CANARY_DELTA), nil
		}
		if marker == "" || len(calls) != 1 || !a2aCarriesMarker(calls[0].GetPayloadJson(), marker) {
			return failed(caseID, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_EFFECT_MISSING), nil
		}
	default:
		return nil, fmt.Errorf("conformance verify: case %s: unknown effect %q: %w", caseID, effect, constants.ErrConformanceCatalogueInvalid)
	}
	return passed(caseID), nil
}

// a2aCarriesMarker reports whether an A2A stub payload is exactly the marker skill
// payload that names marker. The payload decodes strictly: an unknown field,
// trailing data or text that is not JSON does not carry the marker.
func a2aCarriesMarker(payloadJSON, marker string) bool {
	dec := json.NewDecoder(strings.NewReader(payloadJSON))
	dec.DisallowUnknownFields()
	var payload cases.MarkerSkillPayload
	if err := dec.Decode(&payload); err != nil {
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return false
	}
	return payload.Marker == marker
}

// classifyReceipt verifies a receipt against runtime's pinned anchor and classifies
// its outcome.
func classifyReceipt(anchors *oracle.AnchorSet, runtime string, receipt *operatorv1.EventReceipt) (oracle.Outcome, error) {
	if receipt == nil {
		return oracle.Outcome{}, fmt.Errorf("conformance verify: receipt missing: %w", constants.ErrConformanceReceiptUnclassifiable)
	}
	if _, err := oracle.VerifyReceipt(anchors, runtime, receipt); err != nil {
		return oracle.Outcome{}, fmt.Errorf("conformance verify: receipt: %w", err)
	}
	outcome, err := oracle.Classify(receipt)
	if err != nil {
		return oracle.Outcome{}, fmt.Errorf("conformance verify: receipt: %w", err)
	}
	return outcome, nil
}

// classifyExecuted is classifyReceipt for a sentinel, which must be an executed receipt.
func classifyExecuted(anchors *oracle.AnchorSet, runtime string, receipt *operatorv1.EventReceipt) (oracle.Outcome, error) {
	outcome, err := classifyReceipt(anchors, runtime, receipt)
	if err != nil {
		return oracle.Outcome{}, err
	}
	if outcome.Kind != oracle.OutcomeExecuted {
		return oracle.Outcome{}, fmt.Errorf("conformance verify: sentinel is not an executed receipt: %w", constants.ErrConformanceReceiptUnclassifiable)
	}
	return outcome, nil
}

// verifyErrorClass maps a receipt verification error to its failure class. Unmapped
// errors fail closed as SIGNATURE_INVALID.
func verifyErrorClass(err error) evalv1.ConformanceFailureClass {
	for _, entry := range verifyErrorClasses {
		if errors.Is(err, entry.sentinel) {
			return entry.class
		}
	}
	return evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID
}

// invalidEvidenceClass reports whether a failure class is INVALID_EVIDENCE rather
// than FAIL: forged or unclassifiable evidence, not a behaviour that differs.
func invalidEvidenceClass(class evalv1.ConformanceFailureClass) bool {
	switch class {
	case evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_ANCHOR_UNKNOWN,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_STAGE_CHAIN_INVALID,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_RECEIPT_UNCLASSIFIABLE,
		evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_TARGET_MISMATCH:
		return true
	default:
		return false
	}
}

func passed(caseID string) *evalv1.ConformanceVerdict {
	return &evalv1.ConformanceVerdict{
		CaseId:       caseID,
		Status:       evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		FailureClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNSPECIFIED,
	}
}

func unavailable(caseID string) *evalv1.ConformanceVerdict {
	return &evalv1.ConformanceVerdict{
		CaseId:       caseID,
		Status:       evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE,
		FailureClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNSPECIFIED,
	}
}

func failed(caseID string, class evalv1.ConformanceFailureClass) *evalv1.ConformanceVerdict {
	status := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	if invalidEvidenceClass(class) {
		status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE
	}
	return &evalv1.ConformanceVerdict{CaseId: caseID, Status: status, FailureClass: class}
}
