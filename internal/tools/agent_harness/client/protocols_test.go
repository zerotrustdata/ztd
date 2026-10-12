// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"encoding/json"
	"testing"
)

type promptArgFixture struct {
	Arg string `json:"arg"`
}

func (promptArgFixture) isPromptArgs() {}

type skillPayloadFixture struct {
	Input string `json:"input,omitempty"`
}

func (skillPayloadFixture) isSkillPayload() {}

func TestJSONRPCRequest(t *testing.T) {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "test/method",
		Params:  map[string]any{"key": "value"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded JSONRPCRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.JSONRPC != "2.0" {
		t.Error("JSONRPC mismatch")
	}
	if decoded.ID != 1 {
		t.Error("ID mismatch")
	}
	if decoded.Method != "test/method" {
		t.Error("Method mismatch")
	}
}

func TestJSONRPCResponse(t *testing.T) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      1,
		Result:  json.RawMessage(`{"status":"ok"}`),
		Error:   nil,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded JSONRPCResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.JSONRPC != "2.0" {
		t.Error("JSONRPC mismatch")
	}
	if decoded.ID != 1 {
		t.Error("ID mismatch")
	}
	if decoded.Error != nil {
		t.Error("Error should be nil")
	}
}

func TestJSONRPCError(t *testing.T) {
	err := JSONRPCError{
		Code:    -32600,
		Message: "Invalid Request",
		Data:    json.RawMessage(`{"details":"more info"}`),
	}

	data, errData := json.Marshal(err)
	if errData != nil {
		t.Fatalf("failed to marshal: %v", errData)
	}

	var decoded JSONRPCError
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Code != -32600 {
		t.Error("Code mismatch")
	}
	if decoded.Message != "Invalid Request" {
		t.Error("Message mismatch")
	}
}
