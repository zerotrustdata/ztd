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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type stubGovernedCapabilityProbeDispatcher struct {
	bySuffix map[string]func(InferenceProbeRequest) (*operatorv1.InferenceDispatchResponse, error)
}

func (s *stubGovernedCapabilityProbeDispatcher) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	for suffix, handler := range s.bySuffix {
		if req.GetProviderAttemptId() == capabilityProbeAttemptPrefix+"-"+suffix {
			probeReq := InferenceProbeRequest{
				ProviderAttemptID:       req.GetProviderAttemptId(),
				Model:                   req.GetModel(),
				TargetOperatorSessionID: req.GetTargetOperatorSessionId(),
			}
			return handler(probeReq)
		}
	}
	return nil, fmt.Errorf("unexpected attempt %s", req.GetProviderAttemptId())
}

func TestGovernedCapabilityProbeRunner_RecordsDescriptiveOutcomesWithoutExclusion(t *testing.T) {
	t.Parallel()
	variant := &evalv1.ModelVariant{
		VariantId:      "probe-model",
		ProviderClass:  "ollama",
		ServedModelTag: "probe-model:latest",
		ModelDigest:    repeatHex('d'),
		ContextLimit:   4096,
	}
	dispatcher := &stubGovernedCapabilityProbeDispatcher{bySuffix: map[string]func(InferenceProbeRequest) (*operatorv1.InferenceDispatchResponse, error){
		"completion": func(req InferenceProbeRequest) (*operatorv1.InferenceDispatchResponse, error) { //nolint:unparam // The dispatcher contract returns an error; this fixture covers the successful branch.
			return probeResponse(req.ProviderAttemptID, "capability-probe-ok"), nil
		},
		"tools": func(InferenceProbeRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return nil, fmt.Errorf("tools: %w", constants.ErrInferenceToolsUnsupported)
		},
		"structured": func(req InferenceProbeRequest) (*operatorv1.InferenceDispatchResponse, error) { //nolint:unparam // The dispatcher contract returns an error; this fixture covers the successful branch.
			return probeResponse(req.ProviderAttemptID, "not-json"), nil
		},
		"thinking": func(InferenceProbeRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return nil, fmt.Errorf("thinking: %w", constants.ErrInferenceCapabilityUnsupported)
		},
	}}
	runner := NewGovernedCapabilityProbeRunner(dispatcher, "infer-session", func(prefix string) string { return prefix })
	observations, err := runner.RunCapabilityProbes(context.Background(), variant)
	require.NoError(t, err)
	require.Len(t, observations, len(RequiredModelCapabilityKinds()))
	outcomes := map[evalv1.ModelCapabilityKind]evalv1.EvaluationVerdictStatus{}
	for _, observation := range observations {
		outcomes[observation.GetCapability()] = observation.GetOutcome()
		assert.NotEmpty(t, observation.GetObservationDetail())
	}
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, outcomes[evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_COMPLETION])
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSUPPORTED, outcomes[evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_TOOL_CALLING])
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, outcomes[evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_STRUCTURED_OUTPUT])
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSUPPORTED, outcomes[evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_THINKING])
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, outcomes[evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_CONTEXT_LIMIT])
}

type capturingProbeDispatcher struct {
	requests []*operatorv1.InferenceDispatchRequest
	err      error
}

func (d *capturingProbeDispatcher) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	d.requests = append(d.requests, req)
	return nil, d.err
}

func TestGovernedCapabilityProbeRunner_ToolProbeUsesAutoToolChoice(t *testing.T) {
	t.Parallel()
	variant := &evalv1.ModelVariant{VariantId: "probe-model", ServedModelTag: "probe-model:latest", ModelDigest: repeatHex('d')}
	dispatcher := &capturingProbeDispatcher{err: constants.ErrInferenceBackendTimeout}
	runner := NewGovernedCapabilityProbeRunner(dispatcher, "infer-session", func(prefix string) string { return prefix }).(*governedCapabilityProbeRunner)
	_, err := runner.probeToolCallingCapability(context.Background(), variant)
	require.NoError(t, err)
	require.Len(t, dispatcher.requests, 1)
	assert.Equal(t, operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO, dispatcher.requests[0].GetToolChoice().GetMode())
}

func TestGovernedCapabilityProbeRunner_DispatchFailureIsNotUnsupported(t *testing.T) {
	t.Parallel()
	variant := &evalv1.ModelVariant{VariantId: "probe-model", ServedModelTag: "probe-model:latest", ModelDigest: repeatHex('d')}
	runner := NewGovernedCapabilityProbeRunner(&capturingProbeDispatcher{err: fmt.Errorf("probe: %w", constants.ErrInferenceBackendTimeout)}, "infer-session", func(prefix string) string { return prefix }).(*governedCapabilityProbeRunner)
	structured, err := runner.probeStructuredOutputCapability(context.Background(), variant)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, structured.GetOutcome())
	assert.Equal(t, "structured-output probe dispatch failed (backend_timeout)", structured.GetObservationDetail())
	thinking, err := runner.probeThinkingCapability(context.Background(), variant)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, thinking.GetOutcome())
	assert.Equal(t, "thinking probe dispatch failed (backend_timeout)", thinking.GetObservationDetail())
}

func TestGovernedCapabilityProbeRunner_ToolProbeDispatchFailureIsNotUnsupported(t *testing.T) {
	t.Parallel()
	variant := &evalv1.ModelVariant{
		VariantId:      "probe-model",
		ProviderClass:  "ollama",
		ServedModelTag: "probe-model:latest",
		ModelDigest:    repeatHex('d'),
	}
	dispatcher := &stubGovernedCapabilityProbeDispatcher{bySuffix: map[string]func(InferenceProbeRequest) (*operatorv1.InferenceDispatchResponse, error){
		"tools": func(InferenceProbeRequest) (*operatorv1.InferenceDispatchResponse, error) {
			return nil, fmt.Errorf("tools: %w", constants.ErrInferenceBackendTimeout)
		},
	}}
	runner := NewGovernedCapabilityProbeRunner(dispatcher, "infer-session", func(prefix string) string { return prefix }).(*governedCapabilityProbeRunner)
	observation, err := runner.probeToolCallingCapability(context.Background(), variant)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, observation.GetOutcome())
	assert.Equal(t, "tool-call probe dispatch failed (backend_timeout)", observation.GetObservationDetail())
}

func probeResponse(attemptID, text string) *operatorv1.InferenceDispatchResponse {
	result := &operatorv1.InferenceResult{
		ProviderAttemptId: attemptID,
		RequestedModel:    "probe-model:latest",
		ResultDigest:      "digest",
		Parts:             []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: text}}},
	}
	return &operatorv1.InferenceDispatchResponse{
		Result:  result,
		Receipt: &operatorv1.ActionReceipt{ResultSummary: result.GetResultDigest()},
	}
}
