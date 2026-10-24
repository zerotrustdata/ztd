// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
	"time"
)

// SuspendedTransaction represents a transaction awaiting L3 human approval.
type SuspendedTransaction struct {
	TransactionHash         string
	Envelope                json.RawMessage
	CreatedAt               time.Time
	ExpiresAt               time.Time
	ToolName                string
	ToolArguments           json.RawMessage
	UserID                  string
	OperatorID              string
	OperatorSessionID       string // Operator session the envelope targets (envelope.OperatorSessionId); owns the approval audit chain
	SubmitterCLISessionID   string // CLI session that submitted the transaction
	ExpectedCertFingerprint string // Expected mTLS cert fingerprint for verification
	// ClaimedAt is set while an approved transaction is being executed. A claimed row is never
	// recorded as expired: its execution was attempted.
	ClaimedAt *time.Time
}
