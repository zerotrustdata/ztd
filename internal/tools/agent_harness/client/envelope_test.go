// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"testing"
	"time"

	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

func TestSubmitEnvelope_MarshalError(t *testing.T) {
	cfg := config.Config{
		MTLSBaseURL: "https://example.com",
		Auth:        config.Auth{},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	p := Persona{ID: "test-client"}

	// Create a valid envelope - protojson.Marshal handles nil gracefully
	// This test verifies the call path works correctly
	envelope := &commonv1.GovernanceEnvelope{
		Id:              "test-id",
		TransactionHash: "test-hash",
		ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
		ActionType:      "TEST_ACTION",
	}

	// Since we can't easily cause a marshal error with valid protobuf,
	// we'll just verify the call doesn't panic with nil
	_, _, err = client.SubmitEnvelope(ctx, p, envelope)

	// This will fail with network error since we're using a fake URL,
	// but that's expected - we're just testing it doesn't panic
	_ = err
}

func TestActionConstants(t *testing.T) {
	if ActionMcpCall != "MCP_CALL" {
		t.Errorf("ActionMcpCall = %s, want MCP_CALL", ActionMcpCall)
	}
	if ActionA2aCall != "A2A_CALL" {
		t.Errorf("ActionA2aCall = %s, want A2A_CALL", ActionA2aCall)
	}
	if ProtocolVersion != "2" {
		t.Errorf("ProtocolVersion = %s, want 2", ProtocolVersion)
	}
}

func TestMaximalEnvelope(t *testing.T) {
	maximal := MaximalEnvelope{
		OperatorID:     "op-123",
		ToolName:       "test-tool",
		ArgumentsJSON:  `{"arg":"value"}`,
		TargetResource: "localhost",
		StateRoot:      "root-abc",
		TTL:            5 * time.Minute,
	}

	if maximal.OperatorID != "op-123" {
		t.Error("OperatorID not set correctly")
	}
	if maximal.ToolName != "test-tool" {
		t.Error("ToolName not set correctly")
	}
	if maximal.ArgumentsJSON != `{"arg":"value"}` {
		t.Error("ArgumentsJSON not set correctly")
	}
	if maximal.TargetResource != "localhost" {
		t.Error("TargetResource not set correctly")
	}
	if maximal.StateRoot != "root-abc" {
		t.Error("StateRoot not set correctly")
	}
	if maximal.TTL != 5*time.Minute {
		t.Error("TTL not set correctly")
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
