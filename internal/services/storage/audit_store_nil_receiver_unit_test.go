// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// TestNilAuditStore_WritesFailClosed pins M4: every write path on a nil store
// returns ErrAuditStoreDisabled, matching the read paths, never a silent success.
func TestNilAuditStore_WritesFailClosed(t *testing.T) {
	var ass *SQLAuditStore
	event := &Event{OperatorSessionID: "s", Type: constants.Event.Operator.Audit.Command}

	require.ErrorIs(t, ass.CreateSession("s", constants.SessionTypeOperator, "t", "u"), constants.ErrAuditStoreDisabled)
	require.ErrorIs(t, ass.StartSession("s", constants.SessionTypeOperator, "t", "u"), constants.ErrAuditStoreDisabled)
	require.ErrorIs(t, ass.EndSession("s"), constants.ErrAuditStoreDisabled)
	require.ErrorIs(t, ass.RecordEvents([]*Event{event}), constants.ErrAuditStoreDisabled)
	require.ErrorIs(t, ass.RecordEvents(nil), constants.ErrAuditStoreDisabled)
	_, err := ass.RecordEvent(event)
	require.ErrorIs(t, err, constants.ErrAuditStoreDisabled)
	_, _, _, err = ass.RecordEventChained(event)
	require.ErrorIs(t, err, constants.ErrAuditStoreDisabled)
	require.ErrorIs(t, ass.RecordEventReceipt(&models.EventReceiptRecord{}), constants.ErrAuditStoreDisabled)
	require.ErrorIs(t, ass.RecordFileMutation(&FileMutationLog{}), constants.ErrAuditStoreDisabled)
	require.ErrorIs(t, ass.DocDelete(context.Background(), "c", "id"), constants.ErrAuditStoreDisabled)
}

// TestEncryptAuditContent_LockedVaultFailsClosedForEmptyContent pins L2: a missing
// or locked vault fails regardless of content length.
func TestEncryptAuditContent_LockedVaultFailsClosedForEmptyContent(t *testing.T) {
	_, err := encryptAuditContent(nil, auditColumnEventContentText, "1", "")
	require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked)
	_, err = encryptAuditContent(nil, auditColumnEventContentText, "1", "content")
	require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked)
}
