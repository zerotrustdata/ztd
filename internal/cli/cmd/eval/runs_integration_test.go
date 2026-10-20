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
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func TestRunsList_EmptyProject(t *testing.T) {
	env := newStoreRunEnv(t)

	assert.Contains(t, env.mustRun(t, "runs", "list"), "No runs found")
}

func TestRunsList_AfterPrepare(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	out := env.mustRun(t, "runs", "list")
	assert.Contains(t, out, "run-a-1")
	assert.Contains(t, out, "eval-a")
	assert.Contains(t, out, "scheduled")

	var payload runListJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "list"))
	require.Len(t, payload.Runs, 1)
	assert.Equal(t, "run-a-1", payload.Runs[0].RunID)
	assert.Equal(t, "eval-a", payload.Runs[0].CampaignID)
	assert.Equal(t, "scheduled", payload.Runs[0].Status)
	assert.Equal(t, uint64(testModelRoleCells(t)), payload.Runs[0].ExpectedAssignments)
	assert.False(t, payload.Runs[0].Archived)
}

func TestRunsList_FiltersByCampaignAndStatus(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.prepareStoredRun(t, "eval-b", "run-b-1")

	var payload runListJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "list", "--campaign", "eval-b"))
	require.Len(t, payload.Runs, 1)
	assert.Equal(t, "run-b-1", payload.Runs[0].RunID)

	require.NoError(t, env.runJSON(t, &payload, "runs", "list", "--status", "verified"))
	assert.Empty(t, payload.Runs)

	require.NoError(t, env.runJSON(t, &payload, "runs", "list", "--status", "scheduled"))
	assert.Len(t, payload.Runs, 2)
}

func TestRunsShow_AfterPrepare(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	out := env.mustRun(t, "runs", "show", "run-a-1")
	assert.Contains(t, out, "Run: run-a-1")
	assert.Contains(t, out, "Campaign: eval-a")
	assert.Contains(t, out, "Status: scheduled")
	assert.Contains(t, out, fmt.Sprintf("Expected assignments: %d", testModelRoleCells(t)))

	var payload runShowJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "show", "run-a-1"))
	assert.Equal(t, "run-a-1", payload.RunID)
	assert.Equal(t, "eval-a", payload.CampaignID)
	assert.Equal(t, "store-only-inference-session", payload.InferenceSession)
	assert.Equal(t, "store-only-data-session", payload.DataSession)
	assert.Equal(t, uint32(testModelRoleCells(t)), payload.Queued)
	assert.Nil(t, payload.Holder)
}

func TestRunsShow_RejectsMissingRun(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "runs", "show", "missing-run-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runs show")
}

func TestRunsShow_ReportsInterruptedWhenNoProcessHoldsARunningAssignment(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	markFirstAssignmentRunning(t, env)

	var payload runShowJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "show", "run-a-1"))
	assert.Equal(t, "interrupted", payload.Status)
	assert.Equal(t, uint32(1), payload.Running)
}

func TestRunsShow_ReportsRunningOnlyWhileALiveProcessHoldsTheLease(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	markFirstAssignmentRunning(t, env)
	_, err := env.store(t).AcquireRunLease(context.Background(), evaluation.RunLease{RunID: "run-a-1", PID: testPID, Host: testHost, StartedAt: env.deps.now()}, nil)
	require.NoError(t, err)

	var payload runShowJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "show", "run-a-1"))
	assert.Equal(t, "running", payload.Status)
	require.NotNil(t, payload.Holder)
	assert.Equal(t, testPID, payload.Holder.PID)

	out := env.mustRun(t, "runs", "show", "run-a-1")
	assert.Contains(t, out, "Held by: process 4242 on test-host")

	env.control.setAlive(testPID, false)
	var stalePayload runShowJSON
	require.NoError(t, env.runJSON(t, &stalePayload, "runs", "show", "run-a-1"))
	assert.Equal(t, "interrupted", stalePayload.Status, "a lease whose process is gone no longer counts as running")
	assert.Nil(t, stalePayload.Holder)
}

func TestRunsVerify_CoverageReportsIncompleteScheduledRun(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	out, err := env.run(t, "runs", "verify", "run-a-1", "--coverage")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
	assert.Contains(t, out, "incomplete")

	var payload runCoverageJSON
	err = env.runJSON(t, &payload, "runs", "verify", "run-a-1", "--coverage")
	require.Error(t, err)
	assert.False(t, payload.Complete)
	assert.Equal(t, uint64(testModelRoleCells(t)), payload.ExpectedCells)
}

func TestRunsVerify_CoverageCannotCombineWithWitnessRequirements(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	_, err := env.run(t, "runs", "verify", "run-a-1", "--coverage", "--require-observation")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}

func TestRunsVerify_RejectsMissingRun(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "runs", "verify", "missing-run-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runs verify")
}

func TestRunsExport_WritesArtifacts(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	outputRelDir := filepath.Join(constants.EvaluationCampaignExportDirname, "run-a-1")

	out := env.mustRun(t, "runs", "export", "run-a-1", "--output-dir", outputRelDir)
	assert.Contains(t, out, "run-a-1")

	exists, err := env.fileSvc(t).FileExists(context.Background(), filepath.Join(outputRelDir, constants.EvaluationRunSummaryFilename))
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestRunsExport_RejectsSealedOutputDir(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	_, err := env.run(t, "runs", "export", "run-a-1", "--output-dir", filepath.Join("data", "eval", "runs", "run-a-1", "export"))
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationExportDirSealed)
}

func TestRunsExport_RejectsExternalOutputDir(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	_, err := env.run(t, "runs", "export", "run-a-1", "--output-dir", filepath.Join(env.root, "exports", "run-a-1"))
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationExportDirNotRelative)
}

func TestRunsExport_RequiresOutputDir(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	_, err := env.run(t, "runs", "export", "run-a-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output-dir")
}

func TestRunsRepair_BackfillsResults(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	assert.Contains(t, env.mustRun(t, "runs", "repair", "run-a-1"), "Repaired")

	var payload runRepairJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "repair", "run-a-1"))
	assert.Equal(t, "run-a-1", payload.RunID)
}

func TestRunsStart_DryRunPrintsPlanWithoutWriting(t *testing.T) {
	env := newStoreRunEnv(t)
	env.createCampaign(t, "eval-a")
	env.deps.authLoader = func(fs.RuntimeFileService, *config.Config) (*auth.ClientAuthContext, error) {
		return nil, errors.New("CLI auth should not be loaded for a dry run")
	}

	out := env.mustRun(t, "runs", "start", "eval-a", "--dry-run")
	assert.Contains(t, out, "Run start plan")
	assert.Contains(t, out, "eval-a")
	assert.Contains(t, out, "Sessions resolved: skipped (dry run)")

	runIDs, err := env.store(t).ListRunIDs(context.Background())
	require.NoError(t, err)
	assert.Empty(t, runIDs)
}

func TestRunsStart_DryRunJSON(t *testing.T) {
	env := newStoreRunEnv(t)
	env.createCampaign(t, "eval-a")

	var payload runStartPlanJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "start", "eval-a", "--dry-run"))
	assert.Equal(t, "eval-a", payload.CampaignID)
	assert.Equal(t, uint64(testModelRoleCells(t)), payload.CellCount)
	assert.False(t, payload.SessionsResolved)
	assert.Empty(t, payload.InferenceSession)
	assert.Empty(t, payload.DataSession)
	assert.Equal(t, []string{"qwen3:4b"}, payload.ModelTags)
}

func TestRunsStart_PrepareOnlyPersistsRunAndSchedulesAssignments(t *testing.T) {
	env := newGatewayRunEnv(t)
	env.createCampaign(t, "eval-a")

	out := env.mustRun(t, "runs", "start", "eval-a", "--prepare-only", "--publish=false", "--daemon=false")
	assert.Contains(t, out, "Started run")
	assert.Contains(t, out, fmt.Sprintf("Scheduled %d assignments", testModelRoleCells(t)))
	assert.Contains(t, out, "ztd eval runs resume")

	runIDs, err := env.store(t).ListRunIDs(context.Background())
	require.NoError(t, err)
	require.Len(t, runIDs, 1)
	assert.True(t, strings.HasPrefix(runIDs[0], "eval-a-"), runIDs[0])

	assignments, err := env.store(t).ListAssignments(context.Background(), runIDs[0])
	require.NoError(t, err)
	assert.Len(t, assignments, testModelRoleCells(t))
}

func TestRunsStart_PrepareOnlyBindsTheOperatorSessions(t *testing.T) {
	env := newGatewayRunEnv(t)
	env.prepareRun(t, "eval-a", "run-a-1")

	run, err := env.store(t).LoadRun(context.Background(), "run-a-1")
	require.NoError(t, err)
	assert.Equal(t, env.inferenceSession, run.GetCampaignBinding().GetInferenceOperatorSessionId())
	assert.Equal(t, env.dataSession, run.GetCampaignBinding().GetDataOperatorSessionId())
}

func TestRunsStart_PersistentResolvesFrozenDataOperator(t *testing.T) {
	env := newGatewayRunEnv(t)
	_, err := createCampaign(context.Background(), env.deps, env.fileSvc(t), campaignCreateSpec{
		Platform:    testPlatform,
		CampaignID:  "eval-persistent",
		Variants:    []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions: 1,
		Investigation: &evalv1.EvaluationInvestigation{
			Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
			InvestigationId: "checkout-incident",
			DataOperatorId:  env.dataOperatorID,
		},
	})
	require.NoError(t, err)

	result, err := runStartFlow(env.cmd, env.deps, runStartFlowOptions{
		CampaignID:  "eval-persistent",
		RunID:       "run-persistent",
		PrepareOnly: true,
	})
	require.NoError(t, err)
	assert.Equal(t, env.dataOperatorID, result.Topology.Data.OperatorID)
	assert.Equal(t, env.dataSession, result.Topology.Data.OperatorSessionID)

	run, err := env.store(t).LoadRun(context.Background(), "run-persistent")
	require.NoError(t, err)
	assert.Equal(t, env.dataSession, run.GetCampaignBinding().GetDataOperatorSessionId())
}

func TestRunsStart_PersistentFailsPreflightWhenFrozenDataOperatorIsUnavailable(t *testing.T) {
	env := newGatewayRunEnv(t)
	_, err := createCampaign(context.Background(), env.deps, env.fileSvc(t), campaignCreateSpec{
		Platform:    testPlatform,
		CampaignID:  "eval-missing-operator",
		Variants:    []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions: 1,
		Investigation: &evalv1.EvaluationInvestigation{
			Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
			InvestigationId: "checkout-incident",
			DataOperatorId:  "operator-offline",
		},
	})
	require.NoError(t, err)

	_, err = runStartFlow(env.cmd, env.deps, runStartFlowOptions{
		CampaignID: "eval-missing-operator",
		RunID:      "run-missing-operator",
	})
	require.ErrorIs(t, err, constants.ErrDataOperatorNotFound)
	runIDs, err := env.store(t).ListRunIDs(context.Background())
	require.NoError(t, err)
	assert.Empty(t, runIDs, "preflight must fail before a run is persisted")
}

// A live Gateway running a posture the campaign did not freeze fails preflight
// before a run is persisted.
func TestRunsStart_FailsPreflightUnderAnUnfrozenGatewayPosture(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureConsensus, constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			env, _ := newGatewayRunEnvWithPosture(t, posture)
			env.createCampaign(t, "eval-posture")

			_, err := runStartFlow(env.cmd, env.deps, runStartFlowOptions{CampaignID: "eval-posture", RunID: "run-posture"})
			require.ErrorIs(t, err, constants.ErrEvaluationPostureUnsupported)
			require.ErrorIs(t, err, constants.ErrGatewayPostureMismatch)
			runIDs, err := env.store(t).ListRunIDs(context.Background())
			require.NoError(t, err)
			assert.Empty(t, runIDs, "posture preflight must fail before a run is persisted")
		})
	}
}

func TestRunsStart_PersistentRejectsReplacementOperatorAfterReenrollment(t *testing.T) {
	env, stackRun := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	env.createCampaign(t, "eval-session-rotation")
	_, err := runStartFlow(env.cmd, env.deps, runStartFlowOptions{
		CampaignID: "eval-session-rotation", RunID: "run-session-rotation", PrepareOnly: true,
	})
	require.NoError(t, err)
	oldSession := env.dataSession
	require.NoError(t, stackRun.stack.Data.Stop(env.cmd.Context()))
	rotated := stackRun.stack.Witness.LaunchOperator(t, stackRun.stack.DataHost, fixtures.OperatorProcessOptions{
		Capabilities: constants.OperatorCapabilities{constants.OperatorCapabilityData},
	})
	require.NoError(t, rotated.AwaitStarted(env.cmd.Context()))
	_, err = rotated.AwaitHeartbeat(env.cmd.Context())
	require.NoError(t, err)
	workload, err := rotated.Workload()
	require.NoError(t, err)
	require.NotEqual(t, env.dataOperatorID, workload.OperatorID)
	require.NotEqual(t, oldSession, workload.OperatorSessionID)
	operators, err := stackRun.stack.Gateway.Service.GetRegistrationService().ListUserOperators(env.cmd.Context(), stackRun.stack.Identity.UserID)
	require.NoError(t, err)
	_, err = evaluation.ResolveCampaignOperatorTopologyForDataOperator(operators, "", env.dataOperatorID)
	require.ErrorIs(t, err, constants.ErrDataOperatorNotFound)
	assert.Zero(t, stackRun.stack.Provider.ChatCount(), "re-enrollment must not silently replace a persistent investigation's frozen Operator")
}

func TestRunsStart_RejectsUnknownCampaign(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "runs", "start", "missing", "--dry-run")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runs start")
}

func TestRunsStart_RequiresACampaignArgument(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "runs", "start")
	require.Error(t, err)
}

func TestRunsStart_ExecutesAndVerifiesWithoutPublication(t *testing.T) {
	env := newGatewayRunEnv(t)
	publication := env.recordPublication()
	env.createCampaign(t, "eval-a")

	var out bytes.Buffer
	env.cmd.SetOut(&out)
	result, err := runStartFlow(env.cmd, env.deps, runStartFlowOptions{
		CampaignID: "eval-a",
		RunID:      "run-a-1",
		NoAutoBind: true,
		Verify:     true,
	})
	if err != nil {
		if loaded, loadErr := env.store(t).LoadCampaignVerification(context.Background(), "run-a-1"); loadErr == nil {
			t.Logf("verification status=%s failure_count=%d reasons=%v", loaded.GetStatus(), loaded.GetFailureCount(), loaded.GetFailureReasons())
		}
	}
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, 1, result.Executed)
	require.NotNil(t, result.Report)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, result.Report.GetStatus())
	assertPopulationBoundCampaignReport(t, result.Report)
	assert.Contains(t, out.String(), "verification")

	loaded, err := env.store(t).LoadCampaignVerification(context.Background(), "run-a-1")
	require.NoError(t, err)
	assertPopulationBoundCampaignReport(t, loaded)
	assert.Empty(t, publication.completionRunIDs)
	assert.Empty(t, publication.reports)
}

func TestRunsStart_ReleasesTheLeaseAndWritesNoLogFileAfterExecution(t *testing.T) {
	env := newGatewayRunEnv(t)
	env.createCampaign(t, "eval-a")

	env.cmd.SetOut(&bytes.Buffer{})
	_, err := runStartFlow(env.cmd, env.deps, runStartFlowOptions{CampaignID: "eval-a", RunID: "run-a-1", NoAutoBind: true})
	require.NoError(t, err)

	_, err = env.store(t).LoadRunLease(context.Background(), "run-a-1")
	require.Error(t, err)
	assert.True(t, isMissingRecord(err), "the lease is released when execution ends")

	// Command output quotes grade detail and model-derived text, so a run
	// writes no execution log under the runtime root.
	logsExist, err := env.fileSvc(t).FileExists(context.Background(), "eval/logs")
	require.NoError(t, err)
	assert.False(t, logsExist, "a run must not tee its output into a runtime log file")
}

func TestRunsStart_RejectsAnArchivedCampaign(t *testing.T) {
	env := newStoreRunEnv(t)
	env.createCampaign(t, "eval-a")
	env.mustRun(t, "campaigns", "archive", "eval-a")

	_, err := env.run(t, "runs", "start", "eval-a", "--dry-run")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationArchived)
}

func TestRunsResume_ExecutesOneAssignment(t *testing.T) {
	env := newGatewayRunEnv(t)
	runID := env.prepareRun(t, "eval-a", "run-a-1")
	assignment := env.firstQueuedAssignment(t, runID)

	var out bytes.Buffer
	env.cmd.SetOut(&out)
	executed, err := executeRun(env.cmd, env.deps, runExecuteOptions{
		RunID:      runID,
		Limit:      1,
		NoAutoBind: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, executed)
	assert.Contains(t, out.String(), assignment.GetAssignmentId())
}

func TestRunsResume_DefaultsLimitToOne(t *testing.T) {
	env := newGatewayRunEnv(t)
	runID := env.prepareRun(t, "eval-a", "run-a-1")

	executed, err := executeRun(env.cmd, env.deps, runExecuteOptions{RunID: runID, NoAutoBind: true})
	require.NoError(t, err)
	assert.Equal(t, 1, executed)
}

func TestRunsResume_StopWithQueuedAssignmentsIsAnError(t *testing.T) {
	env := newGatewayRunEnv(t)
	runID := env.prepareRun(t, "eval-a", "run-a-1")
	handled := make(chan struct{}, 1)
	env.deps.runControl.onSignalHandled = func() {
		select {
		case handled <- struct{}{}:
		default:
		}
	}
	interrupts := make(chan os.Signal)
	env.deps.runControl.notifyInterrupts = func() (<-chan os.Signal, func()) { return interrupts, func() {} }

	var once sync.Once
	executed, err := executeRun(env.cmd, env.deps, runExecuteOptions{
		RunID:      runID,
		Daemon:     true,
		NoAutoBind: true,
		ResultOutput: func(*evalv1.EvaluationAssignmentResult) {
			once.Do(func() {
				interrupts <- os.Interrupt
				<-handled
			})
		},
	})
	require.ErrorIs(t, err, constants.ErrEvaluationRunStopped)
	assert.Equal(t, 1, executed)
}

func TestRunsResume_RejectsMissingRunWithoutLeavingALease(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "runs", "resume", "missing-run-id")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrNotFound)

	runIDs, listErr := env.store(t).ListRunIDs(context.Background())
	require.NoError(t, listErr)
	assert.Empty(t, runIDs, "a failed resume must not create a run directory")
	assert.Contains(t, env.mustRun(t, "runs", "list"), "No runs found")
}

func TestRunsResume_GatewayOutageReleasesLeaseWithoutExecuting(t *testing.T) {
	env, run := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	env.prepareRun(t, "eval-a", "run-a-1")
	gatewayEvalAppClient(t, env, run)
	config.SetHTTPEndpointOverride("http://127.0.0.1:1")
	config.SetHTTPSEndpointOverride("https://127.0.0.1:1")

	_, err := env.run(t, "runs", "resume", "run-a-1", "--no-auto-bind")
	require.Error(t, err)
	var transportError *url.Error
	assert.ErrorAs(t, err, &transportError)
	assert.Zero(t, run.stack.Provider.ChatCount())

	_, leaseErr := env.store(t).LoadRunLease(context.Background(), "run-a-1")
	assert.True(t, isMissingRecord(leaseErr), "a failed resume releases its lease")
}

func TestRunsResume_ExecutesOneAssignmentViaCLI(t *testing.T) {
	env := newGatewayRunEnv(t)
	runID := env.prepareRun(t, "eval-a", "run-a-1")
	assignment := env.firstQueuedAssignment(t, runID)

	out := env.mustRun(t, "runs", "resume", runID)
	assert.Contains(t, out, assignment.GetAssignmentId())
	assert.Contains(t, out, "Executed 1 assignment(s) for run run-a-1")
}

func TestRunsResume_JSONOutput(t *testing.T) {
	env := newGatewayRunEnv(t)
	runID := env.prepareRun(t, "eval-a", "run-a-1")

	var payload runResumeJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "resume", runID))
	assert.Equal(t, runID, payload.RunID)
	assert.Equal(t, 1, payload.Executed)
	assert.NotEmpty(t, payload.Results)
	assert.Equal(t, int64(testModelRoleCells(t)-1), payload.Remaining)
}

func TestRunsResume_WithPublishFlag(t *testing.T) {
	env := newPublicationRunEnv(t)
	runID := env.prepareRun(t, "eval-a", "run-a-1")
	assignment := env.firstQueuedAssignment(t, runID)

	out := env.mustRun(t, "runs", "resume", runID, "--publish")
	assert.Contains(t, out, assignment.GetAssignmentId())
}

func TestRunsResume_RefusesARunHeldByALiveProcess(t *testing.T) {
	env := newGatewayRunEnv(t)
	runID := env.prepareRun(t, "eval-a", "run-a-1")
	_, err := env.store(t).AcquireRunLease(context.Background(), evaluation.RunLease{RunID: runID, PID: 9999, Host: testHost, StartedAt: env.deps.now()}, nil)
	require.NoError(t, err)
	env.control.setAlive(9999, true)

	executed, err := executeRun(env.cmd, env.deps, runExecuteOptions{RunID: runID, Limit: 1, NoAutoBind: true})
	require.Error(t, err)
	assert.Zero(t, executed)
	assert.ErrorIs(t, err, constants.ErrEvaluationRunLeaseHeld)
}

func TestRunsResume_ReplacesAStaleLeaseAndReportsIt(t *testing.T) {
	env := newGatewayRunEnv(t)
	runID := env.prepareRun(t, "eval-a", "run-a-1")
	_, err := env.store(t).AcquireRunLease(context.Background(), evaluation.RunLease{RunID: runID, PID: 9999, Host: testHost, StartedAt: env.deps.now()}, nil)
	require.NoError(t, err)

	var out bytes.Buffer
	env.cmd.SetOut(&out)
	executed, err := executeRun(env.cmd, env.deps, runExecuteOptions{RunID: runID, Limit: 1, NoAutoBind: true})
	require.NoError(t, err)
	assert.Equal(t, 1, executed)
	assert.Contains(t, out.String(), "Cleared stale lease held by process 9999 on test-host")
}

func TestVerifyRun_PersistsAndPublishesPopulationBoundReport(t *testing.T) {
	env := newGatewayRunEnv(t)
	publication := env.recordPublication()
	runID := env.prepareRun(t, "eval-a", "run-a-1")
	executed, err := executeRun(env.cmd, env.deps, runExecuteOptions{RunID: runID, Limit: 1, NoAutoBind: true})
	require.NoError(t, err)
	require.Equal(t, 1, executed)

	store := env.store(t)
	spec, err := store.LoadCampaignSpec(context.Background(), "eval-a")
	require.NoError(t, err)
	var out bytes.Buffer
	env.cmd.SetOut(&out)
	report, err := verifyRun(env.cmd, env.deps, store, runID, false, false, true, false)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus())

	loaded, err := store.LoadCampaignVerification(context.Background(), runID)
	require.NoError(t, err)
	assert.Equal(t, report.GetStatus(), loaded.GetStatus())
	assertPopulationBoundCampaignReport(t, loaded)
	assert.Equal(t, evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM, loaded.GetProviderObservationPolicy())
	assert.Equal(t, evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM, loaded.GetModelProvenancePolicy())
	assert.Equal(t, spec.GetCampaignDigest(), loaded.GetCampaignDigest())
	assert.Equal(t, spec.GetCatalogDigest(), loaded.GetCatalogDigest())
	assert.Equal(t, spec.GetModelRegistryDigest(), loaded.GetModelRegistryDigest())
	assert.Equal(t, []string{runID}, publication.completionRunIDs)
	require.Len(t, publication.reports, 1)
	assertPopulationBoundCampaignReport(t, publication.reports[0])
	assert.Equal(t, loaded.GetReportDigestRef().GetSha256(), publication.reports[0].GetReportDigestRef().GetSha256())
}

func TestVerifyRun_RejectsMissingRun(t *testing.T) {
	env := newStoreRunEnv(t)

	report, err := verifyRun(env.cmd, env.deps, env.store(t), "missing-run-id", false, false, false, false)
	require.Error(t, err)
	assert.Nil(t, report)
	assert.Contains(t, err.Error(), "runs verify")
}

func TestRunsVerify_ViaCLIPersistsAndPublishesPopulationBoundReport(t *testing.T) {
	env := newGatewayRunEnv(t)
	publication := env.recordPublication()
	env.createCampaign(t, "eval-a")
	env.cmd.SetOut(&bytes.Buffer{})
	_, err := runStartFlow(env.cmd, env.deps, runStartFlowOptions{CampaignID: "eval-a", RunID: "run-a-1", NoAutoBind: true})
	require.NoError(t, err)

	out := env.mustRun(t, "runs", "verify", "run-a-1")
	assert.Contains(t, out, "PASS")

	loaded, err := env.store(t).LoadCampaignVerification(context.Background(), "run-a-1")
	require.NoError(t, err)
	assertPopulationBoundCampaignReport(t, loaded)
	assert.Equal(t, []string{"run-a-1"}, publication.completionRunIDs)
	require.Len(t, publication.reports, 1)
	assert.Equal(t, loaded.GetReportDigestRef().GetSha256(), publication.reports[0].GetReportDigestRef().GetSha256())

	var payload runVerifyJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "verify", "run-a-1"))
	assert.Equal(t, "run-a-1", payload.RunID)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS.String(), payload.Status)
}

func TestRunsPublish_PublishesScheduledRun(t *testing.T) {
	env := newPublicationRunEnv(t)
	env.prepareRun(t, "eval-a", "run-a-1")

	assert.Contains(t, env.mustRun(t, "runs", "publish", "run-a-1"), "Published")
}

func TestRunsPublish_ForceRepublish(t *testing.T) {
	env := newPublicationRunEnv(t)
	env.prepareRun(t, "eval-a", "run-a-1")

	assert.Contains(t, env.mustRun(t, "runs", "publish", "run-a-1", "--force"), "forced republish")
}

func TestRunsPublish_IgnoresLegacyFailedReport(t *testing.T) {
	env := newPublicationRunEnv(t)
	env.prepareRun(t, "eval-a", "run-a-1")
	require.NoError(t, env.store(t).SaveCampaignVerification(context.Background(), "run-a-1", &evalv1.EvaluationVerificationReport{
		SchemaVersion: evaluation.CampaignSchemaVersion,
		ReportId:      "run-a-1",
		RunId:         "run-a-1",
		Status:        evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
		VerifiedAt:    timestamppb.New(time.Unix(1_700_000_200, 0).UTC()),
	}))

	env.mustRun(t, "runs", "publish", "run-a-1")
}

// A non-applicable passing report on an unsettled run is not bound, so publish
// continues; the settled-run refusal is covered by the publication real-stack tests.
func TestRunsPublish_IgnoresPersistedPassingReportThatDoesNotApplyToUnsettledRun(t *testing.T) {
	env := newPublicationRunEnv(t)
	env.prepareRun(t, "eval-a", "run-a-1")
	require.NoError(t, env.store(t).SaveCampaignVerification(context.Background(), "run-a-1", &evalv1.EvaluationVerificationReport{
		SchemaVersion:            constants.CampaignVerifierVersion,
		ReportId:                 "run-a-1",
		RunId:                    "run-a-1",
		Status:                   evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		VerifiedAt:               timestamppb.New(time.Unix(1_700_000_200, 0).UTC()),
		VerifiedPopulationDigest: strings.Repeat("9", 64),
		VerifierReleaseVersion:   constants.EvaluationSourceVersion,
		VerifierContractVersion:  constants.CampaignVerifierVersion,
		ReportDigestRef:          &compliancev1.ComplianceEvidenceReference{Sha256: strings.Repeat("a", 64)},
	}))

	env.mustRun(t, "runs", "publish", "run-a-1")
}

func TestRunsPublish_RejectsWhenGatewayUnhealthy(t *testing.T) {
	env := newPublicationRunEnv(t)
	env.prepareRun(t, "eval-a", "run-a-1")
	config.SetHTTPEndpointOverride("http://127.0.0.1:1")
	config.SetHTTPSEndpointOverride("https://127.0.0.1:1")

	_, err := env.run(t, "runs", "publish", "run-a-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runs publish")
}

func TestRunsCompare_ReportsSharedCellsAndDifferences(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.startStoredRun(t, "eval-a", "run-a-2")

	var payload runCompareJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "compare", "run-a-1", "run-a-2"))
	assert.Equal(t, "run-a-1", payload.Left.RunID)
	assert.Equal(t, "run-a-2", payload.Right.RunID)
	assert.Equal(t, testModelRoleCells(t), payload.Left.Cells)
	assert.Equal(t, testModelRoleCells(t), payload.Right.Cells)
	assert.Empty(t, payload.OnlyInLeft)
	assert.Empty(t, payload.OnlyInRight)
	assert.Zero(t, payload.Regressions)
	assert.Zero(t, payload.Improvements)

	out := env.mustRun(t, "runs", "compare", "run-a-1", "run-a-2")
	assert.Contains(t, out, fmt.Sprintf("run-a-1 (eval-a): %d cells", testModelRoleCells(t)))
	assert.Contains(t, out, "Regressions: 0  Improvements: 0")
}

func TestRunsCompare_FlagsCellsOnlyInOneRun(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	other := &evalv1.ModelVariant{
		VariantId:      "gemma3-1b",
		ServedModelTag: "gemma3:1b",
		ModelDigest:    repeatTestHex('3'),
		ProviderClass:  "ollama",
	}
	_, err := createCampaign(context.Background(), env.deps, env.fileSvc(t), campaignCreateSpec{
		Platform:    testPlatform,
		CampaignID:  "eval-b",
		Variants:    []*evalv1.ModelVariant{other},
		Repetitions: 1,
	})
	require.NoError(t, err)
	env.startStoredRun(t, "eval-b", "run-b-1")

	var payload runCompareJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "compare", "run-a-1", "run-b-1"))
	assert.Len(t, payload.OnlyInLeft, testModelRoleCells(t), "no cell of a different model is shared")
	assert.Len(t, payload.OnlyInRight, testModelRoleCells(t))
}

func TestRunsCompare_RequiresTwoRuns(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "runs", "compare", "only-one")
	require.Error(t, err)
}

func TestPublicRestore_RequiresTarget(t *testing.T) {
	env := newStoreRunEnv(t)

	command := PublicRestoreCmdWithConfig(env.deps.configLoader, env.deps.fileSvcFactory)
	command.SetArgs([]string{"--project-root", env.root})
	err := command.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}

func TestPublicRestore_RejectsBothTargets(t *testing.T) {
	env := newStoreRunEnv(t)

	command := PublicRestoreCmdWithConfig(env.deps.configLoader, env.deps.fileSvcFactory)
	command.SetArgs([]string{"--project-root", env.root, "--queue", "--run-id", "run-1"})
	err := command.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}

func TestResolveRunWitnessPoliciesPreservesStrictRunPolicy(t *testing.T) {
	run := &evalv1.EvaluationRun{
		ProviderObservationPolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_STRICT,
		ModelProvenancePolicy:     evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_STRICT,
	}
	observation, provenance, err := resolveRunWitnessPolicies(run, false, false)
	require.NoError(t, err)
	assert.True(t, observation)
	assert.True(t, provenance)
}

func TestResolveRunWitnessPoliciesRejectsUnspecifiedStoredPolicies(t *testing.T) {
	_, _, err := resolveRunWitnessPolicies(&evalv1.EvaluationRun{
		ProviderObservationPolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
	}, false, false)
	require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}
