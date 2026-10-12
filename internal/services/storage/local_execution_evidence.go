// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"fmt"
	"log/slog"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	vault "github.com/zerotrustdata/ztd/internal/services/vault"
)

// LocalExecutionEvidenceConfig holds the runtime-owned inputs for the local
// evidence stores of one executing runtime. The outbound Operator and the
// Gateway's embedded Operator both build their stores through it, so they
// record the same evidence into their own runtime root.
type LocalExecutionEvidenceConfig struct {
	FileSvc    fs.RuntimeFileService
	Vault      *vault.Vault
	AuditStore *SQLAuditStore
	WorkDir    string
	Logger     *slog.Logger
}

// LocalExecutionEvidence is the git file ledger and history handler of one
// executing runtime. Execution records live in the audit store (events). The
// audit store is owned by the caller and is not closed here.
type LocalExecutionEvidence struct {
	Ledger         *GitLedgerService
	HistoryHandler *HistoryHandler
}

// OpenLocalExecutionEvidence opens the git file ledger and the history handler
// against the runtime file service in cfg. The ledger is required: a runtime
// that can mutate files always snapshots them.
func OpenLocalExecutionEvidence(cfg LocalExecutionEvidenceConfig) (*LocalExecutionEvidence, error) {
	if cfg.FileSvc == nil {
		return nil, fmt.Errorf("local execution evidence: file service: %w", constants.ErrMissingRequiredField)
	}
	if cfg.Vault == nil {
		return nil, fmt.Errorf("local execution evidence: %w", constants.ErrLedgerVaultRequired)
	}
	if cfg.AuditStore == nil {
		return nil, fmt.Errorf("local execution evidence: %w", constants.ErrAuditStoreRequired)
	}

	ledger, err := NewGitLedgerService(&LedgerConfig{EncryptionVault: cfg.Vault}, cfg.Logger, cfg.FileSvc)
	if err != nil {
		return nil, fmt.Errorf("local execution evidence: %w", err)
	}
	cfg.Logger.Info("Ledger initialized")
	historyHandler, err := NewHistoryHandler(cfg.AuditStore, ledger, cfg.Logger, cfg.FileSvc, cfg.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("local execution evidence: %w", err)
	}
	cfg.Logger.Info("History Handler initialized (FETCH_HISTORY ready)")
	return &LocalExecutionEvidence{Ledger: ledger, HistoryHandler: historyHandler}, nil
}
