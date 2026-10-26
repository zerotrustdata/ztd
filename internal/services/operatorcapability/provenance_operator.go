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
)

// ProvenanceOperatorStatus summarizes one active remote provenance operator
// session discovered through the operator registry.
type ProvenanceOperatorStatus struct {
	OperatorID         string
	OperatorSessionID  string
	UserID             string
	Status             string
	ProvenanceEnabled  bool
	ModelStorageRoot   string
	ModelStorageFormat string
	Platform           string
}

// ActiveProvenanceOperators returns every active operator whose
// resolved capabilities include Provenance.
func ActiveProvenanceOperators(operators []*operatorv1.OperatorDocument) []ProvenanceOperatorStatus {
	matches := make([]ProvenanceOperatorStatus, 0)
	for _, op := range ActiveOperatorsWithCapability(operators, constants.OperatorCapabilityProvenance) {
		matches = append(matches, ProvenanceOperatorStatus{
			OperatorID:         op.Id,
			OperatorSessionID:  op.OperatorSessionId,
			UserID:             op.GetUserId(),
			Status:             op.Status,
			ProvenanceEnabled:  true,
			ModelStorageRoot:   op.RuntimeConfig.ProvenanceOperatorModelStorageRoot,
			ModelStorageFormat: op.RuntimeConfig.ProvenanceOperatorModelStorageFormat,
			Platform:           op.RuntimeConfig.Platform,
		})
	}
	return matches
}

// SelectProvenanceOperator resolves exactly one provenance operator. When
// sessionID is non-empty it must match an active provenance operator;
// otherwise exactly one active provenance operator must exist.
func SelectProvenanceOperator(operators []*operatorv1.OperatorDocument, sessionID string) (*ProvenanceOperatorStatus, error) {
	matches := ActiveProvenanceOperators(PreferEmbedded(ActiveOperatorsWithCapability(operators, constants.OperatorCapabilityProvenance)))
	if sessionID != "" {
		for _, match := range matches {
			if match.OperatorSessionID == sessionID {
				selected := match
				return &selected, nil
			}
		}
		return nil, fmt.Errorf("%w: session %s", constants.ErrProvenanceOperatorNotCapable, sessionID)
	}
	switch len(matches) {
	case 0:
		return nil, constants.ErrProvenanceOperatorNotFound
	case 1:
		selected := matches[0]
		return &selected, nil
	default:
		return nil, constants.ErrProvenanceOperatorAmbiguous
	}
}
