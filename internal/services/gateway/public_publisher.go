// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 0.

package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/assignmentaudit"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/publicdisclosure"
)

// ProofArtifactInput is the input type for the proof package builder. Proof
// artifacts are published verbatim; content is never screened or redacted.
type ProofArtifactInput struct {
	Filename            string
	MediaType           string
	Content             []byte
	CampaignID          string
	SourceRunID         string
	VerificationCommand string
}

type PublicOutboxStore interface {
	Append(context.Context, models.PublicOutboxEntry) error
	List(context.Context) ([]models.PublicOutboxEntry, error)
	Update(context.Context, models.PublicOutboxEntry) error
	PruneAcknowledged(context.Context, time.Time) error
	Replace(context.Context, []models.PublicOutboxEntry) error
}

type runtimePublicOutboxStore struct {
	fileSvc fs.RuntimeFileService
	mu      sync.Mutex
}

func newRuntimePublicOutboxStore(fileSvc fs.RuntimeFileService) PublicOutboxStore {
	return &runtimePublicOutboxStore{fileSvc: fileSvc}
}

func (s *runtimePublicOutboxStore) Append(ctx context.Context, entry models.PublicOutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.read(ctx)
	if err != nil {
		return err
	}
	for _, existing := range entries {
		if existing.Sequence != entry.Sequence {
			continue
		}
		if existing.BatchHash != entry.BatchHash || existing.BatchBytes != entry.BatchBytes {
			return constants.ErrPublicFeedOutboxEquivocation
		}
		return constants.ErrPublicFeedDuplicateSequence
	}
	if err := validatePublicOutboxEntry(entry); err != nil {
		return err
	}
	if len(entries) > 0 {
		last := entries[len(entries)-1]
		var batch models.PublicFeedBatch
		if err := json.Unmarshal([]byte(entry.BatchBytes), &batch); err != nil {
			return fmt.Errorf("%w: decode appended batch: %v", constants.ErrPublicFeedOutboxCorrupt, err)
		}
		if entry.Sequence <= last.Sequence {
			return constants.ErrPublicFeedSequenceOutOfOrder
		}
		if batch.FirstSequence != last.Sequence+1 || batch.PreviousBatchHash != last.BatchHash {
			return constants.ErrPublicFeedHashChainMismatch
		}
	}
	entries = append(entries, entry)
	return s.write(ctx, entries)
}

func (s *runtimePublicOutboxStore) List(ctx context.Context) ([]models.PublicOutboxEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(ctx)
}

func (s *runtimePublicOutboxStore) Update(ctx context.Context, entry models.PublicOutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.read(ctx)
	if err != nil {
		return err
	}
	found := false
	for i := range entries {
		if entries[i].Sequence != entry.Sequence {
			continue
		}
		if entries[i].BatchHash != entry.BatchHash || entries[i].BatchBytes != entry.BatchBytes {
			return constants.ErrPublicFeedOutboxEquivocation
		}
		entries[i] = entry
		found = true
		break
	}
	if !found {
		return constants.ErrPublicFeedOutboxCorrupt
	}
	return s.write(ctx, entries)
}

func (s *runtimePublicOutboxStore) Replace(ctx context.Context, entries []models.PublicOutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(ctx, entries)
}

func (s *runtimePublicOutboxStore) readRaw(ctx context.Context) ([]models.PublicOutboxEntry, error) {
	exists, err := s.fileSvc.FileExists(ctx, constants.PublicFeedOutboxPath)
	if err != nil {
		return nil, fmt.Errorf("public-feed: outbox stat: %w", err)
	}
	if !exists {
		return []models.PublicOutboxEntry{}, nil
	}
	data, err := s.fileSvc.ReadFile(ctx, constants.PublicFeedOutboxPath)
	if err != nil {
		return nil, fmt.Errorf("public-feed: outbox read: %w", err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	entries := make([]models.PublicOutboxEntry, 0, len(lines))
	for index, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry models.PublicOutboxEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("%w: line %d: %v", constants.ErrPublicFeedOutboxCorrupt, index+1, err)
		}
		if err := validatePublicOutboxEntry(entry); err != nil {
			return nil, fmt.Errorf("%w: line %d: %v", constants.ErrPublicFeedOutboxCorrupt, index+1, err)
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Sequence < entries[j].Sequence })
	return entries, nil
}

func (s *runtimePublicOutboxStore) PruneAcknowledged(ctx context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.read(ctx)
	if err != nil {
		return err
	}
	pruned := 0
	for len(entries) > 0 {
		head := entries[0]
		if head.Status != models.PublicFeedOutboxStatusAcknowledged || head.AcknowledgedAt == nil || !head.AcknowledgedAt.Before(before) {
			break
		}
		entries = entries[1:]
		pruned++
	}
	if pruned == 0 {
		return nil
	}
	return s.write(ctx, entries)
}

func (s *runtimePublicOutboxStore) read(ctx context.Context) ([]models.PublicOutboxEntry, error) {
	exists, err := s.fileSvc.FileExists(ctx, constants.PublicFeedOutboxPath)
	if err != nil {
		return nil, fmt.Errorf("public-feed: outbox stat: %w", err)
	}
	if !exists {
		return []models.PublicOutboxEntry{}, nil
	}
	data, err := s.fileSvc.ReadFile(ctx, constants.PublicFeedOutboxPath)
	if err != nil {
		return nil, fmt.Errorf("public-feed: outbox read: %w", err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	entries := make([]models.PublicOutboxEntry, 0, len(lines))
	for index, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry models.PublicOutboxEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("%w: line %d: %v", constants.ErrPublicFeedOutboxCorrupt, index+1, err)
		}
		if err := validatePublicOutboxEntry(entry); err != nil {
			return nil, fmt.Errorf("%w: line %d: %v", constants.ErrPublicFeedOutboxCorrupt, index+1, err)
		}
		var batch models.PublicFeedBatch
		if err := json.Unmarshal([]byte(entry.BatchBytes), &batch); err != nil {
			return nil, fmt.Errorf("%w: line %d batch: %v", constants.ErrPublicFeedOutboxCorrupt, index+1, err)
		}
		if len(entries) > 0 {
			previous := entries[len(entries)-1]
			if entry.Sequence == previous.Sequence && (entry.BatchHash != previous.BatchHash || entry.BatchBytes != previous.BatchBytes) {
				return nil, constants.ErrPublicFeedOutboxEquivocation
			}
			if entry.Sequence <= previous.Sequence {
				return nil, constants.ErrPublicFeedOutboxCorrupt
			}
			if batch.FirstSequence != previous.Sequence+1 || batch.PreviousBatchHash != previous.BatchHash {
				return nil, constants.ErrPublicFeedHashChainMismatch
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *runtimePublicOutboxStore) write(ctx context.Context, entries []models.PublicOutboxEntry) error {
	var data bytes.Buffer
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("public-feed: outbox marshal: %w", err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	if err := s.fileSvc.WriteFile(ctx, constants.PublicFeedOutboxPath, data.Bytes(), constants.PermFilePrivate); err != nil {
		return fmt.Errorf("public-feed: outbox write: %w", err)
	}
	return nil
}

func validatePublicOutboxEntry(entry models.PublicOutboxEntry) error {
	var batch models.PublicFeedBatch
	if err := json.Unmarshal([]byte(entry.BatchBytes), &batch); err != nil {
		return fmt.Errorf("%w: decode batch: %v", constants.ErrPublicFeedOutboxCorrupt, err)
	}
	if entry.Sequence <= 0 || batch.LastSequence != entry.Sequence || batch.ContentHash != entry.BatchHash {
		return constants.ErrPublicFeedOutboxCorrupt
	}
	if computeBatchContentHash(batch) != batch.ContentHash {
		return constants.ErrPublicFeedContentHashMismatch
	}
	return nil
}

// PublicPublisherService is the Gateway outbound publisher. It consumes
// persisted O0-allowlisted projections, builds signed append-only batches,
// durably writes an ordered outbox, and transmits to a hosted mirror over
// outbound HTTPS with idempotent retry. Export never mutates authoritative
// eval reports.
type PublicPublisherService struct {
	docStore *DocumentStoreService
	fileSvc  fs.RuntimeFileService
	outbox   PublicOutboxStore
	logger   *slog.Logger
	cfg      models.PublicExportConfig

	signingPrivKey ed25519.PrivateKey
	signingPubKey  ed25519.PublicKey
	signingKeyID   string

	// proofPushMu serializes proof delivery. It is taken before proofMu.
	proofPushMu sync.Mutex
	// proofMu guards proofIndex and serializes proof log appends. It is taken
	// before mu.
	proofMu    sync.Mutex
	proofIndex *publicProofIndex

	mu              sync.Mutex
	mirrorOrigin    string
	ingestAuthToken string
	batchMaxRecords int
	// assignmentEvents supplies the signed event records assignment audit
	// databases are built from. It is the gateway's own mirror store.
	assignmentEvents PublicAssignmentEventSource

	// highWaterSeq is the last acknowledged sequence number.
	highWaterSeq  int64
	feedChainHash string
	batchCount    int
}

// NewPublicPublisherService creates a new PublicPublisherService backed by
// the given document store, file service, logger, export config, Ed25519
// private key, and signing key ID.
func NewPublicPublisherService(docStore *DocumentStoreService, fileSvc fs.RuntimeFileService, logger *slog.Logger, cfg models.PublicExportConfig, priv ed25519.PrivateKey, keyID string) *PublicPublisherService {
	pub := priv.Public().(ed25519.PublicKey)
	return &PublicPublisherService{
		docStore:        docStore,
		fileSvc:         fileSvc,
		outbox:          newRuntimePublicOutboxStore(fileSvc),
		logger:          logger,
		cfg:             cfg,
		signingPrivKey:  priv,
		signingPubKey:   pub,
		signingKeyID:    keyID,
		proofIndex:      newPublicProofIndex(),
		mirrorOrigin:    cfg.MirrorOrigin,
		batchMaxRecords: cfg.BatchMaxRecords,
		feedChainHash:   constants.PublicFeedZeroHashHex,
	}
}

// SetMirrorOrigin updates the mirror origin URL.
func (s *PublicPublisherService) SetMirrorOrigin(origin string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mirrorOrigin = origin
}

func (s *PublicPublisherService) SetIngestAuthToken(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ingestAuthToken = token
}

// SetBatchMaxRecords updates the maximum records per batch limit.
func (s *PublicPublisherService) SetBatchMaxRecords(max int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batchMaxRecords = max
}

// PublicAssignmentEventSource returns the signed event records retained for one
// run's assignment in one source, oldest first. The gateway's public mirror
// store implements it.
type PublicAssignmentEventSource interface {
	AssignmentEventRecords(ctx context.Context, sourceID, runID, assignmentID string) ([]models.PublicFeedRecord, error)
}

// SetAssignmentEventSource sets where assignment audit databases read their
// signed event records. Publishing an assignment fails closed without one.
func (s *PublicPublisherService) SetAssignmentEventSource(source PublicAssignmentEventSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.assignmentEvents = source
}

// validRecordType returns true if the record type is in the closed vocabulary.
func validRecordType(rt models.PublicFeedRecordType) bool {
	switch rt {
	case models.PublicFeedRecordTypeProjection,
		models.PublicFeedRecordTypeEvent,
		models.PublicFeedRecordTypeProofManifest,
		models.PublicFeedRecordTypeKeyRevocation:
		return true
	default:
		return false
	}
}

// BuildBatch constructs a signed batch from records. It validates record
// hashes, computes the content hash, and signs with Ed25519. The batch
// carries the previous batch hash for chain linking.
func (s *PublicPublisherService) BuildBatch(records []models.PublicFeedRecord) (models.PublicFeedBatch, error) {
	if len(records) == 0 {
		return models.PublicFeedBatch{}, constants.ErrPublicFeedBatchEmpty
	}
	if len(records) > s.batchMaxRecords {
		return models.PublicFeedBatch{}, constants.ErrPublicFeedBatchOversized
	}

	// Validate each record.
	for _, r := range records {
		if !validRecordType(r.RecordType) {
			return models.PublicFeedBatch{}, fmt.Errorf("public-feed: build batch: %w: %s", constants.ErrPublicFeedRecordTypeInvalid, r.RecordType)
		}
		computed := sha256.Sum256([]byte(r.RecordBytes))
		if hex.EncodeToString(computed[:]) != r.RecordHash {
			return models.PublicFeedBatch{}, constants.ErrPublicFeedRecordHashMismatch
		}
		if err := publicdisclosure.ValidatePublicFeedRecord(r.RecordType, []byte(r.RecordBytes)); err != nil {
			return models.PublicFeedBatch{}, fmt.Errorf("public-feed: build record: %w", err)
		}
	}

	// Compute batch size in bytes.
	var totalBytes int
	for _, r := range records {
		totalBytes += len(r.RecordBytes)
	}
	if totalBytes > s.cfg.BatchMaxBytes {
		return models.PublicFeedBatch{}, constants.ErrPublicFeedBatchOversized
	}

	recordHashes := make([]string, len(records))
	for i, r := range records {
		recordHashes[i] = r.RecordHash
	}

	s.mu.Lock()
	prevHash := s.feedChainHash
	s.mu.Unlock()

	batch := models.PublicFeedBatch{
		ProtocolVersion:   constants.PublicFeedProtocolVersion,
		SchemaVersion:     constants.PublicFeedSchemaVersion,
		SourceID:          s.cfg.SourceID,
		FirstSequence:     records[0].Sequence,
		LastSequence:      records[len(records)-1].Sequence,
		PreviousBatchHash: prevHash,
		RecordHashes:      recordHashes,
		GeneratedAt:       time.Now().UTC(),
		SigningKeyID:      s.signingKeyID,
		Records:           records,
	}

	// Compute content hash over the canonical batch fields (excluding signature).
	contentHash := s.computeBatchContentHash(batch)
	batch.ContentHash = contentHash

	// Sign the decoded content hash bytes with Ed25519.
	contentHashBytes, err := hex.DecodeString(contentHash)
	if err != nil {
		return models.PublicFeedBatch{}, fmt.Errorf("public-feed: build batch: decode content hash: %w", err)
	}
	sig := ed25519.Sign(s.signingPrivKey, contentHashBytes)
	batch.Signature = hex.EncodeToString(sig)

	return batch, nil
}

// computeBatchContentHash delegates to the package-level computeBatchContentHash.
func (s *PublicPublisherService) computeBatchContentHash(batch models.PublicFeedBatch) string {
	return computeBatchContentHash(batch)
}

// VerifyBatch verifies the batch signature and content hash against the
// provided public key hex string.
func (s *PublicPublisherService) VerifyBatch(batch models.PublicFeedBatch, pubKeyHex string) error {
	// Recompute content hash.
	computed := s.computeBatchContentHash(batch)
	if computed != batch.ContentHash {
		return constants.ErrPublicFeedContentHashMismatch
	}

	// Verify signature.
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		return fmt.Errorf("public-feed: verify batch: decode public key: %w", err)
	}
	sigBytes, err := hex.DecodeString(batch.Signature)
	if err != nil {
		return fmt.Errorf("public-feed: verify batch: decode signature: %w", err)
	}
	contentHashBytes, err := hex.DecodeString(batch.ContentHash)
	if err != nil {
		return fmt.Errorf("public-feed: verify batch: decode content hash: %w", err)
	}
	if !ed25519.Verify(pubKeyBytes, contentHashBytes, sigBytes) {
		return constants.ErrPublicFeedSignatureInvalid
	}
	return nil
}

func isRepairableOutboxError(err error) bool {
	return errors.Is(err, constants.ErrPublicFeedOutboxCorrupt) ||
		errors.Is(err, constants.ErrPublicFeedOutboxEquivocation) ||
		errors.Is(err, constants.ErrPublicFeedHashChainMismatch)
}

func resequencePublicFeedRecords(records []models.PublicFeedRecord, startSequence int64) {
	for index := range records {
		records[index].Sequence = startSequence + int64(index)
	}
}

// PublishLiveEvent appends one validated live event at the next feed sequence
// and hands it to ExportBatch, which is what puts it on the mirror SSE stream.
// A sequence race with another publisher is retried once.
func (s *PublicPublisherService) PublishLiveEvent(ctx context.Context, body []byte) error {
	if !s.cfg.Enabled {
		return constants.ErrPublicFeedDisabled
	}
	if err := publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, body); err != nil {
		return fmt.Errorf("public-feed: publish live event: %w", err)
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if err := s.loadSnapshotFromOutbox(ctx); err != nil {
			return fmt.Errorf("public-feed: publish live event: %w", err)
		}
		s.mu.Lock()
		next := s.highWaterSeq + 1
		s.mu.Unlock()
		sum := sha256.Sum256(body)
		err := s.ExportBatch(ctx, []models.PublicFeedRecord{{
			Sequence:    next,
			RecordType:  models.PublicFeedRecordTypeEvent,
			RecordHash:  hex.EncodeToString(sum[:]),
			RecordBytes: string(body),
		}})
		if err == nil || !errors.Is(err, constants.ErrPublicFeedSequenceOutOfOrder) {
			return err
		}
		last = err
	}
	return last
}

// ExportBatch builds a signed batch from records, durably writes it to the
// outbox, transmits it to the mirror with idempotent retry, and advances
// acknowledgment only after signed mirror acceptance.
func (s *PublicPublisherService) ExportBatch(ctx context.Context, records []models.PublicFeedRecord) error {
	if !s.cfg.Enabled {
		return constants.ErrPublicFeedDisabled
	}
	if len(records) == 0 {
		return constants.ErrPublicFeedBatchEmpty
	}
	if len(records) > s.batchMaxRecords {
		return constants.ErrPublicFeedBatchOversized
	}

	err := s.exportBatchOnce(ctx, records)
	if err == nil {
		return nil
	}
	if !isRepairableOutboxError(err) {
		return err
	}
	if repairErr := s.RepairOutboxFromSnapshot(ctx); repairErr != nil {
		return err
	}
	if reloadErr := s.loadSnapshotFromOutbox(ctx); reloadErr != nil {
		return fmt.Errorf("public-feed: export batch: reload after repair: %w", reloadErr)
	}
	s.mu.Lock()
	nextSequence := s.highWaterSeq + 1
	s.mu.Unlock()
	resequencePublicFeedRecords(records, nextSequence)
	return s.exportBatchOnce(ctx, records)
}

func (s *PublicPublisherService) exportBatchOnce(ctx context.Context, records []models.PublicFeedRecord) error {
	if err := s.loadSnapshotFromOutbox(ctx); err != nil {
		return fmt.Errorf("public-feed: export batch: recover outbox: %w", err)
	}
	if err := s.retransmitUnacknowledgedOutbox(ctx); err != nil {
		return fmt.Errorf("public-feed: export batch: retransmit unacknowledged: %w", err)
	}
	s.mu.Lock()
	nextSequence := s.highWaterSeq + 1
	s.mu.Unlock()
	for i, record := range records {
		if record.Sequence != nextSequence+int64(i) {
			return constants.ErrPublicFeedSequenceOutOfOrder
		}
	}

	batch, err := s.BuildBatch(records)
	if err != nil {
		return fmt.Errorf("public-feed: export batch: build: %w", err)
	}

	// Persist to outbox before network transmission.
	if err := s.writeOutboxEntry(ctx, batch); err != nil {
		return fmt.Errorf("public-feed: export batch: write outbox: %w", err)
	}

	// Transmit to mirror with retry.
	if err := s.transmitBatch(ctx, batch); err != nil {
		return fmt.Errorf("public-feed: export batch: transmit: %w", err)
	}

	// Update snapshot state.
	s.mu.Lock()
	s.highWaterSeq = batch.LastSequence
	s.feedChainHash = batch.ContentHash
	s.batchCount++
	s.mu.Unlock()

	if err := s.persistSnapshot(ctx); err != nil {
		return fmt.Errorf("public-feed: export batch: persist snapshot: %w", err)
	}
	ackWindow := time.Duration(s.cfg.AckWindowSecs) * time.Second
	if err := s.outbox.PruneAcknowledged(ctx, time.Now().UTC().Add(-ackWindow)); err != nil {
		return fmt.Errorf("public-feed: export batch: prune outbox: %w", err)
	}

	return nil
}

// writeOutboxEntry durably writes a batch to the outbox collection.
func (s *PublicPublisherService) writeOutboxEntry(ctx context.Context, batch models.PublicFeedBatch) error {
	batchBytes, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("marshal batch: %w", err)
	}
	entry := models.PublicOutboxEntry{
		Sequence:   batch.LastSequence,
		BatchHash:  batch.ContentHash,
		BatchBytes: string(batchBytes),
		Status:     models.PublicFeedOutboxStatusPending,
		CreatedAt:  time.Now().UTC(),
	}
	return s.outbox.Append(ctx, entry)
}

// transmitBatch sends a batch to the mirror with bounded retry. Returns nil
// only after the mirror accepts the batch.
func (s *PublicPublisherService) transmitBatch(ctx context.Context, batch models.PublicFeedBatch) error {
	s.mu.Lock()
	origin := s.mirrorOrigin
	ingestAuthToken := s.ingestAuthToken
	s.mu.Unlock()

	if origin == "" {
		return constants.ErrPublicFeedMirrorOriginRequired
	}

	maxAttempts := s.cfg.RetryMaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	initialBackoff := time.Duration(s.cfg.RetryInitialBackoffSecs) * time.Second
	maxBackoff := time.Duration(s.cfg.RetryMaxBackoffSecs) * time.Second

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.updateOutboxStatus(ctx, batch.LastSequence, models.PublicFeedOutboxStatusSent, true); err != nil {
			return fmt.Errorf("public-feed: update outbox attempt: %w", err)
		}
		err := s.sendToMirror(ctx, origin, ingestAuthToken, batch)
		if err == nil {
			if err := s.updateOutboxStatus(ctx, batch.LastSequence, models.PublicFeedOutboxStatusAcknowledged, false); err != nil {
				return fmt.Errorf("public-feed: acknowledge outbox: %w", err)
			}
			return nil
		}
		lastErr = err
		if isMirrorRejection(err) {
			if statusErr := s.updateOutboxStatus(ctx, batch.LastSequence, models.PublicFeedOutboxStatusFailed, false); statusErr != nil {
				return fmt.Errorf("public-feed: reject outbox: %w", statusErr)
			}
			return err
		}
		// Retryable error — compute backoff.
		if attempt < maxAttempts {
			backoff := initialBackoff * time.Duration(1<<(attempt-1))
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			if backoff > 0 {
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
	if err := s.updateOutboxStatus(ctx, batch.LastSequence, models.PublicFeedOutboxStatusFailed, false); err != nil {
		return fmt.Errorf("public-feed: exhaust outbox retries: %w", err)
	}
	return fmt.Errorf("%w: %v", constants.ErrPublicFeedMaxRetriesExceeded, lastErr)
}

// isMirrorRejection returns true if the error is a mirror rejection (not a
// transport error).
func isMirrorRejection(err error) bool {
	return errors.Is(err, constants.ErrPublicFeedMirrorRejected)
}

// sendToMirror sends a single ingest request to the mirror.
func publicKeyRegistrationSigningBytes(request models.PublicKeyRegistrationRequest) ([]byte, error) {
	request.Signature = ""
	return json.Marshal(request)
}

func publicKeyRevocationSigningBytes(record models.PublicKeyRevocationRecord) ([]byte, error) {
	record.RevocationSignature = ""
	return json.Marshal(record)
}

func (s *PublicPublisherService) sendToMirror(ctx context.Context, origin, ingestAuthToken string, batch models.PublicFeedBatch) error {
	reqBody := models.PublicIngestRequest{Batch: batch}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal ingest request: %w", err)
	}

	url := strings.TrimRight(origin, "/") + "/ingest"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("%w: %v", constants.ErrPublicFeedMirrorUnreachable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if ingestAuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+ingestAuthToken)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", constants.ErrPublicFeedMirrorUnreachable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w: status %d", constants.ErrPublicFeedMirrorUnreachable, resp.StatusCode)
	}

	var ingestResp models.PublicIngestResponse
	if err := json.NewDecoder(resp.Body).Decode(&ingestResp); err != nil {
		return fmt.Errorf("%w: decode response: %v", constants.ErrPublicFeedMirrorUnreachable, err)
	}

	if !ingestResp.Accepted {
		if ingestResp.RejectionReason == models.PublicFeedIngestRejectionDuplicateSequence &&
			ingestResp.SourceID == batch.SourceID &&
			ingestResp.HighWaterSequence == batch.LastSequence &&
			ingestResp.FeedChainHash == batch.ContentHash {
			return nil
		}
		return fmt.Errorf("%w: %s", constants.ErrPublicFeedMirrorRejected, ingestResp.RejectionReason)
	}

	return nil
}

// updateOutboxStatus updates the status of an outbox entry.
func (s *PublicPublisherService) updateOutboxStatus(ctx context.Context, seq int64, status models.PublicFeedOutboxStatus, incrementAttempt bool) error {
	entries, err := s.outbox.List(ctx)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Sequence != seq {
			continue
		}
		entry.Status = status
		now := time.Now().UTC()
		if incrementAttempt {
			entry.Attempts++
			entry.LastAttemptAt = &now
		}
		if status == models.PublicFeedOutboxStatusAcknowledged {
			entry.AcknowledgedAt = &now
		}
		return s.outbox.Update(ctx, entry)
	}
	return constants.ErrPublicFeedOutboxCorrupt
}

// retransmitUnacknowledgedOutbox transmits any outbox tail entries that have
// not yet been mirror-acknowledged. The publisher high-water sequence must
// remain aligned with mirror acceptance, so unacknowledged entries are
// retried before new batches are appended.
func (s *PublicPublisherService) retransmitUnacknowledgedOutbox(ctx context.Context) error {
	entries, err := s.outbox.List(ctx)
	if err != nil {
		return fmt.Errorf("public-feed: retransmit unacknowledged: list outbox: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Sequence < entries[j].Sequence })
	s.mu.Lock()
	tipSequence := s.highWaterSeq
	tipHash := s.feedChainHash
	if tipSequence == 0 && tipHash == "" {
		tipHash = constants.PublicFeedZeroHashHex
	}
	batchCount := s.batchCount
	s.mu.Unlock()
	for _, entry := range entries {
		if entry.Sequence <= tipSequence {
			continue
		}
		if entry.Status == models.PublicFeedOutboxStatusAcknowledged {
			tipSequence = entry.Sequence
			tipHash = entry.BatchHash
			batchCount++
			continue
		}
		expectedSequence := tipSequence + 1
		// Outbox entries are indexed by the batch's last sequence; continuity
		// is determined by its first sequence, including multi-record batches.
		var batch models.PublicFeedBatch
		if err := json.Unmarshal([]byte(entry.BatchBytes), &batch); err != nil {
			return fmt.Errorf("%w: decode unacknowledged batch: %v", constants.ErrPublicFeedOutboxCorrupt, err)
		}
		if batch.FirstSequence != expectedSequence || batch.LastSequence != entry.Sequence || batch.PreviousBatchHash != tipHash {
			return fmt.Errorf("%w: sequence %d hash chain does not match mirror tip", constants.ErrPublicFeedOutboxCorrupt, entry.Sequence)
		}
		if err := s.transmitBatch(ctx, batch); err != nil {
			return err
		}
		tipSequence = batch.LastSequence
		tipHash = batch.ContentHash
		batchCount++
		s.mu.Lock()
		s.highWaterSeq = tipSequence
		s.feedChainHash = tipHash
		s.batchCount = batchCount
		s.mu.Unlock()
		if err := s.persistSnapshot(ctx); err != nil {
			return fmt.Errorf("public-feed: retransmit unacknowledged: persist snapshot: %w", err)
		}
	}
	return nil
}

// RetransmitOutbox retransmits mirror-unacknowledged outbox tail entries.
// Historical batches are not resent because the mirror retains only a bounded
// batch window and rejects duplicate_sequence for pruned prefixes.
func (s *PublicPublisherService) RetransmitOutbox(ctx context.Context) error {
	if err := s.loadSnapshotFromOutbox(ctx); err != nil {
		return fmt.Errorf("public-feed: retransmit: recover: %w", err)
	}
	if err := s.retransmitUnacknowledgedOutbox(ctx); err != nil {
		return fmt.Errorf("public-feed: retransmit: %w", err)
	}
	return nil
}

// GetSnapshot returns the current high-water sequence and feed-chain hash.
func (s *PublicPublisherService) GetSnapshot(ctx context.Context) (models.PublicFeedSnapshot, error) {
	s.mu.Lock()
	hw := s.highWaterSeq
	fch := s.feedChainHash
	bc := s.batchCount
	s.mu.Unlock()

	if hw == 0 {
		// Try recovering from the outbox entries (e.g. after publisher restart).
		if err := s.loadSnapshotFromOutbox(ctx); err != nil {
			return models.PublicFeedSnapshot{}, fmt.Errorf("public-feed: get snapshot: recover: %w", err)
		}
		s.mu.Lock()
		hw = s.highWaterSeq
		fch = s.feedChainHash
		bc = s.batchCount
		s.mu.Unlock()
	}

	if hw == 0 {
		return models.PublicFeedSnapshot{}, constants.ErrPublicFeedSnapshotNotFound
	}

	return models.PublicFeedSnapshot{
		ProtocolVersion:   constants.PublicFeedProtocolVersion,
		SourceID:          s.cfg.SourceID,
		HighWaterSequence: hw,
		FeedChainHash:     fch,
		BatchCount:        bc,
		GeneratedAt:       time.Now().UTC(),
		Freshness:         models.CampaignFreshnessActive,
	}, nil
}

// persistSnapshot writes the current snapshot to the runtime tree.
func (s *PublicPublisherService) persistSnapshot(ctx context.Context) error {
	s.mu.Lock()
	hw := s.highWaterSeq
	fch := s.feedChainHash
	bc := s.batchCount
	s.mu.Unlock()

	if hw == 0 {
		return nil
	}

	snap := models.PublicFeedSnapshot{
		ProtocolVersion:   constants.PublicFeedProtocolVersion,
		SourceID:          s.cfg.SourceID,
		HighWaterSequence: hw,
		FeedChainHash:     fch,
		BatchCount:        bc,
		GeneratedAt:       time.Now().UTC(),
		Freshness:         models.CampaignFreshnessActive,
	}
	snapBytes, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	return s.fileSvc.WriteFile(ctx, constants.PublicFeedSnapshotPath, snapBytes, constants.PermFilePrivate)
}

// RepairOutboxFromSnapshot compacts a prefix-pruned outbox back to the
// mirror-acknowledged snapshot tip so chain validation can resume.
func (s *PublicPublisherService) RepairOutboxFromSnapshot(ctx context.Context) error {
	var snapshot models.PublicFeedSnapshot
	exists, err := s.fileSvc.FileExists(ctx, constants.PublicFeedSnapshotPath)
	if err != nil {
		return fmt.Errorf("public-feed: repair outbox: stat snapshot: %w", err)
	}
	if !exists {
		return constants.ErrPublicFeedSnapshotNotFound
	}
	data, err := s.fileSvc.ReadFile(ctx, constants.PublicFeedSnapshotPath)
	if err != nil {
		return fmt.Errorf("public-feed: repair outbox: read snapshot: %w", err)
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("%w: decode snapshot: %v", constants.ErrPublicFeedOutboxCorrupt, err)
	}
	if snapshot.SourceID != s.cfg.SourceID || snapshot.HighWaterSequence <= 0 || snapshot.FeedChainHash == "" {
		return fmt.Errorf("%w: invalid snapshot", constants.ErrPublicFeedOutboxCorrupt)
	}

	rawStore, ok := s.outbox.(*runtimePublicOutboxStore)
	if !ok {
		return constants.ErrPublicFeedOutboxCorrupt
	}
	entries, err := rawStore.readRaw(ctx)
	if err != nil {
		return fmt.Errorf("public-feed: repair outbox: %w", err)
	}
	retained := make([]models.PublicOutboxEntry, 0, len(entries))
	tipSequence := snapshot.HighWaterSequence
	tipHash := snapshot.FeedChainHash
	for _, entry := range entries {
		if entry.Sequence <= tipSequence {
			continue
		}
		var batch models.PublicFeedBatch
		if err := json.Unmarshal([]byte(entry.BatchBytes), &batch); err != nil {
			break
		}
		if batch.FirstSequence != tipSequence+1 || batch.LastSequence != entry.Sequence || batch.PreviousBatchHash != tipHash {
			break
		}
		retained = append(retained, entry)
		tipSequence = entry.Sequence
		tipHash = entry.BatchHash
	}
	if len(retained) == len(entries) {
		return nil
	}
	if err := s.outbox.Replace(ctx, retained); err != nil {
		return fmt.Errorf("public-feed: repair outbox: %w", err)
	}
	return nil
}

// loadSnapshotFromOutbox reconstructs the high-water sequence and feed-chain
// hash from the runtime snapshot and durable file outbox after process restart.
func (s *PublicPublisherService) loadSnapshotFromOutbox(ctx context.Context) error {
	var snapshot models.PublicFeedSnapshot
	exists, err := s.fileSvc.FileExists(ctx, constants.PublicFeedSnapshotPath)
	if err != nil {
		return fmt.Errorf("load snapshot from outbox: stat snapshot: %w", err)
	}
	if exists {
		data, err := s.fileSvc.ReadFile(ctx, constants.PublicFeedSnapshotPath)
		if err != nil {
			return fmt.Errorf("load snapshot from outbox: read snapshot: %w", err)
		}
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return fmt.Errorf("%w: decode snapshot: %v", constants.ErrPublicFeedOutboxCorrupt, err)
		}
		if snapshot.SourceID != s.cfg.SourceID || snapshot.HighWaterSequence <= 0 || snapshot.FeedChainHash == "" {
			return fmt.Errorf("%w: invalid snapshot", constants.ErrPublicFeedOutboxCorrupt)
		}
	}

	entries, err := s.outbox.List(ctx)
	if err != nil {
		if errors.Is(err, constants.ErrPublicFeedHashChainMismatch) {
			if repairErr := s.RepairOutboxFromSnapshot(ctx); repairErr != nil {
				return fmt.Errorf("load snapshot from outbox: list: %w", err)
			}
			entries, err = s.outbox.List(ctx)
		}
		if err != nil {
			return fmt.Errorf("load snapshot from outbox: list: %w", err)
		}
	}
	maxSeq := snapshot.HighWaterSequence
	lastHash := snapshot.FeedChainHash
	if maxSeq == 0 && lastHash == "" {
		lastHash = constants.PublicFeedZeroHashHex
	}
	count := snapshot.BatchCount
	if maxSeq == 0 && len(entries) > 0 {
		var firstBatch models.PublicFeedBatch
		if err := json.Unmarshal([]byte(entries[0].BatchBytes), &firstBatch); err != nil {
			return fmt.Errorf("%w: decode first batch: %v", constants.ErrPublicFeedOutboxCorrupt, err)
		}
		if firstBatch.FirstSequence != 1 || firstBatch.PreviousBatchHash != constants.PublicFeedZeroHashHex {
			return constants.ErrPublicFeedOutboxCorrupt
		}
	}
	for _, entry := range entries {
		if entry.Sequence < maxSeq {
			continue
		}
		if entry.Sequence == maxSeq {
			if entry.BatchHash != lastHash {
				return constants.ErrPublicFeedOutboxEquivocation
			}
			continue
		}
		if entry.Status != models.PublicFeedOutboxStatusAcknowledged {
			continue
		}
		var batch models.PublicFeedBatch
		if err := json.Unmarshal([]byte(entry.BatchBytes), &batch); err != nil {
			return fmt.Errorf("%w: decode recovery batch: %v", constants.ErrPublicFeedOutboxCorrupt, err)
		}
		if batch.FirstSequence != maxSeq+1 || batch.PreviousBatchHash != lastHash {
			return constants.ErrPublicFeedOutboxCorrupt
		}
		maxSeq = entry.Sequence
		lastHash = entry.BatchHash
		count++
	}

	s.mu.Lock()
	s.highWaterSeq = maxSeq
	s.feedChainHash = lastHash
	s.batchCount = count
	s.mu.Unlock()
	return nil
}

func (s *PublicPublisherService) registerRotatedKey(ctx context.Context, newKeyID string, newPublicKey ed25519.PublicKey) error {
	s.mu.Lock()
	origin := s.mirrorOrigin
	token := s.ingestAuthToken
	currentKeyID := s.signingKeyID
	currentPrivateKey := append(ed25519.PrivateKey(nil), s.signingPrivKey...)
	s.mu.Unlock()
	if origin == "" {
		return constants.ErrPublicFeedMirrorOriginRequired
	}
	request := models.PublicKeyRegistrationRequest{
		SourceID:     s.cfg.SourceID,
		CurrentKeyID: currentKeyID,
		NewKeyID:     newKeyID,
		PublicKey:    hex.EncodeToString(newPublicKey),
	}
	signingBytes, err := publicKeyRegistrationSigningBytes(request)
	if err != nil {
		return fmt.Errorf("%w: encode request: %v", constants.ErrPublicFeedKeyRegistration, err)
	}
	request.Signature = hex.EncodeToString(ed25519.Sign(currentPrivateKey, signingBytes))
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("%w: encode request: %v", constants.ErrPublicFeedKeyRegistration, err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(origin, "/")+"/keys/register", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: %v", constants.ErrPublicFeedMirrorUnreachable, err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpRequest)
	if err != nil {
		return fmt.Errorf("%w: %v", constants.ErrPublicFeedMirrorUnreachable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 500 {
		return fmt.Errorf("%w: status %d", constants.ErrPublicFeedMirrorUnreachable, response.StatusCode)
	}
	var registrationResponse models.PublicKeyRegistrationResponse
	if err := json.NewDecoder(response.Body).Decode(&registrationResponse); err != nil {
		return fmt.Errorf("%w: decode response: %v", constants.ErrPublicFeedKeyRegistration, err)
	}
	if !registrationResponse.Accepted {
		return constants.ErrPublicFeedKeyRegistration
	}
	return nil
}

// RotateKey generates a new Ed25519 key pair, emits a key revocation record
// as a signed batch, and switches the signing key. Returns the new key ID
// and new public key hex.
func (s *PublicPublisherService) RotateKey(ctx context.Context) (string, string, error) {
	newPub, newPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", constants.ErrPublicFeedKeyGenFailed, err)
	}
	keyDigest := sha256.Sum256(newPub)
	newKeyID := hex.EncodeToString(keyDigest[:])
	if err := s.RotateKeyTo(ctx, newPriv, newKeyID); err != nil {
		return "", "", err
	}
	return newKeyID, hex.EncodeToString(newPub), nil
}

func (s *PublicPublisherService) RotateKeyTo(ctx context.Context, newPriv ed25519.PrivateKey, newKeyID string) error {
	if len(newPriv) != ed25519.PrivateKeySize {
		return constants.ErrPublicFeedSigningKeyRequired
	}
	if newKeyID == "" {
		return constants.ErrPublicFeedSigningKeyIDRequired
	}
	if err := s.loadSnapshotFromOutbox(ctx); err != nil {
		return fmt.Errorf("public-feed: rotate key: recover outbox: %w", err)
	}
	newPub := newPriv.Public().(ed25519.PublicKey)
	if err := s.registerRotatedKey(ctx, newKeyID, newPub); err != nil {
		return fmt.Errorf("public-feed: rotate key: register new key: %w", err)
	}
	revRecord := models.PublicKeyRevocationRecord{
		SourceID:     s.cfg.SourceID,
		RevokedKeyID: s.signingKeyID,
		RevokedAt:    time.Now().UTC(),
		NewKeyID:     newKeyID,
	}
	revocationBytes, err := publicKeyRevocationSigningBytes(revRecord)
	if err != nil {
		return fmt.Errorf("public-feed: rotate key: encode revocation signature payload: %w", err)
	}
	revRecord.RevocationSignature = hex.EncodeToString(ed25519.Sign(s.signingPrivKey, revocationBytes))
	revBytes, err := json.Marshal(revRecord)
	if err != nil {
		return fmt.Errorf("marshal revocation record: %w", err)
	}
	revHash := sha256.Sum256(revBytes)

	s.mu.Lock()
	nextSeq := s.highWaterSeq + 1
	s.mu.Unlock()
	record := models.PublicFeedRecord{
		Sequence:    nextSeq,
		RecordType:  models.PublicFeedRecordTypeKeyRevocation,
		RecordHash:  hex.EncodeToString(revHash[:]),
		RecordBytes: string(revBytes),
	}
	if err := s.ExportBatch(ctx, []models.PublicFeedRecord{record}); err != nil {
		return fmt.Errorf("public-feed: rotate key: export revocation: %w", err)
	}

	s.mu.Lock()
	s.signingPrivKey = newPriv
	s.signingPubKey = newPub
	s.signingKeyID = newKeyID
	s.mu.Unlock()
	return nil
}

// PushProofPackage sends the signed segment suffix after the mirror's current
// chain position. The mirror is authoritative for delivery progress, including
// after an outage or replacement; local sync state is only a checkpoint.
func (s *PublicPublisherService) PushProofPackage(ctx context.Context) error {
	s.proofPushMu.Lock()
	defer s.proofPushMu.Unlock()

	logExists, err := s.cutOverLegacyProofPackage(ctx)
	if err != nil || !logExists {
		return err
	}
	mirrorRoot, err := s.fetchMirrorProofRoot(ctx)
	if err != nil {
		return err
	}
	s.proofMu.Lock()
	err = s.syncProofIndexLocked(ctx)
	var pending publicProofLogRange
	if err == nil {
		pending, err = s.proofIndex.pendingAfter(mirrorRoot)
	}
	s.proofMu.Unlock()
	if err != nil {
		return err
	}
	activeCount := mirrorRoot.ArtifactCount
	if mirrorRoot.SegmentIndex == 0 {
		activeCount = 0 // Genesis replaces any legacy full-manifest projection.
	}
	return s.readProofLogRange(ctx, pending, func(segment models.PublicProofManifestSegment) error {
		artifactIDs := make([]string, len(segment.Entries))
		for i, entry := range segment.Entries {
			artifactIDs[i] = entry.ArtifactID
		}
		if err := s.postProofIngest(ctx, segment, artifactIDs); err != nil {
			return err
		}
		activeCount += len(segment.Entries) - len(segment.WithdrawnArtifactIDs)
		return s.saveProofMirrorSyncState(ctx, models.PublicProofMirrorSyncState{
			SchemaVersion:   constants.PublicProofMirrorSyncSchemaVersion,
			SourceID:        s.cfg.SourceID,
			ProofRootSHA256: segment.ProofRootSHA256,
			SegmentIndex:    segment.SegmentIndex,
			ArtifactCount:   activeCount,
			SyncedAt:        time.Now().UTC(),
		})
	})
}

// cutOverLegacyProofPackage reports whether the proof log exists. A legacy
// package with no log is first cut over to a source-signed genesis segment;
// the mirror never synthesizes a signature.
func (s *PublicPublisherService) cutOverLegacyProofPackage(ctx context.Context) (bool, error) {
	s.proofMu.Lock()
	defer s.proofMu.Unlock()
	if err := s.syncProofIndexLocked(ctx); err != nil {
		return false, err
	}
	if len(s.proofIndex.segments) > 0 {
		return true, nil
	}
	manifest, err := s.loadLegacyProofManifest(ctx)
	if err != nil || manifest == nil {
		return false, err
	}
	if _, err := s.publishLegacyGenesisLocked(ctx, *manifest); err != nil {
		return false, err
	}
	return true, nil
}

// publishLegacyGenesisLocked takes the proof log lock and, when the log is
// still empty, appends the genesis segment for a legacy package: the legacy
// catalog under the legacy manifest's metadata. Callers hold proofMu.
func (s *PublicPublisherService) publishLegacyGenesisLocked(ctx context.Context, manifest models.PublicProofManifest) (models.PublicProofManifest, error) {
	if !manifest.VerificationOK {
		return models.PublicProofManifest{}, constants.ErrPublicFeedProofNotVerified
	}
	held, err := s.lockProofLog(ctx)
	if err != nil {
		return models.PublicProofManifest{}, err
	}
	defer func() { _ = held.Close() }()
	if err := s.syncProofIndexLocked(ctx); err != nil {
		return models.PublicProofManifest{}, err
	}
	if len(s.proofIndex.segments) > 0 {
		return s.proofIndex.headProjection(), nil
	}
	return s.appendPublicationLocked(ctx, manifest.CampaignID, manifest.CampaignRevision, manifest.VerifiedIndexGenerationHash, manifest.Artifacts)
}

// fetchMirrorProofRoot reads the mirror's stored proof root for this source.
// A mirror holding no manifest for the source returns an empty root.
func (s *PublicPublisherService) fetchMirrorProofRoot(ctx context.Context) (models.PublicProofRootResponse, error) {
	s.mu.Lock()
	origin := s.mirrorOrigin
	token := s.ingestAuthToken
	s.mu.Unlock()
	if origin == "" {
		return models.PublicProofRootResponse{}, constants.ErrPublicFeedMirrorOriginRequired
	}
	requestURL := strings.TrimRight(origin, "/") + "/proof-root?source=" + url.QueryEscape(s.cfg.SourceID)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return models.PublicProofRootResponse{}, fmt.Errorf("%w: %v", constants.ErrPublicFeedMirrorUnreachable, err)
	}
	if token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(httpRequest)
	if err != nil {
		return models.PublicProofRootResponse{}, fmt.Errorf("%w: %v", constants.ErrPublicFeedMirrorUnreachable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return models.PublicProofRootResponse{SourceID: s.cfg.SourceID}, nil
	}
	if response.StatusCode != http.StatusOK {
		return models.PublicProofRootResponse{}, fmt.Errorf("%w: proof root status %d", constants.ErrPublicFeedMirrorUnreachable, response.StatusCode)
	}
	var root models.PublicProofRootResponse
	if err := json.NewDecoder(response.Body).Decode(&root); err != nil {
		return models.PublicProofRootResponse{}, fmt.Errorf("%w: decode proof root: %v", constants.ErrPublicFeedMirrorUnreachable, err)
	}
	if root.SourceID != s.cfg.SourceID {
		return models.PublicProofRootResponse{}, fmt.Errorf("%w: proof root for source %q", constants.ErrPublicFeedMirrorUnreachable, root.SourceID)
	}
	return root, nil
}

func proofCatalogFromManifest(manifest models.PublicProofManifest) models.PublicProofCatalog {
	return models.PublicProofCatalog{
		SchemaVersion: constants.PublicProofCatalogSchemaVersion,
		Entries:       manifest.Artifacts,
		GeneratedAt:   manifest.GeneratedAt,
	}
}

// loadProofManifest returns the head projection of the proof log, or the
// legacy manifest when there is no log. A head projection carries the active
// artifact count without listing the artifacts. It returns nil when there is
// neither.
func (s *PublicPublisherService) loadProofManifest(ctx context.Context) (*models.PublicProofManifest, error) {
	s.proofMu.Lock()
	err := s.syncProofIndexLocked(ctx)
	if err == nil && len(s.proofIndex.segments) > 0 {
		manifest := s.proofIndex.headProjection()
		s.proofMu.Unlock()
		return &manifest, nil
	}
	s.proofMu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.loadLegacyProofManifest(ctx)
}

// loadLegacyProofManifest reads the full manifest a pre-segment package
// wrote. It returns nil when there is none.
func (s *PublicPublisherService) loadLegacyProofManifest(ctx context.Context) (*models.PublicProofManifest, error) {
	manifestExists, err := s.fileSvc.FileExists(ctx, constants.PublicProofManifestFilename)
	if err != nil {
		return nil, fmt.Errorf("public-feed: inspect proof manifest: %w", err)
	}
	if !manifestExists {
		return nil, nil
	}
	manifestBytes, err := s.fileSvc.ReadFile(ctx, constants.PublicProofManifestFilename)
	if err != nil {
		return nil, fmt.Errorf("public-feed: read proof manifest: %w", err)
	}
	manifestDecoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	manifestDecoder.DisallowUnknownFields()
	var manifest models.PublicProofManifest
	if err := manifestDecoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("%w: decode manifest: %w", constants.ErrPublicFeedProofManifestInvalid, err)
	}
	var trailing json.RawMessage
	if err := manifestDecoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing manifest JSON", constants.ErrPublicFeedProofManifestInvalid)
	}
	return &manifest, nil
}

// postProofIngest sends one signed segment and verifies its acknowledgement.
func (s *PublicPublisherService) postProofIngest(ctx context.Context, segment models.PublicProofManifestSegment, artifactIDs []string) error {
	artifacts, err := s.readProofArtifacts(ctx, artifactIDs)
	if err != nil {
		return err
	}
	request := models.PublicProofIngestRequest{SourceID: s.cfg.SourceID, Segment: &segment, Artifacts: artifacts}
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("public-feed: encode proof ingest: %w", err)
	}
	s.mu.Lock()
	origin := s.mirrorOrigin
	token := s.ingestAuthToken
	s.mu.Unlock()
	if origin == "" {
		return constants.ErrPublicFeedMirrorOriginRequired
	}
	s.logger.Info("public-feed: pushing proof package to mirror",
		"artifact_count", len(artifacts),
		"segment_index", segment.SegmentIndex,
		"entry_count", len(segment.Entries),
		"request_bytes", len(body),
	)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(origin, "/")+"/proof-ingest", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: %v", constants.ErrPublicFeedMirrorUnreachable, err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 10 * time.Minute}).Do(httpRequest)
	if err != nil {
		return fmt.Errorf("%w: %v", constants.ErrPublicFeedMirrorUnreachable, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 500 {
		return fmt.Errorf("%w: status %d", constants.ErrPublicFeedMirrorUnreachable, response.StatusCode)
	}
	var ingestResponse models.PublicProofIngestResponse
	if err := json.NewDecoder(response.Body).Decode(&ingestResponse); err != nil {
		return fmt.Errorf("%w: decode response: %v", constants.ErrPublicFeedProofIngestRejected, err)
	}
	if response.StatusCode == http.StatusOK && ingestResponse.Accepted && ingestResponse.ProofRoot == segment.ProofRootSHA256 {
		return nil
	}
	if len(ingestResponse.MissingArtifactIDs) > 0 {
		return fmt.Errorf("%w: %w", constants.ErrPublicFeedProofIngestRejected, constants.ErrPublicFeedProofArtifactsMissing)
	}
	return constants.ErrPublicFeedProofIngestRejected
}

func (s *PublicPublisherService) readProofArtifacts(ctx context.Context, artifactIDs []string) ([]models.PublicProofIngestArtifact, error) {
	artifacts := make([]models.PublicProofIngestArtifact, 0, len(artifactIDs))
	for _, artifactID := range artifactIDs {
		if artifactID == "" {
			return nil, constants.ErrPublicFeedProofPathTraversal
		}
		relPath := filepath.Join(constants.PublicProofsDirname, artifactID)
		info, err := s.fileSvc.Lstat(ctx, relPath)
		if err != nil {
			return nil, fmt.Errorf("public-feed: inspect proof artifact: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, constants.ErrPublicFeedProofSymlinkRejected
		}
		content, err := s.fileSvc.ReadFile(ctx, relPath)
		if err != nil {
			return nil, fmt.Errorf("public-feed: read proof artifact: %w", err)
		}
		artifacts = append(artifacts, models.PublicProofIngestArtifact{ArtifactID: artifactID, Content: content})
	}
	return artifacts, nil
}

func (s *PublicPublisherService) loadProofMirrorSyncState(ctx context.Context) (*models.PublicProofMirrorSyncState, error) {
	exists, err := s.fileSvc.FileExists(ctx, constants.PublicProofMirrorSyncFilename)
	if err != nil {
		return nil, fmt.Errorf("public-feed: inspect proof mirror sync state: %w", err)
	}
	if !exists {
		return nil, nil
	}
	data, err := s.fileSvc.ReadFile(ctx, constants.PublicProofMirrorSyncFilename)
	if err != nil {
		return nil, fmt.Errorf("public-feed: read proof mirror sync state: %w", err)
	}
	var state models.PublicProofMirrorSyncState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("public-feed: decode proof mirror sync state: %w", err)
	}
	if state.SchemaVersion != constants.PublicProofMirrorSyncSchemaVersion || state.SourceID != s.cfg.SourceID {
		return nil, nil
	}
	return &state, nil
}

func (s *PublicPublisherService) saveProofMirrorSyncState(ctx context.Context, state models.PublicProofMirrorSyncState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("public-feed: marshal proof mirror sync state: %w", err)
	}
	return s.fileSvc.WriteFile(ctx, constants.PublicProofMirrorSyncFilename, data, constants.PermFilePrivate)
}

func (s *PublicPublisherService) writeProofArtifacts(ctx context.Context, campaignID string, artifacts []ProofArtifactInput) ([]models.PublicProofCatalogEntry, error) {
	if len(artifacts) == 0 {
		return nil, constants.ErrPublicFeedBatchEmpty
	}
	dirInfo, err := s.fileSvc.Lstat(ctx, constants.PublicProofsDirname)
	if err == nil && (dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir()) {
		return nil, constants.ErrPublicFeedProofSymlinkRejected
	}
	if err != nil && !errors.Is(err, constants.ErrNotFound) {
		return nil, fmt.Errorf("public-feed: inspect proof directory: %w", err)
	}
	if err := s.fileSvc.MkdirAll(ctx, constants.PublicProofsDirname, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("public-feed: build proof: mkdir: %w", err)
	}
	entries := make([]models.PublicProofCatalogEntry, 0, len(artifacts))
	for _, art := range artifacts {
		if !safePublicProofFilename(art.Filename) {
			return nil, constants.ErrPublicFeedProofPathTraversal
		}
		if int64(len(art.Content)) > int64(constants.PublicFeedMaxArtifactBytes) {
			return nil, constants.ErrPublicFeedProofOversized
		}
		h := sha256.Sum256(art.Content)
		artifactID := hex.EncodeToString(h[:])
		entry := models.PublicProofCatalogEntry{
			ArtifactID:          artifactID,
			Filename:            art.Filename,
			MediaType:           art.MediaType,
			ByteSize:            int64(len(art.Content)),
			SHA256:              artifactID,
			Classification:      models.PublicFeedProofClassificationPublicSafe,
			CampaignID:          campaignID,
			SourceRunID:         art.SourceRunID,
			GeneratedAt:         time.Now().UTC(),
			VerificationCommand: art.VerificationCommand,
			ImmutableURL:        fmt.Sprintf("/proofs/%s", artifactID),
		}
		if entry.VerificationCommand == "" {
			entry.VerificationCommand = fmt.Sprintf("sha256sum %s", art.Filename)
		}
		relPath := filepath.Join(constants.PublicProofsDirname, artifactID)
		info, err := s.fileSvc.Lstat(ctx, relPath)
		if err != nil && !errors.Is(err, constants.ErrNotFound) {
			return nil, fmt.Errorf("public-feed: build proof: inspect artifact: %w", err)
		}
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil, constants.ErrPublicFeedProofSymlinkRejected
			}
			existing, readErr := s.fileSvc.ReadFile(ctx, relPath)
			if readErr == nil {
				existingHash := sha256.Sum256(existing)
				if hex.EncodeToString(existingHash[:]) == artifactID && len(existing) == len(art.Content) {
					entries = append(entries, entry)
					continue
				}
			}
		}
		if err := s.fileSvc.WriteFile(ctx, relPath, art.Content, constants.PermFilePrivate); err != nil {
			return nil, fmt.Errorf("public-feed: build proof: write artifact: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// finalizeProofManifest appends the signed segment for one publication. The
// returned projection lists this publication's artifacts and counts every
// active artifact.
func (s *PublicPublisherService) finalizeProofManifest(ctx context.Context, campaignID, campaignRevision, verifiedIndexGenHash string, verificationOK bool, newEntries []models.PublicProofCatalogEntry) (models.PublicProofManifest, error) {
	if !verificationOK {
		return models.PublicProofManifest{}, constants.ErrPublicFeedProofNotVerified
	}
	s.proofMu.Lock()
	defer s.proofMu.Unlock()
	held, err := s.lockProofLog(ctx)
	if err != nil {
		return models.PublicProofManifest{}, err
	}
	defer func() { _ = held.Close() }()
	if err := s.syncProofIndexLocked(ctx); err != nil {
		return models.PublicProofManifest{}, err
	}
	return s.appendPublicationLocked(ctx, campaignID, campaignRevision, verifiedIndexGenHash, newEntries)
}

// appendPublicationLocked appends a segment listing the publication's
// artifacts that are not yet active. The genesis segment of a legacy package
// also lists its legacy catalog. Re-publishing a withdrawn artifact is
// equivocation. Callers hold proofMu and the proof log lock, and have synced
// the index.
func (s *PublicPublisherService) appendPublicationLocked(ctx context.Context, campaignID, campaignRevision, verifiedIndexGenHash string, newEntries []models.PublicProofCatalogEntry) (models.PublicProofManifest, error) {
	index := s.proofIndex
	segmentEntries := make([]models.PublicProofCatalogEntry, 0, len(newEntries))
	pending := make(map[string]models.PublicProofCatalogEntry)
	if len(index.segments) == 0 {
		legacy, err := s.loadLegacyProofCatalog(ctx)
		if err != nil {
			return models.PublicProofManifest{}, err
		}
		for _, entry := range legacy.Entries {
			if _, dup := pending[entry.ArtifactID]; !dup {
				pending[entry.ArtifactID] = entry
				segmentEntries = append(segmentEntries, entry)
			}
		}
	}
	admitted := make([]models.PublicProofCatalogEntry, 0, len(newEntries))
	seen := make(map[string]struct{}, len(newEntries))
	for _, entry := range newEntries {
		if _, dup := seen[entry.ArtifactID]; dup {
			continue
		}
		seen[entry.ArtifactID] = struct{}{}
		if stored, active := index.active[entry.ArtifactID]; active {
			admitted = append(admitted, stored)
			continue
		}
		if _, listed := index.listed[entry.ArtifactID]; listed {
			return models.PublicProofManifest{}, constants.ErrPublicFeedProofSegmentEquivocation
		}
		if stored, legacy := pending[entry.ArtifactID]; legacy {
			admitted = append(admitted, stored)
			continue
		}
		segmentEntries = append(segmentEntries, entry)
		admitted = append(admitted, entry)
	}

	previousRoot := constants.PublicFeedZeroHashHex
	if len(index.segments) > 0 {
		previousRoot = index.head.ProofRootSHA256
	}
	segment, err := s.signProofSegment(models.PublicProofManifestSegment{
		SchemaVersion:               constants.PublicProofSegmentSchemaVersion,
		SourceID:                    s.cfg.SourceID,
		SegmentIndex:                int64(len(index.segments)) + 1,
		PreviousRoot:                previousRoot,
		CampaignID:                  campaignID,
		CampaignRevision:            campaignRevision,
		VerifiedIndexGenerationHash: verifiedIndexGenHash,
		VerificationOK:              true,
		Entries:                     segmentEntries,
		VerifierInstructions:        "Run ztd public verify-proof --segments proof-segments.jsonl --artifacts ./proofs --public-key <trusted-ed25519-key-hex>. Verify every signed link from the zero root, apply withdrawals, then hash active artifacts by their content-addressed IDs.",
		GeneratedAt:                 time.Now().UTC(),
	})
	if err != nil {
		return models.PublicProofManifest{}, err
	}
	if err := s.appendProofSegmentLocked(ctx, segment); err != nil {
		return models.PublicProofManifest{}, err
	}
	manifest := publicProofManifestProjection(segment, admitted)
	manifest.ArtifactCount = len(index.active)
	return manifest, nil
}

// publicProofManifestProjection carries the segment's signature and root. Verification
// requires the signed log; the active list alone is not a signed full manifest.
func publicProofManifestProjection(segment models.PublicProofManifestSegment, entries []models.PublicProofCatalogEntry) models.PublicProofManifest {
	return models.PublicProofManifest{
		SchemaVersion:               constants.PublicProofManifestSchemaVersion,
		SegmentIndex:                segment.SegmentIndex,
		ProofRootSHA256:             segment.ProofRootSHA256,
		CampaignID:                  segment.CampaignID,
		CampaignRevision:            segment.CampaignRevision,
		VerifiedIndexGenerationHash: segment.VerifiedIndexGenerationHash,
		VerificationOK:              segment.VerificationOK,
		ArtifactCount:               len(entries),
		Artifacts:                   entries,
		VerifierInstructions:        segment.VerifierInstructions,
		GeneratedAt:                 segment.GeneratedAt,
		SigningKeyID:                segment.SigningKeyID,
		Signature:                   segment.Signature,
	}
}

// BuildProofPackage creates a complete public proof package from a passing
// verification report. It writes artifacts to disk under the public-proofs
// directory, builds a signed root manifest, and updates the proof catalog.
func (s *PublicPublisherService) BuildProofPackage(ctx context.Context, campaignID, campaignRevision, verifiedIndexGenHash string, verificationOK bool, artifacts []ProofArtifactInput) (models.PublicProofManifest, error) {
	if !verificationOK {
		return models.PublicProofManifest{}, constants.ErrPublicFeedProofNotVerified
	}
	entries, err := s.writeProofArtifacts(ctx, campaignID, artifacts)
	if err != nil {
		return models.PublicProofManifest{}, err
	}
	return s.finalizeProofManifest(ctx, campaignID, campaignRevision, verifiedIndexGenHash, verificationOK, entries)
}

// assignmentAuditProofPlan is one request's publication: the artifacts that
// still have to be written, the already-active catalog entries it reuses, and
// the digests reported back to the caller.
type assignmentAuditProofPlan struct {
	artifacts []ProofArtifactInput
	reused    []models.PublicProofCatalogEntry
	response  models.PublicAssignmentAuditProofPublishResponse
}

// planAssignmentAuditProof validates one assignment audit request and plans its
// artifacts. An active assignment database already published for the same run
// is reused by digest and not rebuilt. Otherwise the database is built from the
// gateway's own signed event records. The trace and scenario fixtures are
// published as given.
func (s *PublicPublisherService) planAssignmentAuditProof(ctx context.Context, request models.PublicAssignmentAuditProofPublishRequest) (assignmentAuditProofPlan, error) {
	if request.CampaignID == "" || request.CampaignRevision == "" || request.RunID == "" || request.AssignmentID == "" || request.IndexDigest == "" {
		return assignmentAuditProofPlan{}, fmt.Errorf("public-feed: publish assignment audit proof: %w", constants.ErrMissingRequiredField)
	}
	if (len(request.ScenarioInput) > 0 || len(request.ScenarioGold) > 0) &&
		(request.ScenarioID == "" || len(request.ScenarioInput) == 0 || len(request.ScenarioGold) == 0) {
		return assignmentAuditProofPlan{}, fmt.Errorf("public-feed: publish scenario fixture: %w", constants.ErrMissingRequiredField)
	}

	plan := assignmentAuditProofPlan{response: models.PublicAssignmentAuditProofPublishResponse{Accepted: true, AssignmentID: request.AssignmentID}}
	dbFilename := fmt.Sprintf("assignment-%s.db", request.AssignmentID)
	active, found, err := s.activeRunProofEntry(ctx, request.RunID, dbFilename)
	if err != nil {
		return assignmentAuditProofPlan{}, err
	}
	if found {
		plan.reused = append(plan.reused, active)
		plan.response.DatabaseSHA256 = active.ArtifactID
	} else {
		database, err := s.buildAssignmentDatabase(ctx, request)
		if err != nil {
			return assignmentAuditProofPlan{}, err
		}
		plan.artifacts = append(plan.artifacts, ProofArtifactInput{
			Filename:            dbFilename,
			MediaType:           "application/vnd.sqlite3",
			Content:             database,
			CampaignID:          request.CampaignID,
			SourceRunID:         request.RunID,
			VerificationCommand: fmt.Sprintf("ztd public verify-assignment --db %s --segments proof-segments.jsonl --public-key <trusted-ed25519-key-hex>", dbFilename),
		})
		plan.response.DatabaseSHA256 = hashBytes(database)
	}

	if len(request.AgentTrace) > 0 {
		traceFilename := fmt.Sprintf("assignment-%s.agent-trace.json", request.AssignmentID)
		plan.artifacts = append(plan.artifacts, ProofArtifactInput{
			Filename:            traceFilename,
			MediaType:           constants.MediaTypeJSON,
			Content:             request.AgentTrace,
			CampaignID:          request.CampaignID,
			SourceRunID:         request.RunID,
			VerificationCommand: fmt.Sprintf("ztd public verify-agent-trace --trace %s", traceFilename),
		})
	}
	plan.response.AgentTraceSHA256 = optionalSHA256(request.AgentTrace)

	if len(request.ScenarioInput) > 0 {
		// Frozen fixtures are campaign-scoped: every run of the campaign shares
		// the same bytes, so no single run's prune may remove them.
		plan.artifacts = append(plan.artifacts,
			ProofArtifactInput{
				Filename:   fmt.Sprintf("scenario-%s.input.json", request.ScenarioID),
				MediaType:  constants.MediaTypeJSON,
				Content:    request.ScenarioInput,
				CampaignID: request.CampaignID,
			},
			ProofArtifactInput{
				Filename:   fmt.Sprintf("scenario-%s.gold.json", request.ScenarioID),
				MediaType:  constants.MediaTypeJSON,
				Content:    request.ScenarioGold,
				CampaignID: request.CampaignID,
			},
		)
	}
	plan.response.ScenarioInputSHA256 = optionalSHA256(request.ScenarioInput)
	plan.response.ScenarioGoldSHA256 = optionalSHA256(request.ScenarioGold)
	return plan, nil
}

// buildAssignmentDatabase builds one assignment's audit database from the
// signed event records the gateway stores for it. The caller supplies no rows.
func (s *PublicPublisherService) buildAssignmentDatabase(ctx context.Context, request models.PublicAssignmentAuditProofPublishRequest) ([]byte, error) {
	s.mu.Lock()
	source := s.assignmentEvents
	s.mu.Unlock()
	if source == nil {
		return nil, fmt.Errorf("public-feed: build assignment database: event source: %w", constants.ErrMissingRequiredField)
	}
	records, err := source.AssignmentEventRecords(ctx, s.cfg.SourceID, request.RunID, request.AssignmentID)
	if err != nil {
		return nil, fmt.Errorf("public-feed: build assignment database: read events: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("public-feed: build assignment database: run %s assignment %s: %w", request.RunID, request.AssignmentID, constants.ErrPublicAssignmentAuditEventsMissing)
	}
	database, err := assignmentaudit.BuildDatabase(ctx, s.fileSvc, assignmentaudit.Header{
		SourceID:         s.cfg.SourceID,
		CampaignID:       request.CampaignID,
		CampaignRevision: request.CampaignRevision,
		RunID:            request.RunID,
		AssignmentID:     request.AssignmentID,
	}, records)
	if err != nil {
		return nil, fmt.Errorf("public-feed: build assignment database: %w", err)
	}
	return database, nil
}

// activeRunProofEntry returns the active catalog entry of the artifact named
// filename that one source run published, if there is one. It reads the proof
// index's per-run map, so its cost does not grow with the catalog.
func (s *PublicPublisherService) activeRunProofEntry(ctx context.Context, runID, filename string) (models.PublicProofCatalogEntry, bool, error) {
	s.proofMu.Lock()
	err := s.syncProofIndexLocked(ctx)
	if err == nil && len(s.proofIndex.segments) > 0 {
		for _, id := range s.proofIndex.activeRunArtifactIDs(runID) {
			if entry := s.proofIndex.active[id]; entry.Filename == filename {
				s.proofMu.Unlock()
				return entry, true, nil
			}
		}
		s.proofMu.Unlock()
		return models.PublicProofCatalogEntry{}, false, nil
	}
	s.proofMu.Unlock()
	if err != nil {
		return models.PublicProofCatalogEntry{}, false, err
	}
	legacy, err := s.loadLegacyProofCatalog(ctx)
	if err != nil {
		return models.PublicProofCatalogEntry{}, false, err
	}
	for _, entry := range legacy.Entries {
		if entry.SourceRunID == runID && entry.Filename == filename {
			return entry, true, nil
		}
	}
	return models.PublicProofCatalogEntry{}, false, nil
}

// hashBytes returns the hex SHA-256 of value.
func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// optionalSHA256 returns the hex SHA-256 of body, or "" when body is empty.
func optionalSHA256(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return hashBytes(body)
}

// PublishAssignmentAuditProof publishes one assignment audit database and
// returns its digests.
func (s *PublicPublisherService) PublishAssignmentAuditProof(ctx context.Context, request models.PublicAssignmentAuditProofPublishRequest) (models.PublicAssignmentAuditProofPublishResponse, error) {
	batch, err := s.PublishAssignmentAuditProofBatch(ctx, models.PublicAssignmentAuditProofBatchPublishRequest{
		Proofs:          []models.PublicAssignmentAuditProofPublishRequest{request},
		DeferMirrorPush: request.DeferMirrorPush,
	})
	if err != nil {
		return models.PublicAssignmentAuditProofPublishResponse{}, err
	}
	return batch.Proofs[0], nil
}

// PublishAssignmentAuditProofBatch publishes many assignment audit databases in
// one catalog update and one signed segment. It returns each proof's digests in
// request order. An assignment whose database is already active for its run is
// reused by digest and not rebuilt.
func (s *PublicPublisherService) PublishAssignmentAuditProofBatch(ctx context.Context, request models.PublicAssignmentAuditProofBatchPublishRequest) (models.PublicAssignmentAuditProofBatchPublishResponse, error) {
	response := models.PublicAssignmentAuditProofBatchPublishResponse{Accepted: true, Proofs: []models.PublicAssignmentAuditProofPublishResponse{}}
	if len(request.Proofs) == 0 {
		return response, nil
	}
	var artifacts []ProofArtifactInput
	var reused []models.PublicProofCatalogEntry
	for _, proof := range request.Proofs {
		plan, err := s.planAssignmentAuditProof(ctx, proof)
		if err != nil {
			return models.PublicAssignmentAuditProofBatchPublishResponse{}, err
		}
		artifacts = append(artifacts, plan.artifacts...)
		reused = append(reused, plan.reused...)
		response.Proofs = append(response.Proofs, plan.response)
	}
	campaignID := request.Proofs[0].CampaignID
	campaignRevision := request.Proofs[0].CampaignRevision
	verifiedIndexGenHash := request.Proofs[len(request.Proofs)-1].IndexDigest
	entries := reused
	if len(artifacts) > 0 {
		written, err := s.writeProofArtifacts(ctx, campaignID, artifacts)
		if err != nil {
			return models.PublicAssignmentAuditProofBatchPublishResponse{}, err
		}
		entries = append(written, reused...)
	}
	manifest, err := s.finalizeProofManifest(ctx, campaignID, campaignRevision, verifiedIndexGenHash, true, entries)
	if err != nil {
		return models.PublicAssignmentAuditProofBatchPublishResponse{}, err
	}
	if !request.DeferMirrorPush {
		if err := s.PushProofPackage(ctx); err != nil {
			return models.PublicAssignmentAuditProofBatchPublishResponse{}, err
		}
	}
	response.IngestedProofs = len(request.Proofs)
	response.ProofRootSHA256 = manifest.ProofRootSHA256
	for index := range response.Proofs {
		response.Proofs[index].ProofRootSHA256 = manifest.ProofRootSHA256
	}
	return response, nil
}

// PruneProofCatalogForRun appends a signed withdrawal for one run. Immutable
// artifact bytes remain available for delivery recovery.
func (s *PublicPublisherService) PruneProofCatalogForRun(ctx context.Context, runID string) (int, error) {
	if runID == "" {
		return 0, constants.ErrMissingRequiredField
	}
	s.proofMu.Lock()
	defer s.proofMu.Unlock()
	held, err := s.lockProofLog(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = held.Close() }()
	if err := s.syncProofIndexLocked(ctx); err != nil {
		return 0, err
	}
	if len(s.proofIndex.segments) == 0 {
		// A legacy package is cut over to its genesis segment before the
		// withdrawal is appended.
		legacy, err := s.loadLegacyProofCatalog(ctx)
		if err != nil {
			return 0, err
		}
		if !slices.ContainsFunc(legacy.Entries, func(entry models.PublicProofCatalogEntry) bool { return entry.SourceRunID == runID }) {
			return 0, nil
		}
		manifest, err := s.loadLegacyProofManifest(ctx)
		if err != nil {
			return 0, err
		}
		if manifest == nil {
			return 0, constants.ErrPublicFeedProofManifestInvalid
		}
		if !manifest.VerificationOK {
			return 0, constants.ErrPublicFeedProofNotVerified
		}
		if _, err := s.appendPublicationLocked(ctx, manifest.CampaignID, manifest.CampaignRevision, manifest.VerifiedIndexGenerationHash, nil); err != nil {
			return 0, err
		}
	}
	index := s.proofIndex
	removedIDs := index.activeRunArtifactIDs(runID)
	if len(removedIDs) == 0 {
		return 0, nil
	}
	withdrawal, err := s.signProofSegment(models.PublicProofManifestSegment{
		SchemaVersion:               constants.PublicProofSegmentSchemaVersion,
		SourceID:                    s.cfg.SourceID,
		SegmentIndex:                index.head.SegmentIndex + 1,
		PreviousRoot:                index.head.ProofRootSHA256,
		CampaignID:                  index.head.CampaignID,
		CampaignRevision:            index.head.CampaignRevision,
		VerifiedIndexGenerationHash: index.head.VerifiedIndexGenerationHash,
		VerificationOK:              true,
		WithdrawnArtifactIDs:        removedIDs,
		VerifierInstructions:        index.head.VerifierInstructions,
		GeneratedAt:                 time.Now().UTC(),
	})
	if err != nil {
		return 0, err
	}
	if err := s.appendProofSegmentLocked(ctx, withdrawal); err != nil {
		return 0, err
	}
	// Keep immutable bytes so an unsent prefix can be replayed to a fresh
	// mirror. Signed withdrawals control visibility, not physical deletion.
	_ = s.fileSvc.Remove(ctx, constants.PublicProofCatalogFilename)
	_ = s.fileSvc.Remove(ctx, constants.PublicProofManifestFilename)
	if err := s.resetProofMirrorSyncState(ctx); err != nil {
		return 0, err
	}
	s.logger.Info("public-feed: pruned proof catalog for run", "run_id", runID, "removed_count", len(removedIDs), "remaining_count", len(index.active))
	return len(removedIDs), nil
}

// resetProofMirrorSyncState removes the delivery checkpoint after a withdrawal.
// Delivery always resumes from the mirror's actual segment head.
func (s *PublicPublisherService) resetProofMirrorSyncState(ctx context.Context) error {
	exists, err := s.fileSvc.FileExists(ctx, constants.PublicProofMirrorSyncFilename)
	if err != nil {
		return fmt.Errorf("public-feed: inspect proof mirror sync state: %w", err)
	}
	if !exists {
		return nil
	}
	if err := s.fileSvc.Remove(ctx, constants.PublicProofMirrorSyncFilename); err != nil {
		return fmt.Errorf("public-feed: reset proof mirror sync state: %w", err)
	}
	return nil
}

// FlushProofPackage pushes the proof log to the mirror and returns the head
// projection: its root and the active artifact count. It returns an empty
// manifest when nothing has been published.
func (s *PublicPublisherService) FlushProofPackage(ctx context.Context) (models.PublicProofManifest, error) {
	if err := s.PushProofPackage(ctx); err != nil {
		return models.PublicProofManifest{}, err
	}
	manifest, err := s.loadProofManifest(ctx)
	if err != nil {
		return models.PublicProofManifest{}, fmt.Errorf("public-feed: inspect proof manifest: %w", err)
	}
	if manifest == nil {
		return models.PublicProofManifest{}, nil
	}
	return *manifest, nil
}

// computeProofRootHash computes the SHA-256 proof root hash over the
// artifact hashes and manifest metadata (excluding the signature).
func (s *PublicPublisherService) computeProofRootHash(manifest models.PublicProofManifest) string {
	return computePublicProofRootHash(manifest)
}

func computePublicProofRootHash(manifest models.PublicProofManifest) string {
	h := sha256.New()
	h.Write([]byte(manifest.SchemaVersion))
	h.Write([]byte(manifest.CampaignID))
	h.Write([]byte(manifest.CampaignRevision))
	h.Write([]byte(manifest.VerifiedIndexGenerationHash))
	_, _ = fmt.Fprintf(h, "%t", manifest.VerificationOK)
	_, _ = fmt.Fprintf(h, "%d", manifest.ArtifactCount)
	for _, entry := range manifest.Artifacts {
		h.Write([]byte(entry.ArtifactID))
		h.Write([]byte(entry.SHA256))
		h.Write([]byte(entry.Filename))
		h.Write([]byte(entry.MediaType))
		_, _ = fmt.Fprintf(h, "%d", entry.ByteSize)
	}
	h.Write([]byte(manifest.GeneratedAt.UTC().Format(time.RFC3339Nano)))
	h.Write([]byte(manifest.SigningKeyID))
	return hex.EncodeToString(h.Sum(nil))
}

// ComputeProofRootHash recomputes a legacy full-manifest root. A segment-backed
// projection requires verification through its signed log.
func (s *PublicPublisherService) ComputeProofRootHash(manifest models.PublicProofManifest) (string, error) {
	if manifest.SegmentIndex != 0 {
		return "", constants.ErrPublicFeedProofManifestInvalid
	}
	return s.computeProofRootHash(manifest), nil
}

// GetProofCatalog returns the active public proof catalog.
func (s *PublicPublisherService) GetProofCatalog(ctx context.Context) (models.PublicProofCatalog, error) {
	s.proofMu.Lock()
	err := s.syncProofIndexLocked(ctx)
	if err == nil && len(s.proofIndex.segments) > 0 {
		catalog := s.proofIndex.catalog()
		s.proofMu.Unlock()
		return catalog, nil
	}
	s.proofMu.Unlock()
	if err != nil {
		return models.PublicProofCatalog{}, err
	}
	return s.loadLegacyProofCatalog(ctx)
}

// activeProofEntry returns the active catalog entry of one artifact.
func (s *PublicPublisherService) activeProofEntry(ctx context.Context, artifactID string) (models.PublicProofCatalogEntry, error) {
	s.proofMu.Lock()
	err := s.syncProofIndexLocked(ctx)
	if err == nil && len(s.proofIndex.segments) > 0 {
		entry, active := s.proofIndex.active[artifactID]
		s.proofMu.Unlock()
		if !active {
			return models.PublicProofCatalogEntry{}, constants.ErrPublicFeedProofNotFound
		}
		return entry, nil
	}
	s.proofMu.Unlock()
	if err != nil {
		return models.PublicProofCatalogEntry{}, err
	}
	legacy, err := s.loadLegacyProofCatalog(ctx)
	if err != nil {
		return models.PublicProofCatalogEntry{}, err
	}
	for _, entry := range legacy.Entries {
		if entry.ArtifactID == artifactID {
			return entry, nil
		}
	}
	return models.PublicProofCatalogEntry{}, constants.ErrPublicFeedProofNotFound
}

// loadLegacyProofCatalog reads the catalog a pre-segment package wrote. It
// returns an empty catalog when there is none.
func (s *PublicPublisherService) loadLegacyProofCatalog(ctx context.Context) (models.PublicProofCatalog, error) {
	exists, err := s.fileSvc.FileExists(ctx, constants.PublicProofCatalogFilename)
	if err != nil {
		return models.PublicProofCatalog{}, fmt.Errorf("public-feed: get catalog: stat: %w", err)
	}
	if !exists {
		return models.PublicProofCatalog{
			SchemaVersion: constants.PublicProofCatalogSchemaVersion,
			Entries:       []models.PublicProofCatalogEntry{},
			GeneratedAt:   time.Now().UTC(),
		}, nil
	}

	data, err := s.fileSvc.ReadFile(ctx, constants.PublicProofCatalogFilename)
	if err != nil {
		return models.PublicProofCatalog{}, fmt.Errorf("public-feed: get catalog: read: %w", err)
	}

	var catalog models.PublicProofCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return models.PublicProofCatalog{}, fmt.Errorf("public-feed: get catalog: unmarshal: %w", err)
	}
	if catalog.Entries == nil {
		catalog.Entries = []models.PublicProofCatalogEntry{}
	}
	return catalog, nil
}

// StreamProof serves a proof artifact by its content-addressed ID. It
// verifies the on-disk file hash against the catalog hash before serving,
// rejects symlinks, and sets fixed safe headers (Cache-Control: immutable,
// Content-Type, Content-Disposition).
func (s *PublicPublisherService) StreamProof(ctx context.Context, artifactID string, w http.ResponseWriter) error {
	if artifactID == "" {
		return constants.ErrPublicFeedProofIDRequired
	}

	entry, err := s.activeProofEntry(ctx, artifactID)
	if errors.Is(err, constants.ErrPublicFeedProofNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("public-feed: stream proof: get catalog: %w", err)
	}

	// Resolve the file path and check for symlinks.
	relPath := filepath.Join(constants.PublicProofsDirname, artifactID)

	// Check if the file is a symlink.
	fi, err := s.fileSvc.Lstat(ctx, relPath)
	if err != nil {
		return constants.ErrPublicFeedProofNotFound
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return constants.ErrPublicFeedProofSymlinkRejected
	}

	// Read the file.
	data, err := s.fileSvc.ReadFile(ctx, relPath)
	if err != nil {
		return constants.ErrPublicFeedProofNotFound
	}

	// Verify the hash.
	h := sha256.Sum256(data)
	computedHash := hex.EncodeToString(h[:])
	if computedHash != entry.SHA256 {
		return constants.ErrPublicFeedProofHashMismatch
	}

	// Verify the size.
	if int64(len(data)) != entry.ByteSize {
		return constants.ErrPublicFeedProofSizeMismatch
	}

	// Set safe headers.
	w.Header().Set("Content-Type", entry.MediaType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", entry.Filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))

	// Write the body.
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("public-feed: stream proof: write: %w", err)
	}

	return nil
}
