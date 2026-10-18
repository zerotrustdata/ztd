// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

// OperatorAuditReceiptListResult is the allowlisted response for a local
// Operator receipt query. It carries only query authority/scope metadata and
// canonical signed receipts; unsigned text projections stay local.
type OperatorAuditReceiptListResult struct {
	Receipts        []*operatorv1.ActionReceipt `json:"receipts"`
	Count           int                        `json:"count"`
	OperatorID      string                     `json:"operator_id"`
	Authority       string                     `json:"authority"`
	InvestigationID string                     `json:"investigation_id"`
}
