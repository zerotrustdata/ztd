// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"crypto/ed25519"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestLocalFirstEventsStore_InitDatabase_CommitmentLedgerWardenIntentColumn verifies that
// initDatabase creates the commitment_ledger table with the protocol-aligned
// warden_intent_signature_digest column directly from the schema DDL.
func TestLocalFirstEventsStore_InitDatabase_CommitmentLedgerWardenIntentColumn(t *testing.T) {
	tempDir := testutil.TempDir(t)

	fileSvc := newTestFileSvc(t, tempDir)

	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc, privKey)

	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(fileSvc, sqliteutil.DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	config := &LocalFirstEventsStoreConfig{
		RetentionDays:        7,
		PruneIntervalMinutes: 60,
		EncryptionVault:      testVault,
	}

	ass, err := NewLocalFirstEventsStore(config, db, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { ass.Close() })

	type columnInfo struct {
		CID  int
		Name string
		Type string
	}

	rows, err := ass.db.Query("PRAGMA table_info(commitment_ledger)")
	require.NoError(t, err)
	t.Cleanup(func() { rows.Close() })

	var columns []columnInfo
	for rows.Next() {
		var ci columnInfo
		var notNull, pk int
		var dflt sql.NullString
		require.NoError(t, rows.Scan(&ci.CID, &ci.Name, &ci.Type, &notNull, &dflt, &pk))
		columns = append(columns, ci)
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, columns, "commitment_ledger table should have columns")

	names := make(map[string]bool, len(columns))
	for _, c := range columns {
		names[c.Name] = true
	}

	assert.True(t, names["warden_intent_signature_digest"],
		"commitment_ledger must have warden_intent_signature_digest column; got %v", names)
	assert.False(t, names["actuator_intent_signature_digest"],
		"commitment_ledger must not retain actuator_intent_signature_digest column; got %v", names)
}
