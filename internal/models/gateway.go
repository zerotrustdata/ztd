// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/timesvc"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Document is the internal representation of a stored document.
// Timestamps are native time.Time - convert to wire format via ForWire().
type Document struct {
	ID         string
	Collection string
	Data       map[string]json.RawMessage
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// DecodeData decodes the document's Data fields into target, a typed struct.
// Data is a field map, not raw JSON, so it is re-serialized once before decoding.
func (d *Document) DecodeData(target any) error {
	b, err := json.Marshal(d.Data)
	if err != nil {
		return fmt.Errorf("marshal document data: %w", err)
	}
	if err := json.Unmarshal(b, target); err != nil {
		return fmt.Errorf("unmarshal document data: %w", err)
	}
	return nil
}

// ForWire serializes Document to a JSON-encodable map for the HTTP response boundary.
// Timestamps are formatted as fixed-microsecond UTC strings. The caller's data fields are
// merged with system fields (id, created_at, updated_at).
func (d *Document) ForWire() map[string]json.RawMessage {
	// The +3 is for system fields: id, created_at, updated_at.
	const systemFields = 3
	const maxTotalCapacity = 1000000

	allocSize := maxTotalCapacity
	if len(d.Data) < maxTotalCapacity-systemFields {
		allocSize = len(d.Data) + systemFields
	}

	out := make(map[string]json.RawMessage, allocSize)
	for k, v := range d.Data {
		out[k] = v
	}
	out["id"], _ = json.Marshal(d.ID)
	out["created_at"], _ = json.Marshal(timesvc.FormatTimestamp(d.CreatedAt))
	out["updated_at"], _ = json.Marshal(timesvc.FormatTimestamp(d.UpdatedAt))
	return out
}

// DocFilter represents a single filter condition for DocQuery.
// Op must be one of: ==, !=, <, >, <=, >=
type DocFilter struct {
	Field string          `json:"field"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value"`
}

// DocQueryRequest is the typed body for POST /db/{collection}/_query.
type DocQueryRequest struct {
	Filters []DocFilter `json:"filters,omitempty"`
	OrderBy string      `json:"order_by,omitempty"`
	Limit   int         `json:"limit,omitempty"`
}

// PubSubPublishRequest is the typed body for POST /pubsub/publish.
type PubSubPublishRequest struct {
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

// HealthResponse is the typed response for GET /health.
type HealthResponse struct {
	Status          constants.GatewayMode `json:"status"`
	Mode            constants.GatewayMode `json:"mode"`
	Version         string                `json:"version"`
	PID             int                   `json:"pid"`
	GovernanceReady bool                  `json:"governance_ready"`
	Posture         constants.Posture     `json:"posture"`
	A2AIngress      bool                  `json:"a2a_ingress"` // A2A ingress is mounted (a downstream is configured)
	StateMerkleRoot string                `json:"state_merkle_root,omitempty"`
	Consensus       HealthConsensus       `json:"consensus"`
}

// HealthConsensus is the unauthenticated /health consensus summary. It carries
// counts only: never member IDs, the pinned model, or an endpoint.
type HealthConsensus struct {
	VoteKind string `json:"vote_kind,omitempty"`
	Bound    bool   `json:"bound"`              // an enabled binding exists
	Provider string `json:"provider,omitempty"` // constants.ConsensusProvider of the binding
	Quorum   int    `json:"quorum,omitempty"`   // K
	Members  int    `json:"members,omitempty"`  // N
	Usable   int    `json:"usable"`             // members that can vote now (enabled signer; tribunal: local key present)
	Ready    bool   `json:"ready"`              // Usable >= Quorum on an enabled binding
}

// Describe renders the consensus summary for a status line. A bound binding
// reports provider, quorum, and usable signers; an unbound one names the L2
// requirement the posture imposes, so operators know binding is mandatory.
func (c HealthConsensus) Describe(posture constants.Posture) string {
	if c.Bound {
		line := fmt.Sprintf("%s %d-of-%d | signers %d/%d", c.Provider, c.Quorum, c.Members, c.Usable, c.Members)
		if c.VoteKind == ConsensusVoteKindDecision {
			line += " | decision"
		}
		if !c.Ready {
			line += " | NOT READY"
		}
		return line
	}
	if posture == "" {
		return "not bound (gateway did not report posture)"
	}
	if posture.RequiresL2() {
		return fmt.Sprintf("NOT BOUND — posture %s requires L2: ztd consensus enroll tribunal --model <tag>", posture)
	}
	return fmt.Sprintf("not bound (posture %s does not require L2)", posture)
}

// ConsensusReadiness is the single readiness view of the bound L2 consensus,
// shared by /health, gw status, ztd consensus status, and the startup log.
type ConsensusReadiness struct {
	VoteKind            string
	InferenceOperatorID string
	Bound               bool
	Provider            constants.ConsensusProvider
	Quorum              int
	ModelTag            string
	ModelDigest         string
	Members             []ConsensusMemberReadiness
	// Usable counts members with an enabled trusted signer and, under the
	// tribunal provider, a sealed signing key present on this host.
	Usable int
	// Ready is Bound && Usable >= Quorum.
	Ready bool
}

// ConsensusMemberReadiness is one member's readiness.
type ConsensusMemberReadiness struct {
	AppID         string
	SignerEnabled bool
	// LocalKey is meaningful only for the tribunal provider: the member's
	// sealed app signing key is present and unseals on this host.
	LocalKey bool
}

// Summary is the unauthenticated /health projection of r.
func (r ConsensusReadiness) Summary() HealthConsensus {
	if !r.Bound {
		return HealthConsensus{}
	}
	return HealthConsensus{
		Bound:    true,
		Provider: string(r.Provider),
		VoteKind: r.VoteKind,
		Quorum:   r.Quorum,
		Members:  len(r.Members),
		Usable:   r.Usable,
		Ready:    r.Ready,
	}
}

// StateResponse is the typed response for GET /state.
type StateResponse struct {
	StateMerkleRoot string `json:"state_merkle_root"`
}

// StatusResponse is the typed response for simple ok/error replies.
type StatusResponse struct {
	Status constants.GatewayMode `json:"status"`
}

// PubSubPublishResponse is the typed response for POST /pubsub/publish.
type PubSubPublishResponse struct {
	Receivers int `json:"receivers"`
}

// EventReceiptRecord is the searchable projection of a transaction's current
// signed EventReceipt, with runtime identity and query metadata.
type EventReceiptRecord struct {
	TransactionID     string                     `json:"transaction_id"`
	TransactionHash   string                     `json:"transaction_hash"`
	InvestigationID   string                     `json:"investigation_id"`
	OperatorID        string                     `json:"operator_id"`
	OperatorSessionID string                     `json:"operator_session_id"`
	RequestorUserID   string                     `json:"requestor_user_id"`
	ActingAppID       string                     `json:"acting_app_id"`
	EventType         constants.EventType        `json:"event_type"`
	ActionType        constants.ActionType       `json:"action_type"`
	TargetResource    string                     `json:"target_resource"`
	Status            operatorv1.ExecutionStatus `json:"status"`
	ResultSummary     string                     `json:"result_summary"`
	StateRootBefore   string                     `json:"state_root_before"`
	StateRootAfter    string                     `json:"state_root_after"`
	ExecutedAt        time.Time                  `json:"executed_at"`
	SignerKeyID       string                     `json:"signer_key_id"`
	Signature         string                     `json:"signature"`
	L2Valid           bool                       `json:"l2_valid"`
	L3Valid           bool                       `json:"l3_valid"`
	Timestamp         time.Time                  `json:"timestamp"`
	EventReceipt      *operatorv1.EventReceipt   `json:"event_receipt,omitempty"`
}

// BlobMetaResponse is the typed response for GET /blob/{namespace}/{id}/meta.
type BlobMetaResponse struct {
	ID          string    `json:"id"`
	Namespace   string    `json:"namespace"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	CreatedAt   time.Time `json:"created_at"`
}

// BlobDeleteResponse is the typed response for DELETE /blob/{namespace}/{id} and DELETE /blob/{namespace}.
type BlobDeleteResponse struct {
	Deleted int64 `json:"deleted"`
}

// SSEPushPayload is the wire envelope for SSE push events. UserID is always
// required (ownership). Exactly one of WebSessionID or CliSessionID must be
// set as the delivery target. Event carries the typed event JSON. This is the
// shared typed model for the wire shape produced by the gateway and consumed
// by CLI clients.
type SSEPushPayload struct {
	UserID       string          `json:"user_id"`
	WebSessionID string          `json:"web_session_id"`
	CliSessionID string          `json:"cli_session_id"`
	Event        json.RawMessage `json:"event"`
}

// SSEEventRow is a single row from the sse_events table. UserID is always
// populated. Exactly one of WebSessionID or CLISessionID will be populated.
type SSEEventRow struct {
	ID           int64  `json:"id"`
	UserID       string `json:"user_id"`
	WebSessionID string `json:"web_session_id,omitempty"`
	CLISessionID string `json:"cli_session_id,omitempty"`
	EventType    string `json:"event_type"`
	Payload      string `json:"payload"`
	CreatedAt    string `json:"created_at"`
}

// SSEPublishedEvent is the internal pub/sub envelope for live SSE events.
// It pairs the persisted DB row ID with the raw event payload JSON so that
// the stream handler can deduplicate against replayed rows and emit an `id:`
// field on the live path (fixing the duplicate-delivery race and the
// missing Last-Event-ID cursor on reconnect).
type SSEPublishedEvent struct {
	ID      int64           `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

// SSEErrorEvent is the sentinel payload emitted on the SSE stream when a
// replay query fails, signaling a gap to the client so it can react rather
// than silently missing backlog.
type SSEErrorEvent struct {
	Type   string `json:"type"`
	Reason string `json:"reason"`
}

// SSETruncationEvent is the sentinel payload emitted on the SSE stream when
// a replay hits the row limit, signaling that more backlog may exist beyond
// the returned window.
type SSETruncationEvent struct {
	Type    string `json:"type"`
	SinceID int64  `json:"since_id"`
	Limit   int    `json:"limit"`
}

// SSEEventsResponse is the typed response for GET /api/v1/sse/events.
type SSEEventsResponse struct {
	Events []SSEEventRow `json:"events"`
	Count  int           `json:"count"`
}

// TrustedSignersResponse is the typed response for GET /api/governance/signers.
type TrustedSignersResponse struct {
	Success bool            `json:"success"`
	Signers []TrustedSigner `json:"signers"`
}

// PasskeyChallengeResponse is the typed response for passkey challenge endpoints.
type PasskeyChallengeResponse struct {
	Success    bool                          `json:"success"`
	Options    *protocol.CredentialAssertion `json:"options,omitempty"`
	NeedsSetup bool                          `json:"needs_setup,omitempty"`
	Error      string                        `json:"error,omitempty"`
}

// PasskeyVerifyResponse is the typed response for passkey verify endpoints.
type PasskeyVerifyResponse struct {
	Success      bool               `json:"success"`
	Credential   *PasskeyCredential `json:"credential,omitempty"`
	UserID       string             `json:"user_id,omitempty"`
	WebSessionID string             `json:"web_session_id,omitempty"`
	Error        string             `json:"error,omitempty"`
}

// PasskeyCredentialsResponse is the typed response for GET /api/auth/passkey/credentials.
type PasskeyCredentialsResponse struct {
	Success     bool                `json:"success"`
	Credentials []PasskeyCredential `json:"credentials"`
}

// PasskeyRevokeResponse is the typed response for DELETE /api/auth/passkey/credentials/{id}.
type PasskeyRevokeResponse struct {
	Success   bool `json:"success"`
	Found     bool `json:"found"`
	Remaining int  `json:"remaining"`
}

// AuthLoginChallengeResponse is the typed response for POST /api/auth/login/challenge.
type AuthLoginChallengeResponse struct {
	Success bool                          `json:"success"`
	UserID  string                        `json:"user_id,omitempty"`
	Options *protocol.CredentialAssertion `json:"options,omitempty"`
}

// BootstrapStatusResponse is the typed response for GET /api/v1/auth/bootstrap/status.
//
// The gateway is bootstrapped when it has been started and at least one owner
// user exists (HasAnyUsers). This is the decision signal the CLI coordinator
// uses to choose between bootstrap (first `auth enroll user` creates the first
// real user) and recovery (human-approved flow).
type BootstrapStatusResponse struct {
	Bootstrapped bool `json:"bootstrapped"`
}

// UserMeResponse is the typed response for GET /api/user/me.
type UserMeResponse struct {
	Success bool  `json:"success"`
	User    *User `json:"user"`
}

// WebSessionResponse is the typed response for GET /api/auth/web-session.
type WebSessionResponse struct {
	Success      bool   `json:"success"`
	UserID       string `json:"user_id"`
	WebSessionID string `json:"web_session_id"`
}

// PasskeyRegisterChallengeResponse is the typed response for POST /api/auth/passkeys/register/challenge.
type PasskeyRegisterChallengeResponse struct {
	Success bool                         `json:"success"`
	UserID  string                       `json:"user_id,omitempty"`
	Options *protocol.CredentialCreation `json:"options,omitempty"`
}

// PasskeyAuthVerifyResponse is the typed response for POST /api/auth/passkeys/authenticate/verify.
type PasskeyAuthVerifyResponse struct {
	Success    bool               `json:"success"`
	UserID     string             `json:"user_id,omitempty"`
	Credential *PasskeyCredential `json:"credential,omitempty"`
	Error      string             `json:"error,omitempty"`
	WebSession *WebSessionInfo    `json:"web_session,omitempty"`
}

// WebSessionInfo contains web session details.
type WebSessionInfo struct {
	ID              string `json:"id"`
	ExpiresAtUnixMs int64  `json:"expires_at_unix_ms"`
}

// UserCreateResponse is the typed response for POST /api/users.
type UserCreateResponse struct {
	Success bool   `json:"success"`
	UserID  string `json:"user_id"`
}

// SuspendedTransactionsResponse is the typed response for GET /api/approvals.
type SuspendedTransactionsResponse struct {
	Transactions []SuspendedTxResponse `json:"transactions"`
}

// SuspendedTxResponse represents a suspended transaction.
type SuspendedTxResponse struct {
	TransactionHash string    `json:"transaction_hash"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	ToolName        string    `json:"tool_name"`
	UserID          string    `json:"user_id"`
	OperatorID      string    `json:"operator_id"`
}

// ApprovalRespondRequest is the typed request for POST /api/v1/approvals/respond.
// It records a deny decision from a grounded session, or an explicit CLI
// approval signed over the pending transaction hash. Browser approvals use
// the WebAuthn verify action.
type ApprovalRespondRequest struct {
	ApprovalID   string `json:"approval_id"`
	Approved     bool   `json:"approved"`
	Reason       string `json:"reason,omitempty"`
	CLISignature string `json:"cli_signature,omitempty"`
}

// BootstrapResponse is the typed response for POST /api/auth/bootstrap.
type BootstrapResponse struct {
	Success           bool            `json:"success"`
	User              *User           `json:"user,omitempty"`
	UserID            string          `json:"user_id,omitempty"`
	WebSession        *WebSessionInfo `json:"web_session,omitempty"`
	OperatorSessionID string          `json:"operator_session_id,omitempty"`
	OperatorID        string          `json:"operator_id,omitempty"`
	CLISessionID      string          `json:"cli_session_id,omitempty"`
	CLICert           string          `json:"cli_cert,omitempty"`
	CLICertChain      string          `json:"cli_cert_chain,omitempty"`
	HubTrustBundle    string          `json:"hub_trust_bundle,omitempty"`
}

// CLIEnrollmentResponse is the typed response for POST /api/auth/cli/enroll.
type CLIEnrollmentResponse struct {
	Success           bool   `json:"success"`
	CLISessionID      string `json:"cli_session_id"`
	CLICert           string `json:"cli_cert"`
	CLICertChain      string `json:"cli_cert_chain"`
	HubTrustBundle    string `json:"hub_trust_bundle"`
	UserID            string `json:"user_id"`
	OperatorSessionID string `json:"operator_session_id,omitempty"`
	OperatorID        string `json:"operator_id,omitempty"`
}

// ActuatorPublicKeyExport is the typed JSON structure for exporting the Actuator's public key.
type ActuatorPublicKeyExport struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Algorithm string `json:"algorithm"`
}

// PKIFingerprintResponse is the typed response for GET /.well-known/ztd/pki/fingerprint.
type PKIFingerprintResponse struct {
	RootCA string `json:"root_ca"`
}

// PKICSRSignResponse is the typed response for POST /.well-known/ztd/pki/csr/sign.
type PKICSRSignResponse struct {
	CertificatePEM      string `json:"certificate_pem"`
	CertificateChainPEM string `json:"certificate_chain_pem"`
}

// PlatformSettings represents the typed settings within platform_settings.
// Authority: protocol/models/platform_settings.json
type PlatformSettings struct {
	ActuatorKeyID             string `json:"actuator_key_id"`
	OperatorSessionID         string `json:"operator_session_id,omitempty"`
	LLMCommandGenEnabled      bool   `json:"llm_command_gen_enabled,omitempty"`
	LLMCommandGenVerifier     bool   `json:"llm_command_gen_verifier,omitempty"`
	LLMCommandGenPasses       int    `json:"llm_command_gen_passes,omitempty"`
	GoogleSearchEnabled       bool   `json:"google_search_enabled,omitempty"`
	GoogleSearchAPIKey        string `json:"google_search_api_key,omitempty"`
	GoogleSearchEngineID      string `json:"google_search_engine_id,omitempty"`
	VertexSearchEnabled       bool   `json:"vertex_search_enabled,omitempty"`
	VertexSearchProjectID     string `json:"vertex_search_project_id,omitempty"`
	VertexSearchEngineID      string `json:"vertex_search_engine_id,omitempty"`
	VertexSearchLocation      string `json:"vertex_search_location,omitempty"`
	VertexSearchAPIKey        string `json:"vertex_search_api_key,omitempty"`
	EnableCommandWhitelisting bool   `json:"enable_command_whitelisting,omitempty"`
	EnableCommandBlacklisting bool   `json:"enable_command_blacklisting,omitempty"`
}

// SettingsDocument represents the platform_settings document structure.
// Authority: protocol/models/platform_settings.json
type SettingsDocument struct {
	Settings  *PlatformSettings `json:"settings"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}
