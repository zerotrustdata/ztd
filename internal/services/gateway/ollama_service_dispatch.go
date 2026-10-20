// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// validateWitnessDispatch refuses generic work (EXECUTE_BASH, native MCP tools)
// aimed at a witness-only Operator before an envelope is built. The executing
// Operator enforces the same rule at its own boundary.
func validateWitnessDispatch(op *operatorv1.OperatorDocument, actionType string, payload []byte) error {
	if op == nil {
		return nil
	}
	action := constants.ActionType(actionType)
	if action != constants.ActionTypeExecuteBash && action != constants.ActionTypeMcpCall {
		return nil
	}
	decoded, err := governance.DecodePayloadForAction(action, payload)
	if err != nil {
		return fmt.Errorf("dispatch: %w", constants.ErrTxPayloadDecodeFailed)
	}
	capabilities := operatorcapability.GetOperatorCapabilities(op)
	switch request := decoded.(type) {
	case *operatorv1.CommandRequested:
		return operatorcapability.ValidateWitnessCommand(capabilities, request.GetCommand())
	case *operatorv1.McpCallRequested:
		return operatorcapability.ValidateWitnessNativeTool(capabilities, request.GetToolName())
	}
	return nil
}
