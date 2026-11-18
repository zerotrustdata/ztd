// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/uuid"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestNewOperatorDeployService_Validation(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	results := &mockResultsPublisher{}
	eventsStore := storagetest.NewTestEventsStore(t)

	_, err := NewOperatorDeployService(nil, logger, results, config.OperatorDeployConfig{}, nil, eventsStore)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)

	_, err = NewOperatorDeployService(cfg, nil, results, config.OperatorDeployConfig{}, nil, eventsStore)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)

	_, err = NewOperatorDeployService(cfg, logger, nil, config.OperatorDeployConfig{}, nil, eventsStore)
	require.ErrorIs(t, err, constants.ErrPubSubResultsPublisher)

	_, err = NewOperatorDeployService(cfg, logger, results, config.OperatorDeployConfig{}, nil, nil)
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreRequired)
}

func TestOperatorDeployService_HandleDeployRequest_LocalOverridesAndDeterministicIDs(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	cfg.Endpoint = "127.0.0.1"
	cfg.HTTPPort = 18080
	cfg.HTTPSPort = 18443
	cfg.TrustBundlePath = "/etc/ztd/ca.pem"

	type startCall struct {
		dir    string
		binary string
		args   []string
	}
	var mu sync.Mutex
	starts := make(map[string]startCall)

	hostADir := testutil.TempDir(t)
	hostBDir := testutil.TempDir(t)
	overrides := map[string]operatordeploy.LocalTargetOverride{
		"host-a": {
			DestDir: hostADir,
			StartFunc: func(_ context.Context, dir, binary string, args []string) (int, error) {
				mu.Lock()
				defer mu.Unlock()
				starts["host-a"] = startCall{dir: dir, binary: binary, args: append([]string(nil), args...)}
				return 101, nil
			},
		},
		"host-b": {
			DestDir: hostBDir,
			StartFunc: func(_ context.Context, dir, binary string, args []string) (int, error) {
				mu.Lock()
				defer mu.Unlock()
				starts["host-b"] = startCall{dir: dir, binary: binary, args: append([]string(nil), args...)}
				return 102, nil
			},
		},
	}

	results := &mockResultsPublisher{}
	svc, err := NewOperatorDeployService(
		cfg,
		testutil.NewTestLogger(),
		results,
		config.OperatorDeployConfig{LocalOverrides: overrides},
		nil,
		storagetest.NewTestEventsStore(t),
	)
	require.NoError(t, err)

	req := &operatorv1.OperatorDeployRequested{
		ExecutionId: "exec-deploy-42",
		Hostnames:   []string{"host-a", "host-b"},
	}
	msg := &PubSubCommandMessage{
		ID:                "tx-deploy-42",
		EventType:         constants.EventOperatorDeployRequested,
		OperatorSessionID: "sess-1",
		Payload:           mustMarshalProto(t, req),
	}

	summary, err := svc.HandleDeployRequest(t.Context(), msg)
	require.NoError(t, err)
	require.NotEmpty(t, summary)
	require.Len(t, results.operatorDeployResults, 1)

	pub := results.operatorDeployResults[0]
	assert.Equal(t, "exec-deploy-42", pub.GetExecutionId())
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, pub.GetStatus())
	require.Len(t, pub.GetDeployments(), 2)

	wantID0 := uuid.Format(uuid.NewSHA1(uuid.NameSpaceOID, []byte("exec-deploy-42:0")))
	wantID1 := uuid.Format(uuid.NewSHA1(uuid.NameSpaceOID, []byte("exec-deploy-42:1")))

	assert.Equal(t, "host-a", pub.Deployments[0].Hostname)
	assert.Equal(t, wantID0, pub.Deployments[0].DeploymentId)
	assert.Equal(t, string(models.OperatorDeploymentPhasePendingApproval), pub.Deployments[0].Phase)
	assert.True(t, pub.Deployments[0].Success)
	resolvedA, err := filepath.EvalSymlinks(hostADir)
	require.NoError(t, err)
	assert.Equal(t, resolvedA, pub.Deployments[0].WorkingDir)

	assert.Equal(t, "host-b", pub.Deployments[1].Hostname)
	assert.Equal(t, wantID1, pub.Deployments[1].DeploymentId)
	assert.Equal(t, string(models.OperatorDeploymentPhasePendingApproval), pub.Deployments[1].Phase)
	assert.True(t, pub.Deployments[1].Success)
	resolvedB, err := filepath.EvalSymlinks(hostBDir)
	require.NoError(t, err)
	assert.Equal(t, resolvedB, pub.Deployments[1].WorkingDir)

	assert.Contains(t, starts["host-a"].args, "--endpoint")
	assert.Contains(t, starts["host-a"].args, "127.0.0.1")
	assert.Contains(t, starts["host-a"].args, "--gateway-http-port=18080")
	assert.Contains(t, starts["host-a"].args, "--gateway-https-port=18443")
	assert.Contains(t, starts["host-a"].args, "--trust-bundle=/etc/ztd/ca.pem")
	assert.Contains(t, starts["host-a"].args, "--capabilities=data")
	assert.Contains(t, starts["host-a"].args, "--deployment-id="+wantID0)
	assert.Contains(t, starts["host-b"].args, "--deployment-id="+wantID1)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(summary), &decoded))
	assert.Equal(t, "exec-deploy-42", decoded["execution_id"])
}

type fakeRemoteDeployClient struct {
	host      string
	dialErr   error
	startErr  error
	closed    bool
	mu        sync.Mutex
	startArgs []string
}

func (f *fakeRemoteDeployClient) IsWSL() bool { return false }

func (f *fakeRemoteDeployClient) UploadBinary(_ context.Context, _, remoteDir string) (string, error) {
	return remoteDir + "/.deploy-bin", nil
}

func (f *fakeRemoteDeployClient) ExecuteAgent(_ context.Context, req models.DeployHostRequest) (models.DeployHostResponse, error) {
	switch req.Action {
	case models.DeployHostActionPrepare:
		return models.DeployHostResponse{Success: true, ResolvedDirs: []string{"/opt/ztd/op"}}, nil
	case models.DeployHostActionLink:
		return models.DeployHostResponse{Success: true}, nil
	case models.DeployHostActionStart:
		f.mu.Lock()
		f.startArgs = append([]string(nil), req.Args...)
		f.mu.Unlock()
		if f.startErr != nil {
			return models.DeployHostResponse{Success: false, Error: f.startErr.Error()}, f.startErr
		}
		return models.DeployHostResponse{Success: true, PID: 4242}, nil
	default:
		return models.DeployHostResponse{}, fmt.Errorf("unexpected action %s", req.Action)
	}
}

func (f *fakeRemoteDeployClient) Close() error {
	f.closed = true
	return nil
}

func TestOperatorDeployService_HandleDeployRequest_RemoteAndFailure(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	cfg.Endpoint = "gw.example.internal"
	cfg.HTTPPort = 8080
	cfg.HTTPSPort = 8443

	sourceBin := filepath.Join(testutil.TempDir(t), "ztd")
	// Use a small dummy file as sourceBinary so UploadBinary's os.Executable isn't required.

	t.Run("remote success", func(t *testing.T) {
		client := &fakeRemoteDeployClient{host: "worker-1"}
		results := &mockResultsPublisher{}
		svc, err := NewOperatorDeployService(
			cfg,
			testutil.NewTestLogger(),
			results,
			config.OperatorDeployConfig{
				SourceBinary: sourceBin,
				Capabilities: constants.OperatorCapabilities{constants.OperatorCapabilityData, constants.OperatorCapabilityObserver},
			},
			func(_ context.Context, host string, _ int, _ string) (operatordeploy.RemoteDeployClient, error) {
				assert.Equal(t, "worker-1", host)
				return client, nil
			},
			storagetest.NewTestEventsStore(t),
		)
		require.NoError(t, err)

		msg := &PubSubCommandMessage{
			ID:                "tx-remote-1",
			EventType:         constants.EventOperatorDeployRequested,
			OperatorSessionID: "sess-1",
			Payload:           mustMarshalProto(t, &operatorv1.OperatorDeployRequested{ExecutionId: "exec-r1", Hostnames: []string{"worker-1"}}),
		}
		_, err = svc.HandleDeployRequest(t.Context(), msg)
		require.NoError(t, err)
		assert.True(t, client.closed, "remote deploy client must be closed by cleanup")
		require.Len(t, results.operatorDeployResults, 1)
		res := results.operatorDeployResults[0]
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, res.GetStatus())
		require.Len(t, res.GetDeployments(), 1)
		assert.Equal(t, "/opt/ztd/op", res.Deployments[0].WorkingDir)
		assert.Contains(t, client.startArgs, "--capabilities=data,observer")
	})

	t.Run("remote partial failure publishes failed result and returns error", func(t *testing.T) {
		okClient := &fakeRemoteDeployClient{host: "worker-ok"}
		results := &mockResultsPublisher{}
		svc, err := NewOperatorDeployService(
			cfg,
			testutil.NewTestLogger(),
			results,
			config.OperatorDeployConfig{SourceBinary: sourceBin},
			func(_ context.Context, host string, _ int, _ string) (operatordeploy.RemoteDeployClient, error) {
				if host == "worker-bad" {
					return nil, errors.New("ssh dial refused")
				}
				return okClient, nil
			},
			storagetest.NewTestEventsStore(t),
		)
		require.NoError(t, err)

		msg := &PubSubCommandMessage{
			ID:                "tx-remote-2",
			EventType:         constants.EventOperatorDeployRequested,
			OperatorSessionID: "sess-1",
			Payload:           mustMarshalProto(t, &operatorv1.OperatorDeployRequested{ExecutionId: "exec-r2", Hostnames: []string{"worker-ok", "worker-bad"}}),
		}
		_, err = svc.HandleDeployRequest(t.Context(), msg)
		require.ErrorIs(t, err, constants.ErrOperatorDeployFailed)
		assert.True(t, okClient.closed)
		require.Len(t, results.operatorDeployResults, 1)
		res := results.operatorDeployResults[0]
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, res.GetStatus())
		require.Len(t, res.GetDeployments(), 2)
		assert.True(t, res.Deployments[0].Success)
		assert.Equal(t, string(models.OperatorDeploymentPhasePendingApproval), res.Deployments[0].Phase)
		assert.False(t, res.Deployments[1].Success)
		assert.Equal(t, string(models.OperatorDeploymentPhaseFailed), res.Deployments[1].Phase)
		assert.Contains(t, res.Deployments[1].Error, "ssh dial refused")
	})

	t.Run("invalid payload and invalid hostnames fail closed and publish failed result", func(t *testing.T) {
		results := &mockResultsPublisher{}
		svc, err := NewOperatorDeployService(
			cfg,
			testutil.NewTestLogger(),
			results,
			config.OperatorDeployConfig{},
			nil,
			storagetest.NewTestEventsStore(t),
		)
		require.NoError(t, err)

		_, err = svc.HandleDeployRequest(t.Context(), &PubSubCommandMessage{ID: "bad-proto", Payload: []byte{0xff}})
		require.ErrorIs(t, err, proto.Error)

		_, err = svc.HandleDeployRequest(t.Context(), &PubSubCommandMessage{
			ID:      "empty-hosts",
			Payload: mustMarshalProto(t, &operatorv1.OperatorDeployRequested{ExecutionId: "e1"}),
		})
		require.ErrorIs(t, err, constants.ErrMCPValidateHostnamesEmpty)
		require.Len(t, results.operatorDeployResults, 2)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, results.operatorDeployResults[0].GetStatus())
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, results.operatorDeployResults[1].GetStatus())
	})
}
