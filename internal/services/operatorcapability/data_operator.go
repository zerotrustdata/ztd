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
}

// IsDataOperator reports whether an active remote or configured embedded
// session includes Data, even when other capabilities are enabled.
func IsDataOperator(op *operatorv1.OperatorDocument) bool {
	return HasActiveCapability(op, constants.OperatorCapabilityData)
}

// IsDedicatedDataOperator reports whether op is an active Data session other
// than the Gateway's embedded Operator. Selection prefers dedicated Data
// Operators and falls back to the embedded one only when none is active.
func IsDedicatedDataOperator(op *operatorv1.OperatorDocument) bool {
	return IsDataOperator(op) && constants.OperatorType(op.OperatorType) != constants.OperatorTypeEmbedded
}

// PreferDedicatedDataOperators drops the Gateway's embedded Operator when a
// dedicated Data session is also present.
func PreferDedicatedDataOperators(operators []*operatorv1.OperatorDocument) []*operatorv1.OperatorDocument {
	dedicated := make([]*operatorv1.OperatorDocument, 0, len(operators))
	for _, op := range operators {
		if IsDedicatedDataOperator(op) {
			dedicated = append(dedicated, op)
		}
	}
	if len(dedicated) == 0 {
		return operators
	}
	return dedicated
}

// ActiveDataOperators returns the active data-operator sessions in preference
// order: dedicated Data Operators (host-native Operators started with a data
// role), else the Gateway's embedded Operator.
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
		}
		if constants.OperatorType(op.OperatorType) == constants.OperatorTypeEmbedded {
			embeddedMatches = append(embeddedMatches, status)
		} else {
			dedicatedMatches = append(dedicatedMatches, status)
		}
	}
	switch {
	case len(dedicatedMatches) > 0:
		return dedicatedMatches
	default:
		return embeddedMatches
	}
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
