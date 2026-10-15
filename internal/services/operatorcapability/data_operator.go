// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcapability

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// DataOperatorStatus summarizes the active data-operator session discovered
// through the operator registry.
type DataOperatorStatus struct {
	OperatorID        string
	OperatorSessionID string
	Status            string
	WorkingDirectory  string
	// Embedded is true for the Gateway's embedded Operator, whose working
	// directory is <runtime>/operators/embedded-operator.
	Embedded bool
}

// IsDataOperator reports whether an active remote or configured embedded
// session includes Data, even when other capabilities are enabled.
func IsDataOperator(op *operatorv1.OperatorDocument) bool {
	return HasActiveCapability(op, constants.OperatorCapabilityData)
}

// IsDedicatedDataOperator reports whether op is an active Data session other
// than the Gateway's embedded Operator.
func IsDedicatedDataOperator(op *operatorv1.OperatorDocument) bool {
	return IsDataOperator(op) && constants.OperatorType(op.OperatorType) != constants.OperatorTypeEmbedded
}

// isEmbeddedDataOperator reports whether op is the Gateway's active embedded
// Operator, which is always a Data operator.
func isEmbeddedDataOperator(op *operatorv1.OperatorDocument) bool {
	return IsDataOperator(op) && constants.OperatorType(op.OperatorType) == constants.OperatorTypeEmbedded
}

// PreferEmbeddedDataOperator narrows operators to the Gateway's embedded
// Operator when it is an active Data session; it is the default Data operator.
// Otherwise operators is returned unchanged.
func PreferEmbeddedDataOperator(operators []*operatorv1.OperatorDocument) []*operatorv1.OperatorDocument {
	for _, op := range operators {
		if isEmbeddedDataOperator(op) {
			return []*operatorv1.OperatorDocument{op}
		}
	}
	return operators
}

// ActiveDataOperators returns the active data-operator sessions in preference
// order: the Gateway's embedded Operator, the default Data operator, else the
// dedicated Data Operators (host-native Operators started with a data role).
func ActiveDataOperators(operators []*operatorv1.OperatorDocument) []DataOperatorStatus {
	var dedicatedMatches []DataOperatorStatus
	var embeddedMatches []DataOperatorStatus
	for _, op := range operators {
		if !IsDataOperator(op) {
			continue
		}
		wd := extractWorkingDirectory(op.GetLatestHeartbeatSnapshot())
		if wd == "" && constants.OperatorType(op.OperatorType) == constants.OperatorTypeEmbedded {
			// The embedded Operator never heartbeats; the Gateway registers
			// its working directory in the runtime config instead.
			wd = op.GetRuntimeConfig().GetLocalDir()
		}
		status := DataOperatorStatus{
			OperatorID:        op.Id,
			OperatorSessionID: op.OperatorSessionId,
			Status:            op.Status,
			WorkingDirectory:  wd,
			Embedded:          constants.OperatorType(op.OperatorType) == constants.OperatorTypeEmbedded,
		}
		if status.Embedded {
			embeddedMatches = append(embeddedMatches, status)
		} else {
			dedicatedMatches = append(dedicatedMatches, status)
		}
	}
	if len(embeddedMatches) > 0 {
		return embeddedMatches
	}
	return dedicatedMatches
}

// extractWorkingDirectory reads the pwd from the operator's heartbeat.
// It tries EnvironmentDetails.pwd first, then falls back to SystemIdentity.pwd.
// Returns empty string if the heartbeat cannot be unmarshalled or pwd is absent.
func extractWorkingDirectory(hr *operatorv1.HeartbeatResult) string {
	// Try EnvironmentDetails first
	if env := hr.GetEnvironment(); env != nil && env.GetPwd() != "" {
		return env.GetPwd()
	}
	// Fall back to SystemIdentity
	if si := hr.GetSystemIdentity(); si != nil && si.GetPwd() != "" {
		return si.GetPwd()
	}
	return ""
}

// SelectDataOperator resolves exactly one data-operator. Zero sessions return
// ErrDataOperatorNotFound and several return ErrDataOperatorAmbiguous.
func SelectDataOperator(operators []*operatorv1.OperatorDocument) (*DataOperatorStatus, error) {
	matches := ActiveDataOperators(operators)
	switch len(matches) {
	case 0:
		return nil, constants.ErrDataOperatorNotFound
	case 1:
		selected := matches[0]
		return &selected, nil
	default:
		return nil, fmt.Errorf("%w: %d sessions", constants.ErrDataOperatorAmbiguous, len(matches))
	}
}
