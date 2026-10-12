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
	"path/filepath"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/compliance"
	compliancereport "github.com/zerotrustdata/ztd/internal/services/compliance/report"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "compliance",
		Aliases: []string{"compliances"},
		Short:   "Release compliance evidence and FedRAMP 20x KSI evaluation",
		Long: `Release compliance evidence, FedRAMP 20x Key Security Indicator (KSI)
evaluation, historical snapshots, and COSAiS overlay inspection.

To prepare the compliance evidence for a release, run
"ztd compliance release-prepare" (first run: add --new-key).

KSI evaluation checks ztd's live state against CR26 KSIs and persists
evaluation snapshots for historical metrics.`,
	}

	cmd.AddCommand(
		complianceKSICmdWithConfig(shared.NewFileSvc),
		complianceKSIHistoryCmdWithConfig(shared.NewFileSvc),
		complianceOverlayCmdWithConfig(shared.NewFileSvc),
		complianceReleaseEvidenceProjectionCmdWithConfig(loadComplianceReportBundleInput, compliancereport.VerifyComplianceReportBundle, time.Now),
		complianceReleasePrepareCmdWithConfig(newReleasePrepareDeps()),
		complianceEvidenceCmd(),
		complianceEvidenceGraphCmd(),
		complianceReportCmd(),
	)

	return cmd
}

// complianceKSICmdWithConfig creates the `compliance ksi` subcommand with
// injectable dependencies for testing.
func complianceKSICmdWithConfig(fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error)) *cobra.Command {
	var class string
	var catalogPath string
	var scopeID string
	var runID string
	var assertionAssessmentIDs []string
	var attemptIDs []string
	var scenarioIDs []string
	var actionIDs []string
	var evidenceWindowStartUnixMs int64
	var evidenceWindowEndUnixMs int64
	var masterKeyFile string

	cmd := &cobra.Command{
		Use:   "ksi",
		Short: "Evaluate KSIs and print the result set as JSON",
		Long: `Evaluate ztd's live state against the FedRAMP 20x Key Security Indicators
for the specified certification class and print the KSIResultSet as JSON.
Every result is bound to the declared --scope-id and --run-id so consumers can
prove no result was produced from evidence outside the assessment context.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			fileSvc, err := fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}

			cat, err := loadKSICatalog(catalogPath)
			if err != nil {
				return err
			}

			certClass, err := validateCertClass(class)
			if err != nil {
				return err
			}
			binding, err := buildEvaluationBinding(scopeID, runID, evidenceWindowStartUnixMs, evidenceWindowEndUnixMs, assertionAssessmentIDs, attemptIDs, scenarioIDs, actionIDs)
			if err != nil {
				return err
			}
			// Opened without Initialize so evaluation never generates a master
			// key. Without a usable key store evaluation fails closed.
			ks, ksErr := keystore.NewWithFS(fileSvc, slog.Default(), keystore.Options{MasterKeyFile: masterKeyFile})
			if ksErr != nil {
				return fmt.Errorf("%w: open key store: %w", constants.ErrVaultKeyReadFailed, ksErr)
			}
			resultSet, err := evaluateKSIs(ctx, fileSvc, ks, cat, certClass, binding)
			if err != nil {
				return fmt.Errorf("compliance: cannot evaluate KSIs: %w", err)
			}

			historyStore := newKSIHistoryStore(fileSvc)
			if err := saveKSIHistorySnapshot(ctx, historyStore, resultSet); err != nil {
				slog.Default().Warn("compliance: failed to save KSI history snapshot", "error", err)
			}
			if err := newKSIUnavailableIntervalStore(fileSvc).AppendFromResultSet(ctx, resultSet); err != nil {
				return fmt.Errorf("compliance: save KSI unavailable intervals: %w", err)
			}

			output, err := json.MarshalIndent(resultSet, "", "  ")
			if err != nil {
				return fmt.Errorf("compliance: marshal KSI result set: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), string(output))
			return nil
		},
	}

	cmd.Flags().StringVar(&class, "class", "C", "FedRAMP 20x certification class (A, B, C, D)")
	cmd.Flags().StringVar(&catalogPath, "catalog", constants.DefaultKSICatalogPath, "Path to KSI catalog JSON file")
	cmd.Flags().StringVar(&scopeID, "scope-id", "", "Assessment scope ID binding every result to the declared context (required)")
	cmd.Flags().StringVar(&runID, "run-id", "", "Assessment run ID binding every result to the declared context (required)")
	cmd.Flags().StringSliceVar(&assertionAssessmentIDs, "assertion-assessment-id", nil, "Assertion assessment ID consumed by the KSI evaluation (repeatable, required)")
	cmd.Flags().StringSliceVar(&attemptIDs, "attempt-id", nil, "Allowed evidence attempt ID (repeatable)")
	cmd.Flags().StringSliceVar(&scenarioIDs, "scenario-id", nil, "Allowed evidence scenario ID (repeatable)")
	cmd.Flags().StringSliceVar(&actionIDs, "action-id", nil, "Allowed evidence action or transaction ID (repeatable)")
	cmd.Flags().Int64Var(&evidenceWindowStartUnixMs, "evidence-window-start-unix-ms", 0, "Inclusive start of the evidence collection interval in Unix milliseconds (required)")
	cmd.Flags().Int64Var(&evidenceWindowEndUnixMs, "evidence-window-end-unix-ms", 0, "Inclusive end of the evidence collection interval in Unix milliseconds (required)")
	cmd.Flags().StringVar(&masterKeyFile, "master-key-file", "", "Absolute path to a provisioned base64 32-byte master key outside the runtime root; Linux mode 0400 or 0600. Blank uses the OS key store. No fallback on failure.")
	_ = cmd.MarkFlagRequired("scope-id")
	_ = cmd.MarkFlagRequired("run-id")
	_ = cmd.MarkFlagRequired("assertion-assessment-id")
	_ = cmd.MarkFlagRequired("evidence-window-start-unix-ms")
	_ = cmd.MarkFlagRequired("evidence-window-end-unix-ms")

	return cmd
}

// buildEvaluationBinding constructs an EvaluationBinding from CLI flags using
// the caller-declared evidence collection interval.
func buildEvaluationBinding(scopeID, runID string, evidenceWindowStartUnixMs, evidenceWindowEndUnixMs int64, assertionAssessmentIDs, attemptIDs, scenarioIDs, actionIDs []string) (compliance.EvaluationBinding, error) {
	binding := compliance.EvaluationBinding{
		ScopeID:            scopeID,
		RunID:              runID,
		WindowStartUnixMs:  evidenceWindowStartUnixMs,
		WindowEndUnixMs:    evidenceWindowEndUnixMs,
		EvaluatorID:        constants.KSIEvaluatorID,
		EvaluatorVersion:   constants.KSIEvaluatorVersion,
		MethodDefinitionID: constants.KSIMethodDefinitionVersion,
		AssertionAssessments: compliance.AssertionAssessmentScope{
			AssessmentIDs: assertionAssessmentIDs,
			AttemptIDs:    attemptIDs,
			ScenarioIDs:   scenarioIDs,
			ActionIDs:     actionIDs,
		},
	}
	if err := binding.Validate(); err != nil {
		return compliance.EvaluationBinding{}, fmt.Errorf("compliance: build evaluation binding: %w", err)
	}
	return binding, nil
}

// loadKSICatalog reads and validates the KSI catalog from the given path.
func loadKSICatalog(path string) (*compliance.KSICatalog, error) {
	cat, err := compliance.LoadKSICatalog(path)
	if err != nil {
		return nil, fmt.Errorf("compliance: load KSI catalog: %w", err)
	}
	return cat, nil
}

// evaluateKSIs opens the runtime stores, creates a KSI evaluator, registers
// default methods, and runs evaluation bound to the given binding. Any store,
// vault, or evaluation failure is returned wrapped; the result set is nil
// whenever the error is non-nil.
func evaluateKSIs(ctx context.Context, fileSvc fs.RuntimeFileService, ks *keystore.Keystore, cat *compliance.KSICatalog, class compliance.CertificationClass, binding compliance.EvaluationBinding) (*compliance.KSIResultSet, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	deps, cleanup, err := openEvaluatorDeps(fileSvc, ks)
	if err != nil {
		return nil, fmt.Errorf("compliance: open evaluator deps: %w", err)
	}
	defer cleanup()

	evaluator := compliance.NewKSIEvaluator(cat)
	if err := evaluator.RegisterDefaultMethods(deps); err != nil {
		return nil, fmt.Errorf("compliance: register KSI methods: %w", err)
	}

	resultSet, err := evaluator.Evaluate(ctx, class, binding)
	if err != nil {
		return nil, fmt.Errorf("compliance: evaluate KSIs: %w", err)
	}
	return resultSet, nil
}

// openEvaluatorDeps opens the runtime vault, audit store, git ledger, and
// commitment ledger from the runtime file service. Each evidence source is
// opened by a dedicated opener (openVault, openAuditStore, openLedger); this
// function composes them and aggregates their cleanups so a failure in a later
// opener releases resources acquired by earlier ones. A vault failure is
// returned as-is, so callers see ErrAuditStoreVaultLocked-class errors rather
// than an evaluation over locked content.
func openEvaluatorDeps(fileSvc fs.RuntimeFileService, ks *keystore.Keystore) (compliance.EvaluatorDeps, func(), error) {
	var cleanups []func()

	v, vaultCleanup, err := openVault(ks)
	if err != nil {
		return compliance.EvaluatorDeps{}, nil, fmt.Errorf("compliance: open runtime vault: %w", err)
	}
	cleanups = append(cleanups, vaultCleanup)

	auditStore, auditCleanup, ok := openAuditStore(fileSvc, v)
	if !ok {
		runCleanups(cleanups)
		return compliance.EvaluatorDeps{}, nil, fmt.Errorf("compliance: open audit store: %w", constants.ErrReportStoreUnavailable)
	}
	cleanups = append(cleanups, auditCleanup)
	cl := auditStore.CommitmentLedger()

	ledger, ledgerCleanup, ok := openLedger(fileSvc, v)
	if !ok {
		runCleanups(cleanups)
		return compliance.EvaluatorDeps{}, nil, fmt.Errorf("compliance: open ledger: %w", constants.ErrReportStoreUnavailable)
	}
	cleanups = append(cleanups, ledgerCleanup)

	return compliance.EvaluatorDeps{
		Audit:       auditStore,
		Ledger:      ledger,
		Commitments: cl,
		History:     newKSIHistoryStore(fileSvc),
	}, func() { runCleanups(cleanups) }, nil
}

// runCleanups invokes each cleanup in reverse acquisition order (LIFO) so
// resources are released in the opposite order they were opened.
func runCleanups(cleanups []func()) {
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

// openVault unlocks the runtime vault through ks.OpenVault. It fails closed: a
// nil keystore, an unreadable vault key, or a failed unlock returns a wrapped
// error and no vault. There is no locked or metadata-only fallback, because
// every content column in the runtime stores is encrypted. On success the
// cleanup locks the vault; the caller must run it once the vault is no longer
// in use.
func openVault(ks *keystore.Keystore) (*vault.Vault, func(), error) {
	if ks == nil {
		return nil, nil, fmt.Errorf("%w: key store not provided", constants.ErrVaultKeyReadFailed)
	}
	v, err := ks.OpenVault()
	if err != nil {
		return nil, nil, err
	}
	return v, v.Lock, nil
}

// openRuntimeVault opens the runtime key store (the OS key store, or the
// operator-provisioned master key file when masterKeyFile is set) without
// initializing a new master key, then unlocks the vault through openVault.
// Commands that read encrypted runtime content call it and propagate its error.
func openRuntimeVault(fileSvc fs.RuntimeFileService, masterKeyFile string) (*vault.Vault, func(), error) {
	ks, err := keystore.NewWithFS(fileSvc, slog.Default(), keystore.Options{MasterKeyFile: masterKeyFile})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: open key store: %w", constants.ErrVaultKeyReadFailed, err)
	}
	return openVault(ks)
}

// openAuditStore opens ztd.db once and builds the SQL audit store over it; the
// store's commitment ledger shares the same pool. Returns ok=false on failure.
func openAuditStore(fileSvc fs.RuntimeFileService, v *vault.Vault) (*storage.SQLAuditStore, func(), bool) {
	if err := fileSvc.MkdirAll(context.Background(), constants.DataDirname, constants.PermDirPrivate); err != nil {
		return nil, nil, false
	}
	mainDB, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), slog.Default())
	if err != nil {
		return nil, nil, false
	}
	auditCfg := storage.DefaultAuditStoreConfig()
	auditCfg.EncryptionVault = v
	auditStore, err := storage.NewSQLAuditStore(auditCfg, mainDB, slog.Default(), fileSvc)
	if err != nil {
		mainDB.Close()
		return nil, nil, false
	}
	return auditStore, func() {
		auditStore.Close()
		mainDB.Close()
	}, true
}

// openLedger opens the git-backed ledger service. Returns ok=false on failure.
func openLedger(fileSvc fs.RuntimeFileService, v *vault.Vault) (*storage.GitLedgerService, func(), bool) {
	ledgerCfg := &storage.LedgerConfig{
		EncryptionVault: v,
	}
	ledger, err := storage.NewGitLedgerService(ledgerCfg, slog.Default(), fileSvc)
	if err != nil {
		return nil, nil, false
	}
	return ledger, func() {}, true
}

// complianceKSIHistoryCmdWithConfig creates the `compliance ksi-history` subcommand
// with injectable dependencies for testing.
func complianceKSIHistoryCmdWithConfig(fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error)) *cobra.Command {
	var ksiID string

	cmd := &cobra.Command{
		Use:   "ksi-history",
		Short: "Read KSI evaluation history snapshots",
		Long: `Read persisted KSI evaluation snapshots from .ztd/data/compliance/ksi-history/.
When --ksi is specified, prints the chronological series for that KSI ID.
Without --ksi, prints all snapshots as a JSON array.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fileSvc, err := fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			store := newKSIHistoryStore(fileSvc)

			if ksiID != "" {
				results, err := store.GetHistoryForKSI(ctx, ksiID)
				if err != nil {
					return err
				}
				output, err := json.MarshalIndent(results, "", "  ")
				if err != nil {
					return fmt.Errorf("compliance: marshal KSI history: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(output))
				return nil
			}

			snapshots, err := store.ListSnapshots(ctx)
			if err != nil {
				return err
			}
			if len(snapshots) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "[]")
				return nil
			}
			output, err := json.MarshalIndent(snapshots, "", "  ")
			if err != nil {
				return fmt.Errorf("compliance: marshal KSI history: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(output))
			return nil
		},
	}

	cmd.Flags().StringVar(&ksiID, "ksi", "", "KSI ID to filter history (e.g. KSI-CMT-01)")

	return cmd
}

// newKSIHistoryStore constructs a KSIHistoryStore rooted at the canonical
// runtime KSI history directory. Centralized so callers do not each rebuild
// the directory path and store independently.
func newKSIHistoryStore(fileSvc fs.RuntimeFileService) *compliance.KSIHistoryStore {
	historyDir := filepath.Join(constants.DataDirname, constants.ComplianceDirname, constants.KSIHistoryDirname)
	return compliance.NewKSIHistoryStore(fileSvc, historyDir)
}

func newKSIUnavailableIntervalStore(fileSvc fs.RuntimeFileService) *compliance.UnavailableIntervalStore {
	return compliance.NewUnavailableIntervalStore(fileSvc, filepath.Join(constants.DataDirname, constants.ComplianceDirname))
}

// saveKSIHistorySnapshot persists a KSIResultSet snapshot via the given history
// store. Prunes snapshots older than the retention period after saving.
func saveKSIHistorySnapshot(ctx context.Context, store *compliance.KSIHistoryStore, rs *compliance.KSIResultSet) error {
	if err := store.SaveSnapshot(ctx, rs); err != nil {
		return err
	}

	retention := time.Duration(constants.KSIHistoryRetentionDays) * 24 * time.Hour
	if _, err := store.PruneOlderThan(ctx, retention); err != nil {
		slog.Default().Warn("compliance: failed to prune KSI history", "error", err)
	}

	return nil
}

// validateCertClass validates that the class flag is one of A, B, C, D.
func validateCertClass(class string) (compliance.CertificationClass, error) {
	if len(class) != 1 || !strings.ContainsRune("ABCD", rune(class[0])) {
		return "", fmt.Errorf("%w: invalid certification class %q (must be A, B, C, or D)", constants.ErrValidationFailed, class)
	}
	return compliance.CertificationClass(class), nil
}

// complianceOverlayCmdWithConfig creates the `compliance overlay` subcommand
// with injectable dependencies for testing. It loads COSAiS overlay catalogs
// from a directory and validates KSI overlay references.
func complianceOverlayCmdWithConfig(fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error)) *cobra.Command {
	var overlayDir string
	var catalogPath string

	cmd := &cobra.Command{
		Use:   "overlay",
		Short: "Load and validate COSAiS overlay catalogs",
		Long: `Load COSAiS (Control Overlays for Securing AI Systems) overlay JSON files
from the specified directory and validate that KSI overlay references resolve.
Prints the merged overlay catalog as JSON. Use --overlay-dir to specify a
custom overlay directory (default: docs/reference/).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// fileSvc is initialized for error-injection test parity with the
			// other compliance subcommands. The overlay command reads from
			// docs/reference/ (not .ztd/), so fileSvc itself is unused here.
			if _, err := fileSvcFactory("", slog.Default()); err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}

			overlayCat, err := compliance.LoadOverlaysFromDir(overlayDir)
			if err != nil {
				return fmt.Errorf("compliance: load overlays: %w", err)
			}

			if len(overlayCat.Overlays) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "[]")
				return nil
			}

			ksiCat, err := loadKSICatalog(catalogPath)
			if err != nil {
				return err
			}

			dangling := compliance.ValidateOverlayRefs(ksiCat, overlayCat)
			if len(dangling) > 0 {
				cmd.Printf("WARNING: %d dangling overlay reference(s):\n", len(dangling))
				for _, ref := range dangling {
					cmd.Printf("  %s\n", ref)
				}
			}

			output, err := json.MarshalIndent(overlayCat, "", "  ")
			if err != nil {
				return fmt.Errorf("compliance: marshal overlay catalog: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(output))
			return nil
		},
	}

	cmd.Flags().StringVar(&overlayDir, "overlay-dir", constants.DefaultOverlayDirPath, "Directory containing COSAiS overlay JSON files")
	cmd.Flags().StringVar(&catalogPath, "catalog", constants.DefaultKSICatalogPath, "Path to KSI catalog JSON file")

	return cmd
}
