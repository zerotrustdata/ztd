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
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

const (
	evalModelTag    = "qwen3:4b"
	evalModelDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// OllamaToolCall is one frozen tool call returned by the scripted provider.
type OllamaToolCall struct {
	ID        string          `json:"id,omitempty"`
	Type      string          `json:"type,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// OllamaReply is one complete response to an Ollama /api/chat request.
type OllamaReply struct {
	Content   string           `json:"content,omitempty"`
	Thinking  string           `json:"thinking,omitempty"`
	ToolCalls []OllamaToolCall `json:"tool_calls,omitempty"`
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
// inference path. Replies are consumed once, in order; an extra turn fails
// the owning test rather than inventing a response.
type ScriptedOllama struct {
	server   *httptest.Server
	mu       sync.Mutex
	replies  []OllamaReply
	next     int
	resident bool
}

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
	p := &ScriptedOllama{replies: frozen}
	p.server = httptest.NewServer(http.HandlerFunc(p.serveHTTP(t)))
	t.Cleanup(p.server.Close)
	return p
}

// Endpoint is the base URL accepted by the real inference Operator.
func (p *ScriptedOllama) Endpoint() string { return p.server.URL }

// ChatCount reports the number of scripted chat turns consumed.
func (p *ScriptedOllama) ChatCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.next
}

func (p *ScriptedOllama) serveHTTP(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tags":
			_ = json.NewEncoder(w).Encode(struct {
				Models []struct {
					Name   string `json:"name"`
					Digest string `json:"digest"`
				} `json:"models"`
			}{Models: []struct {
				Name   string `json:"name"`
				Digest string `json:"digest"`
			}{{Name: evalModelTag, Digest: evalModelDigest}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/ps":
			p.mu.Lock()
			resident := p.resident
			p.mu.Unlock()
			models := []struct {
				Name string `json:"name"`
			}{}
			if resident {
				models = append(models, struct {
					Name string `json:"name"`
				}{Name: evalModelTag})
			}
			_ = json.NewEncoder(w).Encode(struct {
				Models []struct {
					Name string `json:"name"`
				} `json:"models"`
			}{Models: models})
		case r.Method == http.MethodPost && r.URL.Path == "/api/chat":
			var request struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, "invalid chat request", http.StatusBadRequest)
				return
			}
			if request.Model != evalModelTag {
				t.Errorf("scripted Ollama: unexpected chat model %q", request.Model)
				http.Error(w, "unexpected model", http.StatusBadRequest)
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
			p.next++
			p.resident = true
			p.mu.Unlock()
			calls := make([]scriptedToolCall, 0, len(reply.ToolCalls))
			for _, call := range reply.ToolCalls {
				calls = append(calls, scriptedToolCall{ID: call.ID, Type: call.Type, Function: scriptedToolFunction{Name: call.Name, Arguments: call.Arguments}})
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			_ = json.NewEncoder(w).Encode(scriptedOllamaChatEvent{
				Model:   evalModelTag,
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
			if request.Model != evalModelTag || string(request.KeepAlive) != "0" {
				t.Errorf("scripted Ollama: unexpected release request model=%q keep_alive=%s", request.Model, request.KeepAlive)
				http.Error(w, "unexpected release request", http.StatusBadRequest)
				return
			}
			p.mu.Lock()
			p.resident = false
			p.mu.Unlock()
			_ = json.NewEncoder(w).Encode(struct {
				Model      string `json:"model"`
				Done       bool   `json:"done"`
				DoneReason string `json:"done_reason"`
			}{Model: evalModelTag, Done: true, DoneReason: "unload"})
		default:
			t.Errorf("scripted Ollama: unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}
