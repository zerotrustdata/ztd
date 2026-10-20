// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// External package: the executor is wired from the production seams the eval
// runner uses, and the drive sentinel imports evaluation, which an in-package
// test could not do without an import cycle.
package evaluation_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	cliop "github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/drive"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	harnessconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	"github.com/zerotrustdata/ztd/internal/uuid"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

const (
	realStackPersonaID  = "ztd-eval-campaign"
	realStackCampaignID = "smoke-campaign"
	realStackScenarioID = "tool-arg-shell-command"
	realStackModelTag   = "qwen3:4b"
	realStackModelHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// realStackRenderedHealthPath matches the absolute health fixture path the
// scenario prompt renders for one attempt's workspace.
var realStackRenderedHealthPath = regexp.MustCompile(`/\S+?/workspaces/ws-[0-9a-f]{16}/status/health\.txt`)

// realStackShellArguments is the run_shell_command argument body for one command
// and its arguments. A struct of strings cannot fail to marshal.
func realStackShellArguments(command string, args ...string) json.RawMessage {
	body, _ := json.Marshal(models.RunShellCommandRequest{Command: command, Args: args})
	return body
}

// realStackHealthCatReply cats the health fixture at the absolute path the
// decoded request's prompt names. The path is the one the Data Operator
// materialized for this attempt, so the shell reads the real workspace file.
func realStackHealthCatReply(request fixtures.OllamaChatRequest) fixtures.OllamaReply {
	for _, message := range request.Messages {
		if path := realStackRenderedHealthPath.FindString(message.Content); path != "" {
			return fixtures.OllamaReply{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolRunShellCommand, Arguments: realStackShellArguments("cat", path)}}}
		}
	}
	return fixtures.OllamaReply{Failure: "scripted provider: the prompt names no workspace health fixture"}
}

// realStackAttempts records the attempt IDs the controller mints, in order. The
// provider hook reads it from the Inference Operator's request goroutine.
type realStackAttempts struct {
	mu  sync.Mutex
	ids []string
}

func (a *realStackAttempts) add(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ids = append(a.ids, id)
}

func (a *realStackAttempts) latest() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.ids) == 0 {
		return ""
	}
	return a.ids[len(a.ids)-1]
}

// inFlightWorkspaceFile returns the absolute path of one workspace-relative
// fixture in the workspace of the attempt the Operator is executing. Investigation
// prompts do not render the workspace root, so the root is derived from the
// attempt the controller minted for the run in flight.
func (env *realStackAgentRun) inFlightWorkspaceFile(relPath string) (string, error) {
	workspace, err := evaluation.NewScenarioWorkspace(env.workingDirectory, env.req.RunID, env.attempts.latest())
	if err != nil {
		return "", err
	}
	return workspace.FilePath(relPath)
}

// realStackReadFileReply is one read_file call on a workspace-relative fixture of
// the attempt in flight.
func realStackReadFileReply(env *realStackAgentRun, relPath string) fixtures.OllamaReply {
	path, err := env.inFlightWorkspaceFile(relPath)
	if err != nil {
		return fixtures.OllamaReply{Failure: err.Error()}
	}
	// A struct of one string cannot fail to marshal.
	args, _ := json.Marshal(struct {
		Path string `json:"path"`
	}{Path: path})
	return fixtures.OllamaReply{ToolCalls: []fixtures.OllamaToolCall{{Name: "read_file", Arguments: args}}}
}

// shellReplies is the frozen provider script for one answer-free tool turn: the
// model runs run_shell_command on the health fixture its prompt names, then
// answers from the output.
// shellRepliesFor scripts one shell turn pair for each scheduled assignment:
// every assignment consumes its own tool call and final answer.
func shellRepliesFor(assignments int) []fixtures.OllamaReply {
	var replies []fixtures.OllamaReply
	for range assignments {
		replies = append(replies, shellReplies()...)
	}
	return replies
}

func shellReplies() []fixtures.OllamaReply {
	return []fixtures.OllamaReply{
		{Respond: realStackHealthCatReply},
		{Content: "HEALTHY"},
	}
}

// realStackInference forwards governed inference and Tribunal intents from the
// application identity to the production harness client, as the eval runner does.
type realStackInference struct {
	client       *harnessclient.Client
	cliSessionID string
}

func (d realStackInference) DispatchInference(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	response, _, err := d.client.DispatchInference(ctx, req)
	return response, err
}

func (d realStackInference) DispatchIntent(ctx context.Context, req *tribunalv1.TribunalIntentRequest) (*tribunalv1.TribunalIntentResult, error) {
	req.CliSessionId = d.cliSessionID
	result, _, err := d.client.DispatchIntent(ctx, req)
	return result, err
}

// realStackOptions selects the campaign the rig initializes. Zero values keep
// the smoke default: tool-arg-shell-command from the default catalog, both
// roles, one repetition, doctrine posture, distinct runtimes.
type realStackOptions struct {
	posture    constants.Posture
	shared     bool
	singleRole bool
	suffix     string
	replies    []fixtures.OllamaReply
	// catalog loads the frozen catalog; nil means evaluation.LoadScenarioCatalog.
	catalog func() (*evalv1.EvaluationScenarioCatalog, map[string]evaluation.ScenarioArtifacts, error)
	// scenarioID names the one scenario the run keeps; empty means realStackScenarioID.
	scenarioID string
	// scenario edits the kept scenario before the catalog digest is recomputed.
	scenario func(*evalv1.EvaluationScenarioDefinition)
	// repetitions is the campaign RepetitionCount; zero means 1.
	repetitions uint32
	// request edits the init request after the catalog is frozen into it. It
	// receives the Data Operator ID, so a persistent investigation can name the
	// ledger it runs against before InitializeCampaign freezes the spec.
	request func(req *evaluation.CampaignInitRequest, dataOperatorID string)
	// denyHumanApprovals makes the owner deny every suspended L3 transaction.
	denyHumanApprovals bool
}

// realStackAgentRun is a campaign initialized on the real eval stack, with the
// executor and sentinel built from the production seams.
type realStackAgentRun struct {
	stack          *fixtures.EvalStack
	store          *evaluation.Store
	files          fs.RuntimeFileService
	controller     *evaluation.CampaignController
	req            evaluation.CampaignInitRequest
	binding        evaluation.CampaignExecutionBinding
	scheduledCount int
	clock          func() time.Time
	gateway        *harnessclient.Client
	appClient      *harnessclient.Client
	inference      realStackInference
	sentinel       *drive.Sentinel
	persona        harnessclient.Persona
	dataOperatorID string
	dataSessionID  string
	// catalog and artifacts are the frozen scenario inputs every run on this rig shares.
	catalog   *evalv1.EvaluationScenarioCatalog
	artifacts map[string]evaluation.ScenarioArtifacts
	// topology is the Inference and Data binding every run on this rig uses.
	topology           evaluation.CampaignOperatorTopology
	inferenceSessionID string
	workingDirectory   string
	attempts           *realStackAttempts
}

// realStackRunPlan names one campaign run started on an existing rig.
type realStackRunPlan struct {
	campaignID  string
	runID       string
	repetitions uint32
	singleRole  bool
	request     func(req *evaluation.CampaignInitRequest, dataOperatorID string)
}

func newRealStackAgentRun(t *testing.T, opts realStackOptions) *realStackAgentRun {
	t.Helper()
	ctx := t.Context()
	stack := fixtures.NewEvalStack(t, fixtures.EvalStackOptions{
		Posture: opts.posture, Replies: opts.replies, SharedRuntime: opts.shared,
	})

	logger := testutil.NewTestLogger()
	root := testutil.TempDir(t)
	keyring := keystoretest.NewMemoryKeyring()
	cfg, err := cliconfig.Load(root)
	require.NoError(t, err)
	cfg.Keyring = keyring
	fileSvc, err := fs.NewRuntimeFileService(root, logger)
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(ctx))
	keyStore, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, keyStore.Initialize())
	require.NoError(t, keyStore.InitVault())
	stack.Identity.InstallCLIRuntime(t, stack.Gateway, fileSvc, cfg)
	stack.EnrollEvalAppIdentity(t, fileSvc, cfg)
	evidenceFiles, err := shared.EvidenceFileSvcFactory(keystore.Options{Keyring: keyring})(root, logger)
	require.NoError(t, err)

	authContext, err := auth.LoadClientAuthContext(fileSvc, cfg)
	require.NoError(t, err)
	cliCredentials := harnessconfig.Auth{ClientCert: authContext.ClientCert, ClientKey: authContext.ClientKey, Certificate: authContext.Certificate, CABundle: cfg.ResolvedTrustBundlePath()}
	gateway, err := harnessclient.New(harnessconfig.Config{
		MTLSBaseURL: cfg.OperatorHTTPURL(), PublicBaseURL: cfg.OperatorDiscoveryURL(),
		Auth: cliCredentials, CLIAuth: cliCredentials, UseCLIConfig: true,
		UserID: authContext.UserID, CLISessionID: authContext.CLISessionID, OperatorSessionID: authContext.OperatorSessionID,
	})
	require.NoError(t, err)
	appCertificate, err := auth.LoadAppIdentity(fileSvc, cfg, constants.EvaluationAppName)
	require.NoError(t, err)
	appClient, err := harnessclient.New(harnessconfig.Config{
		MTLSBaseURL: cfg.OperatorHTTPURL(),
		Auth:        harnessconfig.Auth{Certificate: appCertificate, CABundle: cfg.ResolvedTrustBundlePath()},
		UserID:      authContext.UserID,
	})
	require.NoError(t, err)

	inferenceWorkload, err := stack.Inference.Workload()
	require.NoError(t, err)
	dataWorkload, err := stack.Data.Workload()
	require.NoError(t, err)
	operators, _, err := gateway.ListOperators(ctx)
	require.NoError(t, err)
	topology, err := evaluation.ResolveCampaignOperatorTopologyForDataOperator(operators, inferenceWorkload.OperatorSessionID, dataWorkload.OperatorID)
	require.NoError(t, err)
	topology, err = topology.WithAnchors(operators)
	require.NoError(t, err)

	loadCatalog := opts.catalog
	if loadCatalog == nil {
		loadCatalog = evaluation.LoadScenarioCatalog
	}
	catalog, artifacts, err := loadCatalog()
	require.NoError(t, err)
	scenarioID := opts.scenarioID
	if scenarioID == "" {
		scenarioID = realStackScenarioID
	}
	// Keep the run to the one scenario under test; the catalog digest is
	// recomputed over the truncated catalog.
	var target *evalv1.EvaluationScenarioDefinition
	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetScenarioId() == scenarioID {
			target = scenario
			break
		}
	}
	require.NotNil(t, target, "scenario %s must exist in catalog", scenarioID)
	if opts.singleRole {
		target.EligibleRoles = []evalv1.ModelCampaignRole{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY}
	}
	if opts.scenario != nil {
		opts.scenario(target)
	}
	truncated := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: catalog.GetSchemaVersion(),
		CatalogRef:    catalog.GetCatalogRef(),
		Scenarios:     []*evalv1.EvaluationScenarioDefinition{target},
	}
	truncated.CatalogDigest, err = evaluation.ComputeScenarioCatalogDigest(truncated)
	require.NoError(t, err)

	clock := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	attempts := &realStackAttempts{}
	seq := 0
	newID := func(prefix string) string {
		seq++
		id := fmt.Sprintf("%s-%d", prefix, seq)
		if prefix == "attempt" {
			attempts.add(id)
		}
		return id
	}
	store := evaluation.NewStore(evidenceFiles)
	persona := harnessclient.Persona{ID: realStackPersonaID, CLISessionID: authContext.CLISessionID, UserID: authContext.UserID}
	sentinel, err := drive.NewSentinel(gateway, persona, map[cases.Executor]cliop.Target{
		cases.ExecutorOutbound: {OperatorID: dataWorkload.OperatorID, OperatorSessionID: dataWorkload.OperatorSessionID},
	})
	require.NoError(t, err)
	inference := realStackInference{client: appClient, cliSessionID: persona.CLISessionID}
	executor, err := evaluation.NewCampaignAgentExecutor(
		inference, evaluation.NewCommandLane(gateway, persona), sentinel, store, clock, newID)
	require.NoError(t, err)
	controller := evaluation.NewCampaignController(store, executor, clock, newID)

	env := &realStackAgentRun{
		stack: stack, store: store, files: evidenceFiles, controller: controller,
		clock: clock, gateway: gateway, appClient: appClient,
		inference: inference, sentinel: sentinel, persona: persona,
		dataOperatorID: dataWorkload.OperatorID, dataSessionID: dataWorkload.OperatorSessionID,
		catalog: truncated, artifacts: artifacts, topology: topology,
		inferenceSessionID: inferenceWorkload.OperatorSessionID,
		workingDirectory:   topology.DataWorkingDirectory,
		attempts:           attempts,
	}
	repetitions := opts.repetitions
	if repetitions == 0 {
		repetitions = 1
	}
	runID := "run-smoke-1"
	if opts.suffix != "" {
		runID += "-" + opts.suffix
	}
	env.startRun(t, realStackRunPlan{
		campaignID: realStackCampaignID, runID: runID, repetitions: repetitions,
		singleRole: opts.singleRole, request: opts.request,
	})
	// The owner denies only the agent's governed shell command; enrollment and
	// the inference calls of each agent turn are L3 transactions too.
	if opts.denyHumanApprovals {
		stack.Owner.DenyToolTransactions(constants.MCPToolRunShellCommand)
	}
	return env
}

// startRun initializes one campaign run on the rig's stack, schedules it, and
// makes it the run executeNext drives. Runs that share a campaign ID must freeze
// the same spec, so a sequential run repeats the campaign's investigation.
func (env *realStackAgentRun) startRun(t *testing.T, plan realStackRunPlan) {
	t.Helper()
	ctx := t.Context()
	repetitions := plan.repetitions
	if repetitions == 0 {
		repetitions = 1
	}
	variant := &evalv1.ModelVariant{VariantId: "qwen3-4b", ProviderClass: "ollama", ServedModelTag: realStackModelTag, ModelDigest: realStackModelHash}
	inventory, err := evaluation.MaterializeModelRegistry(plan.campaignID, []*evalv1.ModelVariant{variant})
	require.NoError(t, err)
	req := evaluation.CampaignInitRequest{
		Platform:                   evaluation.PlatformIdentity{Release: "v2.3.0", SourceRevision: "0123abc"},
		CampaignID:                 plan.campaignID,
		RunID:                      plan.runID,
		Catalog:                    env.catalog,
		Inventory:                  inventory,
		ScenarioArtifacts:          env.artifacts,
		RepetitionCount:            repetitions,
		InferenceOperatorSessionID: env.inferenceSessionID,
		DataOperatorSessionID:      env.dataSessionID,
	}
	if plan.request != nil {
		plan.request(&req, env.dataOperatorID)
	}
	_, err = env.controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	scheduled, err := env.controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)
	require.Positive(t, scheduled)
	if plan.singleRole {
		require.Equal(t, int(repetitions), scheduled)
	}
	env.req = req
	env.scheduledCount = scheduled
	env.binding = evaluation.CampaignExecutionBinding{
		Topology:            env.topology,
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       req.Inventory.ToModelRegistryFreeze().Variants,
	}
}

// executeNext runs the next scheduled assignment and returns its result.
func (env *realStackAgentRun) executeNext(t *testing.T) *evalv1.EvaluationAssignmentResult {
	t.Helper()
	res, ok, err := env.controller.ExecuteNextAssignment(t.Context(), env.req.RunID, env.binding, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, ok)
	return res
}

// flipHexDigit returns s with its first hex digit changed.
func flipHexDigit(s string) string {
	if strings.HasPrefix(s, "0") {
		return "1" + s[1:]
	}
	return "0" + s[1:]
}

func TestAgentEvidenceIntegration_SharedRuntimeEmbeddedOperator(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{shared: true, suffix: "shared", replies: shellRepliesFor(2)})
	require.Equal(t, 2, env.scheduledCount, "both roles must be scheduled, one assignment each")

	for i := 0; i < env.scheduledCount; i++ {
		res := env.executeNext(t)
		assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())

		trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
		require.NoError(t, err)
		assert.Equal(t, "agent-trace.v7", trace.SchemaVersion)
		require.NotNil(t, trace.Input, "a v4 trace records the exact model input")
		assert.NotEmpty(t, trace.Input.Messages)
		assert.True(t, trace.SharedRuntime(), "shared runtime trace must report SharedRuntime() == true")
		require.NotEmpty(t, trace.ModelCalls)
		for _, mc := range trace.ModelCalls {
			assert.NotNil(t, mc.Receipt, "every model call must carry a receipt")
		}
	}

	report, err := evaluation.NewCampaignRunVerifier(env.clock).VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus())

	exportReport, err := evaluation.NewCampaignExporter(env.clock).ExportRun(ctx, env.store, env.files, env.req.RunID, constants.EvaluationCampaignExportDirname)
	require.NoError(t, err)
	require.NotEmpty(t, exportReport.Files)
}

func TestAgentEvidenceIntegration_DistinctRuntimes(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{suffix: "distinct", replies: shellRepliesFor(2)})
	require.Equal(t, 2, env.scheduledCount, "both roles must be scheduled, one assignment each")

	for i := 0; i < env.scheduledCount; i++ {
		res := env.executeNext(t)
		assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())

		trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
		require.NoError(t, err)
		assert.Equal(t, "agent-trace.v7", trace.SchemaVersion)
		require.NotNil(t, trace.Input, "a v4 trace records the exact model input")
		assert.NotEmpty(t, trace.Input.Messages)
		assert.False(t, trace.SharedRuntime(), "distinct runtimes must report SharedRuntime() == false")
		require.NotEmpty(t, trace.ModelCalls)
		for _, mc := range trace.ModelCalls {
			assert.NotNil(t, mc.Receipt)
		}
	}

	report, err := evaluation.NewCampaignRunVerifier(env.clock).VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus())

	exportReport, err := evaluation.NewCampaignExporter(env.clock).ExportRun(ctx, env.store, env.files, env.req.RunID, constants.EvaluationCampaignExportDirname)
	require.NoError(t, err)
	require.NotEmpty(t, exportReport.Files)
}

func TestAgentEvidenceIntegration_ProviderFailureAfterExecution(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "provider-fail", replies: []fixtures.OllamaReply{{Failure: "provider unreachable"}}})

	res := env.executeNext(t)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, res.GetLifecycleStatus())
	assert.Empty(t, res.GetInvalidEvidenceDetail())

	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	assert.NotEmpty(t, trace.ProviderFailure)
	require.NotEmpty(t, trace.ModelCalls)
	require.NotNil(t, trace.ModelCalls[0].Receipt)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, trace.ModelCalls[0].Receipt.Status)

	exportReport, err := evaluation.NewCampaignExporter(env.clock).ExportRun(ctx, env.store, env.files, env.req.RunID, constants.EvaluationCampaignExportDirname)
	require.NoError(t, err)
	require.NotEmpty(t, exportReport.Files)

	report, err := evaluation.NewCampaignRunVerifier(env.clock).VerifyRun(ctx, env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus())
}

func TestAgentEvidenceIntegration_ForeignUnclaimedExecutionSharedRuntime(t *testing.T) {
	ctx := t.Context()
	var env *realStackAgentRun
	replies := shellReplies()
	// While the first model turn is in flight, the same Operator executes a
	// governed command that the assignment does not claim.
	replies[0].BeforeReply = func() {
		executionID, err := uuid.NewString()
		if err != nil {
			t.Errorf("new execution ID: %v", err)
			return
		}
		request, err := cliop.BuildExecuteBashDispatchRequest(env.dataSessionID, "echo foreign", executionID, env.persona.CLISessionID)
		if err != nil {
			t.Errorf("build foreign command: %v", err)
			return
		}
		if _, err := env.gateway.DispatchCommand(ctx, env.persona, request); err != nil {
			t.Errorf("dispatch foreign command: %v", err)
		}
	}
	env = newRealStackAgentRun(t, realStackOptions{shared: true, singleRole: true, suffix: "foreign", replies: replies})

	res := env.executeNext(t)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, res.GetLifecycleStatus())
	assert.Contains(t, res.GetInvalidEvidenceDetail(), "unclaimed execution")
	assert.Empty(t, res.GetDeterministicGrades())
	assert.Empty(t, res.GetSemanticGrades())
}

// TestAgentEvidenceIntegration_DigestMismatch covers the stored-evidence half of
// the digest check. The Operator computes ResultDigest from the provider reply
// it signs, so a live run cannot be made to mismatch; instead, a sealed trace
// whose result digest is altered must fail verification.
func TestAgentEvidenceIntegration_DigestMismatch(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "digest-mismatch", replies: shellReplies()})

	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.NotEmpty(t, trace.ModelCalls)

	trace.ModelCalls[0].ResultDigest = flipHexDigit(trace.ModelCalls[0].ResultDigest)
	err = evaluation.VerifyAgentTraceEvidence(trace)
	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid, "a result digest that does not cover the signed result fails closed")
}

func TestAgentEvidenceIntegration_AnchorFailClosed(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "anchor-fail", replies: shellReplies()})

	badBinding := env.binding
	badBinding.Topology.Inference.Anchor = &evalv1.ConformanceAnchor{RuntimeId: "wrong-operator"}

	_, _, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, badBinding, env.req.ScenarioArtifacts)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrConformanceAnchorMissing)
	assert.Equal(t, 0, env.stack.Provider.ChatCount(), "nothing is dispatched when anchor validation fails")
}
