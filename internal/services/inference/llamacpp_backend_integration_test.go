// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package inference

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestLlamaCppBackend_RouterStateFailsClosed(t *testing.T) {
	for name, body := range map[string]string{
		"missing data":    `{}`,
		"null data":       `{"data":null}`,
		"missing status":  `{"data":[{"id":"qwen3"}]}`,
		"unknown status":  `{"data":[{"id":"qwen3","status":{"value":"unknown"}}]}`,
		"duplicate model": `{"data":[{"id":"qwen3","status":{"value":"unloaded"}},{"id":"qwen3","status":{"value":"loaded"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/models", r.URL.Path, "invalid state must be rejected before unloading")
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(server.Close)
			backend, err := NewLlamaCppBackend(server.URL, testutil.TempDir(t), testutil.NewTestLogger())
			require.NoError(t, err)
			ctx := testutil.Context(t)
			_, err = backend.Status(ctx)
			require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
			_, err = backend.ReadResidency(ctx)
			require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
			require.ErrorIs(t, backend.ReleaseModels(ctx, []string{"qwen3"}), constants.ErrInferenceProviderResponseInvalid)
		})
	}
}

func TestLlamaCppBackend_RejectsDigestDriftDuringProviderCall(t *testing.T) {
	for _, decision := range []bool{false, true} {
		name := "generate"
		if decision {
			name = "decide"
		}
		t.Run(name, func(t *testing.T) {
			root := testutil.TempDir(t)
			digest := writeLlamaCppTestModel(t, root, "qwen3.gguf", []byte("before"))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := os.WriteFile(filepath.Join(root, "qwen3.gguf"), []byte("after"), constants.PermFilePrivate); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if decision {
					assert.Equal(t, "/v1/systemone", r.URL.Path)
					_, _ = w.Write([]byte(`{"model":"qwen3","answers":{"safe":{"type":"noul","noul":0.99}},"usage":{"input_tokens":12,"output_tokens":1}}`))
				} else {
					assert.Equal(t, "/v1/chat/completions", r.URL.Path)
					_, _ = w.Write([]byte(llamaCppTestStream))
				}
			}))
			t.Cleanup(server.Close)
			backend, err := NewLlamaCppBackend(server.URL, root, testutil.NewTestLogger())
			require.NoError(t, err)
			ctx := testutil.Context(t)
			if decision {
				response, err := backend.Decide(ctx, models.DecisionRequest{Model: "qwen3", ModelDigest: digest, Input: decisionInput()})
				require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
				assert.Nil(t, response)
			} else {
				response, err := backend.Generate(ctx, models.GenerateRequest{Model: "qwen3", ModelDigest: digest, Messages: textInferenceMessages("hi")})
				require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
				assert.Nil(t, response)
			}
		})
	}
}

func TestLlamaCppModelDigester_RehashesSameSizeWriteWithRestoredMtime(t *testing.T) {
	root := testutil.TempDir(t)
	path := filepath.Join(root, "m.gguf")
	first := writeLlamaCppTestModel(t, root, "m.gguf", []byte("first"))
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))
	digester := newLlamaCppModelDigester(root)
	// Advance the injected clock beyond the settle window without sleeping.
	digester.now = func() time.Time { return time.Now().Add(time.Hour) }
	_, digest, err := digester.modelDigest(testutil.Context(t), "m")
	require.NoError(t, err)
	require.Equal(t, first, digest)
	ctx := testutil.Context(t)
	// Repeat the write until the filesystem's change-time tick advances.
	// The injected future clock above deliberately bypasses the settle window.
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		if !assert.NoError(collect, os.WriteFile(path, []byte("other"), constants.PermFilePrivate)) {
			return
		}
		if !assert.NoError(collect, os.Chtimes(path, old, old)) {
			return
		}
		_, digest, err := digester.modelDigest(ctx, "m")
		assert.NoError(collect, err)
		assert.Equal(collect, models.SHA256Hex([]byte("other")), digest, "restoring mtime must not conceal changed weights")
	}, time.Second, time.Millisecond)
}

func TestLlamaCppModelDigester_CanceledContextDoesNotReturnCachedDigest(t *testing.T) {
	root := testutil.TempDir(t)
	writeLlamaCppTestModel(t, root, "m.gguf", []byte("weights"))
	digester := newLlamaCppModelDigester(root)
	digester.now = func() time.Time { return time.Now().Add(time.Hour) }
	_, _, err := digester.modelDigest(testutil.Context(t), "m")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(testutil.Context(t))
	cancel()
	_, digest, err := digester.modelDigest(ctx, "m")
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, digest)
}

// llamaCppTestGGUF is a minimal GGUF v3 header naming the model architecture.
func llamaCppTestGGUF(architecture string) []byte {
	var out bytes.Buffer
	writeString := func(value string) {
		_ = binary.Write(&out, binary.LittleEndian, uint64(len(value)))
		out.WriteString(value)
	}
	out.WriteString("GGUF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(3))
	_ = binary.Write(&out, binary.LittleEndian, uint64(0)) // tensors
	_ = binary.Write(&out, binary.LittleEndian, uint64(2)) // key-value pairs
	writeString("general.architecture")
	_ = binary.Write(&out, binary.LittleEndian, uint32(8)) // string
	writeString(architecture)
	writeString(architecture + ".context_length")
	_ = binary.Write(&out, binary.LittleEndian, uint32(4)) // uint32
	_ = binary.Write(&out, binary.LittleEndian, uint32(32768))
	return out.Bytes()
}

// llamaCppFakeRouter is an in-process llama-server router: a fixed model
// list with mutable status, a scripted chat stream, and a record of calls.
type llamaCppFakeRouter struct {
	mu       sync.Mutex
	statuses map[string]string
	order    []string
	stream   string
	calls    []string
	chatBody llamaCppChatRequest
}

func newLlamaCppFakeRouter(models ...string) *llamaCppFakeRouter {
	router := &llamaCppFakeRouter{statuses: make(map[string]string)}
	for _, model := range models {
		router.statuses[model] = "unloaded"
		router.order = append(router.order, model)
	}
	return router
}

func (f *llamaCppFakeRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	switch r.URL.Path {
	case "/models":
		listed := llamaCppModelsResponse{}
		for _, id := range f.order {
			listed.Data = append(listed.Data, llamaCppRouterModel{ID: id, Status: llamaCppModelStatus{Value: f.statuses[id]}})
		}
		_ = json.NewEncoder(w).Encode(listed)
	case "/models/unload":
		var request llamaCppModelRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if _, ok := f.statuses[request.Model]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.statuses[request.Model] = "unloaded"
		_, _ = w.Write([]byte(`{"success":true}`))
	case "/v1/chat/completions":
		_ = json.NewDecoder(r.Body).Decode(&f.chatBody)
		model := f.chatBody.Model
		f.statuses[model] = "loaded"
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(f.stream))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *llamaCppFakeRouter) recordedCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func newLlamaCppTestBackend(t *testing.T, router *llamaCppFakeRouter) (*LlamaCppBackend, string) {
	t.Helper()
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	root := testutil.TempDir(t)
	backend, err := NewLlamaCppBackend(server.URL, root, testutil.NewTestLogger())
	require.NoError(t, err)
	return backend, root
}

func writeLlamaCppTestModel(t *testing.T, root, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), constants.PermDirPrivate))
	require.NoError(t, os.WriteFile(path, body, constants.PermFilePrivate))
	return models.SHA256Hex(body)
}

const llamaCppTestStream = "data: {\"model\":\"qwen3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n" +
	"data: {\"model\":\"qwen3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"model\":\"qwen3\",\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2},\"timings\":{\"cache_n\":3,\"prompt_ms\":4,\"predicted_ms\":8}}\n\n" +
	"data: [DONE]\n\n"

func TestLlamaCppBackend_GenerateVerifiesDigestAndUnloadsOnKeepAliveZero(t *testing.T) {
	router := newLlamaCppFakeRouter("qwen3")
	router.stream = llamaCppTestStream
	backend, root := newLlamaCppTestBackend(t, router)
	digest := writeLlamaCppTestModel(t, root, "qwen3.gguf", llamaCppTestGGUF("qwen3"))

	resp, err := backend.Generate(testutil.Context(t), models.GenerateRequest{
		Model:       "qwen3",
		ModelDigest: digest,
		KeepAlive:   "0",
		Messages:    textInferenceMessages("hi"),
	})
	require.NoError(t, err)
	assert.Equal(t, "hello", resp.Parts[0].GetText())
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, int32(7), resp.PromptTokens)
	assert.Equal(t, int32(2), resp.CompletionTokens)
	assert.Equal(t, int32(9), resp.TotalTokens)
	require.NotNil(t, resp.CacheTokens)
	assert.Equal(t, int32(3), *resp.CacheTokens)
	assert.Equal(t, int64(4_000_000), *resp.PromptEvalDurationNS)
	assert.Equal(t, int64(8_000_000), *resp.GenerationDurationNS)
	assert.NotNil(t, resp.TimeToFirstTokenNS)
	assert.Equal(t, digest, resp.ServedModelDigest)
	assert.NotEmpty(t, resp.NormalizedRequestHash)
	assert.NotEmpty(t, resp.OutputHash)
	assert.True(t, router.chatBody.Stream)
	assert.Zero(t, router.chatBody.Temperature)
	assert.Equal(t, []string{"POST /v1/chat/completions", "POST /models/unload"}, router.recordedCalls())
}

func TestLlamaCppBackend_GenerateFailsClosedBeforeEgress(t *testing.T) {
	router := newLlamaCppFakeRouter("qwen3")
	router.stream = llamaCppTestStream
	backend, root := newLlamaCppTestBackend(t, router)
	writeLlamaCppTestModel(t, root, "qwen3.gguf", llamaCppTestGGUF("qwen3"))

	_, err := backend.Generate(testutil.Context(t), models.GenerateRequest{Model: "qwen3", ModelDigest: models.SHA256Hex([]byte("other")), Messages: textInferenceMessages("hi")})
	require.ErrorIs(t, err, constants.ErrInferenceModelDigestMismatch)
	_, err = backend.Generate(testutil.Context(t), models.GenerateRequest{Model: "absent", ModelDigest: models.SHA256Hex([]byte("other")), Messages: textInferenceMessages("hi")})
	require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
	_, err = backend.Generate(testutil.Context(t), models.GenerateRequest{Model: "qwen3", KeepAlive: "5m", Messages: textInferenceMessages("hi")})
	require.ErrorIs(t, err, constants.ErrInferenceCapabilityUnsupported)
	assert.Empty(t, router.recordedCalls(), "no request leaves before validation and digest verification pass")
}

func TestLlamaCppBackend_GenerateEnforcesContextLimit(t *testing.T) {
	router := newLlamaCppFakeRouter("qwen3")
	router.stream = llamaCppTestStream
	backend, _ := newLlamaCppTestBackend(t, router)
	limit := int32(8)

	_, err := backend.Generate(testutil.Context(t), models.GenerateRequest{Model: "qwen3", ContextLimit: &limit, Messages: textInferenceMessages("hi")})
	require.ErrorIs(t, err, constants.ErrInferenceContextOverflow)
}

func TestLlamaCppBackend_GenerateClassifiesProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"type":"exceed_context_size_error","message":"request exceeds the available context size"}}`))
	}))
	t.Cleanup(server.Close)
	backend, err := NewLlamaCppBackend(server.URL, testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)

	_, err = backend.Generate(testutil.Context(t), models.GenerateRequest{Model: "qwen3", Messages: textInferenceMessages("hi")})
	require.ErrorIs(t, err, constants.ErrInferenceContextOverflow)
}

func TestLlamaCppBackend_InventoryDescribesModelsUnderTheModelRoot(t *testing.T) {
	router := newLlamaCppFakeRouter("qwen3", "preset-only")
	backend, root := newLlamaCppTestBackend(t, router)
	body := llamaCppTestGGUF("qwen3")
	digest := writeLlamaCppTestModel(t, root, filepath.Join("qwen3", "qwen3-q4.gguf"), body)
	writeLlamaCppTestModel(t, root, filepath.Join("qwen3", "mmproj-qwen3.gguf"), []byte("projector"))

	entries, err := backend.ListProviderModelInventory(testutil.Context(t))
	require.NoError(t, err)
	require.Len(t, entries, 1, "a router model outside the model root cannot be digested and is skipped")
	entry := entries[0]
	assert.Equal(t, constants.InferenceProviderLlamaCpp, entry.ProviderClass)
	assert.Equal(t, "qwen3", entry.ServedModelTag)
	assert.Equal(t, digest, entry.ModelDigest)
	assert.Equal(t, "qwen3", entry.ModelFamily)
	assert.Equal(t, constants.ModelWeightFormatGGUF, entry.Format)
	assert.Equal(t, uint64(len(body)), entry.ArtifactSizeBytes)
	assert.Equal(t, uint32(32768), entry.ContextLimit)
	assert.Equal(t, []string{"completion", "vision"}, entry.AdvertisedCapabilities)
}

func TestLlamaCppBackend_InventoryWithNoResolvableModelFailsClosed(t *testing.T) {
	backend, _ := newLlamaCppTestBackend(t, newLlamaCppFakeRouter("preset-only"))
	_, err := backend.ListProviderModelInventory(testutil.Context(t))
	require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
}

func TestLlamaCppBackend_ReleaseModelsUnloadsResidentModels(t *testing.T) {
	router := newLlamaCppFakeRouter("qwen3", "gemma", "idle")
	router.statuses["qwen3"] = "loaded"
	router.statuses["gemma"] = "sleeping"
	backend, _ := newLlamaCppTestBackend(t, router)

	residency, err := backend.ReadResidency(testutil.Context(t))
	require.NoError(t, err)
	assert.Equal(t, []ProviderResidencyModel{{Name: "qwen3"}, {Name: "gemma"}}, residency.Models)

	require.NoError(t, backend.ReleaseModels(testutil.Context(t), []string{"qwen3", "gemma", "idle"}))
	assert.Equal(t, []string{"GET /models", "GET /models", "POST /models/unload", "POST /models/unload", "GET /models"}, router.recordedCalls())

	residency, err = backend.ReadResidency(testutil.Context(t))
	require.NoError(t, err)
	assert.Empty(t, residency.Models)
}

func TestLlamaCppBackend_ReleaseModelsFailsClosed(t *testing.T) {
	router := newLlamaCppFakeRouter("qwen3")
	backend, _ := newLlamaCppTestBackend(t, router)

	require.ErrorIs(t, backend.ReleaseModels(testutil.Context(t), nil), constants.ErrInferenceModelTagInvalid)
	require.ErrorIs(t, backend.ReleaseModels(testutil.Context(t), []string{"qwen3;whoami"}), constants.ErrInferenceModelTagInvalid)
	require.ErrorIs(t, backend.ReleaseModels(testutil.Context(t), []string{"absent"}), constants.ErrInferenceModelNotFound)
	assert.NotContains(t, router.recordedCalls(), "POST /models/unload")
}

func TestLlamaCppModelDigester_CachesSettledFilesAndRehashesChanges(t *testing.T) {
	root := testutil.TempDir(t)
	path := filepath.Join(root, "m.gguf")
	first := writeLlamaCppTestModel(t, root, "m.gguf", []byte("first"))
	settled := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(path, settled, settled))
	digester := newLlamaCppModelDigester(root)
	digester.now = func() time.Time { return time.Now().Add(time.Hour) }

	_, digest, err := digester.modelDigest(testutil.Context(t), "m")
	require.NoError(t, err)
	assert.Equal(t, first, digest)
	assert.Contains(t, digester.cached, path, "a file older than the settle window is cached")

	digester.now = time.Now
	second := writeLlamaCppTestModel(t, root, "m.gguf", []byte("second"))
	_, digest, err = digester.modelDigest(testutil.Context(t), "m")
	require.NoError(t, err)
	assert.Equal(t, second, digest, "a changed file identity is re-hashed")
	assert.NotContains(t, digester.cached, path, "a file changed within the settle window is not cached")
}

func TestLlamaCppModelDigester_ComposesSplitModelDigest(t *testing.T) {
	root := testutil.TempDir(t)
	firstShard := writeLlamaCppTestModel(t, root, "big-00001-of-00002.gguf", []byte("a"))
	secondShard := writeLlamaCppTestModel(t, root, "big-00002-of-00002.gguf", []byte("b"))
	digester := newLlamaCppModelDigester(root)

	model, digest, err := digester.modelDigest(testutil.Context(t), "big-00001-of-00002")
	require.NoError(t, err)
	assert.Len(t, model.Shards, 2)
	assert.Equal(t, models.SHA256Hex([]byte(firstShard+"\n"+secondShard)), digest)

	_, _, err = digester.modelDigest(testutil.Context(t), "absent")
	require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
}
