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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/inference/model_provenance"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func testModelProvenanceWindow(t *testing.T, providerAttemptID string) *evalv1.ModelProvenanceAttestationWindow {
	t.Helper()
	digest := strings.Repeat("a", 64)
	window := &evalv1.ModelProvenanceAttestationWindow{
		SchemaVersion:              model_provenance.SchemaVersion,
		ProviderAttemptId:          providerAttemptID,
		ProvenanceOperatorId:       "prov-1",
		ServedModelTag:             "probe-model:7b",
		ExpectedModelDigest:        digest,
		ObservedModelDigest:        digest,
		ManifestDigest:             strings.Repeat("b", 64),
		ManifestVerificationStatus: evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_UNSIGNED,
		AttestedAtUnixMs:           1_700_000_000_000,
		DigestMatch:                true,
	}
	attestationDigest, err := model_provenance.ComputeAttestationDigest(window)
	require.NoError(t, err)
	window.AttestationDigest = attestationDigest
	return window
}

type stubModelProvenanceOperatorLister struct {
	operators []*operatorv1.OperatorDocument
	err       error
}

func (s *stubModelProvenanceOperatorLister) ListOperatorsForProvenance(context.Context) ([]*operatorv1.OperatorDocument, error) {
	return s.operators, s.err
}

func TestModelProvenanceObservationCoordinator_EnsureOperator_SubscribesToProvenanceOperator(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := model_provenance.NewWindowStore(fileSvc)
	require.NoError(t, err)
	pubsubHandler := NewGatewayWebSocketHandler(logger)

	coordinator := NewModelProvenanceObservationCoordinator(
		&DispatchService{},
		&stubModelProvenanceOperatorLister{operators: []*operatorv1.OperatorDocument{
			{
				Id:                "prov-1",
				OperatorSessionId: "sess-prov-1",
				Status:            string(constants.OperatorStatusActive),
				OperatorType:      string(constants.OperatorTypeRemote),
				RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityProvenance)}},
			},
		}},
		pubsubHandler,
		windowStore,
		nil,
		logger,
	)

	assert.NoError(t, coordinator.synchronizeOperatorSubscription(context.Background()))
	assert.NotNil(t, coordinator.operator)
	assert.Equal(t, "sess-prov-1", coordinator.operator.OperatorSessionID)
}

func TestModelProvenanceObservationCoordinator_IngestPersistsAttestationWindow(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := model_provenance.NewWindowStore(fileSvc)
	require.NoError(t, err)
	pubsubHandler := NewGatewayWebSocketHandler(logger)

	announcer := &recordingEvidenceAnnouncer{}
	coordinator := NewModelProvenanceObservationCoordinator(
		&DispatchService{},
		&stubModelProvenanceOperatorLister{operators: []*operatorv1.OperatorDocument{
			{
				Id:                "prov-1",
				OperatorSessionId: "sess-prov-1",
				UserId:            "owner-1",
				Status:            string(constants.OperatorStatusActive),
				OperatorType:      string(constants.OperatorTypeRemote),
				RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityProvenance)}},
			},
		}},
		pubsubHandler,
		windowStore,
		announcer,
		logger,
	)

	require.NoError(t, coordinator.synchronizeOperatorSubscription(context.Background()))
	window := testModelProvenanceWindow(t, "attempt-async-1")
	completion := &evalv1.ModelProvenanceObservationCompleted{Window: window}
	payload, err := proto.Marshal(completion)
	require.NoError(t, err)
	env := &commonv1.GovernanceEnvelope{
		EventType: string(constants.Event.Operator.ModelProvenanceObservation.Completed),
		Payload:   payload,
	}
	wire, err := protojson.Marshal(env)
	require.NoError(t, err)

	pubsubHandler.Publish(pubsub.ResultsChannel("prov-1", "sess-prov-1"), wire)

	loaded, err := windowStore.Load(context.Background(), "attempt-async-1")
	require.NoError(t, err)
	assert.Equal(t, "attempt-async-1", loaded.GetProviderAttemptId())
	assert.Equal(t, []string{string(constants.EventModelProvenanceAttestationRecorded) + "/owner-1/attempt-async-1"}, announcer.recorded)
}

type recordingEvidenceAnnouncer struct{ recorded []string }

func (r *recordingEvidenceAnnouncer) WindowRecorded(_ context.Context, eventType constants.EventType, userID, providerAttemptID string) {
	r.recorded = append(r.recorded, string(eventType)+"/"+userID+"/"+providerAttemptID)
}

func TestModelProvenanceObservationCoordinator_NotifyAttemptBegin_FailsWithoutCmdSubscriber(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	windowStore, err := model_provenance.NewWindowStore(fileSvc)
	require.NoError(t, err)
	op := &operatorv1.OperatorDocument{
		Id:                "prov-1",
		OperatorSessionId: "sess-prov-1",
	}
	dispatchSvc, pubsubHandler := newTestDispatchService(t, "root-abc", op)

	coordinator := NewModelProvenanceObservationCoordinator(
		dispatchSvc,
		&stubModelProvenanceOperatorLister{operators: []*operatorv1.OperatorDocument{
			{
				Id:                op.Id,
				OperatorSessionId: op.OperatorSessionId,
				Status:            string(constants.OperatorStatusActive),
				OperatorType:      string(constants.OperatorTypeRemote),
				RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityProvenance)}},
			},
		}},
		pubsubHandler,
		windowStore,
		nil,
		logger,
	)

	err = coordinator.NotifyAttemptBegin(
		context.Background(),
		"user-1",
		"attempt-1",
		"probe-model:7b",
		strings.Repeat("a", 64),
		strings.Repeat("c", 64),
		"campaign-1",
		time.Now().UnixMilli(),
		0,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrDispatchNoDelivery)
}
