// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operator

import (
	"testing"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestCheckStoppable(t *testing.T) {
	assert.NoError(t, CheckStoppable(&operatorv1.OperatorDocument{OperatorType: string(constants.OperatorTypeRemote)}))
	assert.ErrorIs(t, CheckStoppable(&operatorv1.OperatorDocument{OperatorType: string(constants.OperatorTypeEmbedded)}), constants.ErrOperatorStopEmbedded)
	assert.ErrorIs(t, CheckStoppable(&operatorv1.OperatorDocument{}), constants.ErrOperatorStopNotRemote)
}

func TestNewStopRequest_TrimsReason(t *testing.T) {
	assert.Equal(t, models.StopOperatorRequest{OperatorSessionID: "s1", Reason: "why"}, NewStopRequest("s1", "  why \n"))
}

func TestDecodeStopResponse(t *testing.T) {
	got, err := DecodeStopResponse([]byte(`{"success":true,"operator_id":"op","operator_session_id":"s1","transaction_id":"tx"}`))
	require.NoError(t, err)
	assert.Equal(t, "op", got.OperatorID)

	_, err = DecodeStopResponse([]byte(`{"success":false}`))
	assert.ErrorContains(t, err, "unsuccessful")

	_, err = DecodeStopResponse([]byte(`not json`))
	assert.ErrorContains(t, err, "parse response")
}
