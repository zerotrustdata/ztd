// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestRemoteModelProvenanceClient_Load_RejectsMissingInputs(t *testing.T) {
	t.Parallel()
	_, err := (&remoteModelProvenanceClient{}).Load(context.Background(), "attempt-1")
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrNotFound)

	client := &remoteModelProvenanceClient{client: &cmdtest.MockAPIClient{}}
	_, err = client.Load(context.Background(), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrNotFound)
}

func TestNewModelProvenanceRemote_ReturnsClient(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	remote, err := newModelProvenanceRemote(fileSvc, cfg)
	require.NoError(t, err)
	require.NotNil(t, remote)
}
