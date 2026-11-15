// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration || e2e

package fixtures

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

const (
	evalModelTag    = "qwen3:4b"
	evalModelDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	// evalContextWindow is the context window every served model loads with,
	// matching the num_ctx /api/show reports.
	evalContextWindow = 8192
)

// OllamaToolCall is one frozen tool call returned by the scripted provider.
type OllamaToolCall struct {
	ID        string          `json:"id,omitempty"`
	Type      string          `json:"type,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// OllamaChatMessage is one message of a decoded /api/chat request.
type OllamaChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OllamaChatRequest is the decoded /api/chat body the Inference Operator sent.
type OllamaChatRequest struct {
	Model    string              `json:"model"`
	Messages []OllamaChatMessage `json:"messages"`
	Stream   bool                `json:"stream"`
	Options  struct {
		NumCtx *int64 `json:"num_ctx,omitempty"`
	} `json:"options"`
}

// isModelLoad reports whether the request is the Operator's load step: a
// non-streaming chat with no messages, which loads the model and generates
// nothing.
func (r OllamaChatRequest) isModelLoad() bool {
	return !r.Stream && len(r.Messages) == 0
}

// OllamaReply is one complete response to an Ollama /api/chat request.
type OllamaReply struct {
	Content   string           `json:"content,omitempty"`
	Thinking  string           `json:"thinking,omitempty"`
	ToolCalls []OllamaToolCall `json:"tool_calls,omitempty"`
	// Failure makes the turn fail the way an unavailable provider does: HTTP
	// 500 with an Ollama error body.
	Failure string `json:"-"`
	// FailureStatus overrides the failing turn's HTTP status.
	FailureStatus int `json:"-"`
	// Loop makes the turn a runaway generation: after any scripted tool calls,
	// the provider streams Loop back to back and never sends a done event, so
	// only the Inference Operator's repetition stop can end the turn. A turn
	// still streaming once its bound is reached fails the owning test.
	Loop string `json:"-"`
	// BeforeReply runs in the request handler before the reply is served, so a
	// test can drive governed work while the model turn is in flight.
	BeforeReply func() `json:"-"`
	// Respond, when set, supplies the parts of this turn that depend on the
	// decoded request, such as tool arguments naming a path only known once the
	// run has started. Whatever it leaves unset keeps this reply's scripted value,
	// including Thinking, Content, and the BeforeReply and Failure hooks.
	Respond func(request OllamaChatRequest) OllamaReply `json:"-"`
}

// withResponse fills the parts a Respond hook returned into this scripted turn.
func (r OllamaReply) withResponse(responded OllamaReply) OllamaReply {
	if responded.Content == "" {
		responded.Content = r.Content
	}
	if responded.Thinking == "" {
		responded.Thinking = r.Thinking
	}
	if len(responded.ToolCalls) == 0 {
		responded.ToolCalls = r.ToolCalls
	}
	if responded.Failure == "" {
		responded.Failure = r.Failure
		responded.FailureStatus = r.FailureStatus
	}
	if responded.Loop == "" {
		responded.Loop = r.Loop
	}
	if responded.BeforeReply == nil {
		responded.BeforeReply = r.BeforeReply
	}
	responded.Respond = nil
	return responded
}

type scriptedOllamaShow struct {
	Parameters string `json:"parameters"`
	Details    struct {
		Format            string `json:"format"`
		Family            string `json:"family"`
		ParameterSize     string `json:"parameter_size"`
		QuantizationLevel string `json:"quantization_level"`
	} `json:"details"`
	Capabilities []string `json:"capabilities"`
}

type scriptedOllamaMessage struct {
	Role      string             `json:"role"`
	Content   string             `json:"content"`
	Thinking  string             `json:"thinking,omitempty"`
	ToolCalls []scriptedToolCall `json:"tool_calls,omitempty"`
}

type scriptedToolCall struct {
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type,omitempty"`
	Function scriptedToolFunction `json:"function"`
}

type scriptedToolFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type scriptedOllamaChatEvent struct {
	Model              string                `json:"model"`
	Message            scriptedOllamaMessage `json:"message"`
	Done               bool                  `json:"done"`
	DoneReason         string                `json:"done_reason,omitempty"`
	PromptEvalCount    int                   `json:"prompt_eval_count,omitempty"`
	EvalCount          int                   `json:"eval_count,omitempty"`
	LoadDuration       int64                 `json:"load_duration,omitempty"`
	PromptEvalDuration int64                 `json:"prompt_eval_duration,omitempty"`
	EvalDuration       int64                 `json:"eval_duration,omitempty"`
	TotalDuration      int64                 `json:"total_duration,omitempty"`
}

// ScriptedOllama serves only the Ollama endpoints used by the evaluation
// inference path. Replies are consumed once, in order, whichever served model
// tag each request names; an extra turn fails the owning test rather than
// inventing a response. The load step the Operator sends before each turn
// makes the model resident with its context window and consumes no reply.
type ScriptedOllama struct {
	server   *httptest.Server
	mu       sync.Mutex
	replies  []OllamaReply
	requests []json.RawMessage
	next     int
	served   map[string]string
	// resident maps each resident model to the context window it loaded with.
	resident      map[string]int64
	loads         []string
	chatResidency []OllamaChatResidency
	released      []string
	nextReplyGate *OllamaReplyGate
}

// OllamaChatResidency is what the provider held when one /api/chat request
// arrived: the requested model and the other models already resident.
type OllamaChatResidency struct {
	Model string
	// Others lists the resident models besides Model, sorted.
	Others []string
}

// OllamaReplyGate blocks one chat reply after the scripted request arrives.
// The test owns Release; closing the HTTP request context also unblocks it.
type OllamaReplyGate struct {
	requested chan struct{}
	release   chan struct{}
	once      sync.Once
}

// Requested signals that the held /api/chat request arrived.
func (g *OllamaReplyGate) Requested() <-chan struct{} { return g.requested }

// Release allows the held request to receive its already-scripted response.
func (g *OllamaReplyGate) Release() { g.once.Do(func() { close(g.release) }) }

// NewScriptedOllama starts a frozen Ollama-compatible HTTP server.
func NewScriptedOllama(t *testing.T, replies []OllamaReply) *ScriptedOllama {
	t.Helper()
	frozen := make([]OllamaReply, len(replies))
	for i, reply := range replies {
		frozen[i] = reply
		frozen[i].ToolCalls = append([]OllamaToolCall(nil), reply.ToolCalls...)
		for j := range frozen[i].ToolCalls {
			frozen[i].ToolCalls[j].Arguments = append(json.RawMessage(nil), reply.ToolCalls[j].Arguments...)
		}
	}
	p := &ScriptedOllama{
		replies:  frozen,
		served:   map[string]string{evalModelTag: evalModelDigest},
		resident: map[string]int64{},
	}
	p.server = httptest.NewServer(p.serveHTTP(t))
	t.Cleanup(p.server.Close)
	return p
}

// Endpoint is the base URL accepted by the real inference Operator.
func (p *ScriptedOllama) Endpoint() string { return p.server.URL }

// ServeModel adds one model tag, with its digest, to the tags the provider
// answers for. A heterogeneous campaign serves each formation variant's tag so
// every stack's governed inference resolves to a served model.
func (p *ScriptedOllama) ServeModel(tag, digest string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.served[tag] = digest
}

func (p *ScriptedOllama) isServed(tag string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.served[tag]
	return ok
}

// ServedDigest reports the digest the provider serves for tag, and whether it
// serves tag at all. A test compares a governed freeze against it.
func (p *ScriptedOllama) ServedDigest(tag string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	digest, ok := p.served[tag]
	return digest, ok
}

// ChatCount reports the number of scripted chat turns consumed.
func (p *ScriptedOllama) ChatCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.next
}

// ChatRequests returns the exact /api/chat bodies the Inference Operator sent,
// in arrival order, so a test can assert the model input on the wire.
func (p *ScriptedOllama) ChatRequests() []json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]json.RawMessage(nil), p.requests...)
}

// ChatResidency returns the provider residency each /api/chat request found
// on arrival, in arrival order, so a test can assert which models shared the
// provider with each turn.
func (p *ScriptedOllama) ChatResidency() []OllamaChatResidency {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]OllamaChatResidency(nil), p.chatResidency...)
}

// scriptedLoopBytes bounds a Loop turn's stream. It is far past the Inference
// Operator's repetition window, so a stream still running at the bound means
// the stop never fired.
const scriptedLoopBytes = 1 << 20

// streamLoop streams one runaway generation: the scripted tool calls, then
// text repeated back to back with no done event, until the client disconnects.
func streamLoop(t *testing.T, w http.ResponseWriter, r *http.Request, model string, calls []scriptedToolCall, text string) {
	t.Helper()
	encoder := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	if len(calls) > 0 {
		_ = encoder.Encode(scriptedOllamaChatEvent{Model: model, Message: scriptedOllamaMessage{Role: "assistant", ToolCalls: calls}})
	}
	for sent := 0; sent < scriptedLoopBytes; sent += len(text) {
		if r.Context().Err() != nil {
			return
		}
		if encoder.Encode(scriptedOllamaChatEvent{Model: model, Message: scriptedOllamaMessage{Role: "assistant", Content: text}}) != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	t.Errorf("scripted Ollama: a looping %q turn streamed %d bytes without being stopped", model, scriptedLoopBytes)
}

// LoadedModels returns every model tag the Operator's load step made
// resident, in load order. A model already resident is not loaded again.
func (p *ScriptedOllama) LoadedModels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.loads...)
}

// ReleasedModels returns every model tag a keep-alive 0 release unloaded, in
// release order.
func (p *ScriptedOllama) ReleasedModels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.released...)
}

// HoldNextReply blocks the next /api/chat request until the returned gate is
// released or that request's context is cancelled.
func (p *ScriptedOllama) HoldNextReply() (*OllamaReplyGate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.nextReplyGate != nil {
		return nil, errors.New("scripted Ollama already has a held reply")
	}
	gate := &OllamaReplyGate{requested: make(chan struct{}), release: make(chan struct{})}
	p.nextReplyGate = gate
	return gate, nil
}

func (p *ScriptedOllama) serveHTTP(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tags":
			p.mu.Lock()
			tags := make([]struct {
				Name   string `json:"name"`
				Digest string `json:"digest"`
			}, 0, len(p.served))
			for name, digest := range p.served {
				tags = append(tags, struct {
					Name   string `json:"name"`
					Digest string `json:"digest"`
				}{Name: name, Digest: digest})
			}
			p.mu.Unlock()
			_ = json.NewEncoder(w).Encode(struct {
				Models []struct {
					Name   string `json:"name"`
					Digest string `json:"digest"`
				} `json:"models"`
			}{Models: tags})
		case r.Method == http.MethodPost && r.URL.Path == "/api/show":
			var request struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !p.isServed(request.Name) {
				http.Error(w, "unexpected model metadata request", http.StatusBadRequest)
				return
			}
			show := scriptedOllamaShow{Parameters: "num_ctx 8192\n", Capabilities: []string{"completion", "tools"}}
			show.Details.Format = "gguf"
			show.Details.Family = "qwen3"
			show.Details.ParameterSize = "4.0B"
			show.Details.QuantizationLevel = "Q4_K_M"
			_ = json.NewEncoder(w).Encode(show)
		case r.Method == http.MethodGet && r.URL.Path == "/api/ps":
			type residentModel struct {
				Name          string `json:"name"`
				ContextLength int64  `json:"context_length"`
			}
			p.mu.Lock()
			models := []residentModel{}
			for name, window := range p.resident {
				models = append(models, residentModel{Name: name, ContextLength: window})
			}
			p.mu.Unlock()
			_ = json.NewEncoder(w).Encode(struct {
				Models []residentModel `json:"models"`
			}{Models: models})
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "unreadable chat request", http.StatusBadRequest)
				return
			}
			var request OllamaChatRequest
			if err := json.Unmarshal(body, &request); err != nil {
				http.Error(w, "invalid chat request", http.StatusBadRequest)
				return
			}
			if !p.isServed(request.Model) {
				t.Errorf("scripted Ollama: unexpected chat model %q", request.Model)
				http.Error(w, "unexpected model", http.StatusBadRequest)
				return
			}
			if request.isModelLoad() {
				window := int64(evalContextWindow)
				if request.Options.NumCtx != nil {
					window = *request.Options.NumCtx
				}
				p.mu.Lock()
				p.resident[request.Model] = window
				p.loads = append(p.loads, request.Model)
				p.mu.Unlock()
				_ = json.NewEncoder(w).Encode(scriptedOllamaChatEvent{Model: request.Model, Done: true, DoneReason: "load"})
				return
			}
			p.mu.Lock()
			if p.next >= len(p.replies) {
				p.mu.Unlock()
				t.Errorf("scripted Ollama: unexpected chat request after %d frozen replies", p.ChatCount())
				http.Error(w, "no scripted reply", http.StatusInternalServerError)
				return
			}
			reply := p.replies[p.next]
			p.requests = append(p.requests, body)
			p.next++
			others := []string{}
			for name := range p.resident {
				if name != request.Model {
					others = append(others, name)
				}
			}
			slices.Sort(others)
			p.chatResidency = append(p.chatResidency, OllamaChatResidency{Model: request.Model, Others: others})
			if _, ok := p.resident[request.Model]; !ok {
				t.Errorf("scripted Ollama: chat for %q arrived before its load", request.Model)
			}
			gate := p.nextReplyGate
			p.nextReplyGate = nil
			p.mu.Unlock()
			if reply.Respond != nil {
				reply = reply.withResponse(reply.Respond(request))
			}
			if gate != nil {
				close(gate.requested)
				select {
				case <-gate.release:
				case <-r.Context().Done():
					return
				}
			}
			if reply.BeforeReply != nil {
				reply.BeforeReply()
			}
			if reply.Failure != "" {
				status := reply.FailureStatus
				if status == 0 {
					status = http.StatusInternalServerError
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(struct {
					Error string `json:"error"`
				}{Error: reply.Failure})
				return
			}
			calls := make([]scriptedToolCall, 0, len(reply.ToolCalls))
			for _, call := range reply.ToolCalls {
				calls = append(calls, scriptedToolCall{ID: call.ID, Type: call.Type, Function: scriptedToolFunction{Name: call.Name, Arguments: call.Arguments}})
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			if reply.Loop != "" {
				streamLoop(t, w, r, request.Model, calls, reply.Loop)
				return
			}
			_ = json.NewEncoder(w).Encode(scriptedOllamaChatEvent{
				Model:   request.Model,
				Message: scriptedOllamaMessage{Role: "assistant", Content: reply.Content, Thinking: reply.Thinking, ToolCalls: calls},
				Done:    true, DoneReason: "stop", PromptEvalCount: 1, EvalCount: 1,
				LoadDuration: 1, PromptEvalDuration: 1, EvalDuration: 1, TotalDuration: 1,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/generate":
			var request struct {
				Model     string          `json:"model"`
				KeepAlive json.RawMessage `json:"keep_alive"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, "invalid generate request", http.StatusBadRequest)
				return
			}
			if !p.isServed(request.Model) || string(request.KeepAlive) != "0" {
				t.Errorf("scripted Ollama: unexpected release request model=%q keep_alive=%s", request.Model, request.KeepAlive)
				http.Error(w, "unexpected release request", http.StatusBadRequest)
				return
			}
			p.mu.Lock()
			delete(p.resident, request.Model)
			p.released = append(p.released, request.Model)
			p.mu.Unlock()
			_ = json.NewEncoder(w).Encode(struct {
				Model      string `json:"model"`
				Done       bool   `json:"done"`
				DoneReason string `json:"done_reason"`
			}{Model: request.Model, Done: true, DoneReason: "unload"})
		default:
			t.Errorf("scripted Ollama: unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}
