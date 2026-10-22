// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package oracle

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// VerifyEventReceipt checks that event receipt was signed and persisted by runtimeID's
// pinned actuator key, and that its deterministic stage chain is valid.
func VerifyEventReceipt(anchors *AnchorSet, runtimeID string, eventReceipt *operatorv1.EventReceipt) (*governance.DeterministicProtocolChain, error) {
	key, err := anchors.Key(eventReceipt.GetSignerKeyId(), runtimeID)
	if err != nil {
		return nil, fmt.Errorf("oracle: verify event receipt signer: %w", err)
	}
	if err := governance.VerifyEventReceipt(eventReceipt, key); err != nil {
		return nil, fmt.Errorf("oracle: verify event receipt signature and persistence: %w", err)
	}
	chain, err := governance.ValidateDeterministicProtocolChain(eventReceipt)
	if err != nil {
		return nil, fmt.Errorf("oracle: verify event receipt stage chain: %w", err)
	}
	if err := verifyStageIdentity(chain.Stages, runtimeID); err != nil {
		return nil, fmt.Errorf("oracle: verify event receipt stage identity: %w", err)
	}
	return chain, nil
}

// verifyStageIdentity requires every stage to name one non-empty operator, and
// for an outbound Operator that operator must be the runtime the key is pinned to.
func verifyStageIdentity(stages []*operatorv1.DeterministicStageEvidence, runtimeID string) error {
	operatorID := ""
	for _, stage := range stages {
		id := stage.GetOperatorId()
		if id == "" {
			return fmt.Errorf("stage %s names no operator: %w", stage.GetStageId(), constants.ErrConformanceEventReceiptUnclassifiable)
		}
		if operatorID == "" {
			operatorID = id
		} else if id != operatorID {
			return fmt.Errorf("stage %s names operator %s, want %s: %w", stage.GetStageId(), id, operatorID, constants.ErrConformanceEventReceiptUnclassifiable)
		}
	}
	if runtimeID != GatewayRuntimeID && operatorID != runtimeID {
		return fmt.Errorf("stages name operator %s, pinned runtime is %s: %w", operatorID, runtimeID, constants.ErrConformanceEventReceiptUnclassifiable)
	}
	return nil
}
