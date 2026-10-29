// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/auth"
	"github.com/zerotrustdata/ztd/internal/uuid"
	"github.com/zerotrustdata/ztd/protocol"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// PlatformEnrollmentHandler dispatches the five platform enrollment
// governance actions through the canonical L4/L5 gauntlet. Each handler
// decodes the PlatformEnrollmentGovernancePayload from the envelope
// payload, performs the typed mutation against the document store (or
// delegates to a session service), and returns a receipt summary string
// that the L5Actuator stamps into the signed final receipt.
//
// Receipt summaries contain only public identifiers and attribution
// metadata: request ID, component kind, instance ID, fingerprints,
// decision, actor user ID, and resulting document/session IDs. CSR PEM,
// token hashes, and private keys never appear in summaries or audit
// records.

type platformEnrollmentCompletionUpdate struct {
	Issued                 *models.PlatformEnrollmentCompleteResponse `json:"issued"`
	CompletedAt            time.Time                                  `json:"completed_at"`
	IssuanceEnvelopeID     string                                     `json:"issuance_envelope_id"`
	IssuanceReceiptID      string                                     `json:"issuance_receipt_id"`
	CertificateSerial      string                                     `json:"certificate_serial"`
	CertificateFingerprint string                                     `json:"certificate_fingerprint"`
	OperatorID             string                                     `json:"operator_id,omitempty"`
	OperatorSessionID      string                                     `json:"operator_session_id,omitempty"`
	CLISessionID           string                                     `json:"cli_session_id,omitempty"`
}

type platformEnrollmentRevocationUpdate struct {
	RevokedAt            time.Time `json:"revoked_at"`
	RevokedByUserID      string    `json:"revoked_by_user_id"`
	RevocationReason     string    `json:"revocation_reason"`
	RevocationEnvelopeID string    `json:"revocation_envelope_id"`
	RevocationReceiptID  string    `json:"revocation_receipt_id"`
}

type platformOperatorTerminationUpdate struct {
	Status            string    `json:"status"`
	UpdatedAt         time.Time `json:"updated_at"`
	TerminationReason string    `json:"termination_reason"`
}

type PlatformEnrollmentHandler struct {
	deps   PlatformEnrollmentDeps
	logger platformEnrollmentLogger
}

// newPlatformEnrollmentHandler constructs the handler from the gateway-side
// dependency bundle. The bundle is required in gateway mode; outbound
// (operator) mode never constructs this handler.
func newPlatformEnrollmentHandler(deps PlatformEnrollmentDeps, logger platformEnrollmentLogger) *PlatformEnrollmentHandler {
	return &PlatformEnrollmentHandler{deps: deps, logger: logger}
}

// decodePayload unmarshals the binary protobuf envelope payload into the
// typed PlatformEnrollmentGovernancePayload. The payload is binary proto
// (the L4Warden decodes the same way); protojson is only used at the
// transport boundary.
func (h *PlatformEnrollmentHandler) decodePayload(msg *PubSubCommandMessage) (*commonv1.PlatformEnrollmentGovernancePayload, error) {
	req, err := unmarshalPayload(msg.EventType, msg.Payload)
	if err != nil {
		return nil, fmt.Errorf("platform enrollment: decode payload: %w", err)
	}
	payload, ok := req.(*commonv1.PlatformEnrollmentGovernancePayload)
	if !ok {
		return nil, fmt.Errorf("platform enrollment: unexpected payload type %T: %w", req, constants.ErrTxPayloadActionMismatch)
	}
	return payload, nil
}

// protoComponentKind maps the proto enum back to the typed domain kind.
func protoComponentKind(k commonv1.PlatformComponentKind) (models.PlatformComponentKind, error) {
	switch k {
	case commonv1.PlatformComponentKind_PLATFORM_COMPONENT_KIND_DASHBOARD:
		return models.PlatformComponentDashboard, nil
	case commonv1.PlatformComponentKind_PLATFORM_COMPONENT_KIND_OPERATOR:
		return models.PlatformComponentOperator, nil
	case commonv1.PlatformComponentKind_PLATFORM_COMPONENT_KIND_APPLICATION:
		return models.PlatformComponentApplication, nil
	default:
		return "", constants.ErrPlatformEnrollmentInvalidComponent
	}
}

// protoDecision maps the proto enum back to the typed domain decision.
func protoDecision(d commonv1.PlatformEnrollmentDecision) (models.PlatformEnrollmentDecision, error) {
	switch d {
	case commonv1.PlatformEnrollmentDecision_PLATFORM_ENROLLMENT_DECISION_APPROVE:
		return models.PlatformEnrollmentDecisionApprove, nil
	case commonv1.PlatformEnrollmentDecision_PLATFORM_ENROLLMENT_DECISION_DENY:
		return models.PlatformEnrollmentDecisionDeny, nil
	default:
		return "", constants.ErrPlatformEnrollmentInvalidDecision
	}
}

// HandleCreate is the audit-only CREATE handler. The enrollment service
// writes the pending request document directly via DocSet before
// submitting this envelope (CREATE is classified as non-mutation in
// IsMutation, so invariant 17 does not apply to that initial write).
// This handler decodes the payload, returns a receipt summary, and
// writes nothing to the doc store.
func (h *PlatformEnrollmentHandler) HandleCreate(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("platform enrollment: create context: %w", err)
	}
	payload, err := h.decodePayload(msg)
	if err != nil {
		return "", err
	}
	kind, err := protoComponentKind(payload.GetComponentKind())
	if err != nil {
		return "", err
	}
	fp := fingerprintsFromPayload(payload.GetFingerprints())
	h.logger.Info("platform enrollment create audited",
		"request_id", payload.GetRequestId(),
		"component_kind", string(kind),
		"instance_id", payload.GetInstanceId())
	return fmt.Sprintf("platform enrollment create request_id=%s component=%s instance=%s fingerprints=%s",
		payload.GetRequestId(), string(kind), payload.GetInstanceId(), fingerprintsSummary(fp)), nil
}

// HandleDecide applies the fingerprint-bound cohort through the document
// owner, which rechecks authority and all preconditions in one transaction.
func (h *PlatformEnrollmentHandler) HandleDecide(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	payload, err := h.decodePayload(msg)
	if err != nil {
		return "", err
	}
	decision, err := protoDecision(payload.GetDecision())
	if err != nil {
		return "", err
	}
	req := models.PlatformEnrollmentBatchDecisionRequest{Decision: decision, Reason: payload.GetReason()}
	for _, target := range payload.GetDecisionTargets() {
		req.Requests = append(req.Requests, models.PlatformEnrollmentDecisionTarget{
			RequestID: target.GetRequestId(), Fingerprints: fingerprintsFromPayload(target.GetFingerprints()),
		})
	}
	if len(req.Requests) == 0 {
		req.Requests = []models.PlatformEnrollmentDecisionTarget{{RequestID: payload.GetRequestId(), Fingerprints: fingerprintsFromPayload(payload.GetFingerprints())}}
	} else if payload.GetRequestId() != "" {
		return "", constants.ErrPlatformEnrollmentInvalidDecision
	}
	if err := h.deps.DocStore.DecidePlatformEnrollments(ctx, payload.GetActorUserId(), req, msg.ID); err != nil {
		return "", fmt.Errorf("platform enrollment: decide cohort: %w", err)
	}
	return fmt.Sprintf("platform enrollment decide count=%d decision=%s actor=%s", len(req.Requests), decision, payload.GetActorUserId()), nil
}

// HandleIssue loads the request, verifies it is in the issuing state
// (the enrollment service acquires the issuance lease by transitioning
// approved -> issuing before submitting this envelope), reads the CSR
// PEM from the stored document, calls the PKI signer, stores the issued
// cert material and generated IDs, and performs the issuing -> completed
// transition. The envelope/receipt ID (msg.ID) is stamped as
// issuance_envelope_id and issuance_receipt_id. Operator issuance signs
// both the operator and CLI CSRs and persists the operator document;
// app issuance uses SignPlatformAppCSR for the dual-SAN app certificate.
func (h *PlatformEnrollmentHandler) HandleIssue(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	payload, err := h.decodePayload(msg)
	if err != nil {
		return "", err
	}
	kind, err := protoComponentKind(payload.GetComponentKind())
	if err != nil {
		return "", err
	}
	requestID := payload.GetRequestId()
	if requestID == "" {
		return "", constants.ErrPlatformEnrollmentRequestIDRequired
	}
	actorUserID := payload.GetActorUserId()
	if actorUserID == "" {
		return "", constants.ErrPlatformEnrollmentInvalidDecision
	}

	req, err := loadPlatformEnrollmentRequest(ctx, h.deps, requestID)
	if err != nil {
		return "", err
	}
	if req == nil {
		return "", constants.ErrPlatformEnrollmentRequestNotFound
	}
	// The enrollment service acquires the issuance lease (approved ->
	// issuing) before submitting the ISSUE envelope. The handler starts
	// from the issuing state and performs the signing + issuing ->
	// completed transition. A request still in the approved state means
	// the lease was never acquired; a request in any other state is a
	// concurrent or stale call. A lease naming another holder means this
	// submitter's lease was reclaimed; it must neither sign nor write.
	leaseOwner := payload.GetIssuanceLeaseOwner()
	if req.State != models.PlatformEnrollmentStateIssuing || leaseOwner == "" || req.IssuanceLeaseOwner != leaseOwner {
		return "", constants.ErrPlatformEnrollmentIssuanceInProgress
	}

	issued, operatorID, operatorSessionID, cliSessionID, certSerial, certFingerprint, err := h.signComponent(ctx, req, actorUserID)
	if err != nil {
		// Roll back issuing -> approved so a retry can re-acquire. A
		// failed signing must not permanently consume approval. The
		// rollback must land even when the request was cancelled, or a
		// retry could never re-acquire the approval.
		if _, rollbackErr := h.deps.DocStore.TransitionPlatformEnrollment(context.WithoutCancel(ctx), models.PlatformEnrollmentTransition{
			RequestID: requestID, From: models.PlatformEnrollmentStateIssuing, To: models.PlatformEnrollmentStateApproved,
			LeaseOwner: leaseOwner,
		}); rollbackErr != nil {
			h.logger.Error("platform enrollment: rollback issuing failed",
				"request_id", requestID, "error", rollbackErr)
		}
		return "", fmt.Errorf("platform enrollment: issue %s: %w", requestID, err)
	}

	// Persist the issued response, generated IDs, cert metadata, and
	// transition issuing -> completed in a single lease-fenced update.
	completionFields, err := json.Marshal(platformEnrollmentCompletionUpdate{
		Issued:                 issued,
		CompletedAt:            time.Now().UTC(),
		IssuanceEnvelopeID:     msg.ID,
		IssuanceReceiptID:      msg.ID,
		CertificateSerial:      certSerial,
		CertificateFingerprint: certFingerprint,
		OperatorID:             operatorID,
		OperatorSessionID:      operatorSessionID,
		CLISessionID:           cliSessionID,
	})
	if err != nil {
		return "", fmt.Errorf("platform enrollment: marshal completion %s: %w", requestID, err)
	}
	applied, err := h.deps.DocStore.TransitionPlatformEnrollment(ctx, models.PlatformEnrollmentTransition{
		RequestID: requestID, From: models.PlatformEnrollmentStateIssuing, To: models.PlatformEnrollmentStateCompleted,
		LeaseOwner: leaseOwner, Fields: completionFields,
	})
	if err != nil {
		return "", fmt.Errorf("platform enrollment: complete issuance %s: %w", requestID, err)
	}
	if !applied {
		return "", constants.ErrPlatformEnrollmentIssuanceInProgress
	}

	h.logger.Info("platform enrollment issued",
		"request_id", requestID,
		"component_kind", string(kind),
		"operator_id", operatorID,
		"cert_fingerprint", certFingerprint)
	summary := fmt.Sprintf("platform enrollment issue request_id=%s component=%s cert_fingerprint=%s",
		requestID, string(kind), certFingerprint)
	if operatorID != "" {
		summary += fmt.Sprintf(" operator_id=%s", operatorID)
	}
	return summary, nil
}

// signComponent performs the component-specific signing and returns the
// issued completion response plus the generated IDs, cert serial, and
// cert fingerprint. Operator issuance signs both CSRs and persists the
// operator document; app issuance uses SignPlatformAppCSR for the
// dual-SAN certificate.
func (h *PlatformEnrollmentHandler) signComponent(ctx context.Context, req *models.PlatformEnrollmentRequest, actorUserID string) (*models.PlatformEnrollmentCompleteResponse, string, string, string, string, string, error) {
	trustBundle, err := h.deps.PKI.GatewayTrustBundle()
	if err != nil {
		return nil, "", "", "", "", "", fmt.Errorf("trust bundle: %w", err)
	}

	switch req.ComponentKind {
	case models.PlatformComponentDashboard, models.PlatformComponentApplication:
		return h.signAppComponent(req, actorUserID, trustBundle)
	case models.PlatformComponentOperator:
		return h.signOperatorComponent(ctx, req, actorUserID, trustBundle)
	default:
		return nil, "", "", "", "", "", constants.ErrPlatformEnrollmentInvalidComponent
	}
}

// signAppComponent signs the dashboard/application app CSR with the
// dual-SAN SignPlatformAppCSR signer and builds the app credentials
// response. The app certificate carries the canonical app SPIFFE URI
// plus the approving user's SPIFFE URI.
func (h *PlatformEnrollmentHandler) signAppComponent(req *models.PlatformEnrollmentRequest, actorUserID string, trustBundle []byte) (*models.PlatformEnrollmentCompleteResponse, string, string, string, string, string, error) {
	if req.App == nil {
		return nil, "", "", "", "", "", constants.ErrPlatformEnrollmentInvalidPayload
	}
	certPEM, chainPEM, err := h.deps.PKI.SignPlatformAppCSR(req.App.CSRPEM, req.ComponentName, actorUserID)
	if err != nil {
		return nil, "", "", "", "", "", fmt.Errorf("sign app csr: %w", err)
	}
	certFingerprint := fingerprintFromPEM(certPEM)
	certSerial := serialFromPEM(certPEM)
	expiresAt := expiryFromPEM(certPEM)

	creds := &models.PlatformEnrollmentAppCredentials{
		AppID:       req.ComponentName,
		AppCert:     certPEM,
		CertChain:   chainPEM,
		TrustBundle: string(trustBundle),
		ExpiresAt:   expiresAt,
	}
	resp := &models.PlatformEnrollmentCompleteResponse{
		RequestID:     req.ID,
		ComponentKind: req.ComponentKind,
		App:           creds,
	}
	return resp, "", "", "", certSerial, certFingerprint, nil
}

// signOperatorComponent signs both the operator and CLI CSRs, persists
// the operator document stamped with the approving owner's user_id, and
// builds the operator credentials response. The operator document is
// user-owned (user_id = actorUserID) so the approving owner can discover
// and manage it through ListUserOperators. is_slot remains false:
// platform-enrolled operators are not user-created slots, but they are
// user-owned.
func (h *PlatformEnrollmentHandler) signOperatorComponent(ctx context.Context, req *models.PlatformEnrollmentRequest, actorUserID string, trustBundle []byte) (*models.PlatformEnrollmentCompleteResponse, string, string, string, string, string, error) {
	if req.Operator == nil {
		return nil, "", "", "", "", "", constants.ErrPlatformEnrollmentInvalidPayload
	}
	user, organization, err := loadPlatformEnrollmentOrganization(ctx, h.deps, actorUserID)
	if err != nil {
		return nil, "", "", "", "", "", err
	}
	operatorID, err := uuid.NewString()
	if err != nil {
		return nil, "", "", "", "", "", err
	}
	actuatorPub, err := req.Operator.ActuatorKey()
	if err != nil {
		return nil, "", "", "", "", "", fmt.Errorf("operator actuator key: %w", err)
	}
	actuatorKeyHex := hex.EncodeToString(actuatorPub)
	auditorPub, err := req.Operator.AuditorKey()
	if err != nil {
		return nil, "", "", "", "", "", fmt.Errorf("operator auditor key: %w", err)
	}
	auditorKeyHex := hex.EncodeToString(auditorPub)
	if err := h.refuseActuatorKeyChange(ctx, operatorID, actuatorKeyHex); err != nil {
		return nil, "", "", "", "", "", err
	}
	operatorSessionID, err := uuid.NewString()
	if err != nil {
		return nil, "", "", "", "", "", err
	}
	cliSessionID, err := uuid.NewString()
	if err != nil {
		return nil, "", "", "", "", "", err
	}
	now := time.Now().UTC()

	operatorCertPEM, operatorChainPEM, err := h.deps.PKI.SignCSR(
		req.Operator.OperatorCSRPEM, constants.LeafTypeOperator, organization.ID, operatorID, "", operatorSessionID, "",
	)
	if err != nil {
		return nil, "", "", "", "", "", fmt.Errorf("sign operator csr: %w", err)
	}
	cliCertPEM, cliCertChainPEM, err := h.deps.PKI.SignCSR(
		req.Operator.CLICSRPEM, constants.LeafTypeCLI, "", "", user.ID, cliSessionID, "",
	)
	if err != nil {
		return nil, "", "", "", "", "", fmt.Errorf("sign cli csr: %w", err)
	}
	cliCertFingerprint := fingerprintFromPEM(cliCertPEM)
	cliCertSerial := serialFromPEM(cliCertPEM)

	// Persist the operator document idempotently. The operator identity
	// is certificate-based; user_id is the approving owner so the owner
	// can discover and manage the platform-enrolled operator.
	operatorDoc := &operatorv1.OperatorDocument{
		Id:                   operatorID,
		UserId:               user.ID,
		OrganizationId:       organization.ID,
		Component:            string(constants.ComponentNameZTDO),
		Name:                 req.Hostname,
		Status:               string(constants.OperatorStatusOffline),
		OperatorSessionId:    operatorSessionID,
		OperatorType:         string(constants.OperatorTypeRemote),
		SystemFingerprint:    req.SystemFingerprint,
		OperatorCapabilities: models.OperatorCapabilitiesToProto(req.Operator.Capabilities.WithDefault()),
		// The Actuator key bound at enrollment. Receipts from this Operator
		// verify against it alone (SignerStoreService.ReceiptSignerKey in the Gateway).
		ActuatorPublicKey: actuatorKeyHex,
		ActuatorKeyId:     actuatorKeyHex,
		AuditorPublicKey:  auditorKeyHex,
		AuditorKeyId:      auditorKeyHex,
		Claimed:           true,
		ClaimedAt:         timestamppb.New(now),
		CreatedAt:         timestamppb.New(now),
		UpdatedAt:         timestamppb.New(now),
	}
	if err := h.deps.DocStore.RegisterOperator(ctx, operatorDoc); err != nil {
		return nil, "", "", "", "", "", fmt.Errorf("register operator: %w", err)
	}
	if err := h.supersedeOperatorLeases(ctx, user.ID, req.SystemFingerprint, actuatorKeyHex, operatorID); err != nil {
		return nil, "", "", "", "", "", fmt.Errorf("supersede prior operator leases: %w", err)
	}
	// Announce the enrollment so an already-connected browser session sees
	// the new Operator over SSE instead of only on its next reload or the
	// next unrelated status event's debounced re-list.
	h.deps.DocStore.NotifyOperatorEnrolled(ctx, operatorID, user.ID, req.Hostname)

	creds := &models.PlatformEnrollmentOperatorCredentials{
		OperatorCert:      operatorCertPEM,
		OperatorCertChain: operatorChainPEM,
		HubTrustBundle:    string(trustBundle),
		OperatorSessionID: operatorSessionID,
		OperatorID:        operatorID,
		CLISessionID:      cliSessionID,
		CLICert:           cliCertPEM,
		CLICertChain:      cliCertChainPEM,
		Posture:           h.deps.Posture,
	}
	resp := &models.PlatformEnrollmentCompleteResponse{
		RequestID:     req.ID,
		ComponentKind: req.ComponentKind,
		Operator:      creds,
	}
	return resp, operatorID, operatorSessionID, cliSessionID, cliCertSerial, cliCertFingerprint, nil
}

// supersedeOperatorLeases terminates every other non-terminated remote Operator
// document the owner holds for the same system fingerprint or the same Actuator
// key and deactivates its Operator session. The fingerprint folds in host,
// directory, account, and port, and the Actuator key lives in the Operator's
// working directory, so either match is the same Operator identity enrolling
// again, not a second Operator. Capabilities are not identity: a re-enrollment
// that adds or drops a capability replaces its prior lease. Without this, each
// re-enrollment left the previous lease active beside the new one and a role
// that must resolve to exactly one Operator (provider-boundary observer,
// provenance, inference) became ambiguous.
// The new document is persisted first so a failure here leaves a redundant
// lease, never zero; a retried issuance supersedes whatever it left behind.
// refuseActuatorKeyChange refuses to rebind a stored Operator document to a
// different Actuator key. Rotation is re-enrollment: it mints a new
// operator_id, so the new key binds to a new document and the superseded
// document keeps its own key.
func (h *PlatformEnrollmentHandler) refuseActuatorKeyChange(ctx context.Context, operatorID, actuatorKeyHex string) error {
	doc, err := h.deps.DocStore.DocGet(ctx, marshaler.CollectionName(constants.CollectionOperators), operatorID)
	if err != nil {
		return fmt.Errorf("load operator %s actuator binding: %w", operatorID, err)
	}
	if doc == nil {
		return nil
	}
	stored, err := models.OperatorDocumentFromStore(doc)
	if err != nil {
		return fmt.Errorf("decode operator %s actuator binding: %w", operatorID, err)
	}
	if stored.GetActuatorPublicKey() != "" && stored.GetActuatorPublicKey() != actuatorKeyHex {
		return fmt.Errorf("operator %s: %w", operatorID, constants.ErrOperatorActuatorKeyChanged)
	}
	return nil
}

func (h *PlatformEnrollmentHandler) supersedeOperatorLeases(ctx context.Context, ownerID, systemFingerprint, actuatorKeyID, replacementID string) error {
	if systemFingerprint == "" && actuatorKeyID == "" {
		return nil
	}
	docs, err := h.deps.DocStore.FindOperatorLeases(ctx, ownerID, systemFingerprint, actuatorKeyID)
	if err != nil {
		return fmt.Errorf("query operators for identity: %w", err)
	}
	reason := fmt.Sprintf("superseded by re-enrollment as operator %s", replacementID)
	for _, doc := range docs {
		if doc.ID == replacementID {
			continue
		}
		var sessionID string
		if raw, ok := doc.Data["operator_session_id"]; ok {
			if err := json.Unmarshal(raw, &sessionID); err != nil {
				return fmt.Errorf("decode operator session of %s: %w", doc.ID, err)
			}
		}
		if sessionID != "" {
			if err := h.deps.OperatorSessions.DeactivateOperatorSession(ctx, sessionID); err != nil &&
				!errors.Is(err, constants.ErrGatewayOperatorSessionInvalid) {
				return fmt.Errorf("deactivate operator session of %s: %w", doc.ID, err)
			}
		}
		update, err := json.Marshal(platformOperatorTerminationUpdate{
			Status:            string(constants.OperatorStatusTerminated),
			UpdatedAt:         time.Now().UTC(),
			TerminationReason: reason,
		})
		if err != nil {
			return fmt.Errorf("marshal termination of %s: %w", doc.ID, err)
		}
		if _, err := h.deps.DocStore.DocUpdate(ctx, marshaler.CollectionName(constants.CollectionOperators), doc.ID, update); err != nil {
			return fmt.Errorf("terminate superseded operator %s: %w", doc.ID, err)
		}
	}
	return nil
}

// committedIssuance loads the enrollment request a follow-on mutation carries
// out. PERSIST_POLICY and CREATE_SESSION hold no L3 proof of their own
// (AuthorizedByDecision): their authority is the completed issuance the owner's
// decision committed. Anything short of that completed request fails closed.
func committedIssuance(ctx context.Context, deps PlatformEnrollmentDeps, requestID string) (*models.PlatformEnrollmentRequest, error) {
	req, err := loadPlatformEnrollmentRequest(ctx, deps, requestID)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, constants.ErrPlatformEnrollmentRequestNotFound
	}
	if req.State != models.PlatformEnrollmentStateCompleted {
		return nil, fmt.Errorf("%w: request %s is %s", constants.ErrPlatformEnrollmentIssuanceNotCommitted, requestID, req.State)
	}
	return req, nil
}

// issuanceField pairs one payload value with the committed issuance it must
// equal.
type issuanceField struct {
	name      string
	payload   string
	committed string
}

// matchIssuance returns ErrPlatformEnrollmentIssuanceMismatch naming the first
// field whose payload value differs from the committed issuance.
func matchIssuance(fields ...issuanceField) error {
	for _, f := range fields {
		if f.payload != f.committed {
			return fmt.Errorf("%w: %s", constants.ErrPlatformEnrollmentIssuanceMismatch, f.name)
		}
	}
	return nil
}

// HandlePersistPolicy writes the app policy document via DocSet at the
// target_collection/target_document_id carried in the payload. The
// ownership and cert metadata come from the payload (populated by the
// enrollment service from the ISSUE handler outputs). This handler is
// only invoked for dashboard and application components.
func (h *PlatformEnrollmentHandler) HandlePersistPolicy(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("platform enrollment: persist policy context: %w", err)
	}
	payload, err := h.decodePayload(msg)
	if err != nil {
		return "", err
	}
	requestID := payload.GetRequestId()
	if requestID == "" {
		return "", constants.ErrPlatformEnrollmentRequestIDRequired
	}
	targetCollection := payload.GetTargetCollection()
	targetDocumentID := payload.GetTargetDocumentId()
	if targetCollection == "" || targetDocumentID == "" {
		return "", constants.ErrPlatformEnrollmentInvalidPayload
	}
	ownerUserID := payload.GetOwnerUserId()
	if ownerUserID == "" {
		return "", constants.ErrPlatformEnrollmentInvalidPayload
	}

	req, err := committedIssuance(ctx, h.deps, requestID)
	if err != nil {
		return "", err
	}
	if req.ComponentKind != models.PlatformComponentDashboard && req.ComponentKind != models.PlatformComponentApplication {
		return "", constants.ErrPlatformEnrollmentInvalidComponent
	}
	if err := matchIssuance(
		issuanceField{"target_collection", targetCollection, marshaler.CollectionName(constants.CollectionAppPolicies)},
		issuanceField{"target_document_id", targetDocumentID, protocol.NewWorkloadIdentity().AppSPIFFEID(req.ComponentName)},
		issuanceField{"owner_user_id", ownerUserID, req.ApprovedByUserID},
		issuanceField{"actor_user_id", payload.GetActorUserId(), req.ApprovedByUserID},
		issuanceField{"certificate_serial", payload.GetCertificateSerial(), req.CertificateSerial},
		issuanceField{"certificate_fingerprint", payload.GetCertificateFingerprint(), req.CertificateFingerprint},
	); err != nil {
		return "", err
	}

	existingDoc, err := h.deps.DocStore.DocGet(ctx, targetCollection, targetDocumentID)
	if err != nil {
		return "", fmt.Errorf("platform enrollment: check existing policy: %w", err)
	}
	if existingDoc != nil {
		if err := h.persistAppSigningKey(ctx, req); err != nil {
			return "", err
		}
		policyID := payload.GetPolicyId()
		h.logger.Info("platform enrollment policy already exists, retaining",
			"request_id", requestID,
			"policy_id", policyID,
			"target_document_id", targetDocumentID)
		return fmt.Sprintf("platform enrollment persist_policy retained request_id=%s policy_id=%s document=%s",
			requestID, policyID, targetDocumentID), nil
	}

	now := time.Now().UTC()
	policy := models.AppPolicy{
		AppID:                  targetDocumentID,
		OwnerUserID:            ownerUserID,
		ApprovedByUserID:       payload.GetActorUserId(),
		EnrollmentRequestID:    requestID,
		CertificateSerial:      payload.GetCertificateSerial(),
		CertificateFingerprint: payload.GetCertificateFingerprint(),
		AllowedCollections:     nil,
		RateLimitRPS:           0,
		MaxPayloadBytes:        0,
		RequireL3Approval:      false,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	data, err := json.Marshal(policy)
	if err != nil {
		return "", fmt.Errorf("platform enrollment: marshal policy: %w", err)
	}
	if err := h.deps.DocStore.DocSet(ctx, targetCollection, targetDocumentID, data); err != nil {
		return "", fmt.Errorf("platform enrollment: persist policy: %w", err)
	}

	if err := h.persistAppSigningKey(ctx, req); err != nil {
		return "", err
	}

	policyID := payload.GetPolicyId()
	h.logger.Info("platform enrollment policy persisted",
		"request_id", requestID,
		"policy_id", policyID,
		"target_document_id", targetDocumentID)
	return fmt.Sprintf("platform enrollment persist_policy request_id=%s policy_id=%s document=%s",
		requestID, policyID, targetDocumentID), nil
}

// persistAppSigningKey registers only the separate key committed by this
// completed issuance. Retried PERSIST_POLICY must finish this write even when
// its policy document was already committed by an earlier attempt.
func (h *PlatformEnrollmentHandler) persistAppSigningKey(ctx context.Context, req *models.PlatformEnrollmentRequest) error {
	if req.App == nil {
		return nil
	}
	key, err := req.App.SigningKey()
	if err != nil {
		return fmt.Errorf("platform enrollment: decode signing key: %w", err)
	}
	if key == nil {
		return nil
	}
	digest := sha256.Sum256(key)
	if req.ComponentKind != models.PlatformComponentApplication || hex.EncodeToString(digest[:]) != req.Fingerprints.SigningKey {
		return constants.ErrPlatformEnrollmentIssuanceMismatch
	}
	signer := models.TrustedSigner{
		ID: req.ComponentName, PublicKey: hex.EncodeToString(key), Enabled: true, AddedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(signer)
	if err != nil {
		return fmt.Errorf("platform enrollment: marshal signing key: %w", err)
	}
	// Create-only persistence prevents concurrent enrollments and old retries
	// from replacing an active key between a lookup and an upsert.
	err = h.deps.DocStore.DocCreate(ctx, marshaler.CollectionName(constants.CollectionTrustedSigners), req.ComponentName, data)
	if err == nil {
		return nil
	}
	if !errors.Is(err, constants.ErrAlreadyExists) {
		return fmt.Errorf("platform enrollment: persist signing key: %w", err)
	}
	existing, err := h.deps.DocStore.DocGet(ctx, marshaler.CollectionName(constants.CollectionTrustedSigners), req.ComponentName)
	if err != nil {
		return fmt.Errorf("platform enrollment: read signing key: %w", err)
	}
	if existing != nil {
		var signer models.TrustedSigner
		if err := existing.DecodeData(&signer); err != nil {
			return fmt.Errorf("platform enrollment: decode existing signing key: %w", err)
		}
		if signer.PublicKey != hex.EncodeToString(key) || !signer.Enabled {
			return fmt.Errorf("%w: revoke the existing application enrollment before replacing its signing key", constants.ErrPlatformEnrollmentIssuanceMismatch)
		}
		return nil
	}
	return constants.ErrPlatformEnrollmentIssuanceMismatch
}

func (h *PlatformEnrollmentHandler) HandleRevoke(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("platform enrollment: revoke context: %w", err)
	}
	payload, err := h.decodePayload(msg)
	if err != nil {
		return "", err
	}
	requestID := payload.GetRequestId()
	if requestID == "" {
		return "", constants.ErrPlatformEnrollmentRequestIDRequired
	}
	req, err := loadPlatformEnrollmentRequest(ctx, h.deps, requestID)
	if err != nil {
		return "", err
	}
	if req == nil {
		return "", constants.ErrPlatformEnrollmentRequestNotFound
	}
	if req.State != models.PlatformEnrollmentStateCompleted {
		return "", constants.ErrPlatformEnrollmentInvalidState
	}
	if h.deps.Connections == nil {
		return "", constants.ErrPlatformEnrollmentDepsRequired
	}
	reason := strings.TrimSpace(payload.GetReason())
	if reason == "" {
		reason = string(constants.PlatformEnrollmentIntentRevoke)
	}
	if req.CertificateSerial != "" {
		if err := h.deps.PKI.RevokeCertificate(ctx, req.CertificateSerial, reason); err != nil {
			return "", fmt.Errorf("platform enrollment: revoke certificate: %w", err)
		}
	}
	switch req.ComponentKind {
	case models.PlatformComponentDashboard, models.PlatformComponentApplication:
		if payload.GetTargetDocumentId() == "" {
			return "", constants.ErrPlatformEnrollmentInvalidPayload
		}
		if err := h.deps.DocStore.DocDelete(ctx, marshaler.CollectionName(constants.CollectionAppPolicies), payload.GetTargetDocumentId()); err != nil {
			return "", fmt.Errorf("platform enrollment: revoke app policy: %w", err)
		}
		if err := h.deps.DocStore.DocDelete(ctx, marshaler.CollectionName(constants.CollectionTrustedSigners), req.ComponentName); err != nil {
			return "", fmt.Errorf("platform enrollment: revoke signing key: %w", err)
		}
		boundDoc, err := h.deps.DocStore.DocGet(ctx, marshaler.CollectionName(constants.CollectionConsensus), string(constants.DocIDConsensusBinding))
		if err != nil {
			return "", fmt.Errorf("platform enrollment: read consensus binding for revocation: %w", err)
		}
		if boundDoc != nil {
			var boundPolicy models.ConsensusPolicy
			if err := boundDoc.DecodeData(&boundPolicy); err != nil {
				return "", fmt.Errorf("platform enrollment: decode consensus binding for revocation: %w", err)
			}
			isMember := false
			for _, m := range boundPolicy.MemberAppIDs {
				if m == req.ComponentName {
					isMember = true
					break
				}
			}
			if boundPolicy.PrimaryAppID() == req.ComponentName || isMember {
				if err := h.deps.DocStore.DocDelete(ctx, marshaler.CollectionName(constants.CollectionConsensus), string(constants.DocIDConsensusBinding)); err != nil {
					return "", fmt.Errorf("platform enrollment: revoke consensus binding: %w", err)
				}
			}
		}
		h.deps.Connections.DisconnectIdentity(payload.GetTargetDocumentId())
	case models.PlatformComponentOperator:
		if req.Issued == nil || req.Issued.Operator == nil {
			return "", constants.ErrPlatformEnrollmentInvalidPayload
		}
		operatorSerial := serialFromPEM(req.Issued.Operator.OperatorCert)
		if operatorSerial == "" {
			return "", constants.ErrPlatformEnrollmentInvalidPayload
		}
		if err := h.deps.PKI.RevokeCertificate(ctx, operatorSerial, reason); err != nil {
			return "", fmt.Errorf("platform enrollment: revoke operator certificate: %w", err)
		}
		operatorSPIFFEID, err := spiffeIDFromCertificate(req.Issued.Operator.OperatorCert)
		if err != nil {
			return "", err
		}
		cliSPIFFEID, err := spiffeIDFromCertificate(req.Issued.Operator.CLICert)
		if err != nil {
			return "", err
		}
		if err := h.deps.CLISessions.DeactivateCLISession(ctx, req.CLISessionID); err != nil &&
			!errors.Is(err, constants.ErrCLISessionAlreadyDeactivated) &&
			!errors.Is(err, constants.ErrCLISessionNotFound) {
			return "", fmt.Errorf("platform enrollment: deactivate CLI session: %w", err)
		}
		if err := h.deps.OperatorSessions.DeactivateOperatorSession(ctx, req.OperatorSessionID); err != nil &&
			!errors.Is(err, constants.ErrGatewayOperatorSessionInvalid) {
			return "", fmt.Errorf("platform enrollment: deactivate operator session: %w", err)
		}
		update, err := json.Marshal(platformOperatorTerminationUpdate{
			Status:            string(constants.OperatorStatusTerminated),
			UpdatedAt:         time.Now().UTC(),
			TerminationReason: reason,
		})
		if err != nil {
			return "", fmt.Errorf("platform enrollment: marshal operator revocation: %w", err)
		}
		if _, err := h.deps.DocStore.DocUpdate(ctx, marshaler.CollectionName(constants.CollectionOperators), req.OperatorID, update); err != nil {
			return "", fmt.Errorf("platform enrollment: terminate operator: %w", err)
		}
		h.deps.Connections.DisconnectIdentity(operatorSPIFFEID)
		h.deps.Connections.DisconnectIdentity(cliSPIFFEID)
	default:
		return "", constants.ErrPlatformEnrollmentInvalidComponent
	}
	revocationUpdate, err := json.Marshal(platformEnrollmentRevocationUpdate{
		RevokedAt:            time.Now().UTC(),
		RevokedByUserID:      payload.GetActorUserId(),
		RevocationReason:     reason,
		RevocationEnvelopeID: msg.ID,
		RevocationReceiptID:  msg.ID,
	})
	if err != nil {
		return "", fmt.Errorf("platform enrollment: marshal revocation %s: %w", requestID, err)
	}
	applied, err := h.deps.DocStore.TransitionPlatformEnrollment(ctx, models.PlatformEnrollmentTransition{
		RequestID: requestID, From: models.PlatformEnrollmentStateCompleted, To: models.PlatformEnrollmentStateRevoked,
		Fields: revocationUpdate,
	})
	if err != nil {
		return "", fmt.Errorf("platform enrollment: mark revoked: %w", err)
	}
	if !applied {
		return "", constants.ErrPlatformEnrollmentInvalidState
	}
	return fmt.Sprintf("platform enrollment revoke request_id=%s component=%s", requestID, req.ComponentKind), nil
}

// HandleCreateSession delegates to CLISessionService and
// OperatorSessionService using the session IDs carried in the payload.
// The handler writes nothing to the doc store directly; the session
// services own their persistence. This handler is only invoked for the
// operator component (dashboard and application have no sessions). Both
// sessions are bound to the approving owner's user_id (the actor) so the
// owner can discover and manage the platform-enrolled operator. Each
// session is created only if absent: redeeming a completed request
// re-submits this envelope and must not reactivate a session that was
// deactivated or superseded since issuance.
func (h *PlatformEnrollmentHandler) HandleCreateSession(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("platform enrollment: create session context: %w", err)
	}
	payload, err := h.decodePayload(msg)
	if err != nil {
		return "", err
	}
	requestID := payload.GetRequestId()
	if requestID == "" {
		return "", constants.ErrPlatformEnrollmentRequestIDRequired
	}
	operatorID := payload.GetOperatorId()
	operatorSessionID := payload.GetOperatorSessionId()
	cliSessionID := payload.GetCliSessionId()
	if operatorID == "" || operatorSessionID == "" || cliSessionID == "" {
		return "", constants.ErrPlatformEnrollmentInvalidPayload
	}
	actorUserID := payload.GetActorUserId()
	if actorUserID == "" {
		return "", constants.ErrPlatformEnrollmentInvalidDecision
	}
	req, err := committedIssuance(ctx, h.deps, requestID)
	if err != nil {
		return "", err
	}
	if req.ComponentKind != models.PlatformComponentOperator {
		return "", constants.ErrPlatformEnrollmentInvalidComponent
	}
	if err := matchIssuance(
		issuanceField{"actor_user_id", actorUserID, req.ApprovedByUserID},
		issuanceField{"operator_id", operatorID, req.OperatorID},
		issuanceField{"operator_session_id", operatorSessionID, req.OperatorSessionID},
		issuanceField{"cli_session_id", cliSessionID, req.CLISessionID},
		issuanceField{"certificate_serial", payload.GetCertificateSerial(), req.CertificateSerial},
		issuanceField{"certificate_fingerprint", payload.GetCertificateFingerprint(), req.CertificateFingerprint},
	); err != nil {
		return "", err
	}
	user, organization, err := loadPlatformEnrollmentOrganization(ctx, h.deps, actorUserID)
	if err != nil {
		return "", err
	}

	cliExists, err := h.sessionExists(ctx, constants.CollectionCLISessions, cliSessionID)
	if err != nil {
		return "", err
	}
	operatorExists, err := h.sessionExists(ctx, constants.CollectionOperatorSessions, operatorSessionID)
	if err != nil {
		return "", err
	}

	// Persist the CLI session bound to the approving owner's user_id.
	// The cert fingerprint/serial come from the payload (populated by
	// the enrollment service from the ISSUE handler outputs).
	if !cliExists {
		if err := h.deps.CLISessions.PersistCLISession(ctx,
			cliSessionID, operatorSessionID, user.ID,
			"", payload.GetCertificateFingerprint(), payload.GetCertificateSerial(),
			string(constants.HeartbeatTypeBootstrap),
		); err != nil {
			return "", fmt.Errorf("platform enrollment: persist cli session: %w", err)
		}
	}
	if !operatorExists {
		if err := h.deps.OperatorSessions.PersistOperatorSession(ctx,
			operatorSessionID, user.ID, organization.ID, operatorID,
			string(constants.HeartbeatTypeBootstrap),
		); err != nil {
			return "", fmt.Errorf("platform enrollment: persist operator session: %w", err)
		}
	}

	h.logger.Info("platform enrollment sessions created",
		"request_id", requestID,
		"operator_id", operatorID,
		"operator_session_id", operatorSessionID,
		"cli_session_id", cliSessionID,
		"actor_user_id", actorUserID)
	return fmt.Sprintf("platform enrollment create_session request_id=%s operator_id=%s operator_session=%s cli_session=%s actor=%s",
		requestID, operatorID, operatorSessionID, cliSessionID, actorUserID), nil
}

// HandleBindConsensus binds an enrolled application as the Gateway consensus application.
func (h *PlatformEnrollmentHandler) HandleBindConsensus(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("platform enrollment: bind consensus context: %w", err)
	}
	payload, err := h.decodePayload(msg)
	if err != nil {
		return "", err
	}
	actorUserID := strings.TrimSpace(payload.GetActorUserId())
	if actorUserID == "" {
		return "", constants.ErrPlatformEnrollmentInvalidDecision
	}
	spec, err := models.ResolveConsensusBinding(models.ConsensusBindingSpec{
		Provider:     constants.ConsensusProvider(payload.GetConsensusProvider()),
		AppID:        payload.GetInstanceId(),
		EndpointURL:  payload.GetConsensusEndpointUrl(),
		MemberAppIDs: payload.GetConsensusMemberAppIds(),
		Quorum:       int(payload.GetConsensusQuorum()),
		ModelTag:     payload.GetConsensusModelTag(),
		ModelDigest:  payload.GetConsensusModelDigest(),
		VoteKind:     payload.GetConsensusVoteKind(), InferenceOperatorID: payload.GetConsensusInferenceOperatorId(), VoteThresholds: models.ConsensusThresholdsFromProto(payload.GetConsensusVoteThresholds()), AllowHosted: payload.GetConsensusAllowHosted(),
	})
	if err != nil {
		return "", fmt.Errorf("platform enrollment: %w", err)
	}
	appID := spec.AppID

	// Validate ownership, active enrollment policy, and signing key for every
	// member at the governed L5 boundary before writing the consensus policy.
	for _, memberID := range spec.MemberAppIDs {
		spiffeID := protocol.NewWorkloadIdentity().AppSPIFFEID(memberID)
		appDoc, err := h.deps.DocStore.DocGet(ctx, marshaler.CollectionName(constants.CollectionAppPolicies), spiffeID)
		if err != nil {
			return "", fmt.Errorf("platform enrollment: check app policy: %w", err)
		}
		if appDoc == nil {
			return "", constants.ErrConsensusAppNotEnrolled
		}
		var appPolicy models.AppPolicy
		if err := appDoc.DecodeData(&appPolicy); err != nil {
			return "", fmt.Errorf("platform enrollment: decode app policy: %w", err)
		}
		if appPolicy.OwnerUserID != actorUserID {
			return "", constants.ErrConsensusMemberNotOwned
		}
		signerDoc, err := h.deps.DocStore.DocGet(ctx, marshaler.CollectionName(constants.CollectionTrustedSigners), memberID)
		if err != nil {
			return "", fmt.Errorf("platform enrollment: check trusted signer: %w", err)
		}
		if signerDoc == nil {
			return "", constants.ErrConsensusAppMissingSignerKey
		}
		var signer models.TrustedSigner
		if err := signerDoc.DecodeData(&signer); err != nil {
			return "", fmt.Errorf("platform enrollment: decode trusted signer: %w", err)
		}
		key, err := hex.DecodeString(signer.PublicKey)
		if err != nil || !signer.Enabled || len(key) != ed25519.PublicKeySize {
			return "", constants.ErrConsensusAppMissingSignerKey
		}
	}

	var bindingVersion int64 = 1
	existingDoc, err := h.deps.DocStore.DocGet(ctx, marshaler.CollectionName(constants.CollectionConsensus), string(constants.DocIDConsensusBinding))
	if err == nil && existingDoc != nil {
		var prevPolicy models.ConsensusPolicy
		if err := existingDoc.DecodeData(&prevPolicy); err == nil && prevPolicy.BindingVersion > 0 {
			bindingVersion = prevPolicy.BindingVersion + 1
		}
	}

	now := time.Now().UTC()
	consensusDoc := models.ConsensusPolicy{
		ID:              string(constants.DocIDConsensusBinding),
		BindingVersion:  bindingVersion,
		AppID:           appID,
		MemberAppIDs:    append([]string(nil), spec.MemberAppIDs...),
		Quorum:          spec.Quorum,
		RequireDistinct: true,
		EndpointURL:     spec.EndpointURL,
		Provider:        string(spec.Provider),
		ModelTag:        spec.ModelTag,
		ModelDigest:     spec.ModelDigest,
		VoteKind:        spec.VoteKind, InferenceOperatorID: spec.InferenceOperatorID, VoteThresholds: spec.VoteThresholds, AllowHosted: spec.AllowHosted,
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	data, err := json.Marshal(consensusDoc)
	if err != nil {
		return "", fmt.Errorf("platform enrollment: marshal consensus binding: %w", err)
	}
	if err := h.deps.DocStore.DocSet(ctx, marshaler.CollectionName(constants.CollectionConsensus), string(constants.DocIDConsensusBinding), data); err != nil {
		return "", fmt.Errorf("platform enrollment: persist consensus binding: %w", err)
	}

	h.logger.Info("platform enrollment consensus bound",
		"provider", string(spec.Provider),
		"app_id", appID,
		"endpoint_url", spec.EndpointURL,
		"actor_user_id", actorUserID)
	if spec.Provider == constants.ConsensusProviderTribunal {
		return fmt.Sprintf("platform enrollment bind_consensus provider=%s app_id=%s quorum=%d actor=%s", spec.Provider, appID, spec.Quorum, actorUserID), nil
	}
	return fmt.Sprintf("platform enrollment bind_consensus provider=%s app_id=%s endpoint=%s actor=%s", spec.Provider, appID, spec.EndpointURL, actorUserID), nil
}

// HandleUnbindConsensus clears the Gateway consensus application binding.
func (h *PlatformEnrollmentHandler) HandleUnbindConsensus(ctx context.Context, msg *PubSubCommandMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("platform enrollment: unbind consensus context: %w", err)
	}
	payload, err := h.decodePayload(msg)
	if err != nil {
		return "", err
	}
	actorUserID := strings.TrimSpace(payload.GetActorUserId())
	if actorUserID == "" {
		return "", constants.ErrPlatformEnrollmentInvalidDecision
	}

	existingDoc, err := h.deps.DocStore.DocGet(ctx, marshaler.CollectionName(constants.CollectionConsensus), string(constants.DocIDConsensusBinding))
	if err != nil {
		return "", fmt.Errorf("platform enrollment: check existing consensus: %w", err)
	}
	if existingDoc == nil {
		return "", constants.ErrConsensusBindingNotFound
	}
	var existingPolicy models.ConsensusPolicy
	if err := existingDoc.DecodeData(&existingPolicy); err != nil {
		return "", fmt.Errorf("platform enrollment: decode existing consensus: %w", err)
	}
	if !existingPolicy.Enabled {
		return "", constants.ErrConsensusBindingNotFound
	}

	now := time.Now().UTC()
	existingPolicy.Enabled = false
	existingPolicy.BindingVersion = existingPolicy.BindingVersion + 1
	existingPolicy.UpdatedAt = now

	data, err := json.Marshal(existingPolicy)
	if err != nil {
		return "", fmt.Errorf("platform enrollment: marshal consensus unbind: %w", err)
	}
	if err := h.deps.DocStore.DocSet(ctx, marshaler.CollectionName(constants.CollectionConsensus), string(constants.DocIDConsensusBinding), data); err != nil {
		return "", fmt.Errorf("platform enrollment: persist consensus unbind: %w", err)
	}

	h.logger.Info("platform enrollment consensus unbound",
		"actor_user_id", actorUserID,
		"binding_version", existingPolicy.BindingVersion)
	return fmt.Sprintf("platform enrollment unbind_consensus app_id=%s version=%d actor=%s",
		existingPolicy.PrimaryAppID(), existingPolicy.BindingVersion, actorUserID), nil
}

// sessionExists reports whether a session document is already persisted.
func (h *PlatformEnrollmentHandler) sessionExists(ctx context.Context, collection constants.CollectionName, sessionID string) (bool, error) {
	doc, err := h.deps.DocStore.DocGet(ctx, marshaler.CollectionName(collection), sessionID)
	if err != nil {
		return false, fmt.Errorf("platform enrollment: check existing %s %s: %w", collection, sessionID, err)
	}
	return doc != nil, nil
}

// fingerprintsFromPayload converts the proto fingerprints message into
// the typed domain struct.
func fingerprintsFromPayload(fp *commonv1.PlatformEnrollmentFingerprints) models.PlatformEnrollmentCSRFingerprints {
	if fp == nil {
		return models.PlatformEnrollmentCSRFingerprints{}
	}
	return models.PlatformEnrollmentCSRFingerprints{
		App:        fp.GetApp(),
		Operator:   fp.GetOperator(),
		CLI:        fp.GetCli(),
		SigningKey: fp.GetSigningKey(),
	}
}

func fingerprintsSummary(f models.PlatformEnrollmentCSRFingerprints) string {
	parts := []string{}
	if f.App != "" {
		parts = append(parts, "app="+f.App)
	}
	if f.SigningKey != "" {
		parts = append(parts, "signing_key="+f.SigningKey)
	}
	if f.Operator != "" {
		parts = append(parts, "operator="+f.Operator)
	}
	if f.CLI != "" {
		parts = append(parts, "cli="+f.CLI)
	}
	return fmt.Sprintf("[%s]", strings.Join(parts, ","))
}

// fingerprintFromPEM computes the SHA-256 fingerprint of the DER-encoded
// certificate inside the PEM block. Returns an empty string on decode
// failure; callers treat an empty fingerprint as a non-fatal warning.
func fingerprintFromPEM(certPEM string) string {
	return auth.CertificateFingerprint(certPEM)
}

// serialFromPEM extracts the certificate serial number as a decimal
// string. Returns an empty string on decode failure.
func serialFromPEM(certPEM string) string {
	return auth.CertificateSerialNumber(certPEM)
}

func spiffeIDFromCertificate(certPEM string) (string, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return "", constants.ErrPlatformEnrollmentInvalidPayload
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(cert.URIs) == 0 {
		return "", constants.ErrPlatformEnrollmentInvalidPayload
	}
	return cert.URIs[0].String(), nil
}

// expiryFromPEM extracts the NotAfter timestamp from the PEM-encoded
// certificate. Returns the zero time on decode failure.
func expiryFromPEM(certPEM string) time.Time {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return time.Time{}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}
	}
	return cert.NotAfter
}
