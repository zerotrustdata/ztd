// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package inference

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestLlamaCppBackend_GenerateBindsRawResponseWithoutPersistingProviderContent(t *testing.T) {
	const marker = "PRIVATE-RESPONSE-MARKER"
	valid := llamaCppSSE(`{"choices":[{"index":0,"delta":{"content":"`+marker+`"},"finish_reason":"stop"}]}`, `[DONE]`)
	for _, tc := range []struct {
		name   string
		status int
		body   string
		limit  int64
		want   error
	}{
		{"complete", http.StatusOK, valid, defaultMaxResponseBytes, nil},
		{"invalid frame retains tail", http.StatusOK, "data: {\n\n" + valid, defaultMaxResponseBytes, constants.ErrInferenceProviderResponseInvalid},
		{"oversized response is bounded", http.StatusOK, valid, 64, constants.ErrInferenceProviderResponseInvalid},
		{"error body is excluded", http.StatusInternalServerError, `{"error":{"message":"` + marker + `"}}`, defaultMaxResponseBytes, constants.ErrInferenceGenerateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testutil.Context(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			backend, err := NewLlamaCppBackend(server.URL, testutil.TempDir(t), testutil.NewTestLogger())
			require.NoError(t, err)
			backend.maxResponseBytes = tc.limit
			root := testutil.TempDir(t)
			store, err := NewAttemptStore(storagetest.NewTestFileSvc(t, root))
			require.NoError(t, err)
			require.NoError(t, store.Begin(ctx, &operatorv1.InferenceProviderAttemptRecord{ProviderAttemptId: "attempt-llamacpp", TransactionId: "tx-llamacpp"}))
			response, err := backend.Generate(WithAttemptStore(ctx, store), models.GenerateRequest{Model: "m", Messages: textInferenceMessages("hi"), ProviderAttemptID: "attempt-llamacpp"})
			if tc.want == nil {
				require.NoError(t, err)
				assert.Equal(t, marker, response.Parts[0].GetText())
			} else {
				require.ErrorIs(t, err, tc.want)
				assert.Nil(t, response)
				assert.NotContains(t, err.Error(), marker)
			}
			record, err := store.Get(ctx, "attempt-llamacpp")
			require.NoError(t, err)
			if tc.status == http.StatusOK {
				raw := []byte(tc.body)[:min(int64(len(tc.body)), tc.limit+1)]
				assert.Equal(t, models.SHA256Hex(raw), record.GetRawResponseDigest())
				assert.Equal(t, uint64(len(raw)), record.GetRawResponseSizeBytes())
			} else {
				assert.Empty(t, record.GetRawResponseDigest())
				assert.Zero(t, record.GetRawResponseSizeBytes())
			}
			assertNoFileContains(t, root, marker)
		})
	}
}

func TestLlamaCppBackend_GeneratePublishesScrubbedProgressBeforeCompletion(t *testing.T) {
	ctx := testutil.ContextWithTimeout(t, 5*time.Second)
	progress := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(llamaCppSSE(
			`{"choices":[{"index":0,"delta":{"content":"admin@"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"example.com\n"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"lookahead\n"}}]}`,
		)))
		w.(http.Flusher).Flush()
		select {
		case <-progress:
			_, _ = w.Write([]byte(llamaCppSSE(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)))
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	backend, err := NewLlamaCppBackend(server.URL, testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	// Exercise the production scrubber's in-memory mode; this test owns no
	// token persistence boundary and supplies no database substitute.
	scrubber, err := scrubbing.NewScrubbingService(ctx, &scrubbing.Config{Enabled: true}, testutil.NewTestLogger(), nil)
	require.NoError(t, err)
	var events []*operatorv1.InferenceProgressEvent
	observed := sync.OnceFunc(func() { close(progress) })
	ctx = WithProgressReporter(ctx, func(event *operatorv1.InferenceProgressEvent) error {
		events = append(events, event)
		observed()
		return nil
	})
	response, err := backend.Generate(ctx, models.GenerateRequest{Model: "m", Messages: textInferenceMessages("hi"), Stream: true, Scrubber: scrubber, ProviderAttemptID: "attempt-live"})
	require.NoError(t, err, "the provider finishes only after progress is delivered")
	require.NotEmpty(t, events)
	assert.NotContains(t, progressPartsText(events), "admin@example.com")
	assert.Contains(t, progressPartsText(events), "lookahead")
	response.ProviderAttemptID = "attempt-live"
	require.NoError(t, models.ReconcileInferenceProgress(events, response.ToProtoInferenceResult()))
}

func TestLlamaCppProvider_ExecutionHandlerScrubsRequestAndBindsScrubbedResult(t *testing.T) {
	ctx := testutil.Context(t)
	router := newLlamaCppFakeRouter("qwen3")
	router.stream = strings.ReplaceAll(llamaCppTestStream, "hello", "admin@example.com")
	backend, _ := newLlamaCppTestBackend(t, router)
	logger := testutil.NewTestLogger()
	scrubber, err := scrubbing.NewScrubbingService(ctx, &scrubbing.Config{Enabled: true}, logger, nil)
	require.NoError(t, err)
	handler, err := NewInferenceExecutionHandler(backend, &config.Config{Inference: config.InferenceConfig{Enabled: true, KeepAlive: "-1"}}, scrubber, logger)
	require.NoError(t, err)
	payload := mustMarshalInferenceRequested(t, &operatorv1.InferenceRequested{
		RequestSchemaVersion: constants.InferenceRequestSchemaVersion,
		ProviderAttemptId:    "attempt-handler",
		Role:                 operatorv1.ModelRole_MODEL_ROLE_PRIMARY,
		Model:                "qwen3",
		Messages:             textInferenceMessages("admin@example.com"),
	})
	response, err := handler.ExecuteInference(ctx, &testCommandMessage{payload: payload})
	require.NoError(t, err)
	require.Len(t, router.chatBody.Messages, 1)
	assert.NotContains(t, router.chatBody.Messages[0].Content, "admin@example.com")
	assert.NotEmpty(t, router.chatBody.Messages[0].Content)
	require.Len(t, response.Parts, 1)
	assert.NotContains(t, response.Parts[0].GetText(), "admin@example.com")
	assert.NotEmpty(t, response.Parts[0].GetText())
	wantHash, err := models.ComputeInferenceOutputHash(response.Parts, response.FinishReason)
	require.NoError(t, err)
	assert.Equal(t, wantHash, response.OutputHash)
	assert.Equal(t, "attempt-handler", response.ProviderAttemptID)
}

func TestLlamaCppBackend_GenerateRefusesUnscrubbedProgressBeforeEgress(t *testing.T) {
	router := newLlamaCppFakeRouter("qwen3")
	backend, _ := newLlamaCppTestBackend(t, router)
	ctx := WithProgressReporter(testutil.Context(t), func(*operatorv1.InferenceProgressEvent) error {
		t.Error("unscrubbed progress must not be delivered")
		return nil
	})
	response, err := backend.Generate(ctx, models.GenerateRequest{Model: "qwen3", Messages: textInferenceMessages("hi"), Stream: true})
	require.ErrorIs(t, err, constants.ErrEgressScrubberUnavailable)
	assert.Nil(t, response)
	assert.Empty(t, router.recordedCalls())
}

func TestLlamaCppProvider_DecideUsesTypedRequestAndRouterResidency(t *testing.T) {
	root := testutil.TempDir(t)
	digest := writeLlamaCppTestModel(t, root, "qwen3.gguf", []byte("weights"))
	var calls []string
	var request systemOneRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/llama/v1/systemone":
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			_, _ = w.Write([]byte(`{"model":"qwen3","answers":{"safe":{"type":"noul","noul":0.99}},"usage":{"input_tokens":12,"output_tokens":1}}`))
		case "/llama/models/unload":
			var unload llamaCppModelRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&unload))
			assert.Equal(t, "qwen3", unload.Model)
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			t.Errorf("unexpected provider route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	provider, err := NewProvider(config.InferenceConfig{Provider: constants.InferenceProviderLlamaCpp, LlamaCppEndpoint: server.URL + "/llama", LlamaCppModelRoot: root}, testutil.NewTestLogger())
	require.NoError(t, err)
	ctx := testutil.Context(t)
	input := models.DecisionRequest{Model: "qwen3", ModelDigest: digest, Input: decisionInput(), KeepAlive: "0"}
	response, err := provider.Decide(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, []string{"POST /llama/v1/systemone", "POST /llama/models/unload"}, calls)
	assert.Equal(t, "qwen3", request.Model)
	assert.Equal(t, "noul", request.Questions["safe"].Type)
	assert.Empty(t, request.KeepAlive, "residency belongs to the router")
	assert.Equal(t, digest, response.ServedModelDigest)
	assert.EqualValues(t, 13, response.TotalTokens)
	require.NoError(t, models.ValidateDecisionResult(input.Input, response.Decision))
	input.ModelDigest = strings.Repeat("a", 64)
	_, err = provider.Decide(ctx, input)
	require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
	input.ModelDigest = digest
	input.AllowHosted = true
	_, err = provider.Decide(ctx, input)
	require.ErrorIs(t, err, constants.ErrInferenceHostedNotAllowed)
	assert.Len(t, calls, 2, "rejected decisions must not call the provider or unload")
}

func TestLlamaCppBackend_ReleaseRequiresValidAcknowledgementAndFinalState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		final  string
		want   error
	}{
		{"unload rejected", http.StatusInternalServerError, `{}`, "", constants.ErrInferenceModelReleaseFailed},
		{"model disappears before unload", http.StatusNotFound, `{}`, "", constants.ErrInferenceModelNotFound},
		{"negative acknowledgement", http.StatusOK, `{"success":false}`, "", constants.ErrInferenceProviderResponseInvalid},
		{"malformed acknowledgement", http.StatusOK, `{`, "", constants.ErrInferenceProviderResponseInvalid},
		{"missing final state", http.StatusOK, `{"success":true}`, `{}`, constants.ErrInferenceProviderResponseInvalid},
		{"unknown final state", http.StatusOK, `{"success":true}`, `{"data":[{"id":"m","status":{"value":"unknown"}}]}`, constants.ErrInferenceProviderResponseInvalid},
		{"verified unload", http.StatusOK, `{"success":true}`, `{"data":[{"id":"m","status":{"value":"unloaded"}}]}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unloaded := false
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/models":
					reads++
					body := `{"data":[{"id":"m","status":{"value":"loaded"}}]}`
					if unloaded {
						body = tc.final
					}
					_, _ = w.Write([]byte(body))
				case "/models/unload":
					unloaded = true
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			backend, err := NewLlamaCppBackend(server.URL, testutil.TempDir(t), testutil.NewTestLogger())
			require.NoError(t, err)
			err = backend.ReleaseModels(testutil.Context(t), []string{"m"})
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
			assert.True(t, unloaded)
			if tc.final != "" {
				assert.Equal(t, 2, reads, "successful acknowledgement must be followed by residency verification")
			}
		})
	}
}

func TestLlamaCppBackend_StatusAcceptsExplicitEmptyListAndKnownRouterStates(t *testing.T) {
	for name, body := range map[string]string{
		"explicit empty list": `{"data":[]}`,
		"known router states": `{"data":[{"id":"a","status":{"value":"loaded"}},{"id":"b","status":{"value":"loading"}},{"id":"c","status":{"value":"sleeping"}},{"id":"d","status":{"value":"unloaded"}},{"id":"e","status":{"value":"downloading"}},{"id":"f","status":{"value":"downloaded"}},{"id":"g","status":{"value":"incomplete"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			t.Cleanup(server.Close)
			backend, err := NewLlamaCppBackend(server.URL, testutil.TempDir(t), testutil.NewTestLogger())
			require.NoError(t, err)
			status, err := backend.Status(testutil.Context(t))
			require.NoError(t, err)
			assert.True(t, status.Available)
			residency, err := backend.ReadResidency(testutil.Context(t))
			require.NoError(t, err)
			if len(status.Models) == 0 {
				assert.Empty(t, residency.Models)
			} else {
				assert.Len(t, status.Models, 7)
				assert.Equal(t, []ProviderResidencyModel{{Name: "a"}, {Name: "b"}, {Name: "c"}}, residency.Models)
			}
		})
	}
}
