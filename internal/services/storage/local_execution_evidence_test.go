// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"crypto/ed25519"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	vault "github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newLocalEvidenceFixture builds the runtime file service, unlocked vault, and
// audit store that OpenLocalExecutionEvidence needs, all under an isolated root.
func newLocalEvidenceFixture(t *testing.T) (fs.RuntimeFileService, *vault.Vault, *SQLAuditStore) {
	t.Helper()
	fileSvc := newTestFileSvc(t, testutil.TempDir(t))

	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc, privKey)

	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	auditConfig := DefaultAuditStoreConfig()
	auditConfig.EncryptionVault = testVault
	auditStore, err := NewSQLAuditStore(auditConfig, db, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, auditStore.Close()) })

	return fileSvc, testVault, auditStore
}

func localEvidenceConfig(t *testing.T, fileSvc fs.RuntimeFileService, v *vault.Vault, auditStore *SQLAuditStore) LocalExecutionEvidenceConfig {
	t.Helper()
	return LocalExecutionEvidenceConfig{
		FileSvc:    fileSvc,
		Vault:      v,
		AuditStore: auditStore,
		WorkDir:    testutil.TempDir(t),
		Logger:     testutil.NewTestLogger(),
	}
}

func TestOpenLocalExecutionEvidence_OpensLedgerAndHistory(t *testing.T) {
	fileSvc, v, auditStore := newLocalEvidenceFixture(t)

	evidence, err := OpenLocalExecutionEvidence(localEvidenceConfig(t, fileSvc, v, auditStore))
	require.NoError(t, err)
	require.NotNil(t, evidence.Ledger)
	require.NotNil(t, evidence.HistoryHandler)
}

func TestOpenLocalExecutionEvidence_RequiresDependencies(t *testing.T) {
	fileSvc, v, auditStore := newLocalEvidenceFixture(t)

	tests := []struct {
		name    string
		mutate  func(*LocalExecutionEvidenceConfig)
		wantErr error
	}{
		{"missing vault", func(c *LocalExecutionEvidenceConfig) { c.Vault = nil }, constants.ErrLedgerVaultRequired},
		{"missing audit store", func(c *LocalExecutionEvidenceConfig) { c.AuditStore = nil }, constants.ErrAuditStoreRequired},
		{"missing file service", func(c *LocalExecutionEvidenceConfig) { c.FileSvc = nil }, constants.ErrMissingRequiredField},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := localEvidenceConfig(t, fileSvc, v, auditStore)
			tc.mutate(&cfg)
			evidence, err := OpenLocalExecutionEvidence(cfg)
			require.Nil(t, evidence)
			require.ErrorIs(t, err, tc.wantErr, "err = %v, want %v", err, tc.wantErr)
		})
	}
}
