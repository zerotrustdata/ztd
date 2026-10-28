// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package pubsub

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestPubSubResultsService_RejectsNonPubSubTransportEvents(t *testing.T) {
	svc, client, _ := newResultsTestService(t)
	ctx := t.Context()

	t.Run("rejects local checkpoint event on publishUniversal", func(t *testing.T) {
		env := &commonv1.GovernanceEnvelope{
			Id:        "test-env-local",
			EventType: string(constants.EventPlatformAuditChainCheckpointed),
		}
		err := svc.publishUniversal(ctx, env, "op-1", "sess-1")
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
		assert.Zero(t, client.PublishedCount())
	})

	t.Run("rejects local consensus vote recorded event on publishUniversal", func(t *testing.T) {
		env := &commonv1.GovernanceEnvelope{
			Id:        "test-env-vote",
			EventType: string(constants.EventConsensusVoteRecorded),
		}
		err := svc.publishUniversal(ctx, env, "op-1", "sess-1")
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
		assert.Zero(t, client.PublishedCount())
	})

	t.Run("rejects sse-only agent status updated event on publishUniversal", func(t *testing.T) {
		env := &commonv1.GovernanceEnvelope{
			Id:        "test-env-sse",
			EventType: string(constants.EventAppAgentStatusUpdated),
		}
		err := svc.publishUniversal(ctx, env, "op-1", "sess-1")
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
		assert.Zero(t, client.PublishedCount())
	})

	t.Run("rejects undeclared event on publishUniversal", func(t *testing.T) {
		env := &commonv1.GovernanceEnvelope{
			Id:        "test-env-unknown",
			EventType: "ztd.v1.nonexistent.event",
		}
		err := svc.publishUniversal(ctx, env, "op-1", "sess-1")
		require.ErrorIs(t, err, constants.ErrTxUnknownEventType)
		assert.Zero(t, client.PublishedCount())
	})
}

func TestBuildUniversalResultEnvelope_RejectsNonPubSubTransportOutcome(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	scrubber := newEgressScrubber(t, true)
	ctx := t.Context()

	payload := &operatorv1.CommandResult{
		ExecutionId: "exec-1",
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
	}

	t.Run("rejects local event as outcome", func(t *testing.T) {
		_, err := BuildUniversalResultEnvelope(
			ctx,
			cfg,
			constants.Event.Operator.Command.Requested,
			constants.EventPlatformAuditChainCheckpointed,
			constants.ActionTypeExecuteBash,
			payload,
			scrubber,
			"msg-1",
			"op-1",
			"case-1",
			"inv-1",
			nil,
			"",
			"",
		)
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
	})

	t.Run("rejects sse-only event as outcome", func(t *testing.T) {
		_, err := BuildUniversalResultEnvelope(
			ctx,
			cfg,
			constants.Event.Operator.Command.Requested,
			constants.EventAppAgentStatusUpdated,
			constants.ActionTypeExecuteBash,
			payload,
			scrubber,
			"msg-1",
			"op-1",
			"case-1",
			"inv-1",
			nil,
			"",
			"",
		)
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
	})
}

func TestPublishLFEA_RejectsNonPubSubTransportEvents(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	client := pubsubtest.NewMockOperatorPubSubClient()
	eventsStore := storagetest.NewTestEventsStore(t)
	scrubber := newEgressScrubber(t, true)
	ctx := t.Context()

	msg := &PubSubCommandMessage{
		ID:                "msg-1",
		EventType:         constants.Event.Operator.Command.Requested,
		CaseID:            "case-1",
		InvestigationID:   "investigation-1",
		OperatorSessionID: "session-1",
	}

	payload := &operatorv1.CommandResult{
		ExecutionId: "exec-1",
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
	}

	t.Run("publishLFEATypedResponseTo rejects local event", func(t *testing.T) {
		err := publishLFEATypedResponseTo(ctx, client, cfg, logger, msg, constants.EventPlatformAuditChainCheckpointed, payload, eventsStore, scrubber)
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
		assert.Zero(t, client.PublishedCount())
	})

	t.Run("publishLFEATypedResponseTo rejects sse-only event", func(t *testing.T) {
		err := publishLFEATypedResponseTo(ctx, client, cfg, logger, msg, constants.EventAppAgentStatusUpdated, payload, eventsStore, scrubber)
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
		assert.Zero(t, client.PublishedCount())
	})

	t.Run("publishLFEAErrorTo rejects local event", func(t *testing.T) {
		err := publishLFEAErrorTo(ctx, client, cfg, logger, msg, constants.EventPlatformAuditChainCheckpointed, "something failed", eventsStore, scrubber)
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
		assert.Zero(t, client.PublishedCount())
	})

	t.Run("publishLFEAErrorTo rejects sse-only event", func(t *testing.T) {
		err := publishLFEAErrorTo(ctx, client, cfg, logger, msg, constants.EventAppAgentStatusUpdated, "something failed", eventsStore, scrubber)
		require.ErrorIs(t, err, constants.ErrEventTransportViolation)
		assert.Zero(t, client.PublishedCount())
	})
}
