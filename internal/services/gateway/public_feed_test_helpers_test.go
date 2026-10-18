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
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

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
