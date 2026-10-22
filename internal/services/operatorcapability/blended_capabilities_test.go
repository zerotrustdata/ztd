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

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestBlendedOperatorKeepsEveryCapability(t *testing.T) {
	cfg := &operatorv1.OperatorRuntimeConfig{Capabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityData, constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver})}
	op := &operatorv1.OperatorDocument{Id: "blend", OperatorSessionId: "session", OperatorType: string(constants.OperatorTypeRemote), Status: string(constants.OperatorStatusActive), RuntimeConfig: cfg}
	require.True(t, IsDataOperator(op))
	require.NoError(t, ValidateWitnessCommand(ResolveOperatorCapabilities(cfg), "pwd"))
}

func TestEveryOperatorCapabilityCombinationPreservesMembership(t *testing.T) {
	all := []constants.OperatorCapability{constants.OperatorCapabilityData, constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver}
	for _, operatorType := range []constants.OperatorType{constants.OperatorTypeEmbedded, constants.OperatorTypeRemote} {
		for mask := 1; mask < 1<<len(all); mask++ {
			capabilities := constants.OperatorCapabilities{}
			for i, capability := range all {
				if mask&(1<<i) != 0 {
					capabilities = append(capabilities, capability)
				}
			}
			t.Run(string(operatorType)+"/"+capabilities.String(), func(t *testing.T) {
				cfg := &operatorv1.OperatorRuntimeConfig{Capabilities: models.OperatorCapabilitiesToProto(capabilities)}
				op := &operatorv1.OperatorDocument{Id: "blend", OperatorSessionId: "session", OperatorType: string(operatorType), Status: string(constants.OperatorStatusActive), RuntimeConfig: cfg}
				effective := capabilities.Canonical()
				if operatorType == constants.OperatorTypeEmbedded {
					effective = capabilities.WithEmbeddedBaseline()
				}
				require.Equal(t, effective, GetOperatorCapabilities(op))
				require.Equal(t, effective.Has(constants.OperatorCapabilityData), IsDataOperator(op))
				commandErr := ValidateWitnessCommand(ResolveOperatorCapabilities(cfg), "pwd")
				nativeToolErr := ValidateWitnessNativeTool(ResolveOperatorCapabilities(cfg), constants.MCPToolRunShellCommand)
				if capabilities.Has(constants.OperatorCapabilityData) || capabilities.Has(constants.OperatorCapabilityInference) {
					require.NoError(t, commandErr)
					require.NoError(t, nativeToolErr)
				} else {
					require.ErrorIs(t, commandErr, constants.ErrWitnessCommandNotCapable)
					require.ErrorIs(t, nativeToolErr, constants.ErrWitnessCommandNotCapable)
				}
				require.NoError(t, ValidateWitnessNativeTool(ResolveOperatorCapabilities(cfg), constants.MCPToolAuditEventReceiptList))
			})
		}
	}
}
