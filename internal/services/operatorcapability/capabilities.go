// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcapability

import (
	"fmt"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// ResolveOperatorCapabilities returns every enabled capability. Operators
// without an explicit capability default to Data.
func ResolveOperatorCapabilities(cfg *operatorv1.OperatorRuntimeConfig) constants.OperatorCapabilities {
	if cfg == nil {
		return constants.OperatorCapabilities{constants.OperatorCapabilityData}
	}
	capabilities := models.OperatorCapabilitiesFromProto(cfg.GetCapabilities())
	if len(capabilities) == 0 {
		capabilities = append(capabilities, constants.OperatorCapabilityData)
	}
	return capabilities.Canonical()
}

// GetOperatorCapabilities resolves runtime capabilities; stored capability
// metadata is used only without runtime configuration. The embedded Operator
// type always carries Data.
func GetOperatorCapabilities(op *operatorv1.OperatorDocument) constants.OperatorCapabilities {
	var capabilities constants.OperatorCapabilities
	if op.RuntimeConfig != nil {
		capabilities = ResolveOperatorCapabilities(op.RuntimeConfig)
	} else if len(op.OperatorCapabilities) > 0 {
		capabilities = models.OperatorCapabilitiesFromProto(op.GetOperatorCapabilities()).Canonical()
	} else {
		capabilities = constants.OperatorCapabilities{constants.OperatorCapabilityData}
	}
	if constants.OperatorType(op.OperatorType) == constants.OperatorTypeEmbedded {
		capabilities = capabilities.WithEmbeddedBaseline()
	}
	return capabilities
}

// IsActiveOperatorSession reports whether op is a live, session-bound Operator
// that may serve governed work: an active remote Operator, or an active
// embedded Operator that has reported its runtime configuration.
func IsActiveOperatorSession(op *operatorv1.OperatorDocument) bool {
	if constants.OperatorStatus(op.Status) != constants.OperatorStatusActive || op.OperatorSessionId == "" {
		return false
	}
	switch constants.OperatorType(op.OperatorType) {
	case constants.OperatorTypeRemote:
		return true
	case constants.OperatorTypeEmbedded:
		return op.RuntimeConfig != nil
	default:
		return false
	}
}

// runsGenericWork reports whether capabilities admit generic host work:
// commands and native tools. Witness-only Operators (provenance, observer)
// keep only their narrowly scoped observation handlers.
func runsGenericWork(capabilities constants.OperatorCapabilities) bool {
	return capabilities.Has(constants.OperatorCapabilityData) || capabilities.Has(constants.OperatorCapabilityInference)
}

// ValidateWitnessNativeTool rejects governed native MCP tools on witness-only
// Operators, except reads of the executing runtime's own signed audit store,
// which every Operator serves for its own evidence.
func ValidateWitnessNativeTool(capabilities constants.OperatorCapabilities, toolName string) error {
	if runsGenericWork(capabilities) {
		return nil
	}
	switch toolName {
	case constants.MCPToolAuditReceiptList, constants.MCPToolAuditReceiptGet:
		return nil
	}
	return fmt.Errorf("%w: native tool %q", constants.ErrWitnessCommandNotCapable, toolName)
}

// HasActiveCapability reports whether op is an active Operator session whose
// resolved capabilities include capability.
func HasActiveCapability(op *operatorv1.OperatorDocument, capability constants.OperatorCapability) bool {
	return IsActiveOperatorSession(op) && GetOperatorCapabilities(op).Has(capability)
}
