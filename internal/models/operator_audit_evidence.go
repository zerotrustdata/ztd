// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

var (
	operatorAuditReceiptMarshal   = protojson.MarshalOptions{UseProtoNames: true}
	operatorAuditReceiptUnmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}
)

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

// MarshalJSON encodes the result with canonical protojson for each nested
// ActionReceipt, preserving proto field names and string enum representation.
func (r OperatorAuditReceiptListResult) MarshalJSON() ([]byte, error) {
	type Wire struct {
		Receipts        []json.RawMessage `json:"receipts"`
		Count           int               `json:"count"`
		OperatorID      string            `json:"operator_id"`
		Authority       string            `json:"authority"`
		InvestigationID string            `json:"investigation_id"`
	}
	var receiptWires []json.RawMessage
	if r.Receipts != nil {
		receiptWires = make([]json.RawMessage, 0, len(r.Receipts))
		for _, receipt := range r.Receipts {
			if receipt == nil {
				receiptWires = append(receiptWires, json.RawMessage("null"))
				continue
			}
			raw, err := operatorAuditReceiptMarshal.Marshal(receipt)
			if err != nil {
				return nil, fmt.Errorf("operator audit receipt list: marshal action receipt: %w", err)
			}
			receiptWires = append(receiptWires, raw)
		}
	}
	return json.Marshal(Wire{
		Receipts:        receiptWires,
		Count:           r.Count,
		OperatorID:      r.OperatorID,
		Authority:       r.Authority,
		InvestigationID: r.InvestigationID,
	})
}

// UnmarshalJSON decodes the result, using protojson for each nested ActionReceipt.
func (r *OperatorAuditReceiptListResult) UnmarshalJSON(data []byte) error {
	type Wire struct {
		Receipts        []json.RawMessage `json:"receipts"`
		Count           int               `json:"count"`
		OperatorID      string            `json:"operator_id"`
		Authority       string            `json:"authority"`
		InvestigationID string            `json:"investigation_id"`
	}
	var wire Wire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.Count = wire.Count
	r.OperatorID = wire.OperatorID
	r.Authority = wire.Authority
	r.InvestigationID = wire.InvestigationID
	if wire.Receipts == nil {
		r.Receipts = nil
		return nil
	}
	r.Receipts = make([]*operatorv1.ActionReceipt, 0, len(wire.Receipts))
	for _, raw := range wire.Receipts {
		if len(raw) == 0 || string(raw) == "null" {
			r.Receipts = append(r.Receipts, nil)
			continue
		}
		receipt := &operatorv1.ActionReceipt{}
		if err := operatorAuditReceiptUnmarshal.Unmarshal(raw, receipt); err != nil {
			return fmt.Errorf("operator audit receipt list: unmarshal action receipt: %w", err)
		}
		r.Receipts = append(r.Receipts, receipt)
	}
	return nil
}
