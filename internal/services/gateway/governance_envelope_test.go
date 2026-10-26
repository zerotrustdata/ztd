// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// marshalEnvelope encodes a GovernanceEnvelope in its canonical protojson wire
// form — the exact bytes a BYO client POSTs and the format the downstream
// processor decodes. Identity-binding tests must use this rather than
// hand-written snake_case JSON, which is not the wire format.
func marshalEnvelope(t *testing.T, env *commonv1.GovernanceEnvelope) []byte {
	t.Helper()
	b, err := protojson.Marshal(env)
	require.NoError(t, err)
	return b
}

// identityEnvelope builds a wire envelope carrying only operator/session
// identity, for CLI- and operator-cert binding cases that have no source
// component.
func identityEnvelope(t *testing.T, _, _ string) []byte {
	t.Helper()
	return marshalEnvelope(t, &commonv1.GovernanceEnvelope{
		OperatorId:        "op-1",
		OperatorSessionId: "sess-1",
	})
}

// appEnvelope builds a wire envelope for an app workload (AGENT/CLIENT) that
// authenticates via an app SPIFFE identity.
func appEnvelope(t *testing.T, appID string, source commonv1.Component) []byte {
	t.Helper()
	return marshalEnvelope(t, &commonv1.GovernanceEnvelope{
		OperatorId:      appID,
		ActingAppId:     appID,
		SourceComponent: source,
	})
}

// operatorEnvelope builds a wire envelope bound to the Operator identity that
// operatorPeerRequest presents, so identity binding admits it.
func operatorEnvelope(t *testing.T, id string) []byte {
	t.Helper()
	return marshalEnvelope(t, &commonv1.GovernanceEnvelope{
		Id:                id,
		OperatorId:        "op-1",
		OperatorSessionId: "sess-1",
	})
}

// operatorPeerRequest builds an envelope POST whose mTLS peer certificate is
// the Operator SPIFFE ID that operatorEnvelope names.
func operatorPeerRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	spiffeURL, err := url.Parse("spiffe://ztd.local/operator/org-1/op-1/sess-1")
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{spiffeURL}}},
	}
	return req
}

// fakeEnvelopeProcessor is a test double that returns predetermined results
// for ProcessEnvelope. It captures the payload it was called with so tests
// can assert the handler forwarded the body unchanged.
type fakeEnvelopeProcessor struct {
	receipt    *operatorv1.EventReceipt
	err        error
	gotPayload []byte
	calls      int
}

// using protojson field naming conventions.
func (f *fakeEnvelopeProcessor) ProcessEnvelope(ctx context.Context, payload []byte) (*operatorv1.EventReceipt, error) {
	f.calls++
	f.gotPayload = append([]byte(nil), payload...)
	return f.receipt, f.err
}

func newGovernanceEnvelopeHandler(t *testing.T, proc governance.EnvelopeProcessor) *HTTPHandler {
	t.Helper()
	h, _, _ := setupTestHTTPHandler(t)
	if proc != nil {
		h.governanceController.envProc = proc
	}
	return h
}

func TestGovernanceEnvelope_NotConfigured_Returns503(t *testing.T) {
	h := newGovernanceEnvelopeHandler(t, nil)

	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()

	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestGovernanceEnvelope_NonPostMethod_Returns405(t *testing.T) {
	proc := &fakeEnvelopeProcessor{}
	h := newGovernanceEnvelopeHandler(t, proc)

	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(m, "/api/governance/envelope", nil)
		w := httptest.NewRecorder()
		h.governanceController.handleGovernanceEnvelope(w, req)
		require.Equal(t, http.StatusMethodNotAllowed, w.Code, "method=%s", m)
	}
	require.Zero(t, proc.calls, "envelope processor must not be called for non-POST methods")
}

func TestGovernanceEnvelope_EmptyBody_ReturnsIngressRefusal(t *testing.T) {
	proc := &fakeEnvelopeProcessor{}
	h := newGovernanceEnvelopeHandler(t, proc)

	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader(nil))
	w := httptest.NewRecorder()

	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, proc.calls, "envelope processor must not be called for empty body")
	got := &operatorv1.IngressRefusal{}
	require.NoError(t, protojson.Unmarshal(w.Body.Bytes(), got))
	require.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST, got.GetCode())
	require.Equal(t, constants.ErrInvalidJSONBody.Error(), got.GetError())
}

// TestGovernanceEnvelope_URISANMismatch_Returns403Refusal proves an Operator
// envelope whose certificate names a different Operator session is refused
// IDENTITY_BINDING_FAILED with 403, not a 500, and never reaches the processor.
func TestGovernanceEnvelope_URISANMismatch_Returns403Refusal(t *testing.T) {
	spiffeURL, parseErr := url.Parse("spiffe://ztd.local/operator/org-1/op-9/sess-9")
	require.NoError(t, parseErr)
	proc := &fakeEnvelopeProcessor{receipt: &operatorv1.EventReceipt{TransactionId: "tx-mismatch"}}
	h := newGovernanceEnvelopeHandler(t, proc)

	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader(operatorEnvelope(t, "tx-mismatch")))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{spiffeURL}}}}
	w := httptest.NewRecorder()
	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Zero(t, proc.calls, "an envelope whose certificate does not match must never reach the processor")
	got := &operatorv1.IngressRefusal{}
	require.NoError(t, protojson.Unmarshal(w.Body.Bytes(), got))
	require.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_IDENTITY_BINDING_FAILED, got.GetCode())
	require.Equal(t, constants.ErrURISANMismatch.Error(), got.GetError())
}

func TestGovernanceEnvelope_VerificationErrors_Return403(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"expired", governance.ErrTransactionExpired},
		{"replay", governance.ErrTransactionReplay},
		{"hash mismatch", governance.ErrTransactionHashMismatch},
		{"unknown action", governance.ErrUnknownActionType},
		{"missing l2", governance.ErrL2SignatureMissing},
		{"invalid l2 signature", governance.ErrL2SignatureInvalid},
		{"unknown l2 signer", governance.ErrL2ConsensusSetUnknown},
		{"missing l3 proof", governance.ErrL3ProofMissing},
		{"invalid l3 proof", governance.ErrL3ProofInvalid},
		{"state root mismatch", governance.ErrStateRootMismatch},
		{"state root required", governance.ErrStateRootRequired},
		{"l1 validation failed", fmt.Errorf("%w: violation", governance.ErrL1ValidationFailed)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc := &fakeEnvelopeProcessor{err: tc.err}
			h := newGovernanceEnvelopeHandler(t, proc)

			req := operatorPeerRequest(t, operatorEnvelope(t, "x"))
			w := httptest.NewRecorder()

			h.governanceController.handleGovernanceEnvelope(w, req)

			require.Equal(t, http.StatusForbidden, w.Code, "error %v should map to 403", tc.err)

			var body struct {
				Error string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.NotEmpty(t, body.Error)
		})
	}
}

// TestGovernanceEnvelope_VerifierUnavailable_Returns503 proves an L4 that
// cannot run (dependency absent, vault locked) is a server condition, not a
// caller rejection.
func TestGovernanceEnvelope_VerifierUnavailable_Returns503(t *testing.T) {
	for _, sentinel := range []error{
		governance.ErrL3NotaryNotConfigured,
		governance.ErrStateRootMissing,
		constants.ErrTxDoctrineMissing,
		constants.ErrWardenVaultLocked,
	} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			h := newGovernanceEnvelopeHandler(t, &fakeEnvelopeProcessor{err: sentinel})
			req := operatorPeerRequest(t, operatorEnvelope(t, "x"))
			w := httptest.NewRecorder()

			h.governanceController.handleGovernanceEnvelope(w, req)

			require.Equal(t, http.StatusServiceUnavailable, w.Code)
		})
	}
}

// TestGovernanceEnvelope_Rejection_ReturnsSignedReceipt proves an L4
// rejection hands the caller the Operator's signed receipt with its typed
// rejection_code, and renders only the sentinel as error text.
func TestGovernanceEnvelope_Rejection_ReturnsSignedReceipt(t *testing.T) {
	receipt := &operatorv1.EventReceipt{
		TransactionId: "tx-rejected",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:   operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EXPIRED,
		Signature:     "sig",
	}
	verifyErr := fmt.Errorf("%w: expired 3s ago", governance.ErrTransactionExpired)
	proc := &fakeEnvelopeProcessor{receipt: receipt, err: governance.WithRejectionReceipt(receipt, verifyErr)}
	h := newGovernanceEnvelopeHandler(t, proc)

	req := operatorPeerRequest(t, operatorEnvelope(t, "x"))
	w := httptest.NewRecorder()
	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	got := &operatorv1.GovernanceRejection{}
	require.NoError(t, protojson.Unmarshal(w.Body.Bytes(), got))
	require.Equal(t, governance.ErrTransactionExpired.Error(), got.GetError())
	require.Equal(t, "tx-rejected", got.GetReceipt().GetTransactionId())
	require.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EXPIRED, got.GetReceipt().GetRejectionCode())
	require.Equal(t, "sig", got.GetReceipt().GetSignature())
}

// TestGovernanceEnvelope_UnparseableEnvelope_Refused proves an envelope that
// cannot be decoded is refused at the identity binding gate with 400, and never
// reaches the processor.
func TestGovernanceEnvelope_UnparseableEnvelope_Refused(t *testing.T) {
	proc := &fakeEnvelopeProcessor{err: fmt.Errorf("%w: unexpected token", constants.ErrTxInvalidEnvelope)}
	h := newGovernanceEnvelopeHandler(t, proc)

	req := operatorPeerRequest(t, []byte(`not-json`))
	w := httptest.NewRecorder()

	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, proc.calls, "an unparseable envelope must never reach the processor")
}

func TestGovernanceEnvelope_OversizedPayload_Returns400(t *testing.T) {
	proc := &fakeEnvelopeProcessor{err: fmt.Errorf("payload exceeds 1048576 byte limit: %w", constants.ErrPayloadExceedsLimit)}
	h := newGovernanceEnvelopeHandler(t, proc)

	req := operatorPeerRequest(t, operatorEnvelope(t, "tx-big"))
	w := httptest.NewRecorder()

	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestGovernanceEnvelope_Success_Returns200WithSignedReceipt(t *testing.T) {
	receipt := &operatorv1.EventReceipt{
		TransactionId:    "tx-abc",
		TransactionHash:  "abc123",
		Status:           operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:    "completed",
		StateRootBefore:  "root-before",
		StateRootAfter:   "root-after",
		ExecutedAtUnixMs: 1234567890,
		SignerKeyId:      "Actuator-key-id",
		Signature:        "deadbeef",
	}
	proc := &fakeEnvelopeProcessor{receipt: receipt}
	h := newGovernanceEnvelopeHandler(t, proc)

	body := operatorEnvelope(t, "tx-abc")
	req := operatorPeerRequest(t, body)
	w := httptest.NewRecorder()

	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, proc.calls)

	// The handler stamps the gateway's posture into the envelope before
	// forwarding to the processor. The test config uses PostureNotary, so the
	// processor receives the posture field alongside the client's original
	// fields. Posture is not part of the transaction hash, so this stamp
	// does not invalidate the client's signature.
	var forwarded commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(proc.gotPayload, &forwarded))
	require.Equal(t, "tx-abc", forwarded.GetId())
	require.Equal(t, string(constants.PostureNotary), forwarded.GetPosture(), "handler must stamp gateway posture onto the envelope")

	// Receipt is returned as canonical protojson (proto field names).
	got := &operatorv1.EventReceipt{}
	require.NoError(t, protojson.Unmarshal(w.Body.Bytes(), got))
	require.Equal(t, "tx-abc", got.GetTransactionId())
	require.Equal(t, "abc123", got.GetTransactionHash())
	require.Equal(t, "deadbeef", got.GetSignature())
	require.Equal(t, "Actuator-key-id", got.GetSignerKeyId())
	require.Contains(t, w.Body.String(), `"transaction_id"`)
}

func TestGovernanceEnvelope_CallerSuppliedPosture_ReturnsIngressRefusal(t *testing.T) {
	// Only the gateway sets posture. A caller-supplied one (here a doctrine
	// downgrade sent to a notary gateway) is refused before processing.
	for _, posture := range []constants.Posture{constants.PostureDoctrine, constants.PostureNotary} {
		proc := &fakeEnvelopeProcessor{receipt: &operatorv1.EventReceipt{TransactionId: "tx-posture"}}
		h := newGovernanceEnvelopeHandler(t, proc)

		body := marshalEnvelope(t, &commonv1.GovernanceEnvelope{
			Id:                "tx-posture",
			Posture:           string(posture),
			OperatorId:        "op-1",
			OperatorSessionId: "sess-1",
		})
		req := operatorPeerRequest(t, body)
		w := httptest.NewRecorder()

		h.governanceController.handleGovernanceEnvelope(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code, posture)
		require.JSONEq(t, `{"code":"INGRESS_REFUSAL_CODE_CALLER_SUPPLIED_POSTURE","error":"`+constants.ErrEnvelopePostureCallerSupplied.Error()+`"}`, w.Body.String(), posture)
		require.Zero(t, proc.calls, "a caller-supplied posture must never reach the processor")
	}
}

func TestGovernanceEnvelope_FailedExecution_StillReturns200(t *testing.T) {
	// A signed FAILED receipt is still cryptographic evidence and must be
	// returned to the caller with HTTP 200, not surfaced as a server error.
	receipt := &operatorv1.EventReceipt{
		TransactionId: "tx-fail",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		ResultSummary: "failed: handler error",
		Signature:     "cafef00d",
		SignerKeyId:   "Actuator-key-id",
	}
	// Actuator returns (receipt, execErr) when a handler fails. ProcessEnvelope
	// propagates that pair. The HTTP layer treats a non-nil receipt as
	// authoritative - execErr alone is not 5xx territory.
	proc := &fakeEnvelopeProcessor{receipt: receipt, err: nil}
	h := newGovernanceEnvelopeHandler(t, proc)

	req := operatorPeerRequest(t, operatorEnvelope(t, "tx-fail"))
	w := httptest.NewRecorder()

	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"status":"EXECUTION_STATUS_FAILED"`, "enums render by name, as canonical protojson")
}

func TestGovernanceEnvelope_NilReceiptNilError_Returns500(t *testing.T) {
	// Defensive: a regression in the processor that returns (nil, nil) must
	// not be silently masked as success.
	proc := &fakeEnvelopeProcessor{receipt: nil, err: nil}
	h := newGovernanceEnvelopeHandler(t, proc)

	req := operatorPeerRequest(t, operatorEnvelope(t, "tx-nil"))
	w := httptest.NewRecorder()

	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestVerifyEnvelopeIdentityBinding_NoMTLS_ReturnsError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, identityEnvelope(t, "op-1", "sess-1"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "mTLS client certificate required")
}

func TestVerifyEnvelopeIdentityBinding_NoURISAN_ReturnsError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{}},
	}
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, identityEnvelope(t, "op-1", "sess-1"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "client certificate missing URI SAN")
}

func TestVerifyEnvelopeIdentityBinding_MatchingOperatorSPIFFEID_ReturnsNil(t *testing.T) {
	spiffeURL, parseErr := url.Parse("spiffe://ztd.local/operator/org-1/op-1/sess-1")
	require.NoError(t, parseErr)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			URIs: []*url.URL{spiffeURL},
		}},
	}
	envelope := identityEnvelope(t, "op-1", "sess-1")
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope)
	require.NoError(t, err)
}

func TestVerifyEnvelopeIdentityBinding_MatchingCLISessionWithOperatorClaims_ReturnsNil(t *testing.T) {
	spiffeURL, parseErr := url.Parse("spiffe://ztd.local/cli/user-1/cli-sess-1")
	require.NoError(t, parseErr)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			URIs: []*url.URL{spiffeURL},
		}},
	}
	envelope := marshalEnvelope(t, &commonv1.GovernanceEnvelope{
		EventType:         string(constants.EventAppDocumentUpdateRequested),
		OperatorId:        "op-1",
		OperatorSessionId: "operator-sess-1",
		CliSessionId:      "cli-sess-1",
		SourceComponent:   commonv1.Component_COMPONENT_AGENT,
	})
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope)
	require.NoError(t, err)
}

func TestVerifyEnvelopeIdentityBinding_MismatchedCLISessionWithOperatorClaims_ReturnsError(t *testing.T) {
	spiffeURL, parseErr := url.Parse("spiffe://ztd.local/cli/user-1/other-cli-sess")
	require.NoError(t, parseErr)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			URIs: []*url.URL{spiffeURL},
		}},
	}
	envelope := marshalEnvelope(t, &commonv1.GovernanceEnvelope{
		EventType:         string(constants.EventAppDocumentUpdateRequested),
		OperatorId:        "op-1",
		OperatorSessionId: "operator-sess-1",
		CliSessionId:      "cli-sess-1",
		SourceComponent:   commonv1.Component_COMPONENT_AGENT,
	})
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrURISANMismatch)
}

func TestVerifyEnvelopeIdentityBinding_MismatchedOperatorID_ReturnsError(t *testing.T) {
	spiffeURL, parseErr := url.Parse("spiffe://ztd.local/operator/org-1/op-2/sess-1")
	require.NoError(t, parseErr)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			URIs: []*url.URL{spiffeURL},
		}},
	}
	envelope := identityEnvelope(t, "op-1", "sess-1")
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope)
	require.Error(t, err)
	require.Contains(t, err.Error(), "certificate URI SAN does not match envelope identity claims")
}

func TestVerifyEnvelopeIdentityBinding_MatchingAppSPIFFEID_ReturnsNil(t *testing.T) {
	spiffeURL, parseErr := url.Parse("spiffe://ztd.local/app/op-1")
	require.NoError(t, parseErr)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			URIs: []*url.URL{spiffeURL},
		}},
	}
	envelope := appEnvelope(t, "op-1", commonv1.Component_COMPONENT_AGENT)
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope)
	require.NoError(t, err)
}

func TestVerifyEnvelopeIdentityBinding_AppCertificateAllowsDelegatedEmbeddedOperatorClaims(t *testing.T) {
	spiffeURL, parseErr := url.Parse("spiffe://ztd.local/app/test-app")
	require.NoError(t, parseErr)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			URIs: []*url.URL{spiffeURL},
		}},
	}
	envelope := marshalEnvelope(t, &commonv1.GovernanceEnvelope{
		EventType:         string(constants.EventAppDocumentUpdateRequested),
		OperatorId:        string(constants.DocIDEmbeddedOperator),
		OperatorSessionId: "embedded-operator-session",
		ActingAppId:       "test-app",
		SourceComponent:   commonv1.Component_COMPONENT_AGENT,
	})
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope)
	require.NoError(t, err)
}

func TestVerifyEnvelopeIdentityBinding_AppCertificateRejectsMismatchedActingApp(t *testing.T) {
	spiffeURL, parseErr := url.Parse("spiffe://ztd.local/app/test-app")
	require.NoError(t, parseErr)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			URIs: []*url.URL{spiffeURL},
		}},
	}
	envelope := marshalEnvelope(t, &commonv1.GovernanceEnvelope{
		EventType:         string(constants.EventAppDocumentUpdateRequested),
		OperatorId:        string(constants.DocIDEmbeddedOperator),
		OperatorSessionId: "embedded-operator-session",
		ActingAppId:       "different-app",
		SourceComponent:   commonv1.Component_COMPONENT_AGENT,
	})
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope)
	require.ErrorIs(t, err, constants.ErrIdentityBindingFailed)
}

func TestVerifyEnvelopeIdentityBinding_InvalidJSON_ReturnsError(t *testing.T) {
	spiffeURL, parseErr := url.Parse("spiffe://ztd.local/operator/org-1/op-1/sess-1")
	require.NoError(t, parseErr)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			URIs: []*url.URL{spiffeURL},
		}},
	}
	envelope := []byte(`not-json`)
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope)
	require.ErrorIs(t, err, constants.ErrTxInvalidEnvelope, "an unparseable envelope must be refused, not passed downstream")
}

// Delegated attribution remains required and bound to the app's user SAN even
// though it no longer occupies the authenticated-user context key.
func TestVerifyEnvelopeIdentityBinding_DelegatedRequestor(t *testing.T) {
	appURI, err := url.Parse("spiffe://ztd.local/app/test-app")
	require.NoError(t, err)
	for _, requestor := range []string{"delegated-user", "other-user", ""} {
		t.Run("requestor="+requestor, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, nil)
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{appURI}}}}
			ctx := context.WithValue(req.Context(), constants.ContextKeyAppID, appURI.String())
			ctx = context.WithValue(ctx, constants.ContextKeyDelegatedUserID, "delegated-user")
			req = req.WithContext(ctx)
			wire := marshalEnvelope(t, &commonv1.GovernanceEnvelope{
				SourceComponent: commonv1.Component_COMPONENT_AGENT,
				ActingAppId:     "test-app", RequestorUserId: requestor,
				ActionType: string(constants.ActionTypeDocumentUpdate),
			})
			err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, wire)
			if requestor == "delegated-user" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, constants.ErrIdentityBindingFailed)
			}
		})
	}
}

// TestVerifyEnvelopeIdentityBinding_RequestorBinding proves requestor_user_id
// must equal the user the auth middleware authenticated: a forged, missing, or
// extra requestor is refused, and only an exact match is admitted.
func TestVerifyEnvelopeIdentityBinding_RequestorBinding(t *testing.T) {
	cliURL, parseErr := url.Parse("spiffe://ztd.local/cli/user-A/cli-sess-1")
	require.NoError(t, parseErr)
	appURL, parseErr := url.Parse("spiffe://ztd.local/app/test-app")
	require.NoError(t, parseErr)

	cases := []struct {
		name      string
		uri       *url.URL
		ctxUser   string
		requestor string
		wantErr   bool
	}{
		{name: "match", uri: cliURL, ctxUser: "user-A", requestor: "user-A"},
		{name: "forged requestor", uri: cliURL, ctxUser: "user-A", requestor: "user-B", wantErr: true},
		{name: "missing requestor", uri: cliURL, ctxUser: "user-A", requestor: "", wantErr: true},
		{name: "extra requestor on certificate without a user", uri: appURL, ctxUser: "", requestor: "user-A", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{tc.uri}}}}
			if tc.ctxUser != "" {
				req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyUserID, tc.ctxUser))
			}
			envelope := marshalEnvelope(t, &commonv1.GovernanceEnvelope{
				OperatorId:        "op-1",
				OperatorSessionId: "sess-1",
				CliSessionId:      "cli-sess-1",
				RequestorUserId:   tc.requestor,
			})
			err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope)
			if tc.wantErr {
				require.ErrorIs(t, err, constants.ErrIdentityBindingFailed)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestVerifyEnvelopeIdentityBinding_ActingAppNotInCertificate_Refused proves an
// Operator-bound envelope cannot claim an acting_app_id the certificate does not
// prove, even when the Operator binding itself matches.
func TestVerifyEnvelopeIdentityBinding_ActingAppNotInCertificate_Refused(t *testing.T) {
	spiffeURL, parseErr := url.Parse("spiffe://ztd.local/operator/org-1/op-1/sess-1")
	require.NoError(t, parseErr)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{spiffeURL}}}}
	envelope := marshalEnvelope(t, &commonv1.GovernanceEnvelope{
		OperatorId:        "op-1",
		OperatorSessionId: "sess-1",
		ActingAppId:       "other-app",
	})
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope)
	require.ErrorIs(t, err, constants.ErrIdentityBindingFailed)
}

func TestVerifyEnvelopeIdentityBinding_NoIdentityFields_FailsClosed(t *testing.T) {
	spiffeURL, parseErr := url.Parse("spiffe://ztd.local/operator/org-1/op-1/sess-1")
	require.NoError(t, parseErr)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader([]byte(`{}`)))
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{
			URIs: []*url.URL{spiffeURL},
		}},
	}
	envelope, marshalErr := protojson.Marshal(&commonv1.GovernanceEnvelope{EventType: "test"})
	require.NoError(t, marshalErr)
	err := newTestGovernanceController(t).verifyEnvelopeIdentityBinding(req, envelope)
	require.ErrorIs(t, err, constants.ErrIdentityBindingFailed, "an unbound envelope must fail closed")
}

// TestGovernanceEnvelope_NoTLS_Refused proves an envelope POST without an mTLS
// client certificate is refused with 403 MTLS_REQUIRED, renders only the
// sentinel, and never reaches the processor.
func TestGovernanceEnvelope_NoTLS_Refused(t *testing.T) {
	proc := &fakeEnvelopeProcessor{receipt: &operatorv1.EventReceipt{TransactionId: "tx-unbound"}}
	h := newGovernanceEnvelopeHandler(t, proc)

	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader(operatorEnvelope(t, "tx-unbound")))
	w := httptest.NewRecorder()
	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.JSONEq(t, `{"code":"INGRESS_REFUSAL_CODE_MTLS_REQUIRED","error":"`+constants.ErrMTLSRequired.Error()+`"}`, w.Body.String())
	require.Zero(t, proc.calls)
}

// TestGovernanceEnvelope_ActingAppMismatch_Refused proves an Operator-bound
// envelope whose acting_app_id is not in the certificate is refused with 403
// before it reaches the processor.
func TestGovernanceEnvelope_ActingAppMismatch_Refused(t *testing.T) {
	proc := &fakeEnvelopeProcessor{receipt: &operatorv1.EventReceipt{TransactionId: "tx-app"}}
	h := newGovernanceEnvelopeHandler(t, proc)

	body := marshalEnvelope(t, &commonv1.GovernanceEnvelope{
		Id:                "tx-app",
		OperatorId:        "op-1",
		OperatorSessionId: "sess-1",
		ActingAppId:       "other-app",
	})
	req := operatorPeerRequest(t, body)
	w := httptest.NewRecorder()
	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Zero(t, proc.calls, "an acting app outside the certificate must never reach the processor")
}

// TestGovernanceEnvelope_RequestorMismatch_Refused proves a CLI certificate for
// user A cannot submit an envelope acting for user B: the handler refuses it
// with 403 before it reaches the processor.
func TestGovernanceEnvelope_RequestorMismatch_Refused(t *testing.T) {
	cliURL, parseErr := url.Parse("spiffe://ztd.local/cli/user-A/cli-sess-1")
	require.NoError(t, parseErr)
	proc := &fakeEnvelopeProcessor{receipt: &operatorv1.EventReceipt{TransactionId: "tx-req"}}
	h := newGovernanceEnvelopeHandler(t, proc)

	body := marshalEnvelope(t, &commonv1.GovernanceEnvelope{
		Id:                "tx-req",
		OperatorId:        "op-1",
		OperatorSessionId: "sess-1",
		CliSessionId:      "cli-sess-1",
		RequestorUserId:   "user-B",
	})
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.GovernanceEnvelopes, bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{URIs: []*url.URL{cliURL}}}}
	req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyUserID, "user-A"))
	w := httptest.NewRecorder()
	h.governanceController.handleGovernanceEnvelope(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Zero(t, proc.calls, "a requestor the certificate does not authenticate must never reach the processor")
}
