// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// ─── AuditStore List tests ───────────────────────────────────────────────────

func TestListSessions_NilStore(t *testing.T) {
	t.Parallel()

	var ass *SQLAuditStore
	sessions, err := ass.ListSessions(10, 0)
	require.Error(t, err)
	assert.Nil(t, sessions)
	assert.ErrorIs(t, err, constants.ErrAuditStoreDisabled)
}

func TestListSessions_NilDB(t *testing.T) {
	t.Parallel()

	ass := &SQLAuditStore{db: nil}
	sessions, err := ass.ListSessions(10, 0)
	require.Error(t, err)
	assert.Nil(t, sessions)
	assert.ErrorIs(t, err, constants.ErrAuditStoreDisabled)
}

func TestListEvents_NilStore(t *testing.T) {
	t.Parallel()

	var ass *SQLAuditStore
	events, err := ass.ListEvents("", 10, 0)
	require.Error(t, err)
	assert.Nil(t, events)
	assert.ErrorIs(t, err, constants.ErrAuditStoreDisabled)
}

func TestListEvents_NilDB(t *testing.T) {
	t.Parallel()

	ass := &SQLAuditStore{db: nil}
	events, err := ass.ListEvents("session-1", 10, 0)
	require.Error(t, err)
	assert.Nil(t, events)
	assert.ErrorIs(t, err, constants.ErrAuditStoreDisabled)
}

func TestListFileMutations_NilStore(t *testing.T) {
	t.Parallel()
	var ass *SQLAuditStore
	mutations, err := ass.ListFileMutations(10, 0)
	require.Error(t, err)
	assert.Nil(t, mutations)
	assert.ErrorIs(t, err, constants.ErrAuditStoreDisabled)
}

func TestListFileMutations_NilDB(t *testing.T) {
	t.Parallel()
	ass := &SQLAuditStore{db: nil}
	mutations, err := ass.ListFileMutations(10, 0)
	require.Error(t, err)
	assert.Nil(t, mutations)
	assert.ErrorIs(t, err, constants.ErrAuditStoreDisabled)
}

// ─── CommitmentLedger ListCommitments tests ──────────────────────────────────

func TestListCommitments_NilLedger(t *testing.T) {
	t.Parallel()
	var cl *CommitmentLedger
	commitments, err := cl.ListCommitments()
	require.Error(t, err)
	assert.Nil(t, commitments)
	assert.Contains(t, err.Error(), "not initialized")
}

func TestListCommitments_NilDB(t *testing.T) {
	t.Parallel()
	cl := &CommitmentLedger{db: nil, logger: testutil.NewTestLogger()}
	commitments, err := cl.ListCommitments()
	require.Error(t, err)
	assert.Nil(t, commitments)
	assert.Contains(t, err.Error(), "not initialized")
}

func TestListCommitments_Empty(t *testing.T) {
	t.Parallel()

	cl, _ := setupTestCommitmentLedger(t)
	commitments, err := cl.ListCommitments()
	require.NoError(t, err)
	assert.Empty(t, commitments)
}

func TestListCommitments_WithData(t *testing.T) {
	t.Parallel()

	cl, _ := setupTestCommitmentLedger(t)

	for i := 0; i < 3; i++ {
		attestation := []byte(fmt.Sprintf(`{
			"transaction_id": "tx-%d",
			"transaction_hash": "thash-%d",
			"state_root_at_commit": "sr-%d",
			"l2_signature_digest": "l2-%d",
			"warden_intent_signature_digest": "act-%d",
			"human_signature_digest": "hsig-%d",
			"action_type": "write",
			"target_resource": "/file%d",
			"committed_at_unix_ms": %d,
			"auditor_key_id": "aud-%d",
			"signature": "sig-%d"
		}`, i, i, i, i, i, i, i, 1000+i, i, i))
		var priorHash string
		if i > 0 {
			priorHash = fmt.Sprintf("hash-%d", i-1)
		}
		err := cl.AppendCommitmentJSON(attestation, priorHash, fmt.Sprintf("hash-%d", i))
		require.NoError(t, err)
	}

	commitments, err := cl.ListCommitments()
	require.NoError(t, err)
	assert.Len(t, commitments, 3)

	assert.Equal(t, int64(1), commitments[0].Seq)
	assert.Equal(t, int64(3), commitments[2].Seq)
	assert.Equal(t, "tx-0", commitments[0].TransactionID)
	assert.Equal(t, "tx-2", commitments[2].TransactionID)
}

func TestListCommitments_OrderedByCommittedAt(t *testing.T) {
	t.Parallel()

	cl, _ := setupTestCommitmentLedger(t)

	attestation1 := []byte(`{
		"transaction_id": "tx-first",
		"transaction_hash": "thash-1",
		"committed_at_unix_ms": 1000,
		"action_type": "write",
		"target_resource": "/file1",
		"auditor_key_id": "aud1",
		"signature": "sig1"
	}`)
	require.NoError(t, cl.AppendCommitmentJSON(attestation1, "", "hash-1"))

	attestation2 := []byte(`{
		"transaction_id": "tx-second",
		"transaction_hash": "thash-2",
		"committed_at_unix_ms": 2000,
		"action_type": "write",
		"target_resource": "/file2",
		"auditor_key_id": "aud2",
		"signature": "sig2"
	}`)
	require.NoError(t, cl.AppendCommitmentJSON(attestation2, "hash-1", "hash-2"))

	commitments, err := cl.ListCommitments()
	require.NoError(t, err)
	assert.Len(t, commitments, 2)
	assert.Equal(t, "tx-first", commitments[0].TransactionID)
	assert.Equal(t, "tx-second", commitments[1].TransactionID)
	assert.True(t, commitments[0].CommittedAt.Before(commitments[1].CommittedAt))
}
