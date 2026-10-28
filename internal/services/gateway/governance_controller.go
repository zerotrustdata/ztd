// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	serviceauth "github.com/zerotrustdata/ztd/internal/services/auth"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/protocol"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// GovernanceController handles governance envelope submission endpoints.
// EnvProc is injected at construction time; a nil value means envelope
// submission is not enabled. CLISessions is required.
type GovernanceController struct {
	cfg         *config.Config
	logger      *slog.Logger
	responder   *response.Writer
	deliberator mcp.L2ConsensusDeliberator
	envProc     governance.EnvelopeProcessor
	cliSessions *CLISessionService
}

// GovernanceControllerDeps groups all dependencies for GovernanceController.
// EnvProc may be nil if envelope submission is not enabled.
type GovernanceControllerDeps struct {
	Cfg         *config.Config
	Logger      *slog.Logger
	Responder   *response.Writer
	EnvProc     governance.EnvelopeProcessor
	Deliberator mcp.L2ConsensusDeliberator
	// CLISessions resolves the cli_session_id an envelope claims when it is
	// admitted on an Operator or app certificate. Required: a nil service fails
	// construction with constants.ErrGovernanceCLISessionRequired.
	CLISessions *CLISessionService
}

func newGovernanceController(d GovernanceControllerDeps) (*GovernanceController, error) {
	if d.CLISessions == nil {
		return nil, fmt.Errorf("governance controller: %w", constants.ErrGovernanceCLISessionRequired)
	}
	return &GovernanceController{
		cfg:         d.Cfg,
		logger:      d.Logger,
		responder:   d.Responder,
		envProc:     d.EnvProc,
		deliberator: d.Deliberator,
		cliSessions: d.CLISessions,
	}, nil
}

// verifyEnvelopeIdentityBinding enforces transport-to-envelope identity binding
// (Plan §2). It extracts the mTLS certificate's URI SANs and verifies they match
// the envelope's internal identity claims (cli_session_id, operator_session_id, operator_id, source_component).
// This prevents an Agent cert from impersonating another workload's envelope.
// A cli_session_id claim that is admitted on an Operator or app certificate
// (rather than on its own CLI session certificate) is checked by verifyCLISessionClaim.
func (c *GovernanceController) verifyEnvelopeIdentityBinding(r *http.Request, envelopeBody []byte) error {
	// Ensure mTLS is present
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return constants.ErrMTLSRequired
	}

	cert := r.TLS.PeerCertificates[0]
	if len(cert.URIs) == 0 {
		return constants.ErrCertURISANMissing
	}

	// Parse the envelope from its canonical protojson wire form to extract
	// identity claims. This MUST use the same codec the downstream processor
	// uses (protojson) — decoding with encoding/json would miss the camelCase
	// wire field names and silently bypass identity binding for every real
	// BYO client.
	var envelope commonv1.GovernanceEnvelope
	if err := protojson.Unmarshal(envelopeBody, &envelope); err != nil {
		return fmt.Errorf("identity binding: decode envelope: %w: %w", constants.ErrTxInvalidEnvelope, err)
	}

	// The requestor is the human the envelope acts for, and it must be the user
	// the auth middleware bound to this request: an app's delegated user SAN or
	// another principal's authenticated user. Delegation grants no human session.
	// A certificate without a user cannot claim a non-empty requestor.
	authenticatedUserID := serviceauth.RequestorUserID(r.Context())
	if envelope.GetRequestorUserId() != authenticatedUserID {
		return fmt.Errorf("%w: requestor_user_id does not match the authenticated principal", constants.ErrIdentityBindingFailed)
	}

	cliSessionID := envelope.GetCliSessionId()
	operatorSessionID := envelope.GetOperatorSessionId()
	operatorID := envelope.GetOperatorId()
	actingAppID := envelope.GetActingAppId()
	sourceComponent := envelope.GetSourceComponent()
	actionType := constants.ActionType(envelope.GetActionType())

	wid := protocol.NewWorkloadIdentity()

	// Fail closed for any envelope with no Operator binding. An empty
	// operator_id AND operator_session_id means no delegated Operator authority
	// is claimed, so the only admissible binding is the transport-level app
	// workload identity: an AGENT/CLIENT envelope whose acting_app_id matches
	// the mTLS app SPIFFE ID, and only for a platform-record write (an app
	// persisting cases, investigations, and memories for a browser session).
	// The envelope executes on the Gateway's own command service, which also
	// registers Operator actions: mutations such as EXECUTE_BASH, FILE_EDIT,
	// and SHUTDOWN, and host reads such as FS_READ, FS_GREP, and FETCH_LOGS.
	// An unbound read would therefore read the Gateway container, so reads get
	// no pass-through. MCP and platform enrollment reach the processor
	// in-process, not through this endpoint. A cli_session_id claim alone is
	// not an Operator binding.
	if operatorSessionID == "" && operatorID == "" {
		if isAppRecordWrite(actionType) && actingAppID != "" && isAppComponent(sourceComponent) && certBindsApp(wid, cert.URIs, actingAppID) {
			return c.verifyCLISessionClaim(r.Context(), authenticatedUserID, cliSessionID, operatorSessionID)
		}
		return fmt.Errorf("%w: action %q requires operator_id or operator_session_id binding, or a document write with an acting_app_id matching the app certificate",
			constants.ErrIdentityBindingFailed, actionType)
	}

	// An Operator-bound envelope may still name an acting app. That app must be
	// the one the certificate proves; a free acting_app_id claim is refused.
	if actingAppID != "" && !certBindsApp(wid, cert.URIs, actingAppID) {
		return fmt.Errorf("%w: acting_app_id %q does not match the certificate app identity", constants.ErrIdentityBindingFailed, actingAppID)
	}

	// The auth middleware has already verified the certificate against the CLI
	// session it names, and stamped the operator binding from that persisted
	// session. When the envelope's claims agree with that server-derived context,
	// admit it without re-deriving them from the URI SAN. All claimed identities
	// must agree with the context before this path admits the envelope; raw
	// request headers are never authoritative here.
	if authenticatedCLISessionID, ok := r.Context().Value(constants.ContextKeyCLISessionID).(string); ok &&
		authenticatedCLISessionID != "" && cliSessionID == authenticatedCLISessionID {
		authenticatedOperatorID, _ := r.Context().Value(constants.ContextKeyOperatorID).(string)
		authenticatedOperatorSessionID, _ := r.Context().Value(constants.ContextKeyOperatorSessionID).(string)
		if operatorID == authenticatedOperatorID && operatorSessionID == authenticatedOperatorSessionID {
			return nil
		}
	}

	// A certificate that names the claimed CLI session admits the claim by itself.
	// That pass runs over every URI before any Operator or app match, so an
	// Operator or app admission below is never mistaken for a CLI-session match.
	for _, uri := range cert.URIs {
		spiffeID := uri.String()

		// Check CLI session match — works with or without operator_id
		if cliSessionID != "" {
			if wid.MatchesCLISessionOnly(spiffeID, cliSessionID) {
				return nil
			}
		}

		if operatorSessionID != "" {
			if wid.MatchesCLISessionOnly(spiffeID, operatorSessionID) {
				return nil
			}
		}
	}

	// An Operator or app certificate admits the envelope, so the claimed CLI
	// session must also pass verifyCLISessionClaim before the admission stands.
	for _, uri := range cert.URIs {
		spiffeID := uri.String()

		// Operator cert match — requires both fields
		if operatorSessionID != "" && operatorID != "" {
			// Format: spiffe://ztd.local/operator/<organization_id>/<operator_id>/<operator_session_id>
			// We check if the SPIFFE ID ends with the operator_id and operator_session_id
			if strings.HasSuffix(spiffeID, "/"+operatorID+"/"+operatorSessionID) {
				// Verify it's an Operator SPIFFE ID (starts with spiffe://ztd.local/operator/)
				if strings.HasPrefix(spiffeID, "spiffe://"+protocol.TrustDomain+"/operator/") {
					return c.verifyCLISessionClaim(r.Context(), authenticatedUserID, cliSessionID, operatorSessionID)
				}
			}
		}

		// App workload match — AGENT and CLIENT certificates bind to acting_app_id.
		// The operator fields identify delegated human authority and need not equal
		// the app identity. CLI/operator sessions are matched above.
		if actingAppID != "" && isAppComponent(sourceComponent) {
			if wid.MatchesApp(spiffeID, actingAppID) {
				return c.verifyCLISessionClaim(r.Context(), authenticatedUserID, cliSessionID, operatorSessionID)
			}
		}
	}

	// No matching URI SAN found
	return fmt.Errorf("%w (operator_id=%s, operator_session_id=%s, source_component=%s)",
		constants.ErrURISANMismatch, operatorID, operatorSessionID, sourceComponent)
}

// verifyCLISessionClaim checks the cli_session_id an envelope claims when its
// admission rests on an Operator or app certificate instead of on that
// session's own certificate. The claim must name a CLI session that is active
// and unexpired, owned by the authenticated user, and, when the session records
// operator bindings, bound to the envelope's operator_session_id. A refusal
// wraps constants.ErrIdentityBindingFailed with the specific reason, which the
// HTTP layer renders as a 403. An empty claim is not checked.
func (c *GovernanceController) verifyCLISessionClaim(ctx context.Context, authenticatedUserID, cliSessionID, operatorSessionID string) error {
	if cliSessionID == "" {
		return nil
	}
	session, err := c.cliSessions.loadCLISession(ctx, cliSessionID)
	if errors.Is(err, constants.ErrCLISessionNotFound) {
		return fmt.Errorf("%w: cli_session_id claim names no CLI session: %w", constants.ErrIdentityBindingFailed, err)
	}
	if err != nil {
		return fmt.Errorf("identity binding: load cli_session_id claim: %w", err)
	}
	if !session.IsActive {
		return fmt.Errorf("%w: cli_session_id claim is not active: %w", constants.ErrIdentityBindingFailed, constants.ErrCLISessionInvalid)
	}
	if cliSessionExpired(session, time.Now()) {
		return fmt.Errorf("%w: cli_session_id claim is expired: %w", constants.ErrIdentityBindingFailed, constants.ErrCLISessionExpired)
	}
	if authenticatedUserID == "" || session.UserID != authenticatedUserID {
		return fmt.Errorf("%w: cli_session_id claim is not owned by the authenticated principal: %w", constants.ErrIdentityBindingFailed, constants.ErrCLISessionNotFound)
	}
	if len(boundSessionIDsOf(session)) > 0 && !cliSessionBindsOperator(session, operatorSessionID) {
		return fmt.Errorf("%w: cli_session_id claim is not bound to operator_session_id: %w", constants.ErrIdentityBindingFailed, constants.ErrGatewayCLISessionBindingMismatch)
	}
	return nil
}

// isAppComponent reports whether the envelope's source component is an app
// workload (AGENT or CLIENT) that authenticates via an app SPIFFE identity
// (spiffe://<trust-domain>/app/<operator_id>).
func isAppComponent(c commonv1.Component) bool {
	return c == commonv1.Component_COMPONENT_AGENT || c == commonv1.Component_COMPONENT_CLIENT
}

// isAppRecordWrite reports whether an app may perform actionType with no
// Operator binding: only platform-record document writes (INV-AUTH-ID-05).
func isAppRecordWrite(a constants.ActionType) bool {
	return a == constants.ActionTypeDocumentUpdate || a == constants.ActionTypeDocumentDelete
}

// certBindsApp reports whether one of the certificate's URI SANs is the app
// SPIFFE ID for appID.
func certBindsApp(wid *protocol.WorkloadIdentity, uris []*url.URL, appID string) bool {
	for _, uri := range uris {
		if wid.MatchesApp(uri.String(), appID) {
			return true
		}
	}
	return false
}

// stampEnvelopePosture sets the gateway's governance posture on an incoming
// envelope. The gateway is the sole posture authority: an envelope that
// arrives with any posture already set is refused, never trusted or
// rewritten. Posture is not part of the transaction hash, so stamping it does
// not invalidate the caller's hash.
func stampEnvelopePosture(body []byte, posture string) ([]byte, error) {
	var envelope commonv1.GovernanceEnvelope
	if err := protojson.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode envelope: %w: %w", constants.ErrTxInvalidEnvelope, err)
	}
	if envelope.Posture != "" {
		return nil, fmt.Errorf("stamp posture %q: %w", envelope.Posture, constants.ErrEnvelopePostureCallerSupplied)
	}
	envelope.Posture = posture
	reMarshaled, err := protojson.Marshal(&envelope)
	if err != nil {
		return nil, fmt.Errorf("re-marshal envelope: %w", err)
	}
	return reMarshaled, nil
}

// handleGovernanceEnvelope is the canonical synchronous mutation entry point
// for BYO Gateway clients. It accepts a GovernanceEnvelope, verifies it
// through the Gateway's fail-closed gate (id, hash, expiry, nonce, state
// root, L2/L3 governance), executes it through the Actuator, and returns the
// signed EventReceipt.
//
// Status semantics:
//   - 200 OK: envelope verified and executed (receipt body); receipt.status
//     reflects whether the underlying handler succeeded or failed.
//   - 400 Bad Request: malformed envelope (decode failure, empty body,
//     payload too large) - no governance state mutated. Body is an
//     IngressRefusal.
//   - 403 Forbidden: governance verification failed before execution
//     (expired, replayed, hash mismatch, missing/invalid L2/L3, unknown
//     action type, or transport-to-envelope identity mismatch) - no state
//     mutated. An L4 rejection returns a GovernanceRejection body carrying
//     the Operator's signed GOVERNANCE_REJECTED receipt and its
//     rejection_code; refusals before L4 return an IngressRefusal.
//   - 503 Service Unavailable: envelope processor not configured, or L4
//     could not run (vault locked, verifier dependency absent); the latter
//     returns a GovernanceRejection, the former an IngressRefusal.
//   - 405 Method Not Allowed: non-POST methods, IngressRefusal body.
//
// @Summary		Submit governance envelope
// @Description	Accepts a GovernanceEnvelope, verifies it through the 5-layer gauntlet (L1–L3 pre-flight,
// @Description	L4 warden, L5 actuator), and returns the signed EventReceipt. Transport-to-envelope
// @Description	identity binding is enforced via mTLS certificate URI SAN matching.
// @Tags			governance
// @Accept			json
// @Produce		json
// @Param			envelope	body		[]byte		true	"GovernanceEnvelope (protojson)"
// @Success		200			{object}	operatorv1.EventReceipt	"Canonical protojson EventReceipt"
// @Failure		400			{object}	operatorv1.IngressRefusal	"Bad Request — malformed envelope"
// @Failure		403			{object}	operatorv1.IngressRefusal	"Forbidden — IngressRefusal when refused before L4 (identity binding, outcome, L3 proof unmintable); GovernanceRejection carrying the signed receipt when L4 rejects"
// @Failure		405			{object}	operatorv1.IngressRefusal	"Method Not Allowed"
// @Failure		503			{object}	operatorv1.IngressRefusal	"Service Unavailable — IngressRefusal when the processor is not configured; GovernanceRejection carrying the signed receipt when L4 cannot run"
// @Router			/api/v1/governance/envelopes [post]
func (c *GovernanceController) handleGovernanceEnvelope(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.renderEnvelopeError(w, fmt.Errorf("governance envelope: %s: %w", r.Method, constants.ErrMethodNotAllowed))
		return
	}
	if c.envProc == nil {
		c.renderEnvelopeError(w, fmt.Errorf("governance envelope: %w", constants.ErrEnvelopeProcessorNotInit))
		return
	}
	proc := c.envProc

	body, err := readRequestBody(r, c.cfg.Gateway.MaxPayloadBytes)
	if err != nil {
		c.renderEnvelopeError(w, err)
		return
	}
	if len(body) == 0 {
		c.renderEnvelopeError(w, fmt.Errorf("governance envelope: empty body: %w", constants.ErrInvalidJSONBody))
		return
	}

	// Transport-to-envelope identity binding (Plan §2)
	// Extract mTLS certificate URI SANs and verify they match the envelope's
	// internal identity claims. This prevents an Agent cert from impersonating
	// another workload's envelope.
	if err := c.verifyEnvelopeIdentityBinding(r, body); err != nil {
		c.renderEnvelopeError(w, err)
		return
	}

	// The gateway is the sole posture authority (INV-GOV-POST-02). A caller
	// never sends posture; one that does is refused.
	body, err = stampEnvelopePosture(body, string(c.cfg.Gateway.Posture))
	if err != nil {
		c.renderEnvelopeError(w, err)
		return
	}

	receipt, procErr := proc.ProcessEnvelope(r.Context(), body)
	if procErr != nil {
		c.renderEnvelopeError(w, procErr)
		return
	}
	if receipt == nil {
		// Defensive: a nil receipt with nil error should never happen, but if
		// the processor regresses, do not mask the failure.
		c.renderEnvelopeError(w, fmt.Errorf("governance envelope: %w", constants.ErrNilReceipt))
		return
	}

	// Receipt is returned as canonical protojson. Execution-failure receipts
	// (status=FAILED) are still HTTP 200 because they represent a verified,
	// audited outcome - the caller has cryptographic evidence of the attempt.
	c.responder.ProtoJSONCanonical(w, http.StatusOK, receipt)
}

// renderEnvelopeError writes a direct-submission failure: the signed
// GovernanceRejection for an L4 rejection, otherwise the typed IngressRefusal
// for a refusal before an envelope exists.
func (c *GovernanceController) renderEnvelopeError(w http.ResponseWriter, err error) {
	c.logger.Warn("governance envelope refused", logging.ErrorKind(err))
	status, _ := envelopeErrorCode(err)
	if rejection := governance.RejectionResponse(err); rejection != nil {
		c.responder.ProtoJSONCanonical(w, status, rejection)
		return
	}
	c.responder.ProtoJSONCanonical(w, status, governance.NewIngressRefusal(err))
}

// envelopeBadRequestErrors are caller-side bad input: decode failures and
// empty or oversized payloads.
var envelopeBadRequestErrors = []error{
	constants.ErrRequestBodyRead,
	constants.ErrEnvelopePostureCallerSupplied,
	constants.ErrPubSubEmptyPayload,
	constants.ErrPayloadExceedsLimit,
	constants.ErrTxInvalidEnvelope,
	constants.ErrInvalidJSONBody,
}

// envelopeMethodNotAllowedErrors are requests that use a method the endpoint
// does not accept.
var envelopeMethodNotAllowedErrors = []error{
	constants.ErrMethodNotAllowed,
}

// envelopeUnavailableErrors are server conditions where the endpoint cannot
// serve the request at all.
var envelopeUnavailableErrors = []error{
	constants.ErrEnvelopeProcessorNotInit,
}

// envelopeForbiddenErrors are caller-side refusals raised before L4
// verification, which carry no rejection code. L4 verification sentinels are
// classified by governance.GovernanceRejectionSentinel.
var envelopeForbiddenErrors = []error{
	constants.ErrMTLSRequired,
	constants.ErrCertURISANMissing,
	constants.ErrURISANMismatch,
	constants.ErrIdentityBindingFailed,
	constants.ErrTxOutcomeNotRegistered,
	constants.ErrTxOutcomeNotAllowed,
	constants.ErrTxL3ProofUnmintable,
}

// classifyEnvelopeError maps a governance verification error to an HTTP status.
// Caller-side bad input (decode failures, empty/oversized payloads) maps to 400;
// a verifier that cannot run (vault locked, dependency absent) maps to 503;
// every other governance rejection is 403; anything else is 500.
func classifyEnvelopeError(err error) int {
	status, _ := envelopeErrorCode(err)
	return status
}

// envelopeErrorCode returns the HTTP status and the typed sentinel that
// identifies err. A response renders the sentinel, never the wrapped chain:
// a chain can carry internal audit or store failure text, such as a
// rejection receipt that could not be persisted. An unclassified error is a
// 500 with constants.ErrInternal.
func envelopeErrorCode(err error) (int, error) {
	if err == nil {
		return http.StatusOK, nil
	}
	for _, sentinel := range envelopeBadRequestErrors {
		if errors.Is(err, sentinel) {
			return http.StatusBadRequest, sentinel
		}
	}
	for _, sentinel := range envelopeMethodNotAllowedErrors {
		if errors.Is(err, sentinel) {
			return http.StatusMethodNotAllowed, sentinel
		}
	}
	for _, sentinel := range envelopeUnavailableErrors {
		if errors.Is(err, sentinel) {
			return http.StatusServiceUnavailable, sentinel
		}
	}
	if code, sentinel := governance.GovernanceRejectionSentinel(err); sentinel != nil {
		switch code {
		case operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_RUNTIME_VAULT_LOCKED,
			operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED:
			return http.StatusServiceUnavailable, sentinel
		default:
			return http.StatusForbidden, sentinel
		}
	}
	for _, sentinel := range envelopeForbiddenErrors {
		if errors.Is(err, sentinel) {
			return http.StatusForbidden, sentinel
		}
	}
	return http.StatusInternalServerError, constants.ErrInternal
}

// handleGovernanceDeliberation obtains the internal Tribunal's L2 decision.
// The subject is never executed here; submission still requires independent L4 verification.
//
// @Summary Deliberate with the internal Tribunal
// @Description Authenticates an envelope and returns its Tribunal L2 votes and signed vote inference evidence. Does not execute the subject or mint L3 proofs.
// @Tags governance
// @Accept json
// @Produce json
// @Param envelope body []byte true "GovernanceEnvelope (protojson, without posture)"
// @Success 200 {object} operatorv1.GovernanceDeliberationResult
// @Failure 400 {object} operatorv1.IngressRefusal
// @Failure 403 {object} operatorv1.IngressRefusal
// @Failure 405 {object} operatorv1.IngressRefusal
// @Failure 503 {object} operatorv1.IngressRefusal
// @Router /api/v1/governance/deliberations [post]
func (c *GovernanceController) handleGovernanceDeliberation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.renderEnvelopeError(w, constants.ErrMethodNotAllowed)
		return
	}
	if c.deliberator == nil {
		c.responder.ProtoJSONCanonical(w, http.StatusServiceUnavailable, governance.NewIngressRefusal(constants.ErrConsensusAppNotEnrolled))
		return
	}
	body, err := readRequestBody(r, c.cfg.Gateway.MaxPayloadBytes)
	if err != nil {
		c.renderEnvelopeError(w, err)
		return
	}
	if len(body) == 0 {
		c.renderEnvelopeError(w, constants.ErrInvalidJSONBody)
		return
	}
	if err := c.verifyEnvelopeIdentityBinding(r, body); err != nil {
		c.renderEnvelopeError(w, err)
		return
	}
	body, err = stampEnvelopePosture(body, string(c.cfg.Gateway.Posture))
	if err != nil {
		c.renderEnvelopeError(w, err)
		return
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		c.logger.Warn("governance deliberation: clear response deadline", logging.ErrorKind(err))
	}
	decision, err := c.deliberator.Deliberate(r.Context(), body)
	if err != nil {
		c.renderEnvelopeError(w, err)
		return
	}
	if decision == nil {
		c.renderEnvelopeError(w, constants.ErrTxInvalidEnvelope)
		return
	}
	var env commonv1.GovernanceEnvelope
	if err := protojson.Unmarshal(decision.Envelope, &env); err != nil {
		c.renderEnvelopeError(w, constants.ErrTxInvalidEnvelope)
		return
	}
	// Posture is authoritative on ingress and must not become a caller claim.
	env.Posture = ""
	c.responder.ProtoJSONCanonical(w, http.StatusOK, &operatorv1.GovernanceDeliberationResult{Envelope: &env, ConsensusInferences: decision.Inferences})
}
