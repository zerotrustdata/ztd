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

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// The canonical OperatorDocument is the protobuf message
// *operatorv1.OperatorDocument. These helpers are its only JSON codec: the
// document store, HTTP responses and clients all use the protojson form with
// proto field names, never encoding/json over the generated struct.

var (
	operatorDocumentMarshal = protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}
	// Stored documents are merged field-by-field by the document store and may
	// carry keys owned by other writers, so unknown keys are tolerated on read.
	operatorDocumentUnmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}
)

// MarshalOperatorDocument encodes an OperatorDocument in canonical JSON.
func MarshalOperatorDocument(doc *operatorv1.OperatorDocument) ([]byte, error) {
	return operatorDocumentMarshal.Marshal(doc)
}

// UnmarshalOperatorDocument decodes canonical JSON into an OperatorDocument and
// validates the capability set it carries.
func UnmarshalOperatorDocument(data []byte) (*operatorv1.OperatorDocument, error) {
	doc := &operatorv1.OperatorDocument{}
	if err := operatorDocumentUnmarshal.Unmarshal(data, doc); err != nil {
		return nil, fmt.Errorf("decode operator document: %w", err)
	}
	if err := OperatorCapabilitiesFromProto(doc.GetOperatorCapabilities()).Validate(); err != nil {
		return nil, err
	}
	return doc, nil
}

// OperatorDocumentFromStore decodes a document-store row into an OperatorDocument.
func OperatorDocumentFromStore(doc *Document) (*operatorv1.OperatorDocument, error) {
	raw, err := json.Marshal(doc.ForWire())
	if err != nil {
		return nil, fmt.Errorf("encode stored operator document: %w", err)
	}
	return UnmarshalOperatorDocument(raw)
}

// MarshalOperatorRuntimeConfig encodes an OperatorRuntimeConfig in canonical JSON.
func MarshalOperatorRuntimeConfig(cfg *operatorv1.OperatorRuntimeConfig) ([]byte, error) {
	return operatorDocumentMarshal.Marshal(cfg)
}

// UnmarshalOperatorRuntimeConfig decodes canonical JSON into an
// OperatorRuntimeConfig and validates its capabilities.
func UnmarshalOperatorRuntimeConfig(data []byte) (*operatorv1.OperatorRuntimeConfig, error) {
	cfg := &operatorv1.OperatorRuntimeConfig{}
	if err := operatorDocumentUnmarshal.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("decode operator runtime config: %w", err)
	}
	if err := OperatorCapabilitiesFromProto(cfg.GetCapabilities()).Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// OperatorCapabilitiesFromProto converts the proto capability strings to the typed set.
func OperatorCapabilitiesFromProto(capabilities []string) constants.OperatorCapabilities {
	out := make(constants.OperatorCapabilities, 0, len(capabilities))
	for _, capability := range capabilities {
		out = append(out, constants.OperatorCapability(capability))
	}
	return out
}

// OperatorCapabilitiesToProto converts a typed capability set to the proto capability strings.
func OperatorCapabilitiesToProto(capabilities constants.OperatorCapabilities) []string {
	out := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		out = append(out, string(capability))
	}
	return out
}

func marshalOperatorDocuments(docs []*operatorv1.OperatorDocument) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(docs))
	for _, doc := range docs {
		raw, err := MarshalOperatorDocument(doc)
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

func unmarshalOperatorDocuments(raws []json.RawMessage) ([]*operatorv1.OperatorDocument, error) {
	out := make([]*operatorv1.OperatorDocument, 0, len(raws))
	for _, raw := range raws {
		doc, err := UnmarshalOperatorDocument(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	return out, nil
}

// MarshalJSON encodes the embedded documents with the canonical codec.
func (r OperatorSlotResponse) MarshalJSON() ([]byte, error) {
	docs, err := marshalOperatorDocuments(r.Operators)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Success   bool              `json:"success"`
		Operators []json.RawMessage `json:"operators"`
	}{r.Success, docs})
}

func (r *OperatorSlotResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		Success   bool              `json:"success"`
		Operators []json.RawMessage `json:"operators"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	docs, err := unmarshalOperatorDocuments(wire.Operators)
	if err != nil {
		return err
	}
	r.Success, r.Operators = wire.Success, docs
	return nil
}

// MarshalJSON encodes the embedded document with the canonical codec.
func (r OperatorResponse) MarshalJSON() ([]byte, error) {
	wire := struct {
		Success  bool            `json:"success"`
		Operator json.RawMessage `json:"operator,omitempty"`
	}{Success: r.Success}
	if r.Operator != nil {
		raw, err := MarshalOperatorDocument(r.Operator)
		if err != nil {
			return nil, err
		}
		wire.Operator = raw
	}
	return json.Marshal(wire)
}

func (r *OperatorResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		Success  bool            `json:"success"`
		Operator json.RawMessage `json:"operator,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	r.Success, r.Operator = wire.Success, nil
	if len(wire.Operator) > 0 && string(wire.Operator) != "null" {
		doc, err := UnmarshalOperatorDocument(wire.Operator)
		if err != nil {
			return err
		}
		r.Operator = doc
	}
	return nil
}
