// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// These tests cover the run bundle through the real stack: the CLI coordinator
// asks the Gateway for the run's assignment bundle, the summary names its
// digest, and the public mirror serves the bundle and lists it in the proof
// catalog. Exact record bytes come from the mirror's /stream, because /history
// re-encodes each item.

// mirrorStreamRecord is one retained record the mirror replays on /stream, with
// the record bytes exactly as the mirror stores them.
type mirrorStreamRecord struct {
	sequence int64
	bytes    string
	key      string
}

// mirrorGet returns the status and body of a GET on the public mirror origin.
func mirrorGet(t *testing.T, path string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, gwremote.PublicMirrorOrigin+path, nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { require.NoError(t, response.Body.Close()) }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, body
}

// mirrorProofCatalogEntries returns the active entries of the mirror's proof catalog.
func mirrorProofCatalogEntries(t *testing.T) []models.PublicProofCatalogEntry {
	t.Helper()
	status, body := mirrorGet(t, "/proof-catalog")
	require.Equal(t, http.StatusOK, status, string(body))
	var catalog models.PublicProofCatalog
	require.NoError(t, json.Unmarshal(body, &catalog))
	return catalog.Entries
}

// runBundleEntries returns the active catalog entries that are runID's assignment bundle.
func runBundleEntries(t *testing.T, runID string) []models.PublicProofCatalogEntry {
	t.Helper()
	filename := runID + constants.PublicRunBundleFilenameSuffix
	var entries []models.PublicProofCatalogEntry
	for _, entry := range mirrorProofCatalogEntries(t) {
		if entry.Filename == filename {
			entries = append(entries, entry)
		}
	}
	return entries
}

// assertSingleActiveBundle requires the catalog to list exactly one bundle for
// the run, with the given digest as its artifact id.
func assertSingleActiveBundle(t *testing.T, runID, digest string) {
	t.Helper()
	entries := runBundleEntries(t, runID)
	require.Len(t, entries, 1, "exactly one active assignment bundle for the run")
	assert.Equal(t, digest, entries[0].ArtifactID, "the active bundle is the one the summary names")
	assert.Equal(t, constants.MediaTypeGzip, entries[0].MediaType)
}

// runAuditProofIDs maps the active assignment database and agent trace artifact
// ids of runID to their filenames.
func runAuditProofIDs(t *testing.T, runID string) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, entry := range mirrorProofCatalogEntries(t) {
		if entry.SourceRunID != runID {
			continue
		}
		if strings.HasSuffix(entry.Filename, ".db") || strings.HasSuffix(entry.Filename, ".agent-trace.json") {
			ids[entry.ArtifactID] = entry.Filename
		}
	}
	return ids
}

// latestSummaryBundleDigest returns assignment_bundle_sha256 of the latest
// evaluation_summary the mirror serves, or empty when it names none.
func latestSummaryBundleDigest(t *testing.T) string {
	t.Helper()
	digest := ""
	found := false
	for _, projection := range mirrorProjections(t) {
		var summary struct {
			Kind   string `json:"kind"`
			Bundle string `json:"assignment_bundle_sha256"`
		}
		require.NoError(t, json.Unmarshal(projection.record, &summary))
		if summary.Kind == "evaluation_summary" {
			digest = summary.Bundle
			found = true
		}
	}
	require.True(t, found, "the mirror serves an evaluation summary")
	return digest
}

// mirrorStreamRecords replays the mirror's /stream from the start and returns
// every retained record through the high-water sequence.
func mirrorStreamRecords(t *testing.T, through int64) []mirrorStreamRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, gwremote.PublicMirrorOrigin+"/stream?since_id=0", nil)
	require.NoError(t, err)
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusOK, response.StatusCode)

	var records []mirrorStreamRecord
	var event, data string
	var sequence int64
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if event != "" && event != "snapshot" && event != "truncated" && event != "error" && data != "" {
				var identity struct {
					Key string `json:"idempotency_key"`
				}
				require.NoError(t, json.Unmarshal([]byte(data), &identity))
				records = append(records, mirrorStreamRecord{sequence: sequence, bytes: data, key: identity.Key})
			}
			if sequence >= through {
				return records
			}
			event, data, sequence = "", "", 0
		case strings.HasPrefix(line, "id: "):
			sequence, err = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			require.NoError(t, err)
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	require.Fail(t, "the mirror stream ended before the high-water sequence", "through %d", through)
	return nil
}

// decodeRunBundle gunzips a bundle and decodes each JSONL line.
func decodeRunBundle(t *testing.T, body []byte) []models.PublicRunBundleLine {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(body))
	require.NoError(t, err)
	plain, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	var lines []models.PublicRunBundleLine
	for _, raw := range bytes.Split(bytes.TrimSuffix(plain, []byte("\n")), []byte("\n")) {
		var line models.PublicRunBundleLine
		require.NoError(t, json.Unmarshal(raw, &line))
		lines = append(lines, line)
	}
	return lines
}

// acceptedResultBytes returns, for each assignment, the bytes of the result
// revision the mirror holds last: the snapshot keeps the latest accepted row per
// assignment, so a verified or verification-failed revision supersedes the
// completion revision. The mirror's /stream replays superseded revisions too,
// so the latest sequence wins.
func acceptedResultBytes(t *testing.T, records []mirrorStreamRecord, runID string, assignments []*evalv1.EvaluationAssignment) []string {
	t.Helper()
	owner := map[string]string{}
	for _, assignment := range assignments {
		id := assignment.GetAssignmentId()
		for _, key := range []string{
			evaluation.AssignmentResultIdempotencyKey(runID, id),
			evaluation.AssignmentVerifiedResultIdempotencyKey(runID, id),
			evaluation.AssignmentFailedVerificationResultIdempotencyKey(runID, id),
		} {
			owner[key] = id
		}
	}
	acceptedByAssignment := map[string]string{}
	for _, record := range records {
		if id, ok := owner[record.key]; ok {
			acceptedByAssignment[id] = record.bytes
		}
	}
	require.Len(t, acceptedByAssignment, len(assignments), "the mirror serves every assignment result")
	accepted := make([]string, 0, len(acceptedByAssignment))
	for _, bytes := range acceptedByAssignment {
		accepted = append(accepted, bytes)
	}
	return accepted
}

func TestPublicationRealStack_SummaryNamesTheRunBundleTheMirrorServes(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-bundle-summary", 3)
	executeCampaign(t, env, "pub-bundle-summary")
	runID := onlyRunID(t, run)
	assignments := runAssignments(t, run, runID)

	// (a) The completion summary names a bundle by a 64-hex digest.
	digest := latestSummaryBundleDigest(t)
	require.Regexp(t, `^[0-9a-f]{64}$`, digest, "the summary names its assignment bundle")

	// (b) The mirror serves the bundle under that digest, and the bytes hash to it.
	status, body := mirrorGet(t, "/proofs/"+digest)
	require.Equal(t, http.StatusOK, status, string(body))
	sum := sha256.Sum256(body)
	assert.Equal(t, digest, hex.EncodeToString(sum[:]), "the served bundle hashes to the summary digest")

	// (c) One line per published result, in ascending sequence, each carrying the
	// mirror's exact record bytes and their hash.
	lines := decodeRunBundle(t, body)
	require.Len(t, lines, len(assignments), "one bundle line per assignment result")
	expectedBytes := acceptedResultBytes(t, mirrorStreamRecords(t, mirrorHighWater(t)), runID, assignments)
	lastSequence := int64(-1)
	gotBytes := make([]string, 0, len(lines))
	for _, line := range lines {
		assert.Greater(t, line.Sequence, lastSequence, "bundle lines ascend by sequence")
		lastSequence = line.Sequence
		sum := sha256.Sum256([]byte(line.RecordBytes))
		assert.Equal(t, hex.EncodeToString(sum[:]), line.RecordHash, "record_hash is the SHA-256 of record_bytes")
		gotBytes = append(gotBytes, line.RecordBytes)
	}
	assert.ElementsMatch(t, expectedBytes, gotBytes, "each line carries the exact bytes of one accepted result record")

	// (d) The active catalog lists exactly one bundle entry, under the summary digest.
	assertSingleActiveBundle(t, runID, digest)
}

func TestPublicationRealStack_RepeatPublishKeepsTheSameBundleAndForcedRepublishReplacesIt(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-bundle-repeat", 3)
	executeCampaign(t, env, "pub-bundle-repeat")
	runID := onlyRunID(t, run)
	first := latestSummaryBundleDigest(t)
	require.Regexp(t, `^[0-9a-f]{64}$`, first)
	assertSingleActiveBundle(t, runID, first)

	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	assert.Equal(t, first, latestSummaryBundleDigest(t), "a repeat publish keeps the summary digest")
	assertSingleActiveBundle(t, runID, first)

	audit := runAuditProofIDs(t, runID)
	require.NotEmpty(t, audit, "the run has active audit database and agent trace entries")

	out, err = env.run(t, "runs", "publish", runID, "--force")
	require.NoError(t, err, out)
	assert.Contains(t, out, "forced republish")
	second := latestSummaryBundleDigest(t)
	assertSingleActiveBundle(t, runID, second)

	active := mirrorProofCatalogEntries(t)
	activeIDs := map[string]bool{}
	for _, entry := range active {
		activeIDs[entry.ArtifactID] = true
	}
	for id, filename := range audit {
		assert.True(t, activeIDs[id], "the forced republish keeps the audit artifact active: %s", filename)
	}

	if second == first {
		// Identical records produce identical bundle bytes, so the forced republish
		// reuses the active bundle and withdraws nothing.
		t.Log("forced republish reproduced identical bundle bytes; asserted the bundle is unchanged")
		return
	}
	assert.False(t, activeIDs[first], "the previous bundle is withdrawn from the catalog")
}

func TestPublicationRealStack_SummaryBundleDigestFollowsAVerificationThatChangesNoRecords(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-bundle-verify", 3)
	executeCampaign(t, env, "pub-bundle-verify")
	runID := onlyRunID(t, run)
	published := latestSummaryBundleDigest(t)
	require.Regexp(t, `^[0-9a-f]{64}$`, published)
	assertSingleActiveBundle(t, runID, published)

	out, err := verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	assert.Equal(t, "passed", latestSummary(t).VerifierState, "the verification published its summary revision")
	verified := latestSummaryBundleDigest(t)
	assert.Equal(t, published, verified, "a verification that changes no result records keeps the bundle digest")
	assertSingleActiveBundle(t, runID, verified)
}
