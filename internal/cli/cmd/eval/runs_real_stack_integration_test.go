// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/testutil"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	harnessconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	"github.com/zerotrustdata/ztd/internal/uuid"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func TestRunsExecute_RealStack(t *testing.T) {
	tests := []struct {
		name    string
		posture constants.Posture
		wire    evalv1.EvaluationGovernancePosture
	}{
		{
			name:    "doctrine",
			posture: constants.PostureDoctrine,
			wire:    evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		},
		{
			name:    "ratify",
			posture: constants.PostureRatify,
			wire:    evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY,
		},
		{
			name:    "notary",
			posture: constants.PostureNotary,
			wire:    evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_NOTARY,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			t.Cleanup(cancel)
			root := testutil.TempDir(t)
			keyring := keystoretest.NewMemoryKeyring()
			cfgLoader := func(projectRoot string) (*config.Config, error) {
				cfg, err := config.Load(projectRoot)
				if err != nil {
					return nil, err
				}
				cfg.Keyring = keyring
				return cfg, nil
			}
			deps := nativeEvalDeps{
				configLoader:      cfgLoader,
				fileSvcFactory:    shared.EvidenceFileSvcFactory(keystore.Options{Keyring: keyring}),
				createRuntimeTree: func(ctx context.Context, fileSvc fs.RuntimeFileService) error { return fileSvc.CreateRuntimeTree(ctx) },
				clientFactory:     func(cfg harnessconfig.Config) (*harnessclient.Client, error) { return harnessclient.New(cfg) },
				authLoader:        auth.LoadClientAuthContext,
				bindClientFactory: defaultEvalBindClient,
				campaignPublicationFactory: func(cmd *cobra.Command, fileSvc fs.RuntimeFileService, cfg *config.Config) (campaignVerificationPublication, error) {
					return NewCampaignPublicationCoordinator(cmd, fileSvc, cfg)
				},
				agentExecutorFactory: newProductionCampaignAgentExecutor,
				appEnroller:          enrollEvalAppIdentity,
				runControl:           defaultRunControlDeps(),
				conformance:          defaultConformanceDeps(),
				now:                  time.Now,
				newID:                uuid.NewString,
			}

			stack := fixtures.NewEvalStack(t, fixtures.EvalStackOptions{
				Posture: tt.posture,
				Replies: []fixtures.OllamaReply{{Content: "READY"}},
			})

			fileSvc, err := fs.NewRuntimeFileService(root, slog.Default())
			require.NoError(t, err)
			require.NoError(t, fileSvc.CreateRuntimeTree(ctx))
			keyStore, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, fileSvc)
			require.NoError(t, err)
			require.NoError(t, keyStore.Initialize())
			require.NoError(t, keyStore.InitVault())
			cfg, err := cfgLoader(root)
			require.NoError(t, err)
			stack.Identity.InstallCLIRuntime(t, stack.Gateway, fileSvc, cfg)
			writeTestFrozenInventory(t, root, testQwenVariant())
			dataWorkload, err := stack.Data.Workload()
			require.NoError(t, err)
			if tt.posture.RequiresL3() {
				bindOwnerCLISessionToData(ctx, t, deps, cfg, fileSvc, stack, dataWorkload.OperatorSessionID)
			}

			// Keep the live run to the answer-only Lite scenario: one real model turn,
			// no synthetic tools or receipts, and one signed Inference Operator receipt.
			suite := evaluation.DefaultScenarioSuite()
			suite.ID = "real-stack-single-assignment"
			suite.Version = evaluation.SuiteSchemaVersion
			for _, scenario := range evaluation.DefaultScenarioSuite().Scenarios {
				if scenario.ScenarioID == "instruction-exact-format" {
					suite.Scenarios = []evaluation.SuiteScenario{scenario}
					break
				}
			}
			require.Len(t, suite.Scenarios, 1)
			evidenceFiles, err := deps.fileSvcFactory(root, slog.Default())
			require.NoError(t, err)
			require.NoError(t, evaluation.NewStore(evidenceFiles).CreateSuite(ctx, suite))
			_, err = createCampaign(ctx, deps, evidenceFiles, campaignCreateSpec{
				GovernancePosture: tt.wire,
				Platform:          testPlatform,
				CampaignID:        "real-stack-campaign",
				Variants:          []*evalv1.ModelVariant{testQwenVariant()},
				Repetitions:       1,
				SuiteID:           suite.ID,
				Investigation: &evalv1.EvaluationInvestigation{
					Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
					InvestigationId: "real-stack-investigation",
					DataOperatorId:  dataWorkload.OperatorID,
				},
			})
			require.NoError(t, err)

			cmd := evalCmdWithConfig(deps)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"runs", "start", "real-stack-campaign", "--daemon=false", "--publish=false", "--no-backup", "--project-root", root})
			cmdCtx, cancelCmd := context.WithTimeout(testVersionContext(), 2*time.Minute)
			t.Cleanup(cancelCmd)
			require.NoError(t, cmd.ExecuteContext(cmdCtx), out.String())
			assert.Contains(t, out.String(), "Executed 1 assignment")
			assert.Equal(t, 1, stack.Provider.ChatCount())

			store := evaluation.NewStore(evidenceFiles)
			runIDs, err := store.ListRunIDs(ctx)
			require.NoError(t, err)
			require.Len(t, runIDs, 1)
			assignments, err := store.ListAssignments(ctx, runIDs[0])
			require.NoError(t, err)
			require.Len(t, assignments, 1)
			assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, assignments[0].GetLifecycleStatus())
			trace, err := store.LoadAssignmentAgentTrace(ctx, runIDs[0], assignments[0].GetAssignmentId())
			require.NoError(t, err)
			require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
			require.Len(t, trace.ModelCalls, 1)
			assert.Equal(t, "READY", trace.FinalOutput)
			require.NotNil(t, trace.ModelCalls[0].Receipt)
			receipt := trace.ModelCalls[0].Receipt.ActionReceipt
			workload, err := stack.Inference.Workload()
			require.NoError(t, err)
			assert.Equal(t, workload.OperatorID, trace.Inference.OperatorID)
			assert.Equal(t, workload.OperatorSessionID, trace.Inference.SessionID)
			assert.Equal(t, dataWorkload.OperatorID, trace.Data.OperatorID)
			assert.NotEqual(t, trace.Data.OperatorID, trace.Inference.OperatorID)

			operatorFiles, err := fs.NewRuntimeFileService(stack.InferenceHost.WorkingDir, slog.Default())
			require.NoError(t, err)
			local, err := storage.ReadLocalActionReceipt(operatorFiles, slog.Default(), keystore.Options{Keyring: stack.InferenceHost.Keyring}, receipt.GetTransactionId())
			require.NoError(t, err)
			require.NotNil(t, local)
			assert.True(t, proto.Equal(receipt, local.ActionReceipt), "the imported trace receipt must match the Inference Operator's authoritative local record")
		})
	}
}

// bindOwnerCLISessionToData binds the owner's CLI session to the Data Operator
// before the run, through the same bind `runs start` performs when the Data
// Operator is unbound, and moves the owner's CLI approver onto the replacement
// session. Under an L3 posture every suspension is approved over the owner's
// CLI session, and binding replaces that session, so a bind during the run
// would leave the approver listening on a retired session. Binding first makes
// the run's own bind a no-op, so the session stays put for the whole run.
func bindOwnerCLISessionToData(ctx context.Context, t *testing.T, deps nativeEvalDeps, cfg *config.Config, fileSvc fs.RuntimeFileService, stack *fixtures.EvalStack, dataSessionID string) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	authContext, err := deps.authLoader(fileSvc, cfg)
	require.NoError(t, err)
	gatewayDeps := deps.gatewayDeps()
	operators, err := evalListOperators(cmd, gatewayDeps, cfg, authContext)
	require.NoError(t, err)
	bound, err := evalBindDataOperator(cmd, gatewayDeps, cfg, fileSvc, authContext, operators, dataSessionID, true)
	require.NoError(t, err)
	stack.FollowOwnerCLISession(t, bound.CLISessionID)
}
