// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
)

// deadlineRecorder is a RoundTripper that records the request context deadline
// the http.Client derived from its Timeout.
type deadlineRecorder struct {
	deadline time.Time
	has      bool
}

func (r *deadlineRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.deadline, r.has = req.Context().Deadline()
	return &http.Response{
		StatusCode: http.StatusInternalServerError,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func TestDispatchCommandBody_ClientTimeoutCoversApprovalAndDispatchDeadline(t *testing.T) {
	recorder := &deadlineRecorder{}
	client, err := New(config.Config{MTLSBaseURL: "http://dispatch.test"})
	require.NoError(t, err)
	client.http.Transport = recorder

	start := time.Now()
	_, err = client.DispatchCommandBody(context.Background(), Persona{ID: "test", CLISessionID: "cli-1"}, []byte(`{}`))
	require.NoError(t, err)

	require.True(t, recorder.has, "the dispatch client must bound the request")
	want := mcp.ApprovalRequestTTL + dispatch.RequestDeadline
	assert.GreaterOrEqual(t, recorder.deadline.Sub(start), want-time.Second, "the timeout must outlast the approval wait plus the dispatch deadline")
	assert.Less(t, recorder.deadline.Sub(start), want+time.Minute)
}

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
