// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration || e2e

package fixtures

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/network"
)

// ConsoleSignIn runs the console passkey authentication ceremony for the
// identity and returns the web session cookie the Gateway sets.
func ConsoleSignIn(t *testing.T, f *GatewayFixture, client *http.Client, identity *ClientIdentity) *http.Cookie {
	t.Helper()
	baseURL := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())

	var challenge models.PasskeyChallengeResponse
	status, body := postJSON(t, client, baseURL+constants.APIPaths.AuthPasskeysConsoleAuthenticateChallenge, nil,
		models.PasskeyChallengeRequest{UserID: identity.UserID})
	require.Equal(t, http.StatusOK, status, "console authenticate challenge: %s", body)
	require.NoError(t, json.Unmarshal(body, &challenge))
	require.True(t, challenge.Success, "console authenticate challenge: %s", challenge.Error)

	options := challenge.Options.Response
	assertion := identity.Passkey.Assert(t, options.RelyingPartyID, options.Challenge)
	req := struct {
		UserID            string                            `json:"user_id"`
		AssertionResponse *models.WebAuthnAssertionResponse `json:"assertion_response"`
	}{UserID: identity.UserID, AssertionResponse: &assertion}
	resp := doJSON(t, client, http.MethodPost, baseURL+constants.APIPaths.AuthPasskeysConsoleAuthenticateVerify, nil, req)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "console authenticate verify: %s", body)
	var verified models.PasskeyAuthVerifyResponse
	require.NoError(t, json.Unmarshal(body, &verified))
	require.True(t, verified.Success, "console authenticate verify: %s", verified.Error)

	for _, cookie := range resp.Cookies() {
		if cookie.Name == constants.WebSessionCookieName {
			return cookie
		}
	}
	require.FailNow(t, "console authenticate verify set no web session cookie")
	return nil
}

// ApproveWithPasskey approves a suspended transaction the way the approval
// page does: fetch the challenge bound to txHash, sign it with the identity's
// passkey, and submit the assertion. It returns the verify status and body.
func ApproveWithPasskey(t *testing.T, f *GatewayFixture, client *http.Client, session *http.Cookie, identity *ClientIdentity, txHash string) (int, []byte) {
	t.Helper()
	approvalURL := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort()) + constants.APIPaths.ApprovalsByID + txHash

	resp := doJSON(t, client, http.MethodGet, approvalURL+"/challenge", session, nil)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "approval challenge: %s", body)
	var options protocol.CredentialAssertion
	require.NoError(t, json.Unmarshal(body, &options))

	assertion := identity.Passkey.Assert(t, options.Response.RelyingPartyID, options.Response.Challenge)
	return postJSON(t, client, approvalURL+constants.APIPaths.ApprovalsVerifyAction, session, assertion)
}

func postJSON(t *testing.T, client *http.Client, url string, session *http.Cookie, payload any) (int, []byte) {
	t.Helper()
	resp := doJSON(t, client, http.MethodPost, url, session, payload)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}

func doJSON(t *testing.T, client *http.Client, method, url string, session *http.Cookie, payload any) *http.Response {
	t.Helper()
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, reader)
	require.NoError(t, err)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if session != nil {
		req.AddCookie(session)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}
