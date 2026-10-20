// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestRunsEnv_SharedRuntimeEmbeddedOperatorTopology(t *testing.T) {
	root := testutil.TempDir(t)
	embeddedOp := &operatorv1.OperatorDocument{
		Id:                "embedded-op",
		OperatorSessionId: "embedded-sess-1",
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeEmbedded),
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
			Capabilities: []string{
				string(constants.OperatorCapabilityData),
				string(constants.OperatorCapabilityInference),
			},
			InferenceOllamaEndpoint: "http://provider.example:11434",
		},
		LatestHeartbeatSnapshot: &operatorv1.HeartbeatResult{
			Environment: &operatorv1.EnvironmentDetails{Pwd: root},
		},
		ActuatorPublicKey: testDataOperatorActuatorKey,
		ActuatorKeyId:     testDataOperatorActuatorKey,
	}

	body, err := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: []*operatorv1.OperatorDocument{embeddedOp}})
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.Operators {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	paths := config.DefaultPathsConfig()
	paths.Host = server.URL
	fileSvc, err := fs.NewRuntimeFileService(root, slog.Default())
	require.NoError(t, err)
	cfg := &config.Config{ProjectRoot: root, RuntimeDir: fileSvc.Resolve(""), Paths: &paths}
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	_, _, caBundle := mustGenerateCA(t, "ztd topology test Root CA")
	require.NoError(t, auth.WriteTrustBundleToDisk(context.Background(), fileSvc, cfg, caBundle))
	deps := nativeEvalDeps{
		configLoader:   func(string) (*config.Config, error) { return cfg, nil },
		fileSvcFactory: func(string, *slog.Logger) (fs.RuntimeFileService, error) { return fileSvc, nil },
		clientFactory:  harnessclient.New,
		authLoader: func(fs.RuntimeFileService, *config.Config) (*auth.ClientAuthContext, error) {
			return &auth.ClientAuthContext{UserID: "user-1", CLISessionID: "cli-1"}, nil
		},
	}
	cmd := cmdtest.SilentCobraCommand()
	cmd.SetContext(testVersionContext())
	cmd.Flags().String("project-root", root, "")

	topology, err := resolveOperatorTopologyForDataOperator(cmd, deps, "")
	require.NoError(t, err)
	require.True(t, topology.SharedRuntime(), "embedded operator holding data and inference must resolve as a shared runtime")
	assert.Equal(t, "embedded-op", topology.Inference.OperatorID)
	assert.Equal(t, "embedded-op", topology.Data.OperatorID)
	assert.Equal(t, "embedded-sess-1", topology.Inference.OperatorSessionID)
	assert.Equal(t, "embedded-sess-1", topology.Data.OperatorSessionID)

	anchored, err := topology.WithAnchors([]*operatorv1.OperatorDocument{embeddedOp})
	require.NoError(t, err)
	require.NotNil(t, anchored.Inference.Anchor)
	require.NotNil(t, anchored.Data.Anchor)
	assert.Equal(t, anchored.Inference.Anchor, anchored.Data.Anchor, "shared runtime pins one actuator anchor")
	assert.NoError(t, anchored.ValidateAnchored())
}
