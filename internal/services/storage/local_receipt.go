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
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	vault "github.com/zerotrustdata/ztd/internal/services/vault"
)

// ReadLocalEventReceipt opens one runtime's canonical audit database through
// fileSvc and returns the signed receipt that runtime recorded for
// transactionID, decrypted with the runtime's own vault key from the key store
// opts selects (zero Options select the OS key store). It reads only the local, authoritative store and
// never consults a Gateway mirror. It returns nil, nil when no receipt exists.
func ReadLocalEventReceipt(fileSvc fs.RuntimeFileService, logger *slog.Logger, opts keystore.Options, transactionID string) (*models.EventReceiptRecord, error) {
	if fileSvc == nil {
		return nil, fmt.Errorf("local receipt: file service: %w", constants.ErrMissingRequiredField)
	}
	if logger == nil {
		logger = slog.Default()
	}
	ks, err := keystore.Open(fileSvc, logger, opts)
	if err != nil {
		return nil, fmt.Errorf("local receipt: open runtime keystore: %w", err)
	}
	key, err := ks.LoadVaultKey()
	if err != nil {
		return nil, fmt.Errorf("local receipt: %w: %w", constants.ErrVaultKeyReadFailed, err)
	}
	defer vault.SecureZero(key)
	v, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: logger})
	if err != nil {
		return nil, fmt.Errorf("local receipt: %w", err)
	}
	if err := v.Unlock(key); err != nil {
		return nil, fmt.Errorf("local receipt: unlock vault: %w", err)
	}
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), logger)
	if err != nil {
		return nil, fmt.Errorf("local receipt: %w", err)
	}
	defer db.Close()
	cfg := DefaultAuditStoreConfig()
	cfg.EncryptionVault = v
	store, err := NewSQLAuditStore(cfg, db, logger, fileSvc)
	if err != nil {
		return nil, fmt.Errorf("local receipt: %w", err)
	}
	defer store.Close()
	return store.GetEventReceipt(transactionID)
}
