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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

const (
	// publicOutboxCompactMinBytes is the journal size below which pruned bytes
	// are left in place. Compaction rewrites the live entries, so waiting until
	// the journal is at least twice their size keeps its amortized cost
	// proportional to what was appended.
	publicOutboxCompactMinBytes = 8 << 20
	// publicOutboxLineOverheadBytes estimates a line's fields beyond batch_bytes.
	publicOutboxLineOverheadBytes = 256
)

type PublicOutboxStore interface {
	Append(context.Context, models.PublicOutboxEntry) error
	List(context.Context) ([]models.PublicOutboxEntry, error)
	Update(context.Context, models.PublicOutboxEntry) error
	PruneAcknowledged(context.Context, time.Time) error
	Replace(context.Context, []models.PublicOutboxEntry) error
}

// runtimePublicOutboxStore is the publisher's durable outbox, an append-only
// journal at public-feed/outbox.jsonl. A line carrying batch_bytes adds an
// entry. A line without batch_bytes is a status transition of the entry with
// its sequence and batch hash. Every operation costs what it changes: the store
// keeps the applied entries in memory and reads only lines another process
// appended since the last operation, so a batch is decoded and verified once
// and never rewritten by a status change. Pruning drops entries from the view
// and compacts the journal once pruned bytes outweigh the live ones.
type runtimePublicOutboxStore struct {
	fileSvc fs.RuntimeFileService
	// compactMinBytes is the journal size below which pruned bytes stay in place.
	compactMinBytes int64

	mu      sync.Mutex
	entries []models.PublicOutboxEntry
	file    os.FileInfo
	offset  int64
}

func newRuntimePublicOutboxStore(fileSvc fs.RuntimeFileService) PublicOutboxStore {
	return &runtimePublicOutboxStore{fileSvc: fileSvc, compactMinBytes: publicOutboxCompactMinBytes, entries: []models.PublicOutboxEntry{}}
}

func (s *runtimePublicOutboxStore) Append(ctx context.Context, entry models.PublicOutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(ctx); err != nil {
		return err
	}
	for _, existing := range s.entries {
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
	if len(s.entries) > 0 {
		last := s.entries[len(s.entries)-1]
		var batch models.PublicFeedBatch
		if err := json.Unmarshal([]byte(entry.BatchBytes), &batch); err != nil {
			return fmt.Errorf("%w: decode appended batch: %w", constants.ErrPublicFeedOutboxCorrupt, err)
		}
		if entry.Sequence <= last.Sequence {
			return constants.ErrPublicFeedSequenceOutOfOrder
		}
		if batch.FirstSequence != last.Sequence+1 || batch.PreviousBatchHash != last.BatchHash {
			return constants.ErrPublicFeedHashChainMismatch
		}
	}
	if err := s.appendLine(ctx, entry, true); err != nil {
		return err
	}
	s.entries = append(s.entries, entry)
	return nil
}

func (s *runtimePublicOutboxStore) List(ctx context.Context) ([]models.PublicOutboxEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(ctx); err != nil {
		return nil, err
	}
	return slices.Clone(s.entries), nil
}

// Update journals one entry's status. A lost transition only causes an
// idempotent retransmission, so the line is not synced to disk.
func (s *runtimePublicOutboxStore) Update(ctx context.Context, entry models.PublicOutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(ctx); err != nil {
		return err
	}
	for i := range s.entries {
		if s.entries[i].Sequence != entry.Sequence {
			continue
		}
		if s.entries[i].BatchHash != entry.BatchHash || s.entries[i].BatchBytes != entry.BatchBytes {
			return constants.ErrPublicFeedOutboxEquivocation
		}
		transition := entry
		transition.BatchBytes = ""
		if err := s.appendLine(ctx, transition, false); err != nil {
			return err
		}
		s.entries[i] = entry
		return nil
	}
	return constants.ErrPublicFeedOutboxCorrupt
}

// Replace rewrites the journal with exactly entries, without validating them.
// The next operation replays the new file and fails closed if its chain breaks.
func (s *runtimePublicOutboxStore) Replace(ctx context.Context, entries []models.PublicOutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset()
	_, err := s.write(ctx, entries)
	return err
}

// readRaw returns the journal's entries in sequence order with their status
// transitions applied, without chain validation, for repairing a broken chain.
func (s *runtimePublicOutboxStore) readRaw(ctx context.Context) ([]models.PublicOutboxEntry, error) {
	return readPublicOutboxJournal(ctx, s.fileSvc)
}

// ReadPublicOutboxEntries returns the entries of the outbox journal in sequence
// order with their status transitions applied, without chain validation.
func ReadPublicOutboxEntries(ctx context.Context, fileSvc fs.RuntimeFileService) ([]models.PublicOutboxEntry, error) {
	return readPublicOutboxJournal(ctx, fileSvc)
}

func readPublicOutboxJournal(ctx context.Context, fileSvc fs.RuntimeFileService) ([]models.PublicOutboxEntry, error) {
	exists, err := fileSvc.FileExists(ctx, constants.PublicFeedOutboxPath)
	if err != nil {
		return nil, fmt.Errorf("public-feed: outbox stat: %w", err)
	}
	if !exists {
		return []models.PublicOutboxEntry{}, nil
	}
	data, err := fileSvc.ReadFile(ctx, constants.PublicFeedOutboxPath)
	if err != nil {
		return nil, fmt.Errorf("public-feed: outbox read: %w", err)
	}
	entries := []models.PublicOutboxEntry{}
	for lineNumber := 1; ; lineNumber++ {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			break
		}
		entries, err = applyPublicOutboxLine(entries, data[:end], false)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		data = data[end+1:]
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Sequence < entries[j].Sequence })
	return entries, nil
}

func (s *runtimePublicOutboxStore) PruneAcknowledged(ctx context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(ctx); err != nil {
		return err
	}
	pruned := 0
	for pruned < len(s.entries) {
		head := s.entries[pruned]
		if head.Status != models.PublicFeedOutboxStatusAcknowledged || head.AcknowledgedAt == nil || !head.AcknowledgedAt.Before(before) {
			break
		}
		pruned++
	}
	if pruned == 0 {
		return nil
	}
	s.entries = slices.Clone(s.entries[pruned:])
	var live int64
	for _, entry := range s.entries {
		live += int64(len(entry.BatchBytes)) + publicOutboxLineOverheadBytes
	}
	if s.offset < s.compactMinBytes || s.offset < 2*live {
		return nil
	}
	written, err := s.write(ctx, s.entries)
	if err != nil {
		return err
	}
	info, err := s.fileSvc.Lstat(ctx, constants.PublicFeedOutboxPath)
	if err != nil {
		s.reset()
		return fmt.Errorf("public-feed: outbox stat: %w", err)
	}
	s.file, s.offset = info, written
	return nil
}

func (s *runtimePublicOutboxStore) reset() {
	s.entries = []models.PublicOutboxEntry{}
	s.file = nil
	s.offset = 0
}

// sync brings the in-memory view up to the journal on disk. A first call or a
// replaced file replays every line; otherwise only complete lines past the
// applied offset are read. An incomplete tail is left unapplied. A line that
// fails to apply leaves the view empty, so the next call replays and reports it
// again rather than serving a partial chain.
func (s *runtimePublicOutboxStore) sync(ctx context.Context) error {
	f, err := s.fileSvc.OpenForRead(ctx, constants.PublicFeedOutboxPath)
	if errors.Is(err, constants.ErrNotFound) {
		s.reset()
		return nil
	}
	if err != nil {
		return fmt.Errorf("public-feed: outbox read: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("public-feed: outbox stat: %w", err)
	}
	same := s.file != nil && os.SameFile(s.file, info)
	if same && info.Size() == s.offset && info.ModTime().Equal(s.file.ModTime()) {
		return nil
	}
	if !same || info.Size() <= s.offset {
		s.reset()
	}
	if err := s.applyFrom(f, info); err == nil {
		return nil
	} else if s.offset == 0 {
		s.reset()
		return err
	}
	// The suffix did not follow the view, so the file is not the one it was read
	// from. Replay it whole.
	s.reset()
	if err := s.applyFrom(f, info); err != nil {
		s.reset()
		return err
	}
	return nil
}

// applyFrom applies the complete lines of f past the applied offset.
func (s *runtimePublicOutboxStore) applyFrom(f *os.File, info os.FileInfo) error {
	if _, err := f.Seek(s.offset, io.SeekStart); err != nil {
		return fmt.Errorf("public-feed: outbox seek: %w", err)
	}
	reader := bufio.NewReader(io.LimitReader(f, info.Size()-s.offset))
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			s.file = info
			return nil
		}
		if err != nil {
			return fmt.Errorf("public-feed: outbox read: %w", err)
		}
		entries, err := applyPublicOutboxLine(s.entries, line, true)
		if err != nil {
			return fmt.Errorf("byte %d: %w", s.offset, err)
		}
		s.entries = entries
		s.offset += int64(len(line))
	}
}

// applyPublicOutboxLine applies one journal line. A blank line is ignored. With
// checkChain, a new entry must extend the hash chain of the entries before it.
func applyPublicOutboxLine(entries []models.PublicOutboxEntry, line []byte, checkChain bool) ([]models.PublicOutboxEntry, error) {
	if len(bytes.TrimSpace(line)) == 0 {
		return entries, nil
	}
	var entry models.PublicOutboxEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrPublicFeedOutboxCorrupt, err)
	}
	if entry.BatchBytes == "" {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Sequence != entry.Sequence {
				continue
			}
			if entries[i].BatchHash != entry.BatchHash {
				return nil, constants.ErrPublicFeedOutboxEquivocation
			}
			entry.BatchBytes = entries[i].BatchBytes
			entries[i] = entry
			return entries, nil
		}
		return nil, fmt.Errorf("%w: status transition for unknown sequence %d", constants.ErrPublicFeedOutboxCorrupt, entry.Sequence)
	}
	if err := validatePublicOutboxEntry(entry); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrPublicFeedOutboxCorrupt, err)
	}
	if checkChain && len(entries) > 0 {
		var batch models.PublicFeedBatch
		if err := json.Unmarshal([]byte(entry.BatchBytes), &batch); err != nil {
			return nil, fmt.Errorf("%w: batch: %w", constants.ErrPublicFeedOutboxCorrupt, err)
		}
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
	return append(entries, entry), nil
}

// appendLine appends one journal line, first discarding an incomplete tail left
// by an append that never finished. That entry was never transmitted. When
// durable, the line reaches disk before appendLine returns.
func (s *runtimePublicOutboxStore) appendLine(ctx context.Context, entry models.PublicOutboxEntry, durable bool) error {
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("public-feed: outbox marshal: %w", err)
	}
	line = append(line, '\n')
	f, err := s.fileSvc.OpenForAppend(ctx, constants.PublicFeedOutboxPath, constants.PermFilePrivate)
	if err != nil {
		return fmt.Errorf("public-feed: outbox open: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("public-feed: outbox stat: %w", err)
	}
	if info.Size() > s.offset {
		// Close the O_APPEND handle first: File.Truncate on one fails on Windows.
		if err := f.Close(); err != nil {
			return fmt.Errorf("public-feed: outbox close before truncate: %w", err)
		}
		if err := s.fileSvc.Truncate(ctx, constants.PublicFeedOutboxPath, s.offset); err != nil {
			return fmt.Errorf("public-feed: outbox discard incomplete tail: %w", err)
		}
		if f, err = s.fileSvc.OpenForAppend(ctx, constants.PublicFeedOutboxPath, constants.PermFilePrivate); err != nil {
			return fmt.Errorf("public-feed: outbox reopen: %w", err)
		}
	}
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("public-feed: outbox write: %w", err)
	}
	if durable {
		if err := f.Sync(); err != nil {
			return fmt.Errorf("public-feed: outbox sync: %w", err)
		}
	}
	if info, err = f.Stat(); err != nil {
		return fmt.Errorf("public-feed: outbox stat: %w", err)
	}
	s.file, s.offset = info, info.Size()
	return nil
}

// write atomically replaces the journal with entries and returns its size.
func (s *runtimePublicOutboxStore) write(ctx context.Context, entries []models.PublicOutboxEntry) (int64, error) {
	var data bytes.Buffer
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return 0, fmt.Errorf("public-feed: outbox marshal: %w", err)
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	if err := s.fileSvc.WriteFile(ctx, constants.PublicFeedOutboxPath, data.Bytes(), constants.PermFilePrivate); err != nil {
		return 0, fmt.Errorf("public-feed: outbox write: %w", err)
	}
	return int64(data.Len()), nil
}
