// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/governance/doctrinecorpus"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	fixtureRunID   = "run-1"
	fixtureRemote  = "remote-op"
	fixtureAction  = "FS_READ"
	caseDirect     = "direct-executed"
	caseRejected   = "direct-rejected"
	caseRefused    = "dispatch-refused"
	caseAttended   = "mcp-attended"
	fixtureCanary  = "alpha.txt"
	fixtureSession = "session-1"
)

// fixture builds a passing run record for a four-case list: a DIRECT executed
// mutation, a DIRECT rejection, an outbound dispatch refusal and an attended MCP
// case. Every receipt is signed by a key pinned in the record.
type fixture struct {
	t          *testing.T
	gateway    oracletest.Signer
	remote     oracletest.Signer
	applicable []cases.Case
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{
		t:          t,
		gateway:    oracletest.NewSigner(t),
		remote:     oracletest.NewSigner(t),
		applicable: fixtureCases(),
	}
}

func fixtureCases() []cases.Case {
	notRequired := func(kind cases.ExpectKind, effect cases.Effect) cases.Expect {
		return cases.Expect{
			Kind:     kind,
			L2Status: operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
			L3Status: operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
			Effect:   effect,
		}
	}
	posture := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE
	return []cases.Case{
		{ID: caseDirect, Ingress: cases.IngressDirect, Fault: cases.FaultNone, Action: cases.ActionMutation,
			Posture: posture, Expect: []cases.Expect{notRequired(cases.ExpectExecuted, cases.EffectTargetMarker)}},
		{ID: caseRejected, Ingress: cases.IngressDirect, Fault: cases.FaultDoctrine, Action: cases.ActionMutation,
			Posture: posture, Expect: []cases.Expect{{
				Kind:          cases.ExpectRejected,
				RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE,
				Effect:        cases.EffectNone,
			}}},
		{ID: caseRefused, Ingress: cases.IngressDispatchOutbound, Fault: cases.FaultMalformedJSON, Action: cases.ActionMutation,
			Posture: posture, Expect: []cases.Expect{{
				Kind:        cases.ExpectRefused,
				RefusalCode: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MISSING_FIELD,
				Effect:      cases.EffectNone,
			}}},
		{ID: caseAttended, Ingress: cases.IngressMCP, Fault: cases.FaultAttendedApprove, Action: cases.ActionMutation,
			Posture: posture, Attended: true, Expect: []cases.Expect{notRequired(cases.ExpectExecuted, cases.EffectA2AStubCall)}},
	}
}

// signerFor returns the actuator key of the runtime an executor names.
func (f *fixture) signerFor(executor cases.Executor) oracletest.Signer {
	if executor == cases.ExecutorOutbound {
		return f.remote
	}
	return f.gateway
}

func runtimeFor(executor cases.Executor) string {
	if executor == cases.ExecutorOutbound {
		return fixtureRemote
	}
	return oracle.GatewayRuntimeID
}

func receiptOpts(label, runtime, suffix string, l2 operatorv1.L2Status, l3 operatorv1.L3Status) oracletest.ReceiptOpts {
	return oracletest.ReceiptOpts{
		TransactionID: label + "-" + suffix,
		OperatorID:    runtime,
		SessionID:     fixtureSession,
		ActionType:    fixtureAction,
		L2:            l2,
		L3:            l3,
	}
}

// executedReceipt builds the executed receipt of a case: its ledger link runs from
// the case's before hash to its after hash, so the bracket holds exactly one claim.
func (f *fixture) executedReceipt(c cases.Case, l2 operatorv1.L2Status, l3 operatorv1.L3Status) *operatorv1.ActionReceipt {
	executor := c.Ingress.Executor()
	return f.signerFor(executor).Executed(f.t,
		receiptOpts(c.ID, runtimeFor(executor), "exec", l2, l3), "h0-"+c.ID, "h1-"+c.ID)
}

func canaryEntry(path, content string) *evalv1.CanaryEntry {
	sum := sha256.Sum256([]byte(content))
	return &evalv1.CanaryEntry{Path: path, Sha256: hex.EncodeToString(sum[:]), Size: int64(len(content))}
}

func targetPath(c cases.Case) string {
	return "targets/" + c.ID + ".txt"
}

func (f *fixture) evidence(c cases.Case) *evalv1.ConformanceCaseEvidence {
	f.t.Helper()
	executor := c.Ingress.Executor()
	signer := f.signerFor(executor)
	runtime := runtimeFor(executor)
	expect := c.Expect[0]
	marker := "ztd-conformance " + fixtureRunID + " " + c.ID

	before := signer.Executed(f.t, receiptOpts(c.ID, runtime, "before", operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED),
		"prior-"+c.ID, "h0-"+c.ID)
	last := "h0-" + c.ID
	var observations []*evalv1.ConformanceObservation
	switch expect.Kind {
	case cases.ExpectExecuted:
		observations = []*evalv1.ConformanceObservation{{
			Kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
			Receipt: f.executedReceipt(c, expect.L2Status, expect.L3Status),
		}}
		last = "h1-" + c.ID
	case cases.ExpectRejected:
		observations = []*evalv1.ConformanceObservation{{
			Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
			Rejection: &operatorv1.GovernanceRejection{
				Error:   "rejected",
				Receipt: signer.Rejected(f.t, receiptOpts(c.ID, runtime, "reject", operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED), expect.RejectionCode),
			},
		}}
	case cases.ExpectRefused:
		observations = []*evalv1.ConformanceObservation{{
			Kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
			Refusal: &operatorv1.IngressRefusal{Code: expect.RefusalCode, Error: "refused"},
		}}
	}
	after := signer.Executed(f.t, receiptOpts(c.ID, runtime, "after", operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED),
		last, "h2-"+c.ID)

	canaryBefore := &evalv1.CanaryManifest{Entries: []*evalv1.CanaryEntry{canaryEntry(fixtureCanary, "ztd canary alpha")}}
	canaryAfter := &evalv1.CanaryManifest{Entries: []*evalv1.CanaryEntry{canaryEntry(fixtureCanary, "ztd canary alpha")}}
	var calls []*evalv1.ConformanceA2ACall
	switch effectOf(c) {
	case cases.EffectTargetMarker:
		canaryAfter.Entries = append(canaryAfter.Entries, canaryEntry(targetPath(c), marker))
	case cases.EffectA2AStubCall:
		calls = []*evalv1.ConformanceA2ACall{{SkillName: "stub", PayloadJson: `{"text":"` + marker + `"}`}}
	}

	return &evalv1.ConformanceCaseEvidence{
		CaseId:         c.ID,
		AttemptId:      "attempt-" + c.ID,
		Executor:       string(executor),
		TargetPath:     targetPath(c),
		Marker:         marker,
		Observations:   observations,
		SentinelBefore: before,
		SentinelAfter:  after,
		CanaryBefore:   canaryBefore,
		CanaryAfter:    canaryAfter,
		A2ACalls:       calls,
	}
}

func (f *fixture) quiescence(executor cases.Executor) *evalv1.ConformanceQuiescence {
	f.t.Helper()
	label := "quiesce-" + string(executor)
	runtime := runtimeFor(executor)
	signer := f.signerFor(executor)
	first := signer.Executed(f.t, receiptOpts(label, runtime, "1", operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED),
		"quiesce-prior-"+string(executor), "quiesce-h1-"+string(executor))
	second := signer.Executed(f.t, receiptOpts(label, runtime, "2", operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED),
		"quiesce-h1-"+string(executor), "quiesce-h2-"+string(executor))
	return &evalv1.ConformanceQuiescence{Executor: string(executor), First: first, Second: second}
}

// record returns a passing run record for the fixture's case list.
func (f *fixture) record() *evalv1.ConformanceRunRecord {
	f.t.Helper()
	rec := &evalv1.ConformanceRunRecord{
		RunId:                fixtureRunID,
		SuiteId:              cases.SuiteID,
		SuiteVersion:         cases.SuiteVersion,
		Posture:              evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		Anchors:              []*evalv1.ConformanceAnchor{f.gateway.Anchor(oracle.GatewayRuntimeID), f.remote.Anchor(fixtureRemote)},
		CanaryRoot:           "evaluation/targets/conformance-" + fixtureRunID,
		Quiescence:           []*evalv1.ConformanceQuiescence{f.quiescence(cases.ExecutorEmbedded), f.quiescence(cases.ExecutorOutbound)},
		DoctrineCorpusSha256: doctrinecorpus.SHA256(),
		Attended:             true,
	}
	for _, c := range f.applicable {
		rec.Cases = append(rec.Cases, f.evidence(c))
	}
	return rec
}

// caseEvidence returns the mutable evidence of one case in rec.
func caseEvidence(t *testing.T, rec *evalv1.ConformanceRunRecord, caseID string) *evalv1.ConformanceCaseEvidence {
	t.Helper()
	for _, ev := range rec.GetCases() {
		if ev.GetCaseId() == caseID {
			return ev
		}
	}
	t.Fatalf("record has no case %s", caseID)
	return nil
}

// verdictOf returns the verdict of one case in set.
func verdictOf(t *testing.T, set *evalv1.ConformanceVerdictSet, caseID string) *evalv1.ConformanceVerdict {
	t.Helper()
	for _, v := range set.GetVerdicts() {
		if v.GetCaseId() == caseID {
			return v
		}
	}
	t.Fatalf("verdict set has no verdict for %s", caseID)
	return nil
}

func verifyRecord(t *testing.T, rec *evalv1.ConformanceRunRecord, applicable []cases.Case) *evalv1.ConformanceVerdictSet {
	t.Helper()
	set, err := verifyAgainst(rec, applicable)
	require.NoError(t, err)
	return set
}

func TestVerifyAgainstPassingRecordIsPass(t *testing.T) {
	f := newFixture(t)
	set := verifyRecord(t, f.record(), f.applicable)

	assert.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS, set.GetSuiteVerdict())
	assert.Equal(t, fixtureRunID, set.GetRunId())
	require.Len(t, set.GetVerdicts(), len(f.applicable))
	for _, v := range set.GetVerdicts() {
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, v.GetStatus(), v.GetCaseId())
		assert.Equal(t, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNSPECIFIED, v.GetFailureClass(), v.GetCaseId())
	}
}

func TestVerifyAgainstUnattendedAttendedCaseIsIncomplete(t *testing.T) {
	f := newFixture(t)
	rec := f.record()
	rec.Attended = false
	notRun := caseEvidence(t, rec, caseAttended)
	notRun.Observations = []*evalv1.ConformanceObservation{{
		Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_ATTENDED,
	}}
	notRun.SentinelBefore, notRun.SentinelAfter = nil, nil
	notRun.CanaryBefore, notRun.CanaryAfter, notRun.A2ACalls = nil, nil, nil

	set := verifyRecord(t, rec, f.applicable)

	assert.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INCOMPLETE, set.GetSuiteVerdict())
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE, verdictOf(t, set, caseAttended).GetStatus())
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, verdictOf(t, set, caseDirect).GetStatus())
}

// fixtureRecordMutation mutates one thing in the passing record and names the
// verdict it must produce. An empty wantCase means a record check failed, which
// gives suite INVALID_EVIDENCE with no case verdicts.
type fixtureRecordMutation struct {
	name       string
	mutate     func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord)
	wantSuite  evalv1.ConformanceSuiteVerdict
	wantCase   string
	wantStatus evalv1.EvaluationVerdictStatus
	wantClass  evalv1.ConformanceFailureClass
}

var (
	suiteFail     = evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_FAIL
	suiteInvalid  = evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE
	statusFail    = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	statusInvalid = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE
)

func flipHexDigit(digit byte) byte {
	if digit == '0' {
		return '1'
	}
	return '0'
}

func TestVerifyAgainstNegativeMutations(t *testing.T) {
	tests := []fixtureRecordMutation{
		{
			name: "wrong rejection code",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				receipt := caseEvidence(t, rec, caseRejected).GetObservations()[0].GetRejection().GetReceipt()
				receipt.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_PROOF_MISSING
				f.gateway.Resign(t, receipt)
			},
			wantSuite: suiteFail, wantCase: caseRejected, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CODE_MISMATCH,
		},
		{
			name: "wrong refusal code",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				caseEvidence(t, rec, caseRefused).GetObservations()[0].GetRefusal().Code =
					operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST
			},
			wantSuite: suiteFail, wantCase: caseRefused, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CODE_MISMATCH,
		},
		{
			name: "refusal carrying a receipt",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				caseEvidence(t, rec, caseRefused).GetObservations()[0].Receipt = caseEvidence(t, rec, caseDirect).GetSentinelBefore()
			},
			wantSuite: suiteFail, wantCase: caseRefused, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OUTCOME_MISMATCH,
		},
		{
			name: "untyped observation",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				obs := caseEvidence(t, rec, caseDirect).GetObservations()[0]
				obs.Kind = evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED
				obs.Receipt = nil
				obs.UntypedBody = []byte("<html>proxy error</html>")
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNTYPED_OUTCOME,
		},
		{
			name: "observation kind does not match the expected outcome",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				obs := caseEvidence(t, rec, caseDirect).GetObservations()[0]
				obs.Kind = evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED
				obs.Receipt = nil
				obs.SuspendedTxHash = "tx-pending"
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OUTCOME_MISMATCH,
		},
		{
			name: "missing observation",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				caseEvidence(t, rec, caseDirect).Observations = nil
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OBSERVATION_MISSING,
		},
		{
			name: "extra observation",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				ev := caseEvidence(t, rec, caseDirect)
				ev.Observations = append(ev.Observations, ev.GetObservations()[0])
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OBSERVATION_MISSING,
		},
		{
			name: "not-run observation on a case that is not attended",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				caseEvidence(t, rec, caseDirect).Observations = []*evalv1.ConformanceObservation{{
					Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_ATTENDED,
				}}
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OBSERVATION_MISSING,
		},
		{
			name: "attended case not run in a run marked attended",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				caseEvidence(t, rec, caseAttended).Observations = []*evalv1.ConformanceObservation{{
					Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_NOT_RUN_ATTENDED,
				}}
			},
			wantSuite: suiteFail, wantCase: caseAttended, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_OBSERVATION_MISSING,
		},
		{
			name: "L2 status mismatch",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				c := f.applicable[0]
				caseEvidence(t, rec, caseDirect).GetObservations()[0].Receipt =
					f.executedReceipt(c, operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED)
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_L2_STATUS_MISMATCH,
		},
		{
			name: "L3 status mismatch",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				c := f.applicable[0]
				caseEvidence(t, rec, caseDirect).GetObservations()[0].Receipt =
					f.executedReceipt(c, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID)
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_L3_STATUS_MISMATCH,
		},
		{
			name: "signature byte flipped without re-signing",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				receipt := caseEvidence(t, rec, caseRefused).GetSentinelBefore()
				sig := []byte(receipt.GetSignature())
				sig[0] = flipHexDigit(sig[0])
				receipt.Signature = string(sig)
			},
			wantSuite: suiteInvalid, wantCase: caseRefused, wantStatus: statusInvalid,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID,
		},
		{
			name: "receipt signed by an unpinned key",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				stranger := oracletest.NewSigner(t)
				c := f.applicable[0]
				caseEvidence(t, rec, caseDirect).SentinelAfter = stranger.Executed(t,
					receiptOpts(c.ID, oracle.GatewayRuntimeID, "after", operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED),
					"h1-"+c.ID, "h2-"+c.ID)
			},
			wantSuite: suiteInvalid, wantCase: caseDirect, wantStatus: statusInvalid,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_ANCHOR_UNKNOWN,
		},
		{
			name: "anchor pinned to the wrong runtime",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				rec.Anchors[1] = f.remote.Anchor("other-op")
			},
			wantSuite: suiteInvalid,
		},
		{
			name: "stage reordered and re-signed",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				receipt := caseEvidence(t, rec, caseDirect).GetSentinelAfter()
				stages := receipt.GetDeterministicStageEvidence()
				stages[1], stages[2] = stages[2], stages[1]
				f.gateway.Resign(t, receipt)
			},
			wantSuite: suiteInvalid, wantCase: caseDirect, wantStatus: statusInvalid,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_STAGE_CHAIN_INVALID,
		},
		{
			name: "sentinel is a rejection, not an execution",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				caseEvidence(t, rec, caseDirect).SentinelBefore = f.gateway.Rejected(t,
					receiptOpts(caseDirect, oracle.GatewayRuntimeID, "sentinel", operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED),
					operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE)
			},
			wantSuite: suiteInvalid, wantCase: caseDirect, wantStatus: statusInvalid,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_RECEIPT_UNCLASSIFIABLE,
		},
		{
			name: "extra link between the sentinels",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				c := f.applicable[0]
				caseEvidence(t, rec, caseDirect).SentinelAfter = f.gateway.Executed(t,
					receiptOpts(c.ID, oracle.GatewayRuntimeID, "after", operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED),
					"x-extra-"+c.ID, "h2-"+c.ID)
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_UNCLAIMED_EXECUTION,
		},
		{
			name: "broken ledger link",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				c := f.applicable[0]
				caseEvidence(t, rec, caseDirect).GetObservations()[0].Receipt = f.gateway.Executed(t,
					receiptOpts(c.ID, oracle.GatewayRuntimeID, "exec", operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED),
					"bogus-prior", "h1-"+c.ID)
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_LEDGER_GAP,
		},
		{
			name: "extra canary file",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				ev := caseEvidence(t, rec, caseDirect)
				ev.CanaryAfter.Entries = append(ev.CanaryAfter.Entries, canaryEntry("targets/extra.txt", "extra"))
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CANARY_DELTA,
		},
		{
			name: "target written with the wrong marker",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				ev := caseEvidence(t, rec, caseDirect)
				for i, entry := range ev.CanaryAfter.Entries {
					if entry.GetPath() == ev.GetTargetPath() {
						ev.CanaryAfter.Entries[i] = canaryEntry(ev.GetTargetPath(), "ztd-conformance run-1 wrong")
					}
				}
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CANARY_DELTA,
		},
		{
			name: "missing target write",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				ev := caseEvidence(t, rec, caseDirect)
				ev.CanaryAfter = ev.CanaryBefore
			},
			wantSuite: suiteFail, wantCase: caseDirect, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_EFFECT_MISSING,
		},
		{
			name: "canary changed by a rejected case",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				ev := caseEvidence(t, rec, caseRejected)
				ev.CanaryAfter.Entries = append(ev.CanaryAfter.Entries, canaryEntry("targets/rejected.txt", "leaked"))
			},
			wantSuite: suiteFail, wantCase: caseRejected, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CANARY_DELTA,
		},
		{
			name: "second A2A call",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				ev := caseEvidence(t, rec, caseAttended)
				ev.A2ACalls = append(ev.A2ACalls, ev.GetA2ACalls()[0])
			},
			wantSuite: suiteFail, wantCase: caseAttended, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_EFFECT_MISSING,
		},
		{
			name: "missing A2A call",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				caseEvidence(t, rec, caseAttended).A2ACalls = nil
			},
			wantSuite: suiteFail, wantCase: caseAttended, wantStatus: statusFail,
			wantClass: evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_EFFECT_MISSING,
		},
		{
			name: "missing case id",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				var kept []*evalv1.ConformanceCaseEvidence
				for _, ev := range rec.GetCases() {
					if ev.GetCaseId() != caseRefused {
						kept = append(kept, ev)
					}
				}
				rec.Cases = kept
			},
			wantSuite: suiteInvalid,
		},
		{
			name: "extra case id",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				ghost := caseEvidence(t, rec, caseDirect)
				rec.Cases = append(rec.Cases, &evalv1.ConformanceCaseEvidence{CaseId: "ghost-case", Executor: ghost.GetExecutor()})
			},
			wantSuite: suiteInvalid,
		},
		{
			name: "duplicate case id",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				rec.Cases = append(rec.Cases, caseEvidence(t, rec, caseDirect))
			},
			wantSuite: suiteInvalid,
		},
		{
			name: "wrong doctrine corpus digest",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				rec.DoctrineCorpusSha256 = "00"
			},
			wantSuite: suiteInvalid,
		},
		{
			name: "non-quiescent quiescence pair",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				q := rec.Quiescence[0]
				q.First, q.Second = q.Second, q.First
			},
			wantSuite: suiteInvalid,
		},
		{
			name: "missing quiescence pair",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				rec.Quiescence = rec.Quiescence[:1]
			},
			wantSuite: suiteInvalid,
		},
		{
			name: "case executed on the wrong executor",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				caseEvidence(t, rec, caseDirect).Executor = string(cases.ExecutorOutbound)
			},
			wantSuite: suiteInvalid,
		},
		{
			name: "record without a gateway anchor",
			mutate: func(t *testing.T, f *fixture, rec *evalv1.ConformanceRunRecord) {
				rec.Anchors = rec.Anchors[1:]
			},
			wantSuite: suiteInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			rec := f.record()
			tt.mutate(t, f, rec)

			set := verifyRecord(t, rec, f.applicable)

			assert.Equal(t, tt.wantSuite, set.GetSuiteVerdict())
			if tt.wantCase == "" {
				assert.Empty(t, set.GetVerdicts(), "a record check failure carries no case verdicts")
				return
			}
			v := verdictOf(t, set, tt.wantCase)
			assert.Equal(t, tt.wantStatus, v.GetStatus())
			assert.Equal(t, tt.wantClass, v.GetFailureClass())
		})
	}
}

func TestVerifyAgainstUnclassifiedRejectionNeverMatches(t *testing.T) {
	f := newFixture(t)
	f.applicable[1].Expect[0].RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED
	rec := f.record()
	receipt := caseEvidence(t, rec, caseRejected).GetObservations()[0].GetRejection().GetReceipt()
	receipt.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED
	f.gateway.Resign(t, receipt)

	set := verifyRecord(t, rec, f.applicable)

	v := verdictOf(t, set, caseRejected)
	assert.Equal(t, statusFail, v.GetStatus())
	assert.Equal(t, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_CODE_MISMATCH, v.GetFailureClass())
}

func TestVerifyPersistedPassingRecordIsPass(t *testing.T) {
	f := newFixture(t)
	raw, err := evalv1.MarshalCanonical(f.record())
	require.NoError(t, err)
	set, err := f.verifyPersisted(raw, sha256Hex(raw))
	require.NoError(t, err)
	assert.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_PASS, set.GetSuiteVerdict())
}

func TestVerifyPersistedEveryFlippedBitIsInvalidEvidence(t *testing.T) {
	f := newFixture(t)
	raw, err := evalv1.MarshalCanonical(f.record())
	require.NoError(t, err)
	digest := sha256Hex(raw)

	stride := 1
	if len(raw) >= 64*1024 {
		stride = 7
	}
	for offset := 0; offset < len(raw); offset += stride {
		flipped := append([]byte(nil), raw...)
		flipped[offset] ^= 1 << (offset % 8)

		set, err := f.verifyPersisted(flipped, digest)
		require.NoError(t, err, "offset %d", offset)
		require.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE, set.GetSuiteVerdict(), "offset %d", offset)
	}
}

func TestVerifyPersistedDigestMismatchNamesTheDigestClass(t *testing.T) {
	f := newFixture(t)
	raw, err := evalv1.MarshalCanonical(f.record())
	require.NoError(t, err)

	set, err := f.verifyPersisted(raw, "00")
	require.NoError(t, err)

	assert.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE, set.GetSuiteVerdict())
	assert.Equal(t, fixtureRunID, set.GetRunId())
	require.Len(t, set.GetVerdicts(), 1)
	assert.Empty(t, set.GetVerdicts()[0].GetCaseId())
	assert.Equal(t, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_RECORD_DIGEST_MISMATCH, set.GetVerdicts()[0].GetFailureClass())
}

func TestVerifyPersistedNonCanonicalRecordIsInvalidEvidence(t *testing.T) {
	f := newFixture(t)
	canonical, err := evalv1.MarshalCanonical(f.record())
	require.NoError(t, err)
	raw := append(append([]byte(nil), canonical...), ' ')

	set, err := f.verifyPersisted(raw, sha256Hex(raw))
	require.NoError(t, err)

	assert.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE, set.GetSuiteVerdict())
	assert.Empty(t, set.GetRunId(), "a record that does not decode names no run")
	assert.Empty(t, set.GetVerdicts())
}

func TestVerifyPersistedReceiptSignatureFlipIsSignatureInvalid(t *testing.T) {
	f := newFixture(t)
	rec := f.record()
	receipt := caseEvidence(t, rec, caseDirect).GetSentinelBefore()
	sig := []byte(receipt.GetSignature())
	sig[0] = flipHexDigit(sig[0])
	receipt.Signature = string(sig)
	raw, err := evalv1.MarshalCanonical(rec)
	require.NoError(t, err)

	set, err := f.verifyPersisted(raw, sha256Hex(raw))
	require.NoError(t, err)

	assert.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE, set.GetSuiteVerdict())
	v := verdictOf(t, set, caseDirect)
	assert.Equal(t, statusInvalid, v.GetStatus())
	assert.Equal(t, evalv1.ConformanceFailureClass_CONFORMANCE_FAILURE_CLASS_SIGNATURE_INVALID, v.GetFailureClass())
}

func TestVerifyUnrealPostureIsInvalidEvidenceWithoutCatalogue(t *testing.T) {
	f := newFixture(t)
	rec := f.record()
	rec.Posture = evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED

	set, err := Verify(rec)
	require.NoError(t, err)

	assert.Equal(t, evalv1.ConformanceSuiteVerdict_CONFORMANCE_SUITE_VERDICT_INVALID_EVIDENCE, set.GetSuiteVerdict())
	assert.Empty(t, set.GetVerdicts())
}

// verifyPersisted runs VerifyPersisted's byte path against the fixture's case list.
func (f *fixture) verifyPersisted(raw []byte, digestHex string) (*evalv1.ConformanceVerdictSet, error) {
	return verifyPersisted(raw, digestHex, func(rec *evalv1.ConformanceRunRecord) (*evalv1.ConformanceVerdictSet, error) {
		return verifyAgainst(rec, f.applicable)
	})
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
