// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

type captureEnvelopeSuspender struct{ envelope *commonv1.GovernanceEnvelope }

func (s *captureEnvelopeSuspender) SuspendGovernanceEnvelope(_ context.Context, env *commonv1.GovernanceEnvelope) (models.PlatformEnrollmentL3SuspensionResponse, error) {
	s.envelope = proto.Clone(env).(*commonv1.GovernanceEnvelope)
	return models.PlatformEnrollmentL3SuspensionResponse{}, nil
}

func TestDispatchApprovalCoordinator_CancelledOrExpiredRequestCannotResume(t *testing.T) {
	for _, cancelRequest := range []bool{true, false} {
		name := "approval expiry"
		if cancelRequest {
			name = "caller cancellation"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := &captureEnvelopeSuspender{}
				coordinator, err := NewDispatchApprovalCoordinator(store)
				require.NoError(t, err)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				env := resumeTestEnvelope(t, constants.Event.Operator.FsRead.Requested, fsReadPayload(t))
				done := make(chan error, 1)
				go func() { _, _, err := coordinator.await(ctx, env); done <- err }()
				synctest.Wait()
				require.NotNil(t, store.envelope)
				if cancelRequest {
					cancel()
				} else {
					time.Sleep(mcp.ApprovalRequestTTL + time.Second)
				}
				err = <-done
				if cancelRequest {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.ErrorIs(t, err, constants.ErrTransactionExpired)
				}
				_, err = coordinator.ResumePendingDispatch(t.Context(), store.envelope)
				require.ErrorIs(t, err, constants.ErrTransactionExpired)
				require.Empty(t, coordinator.pending)
			})
		})
	}
}

func TestDispatchApprovalCoordinator_DecisionMayOnlyChangeL3Metadata(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &captureEnvelopeSuspender{}
		coordinator, err := NewDispatchApprovalCoordinator(store)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, _, err := coordinator.await(ctx, resumeTestEnvelope(t, constants.Event.Operator.FsRead.Requested, fsReadPayload(t)))
			done <- err
		}()
		synctest.Wait()
		changed := proto.Clone(store.envelope).(*commonv1.GovernanceEnvelope)
		changed.StateMerkleRoot = "other-root"
		_, err = coordinator.ResumePendingDispatch(t.Context(), changed)
		require.ErrorIs(t, err, constants.ErrTxInvalidEnvelope)
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
	})
}
