// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import "encoding/json"

// ApprovalCompletedEvent is the typed SSE event payload for L3 transaction
// approval. It is emitted by the gateway when a user completes the WebAuthn
// approval ceremony and is consumed by CLI clients (stdio proxy and approve
// command) to detect approval completion without polling.
type ApprovalCompletedEvent struct {
	Type   string `json:"type"`
	UserID string `json:"user_id"`
	TxHash string `json:"tx_hash"`
	// Receipt is the canonical protojson EventReceipt of the resumed transaction,
	// signed by the executing Operator.
	Receipt json.RawMessage `json:"receipt"`
}

// HasReceipt reports whether the completion event carries a receipt. A JSON null
// receipt is absent: an approval.completed event without its signed receipt
// never proves the resumed transaction ran.
func (e ApprovalCompletedEvent) HasReceipt() bool {
	return len(e.Receipt) > 0 && string(e.Receipt) != "null"
}
