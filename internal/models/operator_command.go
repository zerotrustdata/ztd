// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// OperatorCommandRequest is the typed JSON request for POST /api/v1/operators/commands.
type OperatorCommandRequest struct {
	EvaluationRunID         string `json:"evaluation_run_id,omitempty"`
	EvaluationAssignmentID  string `json:"evaluation_assignment_id,omitempty"`
	TargetOperatorSessionID string `json:"target_operator_session_id"`
	EventType               string `json:"event_type"`
	Payload                 []byte `json:"payload"`
	TargetResource          string `json:"target_resource,omitempty"`
	CaseID                  string `json:"case_id,omitempty"`
	InvestigationID         string `json:"investigation_id,omitempty"`
	TaskID                  string `json:"task_id,omitempty"`
	WebSessionID            string `json:"web_session_id,omitempty"`
	CLISessionID            string `json:"cli_session_id,omitempty"`
}

// Validate returns an error if the request is missing required fields.
func (r *OperatorCommandRequest) Validate() error {
	if r.TargetOperatorSessionID == "" {
		return constants.ErrGatewayOperatorSessionIDRequired
	}
	if r.EventType == "" {
		return constants.ErrTxUnknownEventType
	}
	if len(r.Payload) == 0 {
		return fmt.Errorf("%w: %w", constants.ErrMissingRequiredField, constants.ErrTxPayloadMissing)
	}
	if _, err := constants.ValidateGovernedRequest(constants.EventType(r.EventType)); err != nil {
		return err
	}
	return nil
}
