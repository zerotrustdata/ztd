// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// GovernedOllamaModelCommandDispatcher sends provider model-maintenance
// commands as persona through the Gateway's governed command dispatch, and
// decodes each typed result its action returns.
type GovernedOllamaModelCommandDispatcher struct {
	client  laneClient
	persona client.Persona
}

// NewGovernedOllamaModelCommandDispatcher returns a dispatcher that sends
// model-maintenance commands as persona through laneClient.
func NewGovernedOllamaModelCommandDispatcher(laneClient laneClient, persona client.Persona) (*GovernedOllamaModelCommandDispatcher, error) {
	if laneClient == nil {
		return nil, fmt.Errorf("evaluation: Ollama model command dispatcher: %w", constants.ErrMissingRequiredField)
	}
	return &GovernedOllamaModelCommandDispatcher{client: laneClient, persona: persona}, nil
}

// DispatchOllamaModelCommand sends one maintenance command to the request's
// exact Operator session and decodes the result payload its action type owns.
func (d *GovernedOllamaModelCommandDispatcher) DispatchOllamaModelCommand(ctx context.Context, request OllamaModelCommandDispatchRequest) (*OllamaModelCommandDispatchResult, error) {
	if request.TargetOperatorSessionID == "" || request.ExecutionID == "" {
		return nil, fmt.Errorf("evaluation: Ollama model command dispatch: %w", constants.ErrMissingRequiredField)
	}
	actionType := request.ActionType
	if actionType == "" {
		actionType = constants.ActionTypeExecuteBash
	}
	payload := request.Payload
	if len(payload) == 0 && actionType == constants.ActionTypeExecuteBash {
		marshaled, err := MarshalOllamaModelCommandPayload(request)
		if err != nil {
			return nil, err
		}
		payload = marshaled
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("evaluation: Ollama model command dispatch: %w", constants.ErrMissingRequiredField)
	}
	eventType, err := constants.RequestEventForAction(actionType)
	if err != nil {
		return nil, fmt.Errorf("evaluation: Ollama model command dispatch: %w", err)
	}
	outcome, err := d.client.DispatchCommand(ctx, d.persona, models.OperatorCommandRequest{
		TargetOperatorSessionID: request.TargetOperatorSessionID,
		EventType:               string(eventType),
		Payload:                 payload,
		TargetResource:          "ollama-model",
		CaseID:                  request.CaseID,
		InvestigationID:         request.InvestigationID,
		TaskID:                  request.TaskID,
		CLISessionID:            d.persona.CLISessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("evaluation: Ollama model command dispatch: %w", err)
	}
	result := &OllamaModelCommandDispatchResult{
		Status:       int(outcome.Observation.GetHttpStatus()),
		Success:      outcome.Observation.GetKind() == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		ActionType:   actionType,
		ResponseBody: outcome.Body,
	}
	resultPayload := outcome.Dispatch.GetResultPayload()
	if len(resultPayload) == 0 {
		return result, nil
	}
	switch actionType {
	case constants.ActionTypeOllamaModelInventory:
		inventoryResult := &operatorv1.OllamaModelInventoryResult{}
		if err := proto.Unmarshal(resultPayload, inventoryResult); err != nil {
			return nil, fmt.Errorf("evaluation: Ollama model command dispatch: decode inventory result: %w", err)
		}
		result.InventoryResult = inventoryResult
	case constants.ActionTypeOllamaModelResidency:
		residencyResult := &operatorv1.OllamaModelResidencyResult{}
		if err := proto.Unmarshal(resultPayload, residencyResult); err != nil {
			return nil, fmt.Errorf("evaluation: Ollama model command dispatch: decode residency result: %w", err)
		}
		result.ResidencyResult = residencyResult
	case constants.ActionTypeOllamaModelRelease:
		releaseResult := &operatorv1.OllamaModelReleaseResult{}
		if err := proto.Unmarshal(resultPayload, releaseResult); err != nil {
			return nil, fmt.Errorf("evaluation: Ollama model command dispatch: decode release result: %w", err)
		}
		result.ReleaseResult = releaseResult
	default:
		commandResult := &operatorv1.CommandResult{}
		if err := proto.Unmarshal(resultPayload, commandResult); err != nil {
			return nil, fmt.Errorf("evaluation: Ollama model command dispatch: decode command result: %w", err)
		}
		result.CommandResult = commandResult
	}
	return result, nil
}
