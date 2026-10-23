// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/inference/provider_observer"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type stubProviderBoundaryOperatorLister struct {
	operators []*operatorv1.OperatorDocument
	err       error
}

func (s *stubProviderBoundaryOperatorLister) ListOperatorsForObservation(context.Context) ([]*operatorv1.OperatorDocument, error) {
	return s.operators, s.err
}

type mutableProviderBoundaryOperatorLister struct {
	operators []*operatorv1.OperatorDocument
}

func (l *mutableProviderBoundaryOperatorLister) ListOperatorsForObservation(context.Context) ([]*operatorv1.OperatorDocument, error) {
	return l.operators, nil
}

func TestProviderBoundaryObservationCoordinator_EnsureObserver_SubscribesToOwnerScopedObserver(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := provider_observer.NewWindowStore(fileSvc)
	require.NoError(t, err)
	pubsubHandler := NewGatewayWebSocketHandler(logger)

	coordinator := NewProviderBoundaryObservationCoordinator(
		&DispatchService{},
		&stubProviderBoundaryOperatorLister{operators: []*operatorv1.OperatorDocument{
			{
				Id:                "observer-1",
				OperatorSessionId: "sess-observer-1",
				Status:            string(constants.OperatorStatusActive),
				OperatorType:      string(constants.OperatorTypeRemote),
				RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
			},
		}},
		pubsubHandler,
		windowStore,
		nil,
		logger,
	)

	require.NoError(t, coordinator.synchronizeObserverSubscription(context.Background()))
	assert.NotNil(t, coordinator.observer)
	assert.Equal(t, "sess-observer-1", coordinator.observer.OperatorSessionID)
}

func TestProviderBoundaryObservationCoordinator_NotifyAttemptBegin_FailsWithoutCmdSubscriber(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := provider_observer.NewWindowStore(fileSvc)
	require.NoError(t, err)
	op := &operatorv1.OperatorDocument{
		Id:                "observer-1",
		OperatorSessionId: "sess-observer-1",
	}
	dispatchSvc, pubsubHandler := newTestDispatchService(t, "root-abc", op)

	coordinator := NewProviderBoundaryObservationCoordinator(
		dispatchSvc,
		&stubProviderBoundaryOperatorLister{operators: []*operatorv1.OperatorDocument{
			{
				Id:                op.Id,
				OperatorSessionId: op.OperatorSessionId,
				Status:            string(constants.OperatorStatusActive),
				OperatorType:      string(constants.OperatorTypeRemote),
				RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
			},
		}},
		pubsubHandler,
		windowStore,
		nil,
		logger,
	)

	_, err = coordinator.NotifyAttemptBegin(context.Background(), "user-1", "attempt-1", time.Now().UnixMilli(), 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrDispatchNoDelivery)
}

func TestProviderBoundaryObservationCoordinator_PreflightCommandDelivery_FailsWithoutCmdSubscriber(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := provider_observer.NewWindowStore(fileSvc)
	require.NoError(t, err)
	pubsubHandler := NewGatewayWebSocketHandler(logger)

	coordinator := NewProviderBoundaryObservationCoordinator(
		&DispatchService{pubsub: pubsubHandler},
		&stubProviderBoundaryOperatorLister{operators: []*operatorv1.OperatorDocument{
			{
				Id:                "observer-1",
				OperatorSessionId: "sess-observer-1",
				Status:            string(constants.OperatorStatusActive),
				OperatorType:      string(constants.OperatorTypeRemote),
				RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
			},
		}},
		pubsubHandler,
		windowStore,
		nil,
		logger,
	)

	err = coordinator.PreflightCommandDelivery(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
}

func TestProviderBoundaryObservationCoordinator_EnsureObserver_ReSubscribesOnSessionChange(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := provider_observer.NewWindowStore(fileSvc)
	require.NoError(t, err)
	pubsubHandler := NewGatewayWebSocketHandler(logger)
	lister := &mutableProviderBoundaryOperatorLister{operators: []*operatorv1.OperatorDocument{
		{
			Id:                "observer-1",
			OperatorSessionId: "sess-observer-old",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
		},
	}}

	coordinator := NewProviderBoundaryObservationCoordinator(
		&DispatchService{},
		lister,
		pubsubHandler,
		windowStore,
		nil,
		logger,
	)

	require.NoError(t, coordinator.synchronizeObserverSubscription(context.Background()))
	require.NotNil(t, coordinator.observer)
	assert.Equal(t, "sess-observer-old", coordinator.observer.OperatorSessionID)

	lister.operators = []*operatorv1.OperatorDocument{
		{
			Id:                "observer-2",
			OperatorSessionId: "sess-observer-new",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
		},
	}

	require.NoError(t, coordinator.synchronizeObserverSubscription(context.Background()))
	require.NotNil(t, coordinator.observer)
	assert.Equal(t, "sess-observer-new", coordinator.observer.OperatorSessionID)

	window := &evalv1.ProviderBoundaryObservationWindow{
		SchemaVersion:              provider_observer.SchemaVersion,
		ProviderAttemptId:          "attempt-session-switch",
		ObserverId:                 "observer-test",
		ObserverClockSource:        provider_observer.DefaultObserverClockSource,
		WindowStartedAtUnixNanos:   uint64(time.Unix(1_700_000_000, 0).UnixNano()),
		WindowCompletedAtUnixNanos: uint64(time.Unix(1_700_000_010, 0).UnixNano()),
		AttemptStartedAtUnixMs:     time.Unix(1_700_000_000, 0).UnixMilli(),
		AttemptCompletedAtUnixMs:   time.Unix(1_700_000_010, 0).UnixMilli(),
		Samples: []*evalv1.ProviderBoundaryHardwareSample{{
			ObservedAtUnixNanos: uint64(time.Unix(1_700_000_001, 0).UnixNano()),
			HostRamAvailability: evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED,
			HostRamUsedBytes:    1,
			HostRamTotalBytes:   2,
		}},
	}
	digest, err := provider_observer.ComputeObservationDigest(window)
	require.NoError(t, err)
	window.ObservationDigest = digest

	completion := &evalv1.ProviderBoundaryObservationCompleted{Window: window}
	payload, err := proto.Marshal(completion)
	require.NoError(t, err)
	env := &commonv1.GovernanceEnvelope{
		EventType: string(constants.Event.Operator.ProviderBoundaryObservation.Completed),
		Payload:   payload,
	}
	wire, err := protojson.Marshal(env)
	require.NoError(t, err)

	pubsubHandler.Publish(pubsub.ResultsChannel("observer-2", "sess-observer-new"), wire)

	loaded, err := windowStore.Load(context.Background(), "attempt-session-switch")
	require.NoError(t, err)
	assert.Equal(t, "attempt-session-switch", loaded.GetProviderAttemptId())
}

func TestProviderBoundaryObservationCoordinator_EnsureObserver_LogsNotFound(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := provider_observer.NewWindowStore(fileSvc)
	require.NoError(t, err)

	coordinator := NewProviderBoundaryObservationCoordinator(
		&DispatchService{},
		&stubProviderBoundaryOperatorLister{operators: []*operatorv1.OperatorDocument{
			{
				Id:                "data-1",
				OperatorSessionId: "sess-data-1",
				Status:            string(constants.OperatorStatusActive),
				OperatorType:      string(constants.OperatorTypeRemote),
				RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}},
			},
		}},
		NewGatewayWebSocketHandler(logger),
		windowStore,
		nil,
		logger,
	)

	err = coordinator.synchronizeObserverSubscription(context.Background())
	require.Error(t, err)
	assert.Nil(t, coordinator.observer)
}

// duplicateObserverOperators models a re-enrolled observer: two remote sessions
// with the observer flag and the same hardware fingerprint, plus an inference
// node on a different host whose fingerprint matches neither.
func duplicateObserverOperators() []*operatorv1.OperatorDocument {
	observer := func(id, session string) *operatorv1.OperatorDocument {
		return &operatorv1.OperatorDocument{
			Id:                id,
			OperatorSessionId: session,
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			SystemFingerprint: "fp-observer-host",
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
		}
	}
	return []*operatorv1.OperatorDocument{
		observer("observer-old", "sess-observer-old"),
		observer("observer-new", "sess-observer-new"),
		{
			Id:                "inference-1",
			OperatorSessionId: "sess-inference-1",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			SystemFingerprint: "fp-inference-host",
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}},
		},
	}
}

func newDuplicateObserverCoordinator(t *testing.T) (*ProviderBoundaryObservationCoordinator, *GatewayWebSocketHandler) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := provider_observer.NewWindowStore(fileSvc)
	require.NoError(t, err)
	pubsubHandler := NewGatewayWebSocketHandler(logger)
	coordinator := NewProviderBoundaryObservationCoordinator(
		&DispatchService{pubsub: pubsubHandler},
		&stubProviderBoundaryOperatorLister{operators: duplicateObserverOperators()},
		pubsubHandler,
		windowStore,
		nil,
		logger,
	)
	return coordinator, pubsubHandler
}

func TestProviderBoundaryObservationCoordinator_EnsureObserver_ReportsAmbiguousWhenFingerprintTiebreakFails(t *testing.T) {
	coordinator, _ := newDuplicateObserverCoordinator(t)

	err := coordinator.synchronizeObserverSubscription(context.Background())
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrProviderBoundaryObserverAmbiguous)
	require.NotErrorIs(t, err, constants.ErrProviderBoundaryObserverNotFound)
	assert.Nil(t, coordinator.observer)
}

func TestProviderBoundaryObservationCoordinator_EnsureObserver_FingerprintBreaksTieBetweenLiveObservers(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := provider_observer.NewWindowStore(fileSvc)
	require.NoError(t, err)
	pubsubHandler := NewGatewayWebSocketHandler(logger)

	operators := duplicateObserverOperators()
	operators[1].SystemFingerprint = "fp-inference-host"
	coordinator := NewProviderBoundaryObservationCoordinator(
		&DispatchService{pubsub: pubsubHandler},
		&stubProviderBoundaryOperatorLister{operators: operators},
		pubsubHandler,
		windowStore,
		nil,
		logger,
	)

	require.NoError(t, coordinator.synchronizeObserverSubscription(context.Background()))
	require.NotNil(t, coordinator.observer)
	assert.Equal(t, "observer-new", coordinator.observer.OperatorID)
	assert.Equal(t, "sess-observer-new", coordinator.observer.OperatorSessionID)
}

func TestProviderBoundaryObservationCoordinator_IngestAfterDispatchContextCanceled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := provider_observer.NewWindowStore(fileSvc)
	require.NoError(t, err)
	pubsubHandler := NewGatewayWebSocketHandler(logger)

	announcer := &recordingEvidenceAnnouncer{}
	coordinator := NewProviderBoundaryObservationCoordinator(
		&DispatchService{},
		&stubProviderBoundaryOperatorLister{operators: []*operatorv1.OperatorDocument{
			{
				Id:                "observer-1",
				OperatorSessionId: "sess-observer-1",
				UserId:            "owner-1",
				Status:            string(constants.OperatorStatusActive),
				OperatorType:      string(constants.OperatorTypeRemote),
				RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
			},
		}},
		pubsubHandler,
		windowStore,
		announcer,
		logger,
	)

	dispatchCtx, cancel := context.WithCancel(context.Background())
	require.NoError(t, coordinator.synchronizeObserverSubscription(dispatchCtx))
	cancel()

	window := &evalv1.ProviderBoundaryObservationWindow{
		SchemaVersion:              provider_observer.SchemaVersion,
		ProviderAttemptId:          "attempt-async-1",
		ObserverId:                 "observer-test",
		ObserverClockSource:        provider_observer.DefaultObserverClockSource,
		WindowStartedAtUnixNanos:   uint64(time.Unix(1_700_000_000, 0).UnixNano()),
		WindowCompletedAtUnixNanos: uint64(time.Unix(1_700_000_010, 0).UnixNano()),
		AttemptStartedAtUnixMs:     time.Unix(1_700_000_000, 0).UnixMilli(),
		AttemptCompletedAtUnixMs:   time.Unix(1_700_000_010, 0).UnixMilli(),
		Samples: []*evalv1.ProviderBoundaryHardwareSample{{
			ObservedAtUnixNanos: uint64(time.Unix(1_700_000_001, 0).UnixNano()),
			HostRamAvailability: evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED,
			HostRamUsedBytes:    1,
			HostRamTotalBytes:   2,
		}},
	}
	digest, err := provider_observer.ComputeObservationDigest(window)
	require.NoError(t, err)
	window.ObservationDigest = digest

	completion := &evalv1.ProviderBoundaryObservationCompleted{Window: window}
	payload, err := proto.Marshal(completion)
	require.NoError(t, err)
	env := &commonv1.GovernanceEnvelope{
		EventType: string(constants.Event.Operator.ProviderBoundaryObservation.Completed),
		Payload:   payload,
	}
	wire, err := protojson.Marshal(env)
	require.NoError(t, err)

	resultsChannel := pubsub.ResultsChannel("observer-1", "sess-observer-1")
	pubsubHandler.Publish(resultsChannel, wire)

	loaded, err := windowStore.Load(context.Background(), "attempt-async-1")
	require.NoError(t, err)
	assert.Equal(t, "attempt-async-1", loaded.GetProviderAttemptId())
	assert.Equal(t, []string{string(constants.EventProviderBoundaryObservationRecorded) + "/owner-1/attempt-async-1"}, announcer.recorded)
}

func TestProviderBoundaryObservationCoordinator_PreflightCommandDelivery_EmbeddedObserverUsesProcessor(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := provider_observer.NewWindowStore(fileSvc)
	require.NoError(t, err)
	pubsubHandler := NewGatewayWebSocketHandler(logger)
	lister := &stubProviderBoundaryOperatorLister{operators: []*operatorv1.OperatorDocument{
		{
			Id:                string(constants.DocIDEmbeddedOperator),
			OperatorSessionId: "sess-embedded",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeEmbedded),
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
		},
	}}
	processor := embeddedEnvelopeProcessorFunc(func(context.Context, []byte) (*operatorv1.ActionReceipt, error) {
		return &operatorv1.ActionReceipt{}, nil
	})

	withProcessor := NewProviderBoundaryObservationCoordinator(&DispatchService{pubsub: pubsubHandler, embeddedProcessor: processor}, lister, pubsubHandler, windowStore, nil, logger)
	require.NoError(t, withProcessor.PreflightCommandDelivery(context.Background()))

	withoutProcessor := NewProviderBoundaryObservationCoordinator(&DispatchService{pubsub: pubsubHandler}, lister, pubsubHandler, windowStore, nil, logger)
	err = withoutProcessor.PreflightCommandDelivery(context.Background())
	assert.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
}
