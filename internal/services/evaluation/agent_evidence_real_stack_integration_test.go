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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	cliop "github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
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
	realStackShellArgs  = `{"command":"cat","args":["status/health.txt"]}`
)

// shellReplies is the frozen provider script for one answer-free tool turn: the
// model calls run_shell_command, then answers from the output.
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
		{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolRunShellCommand, Arguments: json.RawMessage(realStackShellArgs)}}},
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

type realStackOptions struct {
	posture    constants.Posture
	shared     bool
	singleRole bool
	suffix     string
	replies    []fixtures.OllamaReply
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
	persona        harnessclient.Persona
	dataOperatorID string
	dataSessionID  string
}

func newRealStackAgentRun(t *testing.T, opts realStackOptions) *realStackAgentRun {
	t.Helper()
	ctx := t.Context()
	stack := fixtures.NewEvalStack(t, fixtures.EvalStackOptions{Posture: opts.posture, Replies: opts.replies, SharedRuntime: opts.shared})

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

	catalog, artifacts, err := evaluation.LoadScenarioCatalog()
	require.NoError(t, err)
	variant := &evalv1.ModelVariant{VariantId: "qwen3-4b", ProviderClass: "ollama", ServedModelTag: realStackModelTag, ModelDigest: realStackModelHash}
	inventory, err := evaluation.MaterializeModelRegistry(realStackCampaignID, []*evalv1.ModelVariant{variant})
	require.NoError(t, err)
	req := evaluation.CampaignInitRequest{
		Platform:                   evaluation.PlatformIdentity{Release: "v2.3.0", SourceRevision: "0123abc"},
		CampaignID:                 realStackCampaignID,
		RunID:                      "run-smoke-1",
		Catalog:                    catalog,
		Inventory:                  inventory,
		ScenarioArtifacts:          artifacts,
		RepetitionCount:            1,
		InferenceOperatorSessionID: inferenceWorkload.OperatorSessionID,
		DataOperatorSessionID:      dataWorkload.OperatorSessionID,
	}
	if opts.suffix != "" {
		req.RunID += "-" + opts.suffix
	}
	// Keep the run to the one scenario under test; the catalog digest is
	// recomputed over the truncated catalog.
	var target *evalv1.EvaluationScenarioDefinition
	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetScenarioId() == realStackScenarioID {
			target = scenario
			break
		}
	}
	require.NotNil(t, target, "scenario %s must exist in catalog", realStackScenarioID)
	if opts.singleRole {
		target.EligibleRoles = []evalv1.ModelCampaignRole{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY}
	}
	truncated := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: catalog.GetSchemaVersion(),
		CatalogRef:    catalog.GetCatalogRef(),
		Scenarios:     []*evalv1.EvaluationScenarioDefinition{target},
	}
	truncated.CatalogDigest, err = evaluation.ComputeScenarioCatalogDigest(truncated)
	require.NoError(t, err)
	req.Catalog = truncated

	clock := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	seq := 0
	newID := func(prefix string) string {
		seq++
		return fmt.Sprintf("%s-%d", prefix, seq)
	}
	store := evaluation.NewStore(evidenceFiles)
	persona := harnessclient.Persona{ID: realStackPersonaID, CLISessionID: authContext.CLISessionID, UserID: authContext.UserID}
	sentinel, err := drive.NewSentinel(gateway, persona, map[cases.Executor]cliop.Target{
		cases.ExecutorOutbound: {OperatorID: dataWorkload.OperatorID, OperatorSessionID: dataWorkload.OperatorSessionID},
	})
	require.NoError(t, err)
	executor, err := evaluation.NewCampaignAgentExecutor(
		realStackInference{client: appClient, cliSessionID: persona.CLISessionID},
		evaluation.NewCommandLane(gateway, persona), sentinel, store, clock, newID)
	require.NoError(t, err)
	controller := evaluation.NewCampaignController(store, executor, clock, newID)

	_, err = controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	scheduled, err := controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)
	if opts.singleRole {
		require.Equal(t, 1, scheduled)
	} else {
		require.Equal(t, 2, scheduled)
	}

	return &realStackAgentRun{
		stack: stack, store: store, files: evidenceFiles, controller: controller, req: req,
		binding: evaluation.CampaignExecutionBinding{
			Topology:            topology,
			ModelRegistryDigest: req.Inventory.RegistryDigest,
			ModelRegistry:       req.Inventory.ToModelRegistryFreeze().Variants,
		},
		scheduledCount: scheduled, clock: clock, gateway: gateway, persona: persona,
		dataOperatorID: dataWorkload.OperatorID, dataSessionID: dataWorkload.OperatorSessionID,
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
