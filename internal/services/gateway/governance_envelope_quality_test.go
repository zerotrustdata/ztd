// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

func TestClassifyEnvelopeError_Exhaustive(t *testing.T) {
	cases := []struct {
		err      error
		expected int
	}{
		{nil, http.StatusOK},
		{governance.ErrTransactionIDMissing, http.StatusForbidden},
		{governance.ErrUnknownActionType, http.StatusForbidden},
		{governance.ErrPayloadMissing, http.StatusForbidden},
		{governance.ErrPayloadDecodeFailed, http.StatusForbidden},
		{governance.ErrL1ValidationFailed, http.StatusForbidden},
		{fmt.Errorf("%w: %q", constants.ErrTxDocumentCollectionNotGoverned, "trusted_signers"), http.StatusForbidden},
		{fmt.Errorf("%w: %q", constants.ErrTxTargetOperatorMismatch, "outbound-operator"), http.StatusForbidden},
		{fmt.Errorf("%w: %q", constants.ErrTxTargetSessionMismatch, "foreign-session"), http.StatusForbidden},
		{governance.ErrTransactionHashMissing, http.StatusForbidden},
		{governance.ErrTransactionHashMismatch, http.StatusForbidden},
		{governance.ErrTransactionExpired, http.StatusForbidden},
		{governance.ErrNonceMissing, http.StatusForbidden},
		{governance.ErrTransactionReplay, http.StatusForbidden},
		{governance.ErrStateRootMissing, http.StatusForbidden},
		{governance.ErrStateRootRequired, http.StatusForbidden},
		{governance.ErrStateRootMismatch, http.StatusForbidden},
		{governance.ErrL2SignatureMissing, http.StatusForbidden},
		{governance.ErrL2SignatureInvalid, http.StatusForbidden},
		{governance.ErrL2ConsensusNotConfigured, http.StatusForbidden},
		{governance.ErrL2ConsensusSetUnknown, http.StatusForbidden},
		{governance.ErrL3ProofMissing, http.StatusForbidden},
		{governance.ErrL3ProofInvalid, http.StatusForbidden},
		{governance.ErrL3NotaryNotConfigured, http.StatusForbidden},
		{governance.ErrTxInFlight, http.StatusForbidden},
		{governance.ErrInvalidEnvelope, http.StatusBadRequest},
		{fmt.Errorf("%w: some error", constants.ErrTxInvalidEnvelope), http.StatusBadRequest},
		{constants.ErrPubSubEmptyPayload, http.StatusBadRequest},
		{fmt.Errorf("payload exceeds 1234 byte limit: %w", constants.ErrPayloadExceedsLimit), http.StatusBadRequest},
		{fmt.Errorf("some random error"), http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("err=%v", tc.err), func(t *testing.T) {
			require.Equal(t, tc.expected, classifyEnvelopeError(tc.err))
		})
	}
}

func TestVerifyEnvelopeIdentityBinding_Exhaustive(t *testing.T) {

	mustParseURL := func(s string) *url.URL {
		u, err := url.Parse(s)
		require.NoError(t, err)
		return u
	}

	cases := []struct {
		name              string
		uris              []*url.URL
		operatorID        string
		operatorSessionID string
		actingAppID       string
		source            commonv1.Component
		expectErr         bool
	}{
		{
			name:              "CLI session match",
			uris:              []*url.URL{mustParseURL("spiffe://ztd.local/cli/user-1/sess-1")},
			operatorSessionID: "sess-1",
			expectErr:         false,
		},
		{
			name:              "CLI session mismatch",
			uris:              []*url.URL{mustParseURL("spiffe://ztd.local/cli/user-1/sess-2")},
			operatorSessionID: "sess-1",
			expectErr:         true,
		},
		{
			name:              "Operator ID and session match",
			uris:              []*url.URL{mustParseURL("spiffe://ztd.local/operator/org-1/op-1/sess-1")},
			operatorID:        "op-1",
			operatorSessionID: "sess-1",
			expectErr:         false,
		},
		{
			name:              "Operator ID mismatch",
			uris:              []*url.URL{mustParseURL("spiffe://ztd.local/operator/org-1/op-2/sess-1")},
			operatorID:        "op-1",
			operatorSessionID: "sess-1",
			expectErr:         true,
		},
		{
			name:              "Operator session mismatch",
			uris:              []*url.URL{mustParseURL("spiffe://ztd.local/operator/org-1/op-1/sess-2")},
			operatorID:        "op-1",
			operatorSessionID: "sess-1",
			expectErr:         true,
		},
		{
			name:        "App match",
			uris:        []*url.URL{mustParseURL("spiffe://ztd.local/app/app-1")},
			operatorID:  "op-1",
			actingAppID: "app-1",
			source:      commonv1.Component_COMPONENT_AGENT,
			expectErr:   false,
		},
		{
			name:        "App mismatch",
			uris:        []*url.URL{mustParseURL("spiffe://ztd.local/app/app-2")},
			operatorID:  "op-1",
			actingAppID: "app-1",
			source:      commonv1.Component_COMPONENT_AGENT,
			expectErr:   true,
		},
		{
			name:        "Non-app component skips App check",
			uris:        []*url.URL{mustParseURL("spiffe://ztd.local/app/app-1")},
			operatorID:  "op-1",
			actingAppID: "app-1",
			source:      commonv1.Component_COMPONENT_ZTDO,
			expectErr:   true,
		},
		{
			name:        "Multiple URIs, one matches",
			uris:        []*url.URL{mustParseURL("spiffe://ztd.local/unknown"), mustParseURL("spiffe://ztd.local/app/app-1")},
			operatorID:  "op-1",
			actingAppID: "app-1",
			source:      commonv1.Component_COMPONENT_AGENT,
			expectErr:   false,
		},
		{
			name:              "Wrong trust domain",
			uris:              []*url.URL{mustParseURL("spiffe://other.local/operator/org-1/op-1/sess-1")},
			operatorID:        "op-1",
			operatorSessionID: "sess-1",
			expectErr:         true,
		},
		{
			name:              "Operator match but wrong path prefix",
			uris:              []*url.URL{mustParseURL("spiffe://ztd.local/other/org-1/op-1/sess-1")},
			operatorID:        "op-1",
			operatorSessionID: "sess-1",
			expectErr:         true,
		},
		{
			name:              "Multiple URIs, none match",
			uris:              []*url.URL{mustParseURL("spiffe://ztd.local/unknown1"), mustParseURL("spiffe://ztd.local/unknown2")},
			operatorID:        "op-1",
			operatorSessionID: "sess-1",
			expectErr:         true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.TLS = &tls.ConnectionState{
				PeerCertificates: []*x509.Certificate{
					{
						URIs: tc.uris,
					},
				},
			}
			envelope := marshalEnvelope(t, &commonv1.GovernanceEnvelope{
				OperatorId:        tc.operatorID,
				OperatorSessionId: tc.operatorSessionID,
				ActingAppId:       tc.actingAppID,
				SourceComponent:   tc.source,
			})
			err := verifyEnvelopeIdentityBinding(req, envelope)
			if tc.expectErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "certificate URI SAN does not match envelope identity claims")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestEnvelopeErrorCode_RendersTypedCodeOnly proves a response renders the
// governance sentinel and never the wrapped chain, which can carry an audit or
// store failure such as a rejection receipt that could not be persisted.
func TestEnvelopeErrorCode_RendersTypedCodeOnly(t *testing.T) {
	rejection := fmt.Errorf("%w: %q", constants.ErrTxTargetOperatorMismatch, "remote-operator")
	persistFailure := fmt.Errorf("%w: %w", constants.ErrL5ActuatorRejectionReceiptPersist, constants.ErrAuditSessionUnknown)
	chain := fmt.Errorf("%w: rejection receipt: %w", rejection, persistFailure)

	status, code := envelopeErrorCode(chain)
	require.Equal(t, http.StatusForbidden, status)
	require.Equal(t, constants.ErrTxTargetOperatorMismatch, code)
	require.NotContains(t, code.Error(), "AUDIT_SESSION_UNKNOWN")
	require.NotContains(t, code.Error(), "remote-operator")

	status, code = envelopeErrorCode(fmt.Errorf("store: %w", constants.ErrAuditSessionUnknown))
	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, constants.ErrInternal, code, "an unclassified error renders as the generic internal error")
}
