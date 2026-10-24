// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/publicdisclosure"
)

// publicMirrorReadPageRecords bounds how many records one read handler pulls
// from the store per query while filtering withdrawn or off-kind records.
const publicMirrorReadPageRecords = 256

// PublicMirrorConfig configures trust for proxy-provided visitor identity.
// An empty trusted-proxy set disables forwarded visitor identity entirely.
type PublicMirrorConfig struct {
	TrustedProxyCIDRs []string
}

// PublicMirrorServer is the hermetic reference mirror HTTP server. It accepts
// signed append-only batches from the Gateway outbound publisher at the
// authenticated ingest endpoint and serves anonymous read-only endpoints for
// public browsers: bootstrap, snapshot, cursor-paginated history, replayable
// SSE, and content-addressed proof downloads.
//
// The mirror verifies every batch signature, hash chain, and sequence ordering
// before accepting. It tracks a high-water sequence and feed-chain hash per
// source. Key rotation is supported via revocation records. The mirror exposes
// no mutation, eval-launch, approval, producer, audit, filesystem, pub/sub,
// MCP, A2A, or tool route.
//
// Memory holds only bounded state: per-source summaries and the key registry.
// Records, batches, and proofs live in the PublicMirrorStore and are read per
// request, bounded by the summary high-water mark the reader observed.
//
// This implementation is the reference for the OpenDevOps.ai conformance
// suite. A production mirror (Cloudflare Workers, Supabase, or equivalent)
// must pass the same conformance tests.
type PublicMirrorServer struct {
	mu     sync.RWMutex
	logger *slog.Logger
	store  *PublicMirrorStore

	// writeMu serializes every durable mutation so each one computes its next
	// state from the latest committed state, and orders live broadcasts.
	writeMu sync.Mutex

	activeSourceID string
	sources        map[string]*PublicMirrorSourceSummary
	keys           PublicMirrorKeys

	// sseSubscribers tracks active SSE connections for broadcast.
	sseSubscribers map[*mirrorSSESubscriber]struct{}

	// ingestAuthToken is the shared secret for ingest authentication. If
	// empty, ingest authentication is disabled (test mode).
	ingestAuthToken string

	// maxSSEQueueSize is the bounded SSE queue size per subscriber.
	maxSSEQueueSize int
	// maxSSEReplayRecords caps how many backlog records are sent on connect.
	maxSSEReplayRecords    int
	maxRetainedBatches     int
	maxRetainedRecordBytes int64

	// defaultPageSize is the default cursor page size.
	defaultPageSize int

	// maxPageSize is the maximum cursor page size.
	maxPageSize int

	now                      func() time.Time
	freshnessDelayed         time.Duration
	freshnessStale           time.Duration
	freshnessOffline         time.Duration
	anonymousRateMax         int
	anonymousRateWindow      time.Duration
	anonymousRateClients     map[string]publicMirrorRateWindow
	anonymousRateExpirations []publicMirrorRateExpiry
	anonymousRateExpiryHead  int
	maxSSESubscribers        int
	trustedProxyNetworks     []*net.IPNet
}

// mirrorSSESubscriber represents one active SSE connection.
type mirrorSSESubscriber struct {
	sourceID  string
	ch        chan models.PublicFeedRecord
	resync    chan struct{}
	closed    bool
	truncated bool
	mu        sync.Mutex
}

type publicMirrorRateWindow struct {
	StartedAt time.Time
	Count     int
}

type publicMirrorRateExpiry struct {
	ClientID  string
	StartedAt time.Time
}

// NewPublicMirrorServer creates a new reference mirror server with default
// limits over an open, verified store. The caller owns the store and closes
// it after the server stops. Source public keys are registered via
// RegisterSourceKey before starting ingest.
func NewPublicMirrorServer(logger *slog.Logger, store *PublicMirrorStore, cfg PublicMirrorConfig) (*PublicMirrorServer, error) {
	if store == nil {
		return nil, fmt.Errorf("public mirror: %w: store", constants.ErrMissingRequiredField)
	}
	trustedProxyNetworks, err := parseTrustedProxyCIDRs(cfg.TrustedProxyCIDRs)
	if err != nil {
		return nil, err
	}
	state, err := store.loadState(context.Background())
	if err != nil {
		return nil, fmt.Errorf("public mirror: load store: %w", err)
	}
	return &PublicMirrorServer{
		logger:                   logger,
		store:                    store,
		activeSourceID:           state.activeSourceID,
		sources:                  state.sources,
		keys:                     state.keys,
		sseSubscribers:           make(map[*mirrorSSESubscriber]struct{}),
		maxSSEQueueSize:          100,
		maxSSEReplayRecords:      constants.PublicFeedSSEReplayMaxRecords,
		maxRetainedBatches:       constants.PublicFeedMirrorRetainedBatches,
		maxRetainedRecordBytes:   constants.PublicFeedMirrorRetainedRecordBytes,
		defaultPageSize:          20,
		maxPageSize:              500,
		now:                      time.Now,
		freshnessDelayed:         time.Duration(constants.PublicFeedFreshnessDelayedSeconds) * time.Second,
		freshnessStale:           time.Duration(constants.PublicFeedFreshnessStaleSeconds) * time.Second,
		freshnessOffline:         time.Duration(constants.PublicFeedFreshnessOfflineSeconds) * time.Second,
		anonymousRateMax:         constants.PublicFeedAnonymousRatePerWindow,
		anonymousRateWindow:      time.Duration(constants.PublicFeedAnonymousRateWindowSecs) * time.Second,
		anonymousRateClients:     make(map[string]publicMirrorRateWindow),
		anonymousRateExpirations: make([]publicMirrorRateExpiry, 0),
		maxSSESubscribers:        constants.PublicFeedSSEMaxSubscribers,
		trustedProxyNetworks:     trustedProxyNetworks,
	}, nil
}

// SetIngestAuthToken sets the shared secret for ingest authentication. If
// set, ingest requests must carry an Authorization: Bearer <token> header.
func (m *PublicMirrorServer) SetIngestAuthToken(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ingestAuthToken = token
}

// SetMaxSSEQueueSize sets the bounded SSE queue size per subscriber.
func (m *PublicMirrorServer) SetMaxSSEQueueSize(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxSSEQueueSize = n
}

// SetMaxSSEReplayRecords sets the bounded SSE backlog replay size per connect.
func (m *PublicMirrorServer) SetMaxSSEReplayRecords(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxSSEReplayRecords = n
}

func (m *PublicMirrorServer) SetMaxRetainedBatches(maxBatches int) error {
	if maxBatches <= 0 {
		return constants.ErrPublicFeedRetentionConfig
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxRetainedBatches = maxBatches
	return nil
}

func (m *PublicMirrorServer) SetMaxRetainedRecordBytes(maxBytes int64) error {
	if maxBytes <= 0 {
		return constants.ErrPublicFeedRetentionConfig
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.maxRetainedRecordBytes = maxBytes
	return nil
}

func (m *PublicMirrorServer) SetFreshnessWindows(delayed, stale, offline time.Duration) error {
	if delayed <= 0 || stale <= delayed || offline <= stale {
		return constants.ErrPublicFeedFreshnessConfig
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.freshnessDelayed = delayed
	m.freshnessStale = stale
	m.freshnessOffline = offline
	return nil
}

func (m *PublicMirrorServer) SetAnonymousReadRateLimit(maxRequests int, window time.Duration) error {
	if maxRequests <= 0 || window <= 0 {
		return constants.ErrPublicFeedRateLimitConfig
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.anonymousRateMax = maxRequests
	m.anonymousRateWindow = window
	m.anonymousRateClients = make(map[string]publicMirrorRateWindow)
	m.anonymousRateExpirations = nil
	m.anonymousRateExpiryHead = 0
	return nil
}

func (m *PublicMirrorServer) addSSESubscriber(sourceID string) (*mirrorSSESubscriber, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sseSubscribers) >= m.maxSSESubscribers {
		return nil, false
	}
	subscriber := &mirrorSSESubscriber{
		sourceID: sourceID,
		ch:       make(chan models.PublicFeedRecord, m.maxSSEQueueSize),
		resync:   make(chan struct{}, 1),
	}
	m.sseSubscribers[subscriber] = struct{}{}
	return subscriber, true
}

// requestSnapshotResync asks every open stream of one source to send a fresh
// snapshot, for state changes that append no record (dataset withdrawal).
func (m *PublicMirrorServer) requestSnapshotResync(sourceID string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for sub := range m.sseSubscribers {
		if sub.sourceID != sourceID {
			continue
		}
		select {
		case sub.resync <- struct{}{}:
		default:
		}
	}
}

func (m *PublicMirrorServer) removeSSESubscriber(subscriber *mirrorSSESubscriber) {
	m.mu.Lock()
	delete(m.sseSubscribers, subscriber)
	m.mu.Unlock()
	subscriber.close()
}

// activeSourceIDLocked returns the active source, falling back to the lowest
// known source ID. The caller holds m.mu.
func (m *PublicMirrorServer) activeSourceIDLocked() string {
	if m.activeSourceID != "" {
		return m.activeSourceID
	}
	active := ""
	for sourceID := range m.sources {
		if active == "" || sourceID < active {
			active = sourceID
		}
	}
	return active
}

// sourceView resolves a requested source (or the active source when empty)
// and returns its current immutable summary, nil when the mirror has none.
func (m *PublicMirrorServer) sourceView(requested string) (string, *PublicMirrorSourceSummary) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sourceID := requested
	if sourceID == "" {
		sourceID = m.activeSourceIDLocked()
	}
	return sourceID, m.sources[sourceID]
}

// RegisterSourceKey registers a public key for a source deployment. The
// mirror accepts batches signed by this key until it is revoked.
func (m *PublicMirrorServer) RegisterSourceKey(ctx context.Context, sourceID, keyID string, pubKey ed25519.PublicKey) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	registryID := publicMirrorKeyRegistryID(sourceID, keyID)
	key := append(ed25519.PublicKey(nil), pubKey...)
	if err := m.store.putSourceKey(ctx, registryID, key, sourceID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := m.keys.clone()
	keys.Registry[registryID] = key
	m.keys = keys
	m.activeSourceID = sourceID
	return nil
}

// RevokeSourceKey revokes a signing key for a source deployment. Subsequent
// batches signed by the revoked key are rejected with revoked_key.
func (m *PublicMirrorServer) RevokeSourceKey(ctx context.Context, sourceID, keyID string) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	registryID := publicMirrorKeyRegistryID(sourceID, keyID)
	revokedAt := time.Now().UTC()
	if err := m.store.revokeKey(ctx, registryID, revokedAt); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := m.keys.clone()
	keys.Revoked[registryID] = revokedAt
	m.keys = keys
	return nil
}

// SetSourceFreshness sets the freshness label for a source deployment. Used
// by tests to simulate stale, stopped, or offline sources.
func (m *PublicMirrorServer) SetSourceFreshness(ctx context.Context, sourceID string, freshness models.CampaignFreshness) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	next := m.currentSourceSummary(sourceID)
	next.Freshness = freshness
	if err := m.store.putSourceSummary(ctx, next); err != nil {
		return err
	}
	m.mu.Lock()
	m.sources[sourceID] = &next
	m.mu.Unlock()
	return nil
}

// currentSourceSummary copies the current summary of one source, or a fresh
// summary when the mirror has none. The caller holds m.writeMu.
func (m *PublicMirrorServer) currentSourceSummary(sourceID string) PublicMirrorSourceSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if current, ok := m.sources[sourceID]; ok {
		return *current
	}
	return newPublicMirrorSourceSummary(sourceID)
}

// computeBatchContentHash recomputes the SHA-256 content hash over the
// canonical batch fields (excluding signature). This must match the
// publisher's computation exactly.
func computeBatchContentHash(batch models.PublicFeedBatch) string {
	h := sha256.New()
	h.Write([]byte(batch.ProtocolVersion))
	h.Write([]byte(batch.SchemaVersion))
	h.Write([]byte(batch.SourceID))
	_, _ = fmt.Fprintf(h, "%d", batch.FirstSequence)
	_, _ = fmt.Fprintf(h, "%d", batch.LastSequence)
	h.Write([]byte(batch.PreviousBatchHash))
	for _, rh := range batch.RecordHashes {
		h.Write([]byte(rh))
	}
	h.Write([]byte(batch.GeneratedAt.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(h.Sum(nil))
}

// verifyBatchSignature verifies the Ed25519 signature over the content hash.
func verifyBatchSignature(batch models.PublicFeedBatch, pubKey ed25519.PublicKey) error {
	computed := computeBatchContentHash(batch)
	if computed != batch.ContentHash {
		return constants.ErrPublicFeedContentHashMismatch
	}
	contentHashBytes, err := hex.DecodeString(batch.ContentHash)
	if err != nil {
		return fmt.Errorf("mirror: decode content hash: %w", err)
	}
	sigBytes, err := hex.DecodeString(batch.Signature)
	if err != nil {
		return fmt.Errorf("mirror: decode signature: %w", err)
	}
	if !ed25519.Verify(pubKey, contentHashBytes, sigBytes) {
		return constants.ErrPublicFeedSignatureInvalid
	}
	return nil
}

// validateBatch performs all ingest validation layers: signature, content
// hash, hash chain, sequence ordering, duplicate sequence, oversized batch,
// revoked key, unknown key, and record hash verification. Returns a typed
// rejection reason on failure. A batch identical to one already accepted
// validates cleanly so publisher retries are idempotent.
func (m *PublicMirrorServer) validateBatch(ctx context.Context, batch models.PublicFeedBatch) (models.PublicFeedIngestRejectionReason, error) {
	if len(batch.Records) == 0 {
		return models.PublicFeedIngestRejectionOversizedBatch, constants.ErrPublicFeedBatchEmpty
	}
	if len(batch.Records) > constants.PublicFeedBatchMaxRecords {
		return models.PublicFeedIngestRejectionOversizedBatch, constants.ErrPublicFeedBatchOversized
	}
	if batch.FirstSequence <= 0 || batch.LastSequence < batch.FirstSequence || batch.LastSequence-batch.FirstSequence+1 != int64(len(batch.Records)) || len(batch.RecordHashes) != len(batch.Records) {
		return models.PublicFeedIngestRejectionSequenceOutOfOrder, constants.ErrPublicFeedSequenceOutOfOrder
	}
	var totalBytes int
	for _, r := range batch.Records {
		totalBytes += len(r.RecordBytes)
	}
	if totalBytes > constants.PublicFeedBatchMaxBytes {
		return models.PublicFeedIngestRejectionOversizedBatch, constants.ErrPublicFeedBatchOversized
	}

	m.mu.RLock()
	keys := m.keys
	summary := m.sources[batch.SourceID]
	m.mu.RUnlock()
	registryID := publicMirrorKeyRegistryID(batch.SourceID, batch.SigningKeyID)
	pubKey, keyKnown := keys.Registry[registryID]
	_, isRevoked := keys.Revoked[registryID]

	if !keyKnown {
		return models.PublicFeedIngestRejectionUnknownKey, constants.ErrPublicFeedUnknownKey
	}

	if err := verifyBatchSignature(batch, pubKey); err != nil {
		if err == constants.ErrPublicFeedContentHashMismatch {
			return models.PublicFeedIngestRejectionHashChainMismatch, err
		}
		return models.PublicFeedIngestRejectionSignatureInvalid, err
	}

	// Verify each record hash.
	for index, r := range batch.Records {
		if r.Sequence != batch.FirstSequence+int64(index) || r.RecordHash != batch.RecordHashes[index] {
			return models.PublicFeedIngestRejectionSequenceOutOfOrder, constants.ErrPublicFeedSequenceOutOfOrder
		}
		computed := sha256.Sum256([]byte(r.RecordBytes))
		if hex.EncodeToString(computed[:]) != r.RecordHash {
			return models.PublicFeedIngestRejectionSignatureInvalid, constants.ErrPublicFeedRecordHashMismatch
		}
		if err := publicdisclosure.ValidatePublicFeedRecord(r.RecordType, []byte(r.RecordBytes)); err != nil {
			return models.PublicFeedIngestRejectionSignatureInvalid, fmt.Errorf("public mirror: validate record: %w", err)
		}
	}
	if _, err := validatePublicKeyRevocations(batch, keys); err != nil {
		return models.PublicFeedIngestRejectionSignatureInvalid, err
	}

	// A batch at or below the high-water mark is a retry when it matches the
	// retained batch at the same position, else equivocation or a duplicate.
	var accepted publicMirrorBatchHeader
	var acceptedFound bool
	if summary != nil && batch.FirstSequence <= summary.HighWaterSequence {
		header, found, err := m.store.batchHeader(ctx, batch.SourceID, batch.FirstSequence)
		if err != nil {
			return models.PublicFeedIngestRejectionSignatureInvalid, err
		}
		accepted, acceptedFound = header, found && header.LastSequence == batch.LastSequence
		if acceptedFound && accepted.ContentHash == batch.ContentHash {
			return "", nil
		}
	}
	if isRevoked {
		return models.PublicFeedIngestRejectionRevokedKey, constants.ErrPublicFeedRevokedKey
	}

	// Sequence and hash chain validation.
	if summary != nil && batch.FirstSequence <= summary.HighWaterSequence {
		if acceptedFound {
			return models.PublicFeedIngestRejectionEquivocation, constants.ErrPublicFeedEquivocation
		}
		return models.PublicFeedIngestRejectionDuplicateSequence, constants.ErrPublicFeedDuplicateSequence
	}
	if summary != nil && batch.PreviousBatchHash != summary.FeedChainHash {
		return models.PublicFeedIngestRejectionHashChainMismatch, constants.ErrPublicFeedHashChainMismatch
	}
	if summary == nil && batch.PreviousBatchHash != constants.PublicFeedZeroHashHex {
		return models.PublicFeedIngestRejectionHashChainMismatch, constants.ErrPublicFeedHashChainMismatch
	}

	return "", nil
}

func validatePublicKeyRevocations(batch models.PublicFeedBatch, keys PublicMirrorKeys) ([]models.PublicKeyRevocationRecord, error) {
	revocations := make([]models.PublicKeyRevocationRecord, 0, 1)
	for _, record := range batch.Records {
		if record.RecordType != models.PublicFeedRecordTypeKeyRevocation {
			continue
		}
		if len(revocations) != 0 {
			return nil, constants.ErrPublicFeedKeyRevocation
		}
		decoder := json.NewDecoder(strings.NewReader(record.RecordBytes))
		decoder.DisallowUnknownFields()
		var revocation models.PublicKeyRevocationRecord
		if err := decoder.Decode(&revocation); err != nil {
			return nil, fmt.Errorf("%w: decode: %v", constants.ErrPublicFeedKeyRevocation, err)
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("%w: trailing JSON", constants.ErrPublicFeedKeyRevocation)
		}
		if revocation.SourceID != batch.SourceID || revocation.RevokedKeyID != batch.SigningKeyID || revocation.NewKeyID == "" || revocation.NewKeyID == revocation.RevokedKeyID || revocation.RevokedAt.IsZero() {
			return nil, constants.ErrPublicFeedKeyRevocation
		}
		oldKey, oldKeyKnown := keys.Registry[publicMirrorKeyRegistryID(revocation.SourceID, revocation.RevokedKeyID)]
		_, newKeyKnown := keys.Registry[publicMirrorKeyRegistryID(revocation.SourceID, revocation.NewKeyID)]
		if !oldKeyKnown || !newKeyKnown {
			return nil, constants.ErrPublicFeedKeyRevocation
		}
		signature, err := hex.DecodeString(revocation.RevocationSignature)
		if err != nil || len(signature) != ed25519.SignatureSize {
			return nil, constants.ErrPublicFeedKeyRevocation
		}
		signingBytes, err := publicKeyRevocationSigningBytes(revocation)
		if err != nil || !ed25519.Verify(oldKey, signingBytes, signature) {
			return nil, constants.ErrPublicFeedKeyRevocation
		}
		revocations = append(revocations, revocation)
	}
	return revocations, nil
}

// sourceFreshness derives one source's freshness label. The caller must not
// hold m.mu.
func (m *PublicMirrorServer) sourceFreshness(summary *PublicMirrorSourceSummary) models.CampaignFreshness {
	if summary.Freshness == models.CampaignFreshnessIntentionallyStopped || summary.Freshness == models.CampaignFreshnessSafetyStopped {
		return summary.Freshness
	}
	if summary.LastAcceptedAt.IsZero() {
		return models.CampaignFreshnessSourceOffline
	}
	m.mu.RLock()
	delayed, stale, offline := m.freshnessDelayed, m.freshnessStale, m.freshnessOffline
	m.mu.RUnlock()
	age := m.now().UTC().Sub(summary.LastAcceptedAt)
	switch {
	case age >= offline:
		return models.CampaignFreshnessSourceOffline
	case age >= stale:
		return models.CampaignFreshnessStale
	case age >= delayed:
		return models.CampaignFreshnessDelayed
	default:
		return models.CampaignFreshnessActive
	}
}

// acceptBatch durably appends a validated batch before broadcasting its
// records to SSE subscribers. Re-submitting an accepted batch is a no-op.
func (m *PublicMirrorServer) acceptBatch(ctx context.Context, batch models.PublicFeedBatch) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.mu.RLock()
	keys := m.keys
	maxRetainedBatches := m.maxRetainedBatches
	maxRetainedRecordBytes := m.maxRetainedRecordBytes
	m.mu.RUnlock()
	revocations, err := validatePublicKeyRevocations(batch, keys)
	if err != nil {
		return err
	}
	next := m.currentSourceSummary(batch.SourceID)
	if batch.FirstSequence <= next.HighWaterSequence {
		header, found, err := m.store.batchHeader(ctx, batch.SourceID, batch.FirstSequence)
		if err != nil {
			return err
		}
		if found && header.LastSequence == batch.LastSequence && header.ContentHash == batch.ContentHash {
			return nil
		}
	}
	if batch.FirstSequence != next.HighWaterSequence+1 || batch.PreviousBatchHash != next.FeedChainHash {
		return constants.ErrPublicFeedHashChainMismatch
	}
	next.HighWaterSequence = batch.LastSequence
	next.FeedChainHash = batch.ContentHash
	next.BatchCount++
	next.RetainedBatchCount++
	next.RetainedRecordCount += len(batch.Records)
	for _, r := range batch.Records {
		next.RetainedRecordBytes += int64(len(r.RecordBytes))
	}
	next.LastAcceptedAt = m.now().UTC()
	next.Freshness = models.CampaignFreshnessActive
	stored, err := m.store.appendBatch(ctx, batch, next, revocations, maxRetainedBatches, maxRetainedRecordBytes)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.sources[batch.SourceID] = &stored
	if len(revocations) > 0 {
		updated := m.keys.clone()
		for _, revocation := range revocations {
			updated.Revoked[publicMirrorKeyRegistryID(revocation.SourceID, revocation.RevokedKeyID)] = revocation.RevokedAt
		}
		m.keys = updated
	}
	subscribers := make([]*mirrorSSESubscriber, 0, len(m.sseSubscribers))
	for sub := range m.sseSubscribers {
		if sub.sourceID == batch.SourceID {
			subscribers = append(subscribers, sub)
		}
	}
	m.mu.Unlock()
	for _, sub := range subscribers {
		for _, record := range batch.Records {
			sub.send(record)
		}
	}
	return nil
}

// IngestHandler returns the handler for the private ingest listener. It serves
// only the token-authenticated publisher routes: batch ingest, source key
// registration, proof ingest, and the proof root lookup. No anonymous read
// route is mounted here, and ingest routes never carry CORS headers.
func (m *PublicMirrorServer) IngestHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ingest", m.handleIngest)
	mux.HandleFunc("/keys/register", m.handleKeyRegistration)
	mux.HandleFunc("/proof-ingest", m.handleProofIngest)
	mux.HandleFunc("/proof-root", m.handleProofRoot)
	return mux
}

// PublicHandler returns the handler for the public read listener. It serves
// only the anonymous read routes (rate limited, CORS enabled); no ingest or
// mutation route is mounted here.
func (m *PublicMirrorServer) PublicHandler() http.Handler {
	return m.withCORS(m.publicReadMux())
}

func (m *PublicMirrorServer) publicReadMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/bootstrap", m.withGzip(http.HandlerFunc(m.handleBootstrap)))
	mux.Handle("/snapshot", m.withGzip(http.HandlerFunc(m.handleSnapshot)))
	mux.Handle("/history", m.withGzip(http.HandlerFunc(m.handleHistory)))
	mux.HandleFunc("/stream", m.handleStream)
	mux.Handle("/proof-catalog", m.withGzip(http.HandlerFunc(m.handleProofCatalog)))
	mux.Handle("/proof-manifest", m.withGzip(http.HandlerFunc(m.handleProofManifest)))
	mux.Handle("/proof-segments", m.withGzip(http.HandlerFunc(m.handleProofSegments)))
	mux.HandleFunc("/proofs/", m.handleProofDownload)
	return mux
}

type gzipResponseWriter struct {
	http.ResponseWriter
	writer *gzip.Writer
}

func (w gzipResponseWriter) Write(body []byte) (int, error) {
	return w.writer.Write(body)
}

func (m *PublicMirrorServer) withGzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		next.ServeHTTP(gzipResponseWriter{ResponseWriter: w, writer: writer}, r)
		if err := writer.Close(); err != nil {
			m.logResponseError("mirror: close gzip response", err)
		}
	})
}

func publicMirrorAnonymousReadPath(requestPath string) bool {
	switch requestPath {
	case "/bootstrap", "/snapshot", "/history", "/stream", "/proof-catalog", "/proof-manifest", "/proof-segments":
		return true
	default:
		return strings.HasPrefix(requestPath, "/proofs/")
	}
}

func parseTrustedProxyCIDRs(values []string) ([]*net.IPNet, error) {
	networks := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("public mirror: trusted proxy CIDR is empty: %w", constants.ErrPublicFeedTrustedProxyConfig)
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("public mirror: parse trusted proxy CIDR %q: %w: %v", value, constants.ErrPublicFeedTrustedProxyConfig, err)
		}
		networks = append(networks, network)
	}
	return networks, nil
}

func (m *PublicMirrorServer) publicMirrorClientID(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remoteIP := net.ParseIP(host)
	if remoteIP != nil && m.isTrustedProxy(remoteIP) {
		connectingIPs := r.Header.Values("CF-Connecting-IP")
		if len(connectingIPs) == 1 {
			connectingIP := net.ParseIP(strings.TrimSpace(connectingIPs[0]))
			if isValidPublicMirrorClientIP(connectingIP) {
				return connectingIP.String()
			}
		}
	}
	return host
}

func (m *PublicMirrorServer) isTrustedProxy(remoteIP net.IP) bool {
	for _, network := range m.trustedProxyNetworks {
		if network.Contains(remoteIP) {
			return true
		}
	}
	return false
}

func isValidPublicMirrorClientIP(ip net.IP) bool {
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsMulticast()
}

func (m *PublicMirrorServer) allowAnonymousRead(r *http.Request) bool {
	host := m.publicMirrorClientID(r)
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireAnonymousRateWindows(now)
	window, ok := m.anonymousRateClients[host]
	if ok && !now.Before(window.StartedAt.Add(m.anonymousRateWindow)) {
		delete(m.anonymousRateClients, host)
		ok = false
	}
	if !ok {
		if len(m.anonymousRateClients) >= constants.PublicFeedAnonymousRateMaxClients {
			return false
		}
		window = publicMirrorRateWindow{StartedAt: now, Count: 1}
		m.anonymousRateClients[host] = window
		m.anonymousRateExpirations = append(m.anonymousRateExpirations, publicMirrorRateExpiry{ClientID: host, StartedAt: now})
		return true
	}
	if window.Count >= m.anonymousRateMax {
		return false
	}
	window.Count++
	m.anonymousRateClients[host] = window
	return true
}

func (m *PublicMirrorServer) expireAnonymousRateWindows(now time.Time) {
	for m.anonymousRateExpiryHead < len(m.anonymousRateExpirations) {
		expiry := m.anonymousRateExpirations[m.anonymousRateExpiryHead]
		if now.Before(expiry.StartedAt.Add(m.anonymousRateWindow)) {
			break
		}
		m.anonymousRateExpiryHead++
		window, ok := m.anonymousRateClients[expiry.ClientID]
		if ok && window.StartedAt.Equal(expiry.StartedAt) {
			delete(m.anonymousRateClients, expiry.ClientID)
		}
	}
	if m.anonymousRateExpiryHead == 0 {
		return
	}
	if m.anonymousRateExpiryHead*2 < len(m.anonymousRateExpirations) && m.anonymousRateExpiryHead < 1024 {
		return
	}
	remaining := m.anonymousRateExpirations[m.anonymousRateExpiryHead:]
	m.anonymousRateExpirations = append([]publicMirrorRateExpiry(nil), remaining...)
	m.anonymousRateExpiryHead = 0
}

// withCORS wraps the handler with CORS headers for anonymous read endpoints
// only. Ingest routes are server-to-server and never carry CORS headers.
func (m *PublicMirrorServer) withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicMirrorAnonymousReadPath(r.URL.Path) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Last-Event-ID")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet && publicMirrorAnonymousReadPath(r.URL.Path) && !m.allowAnonymousRead(r) {
			w.Header().Set("Retry-After", strconv.Itoa(int(m.anonymousRateWindow.Seconds())))
			http.Error(w, constants.ErrPublicFeedRateLimited.Error(), http.StatusTooManyRequests)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ingestAuthorized checks the request's bearer token against the configured
// ingest token. An unconfigured token rejects every request: the mirror never
// admits ingest or proof traffic without a shared secret.
func (m *PublicMirrorServer) ingestAuthorized(r *http.Request) error {
	m.mu.RLock()
	token := m.ingestAuthToken
	m.mu.RUnlock()
	if token == "" {
		m.logger.Warn("mirror: ingest token is not configured; rejecting request", "path", r.URL.Path)
		return fmt.Errorf("mirror ingest: %w", constants.ErrPublicFeedIngestTokenRequired)
	}
	provided := r.Header.Get("Authorization")
	expected := "Bearer " + token
	if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		return constants.ErrPublicFeedIngestUnauthorized
	}
	return nil
}

func (m *PublicMirrorServer) handleKeyRegistration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := m.ingestAuthorized(r); err != nil {
		m.writeJSON(w, http.StatusUnauthorized, models.PublicKeyRegistrationResponse{Accepted: false})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, constants.PublicFeedKeyRegistrationMaxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request models.PublicKeyRegistrationRequest
	if err := decoder.Decode(&request); err != nil {
		m.writeJSON(w, http.StatusBadRequest, models.PublicKeyRegistrationResponse{Accepted: false})
		return
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		m.writeJSON(w, http.StatusBadRequest, models.PublicKeyRegistrationResponse{Accepted: false})
		return
	}
	if err := m.admitSourceKey(r.Context(), request); err != nil {
		m.logger.Warn("mirror: key registration rejected", "source_id", request.SourceID, "error", err)
		m.writeJSON(w, http.StatusBadRequest, models.PublicKeyRegistrationResponse{Accepted: false})
		return
	}
	m.writeJSON(w, http.StatusOK, models.PublicKeyRegistrationResponse{Accepted: true})
}

func (m *PublicMirrorServer) admitSourceKey(ctx context.Context, request models.PublicKeyRegistrationRequest) error {
	publicKey, err := hex.DecodeString(request.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize || request.SourceID == "" || request.CurrentKeyID == "" || request.NewKeyID == "" || request.NewKeyID == request.CurrentKeyID {
		return constants.ErrPublicFeedKeyRegistration
	}
	digest := sha256.Sum256(publicKey)
	if request.NewKeyID != hex.EncodeToString(digest[:]) {
		return constants.ErrPublicFeedKeyRegistration
	}
	signature, err := hex.DecodeString(request.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return constants.ErrPublicFeedKeyRegistration
	}
	signingBytes, err := publicKeyRegistrationSigningBytes(request)
	if err != nil {
		return fmt.Errorf("%w: encode request: %v", constants.ErrPublicFeedKeyRegistration, err)
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.mu.RLock()
	keys := m.keys
	m.mu.RUnlock()
	currentRegistryID := publicMirrorKeyRegistryID(request.SourceID, request.CurrentKeyID)
	currentKey, ok := keys.Registry[currentRegistryID]
	if !ok || !ed25519.Verify(currentKey, signingBytes, signature) {
		return constants.ErrPublicFeedKeyRegistration
	}
	newRegistryID := publicMirrorKeyRegistryID(request.SourceID, request.NewKeyID)
	if existing, exists := keys.Registry[newRegistryID]; exists {
		if !bytes.Equal(existing, publicKey) {
			return constants.ErrPublicFeedEquivocation
		}
		return nil
	}
	if _, revoked := keys.Revoked[currentRegistryID]; revoked {
		return constants.ErrPublicFeedRevokedKey
	}
	key := ed25519.PublicKey(publicKey)
	if err := m.store.putSourceKey(ctx, newRegistryID, key, ""); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	updated := m.keys.clone()
	updated.Registry[newRegistryID] = key
	m.keys = updated
	return nil
}

// sourceIngestResponse reports one source's accepted chain position.
func (m *PublicMirrorServer) sourceIngestResponse(sourceID string, resp models.PublicIngestResponse) models.PublicIngestResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if summary := m.sources[sourceID]; summary != nil {
		resp.SourceID = sourceID
		resp.HighWaterSequence = summary.HighWaterSequence
		resp.FeedChainHash = summary.FeedChainHash
		resp.BatchCount = summary.BatchCount
	}
	return resp
}

// handleIngest handles POST /ingest — the authenticated ingest endpoint.
func (m *PublicMirrorServer) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Authenticate.
	if err := m.ingestAuthorized(r); err != nil {
		resp := models.PublicIngestResponse{Accepted: false, RejectionReason: models.PublicFeedIngestRejectionSignatureInvalid}
		m.writeJSON(w, http.StatusUnauthorized, resp)
		return
	}

	var req models.PublicIngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		resp := models.PublicIngestResponse{Accepted: false, RejectionReason: models.PublicFeedIngestRejectionSignatureInvalid}
		m.writeJSON(w, http.StatusBadRequest, resp)
		return
	}

	reason, err := m.validateBatch(r.Context(), req.Batch)
	if err != nil {
		m.logger.Warn("mirror: ingest rejected", "source_id", req.Batch.SourceID, "reason", reason, "error", err)
		resp := m.sourceIngestResponse(req.Batch.SourceID, models.PublicIngestResponse{Accepted: false, RejectionReason: reason})
		m.writeJSON(w, http.StatusOK, resp)
		return
	}

	if err := m.acceptBatch(r.Context(), req.Batch); err != nil {
		m.logger.Error("mirror: persist accepted batch", "source_id", req.Batch.SourceID, "error", err)
		resp := models.PublicIngestResponse{Accepted: false}
		m.writeJSON(w, http.StatusInternalServerError, resp)
		return
	}

	m.writeJSON(w, http.StatusOK, m.sourceIngestResponse(req.Batch.SourceID, models.PublicIngestResponse{Accepted: true}))
}

func (m *PublicMirrorServer) handleProofIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := m.ingestAuthorized(r); err != nil {
		m.writeJSON(w, http.StatusUnauthorized, models.PublicProofIngestResponse{Accepted: false})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, constants.PublicFeedProofIngestMaxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request models.PublicProofIngestRequest
	if err := decoder.Decode(&request); err != nil {
		m.writeJSON(w, http.StatusBadRequest, models.PublicProofIngestResponse{Accepted: false})
		return
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		m.writeJSON(w, http.StatusBadRequest, models.PublicProofIngestResponse{Accepted: false})
		return
	}
	if err := m.storeProofPackage(r.Context(), request); err != nil {
		var missing *publicProofMissingArtifactsError
		if errors.As(err, &missing) {
			m.writeJSON(w, http.StatusBadRequest, models.PublicProofIngestResponse{Accepted: false, MissingArtifactIDs: missing.artifactIDs})
			return
		}
		m.logger.Warn("mirror: proof ingest rejected", "source_id", request.SourceID, "error", err)
		m.writeJSON(w, http.StatusBadRequest, models.PublicProofIngestResponse{Accepted: false})
		return
	}
	root := request.Manifest.ProofRootSHA256
	if request.Segment != nil {
		root = request.Segment.ProofRootSHA256
	}
	m.writeJSON(w, http.StatusOK, models.PublicProofIngestResponse{
		Accepted:      true,
		ArtifactCount: len(request.Artifacts),
		ProofRoot:     root,
	})
}

// publicProofMissingArtifactsError names the manifest artifacts a proof
// ingest neither carried inline nor found stored, so the publisher can resend
// exactly those.
type publicProofMissingArtifactsError struct {
	artifactIDs []string
}

func (e *publicProofMissingArtifactsError) Error() string {
	return fmt.Sprintf("%v: %d artifacts", constants.ErrPublicFeedProofArtifactsMissing, len(e.artifactIDs))
}

func (e *publicProofMissingArtifactsError) Unwrap() error {
	return constants.ErrPublicFeedProofArtifactsMissing
}

// storeProofPackage verifies a signed proof manifest or segment and the artifacts it
// carries inline, then stores them.
func (m *PublicMirrorServer) storeProofPackage(ctx context.Context, request models.PublicProofIngestRequest) error {
	if request.Segment != nil {
		if request.Manifest.SchemaVersion != "" {
			return constants.ErrPublicFeedProofSegmentInvalid
		}
		return m.storeProofSegment(ctx, request.SourceID, *request.Segment, request.Artifacts)
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.mu.RLock()
	keys := m.keys
	m.mu.RUnlock()
	if index, _, err := m.store.proofSegmentHead(ctx, request.SourceID, 0); err != nil {
		return err
	} else if index != 0 {
		return constants.ErrPublicFeedProofSegmentInvalid
	}

	manifest := request.Manifest
	if err := validatePublicProofManifest(request.SourceID, manifest, keys, true); err != nil {
		return err
	}
	if len(request.Artifacts) > manifest.ArtifactCount {
		return constants.ErrPublicFeedProofCatalogMismatch
	}
	artifactIDs := make([]string, len(manifest.Artifacts))
	for index, entry := range manifest.Artifacts {
		artifactIDs[index] = entry.ArtifactID
	}
	storedSizes, err := m.store.proofArtifactSizes(ctx, artifactIDs)
	if err != nil {
		return err
	}
	if err := validatePublicProofEntryList(manifest.Artifacts, request.Artifacts, storedSizes); err != nil {
		return err
	}
	return m.store.putProofPackage(ctx, request.SourceID, manifest, request.Artifacts, storedSizes)
}

func (m *PublicMirrorServer) storeProofSegment(ctx context.Context, sourceID string, segment models.PublicProofManifestSegment, artifacts []models.PublicProofIngestArtifact) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.mu.RLock()
	keys := m.keys
	m.mu.RUnlock()

	if err := validatePublicProofSegment(sourceID, segment, keys, true); err != nil {
		return err
	}

	lastIndex, lastRoot, err := m.store.proofSegmentHead(ctx, sourceID, 0)
	if err != nil {
		return err
	}
	if segment.SegmentIndex <= lastIndex {
		_, existingRoot, err := m.store.proofSegmentHead(ctx, sourceID, segment.SegmentIndex)
		if err != nil {
			return err
		}
		if existingRoot == segment.ProofRootSHA256 {
			return nil
		}
		return constants.ErrPublicFeedProofSegmentEquivocation
	}
	if segment.SegmentIndex != lastIndex+1 {
		return constants.ErrPublicFeedProofSegmentOutOfOrder
	}
	if segment.PreviousRoot != lastRoot {
		return constants.ErrPublicFeedProofSegmentLinkMismatch
	}

	artifactIDs := make([]string, len(segment.Entries))
	for index, entry := range segment.Entries {
		artifactIDs[index] = entry.ArtifactID
	}
	storedSizes, err := m.store.proofArtifactSizes(ctx, artifactIDs)
	if err != nil {
		return err
	}
	if err := validatePublicProofEntryList(segment.Entries, artifacts, storedSizes); err != nil {
		return err
	}
	return m.store.putProofSegment(ctx, sourceID, segment, artifacts, storedSizes)
}

func validatePublicProofSegment(sourceID string, segment models.PublicProofManifestSegment, keys PublicMirrorKeys, rejectRevoked bool) error {
	keyRegistryID := publicMirrorKeyRegistryID(sourceID, segment.SigningKeyID)
	if _, revoked := keys.Revoked[keyRegistryID]; rejectRevoked && revoked {
		return constants.ErrPublicFeedRevokedKey
	}
	key, ok := keys.Registry[keyRegistryID]
	if !ok {
		return constants.ErrPublicFeedUnknownKey
	}
	return models.VerifyPublicProofSegment(sourceID, segment, key)
}

// validatePublicProofManifest verifies a proof manifest's shape, signing key,
// recomputed proof root, and root signature.
func validatePublicProofManifest(sourceID string, manifest models.PublicProofManifest, keys PublicMirrorKeys, rejectRevoked bool) error {
	if sourceID == "" || !manifest.VerificationOK || manifest.SegmentIndex != 0 || manifest.SchemaVersion != constants.PublicProofManifestSchemaVersion {
		return constants.ErrPublicFeedProofManifestInvalid
	}
	if manifest.ArtifactCount <= 0 || manifest.ArtifactCount > constants.PublicFeedProofMaxArtifacts || manifest.ArtifactCount != len(manifest.Artifacts) {
		return constants.ErrPublicFeedProofCatalogMismatch
	}
	keyRegistryID := publicMirrorKeyRegistryID(sourceID, manifest.SigningKeyID)
	if _, revoked := keys.Revoked[keyRegistryID]; rejectRevoked && revoked {
		return constants.ErrPublicFeedRevokedKey
	}
	key, ok := keys.Registry[keyRegistryID]
	if !ok {
		return constants.ErrPublicFeedUnknownKey
	}
	computedRoot := computePublicProofRootHash(manifest)
	if computedRoot != manifest.ProofRootSHA256 {
		return constants.ErrPublicFeedProofManifestInvalid
	}
	rootBytes, err := hex.DecodeString(manifest.ProofRootSHA256)
	if err != nil || len(rootBytes) != sha256.Size {
		return constants.ErrPublicFeedProofManifestInvalid
	}
	signature, err := hex.DecodeString(manifest.Signature)
	if err != nil || !ed25519.Verify(key, rootBytes, signature) {
		return constants.ErrPublicFeedSignatureInvalid
	}
	return nil
}

func validatePublicProofEntryList(entries []models.PublicProofCatalogEntry, inline []models.PublicProofIngestArtifact, storedSizes map[string]int64) error {
	inlineArtifacts := make(map[string][]byte, len(inline))
	for _, artifact := range inline {
		if artifact.ArtifactID == "" {
			return constants.ErrPublicFeedProofHashMismatch
		}
		if _, duplicate := inlineArtifacts[artifact.ArtifactID]; duplicate {
			return constants.ErrPublicFeedProofCatalogMismatch
		}
		inlineArtifacts[artifact.ArtifactID] = artifact.Content
	}
	referenced := make(map[string]bool, len(entries))
	var missing []string
	for _, entry := range entries {
		if referenced[entry.ArtifactID] || !validPublicProofArtifactID(entry.ArtifactID) || entry.ByteSize < 0 {
			return constants.ErrPublicFeedProofCatalogMismatch
		}
		if entry.Classification != models.PublicFeedProofClassificationPublicSafe || entry.ArtifactID != entry.SHA256 {
			return constants.ErrPublicFeedProofCatalogMismatch
		}
		if !safePublicProofFilename(entry.Filename) || entry.ImmutableURL != "/proofs/"+entry.ArtifactID {
			return constants.ErrPublicFeedProofPathTraversal
		}
		referenced[entry.ArtifactID] = true
		content, ok := inlineArtifacts[entry.ArtifactID]
		if !ok {
			size, stored := storedSizes[entry.ArtifactID]
			if !stored {
				missing = append(missing, entry.ArtifactID)
				continue
			}
			if size > constants.PublicFeedMaxArtifactBytes || size != entry.ByteSize {
				return constants.ErrPublicFeedProofSizeMismatch
			}
			continue
		}
		if len(content) > constants.PublicFeedMaxArtifactBytes || int64(len(content)) != entry.ByteSize {
			return constants.ErrPublicFeedProofSizeMismatch
		}
		hash := sha256.Sum256(content)
		if entry.SHA256 != hex.EncodeToString(hash[:]) {
			return constants.ErrPublicFeedProofHashMismatch
		}
	}
	for artifactID := range inlineArtifacts {
		if !referenced[artifactID] {
			return constants.ErrPublicFeedProofCatalogMismatch
		}
	}
	if len(missing) > 0 {
		return &publicProofMissingArtifactsError{artifactIDs: missing}
	}
	return nil
}

func safePublicProofFilename(filename string) bool {
	if filename == "" || path.IsAbs(filename) || strings.Contains(filename, "\\") {
		return false
	}
	cleaned := path.Clean(filename)
	return cleaned == filename && cleaned != "." && cleaned != ".." && path.Base(cleaned) == cleaned
}

// writeStoreUnavailable reports a mirror store read failure.
func (m *PublicMirrorServer) writeStoreUnavailable(w http.ResponseWriter, message string, err error) {
	m.logger.Error(message, "error", err)
	http.Error(w, "mirror store unavailable", http.StatusInternalServerError)
}

// handleBootstrap handles GET /bootstrap — the coherent current read model.
func (m *PublicMirrorServer) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	bootstrap, err := m.currentBootstrap(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read current snapshot", err)
		return
	}
	m.writeJSON(w, http.StatusOK, bootstrap)
}

// currentBootstrap captures records, proofs, withdrawals and cursor under the
// same mutation lock, releasing it before writing to a potentially slow client.
func (m *PublicMirrorServer) currentBootstrap(ctx context.Context, requestedSource string) (models.PublicFeedBootstrap, error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	sourceID, summary := m.sourceView(requestedSource)
	if summary == nil {
		return models.PublicFeedBootstrap{
			ProtocolVersion:     constants.PublicFeedProtocolVersion,
			Snapshot:            offlinePublicMirrorSnapshot(sourceID),
			SourceFreshness:     models.CampaignFreshnessSourceOffline,
			RecentProjections:   []models.PublicFeedObject{},
			ProofCatalogSummary: models.PublicProofCatalogSummary{ArtifactCount: 0, TotalByteSize: 0},
			GeneratedAt:         time.Now().UTC(),
		}, nil
	}

	records, err := m.store.snapshotRecords(ctx, sourceID)
	if err != nil {
		return models.PublicFeedBootstrap{}, err
	}
	// Preserve the transport's newest-first ordering. Unlike a history tail,
	// these are the latest records for every current entity plus recent activity.
	recent := make([]models.PublicFeedObject, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		var item models.PublicFeedObject
		if err := json.Unmarshal([]byte(records[i].RecordBytes), &item); err != nil {
			return models.PublicFeedBootstrap{}, fmt.Errorf("mirror: decode snapshot record: %w", err)
		}
		item.SetInt64Field("sequence", records[i].Sequence)
		recent = append(recent, item)
	}
	proofSummary, hasProof, err := m.store.proofSummary(ctx, sourceID)
	if err != nil {
		return models.PublicFeedBootstrap{}, err
	}
	catalogSummary := models.PublicProofCatalogSummary{ArtifactCount: 0, TotalByteSize: 0}
	if hasProof && proofSummary.ArtifactCount > 0 {
		catalogSummary.ArtifactCount = proofSummary.ArtifactCount
		catalogSummary.TotalByteSize = proofSummary.TotalByteSize
		catalogSummary.LastGeneratedAt = &proofSummary.GeneratedAt
	}

	return models.PublicFeedBootstrap{
		ProtocolVersion:     constants.PublicFeedProtocolVersion,
		Snapshot:            m.snapshotOf(sourceID, summary),
		SourceFreshness:     m.sourceFreshness(summary),
		RecentProjections:   recent,
		ProofCatalogSummary: catalogSummary,
		GeneratedAt:         time.Now().UTC(),
	}, nil
}

// handleSnapshot handles GET /snapshot — current high-water sequence and
// feed-chain hash.
func (m *PublicMirrorServer) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sourceID, summary := m.sourceView(r.URL.Query().Get("source"))
	if summary == nil {
		m.writeJSON(w, http.StatusOK, offlinePublicMirrorSnapshot(sourceID))
		return
	}

	if sequenceText := r.URL.Query().Get("sequence"); sequenceText != "" {
		sequence, err := strconv.ParseInt(sequenceText, 10, 64)
		if err != nil || sequence <= 0 {
			http.Error(w, constants.ErrPublicFeedSnapshotNotFound.Error(), http.StatusNotFound)
			return
		}
		contentHash, later, found, err := m.store.batchAnchor(r.Context(), sourceID, sequence, summary.HighWaterSequence)
		if err != nil {
			m.writeStoreUnavailable(w, "mirror: read snapshot anchor", err)
			return
		}
		if !found {
			http.Error(w, constants.ErrPublicFeedSnapshotNotFound.Error(), http.StatusNotFound)
			return
		}
		anchor := m.snapshotOf(sourceID, summary)
		anchor.HighWaterSequence = sequence
		anchor.FeedChainHash = contentHash
		anchor.BatchCount = summary.BatchCount - later
		m.writeJSON(w, http.StatusOK, anchor)
		return
	}

	m.writeJSON(w, http.StatusOK, m.snapshotOf(sourceID, summary))
}

// handleHistory handles GET /history — cursor-paginated page of public
// records. The cursor is the last sequence number seen; the response
// returns records after the cursor up to the page limit. Optional kind and
// dataset parameters keep only records whose top-level kind and dataset_id
// match, so a single-dataset lookup never pages the whole feed.
func (m *PublicMirrorServer) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cursorStr := r.URL.Query().Get("cursor")
	limitStr := r.URL.Query().Get("limit")
	recordKind := r.URL.Query().Get("kind")
	datasetID := r.URL.Query().Get("dataset")

	limit := m.defaultPageSize
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			limit = n
			if limit > m.maxPageSize {
				limit = m.maxPageSize
			}
		}
	}
	cursor := int64(0)
	if cursorStr != "" {
		if n, err := strconv.ParseInt(cursorStr, 10, 64); err == nil {
			cursor = n
		}
	}

	sourceID, summary := m.sourceView(r.URL.Query().Get("source"))
	if summary == nil {
		m.writeJSON(w, http.StatusOK, models.PublicFeedCursorPage{
			ProtocolVersion: constants.PublicFeedProtocolVersion,
			Items:           []models.PublicFeedObject{},
			HasMore:         false,
			Limit:           limit,
		})
		return
	}

	items := []models.PublicFeedObject{}
	var lastSeq int64
	hasMore := false
	pageSize := max(limit+1, publicMirrorReadPageRecords)
	after := cursor
scan:
	for {
		records, err := m.store.recordsAfter(r.Context(), sourceID, after, summary.HighWaterSequence, pageSize)
		if err != nil {
			m.writeStoreUnavailable(w, "mirror: read history", err)
			return
		}
		for _, rec := range records {
			if mirrorRecordWithdrawn(summary.WithdrawnDatasetIDs, rec) {
				continue
			}
			var item models.PublicFeedObject
			if err := json.Unmarshal([]byte(rec.RecordBytes), &item); err != nil {
				continue
			}
			if recordKind != "" {
				kind, ok := item.StringField("kind")
				if !ok || kind != recordKind {
					continue
				}
			}
			if datasetID != "" {
				id, ok := item.StringField("dataset_id")
				if !ok || id != datasetID {
					continue
				}
			}
			if len(items) >= limit {
				hasMore = true
				break scan
			}
			item.SetInt64Field("sequence", rec.Sequence)
			item["record_type"] = json.RawMessage(strconv.Quote(string(rec.RecordType)))
			items = append(items, item)
			lastSeq = rec.Sequence
		}
		if len(records) < pageSize {
			break
		}
		after = records[len(records)-1].Sequence
	}

	nextCursor := ""
	if hasMore {
		nextCursor = strconv.FormatInt(lastSeq, 10)
	}

	m.writeJSON(w, http.StatusOK, models.PublicFeedCursorPage{
		ProtocolVersion: constants.PublicFeedProtocolVersion,
		Items:           items,
		Cursor:          nextCursor,
		HasMore:         hasMore,
		Limit:           limit,
	})
}

// handleStream handles GET /stream — replayable SSE. The client connects with
// an optional since_id query parameter (or Last-Event-ID header) to resume
// from a specific sequence. The server subscribes first, then sends a
// snapshot and the retained records after the cursor through that snapshot,
// then live records past it. A bounded queue prevents unbounded memory
// growth; overflow sends a truncation sentinel.
func (m *PublicMirrorServer) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	requestedSource := r.URL.Query().Get("source")
	sinceIDStr := r.URL.Query().Get("since_id")
	if sinceIDStr == "" {
		sinceIDStr = r.Header.Get("Last-Event-ID")
	}
	sinceID := int64(0)
	if sinceIDStr != "" {
		if n, err := strconv.ParseInt(sinceIDStr, 10, 64); err == nil {
			sinceID = n
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	sourceID, summary := m.sourceView(requestedSource)
	var sub *mirrorSSESubscriber
	if summary != nil || requestedSource != "" {
		var accepted bool
		sub, accepted = m.addSSESubscriber(sourceID)
		if !accepted {
			sseWriteSentinel(w, flusher, "error", "stream capacity reached")
			flusher.Flush()
			return
		}
		defer m.removeSSESubscriber(sub)
		// Re-read after subscribing so every record accepted from here on is
		// either replayed below or delivered live.
		_, summary = m.sourceView(sourceID)
	}

	// Send snapshot event first.
	if summary == nil {
		sseWriteEvent(w, flusher, "snapshot", offlinePublicMirrorSnapshot(sourceID))
	} else {
		sseWriteEvent(w, flusher, "snapshot", m.snapshotOf(sourceID, summary))
	}

	// Replay retained records through the snapshot, oldest first, bounded so
	// reconnects do not replay the full retained feed over SSE.
	skipThrough := sinceID
	if summary != nil {
		m.mu.RLock()
		maxReplay := m.maxSSEReplayRecords
		m.mu.RUnlock()
		skipThrough = max(sinceID, summary.HighWaterSequence)
		truncated, err := m.replayRecords(r.Context(), w, flusher, sourceID, summary, sinceID, maxReplay)
		if err != nil {
			m.logger.Error("mirror: replay stream records", "source_id", sourceID, "error", err)
			sseWriteSentinel(w, flusher, "error", "replay unavailable")
			return
		}
		if truncated {
			sseWriteSentinel(w, flusher, "truncated", "replay limit reached")
		}
	}

	if sub == nil {
		sseWriteSentinel(w, flusher, "error", "source not found")
		flusher.Flush()
		return
	}

	ctx := r.Context()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive.C:
			sseWriteComment(w, flusher)
		case <-sub.resync:
			_, current := m.sourceView(sourceID)
			if current == nil {
				sseWriteEvent(w, flusher, "snapshot", offlinePublicMirrorSnapshot(sourceID))
			} else {
				sseWriteEvent(w, flusher, "snapshot", m.snapshotOf(sourceID, current))
			}
		case rec, open := <-sub.ch:
			if !open {
				sseWriteSentinel(w, flusher, "error", "stream closed")
				flusher.Flush()
				return
			}
			if sub.consumeTruncated() {
				sseWriteSentinel(w, flusher, "truncated", "subscriber buffer overflow")
			}
			if rec.Sequence <= skipThrough {
				continue
			}
			sseWriteRecord(w, flusher, rec)
		}
	}
}

// replayRecords streams the retained, non-withdrawn records after sinceID
// through the summary high-water mark, at most maxReplay when positive, and
// reports whether more remained.
func (m *PublicMirrorServer) replayRecords(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, sourceID string, summary *PublicMirrorSourceSummary, sinceID int64, maxReplay int) (bool, error) {
	sent := 0
	after := sinceID
	for {
		records, err := m.store.recordsAfter(ctx, sourceID, after, summary.HighWaterSequence, publicMirrorReadPageRecords)
		if err != nil {
			return false, err
		}
		for _, rec := range records {
			if mirrorRecordWithdrawn(summary.WithdrawnDatasetIDs, rec) {
				continue
			}
			if maxReplay > 0 && sent >= maxReplay {
				return true, nil
			}
			sseWriteRecord(w, flusher, rec)
			sent++
		}
		if len(records) < publicMirrorReadPageRecords {
			return false, nil
		}
		after = records[len(records)-1].Sequence
	}
}

// resolveProofManifestSource returns the source whose proof manifest a read
// serves: the requested one, else the active source's, else the lowest.
func (m *PublicMirrorServer) resolveProofManifestSource(ctx context.Context, requested string) (string, bool, error) {
	if requested != "" {
		return requested, true, nil
	}
	m.mu.RLock()
	active := m.activeSourceIDLocked()
	m.mu.RUnlock()
	return m.store.proofManifestSourceID(ctx, active)
}

// handleProofCatalog handles GET /proof-catalog — the full public proof
// catalog, derived from the stored signed manifest. The optional artifact
// parameter narrows it to that one active entry, so a reader can resolve a
// single evidence binding without fetching the whole catalog.
func (m *PublicMirrorServer) handleProofCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	emptyCatalog := models.PublicProofCatalog{
		SchemaVersion: constants.PublicProofCatalogSchemaVersion,
		Entries:       []models.PublicProofCatalogEntry{},
		GeneratedAt:   time.Now().UTC(),
	}
	artifactID := r.URL.Query().Get("artifact")
	if r.URL.Query().Has("artifact") && !validPublicProofArtifactID(artifactID) {
		http.Error(w, "artifact must be a lowercase sha256 hex digest", http.StatusBadRequest)
		return
	}
	sourceID, ok, err := m.resolveProofManifestSource(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: resolve proof catalog source", err)
		return
	}
	if !ok {
		m.writeJSON(w, http.StatusOK, emptyCatalog)
		return
	}
	if artifactID != "" {
		entry, found, err := m.store.proofCatalogEntry(r.Context(), sourceID, artifactID)
		if err != nil {
			m.writeStoreUnavailable(w, "mirror: read proof catalog entry", err)
			return
		}
		if found {
			emptyCatalog.Entries = append(emptyCatalog.Entries, entry)
		}
		m.writeJSON(w, http.StatusOK, emptyCatalog)
		return
	}
	manifest, found, err := m.store.ProofManifest(r.Context(), sourceID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read proof catalog", err)
		return
	}
	if !found {
		m.writeJSON(w, http.StatusOK, emptyCatalog)
		return
	}
	catalog := proofCatalogFromManifest(manifest)
	if catalog.Entries == nil {
		catalog.Entries = []models.PublicProofCatalogEntry{}
	}
	m.writeJSON(w, http.StatusOK, catalog)
}

// handleProofRoot handles GET /proof-root?source= on the private ingest
// listener: the source's stored proof root and artifact count, so the
// publisher can decide what to push without fetching the full manifest.
func (m *PublicMirrorServer) handleProofRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := m.ingestAuthorized(r); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sourceID := r.URL.Query().Get("source")
	if sourceID == "" {
		http.Error(w, "source is required", http.StatusBadRequest)
		return
	}
	summary, found, err := m.store.proofSummary(r.Context(), sourceID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read proof root", err)
		return
	}
	if !found {
		http.Error(w, "proof manifest not found", http.StatusNotFound)
		return
	}
	m.writeJSON(w, http.StatusOK, models.PublicProofRootResponse{
		SourceID:        sourceID,
		SegmentIndex:    summary.SegmentIndex,
		ProofRootSHA256: summary.ProofRootSHA256,
		ArtifactCount:   summary.ArtifactCount,
	})
}

// handleProofManifest handles GET /proof-manifest — the proof root manifest.
func (m *PublicMirrorServer) handleProofManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sourceID, ok, err := m.resolveProofManifestSource(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: resolve proof manifest source", err)
		return
	}
	if !ok {
		http.Error(w, "proof manifest not found", http.StatusNotFound)
		return
	}
	manifest, found, err := m.store.ProofManifest(r.Context(), sourceID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read proof manifest", err)
		return
	}
	if !found {
		http.Error(w, "proof manifest not found", http.StatusNotFound)
		return
	}
	m.writeJSON(w, http.StatusOK, manifest)
}

// handleProofSegments handles GET /proof-segments — the list of chained proof manifest segments.
func (m *PublicMirrorServer) handleProofSegments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	emptySegments := models.PublicProofSegmentsResponse{
		SchemaVersion: constants.PublicProofSegmentSchemaVersion,
		SourceID:      "",
		Segments:      []models.PublicProofManifestSegment{},
	}
	sourceID, ok, err := m.resolveProofManifestSource(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: resolve proof segments source", err)
		return
	}
	if !ok {
		m.writeJSON(w, http.StatusOK, emptySegments)
		return
	}
	emptySegments.SourceID = sourceID
	segments, err := m.store.ProofSegments(r.Context(), sourceID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read proof segments", err)
		return
	}
	if segments == nil {
		segments = []models.PublicProofManifestSegment{}
	}
	m.writeJSON(w, http.StatusOK, models.PublicProofSegmentsResponse{
		SchemaVersion: constants.PublicProofSegmentSchemaVersion,
		SourceID:      sourceID,
		Segments:      segments,
	})
}

// handleProofDownload handles GET /proofs/:artifactID — content-addressed
// proof artifact download with safe headers. The stored file is rehashed
// before any byte is served.
func (m *PublicMirrorServer) handleProofDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	artifactID := strings.TrimPrefix(r.URL.Path, "/proofs/")
	if artifactID == "" {
		http.Error(w, "artifact id required", http.StatusBadRequest)
		return
	}
	if !validPublicProofArtifactID(artifactID) {
		http.Error(w, "proof not found", http.StatusNotFound)
		return
	}
	artifact, found, err := m.store.proofArtifact(r.Context(), artifactID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read proof artifact", err)
		return
	}
	if !found {
		http.Error(w, "proof not found", http.StatusNotFound)
		return
	}
	file, err := m.store.openProofArtifact(r.Context(), artifactID)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: open proof artifact", err)
		return
	}
	defer file.Close()

	// Verify hash before serving.
	hash := sha256.New()
	hashed, err := io.Copy(hash, file)
	if err != nil {
		m.writeStoreUnavailable(w, "mirror: read proof artifact", err)
		return
	}
	if hashed != artifact.ByteSize || hex.EncodeToString(hash.Sum(nil)) != artifactID {
		http.Error(w, "proof hash mismatch", http.StatusInternalServerError)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		m.writeStoreUnavailable(w, "mirror: rewind proof artifact", err)
		return
	}

	if artifact.Entry != nil {
		w.Header().Set("Content-Type", artifact.Entry.MediaType)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", artifact.Entry.Filename))
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.ByteSize, 10))
	if _, err := io.CopyN(w, file, artifact.ByteSize); err != nil {
		m.logResponseError("mirror: write proof response", err)
	}
}

// offlinePublicMirrorSnapshot is the snapshot of a source the mirror has
// never accepted a batch from.
func offlinePublicMirrorSnapshot(sourceID string) models.PublicFeedSnapshot {
	return models.PublicFeedSnapshot{
		ProtocolVersion:   constants.PublicFeedProtocolVersion,
		SourceID:          sourceID,
		HighWaterSequence: 0,
		FeedChainHash:     constants.PublicFeedZeroHashHex,
		BatchCount:        0,
		WithdrawalDigest:  constants.PublicFeedZeroHashHex,
		GeneratedAt:       time.Now().UTC(),
		Freshness:         models.CampaignFreshnessSourceOffline,
	}
}

// snapshotOf returns the snapshot of one accepted source summary. The caller
// must not hold m.mu.
func (m *PublicMirrorServer) snapshotOf(sourceID string, summary *PublicMirrorSourceSummary) models.PublicFeedSnapshot {
	return models.PublicFeedSnapshot{
		ProtocolVersion:   constants.PublicFeedProtocolVersion,
		SourceID:          sourceID,
		HighWaterSequence: summary.HighWaterSequence,
		FeedChainHash:     summary.FeedChainHash,
		BatchCount:        summary.BatchCount,
		WithdrawalDigest:  mirrorWithdrawalDigest(summary.WithdrawnDatasetIDs),
		GeneratedAt:       time.Now().UTC(),
		Freshness:         m.sourceFreshness(summary),
	}
}

// send delivers a record to the subscriber's channel. If the channel is
// full, the record is dropped (the subscriber will see a truncation
// sentinel on its next read).
func (s *mirrorSSESubscriber) send(record models.PublicFeedRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- record:
	default:
		select {
		case <-s.ch:
		default:
		}
		select {
		case s.ch <- record:
		default:
		}
		s.truncated = true
	}
}

func (s *mirrorSSESubscriber) consumeTruncated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	truncated := s.truncated
	s.truncated = false
	return truncated
}

// close closes the subscriber's channel.
func (s *mirrorSSESubscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

// writeJSON writes a JSON response with the given status code.
func (m *PublicMirrorServer) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		m.logResponseError("mirror: encode JSON response", err)
	}
}

func (m *PublicMirrorServer) logResponseError(message string, err error) {
	level := slog.LevelError
	if logging.IsClientDisconnect(err) {
		level = slog.LevelDebug
	}
	m.logger.Log(context.Background(), level, message, logging.ErrorKind(err))
}

// sseWriteEvent writes a named SSE event with a JSON payload.
func sseWriteEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, payload any) { //nolint:unparam // Keep eventType parameter for future SSE event kinds.
	data, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(data))
	flusher.Flush()
}

// sseWriteRecord writes a public feed record as an SSE data event with the
// record sequence as the event ID.
func sseWriteRecord(w http.ResponseWriter, flusher http.Flusher, rec models.PublicFeedRecord) {
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", rec.Sequence, rec.RecordType, rec.RecordBytes)
	flusher.Flush()
}

// sseWriteSentinel writes a sentinel SSE event (truncated or error).
func sseWriteSentinel(w http.ResponseWriter, flusher http.Flusher, sentinelType, reason string) {
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", sentinelType, string(payload))
	flusher.Flush()
}

// sseWriteComment emits an SSE comment keepalive so intermediaries such as
// Cloudflare do not treat idle streams as dead QUIC connections.
func sseWriteComment(w http.ResponseWriter, flusher http.Flusher) {
	fmt.Fprintf(w, ": keepalive\n\n")
	flusher.Flush()
}

// GetSourceState returns one source's chain position and retained record
// count for testing. Returns zero values if the source is unknown.
func (m *PublicMirrorServer) GetSourceState(sourceID string) (highWater int64, feedChainHash string, batchCount int, recordCount int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	summary, ok := m.sources[sourceID]
	if !ok {
		return 0, "", 0, 0
	}
	return summary.HighWaterSequence, summary.FeedChainHash, summary.BatchCount, summary.RetainedRecordCount
}

// InjectBatch directly stores a batch without validation. Used by tests that
// need to set up mirror state without going through the ingest endpoint.
func (m *PublicMirrorServer) InjectBatch(ctx context.Context, batch models.PublicFeedBatch) error {
	return m.acceptBatch(ctx, batch)
}

// CloseAllSubscribers closes all SSE subscribers. Used for clean shutdown.
func (m *PublicMirrorServer) CloseAllSubscribers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for sub := range m.sseSubscribers {
		sub.close()
		delete(m.sseSubscribers, sub)
	}
}

// FlushForTests waits for pending operations to settle. Used in tests after
// ingest to ensure records are visible before reading.
func (m *PublicMirrorServer) FlushForTests() {
	// The acceptBatch method commits to the store and updates state
	// synchronously, so no flush is needed. This method exists for future
	// async paths.
	_ = context.Background()
}
