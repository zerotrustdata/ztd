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

// updateRunStateAgainst posts a running update to a server that answers with
// status and body.
func updateRunStateAgainst(t *testing.T, body string) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, constants.APIPaths.ObserveProducerRunState, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	client, err := New(config.Config{MTLSBaseURL: server.URL})
	require.NoError(t, err)

	return client.UpdateRunState(context.Background(), models.ObserveProducerRunStateRequest{
		SchemaVersion: constants.ObserveEventPayloadSchemaVersion,
		RunID:         "run-a-1",
		RunKind:       models.RunKindEval,
		DisplayName:   "run-a-1",
		Status:        models.RunLifecycleStatusRunning,
		ObservedAt:    time.Now().UTC(),
		CLISessionID:  "cli-1",
	})
}

func TestUpdateRunState_RejectionCarriesTheRefusedTransition(t *testing.T) {
	rejection, err := json.Marshal(models.NewObserveProducerRejection(&models.ObserveTransitionError{
		Kind: models.ObserveProjectionKindRun, ID: "run-a-1",
		From: string(models.RunLifecycleStatusCancelled), To: string(models.RunLifecycleStatusRunning),
	}))
	require.NoError(t, err)

	err = updateRunStateAgainst(t, string(rejection))
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
	assert.ErrorIs(t, err, constants.ErrObserveInvalidTransition)
	var rejected *ObserveProducerRejectedError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, http.StatusBadRequest, rejected.Status)
	var transition *models.ObserveTransitionError
	require.ErrorAs(t, err, &transition)
	assert.Equal(t, string(models.RunLifecycleStatusCancelled), transition.From)
	assert.Equal(t, string(models.RunLifecycleStatusRunning), transition.To)
}

func TestUpdateRunState_RejectionCodeRecoversTheSentinel(t *testing.T) {
	err := updateRunStateAgainst(t, `{"error":"observe: run_kind is required","code":"run_kind_invalid"}`)
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
	assert.ErrorIs(t, err, constants.ErrObserveRunKindRequired)
}

func TestUpdateRunState_UnknownRejectionCodeIsNamed(t *testing.T) {
	err := updateRunStateAgainst(t, `{"error":"new","code":"from_the_future"}`)
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
	assert.ErrorIs(t, err, constants.ErrObserveRejectionUnknown)
}

func TestUpdateRunState_UndecodableRejectionIsAnInvalidResponse(t *testing.T) {
	err := updateRunStateAgainst(t, `not json`)
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
	assert.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
}
