// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

func TestValidateWitnessDispatch(t *testing.T) {
	t.Parallel()

	command, err := proto.Marshal(&operatorv1.CommandRequested{Command: "ollama stop qwen3:0.6b", ExecutionId: "exec-1"})
	require.NoError(t, err)
	shellTool, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: constants.MCPToolRunShellCommand, ArgumentsJson: `{"command":"pwd"}`, ExecutionId: "exec-2"})
	require.NoError(t, err)
	ledgerRead, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: constants.MCPToolAuditReceiptGet, ArgumentsJson: `{"transaction_id":"tx-1"}`, ExecutionId: "exec-3"})
	require.NoError(t, err)

	operatorWith := func(capabilities ...constants.OperatorCapability) *operatorv1.OperatorDocument {
		raw := make([]string, 0, len(capabilities))
		for _, capability := range capabilities {
			raw = append(raw, string(capability))
		}
		return &operatorv1.OperatorDocument{
			OperatorType:  string(constants.OperatorTypeRemote),
			RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: raw},
		}
	}

	tests := []struct {
		name    string
		op      *operatorv1.OperatorDocument
		generic bool
	}{
		{name: "data", op: operatorWith(constants.OperatorCapabilityData), generic: true},
		{name: "inference", op: operatorWith(constants.OperatorCapabilityInference), generic: true},
		{name: "observer", op: operatorWith(constants.OperatorCapabilityObserver)},
		{name: "provenance", op: operatorWith(constants.OperatorCapabilityProvenance)},
		{name: "provenance with data", op: operatorWith(constants.OperatorCapabilityProvenance, constants.OperatorCapabilityData), generic: true},
		{name: "stored observer metadata without runtime config", op: &operatorv1.OperatorDocument{
			OperatorType:         string(constants.OperatorTypeRemote),
			OperatorCapabilities: []string{string(constants.OperatorCapabilityObserver)},
		}},
		{name: "embedded observer carries the embedded baseline", op: &operatorv1.OperatorDocument{
			OperatorType:  string(constants.OperatorTypeEmbedded),
			RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
		}, generic: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for _, generic := range []struct {
				action  constants.ActionType
				payload []byte
			}{
				{action: constants.ActionTypeExecuteBash, payload: command},
				{action: constants.ActionTypeMcpCall, payload: shellTool},
			} {
				err := validateWitnessDispatch(tt.op, string(generic.action), generic.payload)
				if tt.generic {
					require.NoError(t, err, "%s", generic.action)
				} else {
					require.ErrorIs(t, err, constants.ErrWitnessCommandNotCapable, "%s", generic.action)
				}
			}
			require.NoError(t, validateWitnessDispatch(tt.op, string(constants.ActionTypeMcpCall), ledgerRead),
				"every Operator serves reads of its own signed audit store")
		})
	}

	require.ErrorIs(t, validateWitnessDispatch(operatorWith(constants.OperatorCapabilityObserver), string(constants.ActionTypeMcpCall), []byte{0xff}),
		constants.ErrTxPayloadDecodeFailed)
}
