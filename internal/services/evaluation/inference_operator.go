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
	"strings"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
)

// InferenceOperatorStatus summarizes one active inference-capable Operator
// session discovered through the owner-authenticated operator registry.
type InferenceOperatorStatus struct {
	OperatorID        string
	OperatorSessionID string
	Status            string
	InferenceEnabled  bool
	OllamaEndpoint    string
	Embedded          bool
}

// ActiveInferenceOperators returns every active remote or configured embedded
// Operator whose runtime capabilities include Inference.
func ActiveInferenceOperators(operators []*operatorv1.OperatorDocument) []InferenceOperatorStatus {
	matches := make([]InferenceOperatorStatus, 0)
	for _, op := range operators {
		if op.RuntimeConfig == nil || !operatorcapability.HasActiveCapability(op, constants.OperatorCapabilityInference) {
			continue
		}
		matches = append(matches, InferenceOperatorStatus{
			OperatorID:        op.GetId(),
			OperatorSessionID: op.GetOperatorSessionId(),
			Status:            op.GetStatus(),
			InferenceEnabled:  true,
			OllamaEndpoint:    inferenceOperatorOllamaEndpoint(op),
			Embedded:          op.GetOperatorType() == string(constants.OperatorTypeEmbedded),
		})
	}
	return matches
}

// SelectInferenceOperator resolves exactly one inference-capable Operator.
// When sessionID is non-empty it must match an active inference Operator.
// When sessionID is empty, dedicated remote inference Operators take precedence
// over the Gateway's embedded Operator fallback; exactly one dedicated
// inference Operator must exist, or with no dedicated Operators active, the
// embedded Operator is selected.
func SelectInferenceOperator(operators []*operatorv1.OperatorDocument, sessionID string) (*InferenceOperatorStatus, error) {
	return SelectInferenceOperatorForHardware(operators, sessionID, "")
}

// SelectInferenceOperatorForHardware resolves an inference operator using the
// exact OperatorDocument system fingerprint when one is available.
func SelectInferenceOperatorForHardware(
	operators []*operatorv1.OperatorDocument,
	sessionID string,
	systemFingerprint string,
) (*InferenceOperatorStatus, error) {
	matches := ActiveInferenceOperators(operators)
	if systemFingerprint != "" {
		filtered := make([]InferenceOperatorStatus, 0, len(matches))
		for _, op := range operators {
			if op.SystemFingerprint != systemFingerprint {
				continue
			}
			for _, match := range matches {
				if match.OperatorSessionID == op.GetOperatorSessionId() {
					filtered = append(filtered, match)
					break
				}
			}
		}
		matches = filtered
	}
	if sessionID != "" {
		for _, match := range matches {
			if match.OperatorSessionID == sessionID {
				selected := match
				return &selected, nil
			}
		}
		return nil, fmt.Errorf("%w: session %s", constants.ErrInferenceOperatorNotCapable, sessionID)
	}

	var dedicatedMatches []InferenceOperatorStatus
	var embeddedMatches []InferenceOperatorStatus
	for _, match := range matches {
		if match.Embedded {
			embeddedMatches = append(embeddedMatches, match)
		} else {
			dedicatedMatches = append(dedicatedMatches, match)
		}
	}

	if len(dedicatedMatches) == 1 {
		selected := dedicatedMatches[0]
		return &selected, nil
	}
	if len(dedicatedMatches) > 1 {
		candidates := make([]string, 0, len(dedicatedMatches))
		for _, match := range dedicatedMatches {
			candidates = append(candidates, fmt.Sprintf("%s (session %s)", match.OperatorID, match.OperatorSessionID))
		}
		return nil, fmt.Errorf("%w: %s", constants.ErrInferenceOperatorAmbiguous, strings.Join(candidates, ", "))
	}

	// Fall back to the Gateway's embedded Operator when no dedicated inference
	// operators are active.
	switch len(embeddedMatches) {
	case 0:
		return nil, constants.ErrInferenceOperatorNotFound
	case 1:
		selected := embeddedMatches[0]
		return &selected, nil
	default:
		candidates := make([]string, 0, len(embeddedMatches))
		for _, match := range embeddedMatches {
			candidates = append(candidates, fmt.Sprintf("%s (session %s)", match.OperatorID, match.OperatorSessionID))
		}
		return nil, fmt.Errorf("%w: %s", constants.ErrInferenceOperatorAmbiguous, strings.Join(candidates, ", "))
	}
}

func inferenceOperatorOllamaEndpoint(op *operatorv1.OperatorDocument) string {
	if op.RuntimeConfig == nil {
		return ""
	}
	return strings.TrimSpace(op.RuntimeConfig.InferenceOllamaEndpoint)
}

// GovernedInferenceOllamaEndpoint returns the approved provider endpoint from
// the exact Inference Operator runtime_config. Campaign-host environment
// overrides are intentionally rejected.
func GovernedInferenceOllamaEndpoint(operators []*operatorv1.OperatorDocument, inferenceSessionID string) (string, error) {
	if inferenceSessionID == "" {
		return "", fmt.Errorf("evaluation: governed inference endpoint: %w", constants.ErrMissingRequiredField)
	}
	selected, err := SelectInferenceOperator(operators, inferenceSessionID)
	if err != nil {
		return "", fmt.Errorf("evaluation: governed inference endpoint: %w", err)
	}
	endpoint := strings.TrimSpace(selected.OllamaEndpoint)
	if endpoint == "" {
		return "", fmt.Errorf("evaluation: governed inference endpoint: %w", constants.ErrInferenceEndpointInvalid)
	}
	return endpoint, nil
}

// GovernedInferenceDispatcher submits governed inference through the exact
// Inference Operator session.
type GovernedInferenceDispatcher interface {
	DispatchInference(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error)
}

// AgentInferenceDispatcher is the agent executor's Gateway inference surface:
// governed inference, and Tribunal intents whose member calls are governed
// inferences under the same app identity (POST /api/v1/operators/intents).
type AgentInferenceDispatcher interface {
	GovernedInferenceDispatcher
	DispatchIntent(ctx context.Context, req *tribunalv1.TribunalIntentRequest) (*tribunalv1.TribunalIntentResult, error)
}

func campaignRoleToInferenceRole(role evalv1.ModelCampaignRole) (models.InferenceModelRole, error) {
	switch role {
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY:
		return models.InferenceModelRolePrimary, nil
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT:
		return models.InferenceModelRoleAssistant, nil
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE:
		return models.InferenceModelRoleLite, nil
	default:
		return models.InferenceModelRoleUnspecified, fmt.Errorf("evaluation: inference role %s: %w", role, constants.ErrInferenceRoleInvalid)
	}
}

// operatorLoadStateToEvaluation maps the operator-layer model load
// classification onto the evaluation domain's equivalent enum.
func operatorLoadStateToEvaluation(state operatorv1.InferenceLoadState) evalv1.EvaluationLoadState {
	switch state {
	case operatorv1.InferenceLoadState_INFERENCE_LOAD_STATE_COLD:
		return evalv1.EvaluationLoadState_EVALUATION_LOAD_STATE_COLD
	case operatorv1.InferenceLoadState_INFERENCE_LOAD_STATE_WARM:
		return evalv1.EvaluationLoadState_EVALUATION_LOAD_STATE_WARM
	case operatorv1.InferenceLoadState_INFERENCE_LOAD_STATE_UNAVAILABLE:
		return evalv1.EvaluationLoadState_EVALUATION_LOAD_STATE_UNAVAILABLE
	default:
		return evalv1.EvaluationLoadState_EVALUATION_LOAD_STATE_UNSPECIFIED
	}
}

func inferenceResultText(result *operatorv1.InferenceResult) string {
	var builder strings.Builder
	for _, part := range result.GetParts() {
		if part.GetText() != "" {
			builder.WriteString(part.GetText())
		}
	}
	return builder.String()
}
