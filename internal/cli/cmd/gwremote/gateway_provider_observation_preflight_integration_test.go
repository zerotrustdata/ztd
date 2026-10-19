// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gwremote

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func TestPreflightProviderObservationDelivery_AgainstGateway(t *testing.T) {
	preflightPath := constants.APIPaths.InferenceProviderObservations + "_preflight"

	t.Run("accepts a ready gateway and hits the preflight endpoint", func(t *testing.T) {
		gateway, fileSvc, cfg := startObservationPreflightGateway(t, func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(t, w, `{"status":"ready"}`)
		})

		require.NoError(t, PreflightProviderObservationDelivery(fileSvc, cfg))
		require.Equal(t, 2, gateway.requestCount())
		assert.Equal(t, http.MethodGet, gateway.requests[0].Method)
		assert.Equal(t, constants.APIPaths.Health, gateway.requests[0].URL.Path)
		assert.Equal(t, http.MethodGet, gateway.requests[1].Method)
		assert.Equal(t, preflightPath, gateway.requests[1].URL.Path)
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
				writeJSONBody(t, w, `{"status":"stalled"}`)
			},
			wantMessage: `provider observation preflight: unexpected status "stalled"`,
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
				w.WriteHeader(http.StatusInternalServerError)
				writeJSONBody(t, w, `{"error":"observer offline"}`)
			},
			wantErrIs:   constants.ErrHTTPStatusError,
			wantMessage: "provider observation preflight",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, fileSvc, cfg := startObservationPreflightGateway(t, tt.handler)

			err := PreflightProviderObservationDelivery(fileSvc, cfg)
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

// observationPreflightGateway records requests to the Gateway.
type observationPreflightGateway struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (g *observationPreflightGateway) record(r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.requests = append(g.requests, r)
}

func (g *observationPreflightGateway) requestCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.requests)
}

func writeJSONBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	_, err := w.Write([]byte(body))
	require.NoError(t, err)
}

// startObservationPreflightGateway starts a fake Gateway whose configured health response
// is healthy and whose preflight response comes from handler.
func startObservationPreflightGateway(t *testing.T, handler http.HandlerFunc) (*observationPreflightGateway, fs.RuntimeFileService, *config.Config) {
	t.Helper()
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	gateway := &observationPreflightGateway{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gateway.record(r.Clone(r.Context()))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == constants.APIPaths.Health {
			writeJSONBody(t, w, `{"status":"gateway","mode":"gateway","version":"test","posture":"doctrine"}`)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	cmdtest.WithEndpointOverride(t, server.URL)
	return gateway, fileSvc, cfg
}
