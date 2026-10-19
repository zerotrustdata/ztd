// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package tests

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// TestCLIRefresh_ExpiredSessionRecoverable verifies the full Tier 2 refresh
// flow: enroll a CLI identity, expire the session server-side, call the
// refresh endpoint with the still-valid cert, and verify the new session
// works for a downstream mTLS call.
//
// This is the integration-test assertion of the E.3 completion criterion:
// "An expired CLI session on a running stack is recoverable without nuking
// the gateway volume."
func TestCLIRefresh_ExpiredSessionRecoverable(t *testing.T) {
	fixture := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: t.Name(),
	})
	fixture.WaitForReady(t)

	// Enroll a CLI identity with a real cert and session.
	identity := fixtures.EnrollClientIdentity(t, fixture, "user-refresh-int", "org-refresh-int", "sys-fp-int", "host-int")

	// Verify the enrolled CLI session works before expiry.
	cliClient := fixtures.CreateCLIMTLSClient(t, fixture, identity)
	mtlsURL := network.LocalhostHTTPSURL(fixture.Service.GetHTTPSPort())

	// 1. Expire the CLI session server-side by overwriting the session
	//    document with an ExpiresAt in the past. This simulates the TTL
	//    elapsing without waiting for the real 7-day TTL.
	cliSessionID := identity.CLISessionID
	certBlock, _ := pem.Decode([]byte(identity.CLICertificate))
	require.NotNil(t, certBlock)
	cliCert, err := x509.ParseCertificate(certBlock.Bytes)
	require.NoError(t, err)
	certHash := sha256.Sum256(cliCert.Raw)
	expiredSession := models.CLISession{
		ID:                cliSessionID,
		UserID:            identity.UserID,
		OperatorSessionID: identity.OperatorSessionID,
		SystemFingerprint: "sys-fp-int",
		CertFingerprint:   hex.EncodeToString(certHash[:]),
		CertSerial:        "serial-int",
		CreatedAt:         time.Now().Add(-2 * time.Hour),
		ExpiresAt:         time.Now().Add(-1 * time.Hour), // expired
		AbsoluteExpiresAt: time.Now().Add(-1 * time.Hour),
		IdleExpiresAt:     time.Now().Add(-1 * time.Hour),
		SessionType:       string(constants.SessionTypeCLI),
		IsActive:          true,
		LoginMethod:       "mTLS",
	}
	expiredBytes, err := json.Marshal(expiredSession)
	require.NoError(t, err)
	require.NoError(t, fixture.Service.GetDocStore().DocSet(t.Context(),
		marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, expiredBytes,
	))

	// 2. Verify the expired session is rejected on a non-refresh endpoint
	//    (fail-closed). Use the audit receipts endpoint as a stand-in.
	preRefreshReq, _ := http.NewRequest(http.MethodGet, mtlsURL+constants.APIPaths.AuditReceipts, nil)
	preRefreshResp, err := cliClient.Do(preRefreshReq)
	require.NoError(t, err)
	_, _ = io.ReadAll(preRefreshResp.Body)
	preRefreshResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, preRefreshResp.StatusCode,
		"expired session must be rejected on non-refresh endpoints (fail-closed)")

	// 3. Call the refresh endpoint with the still-valid cert. The auth
	//    middleware admits the expired session to the refresh controller,
	//    which extends that same session in place. Use a fresh HTTP client for the
	//    refresh call so the connection pool does not hand out a
	//    connection the gateway may have closed after the 401 above.
	refreshCliClient := fixtures.CreateCLIMTLSClient(t, fixture, identity)
	refreshBody, _ := json.Marshal(models.CLIRefreshRequest{})
	refreshReq, _ := http.NewRequest(http.MethodPost, mtlsURL+constants.APIPaths.AuthCLIRefresh, bytes.NewReader(refreshBody))
	refreshResp, err := refreshCliClient.Do(refreshReq)
	require.NoError(t, err)
	defer refreshResp.Body.Close()
	refreshRespBytes, err := io.ReadAll(refreshResp.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusCreated, refreshResp.StatusCode,
		"refresh should succeed with a valid cert and expired session, body: %s", string(refreshRespBytes))

	var refreshResult models.CLIRefreshResponse
	require.NoError(t, json.Unmarshal(refreshRespBytes, &refreshResult))
	require.True(t, refreshResult.Success)
	require.NotEmpty(t, refreshResult.CLISessionID)
	require.Equal(t, cliSessionID, refreshResult.CLISessionID,
		"refresh must extend the session the certificate names, keeping its ID")
	assert.Equal(t, identity.UserID, refreshResult.UserID)

	// 4. Verify the same session is active again, bound to the same user and
	//    certificate, with a future expiry.
	refreshedDoc, err := fixture.Service.GetDocStore().DocGet(t.Context(),
		marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID)
	require.NoError(t, err)
	require.NotNil(t, refreshedDoc)
	var refreshed models.CLISession
	refreshedBytes, err := json.Marshal(refreshedDoc.Data)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(refreshedBytes, &refreshed))
	assert.True(t, refreshed.IsActive, "the refreshed session must be active")
	assert.Equal(t, identity.UserID, refreshed.UserID)
	assert.Equal(t, expiredSession.CertFingerprint, refreshed.CertFingerprint, "refresh keeps the certificate binding")
	assert.True(t, refreshed.ExpiresAt.After(time.Now()),
		"the refreshed session must have a future expiry (TTL refreshed)")

	// 5. Verify the refreshed session works for a downstream mTLS call with the
	//    same certificate and session ID header.
	identity.CLISessionID = refreshResult.CLISessionID
	newCliClient := fixtures.CreateCLIMTLSClient(t, fixture, identity)
	postRefreshReq, _ := http.NewRequest(http.MethodGet, mtlsURL+constants.APIPaths.AuditReceipts, nil)
	postRefreshResp, err := newCliClient.Do(postRefreshReq)
	require.NoError(t, err)
	_, _ = io.ReadAll(postRefreshResp.Body)
	postRefreshResp.Body.Close()
	assert.Equal(t, http.StatusOK, postRefreshResp.StatusCode,
		"new session must authenticate with the original certificate on downstream endpoints")
}
