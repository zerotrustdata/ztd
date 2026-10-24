// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestGetEventReceipt_DecodesTheCanonicalReceipt(t *testing.T) {
	t.Parallel()
	receipt := &operatorv1.EventReceipt{TransactionId: "transaction/1", TransactionHash: "hash-1", SignerKeyId: "signer-1"}
	body, err := protojson.Marshal(receipt)
	require.NoError(t, err)

	var gotURL string
	client := scriptedClient(t, http.StatusOK, string(body), "")
	client.http.Transport = scriptedRoundTrip(func(req *http.Request) (*http.Response, error) {
		gotURL = req.URL.String()
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
	})

	decoded, raw, err := client.GetEventReceipt(context.Background(), "transaction/1")
	require.NoError(t, err)
	assert.Equal(t, "https://gateway.test"+constants.APIPaths.AuditEventReceipts+"?tx_id=transaction%2F1", gotURL)
	assert.Equal(t, "transaction/1", decoded.GetTransactionId())
	assert.Equal(t, "hash-1", decoded.GetTransactionHash())
	assert.JSONEq(t, string(body), string(raw))

	missing := scriptedClient(t, http.StatusNotFound, `{}`, "")
	decoded, _, err = missing.GetEventReceipt(context.Background(), "transaction/1")
	require.NoError(t, err)
	assert.Nil(t, decoded)

	failed := scriptedClient(t, http.StatusBadGateway, `down`, "")
	_, _, err = failed.GetEventReceipt(context.Background(), "transaction/1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 502")

	broken := scriptedClient(t, http.StatusOK, `{`, "")
	_, _, err = broken.GetEventReceipt(context.Background(), "transaction/1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode canonical event receipt")
}

func TestGetTrustedSignerPublicKey_ValidatesTheGatewayAnswer(t *testing.T) {
	t.Parallel()
	publicKeyHex := strings.Repeat("ab", ed25519.PublicKeySize)
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr error
		wantKey string
	}{
		{name: "enabled signer", status: http.StatusOK, body: `{"id":"signer-1","public_key_hex":"` + publicKeyHex + `","enabled":true}`, wantKey: publicKeyHex},
		{name: "missing signer", status: http.StatusNotFound, wantErr: constants.ErrTrustedSignerKeyNotFound},
		{name: "disabled signer", status: http.StatusOK, body: `{"id":"signer-1","public_key_hex":"` + publicKeyHex + `","enabled":false}`, wantErr: constants.ErrTrustedSignerKeyNotFound},
		{name: "wrong size", status: http.StatusOK, body: `{"id":"signer-1","public_key_hex":"ab","enabled":true}`, wantErr: constants.ErrTrustedSignerKeyNotFound},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := scriptedClient(t, tt.status, tt.body, "https://gateway.test"+constants.APIPaths.GovernanceSignersByID+"signer-1")
			key, err := client.GetTrustedSignerPublicKey(context.Background(), "signer-1")
			if tt.wantKey != "" {
				require.NoError(t, err)
				assert.Equal(t, tt.wantKey, hex.EncodeToString(key))
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, key)
		})
	}
}

func TestDiscoverOperator_SelectsOneActiveDataOperator(t *testing.T) {
	t.Parallel()
	remote := func(id, session, capability string) string {
		return `{"id":"` + id + `","status":"active","operator_session_id":"` + session + `","operator_type":"remote","runtime_config":{"capabilities":["` + capability + `"]}}`
	}
	cases := []struct {
		name     string
		userID   string
		operator string
		body     string
		wantID   string
		wantSess string
		wantErr  error
		skipHTTP bool
	}{
		{
			name:     "single data operator",
			userID:   "user-1",
			body:     `{"success":true,"operators":[` + remote("op-1", "sess-1", "data") + `]}`,
			wantID:   "op-1",
			wantSess: "sess-1",
		},
		{
			name:     "pinned id disambiguates",
			userID:   "user-1",
			operator: "op-2",
			body:     `{"success":true,"operators":[` + remote("op-1", "sess-1", "data") + `,` + remote("op-2", "sess-2", "data") + `]}`,
			wantID:   "op-2",
			wantSess: "sess-2",
		},
		{
			name:    "two data operators are ambiguous",
			userID:  "user-1",
			body:    `{"success":true,"operators":[` + remote("op-1", "sess-1", "data") + `,` + remote("op-2", "sess-2", "data") + `]}`,
			wantErr: constants.ErrEvaluationTargetAmbiguous,
		},
		{
			name:    "inference-only is unavailable",
			userID:  "user-1",
			body:    `{"success":true,"operators":[` + remote("op-1", "sess-1", "inference") + `]}`,
			wantErr: constants.ErrEvaluationTargetUnavailable,
		},
		{
			name:     "user id is required before the request",
			skipHTTP: true,
			wantErr:  constants.ErrMissingRequiredField,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, err := New(config.Config{MTLSBaseURL: "https://gateway.test", UserID: tt.userID, OperatorID: tt.operator})
			require.NoError(t, err)
			calls := 0
			client.http.Transport = scriptedRoundTrip(func(req *http.Request) (*http.Response, error) {
				calls++
				assert.Equal(t, "user-1", req.URL.Query().Get("user_id"))
				assert.Equal(t, constants.APIPaths.Operators, req.URL.Path)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.body)), Request: req}, nil
			})
			id, session, err := client.DiscoverOperator(context.Background())
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				if tt.skipHTTP {
					assert.Zero(t, calls)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantID, id)
			assert.Equal(t, tt.wantSess, session)
			assert.Equal(t, 1, calls)
		})
	}
}

func TestConsensusBinding_ReadsTheOwnerBinding(t *testing.T) {
	t.Parallel()
	client := scriptedClient(t, http.StatusOK, `{"id":"tribunal","provider":"tribunal","quorum":2,"enabled":true,"member_app_ids":["app-1"]}`, "https://gateway.test"+constants.APIPaths.AuthPlatformEnrollmentConsensus)
	binding, err := client.ConsensusBinding(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tribunal", binding.ID)
	assert.Equal(t, 2, binding.Quorum)
	assert.Equal(t, []string{"app-1"}, binding.MemberAppIDs)

	rejected := scriptedClient(t, http.StatusForbidden, `{}`, "")
	_, err = rejected.ConsensusBinding(context.Background())
	require.ErrorIs(t, err, constants.ErrHTTPStatusError)

	broken := scriptedClient(t, http.StatusOK, `{`, "")
	_, err = broken.ConsensusBinding(context.Background())
	require.ErrorIs(t, err, constants.ErrInvalidJSONResponse)
}

func TestDo_RecordsTheExchangeAndKeepsTheCLISessionOffTheOperatorIdentity(t *testing.T) {
	t.Parallel()
	publicKeyHex := strings.Repeat("ab", ed25519.PublicKeySize)
	client, err := New(config.Config{
		MTLSBaseURL:  "https://gateway.test",
		UserID:       "user-1",
		CLISessionID: "cli-session",
	})
	require.NoError(t, err)
	var recorded []Exchange
	client.Record(&recorded)
	client.http.Transport = scriptedRoundTrip(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "user-1", req.Header.Get(constants.HeaderUserID))
		assert.Empty(t, req.Header.Get(constants.HeaderCLISessionID))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"id":"signer-1","public_key_hex":"` + publicKeyHex + `","enabled":true}`)),
			Request:    req,
		}, nil
	})

	_, err = client.GetTrustedSignerPublicKey(context.Background(), "signer-1")
	require.NoError(t, err)
	require.Len(t, recorded, 1)
	assert.Equal(t, http.StatusOK, recorded[0].Status)
	assert.Equal(t, "agent-harness-auditor", recorded[0].Persona)
	assert.Empty(t, recorded[0].Err)

	client.http.Transport = scriptedRoundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial refused")
	})
	_, err = client.GetTrustedSignerPublicKey(context.Background(), "signer-1")
	require.Error(t, err)
	require.Len(t, recorded, 2)
	assert.Contains(t, recorded[1].Err, "dial refused")
	assert.Zero(t, recorded[1].Status)
}
