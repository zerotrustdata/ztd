// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type harnessOllamaModelCommandDispatcher struct {
	client  *harnessclient.Client
	persona harnessclient.Persona
}

func newHarnessOllamaModelCommandDispatcher(cfg *config.Config, authContext *auth.ClientAuthContext, deps gatewayEvalDeps) (*harnessOllamaModelCommandDispatcher, error) {
	client, err := deps.clientFactory(nativeEvalClientConfig(cfg, authContext))
	if err != nil {
		return nil, err
	}
	// Deliberately no OperatorID/OperatorSessionID here: those headers only
	// assert a persisted CLI-operator bind (established out of band via CLI
	// bind, not part of this flow) and the Gateway's CLI auth middleware
	// fails closed with 403 when a session with no such bind sends them. The
	// actual dispatch target travels in the request body's
	// target_operator_session_id, which DispatchService validates on its
	// own regardless of these headers.
	return &harnessOllamaModelCommandDispatcher{
		client: client,
		persona: harnessclient.Persona{
			ID:           "ztd-campaign-model-maintenance",
			UserAgent:    "ztd-eval-campaign",
			UserID:       authContext.UserID,
			CLISessionID: authContext.CLISessionID,
		},
	}, nil
}

func (d *harnessOllamaModelCommandDispatcher) DispatchOllamaModelCommand(ctx context.Context, request evaluation.OllamaModelCommandDispatchRequest) (*evaluation.OllamaModelCommandDispatchResult, error) {
	if d == nil || d.client == nil || request.TargetOperatorSessionID == "" || request.ExecutionID == "" {
		return nil, fmt.Errorf("evaluation: Ollama model command dispatch: %w", constants.ErrMissingRequiredField)
	}
	actionType := request.ActionType
	if actionType == "" {
		actionType = constants.ActionTypeExecuteBash
	}
	payload := request.Payload
	if len(payload) == 0 && actionType == constants.ActionTypeExecuteBash {
		marshaled, err := evaluation.MarshalOllamaModelCommandPayload(request)
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
	result := &evaluation.OllamaModelCommandDispatchResult{
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
	default:
		commandResult := &operatorv1.CommandResult{}
		if err := proto.Unmarshal(resultPayload, commandResult); err != nil {
			return nil, fmt.Errorf("evaluation: Ollama model command dispatch: decode command result: %w", err)
		}
		result.CommandResult = commandResult
	}
	return result, nil
}

// modelCommandEnvironment names the provider endpoint for the `ztd operator
// model` maintenance subcommands, which read ZTD_OLLAMA_ENDPOINT and fail
// closed without it.
func modelCommandEnvironment(endpoint string) map[string]string {
	return map[string]string{string(constants.EnvVar.OllamaEndpoint): endpoint}
}
