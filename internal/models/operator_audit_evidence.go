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
	operatorAuditEventReceiptMarshal   = protojson.MarshalOptions{UseProtoNames: true}
	operatorAuditEventReceiptUnmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}
)

// OperatorAuditEventReceiptListResult is the allowlisted response for a local
// Operator event receipt query. It carries only query authority/scope metadata and
// canonical signed event receipts; unsigned text projections stay local.
type OperatorAuditEventReceiptListResult struct {
	EventReceipts   []*operatorv1.EventReceipt `json:"event_receipts"`
	Count           int                        `json:"count"`
	OperatorID      string                     `json:"operator_id"`
	Authority       string                     `json:"authority"`
	InvestigationID string                     `json:"investigation_id"`
}

// MarshalJSON encodes the result with canonical protojson for each nested
// EventReceipt, preserving proto field names and string enum representation.
func (r OperatorAuditEventReceiptListResult) MarshalJSON() ([]byte, error) {
	type Wire struct {
		EventReceipts   []json.RawMessage `json:"event_receipts"`
		Count           int               `json:"count"`
		OperatorID      string            `json:"operator_id"`
		Authority       string            `json:"authority"`
		InvestigationID string            `json:"investigation_id"`
	}
	var eventReceiptWires []json.RawMessage
	if r.EventReceipts != nil {
		eventReceiptWires = make([]json.RawMessage, 0, len(r.EventReceipts))
		for _, eventReceipt := range r.EventReceipts {
			if eventReceipt == nil {
				eventReceiptWires = append(eventReceiptWires, json.RawMessage("null"))
				continue
			}
			raw, err := operatorAuditEventReceiptMarshal.Marshal(eventReceipt)
			if err != nil {
				return nil, fmt.Errorf("operator audit event receipt list: marshal action event_receipt: %w", err)
			}
			eventReceiptWires = append(eventReceiptWires, raw)
		}
	}
	return json.Marshal(Wire{
		EventReceipts:   eventReceiptWires,
		Count:           r.Count,
		OperatorID:      r.OperatorID,
		Authority:       r.Authority,
		InvestigationID: r.InvestigationID,
	})
}

// UnmarshalJSON decodes the result, using protojson for each nested EventReceipt.
func (r *OperatorAuditEventReceiptListResult) UnmarshalJSON(data []byte) error {
	type Wire struct {
		EventReceipts   []json.RawMessage `json:"event_receipts"`
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
	if wire.EventReceipts == nil {
		r.EventReceipts = nil
		return nil
	}
	r.EventReceipts = make([]*operatorv1.EventReceipt, 0, len(wire.EventReceipts))
	for _, raw := range wire.EventReceipts {
		if len(raw) == 0 || string(raw) == "null" {
			r.EventReceipts = append(r.EventReceipts, nil)
			continue
		}
		eventReceipt := &operatorv1.EventReceipt{}
		if err := operatorAuditEventReceiptUnmarshal.Unmarshal(raw, eventReceipt); err != nil {
			return fmt.Errorf("operator audit event receipt list: unmarshal action event_receipt: %w", err)
		}
		r.EventReceipts = append(r.EventReceipts, eventReceipt)
	}
	return nil
}
