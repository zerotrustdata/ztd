// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	approvals "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

func newTestSuspendedStore(t *testing.T) storage.SuspendedTransactionStore {
	t.Helper()
	logger := testutil.NewTestLogger()
	db, err := openTestDB(t, newTestFileSvc(t), logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db.GetSuspendedStore()
}

func TestGatewayL3Notary_CLIVerifier_RejectsInactiveUser(t *testing.T) {
	logger := testutil.NewTestLogger()
	fileSvc := newTestFileSvc(t)
	db, err := openTestDB(t, fileSvc, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	userSvc := NewUserService(db.GetDocStore(), logger)
	cliSessionSvc := NewCLISessionService(db.GetDocStore(), logger)
	cliVerifier := NewCLISessionVerifier(db.GetDocStore(), nil, logger, userSvc, cliSessionSvc)
	notary := governance.NewGatewayL3Notary(cliVerifier, nil, logger)

	// Create a disabled user
	userID := "disabled-user"
	user := &models.User{
		ID:     userID,
		Status: constants.UserStatusDisabled,
	}
	userBytes, _ := json.Marshal(user)
	require.NoError(t, db.GetDocStore().DocSet(t.Context(), marshaler.CollectionName(constants.CollectionUsers), userID, userBytes))

	txHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cert := newCLIApprovalTestCertificate(t, userID, "some-session")
	proof, err := approvals.SignCLIApproval(txHash, &cert)
	require.NoError(t, err)
	ok, err := notary.VerifyL3Proof(context.Background(), userID, txHash, "some-session", proof)
	require.ErrorIs(t, err, constants.ErrCLIL3UserInactive)
	require.False(t, ok)
}

func TestGatewayL3Notary_CLIVerifier_RejectsUnknownSession(t *testing.T) {
	logger := testutil.NewTestLogger()
	fileSvc := newTestFileSvc(t)
	db, err := openTestDB(t, fileSvc, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	userSvc := NewUserService(db.GetDocStore(), logger)
	cliSessionSvc := NewCLISessionService(db.GetDocStore(), logger)
	cliVerifier := NewCLISessionVerifier(db.GetDocStore(), nil, logger, userSvc, cliSessionSvc)
	notary := governance.NewGatewayL3Notary(cliVerifier, nil, logger)

	// Create an active user
	userID := "active-user"
	user := &models.User{
		ID:     userID,
		Status: constants.UserStatusActive,
	}
	userBytes, _ := json.Marshal(user)
	require.NoError(t, db.GetDocStore().DocSet(t.Context(), marshaler.CollectionName(constants.CollectionUsers), userID, userBytes))

	// A valid signature must still fail if its approving session is not enrolled.
	txHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cert := newCLIApprovalTestCertificate(t, userID, "non-existent-session")
	proof, err := approvals.SignCLIApproval(txHash, &cert)
	require.NoError(t, err)
	ok, err := notary.VerifyL3Proof(context.Background(), userID, txHash, "non-existent-session", proof)
	require.ErrorIs(t, err, constants.ErrCLIL3SessionNotFound)
	require.False(t, ok)
}

func TestGatewayL3Notary_CLIVerifier_RejectsRevokedCertificate(t *testing.T) {
	logger := testutil.NewTestLogger()
	fileSvc := newTestFileSvc(t)
	db, err := openTestDB(t, fileSvc, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	sm := db.GetSecretManager()
	pki := newPKIAuthority(fileSvc, db.GetDocStore(), sm, logger)
	err = pki.InitializePKI(nil)
	require.NoError(t, err)

	userSvc := NewUserService(db.GetDocStore(), logger)
	cliSessionSvc := NewCLISessionService(db.GetDocStore(), logger)
	cliVerifier := NewCLISessionVerifier(db.GetDocStore(), pki, logger, userSvc, cliSessionSvc)
	notary := governance.NewGatewayL3Notary(cliVerifier, nil, logger)

	userID := "user-123"
	user := &models.User{
		ID:     userID,
		Status: constants.UserStatusActive,
	}
	userBytes, _ := json.Marshal(user)
	require.NoError(t, db.GetDocStore().DocSet(t.Context(), marshaler.CollectionName(constants.CollectionUsers), userID, userBytes))

	// Create a CLI session with a revoked certificate serial
	cliSessionID := "cli-session-revoked"
	txHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cert := newCLIApprovalTestCertificate(t, userID, cliSessionID)
	proof, err := approvals.SignCLIApproval(txHash, &cert)
	require.NoError(t, err)
	cliSession := models.CLISession{
		ID:                cliSessionID,
		UserID:            userID,
		OperatorSessionID: "operator-session-123",
		CertFingerprint:   proof.MtlsCertFingerprint,
		CertSerial:        cert.Leaf.SerialNumber.Text(16),
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         time.Now().UTC().Add(24 * time.Hour),
		AbsoluteExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		IdleExpiresAt:     time.Now().UTC().Add(24 * time.Hour),
		SessionType:       string(constants.SessionTypeCLI),
		IsActive:          true,
		LoginMethod:       "csr",
	}
	cliSessionBytes, _ := json.Marshal(cliSession)
	require.NoError(t, db.GetDocStore().DocSet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, cliSessionBytes))

	// Establish that the enrolled session is accepted before revocation.
	ok, err := notary.VerifyL3Proof(context.Background(), userID, txHash, cliSessionID, proof)
	require.NoError(t, err)
	require.True(t, ok)

	// Revoke the certificate
	err = pki.RevokeCertificate(t.Context(), cliSession.CertSerial, "test revocation")
	require.NoError(t, err)

	ok, err = notary.VerifyL3Proof(context.Background(), userID, txHash, cliSessionID, proof)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestGatewayL3Notary_DelegatesToCLI(t *testing.T) {
	logger := testutil.NewTestLogger()
	fileSvc := newTestFileSvc(t)
	db, err := openTestDB(t, fileSvc, logger)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	userSvc := NewUserService(db.GetDocStore(), logger)
	cliSessionSvc := NewCLISessionService(db.GetDocStore(), logger)
	cliVerifier := NewCLISessionVerifier(db.GetDocStore(), nil, logger, userSvc, cliSessionSvc)
	notary := governance.NewGatewayL3Notary(cliVerifier, nil, logger)

	// Create an active user
	userID := "active-user"
	user := &models.User{
		ID:     userID,
		Status: constants.UserStatusActive,
	}
	userBytes, _ := json.Marshal(user)
	require.NoError(t, db.GetDocStore().DocSet(t.Context(), marshaler.CollectionName(constants.CollectionUsers), userID, userBytes))

	// Create a CLI session with a known fingerprint
	cliSessionID := "cli-session-456"
	txHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cert := newCLIApprovalTestCertificate(t, userID, cliSessionID)
	proof, err := approvals.SignCLIApproval(txHash, &cert)
	require.NoError(t, err)
	cliSession := models.CLISession{
		ID:                cliSessionID,
		UserID:            userID,
		OperatorSessionID: "operator-session-456",
		CertFingerprint:   proof.MtlsCertFingerprint,
		CertSerial:        cert.Leaf.SerialNumber.Text(16),
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         time.Now().UTC().Add(24 * time.Hour),
		AbsoluteExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		IdleExpiresAt:     time.Now().UTC().Add(24 * time.Hour),
		SessionType:       string(constants.SessionTypeCLI),
		IsActive:          true,
		LoginMethod:       "csr",
	}
	cliSessionBytes, _ := json.Marshal(cliSession)
	require.NoError(t, db.GetDocStore().DocSet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, cliSessionBytes))

	// The valid proof reaches the CLI verifier, whose PKI dependency is absent.
	ok, err := notary.VerifyL3Proof(context.Background(), userID, txHash, "different-submitter-session", proof)
	require.ErrorIs(t, err, constants.ErrCLIL3PKINotConfigured)
	require.False(t, ok)
}

func TestGatewayL3Notary_DelegatesToPasskey(t *testing.T) {
	logger := testutil.NewTestLogger()
	passkeyL3, user := newPasskeyServiceForTest(t)
	notary := governance.NewGatewayL3Notary(nil, passkeyL3, logger)

	// Use the user already created by newPasskeyServiceForTest
	userID := user.ID

	// Add a dummy credential
	credID := []byte("real-credential-id")
	require.NoError(t, passkeyL3.addCredential(t.Context(), userID, testCredential("real-credential-id")))

	// Create a WebAuthn proof (no mtls_cert_fingerprint)
	txHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	clientData := `{"type":"webauthn.get","challenge":"` + base64.RawURLEncoding.EncodeToString([]byte(txHash)) + `","origin":"localhost"}`
	ok, err := notary.VerifyL3Proof(context.Background(), userID, txHash, "", &commonv1.L3Proof{
		CredentialId:      base64.RawURLEncoding.EncodeToString(credID),
		ClientDataJson:    base64.RawURLEncoding.EncodeToString([]byte(clientData)),
		AuthenticatorData: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 37))),
		Signature:         base64.RawURLEncoding.EncodeToString([]byte("signature")),
	})
	// This will fail signature verification but proves delegation to passkey verifier
	require.Error(t, err)
	require.False(t, ok)
}
