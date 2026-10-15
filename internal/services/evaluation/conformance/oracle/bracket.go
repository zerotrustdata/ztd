// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package oracle

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// VerifyBracket proves that, between two sentinel executions on one runtime,
// the commitment ledger holds exactly the claimed executions, in order.
// before and after must be verified, executed sentinels.
func VerifyBracket(before, after Outcome, claimed []Outcome) error {
	if before.Kind != OutcomeExecuted || after.Kind != OutcomeExecuted {
		return fmt.Errorf("oracle: verify bracket: sentinels are not executed: %w", constants.ErrConformanceLedgerGap)
	}
	if before.Commitment == nil || after.Commitment == nil {
		return fmt.Errorf("oracle: verify bracket: sentinel has no commitment link: %w", constants.ErrConformanceLedgerGap)
	}
	if before.Commitment.TransactionID == after.Commitment.TransactionID {
		return fmt.Errorf("oracle: verify bracket: sentinels are the same transaction %s: %w", before.Commitment.TransactionID, constants.ErrConformanceLedgerGap)
	}

	expected := before.Commitment.Hash
	for index, outcome := range claimed {
		if outcome.Commitment == nil {
			return fmt.Errorf("oracle: verify bracket: claimed outcome %d has no ledger link: %w", index, constants.ErrConformanceLedgerGap)
		}
		if outcome.Commitment.Prior != expected {
			return fmt.Errorf("oracle: verify bracket: claimed outcome %d does not connect to prior hash: %w", index, constants.ErrConformanceLedgerGap)
		}
		expected = outcome.Commitment.Hash
	}
	if after.Commitment.Prior != expected {
		return fmt.Errorf("oracle: verify bracket: ledger holds an unclaimed execution before the closing sentinel: %w", constants.ErrConformanceUnclaimedExecution)
	}
	return nil
}

// OrderLedger returns claimed executions — given in any order, for example
// claim order across concurrent roles that share one runtime — in ledger
// order, walking from the before sentinel's hash, ready for VerifyBracket. A
// claimed link the walk never reaches is a ledger gap, unless the links left
// over run onto the after sentinel: then the ledger holds an execution no claim
// names, and the returned walk lets VerifyBracket report it as unclaimed.
// before and after must carry commitment links; VerifyBracket rejects them
// otherwise, so a sentinel without one yields claimed unchanged.
func OrderLedger(before, after Outcome, claimed []Outcome) ([]Outcome, error) {
	if before.Commitment == nil || after.Commitment == nil {
		return claimed, nil
	}
	for index, outcome := range claimed {
		if outcome.Commitment == nil {
			return nil, fmt.Errorf("oracle: order ledger: claimed outcome %d has no ledger link: %w", index, constants.ErrConformanceLedgerGap)
		}
	}
	used := make([]bool, len(claimed))
	ordered := make([]Outcome, 0, len(claimed))
	current := before.Commitment.Hash
	for {
		next := -1
		for i := range claimed {
			if !used[i] && claimed[i].Commitment.Prior == current {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		used[next] = true
		ordered = append(ordered, claimed[next])
		current = claimed[next].Commitment.Hash
	}
	var leftover []Outcome
	for i := range claimed {
		if !used[i] {
			leftover = append(leftover, claimed[i])
		}
	}
	if closesOn(after.Commitment.Prior, leftover) {
		return ordered, nil
	}
	return nil, fmt.Errorf("oracle: order ledger: claimed ledger links do not connect to the before sentinel: %w", constants.ErrConformanceLedgerGap)
}

// closesOn reports whether links form one contiguous run that ends at hash,
// so that walking back from hash through the links reaches every one of them.
func closesOn(hash string, links []Outcome) bool {
	used := make([]bool, len(links))
	remaining := len(links)
	for remaining > 0 {
		next := -1
		for i := range links {
			if !used[i] && links[i].Commitment.Hash == hash {
				next = i
				break
			}
		}
		if next < 0 {
			return false
		}
		used[next] = true
		remaining--
		hash = links[next].Commitment.Prior
	}
	return true
}
