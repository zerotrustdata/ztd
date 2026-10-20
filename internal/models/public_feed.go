// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 0.

package models

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Public feed record type constants. These mirror the closed vocabulary in
// protocol/models/public_feed.json.
type PublicFeedRecordType string

const (
	PublicFeedRecordTypeProjection    PublicFeedRecordType = "projection"
	PublicFeedRecordTypeEvent         PublicFeedRecordType = "event"
	PublicFeedRecordTypeProofManifest PublicFeedRecordType = "proof_manifest"
	PublicFeedRecordTypeKeyRevocation PublicFeedRecordType = "key_revocation"
)

// PublicFeedOutboxStatus is the lifecycle state of an outbox entry.
type PublicFeedOutboxStatus string

const (
	PublicFeedOutboxStatusPending      PublicFeedOutboxStatus = "pending"
	PublicFeedOutboxStatusSent         PublicFeedOutboxStatus = "sent"
	PublicFeedOutboxStatusAcknowledged PublicFeedOutboxStatus = "acknowledged"
	PublicFeedOutboxStatusFailed       PublicFeedOutboxStatus = "failed"
)

// PublicFeedIngestRejectionReason is the typed rejection reason from the
// mirror when a batch is rejected.
type PublicFeedIngestRejectionReason string

const (
	PublicFeedIngestRejectionSignatureInvalid   PublicFeedIngestRejectionReason = "signature_invalid"
	PublicFeedIngestRejectionSequenceOutOfOrder PublicFeedIngestRejectionReason = "sequence_out_of_order"
	PublicFeedIngestRejectionHashChainMismatch  PublicFeedIngestRejectionReason = "hash_chain_mismatch"
	PublicFeedIngestRejectionDuplicateSequence  PublicFeedIngestRejectionReason = "duplicate_sequence"
	PublicFeedIngestRejectionEquivocation       PublicFeedIngestRejectionReason = "equivocation"
	PublicFeedIngestRejectionOversizedBatch     PublicFeedIngestRejectionReason = "oversized_batch"
	PublicFeedIngestRejectionRevokedKey         PublicFeedIngestRejectionReason = "revoked_key"
	PublicFeedIngestRejectionUnknownKey         PublicFeedIngestRejectionReason = "unknown_key"
)

// PublicFeedProofClassification is the disclosure classification for a proof
// artifact. Only public_safe artifacts appear in the public proof catalog.
type PublicFeedProofClassification string

const (
	PublicFeedProofClassificationPublicSafe PublicFeedProofClassification = "public_safe"
)

// PublicFeedRecord is one record in a signed batch. A record is a serialized
// projection or event payload with its SHA-256 content hash.
type PublicFeedRecord struct {
	Sequence    int64                `json:"sequence"`
	RecordType  PublicFeedRecordType `json:"record_type"`
	RecordHash  string               `json:"record_hash"`
	RecordBytes string               `json:"record_bytes"`
}

// PublicFeedBatch is a signed append-only batch binding source, protocol
// version, schema version, monotonic sequence range, prior-batch hash,
// record hashes, generated time, content hash, signing key ID, and Ed25519
// signature.
type PublicFeedBatch struct {
	ProtocolVersion   string             `json:"protocol_version"`
	SchemaVersion     string             `json:"schema_version"`
	SourceID          string             `json:"source_id"`
	FirstSequence     int64              `json:"first_sequence"`
	LastSequence      int64              `json:"last_sequence"`
	PreviousBatchHash string             `json:"previous_batch_hash"`
	RecordHashes      []string           `json:"record_hashes"`
	GeneratedAt       time.Time          `json:"generated_at"`
	ContentHash       string             `json:"content_hash"`
	SigningKeyID      string             `json:"signing_key_id"`
	Signature         string             `json:"signature"`
	Records           []PublicFeedRecord `json:"records"`
}

// PublicFeedSnapshot binds the high-water sequence and the feed-chain hash.
// WithdrawalDigest, set on every mirror read, is the SHA-256 over the mirror's
// sorted withdrawn dataset IDs (the zero hash when none): withdrawal hides
// records without rewriting the signed chain, so clients holding cached
// records discard them when the digest changes.
type PublicFeedSnapshot struct {
	ProtocolVersion   string            `json:"protocol_version"`
	SourceID          string            `json:"source_id"`
	HighWaterSequence int64             `json:"high_water_sequence"`
	FeedChainHash     string            `json:"feed_chain_hash"`
	BatchCount        int               `json:"batch_count"`
	WithdrawalDigest  string            `json:"withdrawal_digest,omitempty"`
	GeneratedAt       time.Time         `json:"generated_at"`
	Freshness         CampaignFreshness `json:"freshness"`
}

// PublicProofCatalogSummary is the summary of the proof catalog for bootstrap.
type PublicProofCatalogSummary struct {
	ArtifactCount   int        `json:"artifact_count"`
	TotalByteSize   int64      `json:"total_byte_size"`
	LastGeneratedAt *time.Time `json:"last_generated_at,omitempty"`
}

// PublicFeedObject is an open-ended public JSON object. Public projections
// intentionally admit schema-versioned fields owned by the evaluation publisher;
// RawMessage preserves those fields without converting the boundary to an
// untyped Go contract.
type PublicFeedObject map[string]json.RawMessage

// NewPublicFeedObject constructs an open-ended public object from string fields.
func NewPublicFeedObject(fields map[string]string) PublicFeedObject {
	object := make(PublicFeedObject, len(fields))
	for name, value := range fields {
		object[name] = json.RawMessage(strconv.Quote(value))
	}
	return object
}

// StringField returns a string field from the public object.
func (o PublicFeedObject) StringField(name string) (string, bool) {
	value, ok := o[name]
	if !ok {
		return "", false
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil {
		return "", false
	}
	return result, true
}

// SetInt64Field adds or replaces an integer field in the public object.
func (o PublicFeedObject) SetInt64Field(name string, value int64) {
	o[name] = json.RawMessage(strconv.FormatInt(value, 10))
}

// PublicFeedBootstrap is one bounded initial snapshot for public first paint.
type PublicFeedBootstrap struct {
	ProtocolVersion     string                    `json:"protocol_version"`
	Snapshot            PublicFeedSnapshot        `json:"snapshot"`
	SourceFreshness     CampaignFreshness         `json:"source_freshness"`
	RecentProjections   []PublicFeedObject        `json:"recent_projections"`
	ProofCatalogSummary PublicProofCatalogSummary `json:"proof_catalog_summary"`
	GeneratedAt         time.Time                 `json:"generated_at"`
}

// PublicFeedCursorPage is a cursor-paginated page of public records.
type PublicFeedCursorPage struct {
	ProtocolVersion string             `json:"protocol_version"`
	Items           []PublicFeedObject `json:"items"`
	Cursor          string             `json:"cursor,omitempty"`
	HasMore         bool               `json:"has_more"`
	Limit           int                `json:"limit"`
}

// PublicFeedExportBatchResponse is returned after the gateway accepts a
// host-CLI campaign publication batch via mTLS.
type PublicFeedExportBatchResponse struct {
	HighWaterSequence int64  `json:"high_water_sequence"`
	FeedChainHash     string `json:"feed_chain_hash"`
}

// PublicIngestRequest is the authenticated ingest request from the Gateway
// outbound publisher to the hosted mirror.
type PublicIngestRequest struct {
	Batch PublicFeedBatch `json:"batch"`
}

// PublicIngestResponse is the authenticated ingest response from the mirror.
type PublicIngestResponse struct {
	Accepted          bool                            `json:"accepted"`
	SourceID          string                          `json:"source_id,omitempty"`
	HighWaterSequence int64                           `json:"high_water_sequence,omitempty"`
	FeedChainHash     string                          `json:"feed_chain_hash,omitempty"`
	BatchCount        int                             `json:"batch_count,omitempty"`
	RejectionReason   PublicFeedIngestRejectionReason `json:"rejection_reason,omitempty"`
}

type PublicKeyRegistrationRequest struct {
	SourceID     string `json:"source_id"`
	CurrentKeyID string `json:"current_key_id"`
	NewKeyID     string `json:"new_key_id"`
	PublicKey    string `json:"public_key"`
	Signature    string `json:"signature"`
}

type PublicKeyRegistrationResponse struct {
	Accepted bool `json:"accepted"`
}

type PublicProofIngestArtifact struct {
	ArtifactID string `json:"artifact_id"`
	Content    []byte `json:"content"`
}

type PublicProofIngestRequest struct {
	SourceID  string                      `json:"source_id"`
	Manifest  PublicProofManifest         `json:"manifest,omitzero"`
	Segment   *PublicProofManifestSegment `json:"segment,omitempty"`
	Artifacts []PublicProofIngestArtifact `json:"artifacts,omitempty"`
}

type PublicProofIngestResponse struct {
	Accepted      bool   `json:"accepted"`
	ArtifactCount int    `json:"artifact_count,omitempty"`
	ProofRoot     string `json:"proof_root,omitempty"`
	// MissingArtifactIDs lists, on rejection, the manifest artifacts the
	// mirror neither holds nor received inline.
	MissingArtifactIDs []string `json:"missing_artifact_ids,omitempty"`
}

// PublicProofRootResponse is the mirror's current proof root for one source,
// served on the private ingest listener at GET /proof-root.
type PublicProofRootResponse struct {
	SegmentIndex    int64  `json:"segment_index,omitempty"`
	SourceID        string `json:"source_id"`
	ProofRootSHA256 string `json:"proof_root_sha256"`
	ArtifactCount   int    `json:"artifact_count"`
}

// PublicProofSegmentsResponse is the response for GET /proof-segments.
type PublicProofSegmentsResponse struct {
	SchemaVersion string                       `json:"schema_version"`
	SourceID      string                       `json:"source_id"`
	Segments      []PublicProofManifestSegment `json:"segments"`
}

// PublicAssignmentAuditProofPublishRequest asks the gateway-owned public
// publisher to publish one assignment's audit database. The gateway builds the
// database itself from the signed event records it stores for the run and
// assignment; the request carries no database bytes and no key.
type PublicAssignmentAuditProofPublishRequest struct {
	CampaignID string `json:"campaign_id"`
	// CampaignRevision and IndexDigest are required.
	CampaignRevision string `json:"campaign_revision"`
	RunID            string `json:"run_id"`
	AssignmentID     string `json:"assignment_id"`
	IndexDigest      string `json:"index_digest"`
	// AgentTrace is the exact sealed agent trace body for agent assignments.
	// It is published unredacted as a content-addressed proof artifact.
	AgentTrace []byte `json:"agent_trace,omitempty"`
	// ScenarioID names the assignment's scenario. ScenarioInput and
	// ScenarioGold are its frozen canonical input fixture and gold criteria
	// bodies, published unredacted as campaign-scoped proof artifacts.
	ScenarioID      string `json:"scenario_id,omitempty"`
	ScenarioInput   []byte `json:"scenario_input,omitempty"`
	ScenarioGold    []byte `json:"scenario_gold,omitempty"`
	DeferMirrorPush bool   `json:"defer_mirror_push,omitempty"`
}

// PublicAssignmentAuditProofBatchPublishRequest ingests many assignment audit
// export packages in one gateway round trip. The gateway stages all artifacts,
// rebuilds the proof manifest once, and optionally pushes to the mirror.
type PublicAssignmentAuditProofBatchPublishRequest struct {
	Proofs          []PublicAssignmentAuditProofPublishRequest `json:"proofs"`
	DeferMirrorPush bool                                       `json:"defer_mirror_push,omitempty"`
}

type PublicProofCatalogPushResponse struct {
	Accepted        bool   `json:"accepted"`
	ArtifactCount   int    `json:"artifact_count,omitempty"`
	ProofRootSHA256 string `json:"proof_root_sha256,omitempty"`
}

type PublicProofCatalogPruneRequest struct {
	RunID string `json:"run_id"`
}

type PublicProofCatalogPruneResponse struct {
	Accepted       bool `json:"accepted"`
	RemovedCount   int  `json:"removed_count"`
	RemainingCount int  `json:"remaining_count"`
}

// PublicProofMirrorSyncState checkpoints the last accepted segment. The mirror
// head remains authoritative when resuming delivery.
type PublicProofMirrorSyncState struct {
	SegmentIndex    int64     `json:"segment_index,omitempty"`
	SchemaVersion   string    `json:"schema_version"`
	SourceID        string    `json:"source_id"`
	ProofRootSHA256 string    `json:"proof_root_sha256"`
	ArtifactCount   int       `json:"artifact_count"`
	SyncedAt        time.Time `json:"synced_at"`
}

// PublicAssignmentAuditProofPublishResponse reports the digests of one
// published assignment. DatabaseSHA256 is the SHA-256 of the assignment audit
// database the gateway built; the proof segment signature covers it.
type PublicAssignmentAuditProofPublishResponse struct {
	Accepted            bool   `json:"accepted"`
	AssignmentID        string `json:"assignment_id,omitempty"`
	DatabaseSHA256      string `json:"database_sha256,omitempty"`
	AgentTraceSHA256    string `json:"agent_trace_sha256,omitempty"`
	ScenarioInputSHA256 string `json:"scenario_input_sha256,omitempty"`
	ScenarioGoldSHA256  string `json:"scenario_gold_sha256,omitempty"`
	ProofRootSHA256     string `json:"proof_root_sha256,omitempty"`
}

// PublicAssignmentAuditProofBatchPublishResponse reports a batch publication.
// Proofs holds one response per request, in request order.
type PublicAssignmentAuditProofBatchPublishResponse struct {
	Accepted        bool                                        `json:"accepted"`
	IngestedProofs  int                                         `json:"ingested_proofs,omitempty"`
	ProofRootSHA256 string                                      `json:"proof_root_sha256,omitempty"`
	Proofs          []PublicAssignmentAuditProofPublishResponse `json:"proofs"`
}

// PublicKeyRevocationRecord is a key revocation record published as a signed
// batch record before the old key is deactivated.
type PublicKeyRevocationRecord struct {
	SourceID            string    `json:"source_id"`
	RevokedKeyID        string    `json:"revoked_key_id"`
	RevokedAt           time.Time `json:"revoked_at"`
	NewKeyID            string    `json:"new_key_id"`
	RevocationSignature string    `json:"revocation_signature"`
}

// PublicProofCatalogEntry is one entry in the public proof catalog. Each
// artifact is served under an immutable content-addressed identity.
type PublicProofCatalogEntry struct {
	ArtifactID          string                        `json:"artifact_id"`
	Filename            string                        `json:"filename"`
	MediaType           string                        `json:"media_type"`
	ByteSize            int64                         `json:"byte_size"`
	SHA256              string                        `json:"sha256"`
	Classification      PublicFeedProofClassification `json:"classification"`
	CampaignID          string                        `json:"campaign_id"`
	SourceRunID         string                        `json:"source_run_id,omitempty"`
	GeneratedAt         time.Time                     `json:"generated_at"`
	VerificationCommand string                        `json:"verification_command"`
	ImmutableURL        string                        `json:"immutable_url"`
}

// PublicProofManifest is the root manifest for a public proof package.
type PublicProofManifest struct {
	SchemaVersion               string                    `json:"schema_version"`
	SegmentIndex                int64                     `json:"segment_index,omitempty"`
	ProofRootSHA256             string                    `json:"proof_root_sha256"`
	CampaignID                  string                    `json:"campaign_id"`
	CampaignRevision            string                    `json:"campaign_revision"`
	VerifiedIndexGenerationHash string                    `json:"verified_index_generation_hash"`
	VerificationOK              bool                      `json:"verification_ok"`
	ArtifactCount               int                       `json:"artifact_count"`
	Artifacts                   []PublicProofCatalogEntry `json:"artifacts"`
	VerifierInstructions        string                    `json:"verifier_instructions"`
	GeneratedAt                 time.Time                 `json:"generated_at"`
	SigningKeyID                string                    `json:"signing_key_id"`
	Signature                   string                    `json:"signature"`
}

// PublicProofManifestSegment is one signed link in a chained proof log.
// Each publish appends a new segment rather than rewriting the full catalog.
type PublicProofManifestSegment struct {
	SchemaVersion               string                    `json:"schema_version"`
	SourceID                    string                    `json:"source_id"`
	SegmentIndex                int64                     `json:"segment_index"`
	PreviousRoot                string                    `json:"previous_root"`
	CampaignID                  string                    `json:"campaign_id,omitempty"`
	CampaignRevision            string                    `json:"campaign_revision,omitempty"`
	VerifiedIndexGenerationHash string                    `json:"verified_index_generation_hash,omitempty"`
	VerificationOK              bool                      `json:"verification_ok"`
	Entries                     []PublicProofCatalogEntry `json:"entries,omitempty"`
	WithdrawnArtifactIDs        []string                  `json:"withdrawn_artifact_ids,omitempty"`
	GeneratedAt                 time.Time                 `json:"generated_at"`
	SigningKeyID                string                    `json:"signing_key_id"`
	ProofRootSHA256             string                    `json:"proof_root_sha256"`
	Signature                   string                    `json:"signature"`
	VerifierInstructions        string                    `json:"verifier_instructions,omitempty"`
}

// ComputePublicProofSegmentRoot hashes a domain-separated, length-prefixed
// encoding of every signed field. Length prefixes prevent adjacent variable
// fields from having ambiguous encodings. Root and signature are excluded.
func ComputePublicProofSegmentRoot(seg PublicProofManifestSegment) string {
	h := sha256.New()
	field := func(value string) {
		_, _ = fmt.Fprintf(h, "%d:", len(value))
		_, _ = h.Write([]byte(value))
	}
	field("ztd-public-proof-segment-v1")
	field(seg.SchemaVersion)
	field(seg.SourceID)
	field(strconv.FormatInt(seg.SegmentIndex, 10))
	field(seg.PreviousRoot)
	field(seg.CampaignID)
	field(seg.CampaignRevision)
	field(seg.VerifiedIndexGenerationHash)
	field(strconv.FormatBool(seg.VerificationOK))
	field(strconv.Itoa(len(seg.Entries)))
	for _, entry := range seg.Entries {
		field(entry.ArtifactID)
		field(entry.SHA256)
		field(entry.Filename)
		field(entry.MediaType)
		field(strconv.FormatInt(entry.ByteSize, 10))
		field(string(entry.Classification))
		field(entry.CampaignID)
		field(entry.SourceRunID)
		field(entry.GeneratedAt.UTC().Format(time.RFC3339Nano))
		field(entry.VerificationCommand)
		field(entry.ImmutableURL)
	}
	field(strconv.Itoa(len(seg.WithdrawnArtifactIDs)))
	for _, id := range seg.WithdrawnArtifactIDs {
		field(id)
	}
	field(seg.GeneratedAt.UTC().Format(time.RFC3339Nano))
	field(seg.SigningKeyID)
	field(seg.VerifierInstructions)
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyPublicProofSegment validates a segment's signed shape, root, and
// signature against a source and a trusted key. Callers additionally enforce
// chain position and key revocation at their own trust boundary.
func VerifyPublicProofSegment(sourceID string, seg PublicProofManifestSegment, key ed25519.PublicKey) error {
	if sourceID == "" || seg.SourceID != sourceID || !seg.VerificationOK ||
		seg.SchemaVersion != constants.PublicProofSegmentSchemaVersion || seg.SegmentIndex <= 0 ||
		!validProofHash(seg.PreviousRoot) || seg.SigningKeyID == "" || seg.GeneratedAt.IsZero() ||
		len(seg.Entries)+len(seg.WithdrawnArtifactIDs) > constants.PublicFeedProofMaxArtifacts {
		return constants.ErrPublicFeedProofSegmentInvalid
	}
	seen := make(map[string]bool, len(seg.Entries)+len(seg.WithdrawnArtifactIDs))
	for _, entry := range seg.Entries {
		if !validProofHash(entry.ArtifactID) || entry.SHA256 != entry.ArtifactID || seen[entry.ArtifactID] ||
			entry.Classification != PublicFeedProofClassificationPublicSafe || entry.ByteSize < 0 || entry.ByteSize > constants.PublicFeedMaxArtifactBytes {
			return constants.ErrPublicFeedProofCatalogMismatch
		}
		if entry.Filename == "" || entry.Filename == "." || entry.Filename == ".." || path.Base(entry.Filename) != entry.Filename ||
			strings.Contains(entry.Filename, "\\") || entry.ImmutableURL != "/proofs/"+entry.ArtifactID {
			return constants.ErrPublicFeedProofPathTraversal
		}
		seen[entry.ArtifactID] = true
	}
	for _, id := range seg.WithdrawnArtifactIDs {
		if !validProofHash(id) || seen[id] {
			return constants.ErrPublicFeedProofSegmentInvalid
		}
		seen[id] = true
	}
	if ComputePublicProofSegmentRoot(seg) != seg.ProofRootSHA256 {
		return constants.ErrPublicFeedProofSegmentInvalid
	}
	root, err := hex.DecodeString(seg.ProofRootSHA256)
	if err != nil || len(root) != sha256.Size {
		return constants.ErrPublicFeedProofSegmentInvalid
	}
	signature, err := hex.DecodeString(seg.Signature)
	if err != nil || len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, root, signature) {
		return constants.ErrPublicFeedSignatureInvalid
	}
	return nil
}

func validProofHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

// PublicProofCatalog is the full public proof catalog.
type PublicProofCatalog struct {
	SchemaVersion string                    `json:"schema_version"`
	Entries       []PublicProofCatalogEntry `json:"entries"`
	GeneratedAt   time.Time                 `json:"generated_at"`
}

// PublicExportConfig is the owner-only export configuration for the outbound
// publisher. The private key is stored in the Gateway runtime tree, not in
// this configuration.
type PublicExportConfig struct {
	Enabled                 bool   `json:"enabled"`
	MirrorOrigin            string `json:"mirror_origin"`
	SourceID                string `json:"source_id"`
	SigningKeyID            string `json:"signing_key_id"`
	BatchMaxRecords         int    `json:"batch_max_records"`
	BatchMaxBytes           int    `json:"batch_max_bytes"`
	RetryMaxAttempts        int    `json:"retry_max_attempts"`
	RetryInitialBackoffSecs int    `json:"retry_initial_backoff_seconds"`
	RetryMaxBackoffSecs     int    `json:"retry_max_backoff_seconds"`
	AckWindowSecs           int    `json:"ack_confirmation_window_seconds"`
}

// DefaultPublicExportConfig returns the default export configuration with
// sensible defaults from the constants package. The caller overrides
// Enabled, MirrorOrigin, SourceID, and SigningKeyID.
func DefaultPublicExportConfig() PublicExportConfig {
	return PublicExportConfig{
		Enabled:                 false,
		BatchMaxRecords:         constants.PublicFeedBatchMaxRecords,
		BatchMaxBytes:           constants.PublicFeedBatchMaxBytes,
		RetryMaxAttempts:        constants.PublicFeedRetryMaxAttempts,
		RetryInitialBackoffSecs: constants.PublicFeedRetryInitialBackoff,
		RetryMaxBackoffSecs:     constants.PublicFeedRetryMaxBackoff,
		AckWindowSecs:           constants.PublicFeedAckWindowSeconds,
	}
}

type PublicFeedRecordInput struct {
	RecordType  PublicFeedRecordType `json:"record_type"`
	RecordBytes string               `json:"record_bytes"`
}

type PublicKeyRotationState struct {
	SourceID string `json:"source_id"`
	OldKeyID string `json:"old_key_id"`
	NewKeyID string `json:"new_key_id"`
	// SealedNewPrivateKey is the pending signing key, hex-encoded and then
	// encrypted under the keystore master key. It is never stored in the clear.
	SealedNewPrivateKey string `json:"sealed_new_private_key"`
}

type PublicPublisherStatus struct {
	Enabled           bool   `json:"enabled"`
	MirrorOrigin      string `json:"mirror_origin"`
	SourceID          string `json:"source_id"`
	SigningKeyID      string `json:"signing_key_id"`
	HighWaterSequence int64  `json:"high_water_sequence"`
	FeedChainHash     string `json:"feed_chain_hash"`
	BatchCount        int    `json:"batch_count"`
}

// PublicOutboxEntry is one entry in the durable ordered outbox.
type PublicOutboxEntry struct {
	Sequence       int64                  `json:"sequence"`
	BatchHash      string                 `json:"batch_hash"`
	BatchBytes     string                 `json:"batch_bytes"`
	Status         PublicFeedOutboxStatus `json:"status"`
	Attempts       int                    `json:"attempts"`
	LastAttemptAt  *time.Time             `json:"last_attempt_at,omitempty"`
	AcknowledgedAt *time.Time             `json:"acknowledged_at,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
}
