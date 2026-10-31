// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/jitllm/jitllm/engine/grammar"
	"github.com/jitllm/jitllm/engine/model"
	"github.com/jitllm/jitllm/server"
	"github.com/jitllm/jitllm/tok/jinja"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/jitllm"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const jitllmComponent = "jitllm_backend"

// JitLLMBackend implements Provider by running the jitllm engine inside the
// Inference Operator process. The Operator is the inference server: it loads
// .jlm containers from its models folder on first use, owns their device
// placement and residency, and generates through the engine's own API. There
// is no provider endpoint, listener, or credential. The ztd model digest is
// the SHA-256 of the .jlm file the engine loads.
type JitLLMBackend struct {
	engine           *server.Engine
	logger           *slog.Logger
	modelRoot        string
	digests          *modelFileDigester
	maxResponseBytes int64
}

// NewJitLLMBackend builds the in-process engine over modelRoot. It touches
// no hardware and loads no model; the first request naming a model does.
func NewJitLLMBackend(modelRoot string, logger *slog.Logger) (*JitLLMBackend, error) {
	root := strings.TrimSpace(modelRoot)
	if root == "" {
		return nil, fmt.Errorf("%s: %w", jitllmComponent, constants.ErrInferenceJitLLMModelRootRequired)
	}
	if logger == nil {
		return nil, fmt.Errorf("%s: logger: %w", jitllmComponent, constants.ErrMissingRequiredField)
	}
	root = filepath.Clean(root)
	return &JitLLMBackend{
		engine: server.New(server.Config{
			ModelDir: root,
			OnDemand: server.LoadOptions{
				DeviceIDs:       server.DefaultDevices(nil),
				MaxDeviceBlocks: -1,
			},
		}),
		logger:           logger,
		modelRoot:        root,
		digests:          newModelFileDigester(jitllmComponent),
		maxResponseBytes: defaultMaxResponseBytes,
	}, nil
}

// Close unloads every model and releases the devices the engine opened.
func (b *JitLLMBackend) Close() error {
	b.engine.Close()
	return nil
}

func jitllmUnloadAfterRequest(keepAlive string) (bool, error) {
	switch keepAlive {
	case "", constants.InferenceDefaultKeepAlive:
		return false, nil
	case constants.InferenceUnloadKeepAlive:
		return true, nil
	default:
		return false, fmt.Errorf("%s: keep_alive %q: %w", jitllmComponent, keepAlive, constants.ErrInferenceCapabilityUnsupported)
	}
}

// jitllmChatRequest is the normalized engine request. Its JSON encoding is
// what NormalizedRequestHash binds, so every field that changes the output
// is in it.
type jitllmChatRequest struct {
	Model          string              `json:"model"`
	Messages       []model.ChatMessage `json:"messages"`
	Tools          json.RawMessage     `json:"tools,omitempty"`
	SingleToolCall bool                `json:"single_tool_call,omitempty"`
	Temperature    float32             `json:"temperature"`
	TopP           *float32            `json:"top_p,omitempty"`
	TopK           *int32              `json:"top_k,omitempty"`
	Seed           *int32              `json:"seed,omitempty"`
	MaxTokens      int32               `json:"max_tokens"`
	Stop           []string            `json:"stop,omitempty"`
	Grammar        string              `json:"grammar,omitempty"`
}

func jitllmChatRequestFor(req models.GenerateRequest) (jitllmChatRequest, error) {
	messages, err := inferenceMessagesToOllama(req.Messages)
	if err != nil {
		return jitllmChatRequest{}, fmt.Errorf("%s: generate: messages: %w", jitllmComponent, err)
	}
	tools, err := inferenceToolsToOllama(req.Tools, req.ToolChoice)
	if err != nil {
		return jitllmChatRequest{}, fmt.Errorf("%s: generate: tools: %w", jitllmComponent, err)
	}
	chatReq := jitllmChatRequest{
		Model:          req.Model,
		Messages:       make([]model.ChatMessage, 0, len(messages)),
		SingleToolCall: req.ParallelToolCalls != nil && !*req.ParallelToolCalls,
		Temperature:    req.Temperature,
		TopP:           req.TopP,
		TopK:           req.TopK,
		Seed:           req.Seed,
		MaxTokens:      resolveOutputTokenLimit(req.MaxTokens),
		Stop:           req.StopSequences,
	}
	for _, message := range messages {
		mapped := model.ChatMessage{Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID, Name: message.ToolName}
		for _, call := range message.ToolCalls {
			mapped.ToolCalls = append(mapped.ToolCalls, model.ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: string(call.Function.Arguments)})
		}
		chatReq.Messages = append(chatReq.Messages, mapped)
	}
	if len(tools) > 0 {
		if chatReq.Tools, err = json.Marshal(tools); err != nil {
			return jitllmChatRequest{}, fmt.Errorf("%s: generate: tools: %w", jitllmComponent, err)
		}
	}
	if req.ResponseFormat != nil {
		if req.ResponseFormat.GetMediaType() != inferenceJSONMediaType {
			return jitllmChatRequest{}, fmt.Errorf("%s: generate: response format: %w", jitllmComponent, constants.ErrInferenceCapabilityUnsupported)
		}
		if err := validateInferenceToolSchema(req.ResponseFormat.GetJsonSchema()); err != nil {
			return jitllmChatRequest{}, fmt.Errorf("%s: generate: response format: %w", jitllmComponent, err)
		}
		if chatReq.Grammar, err = grammar.FromJSONSchema([]byte(req.ResponseFormat.GetJsonSchema())); err != nil {
			return jitllmChatRequest{}, fmt.Errorf("%s: generate: response format: %w: %w", jitllmComponent, constants.ErrInferenceCapabilityUnsupported, err)
		}
	}
	return chatReq, nil
}

// generateOptions is the engine request for r. An unseeded request draws a
// fresh seed, as the engine's own API does, so unseeded calls do not share
// one sampled sequence.
func (r jitllmChatRequest) generateOptions() (server.GenerateOptions, error) {
	var seed int64
	if r.Seed == nil {
		var randomSeed [8]byte
		if _, err := rand.Read(randomSeed[:]); err != nil {
			return server.GenerateOptions{}, fmt.Errorf("%s: generate: random seed: %w", jitllmComponent, err)
		}
		seed = int64(binary.LittleEndian.Uint64(randomSeed[:]))
	} else {
		seed = int64(*r.Seed)
	}
	sampler := &model.Sampler{Temp: float64(r.Temperature), TopP: 1, Seed: seed}
	if r.TopP != nil {
		sampler.TopP = float64(*r.TopP)
	}
	if r.TopK != nil {
		sampler.TopK = max(int(*r.TopK), 0)
	}
	chat := &server.ChatInput{Messages: r.Messages, AddGenerationPrompt: true, Tools: r.Tools}
	if len(r.Tools) > 0 {
		chat.ToolChoice = model.ToolChoice{Mode: model.ToolChoiceAuto, Single: r.SingleToolCall}
	}
	return server.GenerateOptions{
		ModelID:   r.Model,
		Prompt:    server.Prompt{Kind: server.PromptChat, Chat: chat},
		MaxTokens: int(r.MaxTokens),
		Sampling:  sampler,
		Stop:      r.Stop,
		Grammar:   r.Grammar,
	}, nil
}

// Generate runs one chat completion on the in-process engine and returns the
// generated parts, usage, and engine-measured timings.
func (b *JitLLMBackend) Generate(ctx context.Context, req models.GenerateRequest) (*models.GenerateResponse, error) {
	if req.Model == "" {
		return nil, fmt.Errorf("%s: generate: %w", jitllmComponent, constants.ErrInferenceModelRefInvalid)
	}
	if req.Thinking != nil {
		return nil, fmt.Errorf("%s: generate: thinking: %w", jitllmComponent, constants.ErrInferenceCapabilityUnsupported)
	}
	unload, err := jitllmUnloadAfterRequest(req.KeepAlive)
	if err != nil {
		return nil, err
	}
	if req.ModelDigest != "" {
		if err := b.verifyModelDigest(ctx, req.Model, req.ModelDigest); err != nil {
			return nil, err
		}
	}
	chatReq, err := jitllmChatRequestFor(req)
	if err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal request: %w", jitllmComponent, err)
	}

	var stream *streamSegmenter
	if req.Stream {
		if reporter := ProgressReporterFromContext(ctx); reporter != nil {
			stream, err = newStreamSegmenter(req.Scrubber, reporter, req.ProviderAttemptID)
			if err != nil {
				return nil, fmt.Errorf("%s: generate: %w", jitllmComponent, err)
			}
		}
	}
	if unload {
		defer b.unloadAfterRequest(ctx, req.Model)
	}
	allowParallelToolCalls := req.ParallelToolCalls == nil || *req.ParallelToolCalls
	run := newJitLLMRun(b, ctx, req, chatReq, stream, allowParallelToolCalls)
	opts, err := chatReq.generateOptions()
	if err != nil {
		return nil, err
	}
	if err := b.engine.Generate(ctx, opts, run.emit); err != nil {
		if run.err != nil {
			return nil, run.err
		}
		return nil, b.engineError(ctx, "generate", req.Model, err)
	}
	finished, parts, err := run.finish()
	if err != nil {
		return nil, err
	}
	if store := attemptStoreFromContext(ctx); store != nil {
		if err := store.RecordRawResponse(context.WithoutCancel(ctx), req.ProviderAttemptID, run.raw); err != nil {
			return nil, fmt.Errorf("%s: record raw response digest: %w", jitllmComponent, err)
		}
	}

	promptTokens, completionTokens := int32(finished.PromptTokens), int32(finished.CompletionTokens)
	if finished.PromptTokens < 0 || finished.CompletionTokens < 0 || int64(finished.PromptTokens)+int64(finished.CompletionTokens) > 2147483647 {
		return nil, fmt.Errorf("%s: generate: token usage: %w", jitllmComponent, constants.ErrInferenceProviderResponseInvalid)
	}
	if req.ContextLimit != nil && (promptTokens >= *req.ContextLimit || int64(promptTokens)+int64(completionTokens) > int64(*req.ContextLimit)) {
		return nil, fmt.Errorf("%s: generate: %w: prompt_tokens=%d completion_tokens=%d context_limit=%d", jitllmComponent, constants.ErrInferenceContextOverflow, promptTokens, completionTokens, *req.ContextLimit)
	}
	cacheTokens := int32(finished.Restored)
	promptEval, generation := finished.Prefill.Nanoseconds(), finished.Decode.Nanoseconds()
	finishReason := "stop"
	if finished.Reason == server.FinishMaxTokens {
		finishReason = "length"
	}
	outputHash, err := models.ComputeInferenceOutputHash(parts, finishReason)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: %w", jitllmComponent, err)
	}
	servedModelDigest := ""
	if req.ModelDigest != "" {
		if err := b.verifyModelDigest(ctx, req.Model, req.ModelDigest); err != nil {
			return nil, err
		}
		servedModelDigest = req.ModelDigest
	}
	return &models.GenerateResponse{
		Parts:                 parts,
		PromptTokens:          promptTokens,
		CompletionTokens:      completionTokens,
		TotalTokens:           promptTokens + completionTokens,
		UsageReported:         true,
		CacheTokens:           &cacheTokens,
		PromptEvalDurationNS:  &promptEval,
		GenerationDurationNS:  &generation,
		TimeToFirstTokenNS:    run.timeToFirstToken,
		TimingSource:          operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER,
		FinishReason:          finishReason,
		Model:                 req.Model,
		ServedModelDigest:     servedModelDigest,
		NormalizedRequestHash: models.SHA256Hex(normalized),
		OutputHash:            outputHash,
	}, nil
}

// jitllmRun folds one generate's engine events into response parts. Text
// streams as it arrives; with tools declared, the model's own tool-call
// syntax is read from the token ids and a call is emitted once it closes.
type jitllmRun struct {
	b                      *JitLLMBackend
	ctx                    context.Context
	req                    models.GenerateRequest
	stream                 *streamSegmenter
	allowParallelToolCalls bool
	startedAt              time.Time
	timeToFirstToken       *int64

	tools    *model.ToolStream
	toolIDs  bool
	calls    []model.ToolCall
	parts    []*operatorv1.InferenceResponsePart
	finished *server.Finished
	raw      []byte
	err      error
}

func newJitLLMRun(b *JitLLMBackend, ctx context.Context, req models.GenerateRequest, chatReq jitllmChatRequest, stream *streamSegmenter, allowParallelToolCalls bool) *jitllmRun {
	run := &jitllmRun{b: b, ctx: ctx, req: req, stream: stream, allowParallelToolCalls: allowParallelToolCalls, startedAt: time.Now()}
	if len(chatReq.Tools) > 0 {
		// The engine replaces this with a stream in the model's own syntax
		// when the model declares one (Started.Tools).
		run.tools = model.NewTextToolStream(model.ToolSyntaxHermes, model.ParseToolSet(chatReq.Tools))
	}
	return run
}

// emit is the engine callback. The first error it returns stops the
// generate and is kept so Generate reports it rather than the engine's
// wrapping of it.
func (r *jitllmRun) emit(ev server.Event) error {
	if r.err == nil {
		r.err = r.apply(ev)
	}
	return r.err
}

func (r *jitllmRun) apply(ev server.Event) error {
	if err := r.record(ev); err != nil {
		return err
	}
	switch ev.Kind {
	case server.EventStarted:
		if r.tools != nil && ev.Started != nil && ev.Started.Tools != nil {
			r.tools, r.toolIDs = ev.Started.Tools, true
		}
	case server.EventToken:
		if ev.Token == nil {
			return fmt.Errorf("%s: generate: token event: %w", jitllmComponent, constants.ErrInferenceProviderResponseInvalid)
		}
		if ev.Token.ID >= 0 && r.timeToFirstToken == nil {
			ttft := time.Since(r.startedAt).Nanoseconds()
			r.timeToFirstToken = &ttft
		}
		text := ev.Token.Text
		var calls []model.ToolCall
		switch {
		case r.tools == nil:
		case r.toolIDs && ev.Token.ID < 0:
			// Held-back text the tool stream already decoded from the ids.
			text = ""
		case r.toolIDs:
			text, calls = r.tools.Push(ev.Token.ID)
		default:
			text, calls = r.tools.PushText(ev.Token.Text)
		}
		return r.deliver(text, calls)
	case server.EventFinished:
		if ev.Finished == nil || r.finished != nil {
			return fmt.Errorf("%s: generate: finished event: %w", jitllmComponent, constants.ErrInferenceProviderResponseInvalid)
		}
		r.finished = ev.Finished
	}
	return nil
}

// record appends the event to the raw engine output the attempt record binds
// by digest, bounded like a provider response body.
func (r *jitllmRun) record(ev server.Event) error {
	if ev.Started != nil {
		started := *ev.Started
		started.Tools = nil
		ev.Started = &started
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("%s: generate: record event: %w", jitllmComponent, err)
	}
	if int64(len(r.raw)+len(line)+1) > r.b.maxResponseBytes {
		return fmt.Errorf("%s: generate: %w: response exceeds %d bytes", jitllmComponent, constants.ErrInferenceProviderResponseInvalid, r.b.maxResponseBytes)
	}
	r.raw = append(append(r.raw, line...), '\n')
	return nil
}

func (r *jitllmRun) deliver(text string, calls []model.ToolCall) error {
	var eventParts []*operatorv1.InferenceResponsePart
	if text != "" {
		eventParts = append(eventParts, &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Text{Text: text}})
	}
	for _, call := range calls {
		part, err := r.toolCallPart(call)
		if err != nil {
			return err
		}
		eventParts = append(eventParts, part)
	}
	if len(eventParts) == 0 {
		return nil
	}
	if r.stream != nil {
		released, err := r.stream.accept(r.ctx, eventParts)
		if err != nil {
			return fmt.Errorf("%s: generate: stream segments: %w", jitllmComponent, err)
		}
		eventParts = released
		if err := r.stream.publish(released, r.req.Model, r.timeToFirstToken); err != nil {
			r.b.logger.Warn("Inference progress delivery failed", "provider_attempt_id", r.req.ProviderAttemptID, "error", err)
		}
	}
	r.parts = append(r.parts, eventParts...)
	return nil
}

func (r *jitllmRun) toolCallPart(call model.ToolCall) (*operatorv1.InferenceResponsePart, error) {
	r.calls = append(r.calls, call)
	if !r.allowParallelToolCalls && len(r.calls) > 1 {
		return nil, fmt.Errorf("%s: generate: parallel tool calls: %w", jitllmComponent, constants.ErrInferenceProviderResponseInvalid)
	}
	if call.Name == "" {
		return nil, fmt.Errorf("%s: generate: tool call %d: %w", jitllmComponent, len(r.calls)-1, constants.ErrInferenceProviderResponseInvalid)
	}
	arguments, err := canonicalizeJSON(cmpOrEmptyObject(call.Arguments), true, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: generate: tool call %d arguments: %w", jitllmComponent, len(r.calls)-1, constants.ErrInferenceProviderResponseInvalid)
	}
	callID := call.ID
	if callID == "" {
		callID = "call_" + r.b.engine.NextID("tc")
	}
	return &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
		CallId:        callID,
		Name:          call.Name,
		ArgumentsJson: arguments,
	}}}, nil
}

func cmpOrEmptyObject(arguments string) string {
	if strings.TrimSpace(arguments) == "" {
		return "{}"
	}
	return arguments
}

// finish releases what the tool stream and stream segmenter still hold once
// the engine reports the generate finished.
func (r *jitllmRun) finish() (*server.Finished, []*operatorv1.InferenceResponsePart, error) {
	if r.finished == nil {
		return nil, nil, fmt.Errorf("%s: generate: ended without a finished event: %w", jitllmComponent, constants.ErrInferenceProviderResponseInvalid)
	}
	switch r.finished.Reason {
	case server.FinishStop, server.FinishEOS, server.FinishMaxTokens:
	case server.FinishCancelled:
		return nil, nil, fmt.Errorf("%s: generate: %w", jitllmComponent, cmpErr(r.ctx.Err(), context.Canceled))
	default:
		return nil, nil, fmt.Errorf("%s: generate: finish reason %d: %w", jitllmComponent, r.finished.Reason, constants.ErrInferenceGenerateFailed)
	}
	if r.tools != nil {
		tail, calls, _ := r.tools.Finish()
		if stop := r.finished.StopMatched; stop != "" && r.toolIDs {
			// The ids carry the stop string; the reply does not.
			tail, _, _ = strings.Cut(tail, stop)
		}
		if err := r.deliver(tail, calls); err != nil {
			return nil, nil, err
		}
	}
	if r.stream != nil {
		tail := r.stream.finish(r.ctx)
		if err := r.stream.publish(tail, r.req.Model, r.timeToFirstToken); err != nil {
			r.b.logger.Warn("Inference progress delivery failed", "provider_attempt_id", r.req.ProviderAttemptID, "error", err)
		}
		r.parts = append(r.parts, tail...)
	}
	return r.finished, r.parts, nil
}

func cmpErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// engineError classifies an engine refusal or failure.
func (b *JitLLMBackend) engineError(ctx context.Context, op, modelName string, err error) error {
	var notConverted *model.NotConvertedError
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("%s: %s: %w: %w", jitllmComponent, op, context.Canceled, err)
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %s: %w: %w", jitllmComponent, op, constants.ErrInferenceBackendTimeout, err)
	case errors.Is(err, server.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s: %s: %w: model %q", jitllmComponent, op, constants.ErrInferenceModelNotFound, modelName)
	case errors.As(err, &notConverted):
		return fmt.Errorf("%s: %s: model %q: %w", jitllmComponent, op, modelName, constants.ErrJitLLMContainerInvalid)
	case errors.Is(err, server.ErrOverloaded), errors.Is(err, server.ErrQueueTimeout), errors.Is(err, server.ErrPlacement):
		return fmt.Errorf("%s: %s: %w: %w", jitllmComponent, op, constants.ErrInferenceBackendUnavailable, err)
	case errors.Is(err, server.ErrInvalid):
		return fmt.Errorf("%s: %s: %w: %w", jitllmComponent, op, constants.ErrInferenceGenerationOptionsInvalid, err)
	default:
		b.logger.Warn("jitllm generation failed", "op", op, "model", modelName, "error", err)
		return fmt.Errorf("%s: %s: %w: %w", jitllmComponent, op, constants.ErrInferenceGenerateFailed, err)
	}
}

// Decide answers a System One request with the engine's decision readout.
// The request is the System One body the HTTP providers receive, parsed by
// the engine's own question parser, and the answer is the engine's System
// One response validated by the same decoder.
func (b *JitLLMBackend) Decide(ctx context.Context, req models.DecisionRequest) (*models.DecisionResponse, error) {
	if req.AllowHosted || req.Input.GetAllowHosted() {
		return nil, constants.ErrInferenceHostedNotAllowed
	}
	unload, err := jitllmUnloadAfterRequest(req.KeepAlive)
	if err != nil {
		return nil, err
	}
	req.KeepAlive = ""
	body, err := decisionBody(req)
	if err != nil {
		return nil, err
	}
	if req.ModelDigest != "" {
		if err := b.verifyModelDigest(ctx, req.Model, req.ModelDigest); err != nil {
			return nil, err
		}
	}
	state, questions, err := jitllmDecisionInput(body)
	if err != nil {
		return nil, err
	}
	if unload {
		defer b.unloadAfterRequest(ctx, req.Model)
	}
	result, err := b.engine.Decide(ctx, server.DecideOptions{Model: req.Model, State: state, Questions: questions})
	if err != nil {
		if errors.Is(err, server.ErrInvalid) {
			return nil, fmt.Errorf("%s: decide: %w: %w", jitllmComponent, constants.ErrInferenceDecisionRequestInvalid, err)
		}
		return nil, b.engineError(ctx, "decide", req.Model, err)
	}
	raw := model.DecisionResponse(result.ModelID, questions, result.Answers, result.InputTokens)
	response, err := decodeSystemOneResponse(ctx, raw, body, req, b.maxResponseBytes)
	if err != nil {
		return nil, err
	}
	if req.ModelDigest != "" {
		if err := b.verifyModelDigest(ctx, req.Model, req.ModelDigest); err != nil {
			return nil, err
		}
	}
	return response, nil
}

// jitllmDecisionInput reads a System One request body with the engine's own
// parser, as its HTTP API would.
func jitllmDecisionInput(body []byte) (jinja.Value, []model.DecisionQuestion, error) {
	parsed, err := jinja.FromJSON(body)
	if err != nil || !parsed.IsDict() {
		return jinja.Value{}, nil, fmt.Errorf("%s: decide: request: %w", jitllmComponent, constants.ErrInferenceDecisionRequestInvalid)
	}
	state, _ := parsed.AsDict().Get("state")
	questionsValue, _ := parsed.AsDict().Get("questions")
	questions, err := model.ParseDecisionQuestions(questionsValue)
	if err != nil {
		return jinja.Value{}, nil, fmt.Errorf("%s: decide: questions: %w: %w", jitllmComponent, constants.ErrInferenceDecisionRequestInvalid, err)
	}
	return state, questions, nil
}

// Status lists every model the engine can serve: those loaded and the
// containers in its models folder it would load on demand.
func (b *JitLLMBackend) Status(context.Context) (*models.BackendStatus, error) {
	available := b.engine.ListAvailable()
	names := make([]string, 0, len(available))
	for _, summary := range available {
		names = append(names, summary.ID)
	}
	return &models.BackendStatus{Available: true, Models: names}, nil
}

// ReadResidency lists each loaded model under its id and every name that
// still refers to it.
func (b *JitLLMBackend) ReadResidency(context.Context) (ProviderResidency, error) {
	loaded := b.engine.ListLoaded()
	residency := ProviderResidency{Models: make([]ProviderResidencyModel, 0, len(loaded))}
	seen := map[string]struct{}{}
	add := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		residency.Models = append(residency.Models, ProviderResidencyModel{Name: name})
	}
	for _, summary := range loaded {
		add(summary.ID)
		add(summary.Name)
		for _, alias := range summary.Aliases {
			add(alias)
		}
	}
	return residency, nil
}

// loadedID returns the id of the loaded model name refers to, or "".
func (b *JitLLMBackend) loadedID(name string) string {
	for _, summary := range b.engine.ListLoaded() {
		if summary.ID == name || summary.Name == name || containsString(summary.Aliases, name) {
			return summary.ID
		}
	}
	return ""
}

// ReleaseModels unloads each requested model that is resident and then
// verifies the requested names are absent. The unload closes any session
// still on the model: a governed release is the campaign boundary.
func (b *JitLLMBackend) ReleaseModels(ctx context.Context, release []string) error {
	if len(release) == 0 {
		return fmt.Errorf("%s: release models: %w: at least one model is required", jitllmComponent, constants.ErrInferenceModelTagInvalid)
	}
	for _, name := range release {
		if err := operatorcapability.ValidateServedModelTag(name); err != nil {
			return fmt.Errorf("%s: release models: %w", jitllmComponent, err)
		}
	}
	for _, name := range release {
		id := b.loadedID(name)
		if id == "" {
			if _, err := jitllm.Resolve(b.modelRoot, name); err != nil {
				return fmt.Errorf("%s: release model: %w", jitllmComponent, err)
			}
			continue
		}
		if _, err := b.engine.UnloadModel(id, true); err != nil && !errors.Is(err, server.ErrNotFound) {
			return fmt.Errorf("%s: release model %q: %w: %w", jitllmComponent, name, constants.ErrInferenceBackendUnavailable, err)
		}
	}
	return awaitModelsAbsent(ctx, jitllmComponent, b.ReadResidency, release)
}

// unloadAfterRequest serves keep-alive 0. It leaves a model another request
// is still using loaded rather than closing that request's session.
func (b *JitLLMBackend) unloadAfterRequest(ctx context.Context, name string) {
	id := b.loadedID(name)
	if id == "" {
		return
	}
	if _, err := b.engine.UnloadModel(id, false); err != nil && !errors.Is(err, server.ErrNotFound) {
		b.logger.Warn("jitllm keep-alive 0 unload deferred", "model", name, "error", err)
	}
	_ = ctx
}

// ListProviderModelInventory describes every enabled .jlm container in the
// models folder with its file digest and the metadata its header carries.
// A container this build cannot open cannot be served and is skipped.
func (b *JitLLMBackend) ListProviderModelInventory(ctx context.Context) ([]ProviderModelInventoryEntry, error) {
	containers, err := jitllm.List(b.modelRoot)
	if err != nil {
		return nil, fmt.Errorf("inference: list provider model inventory: %w", err)
	}
	entries := make([]ProviderModelInventoryEntry, 0, len(containers))
	for _, container := range containers {
		if container.Disabled {
			continue
		}
		metadata, err := jitllm.ReadMetadata(container.Path)
		if err != nil {
			b.logger.Warn("jitllm container skipped from inventory", "model", container.ID, "error", err)
			continue
		}
		digest, err := b.digests.fileDigest(ctx, container.Path)
		if err != nil {
			return nil, fmt.Errorf("inference: list provider model inventory: model %q: %w", container.ID, err)
		}
		entries = append(entries, describeJitLLMModel(container, metadata, digest))
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("inference: list provider model inventory: %w", constants.ErrInferenceModelNotFound)
	}
	return entries, nil
}

func describeJitLLMModel(container jitllm.Container, metadata jitllm.Metadata, digest string) ProviderModelInventoryEntry {
	countSource := operatorv1.ParameterCountSource_PARAMETER_COUNT_SOURCE_UNSPECIFIED
	if metadata.ParameterCount > 0 {
		countSource = operatorv1.ParameterCountSource_PARAMETER_COUNT_SOURCE_MODEL_METADATA
	}
	capabilities := []string{"completion"}
	if metadata.Vision {
		capabilities = append(capabilities, "vision")
	}
	return ProviderModelInventoryEntry{
		ProviderClass:          constants.InferenceProviderJitLLM,
		ServedModelTag:         container.ID,
		ModelDigest:            digest,
		ModelFamily:            metadata.Architecture,
		ParameterSize:          formatParameterCount(metadata.ParameterCount),
		ParameterCount:         metadata.ParameterCount,
		ParameterCountSource:   countSource,
		Quantization:           metadata.Quantization,
		Format:                 constants.ModelWeightFormatJLM,
		ArtifactSizeBytes:      container.Size,
		ContextLimit:           metadata.ContextLength,
		AdvertisedCapabilities: capabilities,
	}
}

func (b *JitLLMBackend) verifyModelDigest(ctx context.Context, name, expected string) error {
	container, err := jitllm.Resolve(b.modelRoot, name)
	if err != nil {
		return fmt.Errorf("%s: model digest: %w", jitllmComponent, err)
	}
	digest, err := b.digests.fileDigest(ctx, container.Path)
	if err != nil {
		return err
	}
	if digest != expected {
		return fmt.Errorf("%s: model %q: %w", jitllmComponent, name, constants.ErrInferenceModelDigestMismatch)
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
