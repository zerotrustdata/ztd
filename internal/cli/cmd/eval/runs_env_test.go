// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	testHost = "test-host"
	testPID  = 4242
)

// fakeProcessControl is the run-control process boundary for tests. It makes
// lease liveness deterministic: a lease is live when its host is testHost and
// its PID is marked alive.
type fakeProcessControl struct {
	mu         sync.Mutex
	pid        int
	host       string
	alive      map[int]bool
	interrupts []int
	// exits holds, per PID, the channel that WaitExit blocks on until exit closes it.
	exits map[int]chan struct{}
}

func newFakeProcessControl() *fakeProcessControl {
	return &fakeProcessControl{pid: testPID, host: testHost, alive: map[int]bool{testPID: true}, exits: make(map[int]chan struct{})}
}

func (f *fakeProcessControl) PID() int { return f.pid }

func (f *fakeProcessControl) Hostname() (string, error) { return f.host, nil }

func (f *fakeProcessControl) Alive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[pid]
}

func (f *fakeProcessControl) Interrupt(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interrupts = append(f.interrupts, pid)
	return nil
}

func (f *fakeProcessControl) setAlive(pid int, alive bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alive[pid] = alive
}

func (f *fakeProcessControl) interrupted() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.interrupts...)
}

// exitChan returns the channel that closes when pid exits. Callers hold f.mu.
func (f *fakeProcessControl) exitChan(pid int) chan struct{} {
	ch, ok := f.exits[pid]
	if !ok {
		ch = make(chan struct{})
		f.exits[pid] = ch
	}
	return ch
}

// WaitExit blocks until exit is called for pid, or until ctx ends.
func (f *fakeProcessControl) WaitExit(ctx context.Context, pid int) error {
	f.mu.Lock()
	exited := f.exitChan(pid)
	f.mu.Unlock()
	select {
	case <-exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// exit makes pid die: it is no longer alive, and WaitExit returns for it. Nothing
// is published on the run's stream, which is what a process that dies without
// releasing its run looks like.
func (f *fakeProcessControl) exit(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alive[pid] = false
	if ch := f.exitChan(pid); !isClosed(ch) {
		close(ch)
	}
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func testRunControl(control *fakeProcessControl) runControlDeps {
	return runControlDeps{
		processFactory: func(fs.RuntimeFileService) (processControl, error) { return control, nil },
	}
}

func testQwenVariant() *evalv1.ModelVariant {
	return &evalv1.ModelVariant{
		VariantId:      "qwen3-4b",
		ServedModelTag: "qwen3:4b",
		ModelDigest:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ProviderClass:  "ollama",
	}
}

// runEnv is one project root with a frozen model registry and a deterministic
// process-control boundary. Gateway tests install the real stack explicitly.
type runEnv struct {
	root             string
	deps             nativeEvalDeps
	cmd              *cobra.Command
	control          *fakeProcessControl
	dataOperatorID   string
	dataSession      string
	inferenceSession string
}

func (e *runEnv) fileSvc(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	fileSvc, err := e.deps.fileSvcFactory(e.root, slog.Default())
	require.NoError(t, err)
	return fileSvc
}

func (e *runEnv) store(t *testing.T) *evaluation.Store {
	t.Helper()
	return evaluation.NewStore(e.fileSvc(t))
}

// run executes a ztd eval command line and returns what it printed.
func (e *runEnv) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := evalCmdWithConfig(e.deps)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(append(append([]string{}, args...), "--project-root", e.root))
	err := command.ExecuteContext(testVersionContext())
	return out.String(), err
}

// runJSON executes a ztd eval command line with the global --json flag and
// decodes its single JSON document into payload.
func (e *runEnv) runJSON(t *testing.T, payload any, args ...string) error {
	t.Helper()
	rootCmd := cmdtest.GlobalJSONRoot(t, evalCmdWithConfig(e.deps))
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(append(append([]string{"eval"}, args...), "--project-root", e.root))
	cmdErr := rootCmd.ExecuteContext(testVersionContext())
	if out.Len() > 0 {
		decoder := json.NewDecoder(&out)
		require.NoError(t, decoder.Decode(payload), out.String())
	}
	if cmdErr != nil {
		return cmdErr
	}
	return nil
}

func (e *runEnv) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := e.run(t, args...)
	require.NoError(t, err, out)
	return out
}

// createCampaign freezes a model-role campaign over testQwenVariant, the one
// lane runs execute.
func (e *runEnv) createCampaign(t *testing.T, campaignID string) {
	t.Helper()
	e.createSuiteCampaign(t, campaignID, "")
}

// createSuiteCampaign is createCampaign over the named suite; empty is the
// default suite.
func (e *runEnv) createSuiteCampaign(t *testing.T, campaignID, suiteID string) {
	t.Helper()
	var investigation *evalv1.EvaluationInvestigation
	if e.dataOperatorID != "" {
		investigation = &evalv1.EvaluationInvestigation{
			Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
			InvestigationId: "test-investigation-" + campaignID,
			DataOperatorId:  e.dataOperatorID,
		}
	}
	_, err := createCampaign(context.Background(), e.deps, e.fileSvc(t), campaignCreateSpec{
		Platform:      testPlatform,
		CampaignID:    campaignID,
		Variants:      []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions:   1,
		SuiteID:       suiteID,
		Investigation: investigation,
	})
	require.NoError(t, err)
}

// testModelRoleCells is the assignment count of one run of a default-suite
// campaign over one model at one repetition: one cell per (scenario, eligible
// role).
func testModelRoleCells(t *testing.T) int {
	t.Helper()
	catalog, _, err := evaluation.LoadScenarioCatalog()
	require.NoError(t, err)
	return int(evaluation.CatalogModelRoleCells(catalog))
}

// prepareRun creates a campaign and starts a prepared-only run of it: the run
// and its assignment matrix are persisted and nothing executes.
func (e *runEnv) prepareRun(t *testing.T) string {
	t.Helper()
	e.createCampaign(t, "eval-a")
	return e.startPrepared(t, "eval-a", "run-a-1")
}

func (e *runEnv) startPrepared(t *testing.T, campaignID, runID string) string {
	t.Helper()
	e.cmd.SetOut(&bytes.Buffer{})
	result, err := runStartFlow(e.cmd, e.deps, runStartFlowOptions{CampaignID: campaignID, RunID: runID, PrepareOnly: true})
	require.NoError(t, err)
	require.True(t, result.Prepared)
	return result.RunID
}

func repeatTestHex(ch byte) string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = ch
	}
	return string(out)
}

// testReleaseVersion is the build release the test harness runs commands as.
const testReleaseVersion = "v2.3.0"

// testVersionContext carries the build identity the root command attaches in production.
func testVersionContext() context.Context {
	return shared.ContextWithVersionInfo(context.Background(), serve.VersionInfo{Version: testReleaseVersion, SourceRevision: string(constants.SystemHealthUnknown)})
}

// testPlatform is the build identity campaign fixtures freeze under.
var testPlatform = evaluation.PlatformIdentity{Release: testReleaseVersion}

// testGovernance is the unbound doctrine Gateway governance store-only runs freeze.
var testGovernance = evaluation.RunGovernance{
	Posture:   evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
	Consensus: &evalv1.EvaluationConsensusIdentity{},
}

// firstQueuedAssignment is the assignment a run executes next.
func (e *runEnv) firstQueuedAssignment(t *testing.T, runID string) *evalv1.EvaluationAssignment {
	t.Helper()
	controller := evaluation.NewCampaignController(e.store(t), nil, e.deps.now, adaptNewID(e.deps.newID))
	assignment, ok, err := controller.ResumeNextAssignment(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, ok)
	return assignment
}
