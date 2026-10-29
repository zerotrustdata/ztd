// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const capabilityProbeAttemptPrefix = "capability-probe"

// RequiredModelCapabilityKinds returns the bounded probe set for Phase 3 inventory freeze.
func RequiredModelCapabilityKinds() []evalv1.ModelCapabilityKind {
	return []evalv1.ModelCapabilityKind{
		evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_COMPLETION,
		evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_TOOL_CALLING,
		evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_STRUCTURED_OUTPUT,
		evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_THINKING,
		evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_CONTEXT_LIMIT,
	}
}

type governedCapabilityProbeRunner struct {
	dispatcher GovernedInferenceDispatcher
	sessionID  string
	newID      func(string) string
}

// NewGovernedCapabilityProbeRunner executes bounded inventory capability probes
// through the exact Inference Operator session.
func NewGovernedCapabilityProbeRunner(dispatcher GovernedInferenceDispatcher, inferenceSessionID string, newID func(string) string) GovernedCapabilityProbeRunner {
	return &governedCapabilityProbeRunner{
		dispatcher: dispatcher,
		sessionID:  inferenceSessionID,
		newID:      newID,
	}
}

func (r *governedCapabilityProbeRunner) RunCapabilityProbes(ctx context.Context, variant *evalv1.ModelVariant) ([]*evalv1.ModelCapabilityObservation, error) {
	if r == nil || r.dispatcher == nil || r.sessionID == "" || r.newID == nil || variant == nil || variant.GetServedModelTag() == "" || variant.GetModelDigest() == "" {
		return nil, fmt.Errorf("evaluation: run model capability probes: %w", constants.ErrMissingRequiredField)
	}
	observations := make([]*evalv1.ModelCapabilityObservation, 0, len(RequiredModelCapabilityKinds()))
	for _, kind := range RequiredModelCapabilityKinds() {
		observation, err := r.probeModelCapability(ctx, variant, kind)
		if err != nil {
			return nil, fmt.Errorf("evaluation: run model capability probes: variant %s capability %s: %w", variant.GetVariantId(), kind.String(), err)
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

func (r *governedCapabilityProbeRunner) probeModelCapability(ctx context.Context, variant *evalv1.ModelVariant, kind evalv1.ModelCapabilityKind) (*evalv1.ModelCapabilityObservation, error) {
	switch kind {
	case evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_COMPLETION:
		return r.probeCompletionCapability(ctx, variant)
	case evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_TOOL_CALLING:
		return r.probeToolCallingCapability(ctx, variant)
	case evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_STRUCTURED_OUTPUT:
		return r.probeStructuredOutputCapability(ctx, variant)
	case evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_THINKING:
		return r.probeThinkingCapability(ctx, variant)
	case evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_CONTEXT_LIMIT:
		return probeContextLimitCapability(variant), nil
	default:
		return nil, fmt.Errorf("evaluation: probe model capability: unsupported kind %s", kind.String())
	}
}

func (r *governedCapabilityProbeRunner) dispatchCapabilityProbe(ctx context.Context, req InferenceProbeRequest) (*operatorv1.InferenceDispatchResponse, error) {
	dispatchReq, err := BuildInferenceProbeDispatchRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := r.dispatcher.DispatchInference(ctx, dispatchReq)
	if err != nil {
		return nil, err
	}
	if err := ValidateInferenceProbeResponse(req, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (r *governedCapabilityProbeRunner) baseProbeRequest(variant *evalv1.ModelVariant, attemptSuffix string) InferenceProbeRequest {
	return InferenceProbeRequest{
		ProviderAttemptID:       r.newID(capabilityProbeAttemptPrefix + "-" + attemptSuffix),
		Role:                    models.InferenceModelRolePrimary,
		Model:                   variant.GetServedModelTag(),
		ModelDigest:             variant.GetModelDigest(),
		TargetOperatorSessionID: r.sessionID,
	}
}

func (r *governedCapabilityProbeRunner) probeCompletionCapability(ctx context.Context, variant *evalv1.ModelVariant) (*evalv1.ModelCapabilityObservation, error) {
	req := r.baseProbeRequest(variant, "completion")
	req.Prompt = "Reply with exactly: capability-probe-ok"
	resp, err := r.dispatchCapabilityProbe(ctx, req)
	if err != nil {
		//nolint:nilerr // intentional fallback: probe failure recorded as observation verdict
		return capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_COMPLETION, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, "provider completion probe failed"), nil
	}
	if inferenceResultHasText(resp.GetResult(), "capability-probe-ok") {
		return capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_COMPLETION, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, "completion probe returned expected text"), nil
	}
	return capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_COMPLETION, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, "completion probe returned unexpected output"), nil
}

func (r *governedCapabilityProbeRunner) probeToolCallingCapability(ctx context.Context, variant *evalv1.ModelVariant) (*evalv1.ModelCapabilityObservation, error) {
	req := r.baseProbeRequest(variant, "tools")
	configureToolCallingProbe(&req)
	// AUTO, as agent scenarios declare it: the inference handler and the Ollama
	// backend reject every other mode as a capability the provider lacks.
	req.ToolChoice = &operatorv1.InferenceToolChoice{Mode: operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO}
	resp, err := r.dispatchCapabilityProbe(ctx, req)
	if err != nil {
		//nolint:nilerr // intentional fallback: probe dispatch failure recorded as observation verdict
		return probeDispatchFailure(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_TOOL_CALLING, "tool-call", err), nil
	}
	return toolCallingObservation(resp.GetResult()), nil
}

func (r *governedCapabilityProbeRunner) probeStructuredOutputCapability(ctx context.Context, variant *evalv1.ModelVariant) (*evalv1.ModelCapabilityObservation, error) {
	req := r.baseProbeRequest(variant, "structured")
	req.Prompt = `Return JSON with answer set to "capability-probe-json".`
	req.ResponseFormat = ProbeStructuredResponseFormat()
	resp, err := r.dispatchCapabilityProbe(ctx, req)
	if err != nil {
		//nolint:nilerr // intentional fallback: probe dispatch failure recorded as observation verdict
		return probeDispatchFailure(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_STRUCTURED_OUTPUT, "structured-output", err), nil
	}
	payload, ok := inferenceResultStructuredJSON(resp.GetResult())
	if !ok {
		return capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_STRUCTURED_OUTPUT, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, "structured-output probe returned non-JSON text"), nil
	}
	var decoded struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil || decoded.Answer != "capability-probe-json" {
		//nolint:nilerr // intentional fallback: JSON decode failure recorded as observation verdict
		return capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_STRUCTURED_OUTPUT, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, "structured-output probe returned invalid schema payload"), nil
	}
	return capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_STRUCTURED_OUTPUT, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, "structured-output probe returned valid JSON"), nil
}

func (r *governedCapabilityProbeRunner) probeThinkingCapability(ctx context.Context, variant *evalv1.ModelVariant) (*evalv1.ModelCapabilityObservation, error) {
	req := r.baseProbeRequest(variant, "thinking")
	req.Prompt = "Think briefly, then reply with exactly: capability-probe-thinking"
	req.Thinking = &operatorv1.InferenceThinkingControl{Mode: &operatorv1.InferenceThinkingControl_Enabled{Enabled: true}}
	resp, err := r.dispatchCapabilityProbe(ctx, req)
	if err != nil {
		//nolint:nilerr // intentional fallback: probe dispatch failure recorded as observation verdict
		return probeDispatchFailure(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_THINKING, "thinking", err), nil
	}
	if inferenceResultHasThinking(resp.GetResult()) || inferenceResultHasText(resp.GetResult(), "capability-probe-thinking") {
		return capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_THINKING, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, "thinking probe returned visible thinking or expected answer text"), nil
	}
	return capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_THINKING, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, "thinking probe returned neither thinking nor expected answer"), nil
}

// probeDispatchFailure records a failed probe dispatch. Only a provider
// capability rejection means the model lacks the capability (UNSUPPORTED); any
// other failure, a timeout included, is a failed probe recorded with its class.
func probeDispatchFailure(kind evalv1.ModelCapabilityKind, probe string, err error) *evalv1.ModelCapabilityObservation {
	class := inference.ClassifyAttemptFailure(err)
	if class == inference.AttemptFailureCapabilityRejected {
		return capabilityObservation(kind, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSUPPORTED, "provider rejected "+probe+" probe")
	}
	return capabilityObservation(kind, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, fmt.Sprintf("%s probe dispatch failed (%s)", probe, class))
}

func probeContextLimitCapability(variant *evalv1.ModelVariant) *evalv1.ModelCapabilityObservation {
	if variant.GetContextLimit() > 0 {
		return capabilityObservation(
			evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_CONTEXT_LIMIT,
			evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
			fmt.Sprintf("provider advertised context_limit=%d", variant.GetContextLimit()),
		)
	}
	return capabilityObservation(
		evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_CONTEXT_LIMIT,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE,
		"provider did not advertise context_limit",
	)
}

func capabilityObservation(kind evalv1.ModelCapabilityKind, outcome evalv1.EvaluationVerdictStatus, detail string) *evalv1.ModelCapabilityObservation {
	return &evalv1.ModelCapabilityObservation{
		Capability:        kind,
		Outcome:           outcome,
		ObservationDetail: detail,
	}
}

func inferenceResultHasText(result *operatorv1.InferenceResult, expected string) bool {
	if result == nil {
		return false
	}
	text := strings.ToLower(models.InferenceResultText(result))
	return strings.Contains(text, strings.ToLower(expected))
}

const toolCallingProbeMessage = "capability-probe-tool"

func configureToolCallingProbe(req *InferenceProbeRequest) {
	req.Prompt = "Call probe_echo with message capability-probe-tool. Return exactly one structured tool call; do not call read_file or write a tool call as text."
	req.Tools = []*operatorv1.InferenceToolDeclaration{ProbeEchoToolDeclaration(), {
		Name: "read_file", Description: "Read a file at a supplied path.",
		JsonSchema: `{"additionalProperties":false,"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}`,
	}}
	req.ToolChoice = &operatorv1.InferenceToolChoice{Mode: operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO}
}

// toolCallingObservation accepts the structured protocol, including its exact
// schema, rather than tool-shaped prose or a polluted function name.
func toolCallingObservation(result *operatorv1.InferenceResult) *evalv1.ModelCapabilityObservation {
	valid, calls := true, 0
	for _, part := range result.GetParts() {
		if call := part.GetToolCall(); call != nil {
			calls++
			var args struct {
				Message string `json:"message"`
			}
			decoder := json.NewDecoder(strings.NewReader(call.GetArgumentsJson()))
			decoder.DisallowUnknownFields()
			err := decoder.Decode(&args)
			var trailing json.RawMessage
			valid = valid && call.GetName() == "probe_echo" && err == nil && args.Message == toolCallingProbeMessage && decoder.Decode(&trailing) == io.EOF
		}
		text := strings.TrimSpace(part.GetText())
		if text != "" && textLooksLikeProbeToolCall(text) {
			valid = false
		}
	}
	if valid && calls == 1 {
		return capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_TOOL_CALLING, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, "tool-call probe returned a structured probe_echo call")
	}
	return capabilityObservation(evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_TOOL_CALLING, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, "tool-call probe did not return a structured probe_echo call")
}

func textLooksLikeProbeToolCall(text string) bool {
	for _, name := range []string{"probe_echo", "read_file"} {
		if strings.HasPrefix(text, name) && json.Valid([]byte(strings.TrimSpace(strings.TrimPrefix(text, name)))) {
			return true
		}
	}
	var call struct {
		Name      string          `json:"name"`
		Function  json.RawMessage `json:"function"`
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	return json.Unmarshal([]byte(text), &call) == nil && (call.Name != "" || len(call.Function) > 0 || len(call.ToolCalls) > 0)
}

func inferenceResultHasThinking(result *operatorv1.InferenceResult) bool {
	if result == nil {
		return false
	}
	for _, part := range result.GetParts() {
		if part.GetThinking() != "" {
			return true
		}
	}
	return false
}

func inferenceResultStructuredJSON(result *operatorv1.InferenceResult) (string, bool) {
	if result == nil {
		return "", false
	}
	text := extractStructuredJSONPayload(models.InferenceResultText(result))
	if text == "" {
		return "", false
	}
	if json.Valid([]byte(text)) {
		return text, true
	}
	return "", false
}
