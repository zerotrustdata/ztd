// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func observeGatewayRunEvents(t *testing.T, env *runEnv, runID string) <-chan runEvent {
	t.Helper()
	cfg, err := env.deps.configLoader(env.root)
	require.NoError(t, err)
	fileSvc := env.fileSvc(t)
	authContext, err := env.deps.authLoader(fileSvc, cfg)
	require.NoError(t, err)
	httpClient, err := auth.BuildMTLSClient(fileSvc, cfg, 0)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(testVersionContext(), 2*time.Minute)
	t.Cleanup(cancel)
	events := make(chan runEvent, 16)
	stream, err := subscribeRunEvents(ctx, httpClient, cfg.OperatorPublicURL(), authContext.CLISessionID, runID, events, func() {})
	require.NoError(t, err)
	t.Cleanup(stream.Close)
	return events
}

func awaitGatewayRunEvent(t *testing.T, events <-chan runEvent) runEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a Gateway run event")
		return runEvent{}
	}
}

func requireNoGatewayRunEvent(t *testing.T, events <-chan runEvent) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected Gateway run event: %+v", event)
	case <-time.After(150 * time.Millisecond):
	}
}

func gatewayEvalAppClient(t *testing.T, env *runEnv, run *realStackRun) interface {
	UpdateRunState(context.Context, models.ObserveProducerRunStateRequest) error
	RequestRunStop(context.Context, models.ObserveProducerStopRequest) error
} {
	t.Helper()
	authContext, err := env.deps.authLoader(run.fileSvc, run.cfg)
	require.NoError(t, err)
	appClient, err := inferenceEvalAppClient(run.ctx, io.Discard, env.deps, run.fileSvc, run.cfg, authContext)
	require.NoError(t, err)
	return appClient
}

func publishGatewayRunState(t *testing.T, env *runEnv, run *realStackRun, runID string, status models.RunLifecycleStatus) {
	t.Helper()
	store := env.store(t)
	persisted, err := store.LoadRun(run.ctx, runID)
	require.NoError(t, err)
	startedAt := persisted.GetStartedAt().AsTime().UTC()
	endedAt := time.Now().UTC()
	var ended *time.Time
	if status != models.RunLifecycleStatusRunning && status != models.RunLifecycleStatusQueued {
		ended = &endedAt
	}
	assignments, err := store.ListAssignments(run.ctx, runID)
	require.NoError(t, err)
	client := gatewayEvalAppClient(t, env, run)
	err = client.UpdateRunState(run.ctx, models.ObserveProducerRunStateRequest{
		SchemaVersion:  constants.ObserveEventPayloadSchemaVersion,
		RunID:          runID,
		RunKind:        models.RunKindEval,
		DisplayName:    runID,
		Status:         status,
		CompletedTasks: 0,
		TotalTasks:     len(assignments),
		StartedAt:      &startedAt,
		EndedAt:        ended,
		ObservedAt:     time.Now().UTC(),
		CLISessionID:   mustGatewayCLISession(t, env, run),
	})
	require.NoError(t, err)
}

func mustGatewayCLISession(t *testing.T, env *runEnv, run *realStackRun) string {
	t.Helper()
	authContext, err := env.deps.authLoader(run.fileSvc, run.cfg)
	require.NoError(t, err)
	return authContext.CLISessionID
}
