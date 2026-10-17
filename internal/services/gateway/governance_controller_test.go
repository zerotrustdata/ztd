// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// newTestGovernanceController creates a GovernanceController with minimal
// dependencies for unit-testing the controller.
func newTestGovernanceController(t *testing.T) *GovernanceController {
	t.Helper()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	responder := response.NewWriter(logger)
	return newGovernanceController(GovernanceControllerDeps{Cfg: cfg, Logger: logger, Responder: responder})
}

func TestGovernanceEnvelope_NotConfigured_Returns503_Unit(t *testing.T) {
	c := newTestGovernanceController(t)

	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()

	c.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), constants.ErrEnvelopeProcessorNotInit.Error())
}

// identityBindingRequest builds an httptest.Request carrying an mTLS peer
// certificate with the supplied SPIFFE URI SANs. The request body is unused
// by verifyEnvelopeIdentityBinding (it reads envelopeBody separately).
func identityBindingRequest(t *testing.T, spiffeIDs ...string) *http.Request {
	t.Helper()
	uris := make([]*url.URL, 0, len(spiffeIDs))
	for _, s := range spiffeIDs {
		u, err := url.Parse(s)
		require.NoError(t, err)
		uris = append(uris, u)
	}
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{URIs: uris}},
	}
	return req
}

// mutationEnvelopeBytes builds a wire GovernanceEnvelope with the given action
// type and identity claims, encoded in canonical protojson (the wire form
// verifyEnvelopeIdentityBinding decodes).
func mutationEnvelopeBytes(t *testing.T, actionType constants.ActionType, operatorID, operatorSessionID string, _ commonv1.Component) []byte {
	t.Helper()
	b, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
		ActionType:        string(actionType),
		OperatorId:        operatorID,
		OperatorSessionId: operatorSessionID,
		SourceComponent:   commonv1.Component_COMPONENT_ZTDO,
	})
	require.NoError(t, err)
	return b
}

// TestVerifyEnvelopeIdentityBinding_DocumentUpdateEmptyIdentity_FailsClosed
// asserts that a DOCUMENT_UPDATE mutation envelope with empty operator_id and
// operator_session_id is rejected at the transport boundary with
// ErrIdentityBindingFailed rather than passing through to the downstream
// processor (the previous fail-open behavior).
func TestVerifyEnvelopeIdentityBinding_DocumentUpdateEmptyIdentity_FailsClosed(t *testing.T) {
	req := identityBindingRequest(t, "spiffe://ztd.local/operator/org-1/op-1/sess-1")
	env := mutationEnvelopeBytes(t, constants.ActionTypeDocumentUpdate, "", "", commonv1.Component_COMPONENT_ZTDO)
	err := verifyEnvelopeIdentityBinding(req, env)
	require.Error(t, err)
	require.True(t, errors.Is(err, constants.ErrIdentityBindingFailed), "expected ErrIdentityBindingFailed, got %v", err)
}

// TestVerifyEnvelopeIdentityBinding_DocumentDeleteEmptyIdentity_FailsClosed
// asserts the same fail-closed guarantee for DOCUMENT_DELETE.
func TestVerifyEnvelopeIdentityBinding_DocumentDeleteEmptyIdentity_FailsClosed(t *testing.T) {
	req := identityBindingRequest(t, "spiffe://ztd.local/operator/org-1/op-1/sess-1")
	env := mutationEnvelopeBytes(t, constants.ActionTypeDocumentDelete, "", "", commonv1.Component_COMPONENT_ZTDO)
	err := verifyEnvelopeIdentityBinding(req, env)
	require.Error(t, err)
	require.True(t, errors.Is(err, constants.ErrIdentityBindingFailed), "expected ErrIdentityBindingFailed, got %v", err)
}

// TestVerifyEnvelopeIdentityBinding_FileEditEmptyIdentity_FailsClosed asserts
// the same fail-closed guarantee for FILE_EDIT.
func TestVerifyEnvelopeIdentityBinding_FileEditEmptyIdentity_FailsClosed(t *testing.T) {
	req := identityBindingRequest(t, "spiffe://ztd.local/operator/org-1/op-1/sess-1")
	env := mutationEnvelopeBytes(t, constants.ActionTypeFileEdit, "", "", commonv1.Component_COMPONENT_ZTDO)
	err := verifyEnvelopeIdentityBinding(req, env)
	require.Error(t, err)
	require.True(t, errors.Is(err, constants.ErrIdentityBindingFailed), "expected ErrIdentityBindingFailed, got %v", err)
}

// appMutationEnvelopeBytes builds a DOCUMENT_UPDATE envelope with no operator
// claims, acting as the given app from the given source component.
func appMutationEnvelopeBytes(t *testing.T, actingAppID string, source commonv1.Component) []byte {
	t.Helper()
	b, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
		ActionType:      string(constants.ActionTypeDocumentUpdate),
		ActingAppId:     actingAppID,
		SourceComponent: source,
	})
	require.NoError(t, err)
	return b
}

// TestVerifyEnvelopeIdentityBinding_AppMutationEmptyOperatorFields covers the
// app-only write path (an app persisting platform records for a browser session,
// where no Operator is in the loop): admitted only when acting_app_id matches
// the mTLS app SPIFFE ID from an AGENT/CLIENT source.
func TestVerifyEnvelopeIdentityBinding_AppMutationEmptyOperatorFields(t *testing.T) {
	tests := []struct {
		name    string
		spiffe  string
		env     []byte
		allowed bool
	}{
		{"app match agent", "spiffe://ztd.local/app/test-app", appMutationEnvelopeBytes(t, "test-app", commonv1.Component_COMPONENT_AGENT), true},
		{"app match client", "spiffe://ztd.local/app/test-app", appMutationEnvelopeBytes(t, "test-app", commonv1.Component_COMPONENT_CLIENT), true},
		{"app mismatch", "spiffe://ztd.local/app/test-app", appMutationEnvelopeBytes(t, "other-app", commonv1.Component_COMPONENT_AGENT), false},
		{"no acting app", "spiffe://ztd.local/app/test-app", appMutationEnvelopeBytes(t, "", commonv1.Component_COMPONENT_AGENT), false},
		{"non-app source", "spiffe://ztd.local/app/test-app", appMutationEnvelopeBytes(t, "test-app", commonv1.Component_COMPONENT_ZTDO), false},
		{"operator cert for app claim", "spiffe://ztd.local/operator/org-1/op-1/sess-1", appMutationEnvelopeBytes(t, "test-app", commonv1.Component_COMPONENT_AGENT), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyEnvelopeIdentityBinding(identityBindingRequest(t, tc.spiffe), tc.env)
			if tc.allowed {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, constants.ErrIdentityBindingFailed)
		})
	}
}

// TestVerifyEnvelopeIdentityBinding_AppBindingAdmitsOnlyDocumentMutations
// pins the scope of the app-only binding (INV-AUTH-ID-05). The Gateway-mode
// command service also registers EXECUTE_BASH, FILE_EDIT, RESTORE_FILE, and
// other Operator actions, so an app certificate with no Operator claim must
// reach only the platform-record writes it exists for: DOCUMENT_UPDATE and
// DOCUMENT_DELETE. Every other mutation still needs an Operator binding.
func TestVerifyEnvelopeIdentityBinding_AppBindingAdmitsOnlyDocumentMutations(t *testing.T) {
	appEnvelope := func(actionType constants.ActionType) []byte {
		b, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
			ActionType:      string(actionType),
			ActingAppId:     "test-app",
			SourceComponent: commonv1.Component_COMPONENT_AGENT,
		})
		require.NoError(t, err)
		return b
	}
	for _, actionType := range constants.AllActionTypes {
		if !actionType.IsMutation() {
			continue
		}
		t.Run(string(actionType), func(t *testing.T) {
			err := verifyEnvelopeIdentityBinding(identityBindingRequest(t, "spiffe://ztd.local/app/test-app"), appEnvelope(actionType))
			switch actionType {
			case constants.ActionTypeDocumentUpdate, constants.ActionTypeDocumentDelete:
				require.NoError(t, err)
			default:
				require.ErrorIs(t, err, constants.ErrIdentityBindingFailed)
			}
		})
	}
}

// TestVerifyEnvelopeIdentityBinding_MutationWithMatchingOperatorCert_Admitted
// is the positive counterpart: a DOCUMENT_UPDATE envelope carrying both
// operator_id and operator_session_id, presented via a matching operator
// SPIFFE cert, is admitted (returns nil).
func TestVerifyEnvelopeIdentityBinding_MutationWithMatchingOperatorCert_Admitted(t *testing.T) {
	req := identityBindingRequest(t, "spiffe://ztd.local/operator/org-1/op-1/sess-1")
	env := mutationEnvelopeBytes(t, constants.ActionTypeDocumentUpdate, "op-1", "sess-1", commonv1.Component_COMPONENT_ZTDO)
	err := verifyEnvelopeIdentityBinding(req, env)
	require.NoError(t, err)
}

func TestVerifyEnvelopeIdentityBinding_ReplacementCLISessionUsesAuthenticatedContext(t *testing.T) {
	req := identityBindingRequest(t, "spiffe://ztd.local/cli/user-1/predecessor-cli-session")
	ctx := context.WithValue(req.Context(), constants.ContextKeyCLISessionID, "replacement-cli-session")
	ctx = context.WithValue(ctx, constants.ContextKeyOperatorID, "op-1")
	ctx = context.WithValue(ctx, constants.ContextKeyOperatorSessionID, "sess-1")
	req = req.WithContext(ctx)

	envelope, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
		ActionType:        string(constants.ActionTypeDocumentUpdate),
		CliSessionId:      "replacement-cli-session",
		OperatorId:        "op-1",
		OperatorSessionId: "sess-1",
		SourceComponent:   commonv1.Component_COMPONENT_AGENT,
	})
	require.NoError(t, err)
	require.NoError(t, verifyEnvelopeIdentityBinding(req, envelope))

	// The same authenticated session cannot claim an acting app its CLI
	// certificate does not prove.
	withApp, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
		ActionType:        string(constants.ActionTypeDocumentUpdate),
		CliSessionId:      "replacement-cli-session",
		OperatorId:        "op-1",
		OperatorSessionId: "sess-1",
		ActingAppId:       "test-app",
		SourceComponent:   commonv1.Component_COMPONENT_AGENT,
	})
	require.NoError(t, err)
	require.ErrorIs(t, verifyEnvelopeIdentityBinding(req, withApp), constants.ErrIdentityBindingFailed)
}

func TestVerifyEnvelopeIdentityBinding_AuthenticatedContextRejectsClaimMismatch(t *testing.T) {
	req := identityBindingRequest(t, "spiffe://ztd.local/cli/user-1/predecessor-cli-session")
	ctx := context.WithValue(req.Context(), constants.ContextKeyCLISessionID, "replacement-cli-session")
	ctx = context.WithValue(ctx, constants.ContextKeyOperatorID, "op-1")
	ctx = context.WithValue(ctx, constants.ContextKeyOperatorSessionID, "sess-1")
	req = req.WithContext(ctx)

	envelope, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
		ActionType:        string(constants.ActionTypeDocumentUpdate),
		CliSessionId:      "replacement-cli-session",
		OperatorId:        "other-op",
		OperatorSessionId: "sess-1",
		SourceComponent:   commonv1.Component_COMPONENT_AGENT,
	})
	require.NoError(t, err)
	require.ErrorIs(t, verifyEnvelopeIdentityBinding(req, envelope), constants.ErrURISANMismatch)
}

// TestVerifyEnvelopeIdentityBinding_UnboundEnvelopeAdmitsOnlyAppRecordWrites
// pins the fail-closed rule for envelopes that claim no Operator binding.
// Direct submission executes on the Gateway's own command service, which also
// registers host reads (FS_READ, FS_LIST, FS_GREP, FETCH_LOGS, ...); an
// unbound read would otherwise read the Gateway container. MCP and platform
// enrollment reach the processor in-process, not through this endpoint, so the
// only unbound envelope this boundary admits is the app record write of
// INV-AUTH-ID-05, whichever certificate presents it. A CLI session claim alone
// is not an Operator binding.
func TestVerifyEnvelopeIdentityBinding_UnboundEnvelopeAdmitsOnlyAppRecordWrites(t *testing.T) {
	unboundEnvelope := func(actionType constants.ActionType, cliSessionID, actingAppID string, source commonv1.Component) []byte {
		b, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
			ActionType:      string(actionType),
			CliSessionId:    cliSessionID,
			ActingAppId:     actingAppID,
			SourceComponent: source,
		})
		require.NoError(t, err)
		return b
	}
	presenters := []struct {
		name         string
		spiffe       string
		cliSessionID string
		actingAppID  string
		source       commonv1.Component
		appPath      bool
	}{
		{"operator cert", "spiffe://ztd.local/operator/org-1/op-1/sess-1", "", "", commonv1.Component_COMPONENT_ZTDO, false},
		{"cli session cert", "spiffe://ztd.local/cli/user-1/cli-sess-1", "cli-sess-1", "", commonv1.Component_COMPONENT_CLIENT, false},
		{"app cert", "spiffe://ztd.local/app/test-app", "", "test-app", commonv1.Component_COMPONENT_AGENT, true},
	}
	for _, p := range presenters {
		for _, actionType := range constants.AllActionTypes {
			t.Run(p.name+"/"+string(actionType), func(t *testing.T) {
				err := verifyEnvelopeIdentityBinding(identityBindingRequest(t, p.spiffe), unboundEnvelope(actionType, p.cliSessionID, p.actingAppID, p.source))
				if p.appPath && isAppRecordWrite(actionType) {
					require.NoError(t, err)
					return
				}
				require.ErrorIs(t, err, constants.ErrIdentityBindingFailed)
			})
		}
	}
}
