// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package reporting provides read-only CSV export of all ztd persistent stores.
// It emits one file per logical record type plus a cryptographic verification pass.
package reporting

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

// Options configures a reporting run.
type Options struct {
	// FileSvc owns runtime-relative database and ledger paths.
	FileSvc fs.RuntimeFileService
	// Keystore holds the master-key-wrapped vault key. Required: reports read the
	// encrypted stores, so a missing or unusable key fails the run.
	Keystore *keystore.Keystore
	// OutDir is the external directory to write CSV files into.
	OutDir string
	// Logger is optional; if nil, slog.Default() is used.
	Logger *slog.Logger
}

// RunResult summarises a completed reporting run.
type RunResult struct {
	OutDir    string
	FailCount int
	Files     []FileResult
}

// Run performs a full CSV export of all stores and returns a RunResult.
// A non-zero RunResult.FailCount means at least one verification check failed.
func Run(ctx context.Context, opts Options) (RunResult, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.FileSvc == nil {
		return RunResult{}, fmt.Errorf("%w: runtime file service", constants.ErrMissingRequiredField)
	}

	if err := os.MkdirAll(opts.OutDir, constants.PermDirPrivate); err != nil {
		return RunResult{}, fmt.Errorf("%w: %s: %w", constants.ErrReportOutputDirFailed, opts.OutDir, err)
	}

	fileSvc := opts.FileSvc

	// Open the vault. The report reads encrypted content, so an unlocked vault is required.
	v, err := openVault(opts.Keystore)
	if err != nil {
		return RunResult{}, err
	}

	auditorKey, err := opts.Keystore.SigningPublicKey(constants.SecretsFileAuditorSigningKey)
	if err != nil {
		return RunResult{}, fmt.Errorf("report: auditor trust anchor: %w", err)
	}
	actuatorKey, err := opts.Keystore.SigningPublicKey(constants.SecretsFileActuatorSigningKey)
	if err != nil {
		return RunResult{}, fmt.Errorf("report: actuator trust anchor: %w", err)
	}

	// Open ztd.db once; the local-first events store and its commitment ledger share the pool.
	if err := fileSvc.MkdirAll(context.Background(), constants.DataDirname, constants.PermDirPrivate); err != nil {
		return RunResult{}, fmt.Errorf("%w: data dir: %w", constants.ErrReportStoreUnavailable, err)
	}
	mainDB, err := sqliteutil.OpenDB(fileSvc, sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), logger)
	if err != nil {
		return RunResult{}, fmt.Errorf("%w: main db: %w", constants.ErrReportStoreUnavailable, err)
	}
	defer mainDB.Close()

	// Open the local-first events store; event receipts project its events rows.
	eventsStoreCfg := storage.DefaultLocalFirstEventsStoreConfig()
	eventsStoreCfg.EncryptionVault = v
	eventsStoreCfg.PinnedAuditorPublicKey = auditorKey
	eventsStore, err := storage.NewLocalFirstEventsStore(eventsStoreCfg, mainDB, logger, fileSvc)
	if err != nil {
		return RunResult{}, fmt.Errorf("%w: local-first events store: %w", constants.ErrReportStoreUnavailable, err)
	}
	defer eventsStore.Close()
	cl := eventsStore.CommitmentLedger()

	// Replay store shares the ztd.db pool.
	rs, err := storage.NewReplayStore(mainDB, logger)
	if err != nil {
		return RunResult{}, fmt.Errorf("%w: replay store: %w", constants.ErrReportStoreUnavailable, err)
	}

	// Suspended transaction store shares the ztd.db pool and decrypts with the vault.
	sts, err := storage.NewSuspendedTransactionService(mainDB, v, logger)
	if err != nil {
		return RunResult{}, fmt.Errorf("%w: suspended transaction store: %w", constants.ErrReportStoreUnavailable, err)
	}

	// Open git ledger.
	ledger, err := storage.NewGitLedgerService(&storage.LedgerConfig{
		EncryptionVault: v,
	}, logger, fileSvc)
	if err != nil {
		return RunResult{}, fmt.Errorf("%w: git ledger: %w", constants.ErrReportStoreUnavailable, err)
	}

	now := time.Now().UTC()
	var allFiles []FileResult

	run := func(name string, fn func() (FileResult, error)) {
		if ctx.Err() != nil {
			return
		}
		res, err := fn()
		if err != nil {
			logger.Warn("Reporter failed", "name", name, "error", err)
			return
		}
		logger.Info("Wrote CSV", "file", res.Filename, "rows", res.RowCount)
		allFiles = append(allFiles, res)
	}

	// Per-store reporters.
	run("receipts", func() (FileResult, error) { return reportReceipts(ctx, opts.OutDir, eventsStore) })
	run("sessions", func() (FileResult, error) { return reportSessions(ctx, opts.OutDir, eventsStore) })
	run("events", func() (FileResult, error) { return reportEvents(ctx, opts.OutDir, eventsStore) })
	run("file_mutations", func() (FileResult, error) { return reportFileMutations(ctx, opts.OutDir, eventsStore) })
	run("commitments", func() (FileResult, error) { return reportCommitments(ctx, opts.OutDir, cl) })

	run("replay_nonces", func() (FileResult, error) { return reportReplayNonces(ctx, opts.OutDir, rs) })
	run("suspended_transactions", func() (FileResult, error) {
		return reportSuspendedTransactions(ctx, opts.OutDir, sts)
	})
	run("ledger_merkle_root", func() (FileResult, error) { return reportLedgerMerkleRoot(ctx, opts.OutDir, ledger) })
	run("ledger_commits", func() (FileResult, error) { return reportLedgerCommits(ctx, opts.OutDir, ledger) })

	// Verification pass.
	var failCount int
	if ctx.Err() == nil {
		verRes, vr, verErr := reportVerification(ctx, opts.OutDir, eventsStore, cl, ledger, governance.NewPinnedSignerSource(auditorKey), governance.NewPinnedSignerSource(actuatorKey))
		if verErr != nil {
			logger.Warn("Verification reporter failed", "error", verErr)
		} else {
			allFiles = append(allFiles, verRes)
			failCount = vr.FailCount
			logger.Info("Verification complete", "fail_count", failCount)
		}
	}

	// Manifest.
	if ctx.Err() == nil {
		manifestPath := filepath.Join(opts.OutDir, constants.ReportManifestFilename)
		var mRows []Row
		for _, f := range allFiles {
			mRows = append(mRows, ManifestRow{
				File:           f.Filename,
				RecordType:     recordTypeForFile(f.Filename),
				RowCount:       intStr(f.RowCount),
				SHA256:         f.SHA256,
				GeneratedAtUTC: utcRFC3339(now),
			})
		}
		mRes, mErr := writeCSV(manifestPath, ManifestRow{}.Columns(), mRows)
		if mErr != nil {
			logger.Warn("Failed to write manifest", "error", mErr)
		} else {
			mRes.Filename = constants.ReportManifestFilename
			allFiles = append(allFiles, mRes)
			logger.Info("Wrote manifest", "file", manifestPath, "entries", mRes.RowCount)
		}
	}

	return RunResult{
		OutDir:    opts.OutDir,
		FailCount: failCount,
		Files:     allFiles,
	}, nil
}

// openVault unlocks the runtime vault through ks.OpenVault. Fail-closed: a
// missing keystore, an unreadable key, or a failed unlock returns
// ErrReportVaultUnavailable. There is no metadata-only mode.
func openVault(ks *keystore.Keystore) (*vault.Vault, error) {
	if ks == nil {
		return nil, fmt.Errorf("%w: keystore not configured", constants.ErrReportVaultUnavailable)
	}
	v, err := ks.OpenVault()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrReportVaultUnavailable, err)
	}
	return v, nil
}

// recordTypeForFile derives a human-readable record type from a CSV filename.
func recordTypeForFile(filename string) string {
	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}
