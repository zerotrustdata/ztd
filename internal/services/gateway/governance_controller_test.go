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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type recordedDeliberator struct {
	envelope   *commonv1.GovernanceEnvelope
	inferences []*operatorv1.ConsensusVoteInference
}

func (d *recordedDeliberator) Deliberate(_ context.Context, body []byte) (*governance.ConsensusDeliberation, error) {
	d.envelope = &commonv1.GovernanceEnvelope{}
	if err := protojson.Unmarshal(body, d.envelope); err != nil {
		return nil, err
	}
	d.envelope.Governance = &commonv1.GovernanceMetadata{L2: &commonv1.L2Metadata{Votes: []*commonv1.L2Vote{{SignerKeyId: "tribunal-axiom", Decision: true}}}}
	wire, err := protojson.Marshal(d.envelope)
	return &governance.ConsensusDeliberation{Envelope: wire, Inferences: d.inferences}, err
}

func TestGovernanceDeliberationAuthenticatesBeforeRequestingVotes(t *testing.T) {
	for _, test := range []struct {
		name     string
		posture  string
		identity string
		status   int
	}{
		{name: "valid operator identity", identity: "op-1", status: http.StatusOK},
		{name: "different operator", identity: "other", status: http.StatusForbidden},
		{name: "caller posture", identity: "op-1", posture: "doctrine", status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := newTestGovernanceController(t)
			c.cfg.Gateway.Posture = constants.PostureConsensus
			d := &recordedDeliberator{inferences: []*operatorv1.ConsensusVoteInference{{MemberAppId: "tribunal-axiom"}}}
			c.deliberator = d
			env := &commonv1.GovernanceEnvelope{OperatorId: test.identity, OperatorSessionId: "sess-1", ActionType: string(constants.ActionTypeFileEdit), Posture: test.posture}
			wire, err := protojson.Marshal(env)
			require.NoError(t, err)
			req := identityBindingRequest(t, "spiffe://ztd.local/operator/org-1/op-1/sess-1")
			req.Body = io.NopCloser(bytes.NewReader(wire))
			w := httptest.NewRecorder()
			c.handleGovernanceDeliberation(w, req)
			require.Equal(t, test.status, w.Code, w.Body.String())
			if test.status != http.StatusOK {
				require.Nil(t, d.envelope, "refused identities never cause vote executions")
				return
			}
			require.Equal(t, "consensus", d.envelope.GetPosture())
			var result operatorv1.GovernanceDeliberationResult
			require.NoError(t, protojson.Unmarshal(w.Body.Bytes(), &result))
			require.Empty(t, result.GetEnvelope().GetPosture(), "submission gets its own authoritative posture")
			require.Len(t, result.GetEnvelope().GetGovernance().GetL2().GetVotes(), 1)
			require.Len(t, result.GetConsensusInferences(), 1)
		})
	}
}

// newTestGovernanceController creates a GovernanceController with minimal
// dependencies for unit-testing the controller. The CLI session service is
// backed by an isolated document store, as construction requires it.
func newTestGovernanceController(t *testing.T) *GovernanceController {
	t.Helper()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	responder := response.NewWriter(logger)
	cliSessions := NewCLISessionService(newTestDocumentStoreService(t), logger)
	c, err := newGovernanceController(GovernanceControllerDeps{Cfg: cfg, Logger: logger, Responder: responder, CLISessions: cliSessions})
	require.NoError(t, err)
	return c
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
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, env)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrIdentityBindingFailed, "expected ErrIdentityBindingFailed, got %v", err)
}

// TestVerifyEnvelopeIdentityBinding_DocumentDeleteEmptyIdentity_FailsClosed
// asserts the same fail-closed guarantee for DOCUMENT_DELETE.
func TestVerifyEnvelopeIdentityBinding_DocumentDeleteEmptyIdentity_FailsClosed(t *testing.T) {
	req := identityBindingRequest(t, "spiffe://ztd.local/operator/org-1/op-1/sess-1")
	env := mutationEnvelopeBytes(t, constants.ActionTypeDocumentDelete, "", "", commonv1.Component_COMPONENT_ZTDO)
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, env)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrIdentityBindingFailed, "expected ErrIdentityBindingFailed, got %v", err)
}

// TestVerifyEnvelopeIdentityBinding_FileEditEmptyIdentity_FailsClosed asserts
// the same fail-closed guarantee for FILE_EDIT.
func TestVerifyEnvelopeIdentityBinding_FileEditEmptyIdentity_FailsClosed(t *testing.T) {
	req := identityBindingRequest(t, "spiffe://ztd.local/operator/org-1/op-1/sess-1")
	env := mutationEnvelopeBytes(t, constants.ActionTypeFileEdit, "", "", commonv1.Component_COMPONENT_ZTDO)
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, env)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrIdentityBindingFailed, "expected ErrIdentityBindingFailed, got %v", err)
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
			err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(identityBindingRequest(t, tc.spiffe), tc.env)
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
			err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(identityBindingRequest(t, "spiffe://ztd.local/app/test-app"), appEnvelope(actionType))
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
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, env)
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
	require.NoError(t, newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope))

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
	require.ErrorIs(t, newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, withApp), constants.ErrIdentityBindingFailed)
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
	require.ErrorIs(t, newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope), constants.ErrURISANMismatch)
}

// TestNewGovernanceController_RequiresCLISessions pins the required dependency:
// a controller without its CLI session service is never constructed.
func TestNewGovernanceController_RequiresCLISessions(t *testing.T) {
	c, err := newGovernanceController(GovernanceControllerDeps{
		Cfg:    testutil.NewTestConfig(t),
		Logger: testutil.NewTestLogger(),
	})
	require.ErrorIs(t, err, constants.ErrGovernanceCLISessionRequired)
	require.Nil(t, c)
}

// expireCLISessionForClaim moves every expiry bound of a persisted CLI session
// into the past: the state an expired session is in when an envelope names it.
func expireCLISessionForClaim(t *testing.T, svc *CLISessionService, cliSessionID string) {
	t.Helper()
	session, err := svc.loadCLISession(t.Context(), cliSessionID)
	require.NoError(t, err)
	past := time.Now().UTC().Add(-time.Hour)
	session.ExpiresAt = past
	session.AbsoluteExpiresAt = past
	session.IdleExpiresAt = past
	data, err := json.Marshal(session)
	require.NoError(t, err)
	require.NoError(t, svc.db.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, data))
}

// requireCLISessionClaimVerdict asserts the identity check's verdict on an
// envelope whose cli_session_id claim is admitted on an Operator or app
// certificate. A nil wantReason means the envelope is admitted. Otherwise it is
// refused as a 403 identity-binding failure that wraps wantReason.
func requireCLISessionClaimVerdict(t *testing.T, c *GovernanceController, req *http.Request, envelope []byte, wantReason error) {
	t.Helper()
	err := c.verifyEnvelopeIdentityBinding(req, envelope)
	if wantReason == nil {
		require.NoError(t, err)
		return
	}
	require.ErrorIs(t, err, constants.ErrIdentityBindingFailed)
	require.ErrorIs(t, err, wantReason)
	status, sentinel := envelopeErrorCode(err)
	require.Equal(t, http.StatusForbidden, status)
	require.ErrorIs(t, sentinel, constants.ErrIdentityBindingFailed)
}

// TestVerifyEnvelopeIdentityBinding_OperatorCertCLISessionClaim pins the
// cli_session_id claim check for an envelope admitted on an Operator
// certificate. The claim must name an active, unexpired session that is owned
// by the authenticated user and bound to the envelope's operator_session_id. An
// empty claim is not checked.
func TestVerifyEnvelopeIdentityBinding_OperatorCertCLISessionClaim(t *testing.T) {
	c := newTestGovernanceController(t)
	svc := c.cliSessions
	persistActiveCLISessionForBinding(t, svc, "cli-owned", "user-1", "op-sess-1")
	persistActiveCLISessionForBinding(t, svc, "cli-foreign", "user-2", "op-sess-1")
	persistActiveCLISessionForBinding(t, svc, "cli-bound-elsewhere", "user-1", "op-sess-2")
	persistActiveCLISessionForBinding(t, svc, "cli-inactive", "user-1", "op-sess-1")
	require.NoError(t, svc.DeactivateCLISession(t.Context(), "cli-inactive"))
	persistActiveCLISessionForBinding(t, svc, "cli-expired", "user-1", "op-sess-1")
	expireCLISessionForClaim(t, svc, "cli-expired")

	cases := []struct {
		name       string
		claim      string
		wantReason error // nil means the envelope is admitted
	}{
		{name: "valid claim admitted", claim: "cli-owned"},
		{name: "empty claim admitted", claim: ""},
		{name: "foreign user's session rejected", claim: "cli-foreign", wantReason: constants.ErrCLISessionNotFound},
		{name: "inactive session rejected", claim: "cli-inactive", wantReason: constants.ErrCLISessionInvalid},
		{name: "expired session rejected", claim: "cli-expired", wantReason: constants.ErrCLISessionExpired},
		{name: "unknown session rejected", claim: "cli-unknown", wantReason: constants.ErrCLISessionNotFound},
		{name: "session bound to another operator session rejected", claim: "cli-bound-elsewhere", wantReason: constants.ErrGatewayCLISessionBindingMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := identityBindingRequest(t, "spiffe://ztd.local/operator/org-1/op-1/op-sess-1")
			req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyUserID, "user-1"))
			envelope, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
				ActionType:        string(constants.ActionTypeDocumentUpdate),
				RequestorUserId:   "user-1",
				OperatorId:        "op-1",
				OperatorSessionId: "op-sess-1",
				CliSessionId:      tc.claim,
				SourceComponent:   commonv1.Component_COMPONENT_ZTDO,
			})
			require.NoError(t, err)
			requireCLISessionClaimVerdict(t, c, req, envelope, tc.wantReason)
		})
	}
}

// TestVerifyEnvelopeIdentityBinding_AppCertCLISessionClaim applies the same
// claim check to an app certificate's platform-record write. That envelope names
// no operator_session_id, so the claimed session must also be unbound.
func TestVerifyEnvelopeIdentityBinding_AppCertCLISessionClaim(t *testing.T) {
	c := newTestGovernanceController(t)
	svc := c.cliSessions
	persistActiveCLISessionForBinding(t, svc, "cli-owned-unbound", "user-1", "")
	persistActiveCLISessionForBinding(t, svc, "cli-owned-bound", "user-1", "op-sess-1")
	persistActiveCLISessionForBinding(t, svc, "cli-foreign-unbound", "user-2", "")

	cases := []struct {
		name       string
		claim      string
		wantReason error // nil means the envelope is admitted
	}{
		{name: "unbound session owned by the user admitted", claim: "cli-owned-unbound"},
		{name: "empty claim admitted", claim: ""},
		{name: "foreign user's session rejected", claim: "cli-foreign-unbound", wantReason: constants.ErrCLISessionNotFound},
		{name: "operator-bound session rejected without an operator_session_id", claim: "cli-owned-bound", wantReason: constants.ErrGatewayCLISessionBindingMismatch},
		{name: "unknown session rejected", claim: "cli-unknown", wantReason: constants.ErrCLISessionNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := identityBindingRequest(t, "spiffe://ztd.local/app/test-app")
			req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyUserID, "user-1"))
			envelope, err := protojson.Marshal(&commonv1.GovernanceEnvelope{
				ActionType:      string(constants.ActionTypeDocumentUpdate),
				RequestorUserId: "user-1",
				ActingAppId:     "test-app",
				CliSessionId:    tc.claim,
				SourceComponent: commonv1.Component_COMPONENT_AGENT,
			})
			require.NoError(t, err)
			requireCLISessionClaimVerdict(t, c, req, envelope, tc.wantReason)
		})
	}
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
				err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(identityBindingRequest(t, p.spiffe), unboundEnvelope(actionType, p.cliSessionID, p.actingAppID, p.source))
				if p.appPath && isAppRecordWrite(actionType) {
					require.NoError(t, err)
					return
				}
				require.ErrorIs(t, err, constants.ErrIdentityBindingFailed)
			})
		}
	}
}
