// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func newCLISessionBindingTestService(t *testing.T) *CLISessionService {
	t.Helper()
	return NewCLISessionService(newTestDocumentStoreService(t), testutil.NewTestLogger())
}

// persistActiveCLISessionForBinding persists an active CLI session with
// certificate grounding, the way enrollment does.
func persistActiveCLISessionForBinding(t *testing.T, svc *CLISessionService, cliSessionID, userID, operatorSessionID string) {
	t.Helper()
	require.NoError(t, svc.PersistCLISession(t.Context(), cliSessionID, operatorSessionID, userID, "sys-fp", "cert-fp", "cert-serial", "bootstrap"))
}

func TestCLISessionBinding_BindUpdatesActiveSessionInPlace(t *testing.T) {
	svc := newCLISessionBindingTestService(t)
	persistActiveCLISessionForBinding(t, svc, "binding-session-1", "user-1", "op-session-a")
	before, err := svc.loadCLISession(t.Context(), "binding-session-1")
	require.NoError(t, err)

	updated, err := svc.BindOperatorSessions(t.Context(), "binding-session-1", "op-session-b", []string{"op-session-b", "op-session-a"})
	require.NoError(t, err)
	assert.Equal(t, "binding-session-1", updated.ID)
	assert.Equal(t, "op-session-b", updated.OperatorSessionID)
	assert.Equal(t, []string{"op-session-b", "op-session-a"}, updated.BoundOperatorSessionIDs)

	persisted, err := svc.loadCLISession(t.Context(), "binding-session-1")
	require.NoError(t, err)
	assert.True(t, persisted.IsActive)
	assert.Equal(t, "op-session-b", persisted.OperatorSessionID)
	assert.Equal(t, []string{"op-session-b", "op-session-a"}, persisted.BoundOperatorSessionIDs)
	assert.Equal(t, before.CertFingerprint, persisted.CertFingerprint, "the certificate grounding is kept")
	assert.Equal(t, before.CertSerial, persisted.CertSerial)
	assert.Equal(t, before.LoginMethod, persisted.LoginMethod)
	assert.True(t, before.ExpiresAt.Equal(persisted.ExpiresAt), "a bind does not move the session expiry")

	sessions, err := svc.ListUserCLISessions(t.Context(), "user-1")
	require.NoError(t, err)
	assert.Len(t, sessions, 1, "a bind must not mint a session")
}

func TestCLISessionBinding_BindRejectsMissingSessionWithoutMinting(t *testing.T) {
	svc := newCLISessionBindingTestService(t)

	_, err := svc.BindOperatorSessions(t.Context(), "binding-session-missing", "op-session-a", []string{"op-session-a"})
	require.ErrorIs(t, err, constants.ErrCLISessionNotFound)

	doc, err := svc.db.DocGet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), "binding-session-missing")
	require.NoError(t, err)
	assert.Nil(t, doc, "a bind against a missing session must not create one")
}

func TestCLISessionBinding_BindRejectsInactiveSessionKeepsBinding(t *testing.T) {
	svc := newCLISessionBindingTestService(t)
	persistActiveCLISessionForBinding(t, svc, "binding-session-inactive", "user-1", "op-session-a")
	require.NoError(t, svc.DeactivateCLISession(t.Context(), "binding-session-inactive"))

	_, err := svc.BindOperatorSessions(t.Context(), "binding-session-inactive", "op-session-b", []string{"op-session-b"})
	require.ErrorIs(t, err, constants.ErrCLISessionAlreadyDeactivated)

	persisted, err := svc.loadCLISession(t.Context(), "binding-session-inactive")
	require.NoError(t, err)
	assert.False(t, persisted.IsActive)
	assert.Equal(t, "op-session-a", persisted.OperatorSessionID, "a rejected bind must not rebind an inactive session")
}

func TestCLISessionBinding_BindRejectsInvalidTargets(t *testing.T) {
	svc := newCLISessionBindingTestService(t)
	persistActiveCLISessionForBinding(t, svc, "binding-session-invalid", "user-1", "op-session-a")

	cases := []struct {
		name      string
		sessionID string
		primary   string
		bound     []string
	}{
		{name: "empty session ID", sessionID: "", primary: "op-session-b", bound: []string{"op-session-b"}},
		{name: "empty primary", sessionID: "binding-session-invalid", primary: "", bound: []string{"op-session-b"}},
		{name: "empty bound list", sessionID: "binding-session-invalid", primary: "op-session-b", bound: nil},
		{name: "primary not first", sessionID: "binding-session-invalid", primary: "op-session-b", bound: []string{"op-session-a", "op-session-b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.BindOperatorSessions(t.Context(), tc.sessionID, tc.primary, tc.bound)
			require.ErrorIs(t, err, constants.ErrCLISessionInvalid)
		})
	}

	persisted, err := svc.loadCLISession(t.Context(), "binding-session-invalid")
	require.NoError(t, err)
	assert.Equal(t, "op-session-a", persisted.OperatorSessionID, "rejected binds must leave the session untouched")
	assert.Empty(t, persisted.BoundOperatorSessionIDs)
}

func TestCLISessionBinding_UnbindClearsBindingInPlace(t *testing.T) {
	svc := newCLISessionBindingTestService(t)
	persistActiveCLISessionForBinding(t, svc, "binding-session-unbind", "user-1", "op-session-a")
	_, err := svc.BindOperatorSessions(t.Context(), "binding-session-unbind", "op-session-a", []string{"op-session-a", "op-session-b"})
	require.NoError(t, err)

	cleared, err := svc.UnbindOperatorSessions(t.Context(), "binding-session-unbind")
	require.NoError(t, err)
	assert.Equal(t, "binding-session-unbind", cleared.ID)
	assert.Empty(t, cleared.OperatorSessionID)
	assert.Empty(t, cleared.BoundOperatorSessionIDs)

	persisted, err := svc.loadCLISession(t.Context(), "binding-session-unbind")
	require.NoError(t, err)
	assert.True(t, persisted.IsActive, "an unbound session stays active")
	assert.Empty(t, persisted.OperatorSessionID)
	assert.Empty(t, persisted.BoundOperatorSessionIDs)
	assert.Equal(t, "cert-fp", persisted.CertFingerprint, "the certificate grounding is kept")

	sessions, err := svc.ListUserCLISessions(t.Context(), "user-1")
	require.NoError(t, err)
	assert.Len(t, sessions, 1, "an unbind must not mint a session")
}

func TestCLISessionBinding_UnbindRejectsMissingAndInactiveSessions(t *testing.T) {
	svc := newCLISessionBindingTestService(t)

	_, err := svc.UnbindOperatorSessions(t.Context(), "binding-session-unbind-missing")
	require.ErrorIs(t, err, constants.ErrCLISessionNotFound)

	persistActiveCLISessionForBinding(t, svc, "binding-session-unbind-inactive", "user-1", "op-session-a")
	require.NoError(t, svc.DeactivateCLISession(t.Context(), "binding-session-unbind-inactive"))
	_, err = svc.UnbindOperatorSessions(t.Context(), "binding-session-unbind-inactive")
	require.ErrorIs(t, err, constants.ErrCLISessionAlreadyDeactivated)

	persisted, err := svc.loadCLISession(t.Context(), "binding-session-unbind-inactive")
	require.NoError(t, err)
	assert.Equal(t, "op-session-a", persisted.OperatorSessionID, "a rejected unbind must not clear an inactive session")
}
