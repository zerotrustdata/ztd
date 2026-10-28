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
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
)

// InferenceOperatorStatus summarizes one active inference-capable Operator
// session discovered through the owner-authenticated operator registry.
type InferenceOperatorStatus struct {
	OperatorID        string
	OperatorSessionID string
	Status            string
	InferenceEnabled  bool
	// Provider is the provider class the Operator serves
	// (constants.InferenceProviderOllama or constants.InferenceProviderLlamaCpp)
	// and ProviderEndpoint its approved endpoint.
	Provider         string
	ProviderEndpoint string
	Embedded         bool
}

// ActiveInferenceOperators returns every active remote or configured embedded
// Operator whose runtime capabilities include Inference.
func ActiveInferenceOperators(operators []*operatorv1.OperatorDocument) []InferenceOperatorStatus {
	docs := activeInferenceOperatorDocs(operators, "")
	matches := make([]InferenceOperatorStatus, 0, len(docs))
	for _, op := range docs {
		matches = append(matches, inferenceOperatorStatus(op))
	}
	return matches
}

// activeInferenceOperatorDocs filters operators to active inference-capable
// sessions, and to systemFingerprint's host when it is non-empty.
func activeInferenceOperatorDocs(operators []*operatorv1.OperatorDocument, systemFingerprint string) []*operatorv1.OperatorDocument {
	docs := make([]*operatorv1.OperatorDocument, 0, len(operators))
	for _, op := range operators {
		if op.RuntimeConfig == nil || !operatorcapability.HasActiveCapability(op, constants.OperatorCapabilityInference) {
			continue
		}
		if systemFingerprint != "" && op.SystemFingerprint != systemFingerprint {
			continue
		}
		docs = append(docs, op)
	}
	return docs
}

func inferenceOperatorStatus(op *operatorv1.OperatorDocument) InferenceOperatorStatus {
	provider, endpoint := operatorcapability.InferenceProvider(op.GetRuntimeConfig())
	return InferenceOperatorStatus{
		OperatorID:        op.GetId(),
		OperatorSessionID: op.GetOperatorSessionId(),
		Status:            op.GetStatus(),
		InferenceEnabled:  true,
		Provider:          provider,
		ProviderEndpoint:  endpoint,
		Embedded:          op.GetOperatorType() == string(constants.OperatorTypeEmbedded),
	}
}

// SelectInferenceOperator resolves an active inference-capable Operator, giving
// embedded precedence. An explicit session must belong to an eligible Operator.
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
	var eligible []*operatorv1.OperatorDocument
	for _, op := range operators {
		if sessionID != "" || !op.GetRuntimeConfig().GetInferenceDecisionOnly() {
			eligible = append(eligible, op)
		}
	}
	matches := operatorcapability.PreferEmbedded(activeInferenceOperatorDocs(eligible, systemFingerprint))
	if sessionID != "" {
		for _, op := range matches {
			if op.GetOperatorSessionId() == sessionID {
				selected := inferenceOperatorStatus(op)
				return &selected, nil
			}
		}
		return nil, fmt.Errorf("%w: session %s", constants.ErrInferenceOperatorNotCapable, sessionID)
	}

	matches = operatorcapability.PreferEmbedded(matches)
	switch len(matches) {
	case 0:
		return nil, constants.ErrInferenceOperatorNotFound
	case 1:
		selected := inferenceOperatorStatus(matches[0])
		return &selected, nil
	default:
		candidates := make([]string, 0, len(matches))
		for _, op := range matches {
			candidates = append(candidates, fmt.Sprintf("%s (session %s)", op.GetId(), op.GetOperatorSessionId()))
		}
		return nil, fmt.Errorf("%w: %s", constants.ErrInferenceOperatorAmbiguous, strings.Join(candidates, ", "))
	}
}

// GovernedInferenceOllamaEndpoint returns the approved Ollama endpoint from
// the exact Inference Operator runtime_config, for the `ztd operator model`
// pull commands that only Ollama serves. Campaign-host environment overrides
// are intentionally rejected, and an Operator serving another provider fails
// closed with constants.ErrInferenceProviderOllamaRequired.
func GovernedInferenceOllamaEndpoint(operators []*operatorv1.OperatorDocument, inferenceSessionID string) (string, error) {
	if inferenceSessionID == "" {
		return "", fmt.Errorf("evaluation: governed inference endpoint: %w", constants.ErrMissingRequiredField)
	}
	selected, err := SelectInferenceOperator(operators, inferenceSessionID)
	if err != nil {
		return "", fmt.Errorf("evaluation: governed inference endpoint: %w", err)
	}
	if selected.Provider != constants.InferenceProviderOllama {
		return "", fmt.Errorf("evaluation: governed inference endpoint: session %s serves %s: %w", inferenceSessionID, selected.Provider, constants.ErrInferenceProviderOllamaRequired)
	}
	endpoint := selected.ProviderEndpoint
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
