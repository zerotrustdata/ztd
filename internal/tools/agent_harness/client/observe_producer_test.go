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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
)

func TestRequestRunStop_PostsTheTypedRequestAsAnAppWorkload(t *testing.T) {
	want := models.ObserveProducerStopRequest{
		SchemaVersion: constants.ObserveEventPayloadSchemaVersion,
		CampaignID:    "eval-a",
		RunID:         "run-a-1",
		StopReason:    models.StopReasonGraceful,
		StopScope:     models.StopScopeCycle,
		CLISessionID:  "cli-1",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, constants.APIPaths.ObserveProducerStop, r.URL.Path)
		assert.Empty(t, r.Header.Get(constants.HeaderCLISessionID), "the producer is an app workload, so no CLI session header")
		var got models.ObserveProducerStopRequest
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		assert.Equal(t, want, got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer server.Close()
	client, err := New(config.Config{MTLSBaseURL: server.URL})
	require.NoError(t, err)

	require.NoError(t, client.RequestRunStop(context.Background(), want))
}

func TestUpdateRunState_NonSuccessIsAnHTTPStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, constants.APIPaths.ObserveProducerRunState, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	client, err := New(config.Config{MTLSBaseURL: server.URL})
	require.NoError(t, err)

	err = client.UpdateRunState(context.Background(), models.ObserveProducerRunStateRequest{
		SchemaVersion: constants.ObserveEventPayloadSchemaVersion,
		RunID:         "run-a-1",
		RunKind:       models.RunKindEval,
		DisplayName:   "run-a-1",
		Status:        models.RunLifecycleStatusRunning,
		ObservedAt:    time.Now().UTC(),
		CLISessionID:  "cli-1",
	})
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
}
