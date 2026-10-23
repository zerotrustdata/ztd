// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestShouldPublishViaGatewayWithoutHostExportConfig(t *testing.T) { //nolint:paralleltest // mutates package-level state

	fileSvc, _ := cmdtest.NewCmdTestEnv(t)
	original := GatewayHealthCheck
	GatewayHealthCheck = func() bool { return true }
	t.Cleanup(func() { GatewayHealthCheck = original })

	assert.False(t, isHostPublicFeedConfigured(context.Background(), fileSvc))
	assert.True(t, ShouldPublishViaGateway(context.Background(), fileSvc))
}

func TestShouldPublishViaGatewayReturnsFalseWhenGatewayUnhealthy(t *testing.T) { //nolint:paralleltest // mutates package-level state

	fileSvc, _ := cmdtest.NewCmdTestEnv(t)
	original := GatewayHealthCheck
	GatewayHealthCheck = func() bool { return false }
	t.Cleanup(func() { GatewayHealthCheck = original })

	assert.False(t, isHostPublicFeedConfigured(context.Background(), fileSvc))
	assert.False(t, ShouldPublishViaGateway(context.Background(), fileSvc))
}

func TestIsPublicFeedSequenceOutOfOrder(t *testing.T) {
	t.Parallel()
	assert.True(t, isPublicFeedSequenceOutOfOrder(fmt.Errorf("gateway export batch: %w", constants.ErrPublicFeedSequenceOutOfOrder)))
	assert.False(t, isPublicFeedSequenceOutOfOrder(nil))
	assert.False(t, isPublicFeedSequenceOutOfOrder(fmt.Errorf("other error")))
}

func TestIsPublicFeedSnapshotNotFound(t *testing.T) {
	t.Parallel()
	assert.True(t, isPublicFeedSnapshotNotFound(fmt.Errorf("snapshot lookup: %w", constants.ErrPublicFeedSnapshotNotFound)))
	assert.False(t, isPublicFeedSnapshotNotFound(nil))
}

func TestNewCampaignFeedExporter_RequiresHealthyGateway(t *testing.T) { //nolint:paralleltest // mutates package-level state

	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)
	cfg.Paths.Host = "http://127.0.0.1:1"

	_, err := NewCampaignFeedExporter(context.Background(), fileSvc, cfg)
	assert.ErrorIs(t, err, constants.ErrGatewayUnhealthy)
}
