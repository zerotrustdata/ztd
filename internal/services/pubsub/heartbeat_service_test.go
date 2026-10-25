// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/proto"
)

// mockExecutionHandler is a test-only implementation of ExecutionHandler.
type mockExecutionHandler struct {
	executed                       atomic.Bool
	err                            error
	ExecuteVerifiedTransactionFunc func(ctx context.Context, eventType constants.EventType, cmdMsg governance.CommandMessage) (string, error)
}

func (m *mockExecutionHandler) ExecuteVerifiedTransaction(ctx context.Context, eventType constants.EventType, cmdMsg governance.CommandMessage) (string, error) {
	m.executed.Store(true)
	if m.ExecuteVerifiedTransactionFunc != nil {
		return m.ExecuteVerifiedTransactionFunc(ctx, eventType, cmdMsg)
	}
	return "", m.err
}

// mockResultsPublisher is a simple manual mock for testing
type mockResultsPublisher struct {
	publishHeartbeatCalled bool
	publishHeartbeatError  error
	heartbeats             []proto.Message
	fsListResults          []proto.Message

	inferenceCompletions   []*operatorv1.InferenceCompletion
	inferenceCompletionErr error
}

func (m *mockResultsPublisher) PublishExecutionResult(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	return nil
}

func (m *mockResultsPublisher) PublishCancellationResult(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	return nil
}

func (m *mockResultsPublisher) PublishFileEditResult(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	return nil
}

func (m *mockResultsPublisher) PublishEvaluationWorkspaceSeedResult(ctx context.Context, result *operatorv1.EvaluationWorkspaceSeedResult, originalMsg *PubSubCommandMessage) error {
	return nil
}

func (m *mockResultsPublisher) PublishFsListResult(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	m.fsListResults = append(m.fsListResults, result)
	return nil
}

func (m *mockResultsPublisher) PublishFsGrepResult(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	return nil
}

func (m *mockResultsPublisher) PublishShutdownAcknowledgement(ctx context.Context, result proto.Message, originalMsg *PubSubCommandMessage) error {
	return nil
}

func (m *mockResultsPublisher) PublishInferenceCompletion(ctx context.Context, env *commonv1.GovernanceEnvelope, completion *operatorv1.InferenceCompletion) error {
	m.inferenceCompletions = append(m.inferenceCompletions, completion)
	return m.inferenceCompletionErr
}

func (m *mockResultsPublisher) PublishInferenceProgress(ctx context.Context, originalMsg *PubSubCommandMessage, progress *operatorv1.InferenceProgressEvent) error {
	return nil
}

func (m *mockResultsPublisher) PublishProviderBoundaryObservationCompleted(ctx context.Context, origin *commonv1.GovernanceEnvelope, completion *evalv1.ProviderBoundaryObservationCompleted) error {
	return nil
}

func (m *mockResultsPublisher) PublishModelProvenanceObservationCompleted(ctx context.Context, origin *commonv1.GovernanceEnvelope, completion *evalv1.ModelProvenanceObservationCompleted) error {
	return nil
}

func (m *mockResultsPublisher) PublishExecutionStatus(ctx context.Context, status proto.Message, originalMsg *PubSubCommandMessage) error {
	return nil
}

func (m *mockResultsPublisher) PublishHeartbeat(ctx context.Context, heartbeat proto.Message) error {
	m.publishHeartbeatCalled = true
	m.heartbeats = append(m.heartbeats, heartbeat)
	return m.publishHeartbeatError
}

func (m *mockResultsPublisher) PublishEventReceipt(ctx context.Context, env *commonv1.GovernanceEnvelope, receipt *operatorv1.EventReceipt) error {
	return nil
}

func TestNewHeartbeatService(t *testing.T) {
	t.Run("returns non-nil service with config and logger", func(t *testing.T) {
		t.Parallel()
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		svc := NewHeartbeatService(cfg, logger, nil)
		require.NotNil(t, svc)
		assert.Equal(t, cfg, svc.config)
		assert.Equal(t, logger, svc.logger)
	})
}

func TestHeartbeatService_SetResultsPublisher(t *testing.T) {
	t.Parallel()
	svc := NewHeartbeatService(testutil.NewTestConfig(t), testutil.NewTestLogger(), nil)
	publisher := &mockResultsPublisher{}

	svc.SetResultsPublisher(publisher)
	assert.Same(t, publisher, svc.results)

	svc.SetResultsPublisher(nil)
	assert.Nil(t, svc.results)
}

func TestHeartbeatService_SetContext(t *testing.T) {
	t.Parallel()
	svc := NewHeartbeatService(testutil.NewTestConfig(t), testutil.NewTestLogger(), nil)
	ctx := testutil.Context(t)

	svc.SetContext(ctx)
	assert.Equal(t, ctx, svc.ctx)
}
