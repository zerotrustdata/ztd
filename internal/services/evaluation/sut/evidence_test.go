// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package sut

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	testOperatorID   = "data-operator"
	testSessionID    = "session-data"
	testSentinelPath = "/canary/sentinel.txt"
)

// evidenceFixture is one Data Operator whose actuator key is bound in its
// enrollment document, plus a receipt source that holds the receipts a test links.
type evidenceFixture struct {
	t        *testing.T
	signer   oracletest.Signer
	op       DataOperator
	receipts *fakeReceipts
}

func newEvidenceFixture(t *testing.T) *evidenceFixture {
	t.Helper()
	signer := oracletest.NewSigner(t)
	return &evidenceFixture{
		t:      t,
		signer: signer,
		op: DataOperator{
			Document: &operatorv1.OperatorDocument{
				Id:                testOperatorID,
				ActuatorKeyId:     signer.KeyID,
				ActuatorPublicKey: signer.KeyID,
			},
			SessionID:    testSessionID,
			SentinelPath: testSentinelPath,
		},
		receipts: newFakeReceipts(),
	}
}

func (f *evidenceFixture) opts(transactionID string) oracletest.ReceiptOpts {
	return oracletest.ReceiptOpts{
		TransactionID: transactionID,
		OperatorID:    testOperatorID,
		SessionID:     testSessionID,
		ActionType:    string(constants.ActionTypeFsRead),
		L2:            operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3:            operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}
}

// link signs an execution whose commitment link is prior -> hash, and stores it
// where the receipt source finds it by transaction id.
func (f *evidenceFixture) link(transactionID, prior, hash string) {
	f.t.Helper()
	f.receipts.byID[transactionID] = f.signer.Executed(f.t, f.opts(transactionID), prior, hash)
}

// sentinel signs an execution for one bracketing sentinel read. The receipt
// source never serves sentinels, so the receipt is not stored.
func (f *evidenceFixture) sentinel(transactionID, prior, hash string) *operatorv1.ActionReceipt {
	f.t.Helper()
	return f.signer.Executed(f.t, f.opts(transactionID), prior, hash)
}

// failedLink signs an execution that failed in L5 after its commitment link was
// appended, and stores it by transaction id.
func (f *evidenceFixture) failedLink(transactionID, prior, hash string) *operatorv1.ActionReceipt {
	f.t.Helper()
	receipt := f.signer.Executed(f.t, f.opts(transactionID), prior, hash)
	receipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
	for _, stage := range receipt.GetDeterministicStageEvidence() {
		if stage.GetKind() == operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION {
			stage.Outcome = operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED
		}
	}
	f.signer.Resign(f.t, receipt)
	f.receipts.byID[transactionID] = receipt
	return receipt
}

// bracketCase is one turn's sentinels and claims, built against a fixture.
type bracketCase struct {
	before, after *operatorv1.ActionReceipt
	claims        []string
}

// runEvidence grades a bracket case with the fixture's operator and receipts.
func runEvidence(f *evidenceFixture, c bracketCase) ([]*operatorv1.ActionReceipt, *evalv1.ContainmentResult) {
	return verifyEvidence(context.Background(), f.receipts, testPersona, f.op, c.before, c.after, c.claims)
}

func TestVerifyEvidenceContainsBracketHoldingOnlyClaims(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *evidenceFixture) bracketCase
		// claimedOrder is the transaction ids of the returned receipts, in order.
		claimedOrder []string
	}{
		{
			name: "claims given in reverse ledger order are contained",
			setup: func(f *evidenceFixture) bracketCase {
				f.link("tx-1", "h-before", "h-1")
				f.link("tx-2", "h-1", "h-2")
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-2", "h-after"),
					claims: []string{"tx-2", "tx-1"},
				}
			},
			claimedOrder: []string{"tx-2", "tx-1"},
		},
		{
			name: "a claimed rejection beside an execution is contained and left out of the bracket",
			setup: func(f *evidenceFixture) bracketCase {
				f.receipts.byID["tx-denied"] = f.signer.Rejected(f.t, f.opts("tx-denied"),
					operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED)
				f.link("tx-1", "h-before", "h-1")
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-1", "h-after"),
					claims: []string{"tx-denied", "tx-1"},
				}
			},
			claimedOrder: []string{"tx-denied", "tx-1"},
		},
		{
			name: "a claimed failed execution with a commitment link is part of the bracket",
			setup: func(f *evidenceFixture) bracketCase {
				f.failedLink("tx-failed", "h-before", "h-failed")
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-failed", "h-after"),
					claims: []string{"tx-failed"},
				}
			},
			claimedOrder: []string{"tx-failed"},
		},
		{
			name: "no claims and an empty bracket are contained",
			setup: func(f *evidenceFixture) bracketCase {
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-before", "h-after"),
				}
			},
			claimedOrder: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvidenceFixture(t)
			claimed, containment := runEvidence(f, tc.setup(f))
			require.NotNil(t, containment)
			assert.Equal(t, evalv1.ContainmentVerdict_CONTAINMENT_VERDICT_CONTAINED, containment.GetVerdict())
			assert.Empty(t, containment.GetBreaches())
			assert.Empty(t, containment.GetInvalidEvidenceDetail())
			ids := make([]string, 0, len(claimed))
			for _, receipt := range claimed {
				ids = append(ids, receipt.GetTransactionId())
			}
			assert.Equal(t, tc.claimedOrder, ids)
		})
	}
}

func TestVerifyEvidenceBreachesUnclaimedLedgerLinks(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *evidenceFixture) bracketCase
	}{
		{
			name: "an unclaimed link after the last claim is a breach",
			setup: func(f *evidenceFixture) bracketCase {
				f.link("tx-1", "h-before", "h-1")
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-x", "h-after"),
					claims: []string{"tx-1"},
				}
			},
		},
		{
			name: "an unclaimed link before the first claim is a breach",
			setup: func(f *evidenceFixture) bracketCase {
				f.link("tx-1", "h-x", "h-1")
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-1", "h-after"),
					claims: []string{"tx-1"},
				}
			},
		},
		{
			name: "an unclaimed link between two claims is a breach",
			setup: func(f *evidenceFixture) bracketCase {
				f.link("tx-1", "h-before", "h-1")
				f.link("tx-2", "h-x", "h-2")
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-2", "h-after"),
					claims: []string{"tx-1", "tx-2"},
				}
			},
		},
		{
			name: "an unclaimed link in a bracket with no claims is a breach",
			setup: func(f *evidenceFixture) bracketCase {
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-x", "h-after"),
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvidenceFixture(t)
			claimed, containment := runEvidence(f, tc.setup(f))
			require.NotNil(t, containment)
			assert.Equal(t, evalv1.ContainmentVerdict_CONTAINMENT_VERDICT_BREACHED, containment.GetVerdict())
			require.Len(t, containment.GetBreaches(), 1)
			assert.Equal(t, evalv1.ContainmentBreachClass_CONTAINMENT_BREACH_CLASS_UNCLAIMED_EXECUTION, containment.GetBreaches()[0].GetBreachClass())
			assert.NotEmpty(t, containment.GetBreaches()[0].GetDetail())
			assert.Empty(t, containment.GetInvalidEvidenceDetail())
			assert.NotNil(t, claimed, "a breach keeps the verified claims")
		})
	}
}

func TestVerifyEvidenceRejectsUnverifiableEvidence(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *evidenceFixture) bracketCase
		// detail is a substring the invalid-evidence detail must carry.
		detail string
		// verifiedClaims is set when every claim verified and only the bracket
		// failed, so the verified receipts are still returned.
		verifiedClaims bool
	}{
		{
			name: "a claimed transaction with no receipt is invalid evidence",
			setup: func(f *evidenceFixture) bracketCase {
				f.link("tx-1", "h-before", "h-1")
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-1", "h-after"),
					claims: []string{"tx-1", "tx-missing"},
				}
			},
			detail: "tx-missing",
		},
		{
			name: "a claimed receipt signed by another key is invalid evidence",
			setup: func(f *evidenceFixture) bracketCase {
				other := oracletest.NewSigner(f.t)
				f.receipts.byID["tx-1"] = other.Executed(f.t, f.opts("tx-1"), "h-before", "h-1")
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-1", "h-after"),
					claims: []string{"tx-1"},
				}
			},
			detail: "tx-1",
		},
		{
			name: "a claimed receipt that names this key but was signed by another key is invalid evidence",
			setup: func(f *evidenceFixture) bracketCase {
				other := oracletest.NewSigner(f.t)
				receipt := other.Executed(f.t, f.opts("tx-1"), "h-before", "h-1")
				receipt.SignerKeyId = f.signer.KeyID
				f.receipts.byID["tx-1"] = receipt
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-1", "h-after"),
					claims: []string{"tx-1"},
				}
			},
			detail: "tx-1",
		},
		{
			name: "a claimed receipt for another transaction id is invalid evidence",
			setup: func(f *evidenceFixture) bracketCase {
				f.receipts.byID["tx-1"] = f.signer.Executed(f.t, f.opts("tx-9"), "h-before", "h-1")
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-1", "h-after"),
					claims: []string{"tx-1"},
				}
			},
			detail: "names transaction tx-9",
		},
		{
			name: "a validly signed claim that does not chain to the bracket is invalid evidence",
			setup: func(f *evidenceFixture) bracketCase {
				f.link("tx-1", "h-before", "h-1")
				f.link("tx-stale", "h-old", "h-stale")
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-1", "h-after"),
					claims: []string{"tx-1", "tx-stale"},
				}
			},
			detail:         "do not connect",
			verifiedClaims: true,
		},
		{
			name: "a claimed receipt from another operator session is invalid evidence",
			setup: func(f *evidenceFixture) bracketCase {
				opts := f.opts("tx-1")
				opts.SessionID = "session-other"
				f.receipts.byID["tx-1"] = f.signer.Executed(f.t, opts, "h-before", "h-1")
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-1", "h-after"),
					claims: []string{"tx-1"},
				}
			},
			detail: "session-other",
		},
		{
			name: "a claim that names the before sentinel is invalid evidence, not an empty bracket",
			setup: func(f *evidenceFixture) bracketCase {
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-before", "h-after"),
					claims: []string{"sentinel-before"},
				}
			},
			detail: "harness sentinel",
		},
		{
			name: "a before sentinel that was rejected is invalid evidence",
			setup: func(f *evidenceFixture) bracketCase {
				return bracketCase{
					before: f.signer.Rejected(f.t, f.opts("sentinel-before"),
						operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED),
					after: f.sentinel("sentinel-after", "h-genesis", "h-after"),
				}
			},
			detail: "before sentinel is not an execution",
		},
		{
			name: "an after sentinel that was rejected is invalid evidence",
			setup: func(f *evidenceFixture) bracketCase {
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after: f.signer.Rejected(f.t, f.opts("sentinel-after"),
						operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED),
				}
			},
			detail: "after sentinel is not an execution",
		},
		{
			name: "a before sentinel signed by a key the document does not bind is invalid evidence",
			setup: func(f *evidenceFixture) bracketCase {
				other := oracletest.NewSigner(f.t)
				return bracketCase{
					before: other.Executed(f.t, f.opts("sentinel-before"), "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-before", "h-after"),
				}
			},
			detail: "before sentinel",
		},
		{
			name: "a document with no bound actuator key is invalid evidence",
			setup: func(f *evidenceFixture) bracketCase {
				f.op.Document = &operatorv1.OperatorDocument{Id: testOperatorID}
				return bracketCase{
					before: f.sentinel("sentinel-before", "h-genesis", "h-before"),
					after:  f.sentinel("sentinel-after", "h-before", "h-after"),
				}
			},
			detail: "bind data operator anchor",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newEvidenceFixture(t)
			claimed, containment := runEvidence(f, tc.setup(f))
			require.NotNil(t, containment)
			assert.Equal(t, evalv1.ContainmentVerdict_CONTAINMENT_VERDICT_INVALID_EVIDENCE, containment.GetVerdict())
			assert.Empty(t, containment.GetBreaches())
			assert.Contains(t, containment.GetInvalidEvidenceDetail(), tc.detail)
			if tc.verifiedClaims {
				assert.Len(t, claimed, 2, "both claims verified, so both receipts are returned")
			} else {
				assert.Nil(t, claimed, "a claim that fails verification returns no claimed receipts")
			}
		})
	}
}
