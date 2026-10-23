// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package oracle

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func linkOutcome(txID, prior, hash string) Outcome {
	return Outcome{Kind: OutcomeExecuted, Commitment: &Link{Prior: prior, Hash: hash, TransactionID: txID}}
}

func TestVerifyBracket(t *testing.T) {
	before := linkOutcome("tx-before", "h-0", "h-1")
	claimedA := linkOutcome("tx-a", "h-1", "h-2")
	claimedB := linkOutcome("tx-b", "h-2", "h-3")

	tests := []struct {
		name    string
		before  Outcome
		after   Outcome
		claimed []Outcome
		want    error
	}{
		{
			name:   "zero claimed and adjacent sentinels",
			before: before, after: linkOutcome("tx-after", "h-1", "h-9"),
		},
		{
			name:   "genesis opening sentinel (empty prior) on a fresh ledger",
			before: linkOutcome("tx-before", "", "h-1"), after: linkOutcome("tx-after", "h-1", "h-9"),
		},
		{
			name:   "closing sentinel with an empty prior does not follow the opening sentinel",
			before: before, after: linkOutcome("tx-after", "", "h-9"),
			want: constants.ErrConformanceUnclaimedExecution,
		},
		{
			name:   "claimed outcome with an empty prior does not connect",
			before: before, after: linkOutcome("tx-after", "h-2", "h-9"),
			claimed: []Outcome{linkOutcome("tx-a", "", "h-2")},
			want:    constants.ErrConformanceLedgerGap,
		},
		{
			name:   "zero claimed with an unclaimed execution between sentinels",
			before: before, after: linkOutcome("tx-after", "h-2", "h-9"),
			want: constants.ErrConformanceUnclaimedExecution,
		},
		{
			name:   "one claimed link that connects",
			before: before, after: linkOutcome("tx-after", "h-2", "h-9"),
			claimed: []Outcome{claimedA},
		},
		{
			name:   "two claimed links that connect in order",
			before: before, after: linkOutcome("tx-after", "h-3", "h-9"),
			claimed: []Outcome{claimedA, claimedB},
		},
		{
			name:   "gap: claimed prior does not match the before hash",
			before: before, after: linkOutcome("tx-after", "h-2", "h-9"),
			claimed: []Outcome{linkOutcome("tx-a", "h-x", "h-2")},
			want:    constants.ErrConformanceLedgerGap,
		},
		{
			name:   "extra link: closing sentinel does not follow the last claimed hash",
			before: before, after: linkOutcome("tx-after", "h-1", "h-9"),
			claimed: []Outcome{claimedA},
			want:    constants.ErrConformanceUnclaimedExecution,
		},
		{
			name:   "before and after are the same transaction",
			before: before, after: linkOutcome("tx-before", "h-1", "h-9"),
			want: constants.ErrConformanceLedgerGap,
		},
		{
			name:   "claimed outcome with no ledger link",
			before: before, after: linkOutcome("tx-after", "h-1", "h-9"),
			claimed: []Outcome{{Kind: OutcomeRejected}},
			want:    constants.ErrConformanceLedgerGap,
		},
		{
			name:   "sentinel that is not an executed outcome",
			before: Outcome{Kind: OutcomeRejected}, after: linkOutcome("tx-after", "h-1", "h-9"),
			want: constants.ErrConformanceLedgerGap,
		},
		{
			name:   "sentinel with no commitment link",
			before: before, after: Outcome{Kind: OutcomeExecuted},
			want: constants.ErrConformanceLedgerGap,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyBracket(tt.before, tt.after, tt.claimed)
			if tt.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

// TestOrderLedgerThenVerifyBracket covers claims gathered across roles that
// share one runtime: they arrive out of ledger order, and ordering must neither
// hide an unclaimed execution nor accept a link that never joins the ledger.
func TestOrderLedgerThenVerifyBracket(t *testing.T) {
	before := linkOutcome("tx-before", "h-0", "h-1")
	a := linkOutcome("tx-a", "h-1", "h-2")
	b := linkOutcome("tx-b", "h-2", "h-3")
	c := linkOutcome("tx-c", "h-3", "h-4")

	tests := []struct {
		name    string
		after   Outcome
		claimed []Outcome
		want    error
	}{
		{name: "claims in ledger order", after: linkOutcome("tx-after", "h-4", "h-9"), claimed: []Outcome{a, b, c}},
		{name: "claims interleaved across roles", after: linkOutcome("tx-after", "h-4", "h-9"), claimed: []Outcome{c, a, b}},
		{name: "no claims, adjacent sentinels", after: linkOutcome("tx-after", "h-1", "h-9")},
		{
			name: "middle execution unclaimed", after: linkOutcome("tx-after", "h-4", "h-9"),
			claimed: []Outcome{c, a}, want: constants.ErrConformanceUnclaimedExecution,
		},
		{
			name: "trailing execution unclaimed", after: linkOutcome("tx-after", "h-4", "h-9"),
			claimed: []Outcome{b, a}, want: constants.ErrConformanceUnclaimedExecution,
		},
		{
			name: "claim that never joins the bracket", after: linkOutcome("tx-after", "h-2", "h-9"),
			claimed: []Outcome{a, linkOutcome("tx-x", "h-x", "h-y")}, want: constants.ErrConformanceLedgerGap,
		},
		{
			name: "claim with no ledger link", after: linkOutcome("tx-after", "h-2", "h-9"),
			claimed: []Outcome{a, {Kind: OutcomeRejected}}, want: constants.ErrConformanceLedgerGap,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ordered, err := OrderLedger(before, tt.after, tt.claimed)
			if err == nil {
				err = VerifyBracket(before, tt.after, ordered)
			}
			if tt.want == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.want)
		})
	}
}
