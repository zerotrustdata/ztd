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
	"net/http"
	"net/http/httptest"
	"testing"

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
