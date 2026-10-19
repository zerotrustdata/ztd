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
	"fmt"
	"log/slog"
	"net/http"
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
	"github.com/zerotrustdata/ztd/internal/models"
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
			ActuatorPublicKey: testInferenceOperatorActuatorKey,
			ActuatorKeyId:     testInferenceOperatorActuatorKey,
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

// writeGatewayHealthResponse answers the posture preflight with the doctrine
// posture every materialized campaign freezes.
func writeGatewayHealthResponse(w http.ResponseWriter, r *http.Request) bool {
	return writeGatewayPostureResponse(w, r, constants.PostureDoctrine)
}

func writeGatewayPostureResponse(w http.ResponseWriter, r *http.Request, posture constants.Posture) bool {
	if r.Method != http.MethodGet || r.URL.Path != constants.APIPaths.Health {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"posture":"` + string(posture) + `"}`))
	return true
}

func writeWitnessPreflightResponse(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.SSEStream:
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return true
	case r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.InferenceProviderObservations+"_preflight":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
		return true
	default:
		return false
	}
}

// fakeRunStreamUserID and fakeRunStreamCLISessionID are the owner identity the
// fake Gateway's CLI session stream routes to. writeGatewayCredentials enrolls
// the same identity.
const (
	fakeRunStreamUserID       = "user-1"
	fakeRunStreamCLISessionID = "cli-1"
)

// fakeRunStream is the Gateway's CLI session event stream as the fake Gateway
// serves it. Every connection stays open and receives the events published while
// it is connected, and nothing is replayed, as with since_id=0. The producer
// endpoints publish what the real Gateway emits for them: a stop request becomes
// ai.eval.stop.requested and a run-state update becomes app.run.status.updated.
// Tests can also publish a status directly, the way the executing process's
// release does.
type fakeRunStream struct {
	mu    sync.Mutex
	conns map[chan string]struct{}
	// stops and states receive each producer request as it is accepted.
	stops  chan models.ObserveProducerStopRequest
	states chan models.ObserveProducerRunStateRequest
	// rejectRunState, when non-zero, is the HTTP status every run-state update is
	// answered with instead of being accepted.
	rejectRunState int
}

// fakeSSEEvent is the {type, data} envelope the Gateway wraps a published event in.
type fakeSSEEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

func newFakeRunStream() *fakeRunStream {
	return &fakeRunStream{
		conns:  make(map[chan string]struct{}),
		stops:  make(chan models.ObserveProducerStopRequest, 64),
		states: make(chan models.ObserveProducerRunStateRequest, 64),
	}
}

// serve holds one CLI session stream connection open and writes each event
// published to it. It registers before the headers flush, so an event published
// after the client connects is never lost.
func (s *fakeRunStream) serve(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	frames := make(chan string, 64)
	s.mu.Lock()
	s.conns[frames] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, frames)
		s.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case frame := <-frames:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
			flusher.Flush()
		}
	}
}

// publish sends one event envelope to every open connection. Frames are sent
// without blocking, so a connection that is not reading loses the event rather
// than stalling the producer.
func (s *fakeRunStream) publish(eventType string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("fake run stream: marshal %s: %w", eventType, err)
	}
	event, err := json.Marshal(fakeSSEEvent{Type: eventType, Data: data})
	if err != nil {
		return fmt.Errorf("fake run stream: envelope %s: %w", eventType, err)
	}
	frame, err := json.Marshal(models.SSEPushPayload{UserID: fakeRunStreamUserID, CliSessionID: fakeRunStreamCLISessionID, Event: event})
	if err != nil {
		return fmt.Errorf("fake run stream: frame %s: %w", eventType, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn := range s.conns {
		select {
		case conn <- string(frame):
		default:
		}
	}
	return nil
}

// publishRunStatus publishes an app.run.status.updated event for runID, as the
// executing process does when it releases the run.
func (s *fakeRunStream) publishRunStatus(t *testing.T, runID string, status models.RunLifecycleStatus) {
	t.Helper()
	require.NoError(t, s.publish(string(constants.EventAppRunStatusUpdated), models.RunStatusUpdatedPayload{
		SchemaVersion: constants.ObserveEventPayloadSchemaVersion,
		RunID:         runID,
		RunKind:       models.RunKindEval,
		DisplayName:   runID,
		Status:        status,
		ObservedAt:    time.Now().UTC(),
	}))
}

// acceptStop records a stop request and publishes the ai.eval.stop.requested
// event the Gateway emits for it.
func (s *fakeRunStream) acceptStop(w http.ResponseWriter, r *http.Request) {
	var req models.ObserveProducerStopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	if err := s.publish(string(constants.EventAiEvalStopRequested), models.EvalStopRequestedPayload{
		SchemaVersion: req.SchemaVersion,
		CampaignID:    req.CampaignID,
		RunID:         req.RunID,
		StopReason:    req.StopReason,
		StopScope:     req.StopScope,
		RequestedAt:   now,
		ObservedAt:    now,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	select {
	case s.stops <- req:
	default:
	}
	writeProducerAccepted(w)
}

// acceptRunState records a run-state update and publishes the app.run.status.updated
// event the Gateway emits for it.
func (s *fakeRunStream) acceptRunState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	reject := s.rejectRunState
	s.mu.Unlock()
	if reject != 0 {
		http.Error(w, "run state rejected", reject)
		return
	}
	var req models.ObserveProducerRunStateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.publish(string(constants.EventAppRunStatusUpdated), models.RunStatusUpdatedPayload{
		SchemaVersion:  req.SchemaVersion,
		RunID:          req.RunID,
		RunKind:        req.RunKind,
		DisplayName:    req.DisplayName,
		Status:         req.Status,
		ActiveTaskID:   req.ActiveTaskID,
		CompletedTasks: req.CompletedTasks,
		TotalTasks:     req.TotalTasks,
		StartedAt:      req.StartedAt,
		EndedAt:        req.EndedAt,
		ObservedAt:     req.ObservedAt,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	select {
	case s.states <- req:
	default:
	}
	writeProducerAccepted(w)
}

// awaitStop returns the next stop request the fake Gateway accepted.
func (s *fakeRunStream) awaitStop(t *testing.T) models.ObserveProducerStopRequest {
	t.Helper()
	select {
	case req := <-s.stops:
		return req
	case <-time.After(10 * time.Second):
		require.FailNow(t, "no stop request reached the Gateway")
		return models.ObserveProducerStopRequest{}
	}
}

// rejectRunStates makes the fake Gateway answer every later run-state update with
// status, so a run's report to the Gateway fails.
func (s *fakeRunStream) rejectRunStates(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectRunState = status
}

// awaitRunState returns the next run-state update the fake Gateway accepted.
func (s *fakeRunStream) awaitRunState(t *testing.T) models.ObserveProducerRunStateRequest {
	t.Helper()
	select {
	case req := <-s.states:
		return req
	case <-time.After(10 * time.Second):
		require.FailNow(t, "no run-state update reached the Gateway")
		return models.ObserveProducerRunStateRequest{}
	}
}

func writeProducerAccepted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"accepted":true}`))
}

// runEnv is one project root wired to a fake Gateway and a fake process
// boundary, with testQwenVariant in its model registry.
type runEnv struct {
	root    string
	deps    nativeEvalDeps
	cmd     *cobra.Command
	control *fakeProcessControl
	// stream is the Gateway's CLI session event stream, served once the fake
	// Gateway executes.
	stream *fakeRunStream
	// operatorSource lists the operators the fake Gateway reports, read on each
	// request.
	operatorSource func() []*operatorv1.OperatorDocument
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
	_, err := createCampaign(context.Background(), e.deps, e.fileSvc(t), campaignCreateSpec{
		Platform:    testPlatform,
		CampaignID:  campaignID,
		Variants:    []*evalv1.ModelVariant{testQwenVariant()},
		Repetitions: 1,
		SuiteID:     suiteID,
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
		UserID:            fakeRunStreamUserID,
		OperatorID:        "data-op",
		CLISessionID:      fakeRunStreamCLISessionID,
	}))
	writeEvalAppCredentials(t, e)
}

// writeEvalAppCredentials installs the ztd-eval application identity, which the
// run's lifecycle reports and stop requests present.
func writeEvalAppCredentials(t *testing.T, e *runEnv) {
	t.Helper()
	fileSvc := e.fileSvc(t)
	cfg, err := e.deps.configLoader(e.root)
	require.NoError(t, err)

	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cmdtest.WriteSealedKey(t, fileSvc, cfg, cfg.AppKeyFile(constants.EvaluationAppName), keyPEM)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cmdtest.GenerateApproveTestCertDER(t, priv)})
	require.NoError(t, fileSvc.WriteFile(context.Background(), cmdtest.MustRel(t, fileSvc, cfg.AppCertFile(constants.EvaluationAppName)), certPEM, constants.PermFilePrivate))

	// The app certificate is self-anchored, so the trust bundle must carry it for
	// the identity to load as trusted.
	bundle, err := fileSvc.ReadFile(context.Background(), cfg.DefaultTrustBundleRelPath())
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), cfg.DefaultTrustBundleRelPath(), append(bundle, certPEM...), constants.PermFilePrivate))
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
	assert.Equal(t, uint32(testModelRoleCells(t)), report.GetExpectedAssignmentCount())
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

// testReleaseVersion is the build release the test harness runs commands as.
const testReleaseVersion = "v2.3.0"

// testVersionContext carries the build identity the root command attaches in production.
func testVersionContext() context.Context {
	return shared.ContextWithVersionInfo(context.Background(), serve.VersionInfo{Version: testReleaseVersion, SourceRevision: string(constants.SystemHealthUnknown)})
}

// testPlatform is the build identity campaign fixtures freeze under.
var testPlatform = evaluation.PlatformIdentity{Release: testReleaseVersion}
