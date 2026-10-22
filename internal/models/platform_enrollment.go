// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/uuid"
)

// platformAppNamePattern is the application name format. The Gateway and the
// CLI both enforce it through ValidateAppName.
var platformAppNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidateAppName is the single application name rule: non-empty, at most
// constants.PlatformEnrollmentMaxAppNameBytes bytes, matching the lowercase
// name format, and not a reserved platform or operator identity. A reserved
// name fails with both ErrPlatformEnrollmentInvalidAppName and
// ErrPlatformEnrollmentReservedIdentity.
func ValidateAppName(name string) error {
	if name == "" {
		return constants.ErrPlatformEnrollmentAppNameRequired
	}
	if len(name) > constants.PlatformEnrollmentMaxAppNameBytes || !platformAppNamePattern.MatchString(name) {
		return constants.ErrPlatformEnrollmentInvalidAppName
	}
	if isReservedPlatformAppName(name) {
		return fmt.Errorf("%w: %w", constants.ErrPlatformEnrollmentInvalidAppName, constants.ErrPlatformEnrollmentReservedIdentity)
	}
	return nil
}

// isReservedPlatformAppName reports whether name collides with a canonical
// platform component identity or a reserved operator/CLI name. Those
// identities are issued only through the owner-approved enrollment protocol.
func isReservedPlatformAppName(name string) bool {
	switch name {
	case "ztd", "ztdg", PlatformOperatorName, "ztde", PlatformDashboardName,
		"ztd-gateway", "ztd-operator", "ztd-ensemble", "ztd-dashboard", "ztd-cli",
		"operator", "ensemble", "dashboard", "gateway", "cli", "admin", "system":
		return true
	default:
		return false
	}
}

type PlatformComponentKind string

type PlatformEnrollmentState string

type PlatformEnrollmentDecision string

const (
	PlatformComponentDashboard   PlatformComponentKind = "dashboard"
	PlatformComponentOperator    PlatformComponentKind = "operator"
	PlatformComponentApplication PlatformComponentKind = "application"

	PlatformEnrollmentStatePending   PlatformEnrollmentState = "pending"
	PlatformEnrollmentStateApproved  PlatformEnrollmentState = "approved"
	PlatformEnrollmentStateIssuing   PlatformEnrollmentState = "issuing"
	PlatformEnrollmentStateCompleted PlatformEnrollmentState = "completed"
	PlatformEnrollmentStateDenied    PlatformEnrollmentState = "denied"
	PlatformEnrollmentStateExpired   PlatformEnrollmentState = "expired"
	PlatformEnrollmentStateRevoked   PlatformEnrollmentState = "revoked"

	PlatformEnrollmentDecisionApprove PlatformEnrollmentDecision = "approve"
	PlatformEnrollmentDecisionDeny    PlatformEnrollmentDecision = "deny"

	PlatformDashboardName = "ztdd"
	PlatformOperatorName  = "ztdo"
)

func (k PlatformComponentKind) CanonicalName(appName ...string) (string, error) {
	switch k {
	case PlatformComponentDashboard:
		return PlatformDashboardName, nil
	case PlatformComponentOperator:
		return PlatformOperatorName, nil
	case PlatformComponentApplication:
		if len(appName) > 0 && appName[0] != "" {
			return appName[0], nil
		}
		return "", constants.ErrPlatformEnrollmentAppNameRequired
	default:
		return "", constants.ErrPlatformEnrollmentInvalidComponent
	}
}

func (s PlatformEnrollmentState) IsTerminal() bool {
	return s == PlatformEnrollmentStateCompleted || s == PlatformEnrollmentStateDenied || s == PlatformEnrollmentStateExpired || s == PlatformEnrollmentStateRevoked
}

// ExpiresWithRequest reports whether the request TTL ends this state. An
// issuing request is bounded by its issuance lease instead: only lease
// reconciliation moves it, so expiry never lands mid-issuance.
func (s PlatformEnrollmentState) ExpiresWithRequest() bool {
	return s == PlatformEnrollmentStatePending || s == PlatformEnrollmentStateApproved
}

// canTransitionTo is the enrollment lifecycle. Leaving issuing back to
// approved is the lease rollback; issuing to expired is a reclaimed lease
// whose request TTL has also passed.
func (s PlatformEnrollmentState) canTransitionTo(to PlatformEnrollmentState) bool {
	switch s {
	case PlatformEnrollmentStatePending:
		return to == PlatformEnrollmentStateApproved || to == PlatformEnrollmentStateDenied || to == PlatformEnrollmentStateExpired
	case PlatformEnrollmentStateApproved:
		return to == PlatformEnrollmentStateIssuing || to == PlatformEnrollmentStateExpired
	case PlatformEnrollmentStateIssuing:
		return to == PlatformEnrollmentStateCompleted || to == PlatformEnrollmentStateApproved || to == PlatformEnrollmentStateExpired
	case PlatformEnrollmentStateCompleted:
		return to == PlatformEnrollmentStateRevoked
	default:
		return false
	}
}

// PlatformEnrollmentTransition is one lifecycle write of a stored request. It
// applies only while the request is still in From. LeaseOwner is the issuance
// lease: the holder being acquired when To is issuing, and the holder the
// write is fenced on when From is issuing. Fields are the typed fields the
// transition stamps beside the state, encoded as a JSON object.
type PlatformEnrollmentTransition struct {
	RequestID      string
	From           PlatformEnrollmentState
	To             PlatformEnrollmentState
	LeaseOwner     string
	LeaseExpiresAt time.Time
	Fields         json.RawMessage
}

// Validate rejects an edge outside the lifecycle and an issuance write that
// does not name its lease.
func (t PlatformEnrollmentTransition) Validate() error {
	if t.RequestID == "" {
		return constants.ErrPlatformEnrollmentRequestIDRequired
	}
	if !t.From.canTransitionTo(t.To) {
		return fmt.Errorf("%w: %s -> %s", constants.ErrPlatformEnrollmentInvalidState, t.From, t.To)
	}
	leased := t.From == PlatformEnrollmentStateIssuing || t.To == PlatformEnrollmentStateIssuing
	if leased != (t.LeaseOwner != "") {
		return fmt.Errorf("%w: %s -> %s lease owner", constants.ErrPlatformEnrollmentInvalidState, t.From, t.To)
	}
	return nil
}

// PlatformEnrollmentCreateRequest is the body of a platform enrollment request.
// DeploymentID is the launch identifier `operator deploy` assigns to a worker;
// it is never persisted and only keys the pending-request event to the CLI
// that launched the worker.
type PlatformEnrollmentCreateRequest struct {
	TokenHash         string                      `json:"token_hash" binding:"required"` // SHA-256 hex of the requester-generated token.
	ComponentKind     PlatformComponentKind       `json:"component_kind"`
	AppName           string                      `json:"app_name,omitempty"`
	InstanceID        string                      `json:"instance_id"`
	Hostname          string                      `json:"hostname"`
	SystemFingerprint string                      `json:"system_fingerprint,omitempty"`
	DeploymentID      string                      `json:"deployment_id,omitempty"`
	App               *PlatformAppCSRPayload      `json:"app,omitempty"`
	Operator          *PlatformOperatorCSRPayload `json:"operator,omitempty"`
}

func (r PlatformEnrollmentCreateRequest) ValidateShape() error {
	if r.TokenHash == "" {
		return constants.ErrPlatformEnrollmentTokenRequired
	}
	if len(r.TokenHash) != sha256.Size*2 || strings.ToLower(r.TokenHash) != r.TokenHash {
		return constants.ErrPlatformEnrollmentInvalidToken
	}
	if _, err := hex.DecodeString(r.TokenHash); err != nil {
		return constants.ErrPlatformEnrollmentInvalidToken
	}
	if _, err := r.ComponentKind.CanonicalName(r.AppName); err != nil {
		return err
	}
	if r.DeploymentID != "" {
		if _, err := uuid.Parse(r.DeploymentID); err != nil {
			return constants.ErrPlatformEnrollmentInvalidDeploymentID
		}
	}
	if r.InstanceID == "" {
		return constants.ErrPlatformEnrollmentInstanceIDRequired
	}
	if r.Hostname == "" {
		return constants.ErrPlatformEnrollmentHostnameRequired
	}
	if len(r.InstanceID) > constants.PlatformEnrollmentMaxInstanceIDBytes {
		return constants.ErrPlatformEnrollmentInvalidInstanceID
	}
	if len(r.Hostname) > constants.PlatformEnrollmentMaxHostnameBytes {
		return constants.ErrPlatformEnrollmentInvalidHostname
	}
	if r.ComponentKind == PlatformComponentApplication {
		if err := ValidateAppName(r.AppName); err != nil {
			return err
		}
	}
	if r.ComponentKind == PlatformComponentOperator {
		if r.App != nil || r.Operator == nil || r.Operator.OperatorCSRPEM == "" || r.Operator.CLICSRPEM == "" {
			return constants.ErrPlatformEnrollmentInvalidPayload
		}
		if r.SystemFingerprint == "" {
			return constants.ErrPlatformEnrollmentFingerprintRequired
		}
		return nil
	}
	if r.App == nil || r.Operator != nil || r.App.CSRPEM == "" {
		return constants.ErrPlatformEnrollmentInvalidPayload
	}
	return nil
}

// NewPlatformEnrollmentToken creates the requester's opaque recovery token.
// Callers must persist it before submitting an enrollment request.
func NewPlatformEnrollmentToken() (string, error) {
	value := make([]byte, constants.PlatformEnrollmentTokenBytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("platform enrollment: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// PlatformEnrollmentTokenHash returns the lowercase SHA-256 hex digest of token.
func PlatformEnrollmentTokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

type PlatformAppCSRPayload struct {
	CSRPEM string `json:"csr_pem"`
	// SigningPublicKey is an optional hex Ed25519 L2 key, independent of the
	// P-256 transport CSR. Completion proves possession of both keys.
	SigningPublicKey string `json:"signing_public_key,omitempty"`
}

// SigningKey decodes the optional L2 public key without changing transport PKI.
func (p *PlatformAppCSRPayload) SigningKey() (ed25519.PublicKey, error) {
	if p.SigningPublicKey == "" {
		return nil, nil
	}
	key, err := hex.DecodeString(p.SigningPublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, constants.ErrPlatformEnrollmentSigningKeyInvalid
	}
	return ed25519.PublicKey(key), nil
}

type PlatformOperatorCSRPayload struct {
	OperatorCSRPEM string `json:"operator_csr_pem"`
	CLICSRPEM      string `json:"cli_csr_pem"`
	// ActuatorPublicKey is the hex Ed25519 public key of this Operator's L5 Actuator.
	ActuatorPublicKey string `json:"actuator_public_key"`
}

// ActuatorKey decodes the presented Actuator public key. It fails with
// constants.ErrOperatorActuatorKeyInvalid unless the key is exactly
// ed25519.PublicKeySize bytes of hex.
func (p *PlatformOperatorCSRPayload) ActuatorKey() (ed25519.PublicKey, error) {
	return DecodeActuatorPublicKey(p.ActuatorPublicKey)
}

// DecodeActuatorPublicKey decodes a hex Ed25519 Actuator public key. Anything
// other than exactly ed25519.PublicKeySize bytes of hex is
// constants.ErrOperatorActuatorKeyInvalid.
func DecodeActuatorPublicKey(keyHex string) (ed25519.PublicKey, error) {
	pub, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", constants.ErrOperatorActuatorKeyInvalid, err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: public key size %d", constants.ErrOperatorActuatorKeyInvalid, len(pub))
	}
	return ed25519.PublicKey(pub), nil
}

type PlatformEnrollmentCSRFingerprints struct {
	App        string `json:"app,omitempty"`
	Operator   string `json:"operator,omitempty"`
	CLI        string `json:"cli,omitempty"`
	SigningKey string `json:"signing_key,omitempty"`
}

type PlatformEnrollmentCreateResponse struct {
	RequestID     string                            `json:"request_id"`
	ComponentKind PlatformComponentKind             `json:"component_kind"`
	ComponentName string                            `json:"component_name"`
	Fingerprints  PlatformEnrollmentCSRFingerprints `json:"fingerprints"`
	ApprovalURL   string                            `json:"approval_url"`
	ExpiresAt     time.Time                         `json:"expires_at"`
}

type PlatformEnrollmentStatusResponse struct {
	RequestID     string                  `json:"request_id"`
	ComponentKind PlatformComponentKind   `json:"component_kind"`
	State         PlatformEnrollmentState `json:"state"`
	ExpiresAt     time.Time               `json:"expires_at"`
	RetryAfter    int                     `json:"retry_after_seconds,omitempty"`
	FailureReason string                  `json:"failure_reason,omitempty"`
}

type PlatformEnrollmentPendingRequest struct {
	RequestID         string                            `json:"request_id"`
	ComponentKind     PlatformComponentKind             `json:"component_kind"`
	ComponentName     string                            `json:"component_name"`
	InstanceID        string                            `json:"instance_id"`
	Hostname          string                            `json:"hostname"`
	SystemFingerprint string                            `json:"system_fingerprint,omitempty"`
	Fingerprints      PlatformEnrollmentCSRFingerprints `json:"fingerprints"`
	State             PlatformEnrollmentState           `json:"state"`
	CreatedAt         time.Time                         `json:"created_at"`
	ExpiresAt         time.Time                         `json:"expires_at"`
}

type PlatformEnrollmentPendingResponse struct {
	Requests []PlatformEnrollmentPendingRequest `json:"requests"`
}

type PlatformEnrollmentEnrolledRequest struct {
	RequestID         string                  `json:"request_id"`
	ComponentKind     PlatformComponentKind   `json:"component_kind"`
	ComponentName     string                  `json:"component_name"`
	InstanceID        string                  `json:"instance_id"`
	Hostname          string                  `json:"hostname"`
	State             PlatformEnrollmentState `json:"state"`
	OperatorID        string                  `json:"operator_id,omitempty"`
	OperatorSessionID string                  `json:"operator_session_id,omitempty"`
	CLISessionID      string                  `json:"cli_session_id,omitempty"`
	PolicyID          string                  `json:"policy_id,omitempty"`
	CompletedAt       *time.Time              `json:"completed_at,omitempty"`
	RevokedAt         *time.Time              `json:"revoked_at,omitempty"`
}

type PlatformEnrollmentEnrolledResponse struct {
	Enrollments []PlatformEnrollmentEnrolledRequest `json:"enrollments"`
}

type PlatformEnrollmentDecisionRequest struct {
	RequestID string                     `json:"request_id"`
	Decision  PlatformEnrollmentDecision `json:"decision"`
	Reason    string                     `json:"reason,omitempty"`
}

func (r PlatformEnrollmentDecisionRequest) Validate() error {
	if r.RequestID == "" {
		return constants.ErrPlatformEnrollmentRequestIDRequired
	}
	if r.Decision != PlatformEnrollmentDecisionApprove && r.Decision != PlatformEnrollmentDecisionDeny {
		return constants.ErrPlatformEnrollmentInvalidDecision
	}
	if len(r.Reason) > constants.PlatformEnrollmentMaxReasonBytes {
		return constants.ErrPlatformEnrollmentReasonTooLong
	}
	return nil
}

// PlatformEnrollmentDecisionTarget binds an owner decision to the exact keys
// displayed in the pending snapshot, including when enrollment is batched.
type PlatformEnrollmentDecisionTarget struct {
	RequestID    string                            `json:"request_id"`
	Fingerprints PlatformEnrollmentCSRFingerprints `json:"fingerprints"`
}

type PlatformEnrollmentBatchDecisionRequest struct {
	Requests []PlatformEnrollmentDecisionTarget `json:"requests"`
	Decision PlatformEnrollmentDecision         `json:"decision"`
	Reason   string                             `json:"reason,omitempty"`
}

func (r PlatformEnrollmentBatchDecisionRequest) Validate() error {
	if len(r.Requests) == 0 || len(r.Requests) > constants.PlatformEnrollmentMaxDecisionBatch {
		return constants.ErrPlatformEnrollmentInvalidDecision
	}
	seen := make(map[string]struct{}, len(r.Requests))
	for _, target := range r.Requests {
		if err := (PlatformEnrollmentDecisionRequest{RequestID: target.RequestID, Decision: r.Decision, Reason: r.Reason}).Validate(); err != nil {
			return err
		}
		if _, exists := seen[target.RequestID]; exists {
			return constants.ErrPlatformEnrollmentInvalidDecision
		}
		seen[target.RequestID] = struct{}{}
	}
	return nil
}

type PlatformEnrollmentBatchDecisionResponse struct {
	Requests       []PlatformEnrollmentDecisionResponse `json:"requests"`
	EventReceiptID string                               `json:"event_receipt_id"`
}

type PlatformEnrollmentDecisionResponse struct {
	RequestID string                  `json:"request_id"`
	State     PlatformEnrollmentState `json:"state"`
}

type PlatformEnrollmentRevokeRequest struct {
	RequestID string `json:"request_id"`
	Reason    string `json:"reason,omitempty"`
}

func (r PlatformEnrollmentRevokeRequest) Validate() error {
	if r.RequestID == "" {
		return constants.ErrPlatformEnrollmentRequestIDRequired
	}
	if len(r.Reason) > constants.PlatformEnrollmentMaxReasonBytes {
		return constants.ErrPlatformEnrollmentReasonTooLong
	}
	return nil
}

type PlatformEnrollmentRevokeResponse struct {
	RequestID     string                  `json:"request_id"`
	ComponentKind PlatformComponentKind   `json:"component_kind"`
	State         PlatformEnrollmentState `json:"state"`
}

const PlatformEnrollmentStatusL3Required = "l3_required"

type PlatformEnrollmentL3SuspensionResponse struct {
	Status      string            `json:"status"`
	Posture     constants.Posture `json:"posture"`
	TxHash      string            `json:"tx_hash"`
	ApprovalURL string            `json:"approval_url"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Message     string            `json:"message"`
}

type PlatformEnrollmentL3RequiredError struct {
	Suspension PlatformEnrollmentL3SuspensionResponse
}

func (e *PlatformEnrollmentL3RequiredError) Error() string {
	if e == nil {
		return constants.ErrPlatformEnrollmentL3Required.Error()
	}
	if e.Suspension.Message != "" {
		return fmt.Sprintf("%s: %s", constants.ErrPlatformEnrollmentL3Required.Error(), e.Suspension.Message)
	}
	return constants.ErrPlatformEnrollmentL3Required.Error()
}

func (e *PlatformEnrollmentL3RequiredError) Unwrap() error {
	return constants.ErrPlatformEnrollmentL3Required
}

type PlatformEnrollmentProofs struct {
	App        string `json:"app,omitempty"`
	Operator   string `json:"operator,omitempty"`
	CLI        string `json:"cli,omitempty"`
	SigningKey string `json:"signing_key,omitempty"`
}

type PlatformEnrollmentCompleteRequest struct {
	Token  string                   `json:"token"`
	Proofs PlatformEnrollmentProofs `json:"proofs"`
}

type PlatformEnrollmentAppCredentials struct {
	AppID       string    `json:"app_id"`
	AppCert     string    `json:"app_cert"`
	CertChain   string    `json:"cert_chain"`
	TrustBundle string    `json:"trust_bundle"`
	ExpiresAt   time.Time `json:"expires_at"`
	PolicyID    string    `json:"policy_id"`
}

type PlatformEnrollmentOperatorCredentials struct {
	OperatorCert      string            `json:"operator_cert"`
	OperatorCertChain string            `json:"operator_cert_chain"`
	HubTrustBundle    string            `json:"hub_trust_bundle"`
	OperatorSessionID string            `json:"operator_session_id"`
	OperatorID        string            `json:"operator_id"`
	CLISessionID      string            `json:"cli_session_id"`
	CLICert           string            `json:"cli_cert"`
	CLICertChain      string            `json:"cli_cert_chain"`
	Posture           constants.Posture `json:"posture,omitempty"`
}

type PlatformEnrollmentCompleteResponse struct {
	RequestID     string                                 `json:"request_id"`
	ComponentKind PlatformComponentKind                  `json:"component_kind"`
	App           *PlatformEnrollmentAppCredentials      `json:"app,omitempty"`
	Operator      *PlatformEnrollmentOperatorCredentials `json:"operator,omitempty"`
}

type PlatformEnrollmentRequest struct {
	ID                       string                              `json:"request_id"`
	TokenHash                string                              `json:"token_hash"`
	ComponentKind            PlatformComponentKind               `json:"component_kind"`
	ComponentName            string                              `json:"component_name"`
	AppName                  string                              `json:"app_name,omitempty"`
	InstanceID               string                              `json:"instance_id"`
	Hostname                 string                              `json:"hostname"`
	SystemFingerprint        string                              `json:"system_fingerprint,omitempty"`
	App                      *PlatformAppCSRPayload              `json:"app,omitempty"`
	Operator                 *PlatformOperatorCSRPayload         `json:"operator,omitempty"`
	Fingerprints             PlatformEnrollmentCSRFingerprints   `json:"fingerprints"`
	State                    PlatformEnrollmentState             `json:"state"`
	AttemptCount             int                                 `json:"attempt_count"`
	CreatedAt                time.Time                           `json:"created_at"`
	ExpiresAt                time.Time                           `json:"expires_at"`
	LastTransitionAt         time.Time                           `json:"last_transition_at"`
	ApprovedByUserID         string                              `json:"approved_by_user_id,omitempty"`
	DecidedAt                *time.Time                          `json:"decided_at,omitempty"`
	DecisionReason           string                              `json:"decision_reason,omitempty"`
	DecisionEnvelopeID       string                              `json:"decision_envelope_id,omitempty"`
	DecisionEventReceiptID   string                              `json:"decision_event_receipt_id,omitempty"`
	IssuanceEnvelopeID       string                              `json:"issuance_envelope_id,omitempty"`
	IssuanceEventReceiptID   string                              `json:"issuance_event_receipt_id,omitempty"`
	IssuanceLeaseOwner       string                              `json:"issuance_lease_owner,omitempty"`
	IssuanceLeaseExpiresAt   *time.Time                          `json:"issuance_lease_expires_at,omitempty"`
	OperatorID               string                              `json:"operator_id,omitempty"`
	OperatorSessionID        string                              `json:"operator_session_id,omitempty"`
	CLISessionID             string                              `json:"cli_session_id,omitempty"`
	PolicyID                 string                              `json:"policy_id,omitempty"`
	CertificateSerial        string                              `json:"certificate_serial,omitempty"`
	CertificateFingerprint   string                              `json:"certificate_fingerprint,omitempty"`
	Issued                   *PlatformEnrollmentCompleteResponse `json:"issued,omitempty"`
	CompletedAt              *time.Time                          `json:"completed_at,omitempty"`
	RevokedAt                *time.Time                          `json:"revoked_at,omitempty"`
	RevokedByUserID          string                              `json:"revoked_by_user_id,omitempty"`
	RevocationReason         string                              `json:"revocation_reason,omitempty"`
	RevocationEnvelopeID     string                              `json:"revocation_envelope_id,omitempty"`
	RevocationEventReceiptID string                              `json:"revocation_event_receipt_id,omitempty"`
	FailureReason            string                              `json:"failure_reason,omitempty"`
}

func (r PlatformEnrollmentRequest) PendingMetadata() PlatformEnrollmentPendingRequest {
	return PlatformEnrollmentPendingRequest{
		RequestID:         r.ID,
		ComponentKind:     r.ComponentKind,
		ComponentName:     r.ComponentName,
		InstanceID:        r.InstanceID,
		Hostname:          r.Hostname,
		SystemFingerprint: r.SystemFingerprint,
		Fingerprints:      r.Fingerprints,
		State:             r.State,
		CreatedAt:         r.CreatedAt,
		ExpiresAt:         r.ExpiresAt,
	}
}

func (r PlatformEnrollmentRequest) EnrolledMetadata() PlatformEnrollmentEnrolledRequest {
	return PlatformEnrollmentEnrolledRequest{
		RequestID:         r.ID,
		ComponentKind:     r.ComponentKind,
		ComponentName:     r.ComponentName,
		InstanceID:        r.InstanceID,
		Hostname:          r.Hostname,
		State:             r.State,
		OperatorID:        r.OperatorID,
		OperatorSessionID: r.OperatorSessionID,
		CLISessionID:      r.CLISessionID,
		PolicyID:          r.PolicyID,
		CompletedAt:       r.CompletedAt,
		RevokedAt:         r.RevokedAt,
	}
}

func (r PlatformEnrollmentRequest) ValidateStoredIdentity() error {
	name, err := r.ComponentKind.CanonicalName(r.ComponentName)
	if err != nil {
		return err
	}
	if r.ComponentName != name {
		return fmt.Errorf("%w: component name", constants.ErrPlatformEnrollmentStoredRequestInvalid)
	}
	return nil
}
