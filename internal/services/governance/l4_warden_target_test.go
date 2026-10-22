// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/gateway/embedded"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestL4Warden_RejectsForeignExecutionTarget(t *testing.T) {
	warden, key := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	env := signedEnvelope(t, constants.ActionTypeFsRead, typedPayload(t, constants.ActionTypeFsRead), key, constants.PostureDoctrine)
	env.OperatorId = "foreign-outbound-operator"
	rehash(t, env)
	_, err := warden.VerifyEnvelope(context.Background(), env)
	require.ErrorIs(t, err, constants.ErrTxTargetOperatorMismatch)
}

func TestL4Warden_RejectsMcpCallTargetOtherThanItsTool(t *testing.T) {
	warden, key := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	for _, target := range []string{constants.MCPToolAuditReceiptList, ""} {
		env := signedEnvelope(t, constants.ActionTypeMcpCall, typedPayload(t, constants.ActionTypeMcpCall), key, constants.PostureDoctrine)
		env.TargetResource = target
		rehash(t, env)
		_, err := warden.VerifyEnvelope(context.Background(), env)
		require.ErrorIs(t, err, constants.ErrTxMcpTargetMismatch, target)
	}
}

func TestL4Warden_ExecutionTarget(t *testing.T) {
	outbound := &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "operator-session-1"}
	gateway := embedded.New(nil, nil)
	cases := []struct {
		name       string
		target     ExecutionTarget
		operatorID string
		action     constants.ActionType
		wantError  bool
	}{
		{"outbound own Operator", outbound, "operator-1", constants.ActionTypeFsRead, false},
		{"outbound foreign Operator", outbound, "operator-2", constants.ActionTypeFsRead, true},
		{"gateway outbound Operator", gateway, "operator-1", constants.ActionTypeFsRead, true},
		{"gateway embedded Operator", gateway, string(constants.DocIDEmbeddedOperator), constants.ActionTypeFsRead, false},
		{"gateway enrollment bootstrap", gateway, string(constants.DocIDEmbeddedOperator), constants.ActionTypePlatformEnrollmentCreate, false},
		{"unbound enrollment rejected", gateway, "", constants.ActionTypePlatformEnrollmentCreate, true},
		{"missing authority", nil, "operator-1", constants.ActionTypeFsRead, true},
		{"unbound host read", gateway, "", constants.ActionTypeFsRead, true},
		{"unbound app update", gateway, "", constants.ActionTypeDocumentUpdate, false},
		{"unbound app delete", gateway, "", constants.ActionTypeDocumentDelete, false},
		{"unbound app update without target dependency", nil, "", constants.ActionTypeDocumentUpdate, true},
		{"bound document foreign Operator", gateway, "operator-1", constants.ActionTypeDocumentUpdate, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replay := testutil.NewStatefulMockReplayStore()
			warden, key := createStrictVerifier(t, replay, testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
			warden.executionTarget = tc.target
			env := signedEnvelope(t, tc.action, typedPayload(t, tc.action), key, constants.PostureDoctrine)
			env.OperatorId = tc.operatorID
			env.OperatorSessionId = "" // this table covers operator targeting only
			rehash(t, env)
			_, err := warden.VerifyEnvelope(context.Background(), env)
			if tc.wantError {
				require.ErrorIs(t, err, constants.ErrTxTargetOperatorMismatch)
				require.NotContains(t, replay.Nonces, env.Nonce, "rejection must release the reserved nonce")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestL4Warden_TargetSession proves a claimed operator_session_id is verified
// against the session the runtime owns, not trusted because it is hashed.
func TestL4Warden_TargetSession(t *testing.T) {
	cases := []struct {
		name       string
		target     ExecutionTarget
		operatorID string
		sessionID  string
		wantError  bool
	}{
		{"own session", &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "own-session"}, "operator-1", "own-session", false},
		{"no session claimed", &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "own-session"}, "operator-1", "", false},
		{"foreign session", &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "own-session"}, "operator-1", "cli-session-of-someone-else", true},
		{"runtime without a session", &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}}, "operator-1", "any-session", true},
		{"unclaimed embedded Operator", embedded.New(emptyDocStore{}, nil), string(constants.DocIDEmbeddedOperator), "any-session", true},
		{"session without operator", &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "own-session"}, "", "own-session", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replay := testutil.NewStatefulMockReplayStore()
			warden, key := createStrictVerifier(t, replay, testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
			warden.executionTarget = tc.target
			action := constants.ActionTypeDocumentUpdate
			if tc.operatorID != "" {
				action = constants.ActionTypeFsRead
			}
			env := signedEnvelope(t, action, typedPayload(t, action), key, constants.PostureDoctrine)
			env.OperatorId = tc.operatorID
			env.OperatorSessionId = tc.sessionID
			rehash(t, env)
			_, err := warden.VerifyEnvelope(context.Background(), env)
			if tc.wantError {
				require.ErrorIs(t, err, constants.ErrTxTargetSessionMismatch)
				require.NotContains(t, replay.Nonces, env.Nonce, "rejection must release the reserved nonce")
				return
			}
			require.NoError(t, err)
		})
	}
}

// emptyDocStore holds no documents, so an embedded Operator over it is unclaimed.
type emptyDocStore struct{}

func (emptyDocStore) DocGet(context.Context, string, string) (*models.Document, error) {
	return nil, nil
}

func (emptyDocStore) DocSet(context.Context, string, string, json.RawMessage) error {
	return nil
}

func (emptyDocStore) DocUpdate(context.Context, string, string, json.RawMessage) (*models.Document, error) {
	return nil, constants.ErrNotFound
}
