// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestSubmitEnvelope_ReturnsTypedOutcomeAndRecordsPath(t *testing.T) {
	tests := []struct {
		name         string
		responseCode int
		responseBody []byte
		wantKind     evalv1.ConformanceObservationKind
	}{
		{
			name:         "accepted receipt",
			responseCode: http.StatusOK,
			responseBody: canonicalBody(t, outcomeReceipt("tx-1")),
			wantKind:     evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		},
		{
			name:         "governance rejection",
			responseCode: http.StatusForbidden,
			responseBody: canonicalBody(t, outcomeRejection("tx-1")),
			wantKind:     evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
		},
		{
			name:         "ingress refusal",
			responseCode: http.StatusBadRequest,
			responseBody: canonicalBody(t, outcomeRefusal(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST)),
			wantKind:     evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
		},
		{
			name:         "server error text is untyped",
			responseCode: http.StatusInternalServerError,
			responseBody: []byte(`{"error": "internal error"}`),
			wantKind:     evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != constants.APIPaths.GovernanceEnvelopes {
					t.Errorf("expected path %s, got %s", constants.APIPaths.GovernanceEnvelopes, r.URL.Path)
				}

				w.WriteHeader(tt.responseCode)
				_, _ = w.Write(tt.responseBody)
			})

			server := httptest.NewServer(handler)
			defer server.Close()

			client, err := New(config.Config{MTLSBaseURL: server.URL, Auth: config.Auth{}})
			require.NoError(t, err)

			outcome, err := client.SubmitEnvelope(context.Background(), Persona{ID: "test-client"}, outcomeEnvelope())
			require.NoError(t, err)
			assert.Equal(t, tt.wantKind, outcome.Observation.GetKind())
			assert.Equal(t, tt.responseBody, outcome.Body)
		})
	}
}

func TestSubmitMaximal(t *testing.T) {
	tests := []struct {
		name         string
		maximal      MaximalEnvelope
		responseCode int
		responseBody []byte
		wantKind     evalv1.ConformanceObservationKind
		verifyFields bool
	}{
		{
			name: "minimal maximal envelope",
			maximal: MaximalEnvelope{
				OperatorID:     "op-123",
				ToolName:       "test-tool",
				ArgumentsJSON:  `{"arg":"value"}`,
				TargetResource: "localhost",
				StateRoot:      "root-abc",
			},
			responseCode: http.StatusOK,
			responseBody: canonicalBody(t, outcomeReceipt("tx-1")),
			wantKind:     evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
			verifyFields: true,
		},
		{
			name: "with custom TTL",
			maximal: MaximalEnvelope{
				OperatorID:     "op-123",
				ToolName:       "test-tool",
				ArgumentsJSON:  `{"arg":"value"}`,
				TargetResource: "localhost",
				StateRoot:      "root-abc",
				TTL:            10 * time.Minute,
			},
			responseCode: http.StatusOK,
			responseBody: canonicalBody(t, outcomeReceipt("tx-1")),
			wantKind:     evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
			verifyFields: true,
		},
		{
			name: "server error",
			maximal: MaximalEnvelope{
				OperatorID:     "op-123",
				ToolName:       "test-tool",
				ArgumentsJSON:  `{"arg":"value"}`,
				TargetResource: "localhost",
				StateRoot:      "root-abc",
			},
			responseCode: http.StatusInternalServerError,
			responseBody: []byte(`{"error": "internal error"}`),
			wantKind:     evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedEnvelope []byte

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != constants.APIPaths.GovernanceEnvelopes {
					t.Errorf("expected path %s, got %s", constants.APIPaths.GovernanceEnvelopes, r.URL.Path)
				}

				var err error
				receivedEnvelope, err = io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("failed to read body: %v", err)
				}

				w.WriteHeader(tt.responseCode)
				_, _ = w.Write(tt.responseBody)
			})

			server := httptest.NewServer(handler)
			defer server.Close()

			client, err := New(config.Config{MTLSBaseURL: server.URL, Auth: config.Auth{}})
			require.NoError(t, err)

			txHash, outcome, err := client.SubmitMaximal(context.Background(), Persona{ID: "test-client"}, tt.maximal)
			require.NoError(t, err)
			assert.NotEmpty(t, txHash)
			assert.Equal(t, tt.wantKind, outcome.Observation.GetKind())
			assert.Equal(t, tt.responseBody, outcome.Body)

			if tt.verifyFields {
				// Verify the envelope was sent with correct fields
				bodyStr := string(receivedEnvelope)
				if tt.maximal.OperatorID != "" && !contains(bodyStr, tt.maximal.OperatorID) {
					t.Error("envelope should contain OperatorID")
				}
				if tt.maximal.ToolName != "" && !contains(bodyStr, tt.maximal.ToolName) {
					t.Error("envelope should contain ToolName")
				}
				if tt.maximal.StateRoot != "" && !contains(bodyStr, tt.maximal.StateRoot) {
					t.Error("envelope should contain StateRoot")
				}
			}
		})
	}
}

func TestSubmitMaximal_DefaultTTL(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(canonicalBody(t, outcomeReceipt("tx-1")))
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := config.Config{
		MTLSBaseURL: server.URL,
		Auth:        config.Auth{},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	p := Persona{ID: "test-client"}

	maximal := MaximalEnvelope{
		OperatorID:     "op-123",
		ToolName:       "test-tool",
		ArgumentsJSON:  `{"arg":"value"}`,
		TargetResource: "localhost",
		StateRoot:      "root-abc",
		TTL:            0, // Should use default 5 minutes
	}

	txHash, _, err := client.SubmitMaximal(ctx, p, maximal)

	if err != nil {
		t.Fatalf("SubmitMaximal() failed: %v", err)
	}

	if txHash == "" {
		t.Error("txHash should not be empty")
	}
}
