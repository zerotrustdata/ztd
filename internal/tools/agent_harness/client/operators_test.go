// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"testing"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
)

func dataOperator(id, sessionID string) *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id: id, OperatorSessionId: sessionID,
		Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote),
	}
}

func TestDiscoverRemoteOperator_RequiresAuthenticatedUser(t *testing.T) {
	t.Parallel()
	client, err := New(config.Config{MTLSBaseURL: "https://gateway.invalid"})
	require.NoError(t, err)

	_, _, err = client.DiscoverRemoteOperator(context.Background())

	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}
