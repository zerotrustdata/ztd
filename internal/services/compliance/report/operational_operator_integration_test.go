// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package report

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/compliance/catalog"
	"github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// TestOperationalOperatorEvidence_GeneratesNativeAssuranceAndVerifiesOffline
// dispatches one real governed command through the real Gateway. The outbound
// Data Operator executes it and writes its own signed receipt, commitment, and
// audit rows. The test then reads that Operator's authoritative store, exports
// it as an operational source, and generates and verifies the bundle offline.
// Only the model provider is scripted, and this path makes no model call.
func TestOperationalOperatorEvidence_GeneratesNativeAssuranceAndVerifiesOffline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	logger := testutil.NewTestLogger()
	stack := fixtures.NewEvalStack(t, fixtures.EvalStackOptions{})
	dataWorkload, err := stack.Data.Workload()
	require.NoError(t, err)

	payload, err := proto.Marshal(&operatorv1.CommandRequested{
		Command:       "echo ztd-operational-evidence",
		ExecutionId:   "operational-evidence-marker",
		Justification: "Produce operational evidence for the assurance report",
		Intent:        "Print a fixed verification marker",
	})
	require.NoError(t, err)
	body, err := json.Marshal(models.OperatorCommandRequest{
		TargetOperatorSessionID: dataWorkload.OperatorSessionID,
		EventType:               string(constants.Event.Operator.Command.Requested),
		Payload:                 payload,
	})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		network.LocalhostHTTPSURL(stack.Gateway.Service.GetHTTPSPort())+constants.APIPaths.OperatorsCommands, bytes.NewReader(body))
	require.NoError(t, err)
	req.Close = true
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(constants.HeaderCLISessionID, stack.Identity.CLISessionID)
	client := fixtures.CreateCLIMTLSClient(t, stack.Gateway, stack.Identity)
	resp, err := client.Do(req)
	require.NoError(t, err)
	respBody, readErr := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, readErr)
	require.Equalf(t, http.StatusOK, resp.StatusCode, "dispatch response: %s", respBody)

	var result operatorv1.GovernedDispatchResult
	require.NoError(t, protojson.Unmarshal(respBody, &result), "dispatch response: %s", respBody)
	require.Equal(t, string(constants.Event.Operator.Command.Completed), result.GetEventType())
	require.NotEmpty(t, result.GetTransactionId())
	require.NotNil(t, result.GetReceipt())
	var commandResult operatorv1.CommandResult
	require.NoError(t, proto.Unmarshal(result.GetResultPayload(), &commandResult))
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, commandResult.GetStatus())

	// The Data Operator's authoritative store and keystore, opened read-only.
	dataFiles, err := fs.NewRuntimeFileService(stack.DataHost.WorkingDir, logger)
	require.NoError(t, err)
	ks, err := keystore.Open(dataFiles, logger, keystore.Options{Keyring: stack.DataHost.Keyring})
	require.NoError(t, err)
	vaultKey, err := ks.LoadVaultKey()
	require.NoError(t, err)
	t.Cleanup(func() { vault.SecureZero(vaultKey) })
	dataVault, err := vault.NewVault(&vault.VaultConfig{FileSvc: dataFiles, Logger: logger})
	require.NoError(t, err)
	require.NoError(t, dataVault.Unlock(vaultKey))

	// The actuator key signs receipts; it is the key the Gateway registered for
	// this Operator. The auditor key signs commitments and is the Operator's own
	// keystore secret, derived the same way its secret manager derives it.
	operatorDoc, err := stack.Gateway.Service.GetRegistrationService().GetOperator(ctx, dataWorkload.OperatorID)
	require.NoError(t, err)
	actuatorPublicKey, err := models.DecodeActuatorPublicKey(operatorDoc.GetActuatorPublicKey())
	require.NoError(t, err)
	actuatorKeyID := hex.EncodeToString(actuatorPublicKey)
	require.Equal(t, actuatorKeyID, result.GetReceipt().GetSignerKeyId(), "the dispatched receipt must be signed by the registered actuator key")
	auditorSeedHex, err := ks.DecryptSecret(constants.SecretsFileAuditorSigningKey)
	require.NoError(t, err)
	auditorSeed, err := hex.DecodeString(auditorSeedHex)
	require.NoError(t, err)
	require.Len(t, auditorSeed, ed25519.SeedSize)
	auditorPublicKey := ed25519.NewKeyFromSeed(auditorSeed).Public().(ed25519.PublicKey)
	auditorKeyID := hex.EncodeToString(auditorPublicKey)

	assessmentAsOf := time.Now().UTC()
	windowStart := assessmentAsOf.Add(-time.Minute)
	windowEnd := assessmentAsOf.Add(time.Minute)
	reader, err := storage.OpenReadOnlyOperationalEvidence(dataFiles.Resolve(constants.CanonicalDBRelPath), dataVault, logger)
	require.NoError(t, err)
	snapshot, err := reader.Snapshot(ctx, storage.OperationalEvidenceQuery{WindowStart: windowStart, WindowEnd: windowEnd, MaxRows: 10})
	require.NoError(t, err)
	require.NoError(t, reader.Close())

	receiptFound := false
	for _, receipt := range snapshot.Receipts {
		if receipt.TransactionID == result.GetTransactionId() {
			receiptFound = true
		}
	}
	require.True(t, receiptFound, "the Data Operator's receipt for the dispatched transaction must be in the window")
	commitmentFound := false
	for _, commitment := range snapshot.Commitments {
		if commitment.TransactionID == result.GetTransactionId() {
			commitmentFound = true
		}
	}
	require.True(t, commitmentFound, "the Data Operator's commitment for the dispatched transaction must be in the window")

	scope := validGenerationScope(windowStart, windowEnd)
	scope.AssessmentAsOf = timestamppb.New(assessmentAsOf)
	admission := scope.SourceAdmissions[0]
	admission.SourceKind = "operator-audit"
	admission.SourceVersion = "1.0.0"
	admission.SourceScopeId = "operator-scope-1"
	admission.OwnerRuntimeBoundary = "operator-1"
	admission.AcquisitionBoundary = "operator-local-export"
	admission.RunId = "operator-run-1"
	admission.VerifierRef = &compliancev1.VersionedReference{Id: "operational-export", Version: "1.0.0"}
	admission.DisclosureClassification = constants.ComplianceBundleProfileRestricted

	sourceDir := testutil.TempDir(t)
	_, err = evidence.ExportOperationalEvidence(ctx, snapshot, evidence.OperationalExportRequest{
		ScopeID:              scope.GetScopeId(),
		AdmissionID:          admission.GetAdmissionId(),
		SourceKind:           admission.GetSourceKind(),
		SourceVersion:        admission.GetSourceVersion(),
		SourceScopeID:        admission.GetSourceScopeId(),
		OwnerRuntimeBoundary: admission.GetOwnerRuntimeBoundary(),
		AcquisitionBoundary:  admission.GetAcquisitionBoundary(),
		RunID:                admission.GetRunId(),
		VerifierID:           admission.GetVerifierRef().GetId(),
		VerifierVersion:      admission.GetVerifierRef().GetVersion(),
		WindowStart:          windowStart,
		WindowEnd:            windowEnd,
		MaxRows:              10,
		OutputDir:            sourceDir,
	})
	require.NoError(t, err)

	sourceRoot := path.Join(constants.ComplianceBundleSourcesDirname, constants.ComplianceOperationalExportDirname, admission.GetAdmissionId())
	inventoryBody, err := os.ReadFile(filepath.Join(sourceDir, constants.ComplianceOperationalInventoryFilename))
	require.NoError(t, err)
	inventory := &evidence.OperationalSourceInventory{}
	require.NoError(t, decodeJSON(inventoryBody, inventory))
	sourceBodies := map[string][]byte{path.Join(sourceRoot, constants.ComplianceOperationalInventoryFilename): inventoryBody}
	for _, artifact := range inventory.Artifacts {
		body, readErr := os.ReadFile(filepath.Join(sourceDir, artifact.RelativePath))
		require.NoError(t, readErr)
		sourceBodies[path.Join(sourceRoot, artifact.RelativePath)] = body
	}

	trust := &assessedEvidenceSignerStub{keys: map[string]ed25519.PublicKey{actuatorKeyID: actuatorPublicKey, auditorKeyID: auditorPublicKey}}
	importer := evidence.NewOperationalExportImporter(&bundledSourceArtifactReader{bodies: sourceBodies}, trust, path.Join(sourceRoot, constants.ComplianceOperationalInventoryFilename), sourceRoot, scope.GetScopeId(), admission, assessmentAsOf, func() time.Time { return assessmentAsOf })
	assertions, frameworks, crosswalks, err := catalog.LoadCanonicalCatalogs()
	require.NoError(t, err)
	generatedAt := assessmentAsOf.Add(time.Second)
	identity := bundleSigningIdentityAtFixture(t, generatedAt)
	generation := GenerationRequest{
		Scope:      scope,
		Sources:    []GenerationSource{{AdmissionID: admission.GetAdmissionId(), Importer: importer}},
		Assertions: assertions,
		Frameworks: frameworks,
		Crosswalks: crosswalks,
	}
	generated, err := GenerateComplianceAnalysis(ctx, generation)
	graphReport := ""
	if generated != nil && generated.GraphReport != nil {
		graphReport = generated.GraphReport.String()
	}
	require.NoError(t, err, graphReport)
	assembly, err := GenerateSignedComplianceBundle(ctx, SignedBundleGenerationRequest{
		Generation:      generation,
		Profile:         ProfileRestricted,
		ReportID:        "operator-assurance-1",
		GeneratedAt:     generatedAt,
		SigningIdentity: identity,
		SourceArtifacts: operationalSourceArtifacts(sourceBodies),
	})
	require.NoError(t, err)
	require.NotEmpty(t, assembly.Bundle.GetAnalysis().GetEvidenceResources())

	bundleBodies := make(map[string][]byte, len(assembly.ArtifactBodies))
	for _, artifact := range assembly.ArtifactBodies {
		bundleBodies[artifact.BundlePath] = artifact.Body
	}
	reportPublicKey := identity.privateKey.Public().(ed25519.PublicKey)
	reportPolicy := &compliancev1.ComplianceReportTrustPolicy{
		PolicyId:      "operator-report-policy",
		PolicyVersion: "1.0.0",
		TrustedKeys: []*compliancev1.ComplianceReportTrustedKey{{
			Metadata:         proto.Clone(identity.metadata).(*compliancev1.ComplianceReportSigningKeyMetadata),
			PublicKey:        hex.EncodeToString(reportPublicKey),
			AssessmentId:     "operator-assessment-1",
			AssessorIdentity: "operator-assessor",
			AssessedAt:       timestamppb.New(assessmentAsOf),
			AllowedScopeRefs: []string{scope.GetScopeId()},
		}},
	}
	verification, err := VerifyComplianceReportBundle(ctx, BundleVerificationRequest{Bundle: assembly.Bundle, Reader: &bundleArtifactReaderStub{bodies: bundleBodies}, TrustPolicy: reportPolicy, EvidenceTrust: trust, VerifiedAt: generatedAt.Add(time.Minute)})
	require.NoError(t, err)
	require.True(t, verification.GetValid(), verification.GetFailures())
}

func decodeJSON(body []byte, target any) error {
	return json.Unmarshal(body, target)
}

func operationalSourceArtifacts(sourceBodies map[string][]byte) []SourceArtifact {
	artifacts := make([]SourceArtifact, 0, len(sourceBodies))
	for bundlePath, body := range sourceBodies {
		artifacts = append(artifacts, SourceArtifact{BundlePath: bundlePath, Body: body, MediaType: constants.MediaTypeJSON})
	}
	sort.Slice(artifacts, func(left, right int) bool { return artifacts[left].BundlePath < artifacts[right].BundlePath })
	return artifacts
}
