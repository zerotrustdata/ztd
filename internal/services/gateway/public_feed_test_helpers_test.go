// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 0.

//go:build integration

package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// assignmentSeed names one assignment whose live events a test seeds.
type assignmentSeed struct {
	runID        string
	assignmentID string
	events       int
}

// seedAssignmentEvents publishes one signed batch holding the canonical live
// events of every seeded assignment, through the publisher's mirror. The mirror
// store indexes them as it indexes the gateway's own feed, so they are the
// rows an assignment audit database is built from.
func seedAssignmentEvents(t *testing.T, publisher *PublicPublisherService, seeds ...assignmentSeed) {
	t.Helper()
	ctx := context.Background()
	publisher.mu.Lock()
	sequence := publisher.highWaterSeq + 1
	publisher.mu.Unlock()
	var records []models.PublicFeedRecord
	for _, seed := range seeds {
		for index := range seed.events {
			records = append(records, liveEventRecord(t, sequence, seed.runID, seed.assignmentID,
				fmt.Sprintf("%s-event-%d", seed.assignmentID, index), uint32(index), uint32(seed.events)))
			sequence++
		}
	}
	require.NoError(t, publisher.ExportBatch(ctx, records))
}

// liveEventRecord returns an event record at sequence whose canonical live
// event body names the run and assignment, with the given progress.
func liveEventRecord(t *testing.T, sequence int64, runID, assignmentID, eventID string, completed, total uint32) models.PublicFeedRecord {
	t.Helper()
	body, err := evalv1.MarshalCanonical(&evalv1.PublicLiveEvent{
		SchemaVersion:   "1.9.0",
		Kind:            "stage_updated",
		DatasetId:       "dataset-1",
		QualityState:    "live_in_progress",
		ObservedAt:      "2026-10-19T00:00:00Z",
		EventId:         eventID,
		RunId:           runID,
		AssignmentId:    assignmentID,
		LifecycleStatus: "running",
		Completed:       proto.Uint32(completed),
		Total:           proto.Uint32(total),
	})
	require.NoError(t, err)
	digest := sha256.Sum256(body)
	return models.PublicFeedRecord{
		Sequence:    sequence,
		RecordType:  models.PublicFeedRecordTypeEvent,
		RecordHash:  hex.EncodeToString(digest[:]),
		RecordBytes: string(body),
	}
}

// countingEventSource counts the reads an assignment audit database build makes.
type countingEventSource struct {
	inner PublicAssignmentEventSource
	calls int
}

func (c *countingEventSource) AssignmentEventRecords(ctx context.Context, sourceID, runID, assignmentID string) ([]models.PublicFeedRecord, error) {
	c.calls++
	return c.inner.AssignmentEventRecords(ctx, sourceID, runID, assignmentID)
}

// readTestProofSegments decodes every line of the publisher's proof log
// straight from disk, independently of the publisher's index.
func readTestProofSegments(t *testing.T, publisher *PublicPublisherService) []models.PublicProofManifestSegment {
	t.Helper()
	data, err := publisher.fileSvc.ReadFile(context.Background(), constants.PublicProofSegmentsFilename)
	require.NoError(t, err)
	var segments []models.PublicProofManifestSegment
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		segment, err := decodeProofSegmentLine(line)
		require.NoError(t, err)
		segments = append(segments, segment)
	}
	return segments
}

// PublicFeedRecordTypeInvalid is a sentinel invalid record type used by tests
// to verify that the publisher rejects unknown record types.
const PublicFeedRecordTypeInvalid models.PublicFeedRecordType = "invalid_type"

// httptestResponseWriter returns a fresh httptest.ResponseRecorder for
// StreamProof tests that need to inspect response headers and body.
func httptestResponseWriter() *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}
