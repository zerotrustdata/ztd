// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.

package operatorcmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestDeploymentStateRejectsIncompleteOrUnknownProgress(t *testing.T) {
	t.Parallel()
	for _, data := range []string{
		`{`, `{}`, `{"phase":"unknown","updated_at":"2026-10-08T00:00:00Z"}`,
		`{"phase":"pending_approval","updated_at":"2026-10-08T00:00:00Z"}`,
		`{"phase":"ready","updated_at":"2026-10-08T00:00:00Z"}`,
		`{"phase":"failed","updated_at":"2026-10-08T00:00:00Z"}`,
		`{"phase":"ready","operator_session_id":"session"}`,
		`{"phase":"requesting","updated_at":"2026-10-08T00:00:00Z"}`,
		`{"phase":"requesting","fingerprints":{"operator":"op-fp"},"updated_at":"2026-10-08T00:00:00Z"}`,
	} {
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			_, err := decodeOperatorDeploymentState([]byte(data))
			require.ErrorIs(t, err, constants.ErrOperatorDeployFailed)
		})
	}
	state, err := decodeOperatorDeploymentState([]byte("null"))
	require.NoError(t, err)
	require.Nil(t, state)
}

func TestDeploymentStateAcceptsARequestingWorkerWithBothFingerprints(t *testing.T) {
	t.Parallel()
	state, err := decodeOperatorDeploymentState([]byte(`{"phase":"requesting","fingerprints":{"operator":"op-fp","cli":"cli-fp"},"updated_at":"2026-10-08T00:00:00Z"}`))
	require.NoError(t, err)
	require.Equal(t, models.OperatorDeploymentPhaseRequesting, state.Phase)
	require.Equal(t, models.PlatformEnrollmentCSRFingerprints{Operator: "op-fp", CLI: "cli-fp"}, state.Fingerprints)
}
