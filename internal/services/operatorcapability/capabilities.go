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
// metadata is used only without runtime configuration. Deployment type does
// not add capabilities.
func GetOperatorCapabilities(op *operatorv1.OperatorDocument) constants.OperatorCapabilities {
	var capabilities constants.OperatorCapabilities
	if op.RuntimeConfig != nil {
		capabilities = ResolveOperatorCapabilities(op.RuntimeConfig)
	} else if len(op.OperatorCapabilities) > 0 {
		capabilities = models.OperatorCapabilitiesFromProto(op.GetOperatorCapabilities()).Canonical()
	} else {
		capabilities = constants.OperatorCapabilities{constants.OperatorCapabilityData}
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
// Operators, except reads of the executing runtime's own signed local-first events store,
// which every Operator serves for its own evidence.
func ValidateWitnessNativeTool(capabilities constants.OperatorCapabilities, toolName string) error {
	if runsGenericWork(capabilities) {
		return nil
	}
	switch toolName {
	case constants.MCPToolEventReceiptList, constants.MCPToolEventReceiptGet:
		return nil
	}
	return fmt.Errorf("%w: native tool %q", constants.ErrWitnessCommandNotCapable, toolName)
}

// HasActiveCapability reports whether op is an active Operator session whose
// resolved capabilities include capability.
func HasActiveCapability(op *operatorv1.OperatorDocument, capability constants.OperatorCapability) bool {
	return IsActiveOperatorSession(op) && GetOperatorCapabilities(op).Has(capability)
}

// ActiveOperatorsWithCapability returns configured active sessions that carry
// capability, independently of deployment type.
func ActiveOperatorsWithCapability(ops []*operatorv1.OperatorDocument, capability constants.OperatorCapability) []*operatorv1.OperatorDocument {
	var matches []*operatorv1.OperatorDocument
	for _, op := range ops {
		if op != nil && op.GetRuntimeConfig() != nil && HasActiveCapability(op, capability) {
			matches = append(matches, op)
		}
	}
	return matches
}

// PreferEmbedded gives the Gateway's embedded Operator precedence among
// otherwise eligible Operators. Without an embedded match, ambiguity remains
// visible to the caller.
func PreferEmbedded(ops []*operatorv1.OperatorDocument) []*operatorv1.OperatorDocument {
	var embedded []*operatorv1.OperatorDocument
	for _, op := range ops {
		if constants.OperatorType(op.GetOperatorType()) == constants.OperatorTypeEmbedded {
			embedded = append(embedded, op)
		}
	}
	if len(embedded) > 0 {
		return embedded
	}
	return ops
}
