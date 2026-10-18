// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gwremote

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// startProvenanceGateway starts a fake Gateway whose responses come from handler,
// points the CLI endpoint override at it, and forces the health check healthy.
func startProvenanceGateway(t *testing.T, handler http.HandlerFunc) (*provenanceGateway, fs.RuntimeFileService, *config.Config) {
	t.Helper()
	WithGatewayHealthCheck(t, true)
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	gateway := &provenanceGateway{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway.record(r.Clone(r.Context()))
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	cmdtest.WithEndpointOverride(t, server.URL)
	return gateway, fileSvc, cfg
}

func TestPreflightModelProvenanceDelivery(t *testing.T) {
	t.Run("rejects an unhealthy gateway without any network call", func(t *testing.T) {
		WithGatewayHealthCheck(t, false)
		fileSvc, cfg := cmdtest.NewCmdTestEnv(t)

		err := preflightModelProvenanceDelivery(fileSvc, cfg)
		require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	})

	t.Run("accepts a ready gateway and hits the preflight endpoint", func(t *testing.T) {
		gateway, fileSvc, cfg := startProvenanceGateway(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(t, w, `{"status":"ready"}`)
		})

		require.NoError(t, preflightModelProvenanceDelivery(fileSvc, cfg))
		require.Equal(t, 1, gateway.requestCount())
		assert.Equal(t, http.MethodGet, gateway.requests[0].Method)
		assert.Equal(t, provenancePreflightPath, gateway.requests[0].URL.Path)
	})

	tests := []struct {
		name        string
		handler     http.HandlerFunc
		wantErrIs   error
		wantMessage string
	}{
		{
			name: "non-ready status is rejected with the reported status",
			handler: func(w http.ResponseWriter, r *http.Request) {
				writeJSONBody(t, w, `{"status":"degraded"}`)
			},
			wantMessage: `unexpected status "degraded"`,
		},
		{
			name: "missing status is rejected",
			handler: func(w http.ResponseWriter, r *http.Request) {
				writeJSONBody(t, w, `{}`)
			},
			wantMessage: `unexpected status ""`,
		},
		{
			name: "response of the wrong JSON shape is an invalid JSON response",
			handler: func(w http.ResponseWriter, r *http.Request) {
				writeJSONBody(t, w, `[]`)
			},
			wantErrIs: constants.ErrInvalidJSONResponse,
		},
		{
			name: "gateway HTTP error is wrapped with preflight context",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				writeJSONBody(t, w, `{"error":"provenance operator offline"}`)
			},
			wantErrIs:   constants.ErrHTTPStatusError,
			wantMessage: "model provenance preflight",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, fileSvc, cfg := startProvenanceGateway(t, tt.handler)

			err := preflightModelProvenanceDelivery(fileSvc, cfg)
			require.Error(t, err)
			if tt.wantErrIs != nil {
				assert.ErrorIs(t, err, tt.wantErrIs)
			}
			if tt.wantMessage != "" {
				assert.Contains(t, err.Error(), tt.wantMessage)
			}
		})
	}
}

func TestPreflightModelProvenanceAttestation(t *testing.T) {
	const tag = "probe-model:7b"
	digest := strings.Repeat("c", 64)

	t.Run("rejects an unhealthy gateway", func(t *testing.T) {
		WithGatewayHealthCheck(t, false)
		fileSvc, cfg := cmdtest.NewCmdTestEnv(t)

		err := PreflightModelProvenanceAttestation(fileSvc, cfg, tag, digest)
		require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	})

	t.Run("sends the tag and digest as escaped query parameters", func(t *testing.T) {
		gateway, fileSvc, cfg := startProvenanceGateway(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(t, w, `{"status":"ready"}`)
		})
		trickyTag := "registry.example/team model:7b&x=1"

		require.NoError(t, PreflightModelProvenanceAttestation(fileSvc, cfg, trickyTag, digest))

		queries := gateway.attestQueries()
		require.Len(t, queries, 1)
		assert.Equal(t, trickyTag, queries[0]["served_model_tag"], "tag must round-trip through URL encoding intact")
		assert.Equal(t, digest, queries[0]["expected_model_digest"])
	})

	tests := []struct {
		name        string
		handler     http.HandlerFunc
		wantErrIs   error
		wantMessage string
	}{
		{
			name: "non-ready status names the model tag",
			handler: func(w http.ResponseWriter, r *http.Request) {
				writeJSONBody(t, w, `{"status":"digest_mismatch"}`)
			},
			wantMessage: `for "probe-model:7b": unexpected status "digest_mismatch"`,
		},
		{
			name: "response of the wrong JSON shape is an invalid JSON response",
			handler: func(w http.ResponseWriter, r *http.Request) {
				writeJSONBody(t, w, `"ready"`)
			},
			wantErrIs: constants.ErrInvalidJSONResponse,
		},
		{
			name: "gateway HTTP error names the model tag",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				writeJSONBody(t, w, `{"error":"upstream"}`)
			},
			wantErrIs:   constants.ErrHTTPStatusError,
			wantMessage: `attestation preflight for "probe-model:7b"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, fileSvc, cfg := startProvenanceGateway(t, tt.handler)

			err := PreflightModelProvenanceAttestation(fileSvc, cfg, tag, digest)
			require.Error(t, err)
			if tt.wantErrIs != nil {
				assert.ErrorIs(t, err, tt.wantErrIs)
			}
			if tt.wantMessage != "" {
				assert.Contains(t, err.Error(), tt.wantMessage)
			}
		})
	}
}

func TestPreflightModelProvenanceContext_SSEReadyBeforeDispatch(t *testing.T) {
	events := make(chan string, 2)
	connected := make(chan struct{})
	_, fileSvc, cfg := startProvenanceGateway(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case constants.APIPaths.SSEStream:
			w.Header().Set("Content-Type", "text/event-stream")
			require.NotEmpty(t, r.Header.Get(constants.HeaderCLISessionID))
			// Mirror SSEController: the subscription is live before the
			// header flush that clients treat as stream readiness.
			close(connected)
			w.(http.Flusher).Flush()
			for {
				select {
				case data := <-events:
					fmt.Fprintf(w, "data: %s\n\n", data)
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		case provenanceAttestPath:
			select {
			case <-connected:
			default:
				t.Error("attestation dispatched before SSE was ready")
			}
			requestID := r.URL.Query().Get("request_id")
			require.NotEmpty(t, requestID)
			events <- fmt.Sprintf(`{"event":{"type":%q,"data":{"request_id":"unrelated","phase":"failed"}}}`, constants.EventModelProvenancePreflightProgress)
			events <- fmt.Sprintf(`{"event":{"type":%q,"data":{"request_id":%q,"served_model_tag":"probe-model:7b","phase":"attesting_storage"}}}`, constants.EventModelProvenancePreflightProgress, requestID)
			<-r.Context().Done()
		default:
			writeJSONBody(t, w, `{"status":"ready"}`)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var phases []string
	err := preflightModelProvenanceAttestationContext(ctx, fileSvc, cfg, "probe-model:7b", strings.Repeat("a", 64), func(event models.ModelProvenancePreflightProgress) {
		phases = append(phases, event.Phase)
		cancel()
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, []string{"attesting_storage"}, phases)
}
