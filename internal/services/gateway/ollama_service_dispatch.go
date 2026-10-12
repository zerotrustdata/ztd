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

func validateWitnessCommandDispatch(op *operatorv1.OperatorDocument, actionType string, payload []byte) error {
	if op == nil || actionType != string(constants.ActionTypeExecuteBash) {
		return nil
	}
	decoded, err := governance.DecodePayloadForAction(constants.ActionTypeExecuteBash, payload)
	if err != nil {
		return fmt.Errorf("dispatch: %w", constants.ErrTxPayloadDecodeFailed)
	}
	cmdReq, ok := decoded.(*operatorv1.CommandRequested)
	if !ok || cmdReq == nil {
		return nil
	}
	return operatorcapability.ValidateWitnessCommand(op.RuntimeConfig, cmdReq.GetCommand())
}
