// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcapability

import (
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

// HasActiveCapability reports whether op is an active Operator session whose
// resolved capabilities include capability.
func HasActiveCapability(op *operatorv1.OperatorDocument, capability constants.OperatorCapability) bool {
	return IsActiveOperatorSession(op) && GetOperatorCapabilities(op).Has(capability)
}
