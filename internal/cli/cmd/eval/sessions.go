// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"fmt"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
)

// operatorSessions is the pair of operator sessions a dispatching command
// targets: the inference Operator and the stack's data-operator. Fields for
// capabilities a command did not request are empty.
type operatorSessions struct {
	InferenceSessionID string
	DataSessionID      string
	DataOperatorID     string
}

// resolveOperatorSessions asks the Gateway for active operator sessions and
// resolves each requested capability. Only commands that dispatch work call it.
func resolveOperatorSessions(cmd *cobra.Command, deps nativeEvalDeps, capabilities ...constants.OperatorCapability) (operatorSessions, error) {
	gatewayDeps := deps.gatewayDeps()
	cfg, _, authContext, err := gatewayEvalEnvironment(cmd, gatewayDeps)
	if err != nil {
		return operatorSessions{}, err
	}
	operators, err := evalListOperators(cmd, gatewayDeps, cfg, authContext)
	if err != nil {
		return operatorSessions{}, err
	}
	return resolveOperatorSessionsFrom(operators, capabilities...)
}

func resolveOperatorSessionsFrom(operators []*operatorv1.OperatorDocument, capabilities ...constants.OperatorCapability) (operatorSessions, error) {
	var sessions operatorSessions
	for _, capability := range capabilities {
		switch capability {
		case constants.OperatorCapabilityInference:
			inference, err := evaluation.SelectInferenceOperator(operators, "")
			if err != nil {
				return operatorSessions{}, err
			}
			sessions.InferenceSessionID = inference.OperatorSessionID
		case constants.OperatorCapabilityData:
			data, err := operatorcapability.SelectDataOperator(operators)
			if err != nil {
				return operatorSessions{}, err
			}
			sessions.DataSessionID = data.OperatorSessionID
			sessions.DataOperatorID = data.OperatorID
		default:
			return operatorSessions{}, fmt.Errorf("evaluation: %w: %q", constants.ErrOperatorCapabilityInvalid, capability)
		}
	}
	return sessions, nil
}
