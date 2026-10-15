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
