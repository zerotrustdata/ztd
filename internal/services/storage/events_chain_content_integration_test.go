//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func recordContentEvent(t *testing.T, ass *LocalFirstEventsStore, sessionID, text, stdout string) int64 {
	t.Helper()
	id, err := ass.RecordEvent(&Event{
		OperatorSessionID: sessionID,
		Timestamp:         time.Now().UTC(),
		Type:              constants.EventType("COMMAND_EXECUTION"),
		ContentText:       text,
		CommandRaw:        "echo " + text,
		CommandStdout:     stdout,
		CommandExitCode:   constants.ExitCodeNone,
	})
	require.NoError(t, err)
	return id
}

func setRawColumn(t *testing.T, ass *LocalFirstEventsStore, query string, args ...any) {
	t.Helper()
	_, err := ass.db.ExecWithRetry(t.Context(), query, args...)
	require.NoError(t, err)
}

func TestContentDigest_IsKeyedNotPlainSHA256(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("digest-session", constants.SessionTypeOperator, "", ""))
	// Short, low-entropy content with no execution id is the offline brute-force case.
	id := recordContentEvent(t, ass, "digest-session", "yes", "")

	var stored string
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT content_digest FROM events WHERE id = ?`, id).Scan(&stored))

	raw, err := json.Marshal(eventContentDigestInput{
		ContentText:     "yes",
		CommandRaw:      "echo yes",
		CommandExitCode: constants.ExitCodeNone,
	})
	require.NoError(t, err)
	plain := sha256.Sum256(raw)
	assert.NotEqual(t, hex.EncodeToString(plain[:]), stored, "content_digest must not be the unkeyed SHA-256 of the content")
	assert.Len(t, stored, 64)

	require.NoError(t, ass.VerifyChainContent(t.Context(), 0))
}

func TestVerifyChainContent_DetectsTamperedCiphertext(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("tamper-session", constants.SessionTypeOperator, "", ""))
	first := recordContentEvent(t, ass, "tamper-session", "first", "out-one")
	recordContentEvent(t, ass, "tamper-session", "second", "out-two")
	require.NoError(t, ass.VerifyChainContent(t.Context(), 0))

	blob := rawColumn(t, ass, `SELECT command_stdout FROM events WHERE id = ?`, first)
	require.NotEmpty(t, blob)
	blob[len(blob)-1] ^= 0xff
	setRawColumn(t, ass, `UPDATE events SET command_stdout = ? WHERE id = ?`, blob, first)

	// The hash walk only covers the stored digest, so it cannot see the altered blob.
	require.NoError(t, ass.VerifyChain(t.Context(), 0))
	err := ass.VerifyChainContent(t.Context(), 0)
	require.ErrorIs(t, err, constants.ErrAuditChainContentMismatch)
}

func TestVerifyChainContent_DetectsSwappedCiphertext(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("swap-session", constants.SessionTypeOperator, "", ""))
	first := recordContentEvent(t, ass, "swap-session", "first", "out-one")
	second := recordContentEvent(t, ass, "swap-session", "second", "out-two")
	require.NoError(t, ass.VerifyChainContent(t.Context(), 0))

	t.Run("same column across rows", func(t *testing.T) {
		a := rawColumn(t, ass, `SELECT command_stdout FROM events WHERE id = ?`, first)
		b := rawColumn(t, ass, `SELECT command_stdout FROM events WHERE id = ?`, second)
		setRawColumn(t, ass, `UPDATE events SET command_stdout = ? WHERE id = ?`, b, first)
		setRawColumn(t, ass, `UPDATE events SET command_stdout = ? WHERE id = ?`, a, second)
		require.ErrorIs(t, ass.VerifyChainContent(t.Context(), 0), constants.ErrAuditChainContentMismatch)

		// The read path refuses the moved blob as well, instead of returning another row's output.
		_, err := ass.GetEvents(scopeForSession("swap-session"), 10, 0)
		require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreDecryptFailed)

		setRawColumn(t, ass, `UPDATE events SET command_stdout = ? WHERE id = ?`, a, first)
		setRawColumn(t, ass, `UPDATE events SET command_stdout = ? WHERE id = ?`, b, second)
		require.NoError(t, ass.VerifyChainContent(t.Context(), 0))
	})

	t.Run("different columns of one row", func(t *testing.T) {
		text := rawColumn(t, ass, `SELECT content_text FROM events WHERE id = ?`, first)
		raw := rawColumn(t, ass, `SELECT command_raw FROM events WHERE id = ?`, first)
		setRawColumn(t, ass, `UPDATE events SET content_text = ?, command_raw = ? WHERE id = ?`, raw, text, first)
		require.ErrorIs(t, ass.VerifyChainContent(t.Context(), 0), constants.ErrAuditChainContentMismatch)

		setRawColumn(t, ass, `UPDATE events SET content_text = ?, command_raw = ? WHERE id = ?`, text, raw, first)
		require.NoError(t, ass.VerifyChainContent(t.Context(), 0))
	})
}

func TestEncryptedColumnsAreBoundToTheirRow(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("bind-a", constants.SessionTypeOperator, "title a", "identity a"))
	require.NoError(t, ass.CreateSession("bind-b", constants.SessionTypeOperator, "title b", "identity b"))

	a := rawColumn(t, ass, `SELECT title FROM sessions WHERE id = ?`, "bind-a")
	b := rawColumn(t, ass, `SELECT title FROM sessions WHERE id = ?`, "bind-b")
	setRawColumn(t, ass, `UPDATE sessions SET title = ? WHERE id = ?`, b, "bind-a")
	setRawColumn(t, ass, `UPDATE sessions SET title = ? WHERE id = ?`, a, "bind-b")
	_, err := ass.GetOperatorSession("bind-a")
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreDecryptFailed)

	// A blob moved to another column of the same row fails too.
	setRawColumn(t, ass, `UPDATE sessions SET title = ? WHERE id = ?`, a, "bind-a")
	identity := rawColumn(t, ass, `SELECT user_identity FROM sessions WHERE id = ?`, "bind-a")
	setRawColumn(t, ass, `UPDATE sessions SET title = ? WHERE id = ?`, identity, "bind-a")
	_, err = ass.GetOperatorSession("bind-a")
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreDecryptFailed)

	// Two file mutations of one event are bound to distinct row identities.
	eventID := recordContentEvent(t, ass, "bind-b", "files", "")
	for _, path := range []string{"/a.txt", "/b.txt"} {
		require.NoError(t, ass.RecordFileMutation(&FileMutationLog{EventID: eventID, Filepath: path, Operation: FileMutationWrite, DiffStat: "1 file"}))
	}
	mutations, err := ass.GetFileMutations(eventID)
	require.NoError(t, err)
	require.Len(t, mutations, 2)
	pathA := rawColumn(t, ass, `SELECT filepath FROM file_mutation_log WHERE id = ?`, mutations[0].ID)
	pathB := rawColumn(t, ass, `SELECT filepath FROM file_mutation_log WHERE id = ?`, mutations[1].ID)
	setRawColumn(t, ass, `UPDATE file_mutation_log SET filepath = ? WHERE id = ?`, pathB, mutations[0].ID)
	setRawColumn(t, ass, `UPDATE file_mutation_log SET filepath = ? WHERE id = ?`, pathA, mutations[1].ID)
	_, err = ass.GetFileMutations(eventID)
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreDecryptFailed)
}

func TestLockedVaultFailsClosedForEventWriteAndContentVerify(t *testing.T) {
	ass := newIntegrationEventsStore(t)
	require.NoError(t, ass.CreateSession("locked-digest", constants.SessionTypeOperator, "", ""))
	recordContentEvent(t, ass, "locked-digest", "kept", "")
	require.NoError(t, ass.VerifyChainContent(t.Context(), 0))

	ass.encryptionVault.Lock()

	// An event with no content still carries a keyed digest, so even it cannot be written.
	_, err := ass.RecordEvent(&Event{
		OperatorSessionID: "locked-digest",
		Timestamp:         time.Now().UTC(),
		Type:              constants.EventType("COMMAND_EXECUTION"),
	})
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreVaultLocked)

	// The link-only walk still runs; content verification cannot and fails closed.
	require.NoError(t, ass.VerifyChain(t.Context(), 0))
	require.ErrorIs(t, ass.VerifyChainContent(t.Context(), 0), constants.ErrLocalFirstEventsStoreVaultLocked)
}
