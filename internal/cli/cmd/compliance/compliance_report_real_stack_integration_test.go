// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package compliancecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	cliop "github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	compliancereport "github.com/zerotrustdata/ztd/internal/services/compliance/report"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/drive"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	harnessconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

const (
	complianceRealCampaignID = "compliance-campaign"
	complianceRealRunID      = "run-compliance-real"
	complianceRealPersonaID  = "compliance-campaign-agent"
	complianceRealScenarioID = "tool-arg-shell-command"
	complianceRealModelTag   = "qwen3:4b"
	complianceRealShellArgs  = `{"command":"cat","args":["status/health.txt"]}`
)

// complianceRealShellReplies is the provider script for one answer-free tool
// turn: the model calls run_shell_command, then answers from the output.
func complianceRealShellReplies() []fixtures.OllamaReply {
	return []fixtures.OllamaReply{
		{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolRunShellCommand, Arguments: json.RawMessage(complianceRealShellArgs)}}},
		{Content: "HEALTHY"},
	}
}

// complianceRealInference forwards governed inference and Tribunal intents from
// the application identity to the production harness client, as the eval runner does.
type complianceRealInference struct {
	client       *harnessclient.Client
	cliSessionID string
}

func (d complianceRealInference) DispatchInference(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	response, _, err := d.client.DispatchInference(ctx, req)
	return response, err
}

func (d complianceRealInference) DispatchIntent(ctx context.Context, req *tribunalv1.TribunalIntentRequest) (*tribunalv1.TribunalIntentResult, error) {
	req.CliSessionId = d.cliSessionID
	result, _, err := d.client.DispatchIntent(ctx, req)
	return result, err
}

// complianceRealRun is one campaign run executed to completion on the real eval
// stack and sealed into its evidence root. Every mutation case works on a copy
// of that root, so the run executes once for the whole test.
type complianceRealRun struct {
	logger    *slog.Logger
	keyring   keystore.Keyring
	plain     fs.RuntimeFileService
	attemptID string
}

// newComplianceRealRun boots a real Gateway with distinct Data and Inference
// Operators and a Provenance Operator, executes the one scored assignment through
// the production campaign executor, seals the Gateway's witness windows through
// the production witness capture, and verifies the run.
func newComplianceRealRun(t *testing.T) *complianceRealRun {
	t.Helper()
	ctx := t.Context()
	stack := fixtures.NewEvalStack(t, fixtures.EvalStackOptions{Replies: complianceRealShellReplies(), Provenance: true})

	logger := testutil.NewTestLogger()
	root := testutil.TempDir(t)
	keyring := keystoretest.NewMemoryKeyring()
	cfg, err := cliconfig.Load(root)
	require.NoError(t, err)
	cfg.Keyring = keyring
	plain, err := fs.NewRuntimeFileService(root, logger)
	require.NoError(t, err)
	require.NoError(t, plain.CreateRuntimeTree(ctx))
	keyStore, err := keystore.NewWithKeyringAndFS(logger, keyring, plain)
	require.NoError(t, err)
	require.NoError(t, keyStore.Initialize())
	require.NoError(t, keyStore.InitVault())
	stack.Identity.InstallCLIRuntime(t, stack.Gateway, plain, cfg)
	stack.EnrollEvalAppIdentity(t, plain, cfg)
	evidence, err := shared.EvidenceFileSvcFactory(keystore.Options{Keyring: keyring})(root, logger)
	require.NoError(t, err)

	authContext, err := auth.LoadClientAuthContext(plain, cfg)
	require.NoError(t, err)
	cliCredentials := harnessconfig.Auth{ClientCert: authContext.ClientCert, ClientKey: authContext.ClientKey, Certificate: authContext.Certificate, CABundle: cfg.ResolvedTrustBundlePath()}
	gateway, err := harnessclient.New(harnessconfig.Config{
		MTLSBaseURL: cfg.OperatorHTTPURL(), PublicBaseURL: cfg.OperatorDiscoveryURL(),
		Auth: cliCredentials, CLIAuth: cliCredentials, UseCLIConfig: true,
		UserID: authContext.UserID, CLISessionID: authContext.CLISessionID, OperatorSessionID: authContext.OperatorSessionID,
	})
	require.NoError(t, err)
	appCertificate, err := auth.LoadAppIdentity(plain, cfg, constants.EvaluationAppName)
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
	// Keep the run to the one scenario, scheduled for the primary role only, so
	// the run schedules exactly one assignment.
	var target *evalv1.EvaluationScenarioDefinition
	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetScenarioId() == complianceRealScenarioID {
			target = scenario
			break
		}
	}
	require.NotNil(t, target, "scenario %s must exist in catalog", complianceRealScenarioID)
	target.EligibleRoles = []evalv1.ModelCampaignRole{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY}
	truncated := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: catalog.GetSchemaVersion(),
		CatalogRef:    catalog.GetCatalogRef(),
		Scenarios:     []*evalv1.EvaluationScenarioDefinition{target},
	}
	truncated.CatalogDigest, err = evaluation.ComputeScenarioCatalogDigest(truncated)
	require.NoError(t, err)

	// The variant declares the digest of the model store the Provenance Operator
	// attests, so the attestation window is a digest match.
	variant := &evalv1.ModelVariant{VariantId: "qwen3-4b", ProviderClass: "ollama", ServedModelTag: complianceRealModelTag, ModelDigest: stack.ModelStore.ModelDigest}
	inventory, err := evaluation.MaterializeModelRegistry(complianceRealCampaignID, []*evalv1.ModelVariant{variant})
	require.NoError(t, err)
	req := evaluation.CampaignInitRequest{
		Platform:                   evaluation.PlatformIdentity{Release: "v2.3.0", SourceRevision: "0123abc"},
		CampaignID:                 complianceRealCampaignID,
		RunID:                      complianceRealRunID,
		Catalog:                    truncated,
		Inventory:                  inventory,
		ScenarioArtifacts:          artifacts,
		RepetitionCount:            1,
		InferenceOperatorSessionID: inferenceWorkload.OperatorSessionID,
		DataOperatorSessionID:      dataWorkload.OperatorSessionID,
	}
	binding := evaluation.CampaignExecutionBinding{
		Topology:            topology,
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       evaluation.InferenceVariantsFromEvalRegistry(req.Inventory.Variants),
	}

	clock := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	seq := 0
	newID := func(prefix string) string {
		seq++
		return fmt.Sprintf("%s-%d", prefix, seq)
	}
	store := evaluation.NewStore(evidence)
	persona := harnessclient.Persona{ID: complianceRealPersonaID, CLISessionID: authContext.CLISessionID, UserID: authContext.UserID}
	sentinel, err := drive.NewSentinel(gateway, persona, map[cases.Executor]cliop.Target{
		cases.ExecutorOutbound: {OperatorID: dataWorkload.OperatorID, OperatorSessionID: dataWorkload.OperatorSessionID},
	})
	require.NoError(t, err)
	executor, err := evaluation.NewCampaignAgentExecutor(
		complianceRealInference{client: appClient, cliSessionID: persona.CLISessionID},
		evaluation.NewCommandLane(gateway, persona), sentinel, store, clock, newID)
	require.NoError(t, err)
	controller := evaluation.NewCampaignController(store, executor, clock, newID)

	_, err = controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	scheduled, err := controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)
	require.Equal(t, 1, scheduled, "the primary-role scenario schedules one assignment")

	result, ok, err := controller.ExecuteNextAssignment(ctx, req.RunID, binding, artifacts)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "invalid evidence: %s", result.GetInvalidEvidenceDetail())
	require.NotEmpty(t, result.GetModelInferences())
	attemptID := result.GetModelInferences()[0].GetProviderAttemptId()
	require.NotEmpty(t, attemptID)

	// Witness capture runs as the production run execution does: per assignment,
	// then for the whole run, then waits for the strict windows to land.
	witness, err := gwremote.NewCampaignWitnessCapture(plain, store, cfg)
	require.NoError(t, err)
	require.NoError(t, witness.CaptureAssignmentEvidence(ctx, result))
	require.NoError(t, witness.CaptureRunProviderObservationEvidence(ctx, req.RunID))
	require.NoError(t, witness.CaptureRunModelProvenanceEvidence(ctx, req.RunID))
	require.NoError(t, witness.WaitForRunModelProvenanceEvidence(ctx, req.RunID))
	require.NoError(t, witness.WaitForRunProviderObservationEvidence(ctx, req.RunID))

	report, err := evaluation.NewCampaignRunVerifier(clock).VerifyRun(ctx, store, req.RunID, req.Catalog, req.ScenarioArtifacts)
	require.NoError(t, err)
	require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus())

	return &complianceRealRun{logger: logger, keyring: keyring, plain: plain, attemptID: attemptID}
}

// copyFiles returns an independent copy of the run's runtime root, read through
// the evidence service over the copy. Sealed records are copied as their sealed
// bytes under the same keyring, so the copy opens exactly as the run does.
func (r *complianceRealRun) copyFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	ctx := t.Context()
	root := testutil.TempDir(t)
	dst, err := fs.NewRuntimeFileService(root, r.logger)
	require.NoError(t, err)
	require.NoError(t, dst.CreateRuntimeTree(ctx))
	require.NoError(t, copyRuntimeTree(ctx, r.plain, dst, ""))
	files, err := shared.EvidenceFileSvcFactory(keystore.Options{Keyring: r.keyring})(root, r.logger)
	require.NoError(t, err)
	return files
}

// copyRuntimeTree copies every file and directory under rel from src to dst.
func copyRuntimeTree(ctx context.Context, src, dst fs.RuntimeFileService, rel string) error {
	entries, err := src.ReadDir(ctx, rel)
	if err != nil {
		return fmt.Errorf("read runtime directory %q: %w", rel, err)
	}
	for _, entry := range entries {
		child := path.Join(rel, entry.Name())
		if entry.IsDir() {
			if err := dst.MkdirAll(ctx, child, constants.PermDirStandard); err != nil {
				return fmt.Errorf("create runtime directory %q: %w", child, err)
			}
			if err := copyRuntimeTree(ctx, src, dst, child); err != nil {
				return err
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat runtime file %q: %w", child, err)
		}
		data, err := src.ReadFile(ctx, child)
		if err != nil {
			return fmt.Errorf("read runtime file %q: %w", child, err)
		}
		if err := dst.WriteFile(ctx, child, data, info.Mode().Perm()); err != nil {
			return fmt.Errorf("write runtime file %q: %w", child, err)
		}
	}
	return nil
}

// removeSealedWitnessBody deletes the sealed body that one attempt's witness
// reference names, leaving the reference behind, so the evidence points at a
// witness it no longer holds.
func removeSealedWitnessBody(t *testing.T, files fs.RuntimeFileService, attemptID, witnessDirname, refFilename string) {
	t.Helper()
	ctx := t.Context()
	evidenceDir := path.Join(constants.DataDirname, constants.EvaluationDirname, constants.EvaluationRunsDirname, complianceRealRunID, constants.EvaluationEvidenceDirname)
	refBody, err := files.ReadFile(ctx, path.Join(evidenceDir, witnessDirname, attemptID, refFilename))
	require.NoError(t, err)
	reference := &compliancev1.ComplianceEvidenceReference{}
	require.NoError(t, compliancev1.UnmarshalCanonical(refBody, reference))
	require.NoError(t, files.Remove(ctx, path.Join(evidenceDir, reference.GetSha256()+constants.FileExtJSON)))
}

type campaignComplianceBundleFixture struct {
	fileSvc      fs.RuntimeFileService
	bundle       *compliancev1.ComplianceReportBundle
	bundleDir    string
	policy       *compliancev1.ComplianceReportTrustPolicy
	verifiedAt   time.Time
	admissionID  string
	runID        string
	attemptID    string
	verification *evalv1.EvaluationVerificationReport
}

// generateComplianceRealBundle runs the compliance report generator over the run
// in files and exports the bundle. It returns the generator's error so a test
// can assert that missing sealed evidence fails generation.
func generateComplianceRealBundle(t *testing.T, files fs.RuntimeFileService, run *complianceRealRun) (campaignComplianceBundleFixture, error) {
	t.Helper()
	ctx := t.Context()
	assessmentAsOf := time.Now().UTC()
	scopeID := constants.EvalScopePrefix + complianceRealCampaignID
	identity, policy := complianceReportSigningFixtureForTest(t, scopeID)
	cmd := complianceReportGenerateCmdWithConfig(cmdtest.FileSvcFactoryFor(files), func(context.Context, signingKeyLocation, string, string) (*compliancereport.ComplianceReportSigningIdentity, error) {
		return identity, nil
	}, func() time.Time { return assessmentAsOf })
	configureComplianceReportGenerateCommand(t, cmd)
	scopePath := writeComplianceAssessmentScopeForTest(t, scopeID, []string{complianceRealRunID}, assessmentAsOf)
	scopeBody, err := os.ReadFile(scopePath)
	require.NoError(t, err)
	scope := &compliancev1.AssessmentScope{}
	require.NoError(t, compliancev1.UnmarshalCanonical(scopeBody, scope))
	admission := scope.SourceAdmissions[0]
	admission.SourceKind = constants.EvaluationSourceKindCampaign
	admission.VerifierRef = &compliancev1.VersionedReference{Id: constants.CampaignVerifierID, Version: constants.CampaignVerifierVersion}
	admission.ProviderObservationPolicy = compliancev1.AssessmentWitnessPolicy_ASSESSMENT_WITNESS_POLICY_STRICT
	admission.ModelProvenancePolicy = compliancev1.AssessmentWitnessPolicy_ASSESSMENT_WITNESS_POLICY_STRICT
	scopeBody, err = compliancev1.MarshalCanonical(scope)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(scopePath, scopeBody, constants.PermFilePrivate))
	require.NoError(t, cmd.Flags().Set("scope", scopePath))
	require.NoError(t, cmd.Flags().Set("eval-run", complianceRealRunID))
	var output bytes.Buffer
	cmd.SetOut(&output)
	if err := cmd.RunE(cmd, nil); err != nil {
		return campaignComplianceBundleFixture{}, err
	}
	descriptor := strings.TrimSpace(output.String())
	descriptorPath, err := files.Rel(descriptor)
	require.NoError(t, err)
	descriptorBody, err := files.ReadFile(ctx, descriptorPath)
	require.NoError(t, err)
	bundle := &compliancev1.ComplianceReportBundle{}
	require.NoError(t, compliancev1.UnmarshalCanonical(descriptorBody, bundle))
	bundleDir := path.Dir(filepath.ToSlash(descriptorPath))
	verificationPath := path.Join(bundleDir, constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, admission.GetAdmissionId(), constants.ComplianceBundleSourceVerificationFilename)
	verificationBody, err := files.ReadFile(ctx, verificationPath)
	require.NoError(t, err)
	verification := &evalv1.EvaluationVerificationReport{}
	require.NoError(t, evalv1.UnmarshalCanonical(verificationBody, verification))
	exportedDir := filepath.Dir(exportComplianceReportForTest(t, files))
	return campaignComplianceBundleFixture{fileSvc: files, bundle: bundle, bundleDir: exportedDir, policy: policy, verifiedAt: assessmentAsOf.Add(time.Minute), admissionID: admission.GetAdmissionId(), runID: complianceRealRunID, attemptID: run.attemptID, verification: verification}, nil
}

func generateComplianceRealBundleFixture(t *testing.T, files fs.RuntimeFileService, run *complianceRealRun) campaignComplianceBundleFixture {
	t.Helper()
	fixture, err := generateComplianceRealBundle(t, files, run)
	require.NoError(t, err)
	return fixture
}

func verifyCampaignComplianceBundleFixture(t *testing.T, fixture campaignComplianceBundleFixture) *compliancev1.ComplianceVerificationReport {
	t.Helper()
	root, err := os.OpenRoot(fixture.bundleDir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	report, err := compliancereport.VerifyComplianceReportBundle(context.Background(), compliancereport.BundleVerificationRequest{
		Bundle: fixture.bundle, Reader: &complianceBundleRootReader{root: root}, TrustPolicy: fixture.policy, VerifiedAt: fixture.verifiedAt,
	})
	require.NoError(t, err)
	return report
}

// bundleRuntimeFile maps one runtime path of the campaign to its file in the
// exported bundle.
func bundleRuntimeFile(fixture campaignComplianceBundleFixture, runtimePath string) string {
	return filepath.Join(fixture.bundleDir, constants.ComplianceBundleSourcesDirname, constants.ComplianceBundleSourceEvalsDirname, fixture.admissionID, constants.ComplianceBundleSourceRuntimeDirname, filepath.FromSlash(runtimePath))
}

// sealedWitnessBundlePath names the bundle path of one attempt's sealed witness
// body. The keyed reference under witnessDirname beside it names that body by
// digest.
func sealedWitnessBundlePath(t *testing.T, fixture campaignComplianceBundleFixture, witnessDirname, refFilename string) string {
	t.Helper()
	evidenceDir := path.Join(constants.DataDirname, constants.EvaluationDirname, constants.EvaluationRunsDirname, fixture.runID, constants.EvaluationEvidenceDirname)
	refBody, err := os.ReadFile(bundleRuntimeFile(fixture, path.Join(evidenceDir, witnessDirname, fixture.attemptID, refFilename)))
	require.NoError(t, err)
	reference := &compliancev1.ComplianceEvidenceReference{}
	require.NoError(t, compliancev1.UnmarshalCanonical(refBody, reference))
	return path.Join(evidenceDir, reference.GetSha256()+constants.FileExtJSON)
}

// TestComplianceReportGenerateCmdWithConfig_SealedRunWitnessMutations executes one
// campaign on the real eval stack and holds every witness mutation to that one
// sealed run. Bundle mutations edit a freshly exported bundle of a copy of the
// run's evidence; missing-witness cases delete a sealed body from a copy before
// generation.
func TestComplianceReportGenerateCmdWithConfig_SealedRunWitnessMutations(t *testing.T) {
	run := newComplianceRealRun(t)

	t.Run("sealed run generates a bundle that verifies offline", func(t *testing.T) {
		fixture := generateComplianceRealBundleFixture(t, run.copyFiles(t), run)
		assert.True(t, verifyCampaignComplianceBundleFixture(t, fixture).GetValid())
	})

	tests := []struct {
		name        string
		runtimePath func(t *testing.T, fixture campaignComplianceBundleFixture) string
		remove      bool
	}{
		{name: "tampered provider attempt", runtimePath: func(t *testing.T, fixture campaignComplianceBundleFixture) string {
			return sealedWitnessBundlePath(t, fixture, constants.EvaluationProviderObservationsDirname, constants.EvaluationProviderAttemptRefFilename)
		}},
		{name: "missing provider attempt", runtimePath: func(t *testing.T, fixture campaignComplianceBundleFixture) string {
			return sealedWitnessBundlePath(t, fixture, constants.EvaluationProviderObservationsDirname, constants.EvaluationProviderAttemptRefFilename)
		}, remove: true},
		{name: "tampered provider observation", runtimePath: func(t *testing.T, fixture campaignComplianceBundleFixture) string {
			return sealedWitnessBundlePath(t, fixture, constants.EvaluationProviderObservationsDirname, constants.EvaluationProviderWindowRefFilename)
		}},
		{name: "missing provider observation", runtimePath: func(t *testing.T, fixture campaignComplianceBundleFixture) string {
			return sealedWitnessBundlePath(t, fixture, constants.EvaluationProviderObservationsDirname, constants.EvaluationProviderWindowRefFilename)
		}, remove: true},
		{name: "tampered model provenance", runtimePath: func(t *testing.T, fixture campaignComplianceBundleFixture) string {
			return sealedWitnessBundlePath(t, fixture, constants.EvaluationModelProvenanceDirname, constants.EvaluationModelProvenanceWindowRefFilename)
		}},
		{name: "missing model provenance", runtimePath: func(t *testing.T, fixture campaignComplianceBundleFixture) string {
			return sealedWitnessBundlePath(t, fixture, constants.EvaluationModelProvenanceDirname, constants.EvaluationModelProvenanceWindowRefFilename)
		}, remove: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := generateComplianceRealBundleFixture(t, run.copyFiles(t), run)
			assert.True(t, verifyCampaignComplianceBundleFixture(t, fixture).GetValid())
			sourcePath := bundleRuntimeFile(fixture, test.runtimePath(t, fixture))
			if test.remove {
				require.NoError(t, os.Remove(sourcePath))
			} else {
				require.NoError(t, os.WriteFile(sourcePath, []byte(`{}`), constants.PermFilePrivate))
			}

			report := verifyCampaignComplianceBundleFixture(t, fixture)
			assert.False(t, report.GetValid())
			assert.NotEmpty(t, report.GetFailures())
		})
	}

	generationTests := []struct {
		name     string
		dir      string
		filename string
	}{
		{name: "missing model provenance fails generation", dir: constants.EvaluationModelProvenanceDirname, filename: constants.EvaluationModelProvenanceWindowRefFilename},
		{name: "missing provider attempt fails generation", dir: constants.EvaluationProviderObservationsDirname, filename: constants.EvaluationProviderAttemptRefFilename},
		{name: "missing provider observation fails generation", dir: constants.EvaluationProviderObservationsDirname, filename: constants.EvaluationProviderWindowRefFilename},
	}
	for _, test := range generationTests {
		t.Run(test.name, func(t *testing.T) {
			files := run.copyFiles(t)
			removeSealedWitnessBody(t, files, run.attemptID, test.dir, test.filename)

			_, err := generateComplianceRealBundle(t, files, run)
			require.Error(t, err)
			assert.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
		})
	}
}
