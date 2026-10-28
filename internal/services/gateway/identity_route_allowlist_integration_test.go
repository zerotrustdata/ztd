// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// This matrix is the Gateway HTTP boundary inventory for enrolled app and
// outbound Operator certificates. Allowed requests must pass identity routing;
// every other mTLS route is denied before its controller can run.
func TestMTLSIdentityRouteAllowlistInventory(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{TestName: "identity-route-allowlist"})
	f.WaitForReady(t)
	owner := fixtures.BootstrapCLIIdentity(t, f, "identity-route-owner")
	cliClient := fixtures.CreateCLIMTLSClient(t, f, owner)
	t.Cleanup(cliClient.CloseIdleConnections)
	appClient := enrollDelegatedApp(t, f, owner, "route-inventory-app")
	operator := fixtures.EnrollClientIdentity(t, f, "route-inventory-operator-user", "route-inventory-org", "route-inventory-fingerprint", "route-inventory-host")
	operatorClient := fixtures.CreateMTLSClient(t, f, operator)
	t.Cleanup(operatorClient.CloseIdleConnections)
	baseURL := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())

	tests := []struct {
		name   string
		client *http.Client
		method string
		path   string
		allow  bool
	}{
		{name: "app MCP ingress", client: appClient, method: http.MethodPost, path: constants.APIPaths.MCPEndpoint, allow: true},
		{name: "app A2A ingress", client: appClient, method: http.MethodPost, path: constants.APIPaths.A2ACall, allow: true},
		{name: "app governed envelope ingress", client: appClient, method: http.MethodPost, path: constants.APIPaths.GovernanceEnvelopes, allow: true},
		{name: "app L2 governance deliberation", client: appClient, method: http.MethodPost, path: constants.APIPaths.GovernanceDeliberations, allow: true},
		{name: "app Tribunal intent", client: appClient, method: http.MethodPost, path: constants.APIPaths.OperatorsIntents, allow: true},
		{name: "app observe agent push", client: appClient, method: http.MethodPost, path: constants.APIPaths.ObserveProducerAgentState, allow: true},
		{name: "app observe run push", client: appClient, method: http.MethodPost, path: constants.APIPaths.ObserveProducerRunState, allow: true},
		{name: "app observe stop push", client: appClient, method: http.MethodPost, path: constants.APIPaths.ObserveProducerStop, allow: true},
		{name: "app platform settings read", client: appClient, method: http.MethodGet, path: constants.APIPaths.DataSettings, allow: true},
		{name: "app document read", client: appClient, method: http.MethodGet, path: constants.APIPaths.DataPrefix + "app_policies/route-inventory-app", allow: true},
		{name: "app read-only document query", client: appClient, method: http.MethodPost, path: constants.APIPaths.DataPrefix + "app_policies/_query", allow: true},
		{name: "app provider observation read", client: appClient, method: http.MethodGet, path: constants.APIPaths.InferenceProviderObservations + "attempt-1", allow: true},
		{name: "app model provenance read", client: appClient, method: http.MethodGet, path: constants.APIPaths.InferenceModelProvenanceAttestations + "attempt-1", allow: true},
		{name: "app campaign publication state read", client: appClient, method: http.MethodGet, path: constants.EvalCampaignPublicationStatePath("run-1"), allow: true},
		{name: "app event receipt read", client: appClient, method: http.MethodGet, path: constants.APIPaths.AuditEventReceipts, allow: true},
		{name: "app trusted signer key read", client: appClient, method: http.MethodGet, path: constants.APIPaths.GovernanceSignersByID + "route-inventory-app", allow: true},
		{name: "app signer registration", client: appClient, method: http.MethodPost, path: constants.APIPaths.GovernanceSigners},
		{name: "app operator inventory read", client: appClient, method: http.MethodGet, path: constants.APIPaths.Operators},
		{name: "app signer revocation", client: appClient, method: http.MethodDelete, path: constants.APIPaths.GovernanceSignersByID + "route-inventory-app"},
		{name: "app settings write", client: appClient, method: http.MethodPut, path: constants.APIPaths.DataSettings},
		{name: "app trust-store document write", client: appClient, method: http.MethodPut, path: constants.APIPaths.DataPrefix + "trusted_signers/route-inventory-app"},
		{name: "app certificate revocation", client: appClient, method: http.MethodPost, path: constants.APIPaths.PKICertificatesRevoke},
		{name: "app reverse method on read route", client: appClient, method: http.MethodDelete, path: constants.APIPaths.DataSettings},
		{name: "app operator target mutation", client: appClient, method: http.MethodPost, path: constants.APIPaths.OperatorsTarget},
		{name: "app Gateway reputation signing", client: appClient, method: http.MethodPost, path: constants.APIPaths.GatewayReputationSign, allow: true},
		{name: "operator session validation", client: operatorClient, method: http.MethodPost, path: constants.APIPaths.OperatorsValidate, allow: true},
		{name: "operator target update", client: operatorClient, method: http.MethodPost, path: constants.APIPaths.OperatorsTarget, allow: true},
		{name: "operator pubsub renewal and command transport", client: operatorClient, method: http.MethodGet, path: constants.APIPaths.PubSubStream, allow: true},
		{name: "operator cannot read another session", client: operatorClient, method: http.MethodGet, path: constants.APIPaths.OperatorsSession + "other-session"},
		{name: "operator audit receipt read", client: operatorClient, method: http.MethodGet, path: constants.APIPaths.AuditEventReceipts},
		{name: "operator app signer registration", client: operatorClient, method: http.MethodPost, path: constants.APIPaths.GovernanceSigners},
		{name: "operator signer revocation", client: operatorClient, method: http.MethodDelete, path: constants.APIPaths.GovernanceSignersByID + "route-inventory-app"},
		{name: "operator settings write", client: operatorClient, method: http.MethodPut, path: constants.APIPaths.DataSettings},
		{name: "operator trust-store document write", client: operatorClient, method: http.MethodPut, path: constants.APIPaths.DataPrefix + "trusted_signers/route-inventory-app"},
		{name: "operator certificate revocation", client: operatorClient, method: http.MethodPost, path: constants.APIPaths.PKICertificatesRevoke},
		{name: "operator Gateway reputation signing", client: operatorClient, method: http.MethodPost, path: constants.APIPaths.GatewayReputationSign},
		{name: "CLI identity remains on human authorization path", client: cliClient, method: http.MethodPost, path: constants.APIPaths.GovernanceSigners, allow: true},
	}
	// Other security-sensitive RouteAuthMTLS endpoints are listed here so new
	// identity classes cannot inherit access from the generic mTLS fallback.
	deniedRouteInventory := []struct {
		name   string
		method string
		path   string
	}{
		{name: "operator command dispatch", method: http.MethodPost, path: constants.APIPaths.OperatorsCommands},
		{name: "operator stop", method: http.MethodPost, path: constants.APIPaths.OperatorsStop},
		{name: "PKI CSR signing", method: http.MethodPost, path: constants.APIPaths.PKICSRSign},
		{name: "CLI enrollment token generation", method: http.MethodPost, path: constants.APIPaths.AuthEnrollmentTokenGenerate},
		{name: "CLI rotation", method: http.MethodPost, path: constants.APIPaths.AuthCLIRotate},
		{name: "CLI refresh", method: http.MethodPost, path: constants.APIPaths.AuthCLIRefresh},
		{name: "CLI bind", method: http.MethodPost, path: constants.APIPaths.AuthCLIBind},
		{name: "CLI unbind", method: http.MethodPost, path: constants.APIPaths.AuthCLIUnbind},
		{name: "CLI logout", method: http.MethodPost, path: constants.APIPaths.AuthCLILogout},
		{name: "CLI session read", method: http.MethodGet, path: constants.APIPaths.AuthCLISession},
		{name: "CLI passkey status", method: http.MethodGet, path: constants.APIPaths.AuthPasskeysCLIStatus},
		{name: "CLI approval list", method: http.MethodGet, path: constants.APIPaths.ApprovalsCLIList},
		{name: "audit receipt export read", method: http.MethodGet, path: constants.APIPaths.AuditEventReceiptsExport},
		{name: "pubsub publishing", method: http.MethodPost, path: constants.APIPaths.PubSubPublish},
		{name: "SSE event publish", method: http.MethodPost, path: constants.APIPaths.SSEEvents},
		{name: "public feed proof publish", method: http.MethodPost, path: constants.APIPaths.PublicFeedProofsPush},
		{name: "public feed proof prune", method: http.MethodPost, path: constants.APIPaths.PublicFeedProofsPrune},
		{name: "campaign publication state write", method: http.MethodPut, path: constants.EvalCampaignPublicationStatePath("run-1")},
	}
	for _, identity := range []struct {
		name   string
		client *http.Client
	}{{name: "app", client: appClient}, {name: "operator", client: operatorClient}} {
		for _, route := range deniedRouteInventory {
			tests = append(tests, struct {
				name   string
				client *http.Client
				method string
				path   string
				allow  bool
			}{name: identity.name + " " + route.name, client: identity.client, method: route.method, path: route.path})
		}
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), tc.method, baseURL+tc.path, strings.NewReader("{}"))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			response, err := tc.client.Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			if tc.allow {
				require.NotContains(t, string(body), constants.ErrPrivilegedEndpointAccess.Error(), "allowed identity route %s %s was denied", tc.method, tc.path)
				return
			}
			require.Equal(t, http.StatusForbidden, response.StatusCode, "identity route must be denied before handler: %s %s", tc.method, tc.path)
			require.Contains(t, string(body), constants.ErrPrivilegedEndpointAccess.Error(), "identity route denial must come from the identity allowlist")
		})
	}
}
