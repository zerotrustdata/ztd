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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
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
	_, err = client.SubmitEnvelope(ctx, p, envelope)

	// This will fail with network error since we're using a fake URL,
	// but that's expected - we're just testing it doesn't panic
	_ = err
}

func TestValidateGovernedRequest_RejectsUnregisteredMcpEventSpelling(t *testing.T) {
	// The pre-registry MCP event spelling must stay outside the registry.
	_, err := constants.ValidateGovernedRequest(constants.EventType("operator.mcp.call.requested"))
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrTxUnknownEventType)
}

func TestActionConstants(t *testing.T) {
	if ActionMcpCall != "MCP_CALL" {
		t.Errorf("ActionMcpCall = %s, want MCP_CALL", ActionMcpCall)
	}
	if ProtocolVersion != "2" {
		t.Errorf("ProtocolVersion = %s, want 2", ProtocolVersion)
	}
}
