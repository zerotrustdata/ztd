// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcapability

import (
	"testing"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestValidateServedModelTag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tag  string
		ok   bool
	}{
		{name: "namespaced tag", tag: "registry.example/team/qwen3:0.6b", ok: true},
		{name: "empty tag", tag: "", ok: false},
		{name: "option-like tag", tag: "--all", ok: false},
		{name: "shell metacharacter", tag: "qwen3:0.6b;whoami", ok: false},
		{name: "whitespace", tag: "qwen3 0.6b", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateServedModelTag(tt.tag)
			if tt.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, constants.ErrInferenceModelTagInvalid)
		})
	}
}

func TestOllamaMaintenanceCommands_BuildExpectedOperatorPaths(t *testing.T) {
	t.Parallel()
	pull, err := OllamaPullCommand("qwen3:0.6b")
	require.NoError(t, err)
	assert.Equal(t, "ztd operator model pull qwen3:0.6b", pull)

	copyCmd, err := OllamaCopyCommand("qwen3:0.6b", "qwen3.8:27b")
	require.NoError(t, err)
	assert.Equal(t, "ztd operator model copy qwen3:0.6b qwen3.8:27b", copyCmd)
}

func TestValidateWitnessCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  *operatorv1.OperatorRuntimeConfig
		want bool
	}{
		{name: "ordinary operator is allowed", cfg: &operatorv1.OperatorRuntimeConfig{}, want: true},
		{name: "provider observer is rejected", cfg: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}}, want: false},
		{name: "provenance operator is rejected", cfg: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityProvenance)}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capabilities := ResolveOperatorCapabilities(tt.cfg)
			commandErr := ValidateWitnessCommand(capabilities, "ollama ps")
			nativeToolErr := ValidateWitnessNativeTool(capabilities, constants.MCPToolRunShellCommand)
			for _, ledgerRead := range []string{constants.MCPToolAuditEventReceiptList, constants.MCPToolAuditEventReceiptGet} {
				require.NoError(t, ValidateWitnessNativeTool(capabilities, ledgerRead), ledgerRead)
			}
			if tt.want {
				require.NoError(t, commandErr)
				require.NoError(t, nativeToolErr)
				return
			}
			require.ErrorIs(t, commandErr, constants.ErrWitnessCommandNotCapable)
			require.ErrorIs(t, nativeToolErr, constants.ErrWitnessCommandNotCapable)
		})
	}
}
