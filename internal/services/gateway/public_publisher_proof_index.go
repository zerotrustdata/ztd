// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// publicProofIndex is the publisher's in-memory view of its signed proof log
// (constants.PublicProofSegmentsFilename). It is built once by replaying and
// verifying the log, then advanced by each append, so publication, catalog
// reads, and delivery never rescan history. Appends made by another process
// are applied by reading only the bytes past offset.
type publicProofIndex struct {
	// file identifies the indexed log. A replaced or shortened log is
	// replayed from the start.
	file os.FileInfo
	// offset is the length of the complete lines applied. Bytes past it are
	// an incomplete tail.
	offset   int64
	segments []publicProofIndexedSegment
	// head is the last segment without its entries and withdrawals.
	head models.PublicProofManifestSegment
	// order is the active catalog order; it may hold withdrawn IDs until the
	// next compaction.
	order  []string
	active map[string]models.PublicProofCatalogEntry
	// listed holds every artifact ID the log has listed, withdrawn or not. A
	// listed ID can never be listed again.
	listed map[string]struct{}
	// runs lists artifact IDs by source run; withdrawn IDs are filtered on read.
	runs map[string][]string
}

// publicProofIndexedSegment locates one segment line in the log.
type publicProofIndexedSegment struct {
	offset int64
	root   string
}

// publicProofLogRange is the run of log lines delivery reads for one push.
type publicProofLogRange struct {
	file  os.FileInfo
	start int64
	end   int64
	// roots are the expected roots of the lines in the range, in order.
	roots []string
}

func newPublicProofIndex() *publicProofIndex {
	return &publicProofIndex{
		active: make(map[string]models.PublicProofCatalogEntry),
		listed: make(map[string]struct{}),
		runs:   make(map[string][]string),
	}
}

// check reports whether seg is the valid next link: consecutive index and
// root link, no relisted artifact, and withdrawals of active artifacts only.
// Artifact limits apply to each signed segment, not the
// accumulated catalog. These are the mirror's rules.
func (x *publicProofIndex) check(seg models.PublicProofManifestSegment) error {
	previousRoot := constants.PublicFeedZeroHashHex
	if len(x.segments) > 0 {
		previousRoot = x.head.ProofRootSHA256
	}
	if seg.SegmentIndex != int64(len(x.segments))+1 || seg.PreviousRoot != previousRoot {
		return constants.ErrPublicFeedProofSegmentOutOfOrder
	}
	for _, entry := range seg.Entries {
		if _, listed := x.listed[entry.ArtifactID]; listed {
			return constants.ErrPublicFeedProofSegmentEquivocation
		}
	}
	for _, id := range seg.WithdrawnArtifactIDs {
		if _, active := x.active[id]; !active {
			return constants.ErrPublicFeedProofSegmentInvalid
		}
	}
	return nil
}

// commit applies a checked segment whose line starts at offset and is length
// bytes long, including its newline.
func (x *publicProofIndex) commit(seg models.PublicProofManifestSegment, offset, length int64) {
	x.segments = append(x.segments, publicProofIndexedSegment{offset: offset, root: seg.ProofRootSHA256})
	for _, entry := range seg.Entries {
		x.listed[entry.ArtifactID] = struct{}{}
		x.active[entry.ArtifactID] = entry
		x.order = append(x.order, entry.ArtifactID)
		if entry.SourceRunID != "" {
			x.runs[entry.SourceRunID] = append(x.runs[entry.SourceRunID], entry.ArtifactID)
		}
	}
	for _, id := range seg.WithdrawnArtifactIDs {
		delete(x.active, id)
	}
	if len(seg.WithdrawnArtifactIDs) > 0 && len(x.order) > 2*len(x.active) {
		order := make([]string, 0, len(x.active))
		for _, id := range x.order {
			if _, active := x.active[id]; active {
				order = append(order, id)
			}
		}
		x.order = order
	}
	x.head = seg
	x.head.Entries = nil
	x.head.WithdrawnArtifactIDs = nil
	x.offset = offset + length
}

// catalog returns the active entries in listing order.
func (x *publicProofIndex) catalog() models.PublicProofCatalog {
	entries := make([]models.PublicProofCatalogEntry, 0, len(x.active))
	for _, id := range x.order {
		if entry, active := x.active[id]; active {
			entries = append(entries, entry)
		}
	}
	return models.PublicProofCatalog{
		SchemaVersion: constants.PublicProofCatalogSchemaVersion,
		Entries:       entries,
		GeneratedAt:   x.head.GeneratedAt,
	}
}

// headProjection is the head segment's root and signature with the active
// artifact count. It does not list the active artifacts.
func (x *publicProofIndex) headProjection() models.PublicProofManifest {
	manifest := publicProofManifestProjection(x.head, nil)
	manifest.ArtifactCount = len(x.active)
	return manifest
}

// activeRunArtifactIDs returns the active artifact IDs of one source run, in
// listing order.
func (x *publicProofIndex) activeRunArtifactIDs(runID string) []string {
	ids := make([]string, 0, len(x.runs[runID]))
	for _, id := range x.runs[runID] {
		if _, active := x.active[id]; active {
			ids = append(ids, id)
		}
	}
	return ids
}

// pendingAfter returns the log lines after the mirror's head. It rejects a
// divergent or advanced mirror head before anything is sent. Index zero
// denotes an empty mirror or a legacy package.
func (x *publicProofIndex) pendingAfter(root models.PublicProofRootResponse) (publicProofLogRange, error) {
	if root.SegmentIndex < 0 || root.SegmentIndex > int64(len(x.segments)) {
		return publicProofLogRange{}, constants.ErrPublicFeedProofSegmentOutOfOrder
	}
	if root.SegmentIndex > 0 && x.segments[root.SegmentIndex-1].root != root.ProofRootSHA256 {
		return publicProofLogRange{}, constants.ErrPublicFeedProofSegmentEquivocation
	}
	pending := x.segments[root.SegmentIndex:]
	logRange := publicProofLogRange{file: x.file, start: x.offset, end: x.offset, roots: make([]string, len(pending))}
	for i, segment := range pending {
		logRange.roots[i] = segment.root
	}
	if len(pending) > 0 {
		logRange.start = pending[0].offset
	}
	return logRange, nil
}

// proofSigningKey returns the current signing key under the service lock.
func (s *PublicPublisherService) proofSigningKey() (ed25519.PrivateKey, ed25519.PublicKey, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signingPrivKey, s.signingPubKey, s.signingKeyID
}

// verifyProofSegmentLocked checks seg as the index's next link and verifies
// its shape, root, and signature. A segment signed by a rotated key has its
// shape and root verified here; its signature is verified by the mirror's
// key registry, since the publisher keeps only its current key.
func (s *PublicPublisherService) verifyProofSegmentLocked(seg models.PublicProofManifestSegment) error {
	if err := s.proofIndex.check(seg); err != nil {
		return err
	}
	_, publicKey, keyID := s.proofSigningKey()
	err := models.VerifyPublicProofSegment(s.cfg.SourceID, seg, publicKey)
	if errors.Is(err, constants.ErrPublicFeedSignatureInvalid) && seg.SigningKeyID != keyID {
		return nil
	}
	return err
}

// syncProofIndexLocked brings the index up to the log on disk: a first call
// or a replaced log replays every line, otherwise only lines appended since
// the last sync are read. Every applied line is verified; an invalid line
// fails closed. An incomplete tail is left unapplied. Callers hold proofMu.
func (s *PublicPublisherService) syncProofIndexLocked(ctx context.Context) error {
	f, err := s.fileSvc.OpenForRead(ctx, constants.PublicProofSegmentsFilename)
	if errors.Is(err, constants.ErrNotFound) {
		if s.proofIndex.file != nil || len(s.proofIndex.segments) > 0 {
			s.proofIndex = newPublicProofIndex()
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("public-feed: open proof segments: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("public-feed: inspect proof segments: %w", err)
	}
	if s.proofIndex.file == nil || !os.SameFile(s.proofIndex.file, info) || info.Size() < s.proofIndex.offset {
		replayed := newPublicProofIndex()
		replayed.file = info
		previous := s.proofIndex
		s.proofIndex = replayed
		if err := s.applyProofLogLocked(f, info.Size()); err != nil {
			s.proofIndex = previous
			return err
		}
		return nil
	}
	s.proofIndex.file = info
	return s.applyProofLogLocked(f, info.Size())
}

// applyProofLogLocked verifies and applies the complete lines of f between
// the index offset and size.
func (s *PublicPublisherService) applyProofLogLocked(f *os.File, size int64) error {
	if size == s.proofIndex.offset {
		return nil
	}
	if _, err := f.Seek(s.proofIndex.offset, io.SeekStart); err != nil {
		return fmt.Errorf("public-feed: seek proof segments: %w", err)
	}
	reader := bufio.NewReader(io.LimitReader(f, size-s.proofIndex.offset))
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("public-feed: read proof segments: %w", err)
		}
		seg, err := decodeProofSegmentLine(line)
		if err != nil {
			return fmt.Errorf("%w: segment line at byte %d: %w", constants.ErrPublicFeedProofSegmentInvalid, s.proofIndex.offset, err)
		}
		if err := s.verifyProofSegmentLocked(seg); err != nil {
			return fmt.Errorf("public-feed: proof segment %d at byte %d: %w", seg.SegmentIndex, s.proofIndex.offset, err)
		}
		s.proofIndex.commit(seg, s.proofIndex.offset, int64(len(line)))
	}
}

func decodeProofSegmentLine(line []byte) (models.PublicProofManifestSegment, error) {
	var seg models.PublicProofManifestSegment
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&seg); err != nil {
		return models.PublicProofManifestSegment{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return models.PublicProofManifestSegment{}, fmt.Errorf("trailing JSON: %w", constants.ErrInvalidJSONBody)
	}
	return seg, nil
}

// lockProofLog takes the cross-process append lock on the proof log. Another
// process holding it fails the append rather than forking the chain.
func (s *PublicPublisherService) lockProofLog(ctx context.Context) (io.Closer, error) {
	held, err := s.fileSvc.TryLock(ctx, constants.PublicProofSegmentsLockFilename, constants.PermFilePrivate)
	if errors.Is(err, constants.ErrRuntimeFileLocked) {
		return nil, fmt.Errorf("public-feed: another process is appending to the proof log; retry when it finishes: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("public-feed: lock proof log: %w", err)
	}
	return held, nil
}

// signProofSegment fills in the root, key ID, and signature of seg.
func (s *PublicPublisherService) signProofSegment(seg models.PublicProofManifestSegment) (models.PublicProofManifestSegment, error) {
	privateKey, _, keyID := s.proofSigningKey()
	seg.SigningKeyID = keyID
	seg.ProofRootSHA256 = models.ComputePublicProofSegmentRoot(seg)
	root, err := hex.DecodeString(seg.ProofRootSHA256)
	if err != nil {
		return models.PublicProofManifestSegment{}, fmt.Errorf("public-feed: decode proof segment root: %w", err)
	}
	seg.Signature = hex.EncodeToString(ed25519.Sign(privateKey, root))
	return seg, nil
}

// appendProofSegmentLocked verifies seg as the next link, appends it to the
// log, and applies it to the index. Callers hold proofMu and the proof log
// lock, and have synced the index under that lock, so bytes past the index
// offset are the incomplete tail of an append that never finished; that
// segment was never acknowledged or delivered, and is discarded.
func (s *PublicPublisherService) appendProofSegmentLocked(ctx context.Context, seg models.PublicProofManifestSegment) error {
	if err := s.verifyProofSegmentLocked(seg); err != nil {
		return err
	}
	line, err := json.Marshal(seg)
	if err != nil {
		return fmt.Errorf("public-feed: marshal proof segment: %w", err)
	}
	line = append(line, '\n')
	f, err := s.fileSvc.OpenForAppend(ctx, constants.PublicProofSegmentsFilename, constants.PermFilePrivate)
	if err != nil {
		return fmt.Errorf("public-feed: open proof segments for append: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("public-feed: inspect proof segments: %w", err)
	}
	index := s.proofIndex
	if (index.file != nil && !os.SameFile(index.file, info)) || info.Size() < index.offset {
		return fmt.Errorf("%w: proof log changed under the append lock", constants.ErrPublicFeedProofSegmentInvalid)
	}
	if info.Size() > index.offset {
		s.logger.Warn("public-feed: discarding incomplete proof segment tail", "bytes", info.Size()-index.offset)
		// Close the O_APPEND handle before truncating: File.Truncate on an
		// O_APPEND handle fails on Windows with "Access is denied" because
		// the handle carries FILE_APPEND_DATA only. Truncate by path, then
		// reopen for the append below.
		if err := f.Close(); err != nil {
			return fmt.Errorf("public-feed: close proof segments before truncate: %w", err)
		}
		if err := s.fileSvc.Truncate(ctx, constants.PublicProofSegmentsFilename, index.offset); err != nil {
			return fmt.Errorf("public-feed: discard incomplete proof segment tail: %w", err)
		}
		reopened, err := s.fileSvc.OpenForAppend(ctx, constants.PublicProofSegmentsFilename, constants.PermFilePrivate)
		if err != nil {
			return fmt.Errorf("public-feed: reopen proof segments after truncate: %w", err)
		}
		f = reopened
	}
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("public-feed: write proof segment: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("public-feed: sync proof segment: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("public-feed: close proof segment: %w", err)
	}
	index.file = info
	index.commit(seg, index.offset, int64(len(line)))
	return nil
}

// readProofLogRange decodes each line of logRange in order and passes it to
// fn. A line whose root differs from the index fails closed.
func (s *PublicPublisherService) readProofLogRange(ctx context.Context, logRange publicProofLogRange, fn func(models.PublicProofManifestSegment) error) error {
	if len(logRange.roots) == 0 {
		return nil
	}
	f, err := s.fileSvc.OpenForRead(ctx, constants.PublicProofSegmentsFilename)
	if err != nil {
		return fmt.Errorf("public-feed: open proof segments: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("public-feed: inspect proof segments: %w", err)
	}
	if !os.SameFile(logRange.file, info) || info.Size() < logRange.end {
		return fmt.Errorf("%w: proof log replaced during delivery", constants.ErrPublicFeedProofSegmentInvalid)
	}
	if _, err := f.Seek(logRange.start, io.SeekStart); err != nil {
		return fmt.Errorf("public-feed: seek proof segments: %w", err)
	}
	reader := bufio.NewReader(io.LimitReader(f, logRange.end-logRange.start))
	for _, root := range logRange.roots {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("%w: read indexed segment: %w", constants.ErrPublicFeedProofSegmentInvalid, err)
		}
		seg, err := decodeProofSegmentLine(line)
		if err != nil || seg.ProofRootSHA256 != root {
			return fmt.Errorf("%w: indexed segment differs from the log", constants.ErrPublicFeedProofSegmentInvalid)
		}
		if err := fn(seg); err != nil {
			return err
		}
	}
	return nil
}
