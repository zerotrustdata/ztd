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
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	testInferenceSession             = "infer-session"
	testDataSession                  = "data-session"
	testDataOperatorWorkingDirectory = "/home/operator"
	testHost                         = "test-host"
	testPID                          = 4242
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
}

func newFakeProcessControl() *fakeProcessControl {
	return &fakeProcessControl{pid: testPID, host: testHost, alive: map[int]bool{testPID: true}}
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

func testRunControl(control *fakeProcessControl) runControlDeps {
	return runControlDeps{
		processFactory: func(fs.RuntimeFileService) (processControl, error) { return control, nil },
		pollInterval:   5 * time.Millisecond,
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

func testRunOperators() []*operatorv1.OperatorDocument {
	return []*operatorv1.OperatorDocument{
		{
			Id:                "infer-op",
			OperatorSessionId: testInferenceSession,
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
				Capabilities:            []string{string(constants.OperatorCapabilityInference)},
				InferenceOllamaEndpoint: "http://provider.example:11434",
			},
		},
		{
			Id:                      "data-op",
			OperatorSessionId:       testDataSession,
			Status:                  string(constants.OperatorStatusActive),
			OperatorType:            string(constants.OperatorTypeRemote),
			RuntimeConfig:           &operatorv1.OperatorRuntimeConfig{},
			LatestHeartbeatSnapshot: &operatorv1.HeartbeatResult{Environment: &operatorv1.EnvironmentDetails{Pwd: testDataOperatorWorkingDirectory}},
			ActuatorPublicKey:       testDataOperatorActuatorKey,
			ActuatorKeyId:           testDataOperatorActuatorKey,
		},
	}
}

func writePublicationProofResponse(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case constants.APIPaths.PublicFeedProofs, constants.APIPaths.PublicFeedProofsBatch, constants.APIPaths.PublicFeedProofsPush:
		if r.Method != http.MethodPost {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true}`))
		return true
	default:
		return false
	}
}

func writeWitnessPreflightResponse(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.SSEStream:
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return true
	case r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.InferenceProviderObservations+"_preflight",
		r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.InferenceModelProvenanceAttestations+"_preflight",
		r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, constants.APIPaths.InferenceModelProvenanceAttestations+"_attest"):
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
		return true
	default:
		return false
	}
}

// runEnv is one project root wired to a fake Gateway and a fake process
// boundary, with the formation catalog variants in its model registry.
type runEnv struct {
	root    string
	deps    nativeEvalDeps
	cmd     *cobra.Command
	control *fakeProcessControl
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

// testFormationID is the single formation the default test campaign runs: one
// stack, so a run schedules one assignment per scenario.
const testFormationID = "ultra-efficient-speedster"

// createCampaign freezes a one-formation system-lane campaign, the only lane
// runs execute.
func (e *runEnv) createCampaign(t *testing.T, campaignID string) {
	t.Helper()
	_, err := createCampaign(context.Background(), e.deps, e.fileSvc(t), campaignCreateSpec{
		Platform:     testPlatform,
		CampaignID:   campaignID,
		Variants:     testFormationCatalogCLIVariants(),
		Repetitions:  1,
		Formations:   true,
		FormationIDs: []string{testFormationID},
	})
	require.NoError(t, err)
}

// prepareRun creates a campaign and starts a prepared-only run of it: the run
// and its assignment matrix are persisted and nothing executes.
func (e *runEnv) prepareRun(t *testing.T, campaignID, runID string) string {
	t.Helper()
	e.createCampaign(t, campaignID)
	return e.startPrepared(t, campaignID, runID)
}

func (e *runEnv) startPrepared(t *testing.T, campaignID, runID string) string {
	t.Helper()
	e.cmd.SetOut(&bytes.Buffer{})
	result, err := runStartFlow(e.cmd, e.deps, runStartFlowOptions{CampaignID: campaignID, RunID: runID, PrepareOnly: true})
	require.NoError(t, err)
	require.True(t, result.Prepared)
	return result.RunID
}

func writeGatewayCredentials(t *testing.T, e *runEnv) {
	t.Helper()
	fileSvc := e.fileSvc(t)
	cfg, err := e.deps.configLoader(e.root)
	require.NoError(t, err)

	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cmdtest.WriteSealedKey(t, fileSvc, cfg, cfg.CLIKeyFile(), keyPEM)

	certDER := cmdtest.GenerateApproveTestCertDER(t, priv)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	require.NoError(t, fileSvc.WriteFile(context.Background(), cmdtest.MustRel(t, fileSvc, cfg.CLICertFile()), certPEM, constants.PermFilePrivate))
	require.NoError(t, fileSvc.WriteFile(context.Background(), cfg.DefaultTrustBundleRelPath(), certPEM, constants.PermFilePrivate))

	require.NoError(t, auth.SaveCredentials(fileSvc, cfg, &auth.Credentials{
		OperatorSessionID: testDataSession,
		UserID:            "user-1",
		OperatorID:        "data-op",
		CLISessionID:      "cli-1",
	}))
}

type recordingCampaignVerificationPublication struct {
	completionRunIDs []string
	reports          []*evalv1.EvaluationVerificationReport
}

func (p *recordingCampaignVerificationPublication) PublishRunCompletion(_ context.Context, runID string, _ time.Time) (int, error) {
	p.completionRunIDs = append(p.completionRunIDs, runID)
	return 1, nil
}

func (p *recordingCampaignVerificationPublication) PublishRunVerification(_ context.Context, _ string, report *evalv1.EvaluationVerificationReport) (int, error) {
	p.reports = append(p.reports, report)
	return 1, nil
}

// recordPublication routes verification publication to a recorder.
func (e *runEnv) recordPublication() *recordingCampaignVerificationPublication {
	publication := &recordingCampaignVerificationPublication{}
	e.deps.campaignPublicationFactory = func(*cobra.Command, fs.RuntimeFileService, *config.Config) (campaignVerificationPublication, error) {
		return publication, nil
	}
	return publication
}

func assertPopulationBoundCampaignReport(t *testing.T, report *evalv1.EvaluationVerificationReport) {
	t.Helper()
	require.NotNil(t, report)
	assert.Equal(t, constants.CampaignVerifierVersion, report.GetSchemaVersion())
	assert.Equal(t, constants.CampaignVerifierVersion, report.GetVerifierContractVersion())
	assert.Equal(t, constants.EvaluationSourceVersion, report.GetVerifierReleaseVersion())
	assert.Equal(t, uint32(evaluation.DefaultSuiteScenarioCount), report.GetExpectedAssignmentCount())
	assert.Equal(t, uint32(1), report.GetVerifiedAssignmentCount())
	assert.NotEmpty(t, report.GetVerifiedPopulationDigest())
	assert.NotEmpty(t, report.GetCampaignDigest())
	assert.NotEmpty(t, report.GetCatalogDigest())
	assert.NotEmpty(t, report.GetModelRegistryDigest())
	require.NotNil(t, report.GetReportDigestRef())
	assert.Len(t, report.GetReportDigestRef().GetSha256(), 64)
}

func repeatTestHex(ch byte) string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = ch
	}
	return string(out)
}

func testFormationCatalogCLIVariants() []*evalv1.ModelVariant {
	digestByTag := map[string]byte{
		"qwen3.5:9b":     '1',
		"ministral-3:3b": '2',
		"gemma3:1b":      '3',
		"deepseek-r1:7b": '4',
		"llama3.2:1b":    '5',
		"gemma4:e4b":     '6',
		"qwen2.5:7b":     '7',
		"llama3.1:8b":    '8',
		"phi4-mini:3.8b": '9',
		"qwen3.5:4b":     'b',
		"gemma4:e2b":     'c',
	}
	return evaluation.FormationCatalogFixtureVariants(func(tag string) string {
		ch := digestByTag[tag]
		if ch == 0 {
			ch = 'a'
		}
		return repeatTestHex(ch)
	})
}

// testFormationCatalogCLITags returns the served model tags of
// testFormationCatalogCLIVariants in the sorted order campaigns report them.
func testFormationCatalogCLITags() []string {
	variants := testFormationCatalogCLIVariants()
	tags := make([]string, 0, len(variants))
	for _, variant := range variants {
		tags = append(tags, variant.GetServedModelTag())
	}
	sort.Strings(tags)
	return tags
}

// testReleaseVersion is the build release the test harness runs commands as.
const testReleaseVersion = "v2.3.0"

// testVersionContext carries the build identity the root command attaches in production.
func testVersionContext() context.Context {
	return shared.ContextWithVersionInfo(context.Background(), serve.VersionInfo{Version: testReleaseVersion, SourceRevision: string(constants.SystemHealthUnknown)})
}

// testPlatform is the build identity campaign fixtures freeze under.
var testPlatform = evaluation.PlatformIdentity{Release: testReleaseVersion}
