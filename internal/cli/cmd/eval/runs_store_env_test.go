// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// newStoreRunEnv provides the local runtime files and frozen model inventory
// used by campaign, suite, and rollout commands without starting a Gateway.
// Its auth identity is a deliberate local archive actor for commands that
// record an archive; it does not represent an authenticated Gateway session.
func newStoreRunEnv(t *testing.T) *runEnv {
	t.Helper()
	root := testutil.TempDir(t)
	writeTestFrozenInventory(t, root, testQwenVariant())
	files, err := fs.NewRuntimeFileService(root, slog.Default())
	if err != nil {
		t.Fatalf("create runtime file service: %v", err)
	}
	if err := files.CreateRuntimeTree(context.Background()); err != nil {
		t.Fatalf("create runtime tree: %v", err)
	}
	keyring := keystoretest.NewMemoryKeyring()
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, files)
	if err != nil {
		t.Fatalf("create keystore: %v", err)
	}
	if err := ks.Initialize(); err != nil {
		t.Fatalf("initialize keystore: %v", err)
	}
	if err := ks.InitVault(); err != nil {
		t.Fatalf("initialize evidence vault: %v", err)
	}
	paths := config.DefaultPathsConfig()
	cfg := &config.Config{ProjectRoot: root, RuntimeDir: files.Resolve(""), Paths: &paths, Keyring: keyring}
	control := newFakeProcessControl()
	deps := nativeEvalDeps{
		configLoader: func(string) (*config.Config, error) { return cfg, nil },
		fileSvcFactory: func(string, *slog.Logger) (fs.RuntimeFileService, error) {
			localFiles, err := fs.NewRuntimeFileService(root, slog.Default())
			if err != nil {
				return nil, err
			}
			return evaluation.NewEvidenceFileService(localFiles, func(vaultFiles fs.RuntimeFileService) (evaluation.EvidenceVault, error) {
				store, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, vaultFiles)
				if err != nil {
					return nil, err
				}
				return store.OpenVault()
			})
		},
		createRuntimeTree: func(context.Context, fs.RuntimeFileService) error { return nil },
		authLoader: func(fs.RuntimeFileService, *config.Config) (*auth.ClientAuthContext, error) {
			return &auth.ClientAuthContext{UserID: "local-archive-actor", OperatorID: "local-operator"}, nil
		},
		runControl: testRunControl(control),
		now:        func() time.Time { return time.Unix(1789657337, 0).UTC() },
		newID:      func() (string, error) { return "test-id", nil },
	}
	cmd := cmdtest.SilentCobraCommand()
	cmd.SetContext(testVersionContext())
	cmd.Flags().String("project-root", root, "")
	return &runEnv{root: root, deps: deps, cmd: cmd, control: control}
}

// prepareStoredRun writes the same campaign, run, and assignment records used
// by local CLI views without running the Gateway preflight or publication flow.
func (e *runEnv) prepareStoredRun(t *testing.T, campaignID, runID string) string {
	t.Helper()
	e.createCampaign(t, campaignID)
	return e.startStoredRun(t, campaignID, runID)
}

func (e *runEnv) startStoredRun(t *testing.T, campaignID, runID string) string {
	t.Helper()
	ctx := context.Background()
	controller := evaluation.NewCampaignController(e.store(t), nil, e.deps.now, adaptNewID(e.deps.newID))
	_, err := controller.StartRun(ctx, evaluation.RunStartRequest{
		CampaignID:                 campaignID,
		RunID:                      runID,
		InferenceOperatorSessionID: "store-only-inference-session",
		DataOperatorSessionID:      "store-only-data-session",
		PlatformRelease:            testPlatform.Release,
		Governance:                 testGovernance,
		ProviderObservationPolicy:  evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
		ModelProvenancePolicy:      evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
	})
	if err != nil {
		t.Fatalf("start stored run: %v", err)
	}
	_, err = controller.ScheduleRun(ctx, runID)
	if err != nil {
		t.Fatalf("schedule stored run: %v", err)
	}
	return runID
}
