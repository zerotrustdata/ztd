// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// Exercise the real TLS listener, authentication middleware, and SQLite store.
// App policies and app certificates use the fixture PKI; Operator and CLI
// certificates come from device enrollment. All four identities must be able
// to read the seeded document, but none may mutate it through the data API.
func TestDataAPIReadOnlyAcrossMTLSIdentities(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "data-api-read-only",
		Posture:  constants.PostureDoctrine,
	})
	f.WaitForReady(t)
	owner := fixtures.EnrollClientIdentity(t, f, "data-api-owner", "data-api-org", "data-api-host", "data-api.local")
	first, err := f.Service.GetUserService().IsFirstUser(t.Context(), owner.UserID)
	require.NoError(t, err)
	require.True(t, first)

	clients := []struct {
		name   string
		client *http.Client
	}{
		{"app", seedUnprovenAppWorkload(t, f)},
		{"owner-delegated-app", seedUnprovenAppWorkload(t, f, owner.UserID)},
		{"outbound-operator", fixtures.CreateMTLSClient(t, f, owner)},
		{"owner-cli", fixtures.CreateCLIMTLSClient(t, f, owner)},
	}
	baseURL := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())
	collections := []constants.CollectionName{
		constants.CollectionTrustedSigners,
		constants.CollectionRevokedCertificates,
		constants.CollectionUsers,
		constants.CollectionOperators,
		constants.CollectionOperatorSessions,
		constants.CollectionBoundSessions,
		constants.CollectionPasskeyChallenges,
		constants.CollectionSettings,
		constants.CollectionAppPolicies,
		constants.CollectionConsensus,
		constants.CollectionInvestigations,
	}
	for _, identity := range clients {
		t.Run(identity.name, func(t *testing.T) {
			defer identity.client.CloseIdleConnections()
			for _, collection := range collections {
				t.Run(string(collection), func(t *testing.T) {
					const id = "read-only-regression"
					path := constants.APIPaths.DataPrefix + string(collection) + "/" + id
					for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
						t.Run(method, func(t *testing.T) {
							original := json.RawMessage(`{"value":"original"}`)
							require.NoError(t, f.Service.GetDocStore().DocSet(t.Context(), string(collection), id, original))
							before, err := f.Service.GetDocStore().DocGet(t.Context(), string(collection), id)
							require.NoError(t, err)
							require.NotNil(t, before)

							status, body := requestDataAPI(t, identity.client, http.MethodGet, baseURL+path, "")
							require.Equal(t, http.StatusOK, status, "identity must authenticate: %s", body)
							status, body = requestDataAPI(t, identity.client, method, baseURL+path, `{"value":"attacker"}`)
							assert.Equal(t, http.StatusMethodNotAllowed, status, "%s", body)
							after, err := f.Service.GetDocStore().DocGet(t.Context(), string(collection), id)
							require.NoError(t, err)
							assert.Equal(t, before, after, "rejected write must leave document and timestamps unchanged")
						})
					}
				})
			}

			t.Run("settings-singleton", func(t *testing.T) {
				for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
					t.Run(method, func(t *testing.T) {
						store := f.Service.GetDocStore()
						before, err := store.DocGet(t.Context(), string(constants.CollectionSettings), string(constants.DocIDPlatformSettings))
						require.NoError(t, err)
						status, body := requestDataAPI(t, identity.client, method, baseURL+constants.APIPaths.DataSettings, `{"value":"attacker"}`)
						assert.Equal(t, http.StatusMethodNotAllowed, status, "%s", body)
						after, err := store.DocGet(t.Context(), string(constants.CollectionSettings), string(constants.DocIDPlatformSettings))
						require.NoError(t, err)
						assert.Equal(t, before, after)
					})
				}
			})
		})
	}
}

func requestDataAPI(t *testing.T, client *http.Client, method, url, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", constants.HeaderValueApplicationJSON)
	resp, err := client.Do(req)
	require.NoError(t, err)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	return resp.StatusCode, data
}
