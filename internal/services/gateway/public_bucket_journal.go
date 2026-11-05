// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// publicBucketJournalCompactBytes is the journal size past which it is rewritten
// to its live entries. A pointer line is written at most once a minute.
const publicBucketJournalCompactBytes = 1 << 20

// publicBucketJournal is the bucket publisher's durable state, an append-only
// file at public-feed/bucket-journal.jsonl: the last pointer that reached the
// bucket and the deletes and purges still owed. Objects already uploaded are not
// journaled, because the bucket's own listing is authoritative. One publisher
// loop owns it, so it takes no lock.
type publicBucketJournal struct {
	fileSvc fs.RuntimeFileService
	size    int64

	revision   int64
	sequence   int64
	withdrawal string
	deletes    []string
	purges     []string
}

// loadPublicBucketJournal replays the journal. A final line with no newline is an
// append that never finished, and it is dropped. Any other bad line fails closed.
func loadPublicBucketJournal(ctx context.Context, fileSvc fs.RuntimeFileService) (*publicBucketJournal, error) {
	journal := &publicBucketJournal{fileSvc: fileSvc}
	data, err := fileSvc.ReadFile(ctx, constants.PublicFeedBucketJournalPath)
	if errors.Is(err, constants.ErrNotFound) {
		return journal, nil
	}
	if err != nil {
		return nil, fmt.Errorf("public-feed: bucket journal read: %w", err)
	}
	complete := bytes.LastIndexByte(data, '\n') + 1
	for lineNumber, line := range bytes.Split(data[:complete], []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var entry models.PublicBucketJournalEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", constants.ErrPublicBucketJournalCorrupt, lineNumber+1, err)
		}
		if err := journal.apply(entry); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber+1, err)
		}
	}
	journal.size = int64(complete)
	if complete < len(data) {
		if err := fileSvc.Truncate(ctx, constants.PublicFeedBucketJournalPath, journal.size); err != nil {
			return nil, fmt.Errorf("public-feed: bucket journal discard incomplete tail: %w", err)
		}
	}
	return journal, nil
}

func (j *publicBucketJournal) apply(entry models.PublicBucketJournalEntry) error {
	switch entry.Kind {
	case models.PublicBucketJournalPointer:
		j.revision, j.sequence, j.withdrawal = entry.Revision, entry.Sequence, entry.WithdrawalDigest
	case models.PublicBucketJournalDelete:
		if !slices.Contains(j.deletes, entry.Key) {
			j.deletes = append(j.deletes, entry.Key)
		}
	case models.PublicBucketJournalDeleted:
		j.deletes = slices.DeleteFunc(j.deletes, func(key string) bool { return key == entry.Key })
	case models.PublicBucketJournalPurge:
		if !slices.Contains(j.purges, entry.Key) {
			j.purges = append(j.purges, entry.Key)
		}
	case models.PublicBucketJournalPurged:
		j.purges = slices.DeleteFunc(j.purges, func(key string) bool { return key == entry.Key })
	default:
		return fmt.Errorf("%w: unknown kind %q", constants.ErrPublicBucketJournalCorrupt, entry.Kind)
	}
	return nil
}

// record makes entries durable, then applies them.
func (j *publicBucketJournal) record(ctx context.Context, entries ...models.PublicBucketJournalEntry) error {
	if len(entries) == 0 {
		return nil
	}
	var lines bytes.Buffer
	for _, entry := range entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("public-feed: bucket journal encode: %w", err)
		}
		lines.Write(encoded)
		lines.WriteByte('\n')
	}
	file, err := j.fileSvc.OpenForAppend(ctx, constants.PublicFeedBucketJournalPath, constants.PermFilePrivate)
	if err != nil {
		return fmt.Errorf("public-feed: bucket journal open: %w", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(lines.Bytes()); err != nil {
		return fmt.Errorf("public-feed: bucket journal write: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("public-feed: bucket journal sync: %w", err)
	}
	for _, entry := range entries {
		if err := j.apply(entry); err != nil {
			return err
		}
	}
	j.size += int64(lines.Len())
	if j.size >= publicBucketJournalCompactBytes {
		return j.compact(ctx)
	}
	return nil
}

// compact atomically replaces the journal with its live entries.
func (j *publicBucketJournal) compact(ctx context.Context) error {
	var lines bytes.Buffer
	write := func(entry models.PublicBucketJournalEntry) error {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("public-feed: bucket journal encode: %w", err)
		}
		lines.Write(encoded)
		lines.WriteByte('\n')
		return nil
	}
	if j.revision != 0 {
		if err := write(models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPointer, Revision: j.revision, Sequence: j.sequence, WithdrawalDigest: j.withdrawal}); err != nil {
			return err
		}
	}
	for _, key := range j.deletes {
		if err := write(models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalDelete, Key: key}); err != nil {
			return err
		}
	}
	for _, url := range j.purges {
		if err := write(models.PublicBucketJournalEntry{Kind: models.PublicBucketJournalPurge, Key: url}); err != nil {
			return err
		}
	}
	if err := j.fileSvc.WriteFile(ctx, constants.PublicFeedBucketJournalPath, lines.Bytes(), constants.PermFilePrivate); err != nil {
		return fmt.Errorf("public-feed: bucket journal compact: %w", err)
	}
	j.size = int64(lines.Len())
	return nil
}

// ReadPublicBucketStatus reports the bucket publisher's state from its journal,
// or nil when bucket publishing is off.
func ReadPublicBucketStatus(ctx context.Context, fileSvc fs.RuntimeFileService, exportConfig models.PublicExportConfig) (*models.PublicBucketStatus, error) {
	if exportConfig.Bucket == nil {
		return nil, nil
	}
	journal, err := loadPublicBucketJournal(ctx, fileSvc)
	if err != nil {
		return nil, err
	}
	return &models.PublicBucketStatus{
		Name:            exportConfig.Bucket.Name,
		PointerRevision: journal.revision,
		PointerSequence: journal.sequence,
		PendingDeletes:  len(journal.deletes),
		PendingPurges:   len(journal.purges),
	}, nil
}
