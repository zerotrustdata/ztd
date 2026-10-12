// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.

//go:build integration

package operatorcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestDeploymentReadsPendingRequestWithoutLogs(t *testing.T) {
	dir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(dir, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	data, err := json.Marshal(models.OperatorDeploymentState{
		Phase: models.OperatorDeploymentPhasePendingApproval, RequestID: "resumed-request", UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(constants.DeploymentDirname, constants.DeploymentStateFileOperator), data, constants.PermFilePrivate))
	state, err := (deploySSH{local: true}).readDeploymentState(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, "resumed-request", state.RequestID)
}

func TestDeploymentRejectsPreviousLaunchProgress(t *testing.T) {
	dir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(dir, slog.Default())
	require.NoError(t, err)
	data, err := json.Marshal(models.OperatorDeploymentState{
		LaunchID: "previous", Phase: models.OperatorDeploymentPhaseReady, OperatorSessionID: "old-session", UpdatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(constants.DeploymentDirname, constants.DeploymentStateFileOperator), data, constants.PermFilePrivate))
	state, err := (deploySSH{local: true, launchID: "current"}).readDeploymentState(context.Background(), dir)
	require.NoError(t, err)
	require.Nil(t, state)
}

func TestDeploymentReadsSessionAndFailureWithoutLogs(t *testing.T) {
	for _, phase := range []models.OperatorDeploymentPhase{models.OperatorDeploymentPhaseEnrolled, models.OperatorDeploymentPhaseReady, models.OperatorDeploymentPhaseFailed} {
		t.Run(string(phase), func(t *testing.T) {
			dir := testutil.TempDir(t)
			fileSvc, err := fs.NewRuntimeFileService(dir, slog.Default())
			require.NoError(t, err)
			data, err := json.Marshal(models.OperatorDeploymentState{
				LaunchID: "current", Phase: phase, RequestID: "original-request", OperatorSessionID: "session", Error: "startup failed", UpdatedAt: time.Now().UTC(),
			})
			require.NoError(t, err)
			require.NoError(t, fileSvc.WriteFile(context.Background(), filepath.Join(constants.DeploymentDirname, constants.DeploymentStateFileOperator), data, constants.PermFilePrivate))
			target := deploySSH{local: true, launchID: "current"}

			state, err := target.readDeploymentState(context.Background(), dir)
			require.NoError(t, err)
			require.Equal(t, phase, state.Phase)
			waitErr := explainDeployWait(target, dir, fmt.Errorf("%w: wait ended", constants.ErrOperatorDeployFailed))
			require.ErrorIs(t, waitErr, constants.ErrOperatorDeployFailed)
			if phase == models.OperatorDeploymentPhaseFailed {
				require.ErrorContains(t, waitErr, "startup failed")
			} else {
				require.NotContains(t, waitErr.Error(), "startup failed")
			}
		})
	}
}

func TestEnrollDeployedOperatorsReportsEachWorkersAnnouncedSession(t *testing.T) {
	events := newDeploymentEvents()
	ready, unannounced := events.watch("req-ready"), events.watch("req-unannounced")
	pending, err := json.Marshal(models.PlatformEnrollmentPendingResponse{Requests: []models.PlatformEnrollmentPendingRequest{
		{RequestID: "req-ready", Fingerprints: testWorkerFingerprints},
		{RequestID: "req-unannounced", Fingerprints: testWorkerFingerprints},
	}})
	require.NoError(t, err)
	target := deploySSH{local: true}

	gateway := &windowedGateway{MockAPIClient: &cmdtest.MockAPIClient{}, pending: pending, watches: map[string]*deploymentWatch{"req-ready": ready}}
	ops := []deployedOperator{{target: target, Dir: testutil.TempDir(t), RequestID: "req-ready", Fingerprints: testWorkerFingerprints, watch: ready}}
	require.NoError(t, enrollDeployedOperators(context.Background(), gateway, ops, operatorDeployApprovalWindow))
	require.Equal(t, "session-req-ready", ops[0].SessionID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	silent := &silentGateway{MockAPIClient: &cmdtest.MockAPIClient{}, pending: pending}
	ops = []deployedOperator{{target: target, Dir: testutil.TempDir(t), RequestID: "req-unannounced", Fingerprints: testWorkerFingerprints, watch: unannounced}}
	require.ErrorIs(t, enrollDeployedOperators(ctx, silent, ops, operatorDeployApprovalWindow), constants.ErrOperatorDeployFailed)
}

// silentGateway approves every staged request but never announces readiness.
type silentGateway struct {
	*cmdtest.MockAPIClient
	pending []byte
}

func (g *silentGateway) Get(string) ([]byte, error) { return g.pending, nil }

func (g *silentGateway) Post(_ string, body interface{}) ([]byte, error) {
	return approvedDecision(body.(models.PlatformEnrollmentBatchDecisionRequest))
}

func TestOperatorDeploymentStateCommandReadsOnlyRequestedRuntime(t *testing.T) {
	dir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(dir, slog.Default())
	require.NoError(t, err)
	cmd := operatorDeploymentStateCmdWithFactory(func(base string, _ *slog.Logger) (fs.RuntimeFileService, error) {
		require.Equal(t, dir, base)
		return fileSvc, nil
	})
	var output, errOutput bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&errOutput)
	cmd.SetArgs([]string{"--working-dir", dir})
	require.NoError(t, cmd.Execute())
	require.JSONEq(t, "null", output.String())
	require.False(t, fileExistsForTest(t, fileSvc), "read command must not create runtime files")
}

func fileExistsForTest(t *testing.T, fileSvc fs.RuntimeFileService) bool {
	t.Helper()
	exists, err := fileSvc.FileExists(context.Background(), "")
	require.NoError(t, err)
	return exists
}
