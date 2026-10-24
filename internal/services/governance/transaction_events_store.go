// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"encoding/json"
)

//go:generate mockery --name TransactionEventsStore --output ./mocks --dir .

// TransactionEventsStore defines the interface for persisting audit documents
// to a document store. Implemented natively by DocumentStoreService (gateway
// mode, persists JSON documents) and storage.LocalFirstEventsStore (outbound mode,
// decodes the payload as an EventReceiptRecord and records it in the receipts
// table). No adapter is required in either production mode.
type TransactionEventsStore interface {
	DocSet(ctx context.Context, collection, id string, data json.RawMessage) error
	DocDelete(ctx context.Context, collection, id string) error
}
