// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package compliancecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/pathutil"
	"github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
)

func complianceEvidenceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "evidence",
		Short: "Export source evidence from the selected runtime",
	}
	cmd.AddCommand(complianceEvidenceExportCmdWithConfig(shared.NewFileSvc))
	return cmd
}

func complianceEvidenceExportCmdWithConfig(fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error)) *cobra.Command {
	var scopePath string
	var outputDir string
	var maxRows int

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export bounded operational evidence for a hand-authored assessment scope",
		Long: `Export the bounded operational source package for an assessment scope you
wrote by hand. This is a low-level step.

To prepare release compliance evidence, run "ztd compliance release-prepare"
instead: it builds the scope, exports, signs, verifies, and projects in one
command.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if scopePath == "" || outputDir == "" {
				return fmt.Errorf("%w: --scope and --out are required (to prepare release evidence, run \"ztd compliance release-prepare\")", constants.ErrValidationFailed)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			fileSvc, err := fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			scope, err := loadComplianceAssessmentScope(scopePath)
			if err != nil {
				return err
			}
			v, vaultCleanup, err := openRuntimeVault(fileSvc, keystore.Options{})
			if err != nil {
				return err
			}
			defer vaultCleanup()
			dbPath := pathutil.ResolveDBPath(fileSvc.Resolve(constants.DataDirname), constants.DbFilename)
			inventory, _, err := exportOperationalEvidence(ctx, dbPath, v, scope, outputDir, maxRows)
			if err != nil {
				return err
			}
			body, err := json.Marshal(inventory)
			if err != nil {
				return fmt.Errorf("compliance evidence: marshal export inventory: %w", err)
			}
			if _, err := cmd.OutOrStdout().Write(append(body, '\n')); err != nil {
				return fmt.Errorf("compliance evidence: write export inventory: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scopePath, "scope", "", "Canonical assessment scope selecting the runtime source and evidence window")
	cmd.Flags().StringVar(&outputDir, "out", "", "Output directory for the operational source package")
	cmd.Flags().IntVar(&maxRows, "max-rows", constants.ComplianceOperationalExportDefaultMaxRows, "Maximum receipts and commitments to export")
	return cmd
}

// exportOperationalEvidence reads the scope's single operational source
// admission from a read-only database snapshot and writes its bounded source
// package. encryptionVault must be unlocked: the receipt and commitment content
// columns are encrypted, and the snapshot fails with ErrLocalFirstEventsStoreVaultLocked
// otherwise. It returns the snapshot so callers can derive signer trust from
// the exact rows that were exported.
func exportOperationalEvidence(ctx context.Context, dbPath string, encryptionVault *vault.Vault, scope *compliancev1.AssessmentScope, outputDir string, maxRows int) (*evidence.OperationalSourceInventory, *storage.OperationalEvidenceSnapshot, error) {
	if len(scope.GetSourceAdmissions()) != 1 {
		return nil, nil, fmt.Errorf("%w: operational export requires exactly one source admission", constants.ErrValidationFailed)
	}
	admission := scope.GetSourceAdmissions()[0]
	reader, err := storage.OpenReadOnlyOperationalEvidence(dbPath, encryptionVault, slog.Default())
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = reader.Close() }()
	snapshot, err := reader.Snapshot(ctx, storage.OperationalEvidenceQuery{
		WindowStart: scope.GetAssessmentWindowStart().AsTime(),
		WindowEnd:   scope.GetAssessmentWindowEnd().AsTime(),
		MaxRows:     maxRows,
	})
	if err != nil {
		return nil, nil, err
	}
	inventory, err := evidence.ExportOperationalEvidence(ctx, snapshot, evidence.OperationalExportRequest{
		ScopeID:              scope.GetScopeId(),
		AdmissionID:          admission.GetAdmissionId(),
		SourceKind:           admission.GetSourceKind(),
		SourceVersion:        admission.GetSourceVersion(),
		SourceScopeID:        admission.GetSourceScopeId(),
		OwnerRuntimeBoundary: admission.GetOwnerRuntimeBoundary(),
		AcquisitionBoundary:  admission.GetAcquisitionBoundary(),
		RunID:                admission.GetRunId(),
		SnapshotID:           admission.GetSnapshotId(),
		VerifierID:           admission.GetVerifierRef().GetId(),
		VerifierVersion:      admission.GetVerifierRef().GetVersion(),
		WindowStart:          scope.GetAssessmentWindowStart().AsTime(),
		WindowEnd:            scope.GetAssessmentWindowEnd().AsTime(),
		MaxRows:              maxRows,
		OutputDir:            outputDir,
	})
	if err != nil {
		return nil, nil, err
	}
	return inventory, snapshot, nil
}
