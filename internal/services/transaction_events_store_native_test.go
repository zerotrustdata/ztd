// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"testing"

	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/storage"
)

// TestLocalFirstEventsStore_SatisfiesTransactionEventsStoreNatively asserts that
// *storage.LocalFirstEventsStore satisfies governance.TransactionEventsStore without
// an adapter. Outbound mode wires LocalFirstEventsStore directly into GovernanceDeps;
// the eventsStoreTransactionStore adapter existed only because LocalFirstEventsStore
// lacked a DocSet method. This compile-time assertion fails pre-fix.
func TestLocalFirstEventsStore_SatisfiesTransactionEventsStoreNatively(t *testing.T) {
	t.Parallel()
	var _ governance.TransactionEventsStore = (*storage.LocalFirstEventsStore)(nil)
}

// TestDocumentStoreService_SatisfiesTransactionEventsStoreNatively asserts
// that *gateway.DocumentStoreService satisfies governance.TransactionEventsStore
// natively (gateway mode wires it directly). This already passes; it is
// co-located with the LocalFirstEventsStore assertion to document the contract that
// both mode-specific local-first events stores satisfy the interface without adapters.
func TestDocumentStoreService_SatisfiesTransactionEventsStoreNatively(t *testing.T) {
	t.Parallel()
	var _ governance.TransactionEventsStore = (*gateway.DocumentStoreService)(nil)
}
