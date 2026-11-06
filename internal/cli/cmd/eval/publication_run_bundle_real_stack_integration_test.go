// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

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
// catalog. Byte-for-byte equality of each result record with the accepted
// snapshot rows is covered by TestRunBundle_RecordBytesMatchAcceptedSnapshotRows
// in internal/services/gateway. Here /history supplies each result's sequence
// and JSON value: it re-encodes items, so it cannot give exact bytes.

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
func mirrorProofCatalogEntries(t *testing.T, run *realStackRun) []models.PublicProofCatalogEntry {
	t.Helper()
	return publicationLayoutArtifacts(t, run)
}

// runBundleEntries returns the active catalog entries that are runID's assignment bundle.
func runBundleEntries(t *testing.T, run *realStackRun, runID string) []models.PublicProofCatalogEntry {
	t.Helper()
	filename := runID + constants.PublicRunBundleFilenameSuffix
	var entries []models.PublicProofCatalogEntry
	for _, entry := range mirrorProofCatalogEntries(t, run) {
		if entry.Filename == filename {
			entries = append(entries, entry)
		}
	}
	return entries
}

// assertSingleActiveBundle requires the catalog to list exactly one bundle for
// the run, with the given digest as its artifact id.
func assertSingleActiveBundle(t *testing.T, run *realStackRun, runID, digest string) {
	t.Helper()
	entries := runBundleEntries(t, run, runID)
	require.Len(t, entries, 1, "exactly one active assignment bundle for the run")
	assert.Equal(t, digest, entries[0].ArtifactID, "the active bundle is the one the summary names")
	assert.Equal(t, constants.MediaTypeGzip, entries[0].MediaType)
}

// nonBundleProofIDs maps every active catalog artifact id that is not a run
// bundle, such as the campaign's scenario fixtures, to its filename.
func nonBundleProofIDs(t *testing.T, run *realStackRun) map[string]string {
	t.Helper()
	ids := map[string]string{}
	for _, entry := range mirrorProofCatalogEntries(t, run) {
		if !strings.HasSuffix(entry.Filename, constants.PublicRunBundleFilenameSuffix) {
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

// acceptedResultProjections returns, for each assignment ID, the history
// projection of the result revision the mirror holds last: the snapshot keeps the
// latest accepted row per assignment, so a verified or verification-failed
// revision supersedes the completion revision. /history keeps superseded
// revisions too, so the highest sequence per assignment wins.
func acceptedResultProjections(t *testing.T, projections []publicationProjection, runID string, assignments []*evalv1.EvaluationAssignment) map[string]publicationProjection {
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
	acceptedByAssignment := map[string]publicationProjection{}
	for _, projection := range projections {
		id, ok := owner[projection.key]
		if !ok {
			continue
		}
		if current, seen := acceptedByAssignment[id]; seen && current.sequence > projection.sequence {
			continue
		}
		acceptedByAssignment[id] = projection
	}
	require.Len(t, acceptedByAssignment, len(assignments), "the mirror serves every assignment result")
	return acceptedByAssignment
}

// historyRecordValue decodes a history item with the sequence and record_type
// fields /history adds removed, so it can be compared by JSON value with a
// bundle line's record bytes.
func historyRecordValue(t *testing.T, projection publicationProjection) any {
	t.Helper()
	var object map[string]any
	require.NoError(t, json.Unmarshal([]byte(projection.raw), &object))
	delete(object, "sequence")
	delete(object, "record_type")
	return object
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
	status, body := mirrorGet(t, "/runs/"+digest+".jsonl.gz")
	require.Equal(t, http.StatusOK, status, string(body))
	sum := sha256.Sum256(body)
	assert.Equal(t, digest, hex.EncodeToString(sum[:]), "the served bundle hashes to the summary digest")

	// (c) One line per published result, in ascending sequence. Each line's
	// record_hash is the SHA-256 of its record_bytes, its sequence is the latest
	// history sequence of an assignment's result, and its record_bytes has the JSON
	// value of that history item.
	lines := decodeRunBundle(t, body)
	require.Len(t, lines, len(assignments), "one bundle line per assignment result")
	accepted := acceptedResultProjections(t, mirrorProjections(t), runID, assignments)
	bySequence := map[int64]publicationProjection{}
	expectedSequences := make([]int64, 0, len(accepted))
	for _, projection := range accepted {
		bySequence[projection.sequence] = projection
		expectedSequences = append(expectedSequences, projection.sequence)
	}
	lastSequence := int64(-1)
	gotSequences := make([]int64, 0, len(lines))
	for _, line := range lines {
		assert.Greater(t, line.Sequence, lastSequence, "bundle lines ascend by sequence")
		lastSequence = line.Sequence
		sum := sha256.Sum256([]byte(line.RecordBytes))
		assert.Equal(t, hex.EncodeToString(sum[:]), line.RecordHash, "record_hash is the SHA-256 of record_bytes")
		gotSequences = append(gotSequences, line.Sequence)
		projection, ok := bySequence[line.Sequence]
		require.True(t, ok, "bundle line sequence %d is the latest history sequence of an assignment result", line.Sequence)
		var recordValue any
		require.NoError(t, json.Unmarshal([]byte(line.RecordBytes), &recordValue))
		require.Equal(t, historyRecordValue(t, projection), recordValue, "record_bytes has the JSON value of history sequence %d", line.Sequence)
	}
	assert.ElementsMatch(t, expectedSequences, gotSequences, "each assignment's latest result revision is bundled once")

	// (d) The active catalog lists exactly one bundle entry, under the summary digest.
	assertSingleActiveBundle(t, run, runID, digest)
}

func TestPublicationRealStack_RepeatPublishKeepsTheSameBundleAndForcedRepublishReplacesIt(t *testing.T) {
	env, run := newPublicationEnv(t, severityReplies(3))
	createPublicationCampaign(t, run, "pub-bundle-repeat", 3)
	executeCampaign(t, env, "pub-bundle-repeat")
	runID := onlyRunID(t, run)
	first := latestSummaryBundleDigest(t)
	require.Regexp(t, `^[0-9a-f]{64}$`, first)
	assertSingleActiveBundle(t, run, runID, first)

	out, err := env.run(t, "runs", "publish", runID)
	require.NoError(t, err, out)
	assert.Equal(t, first, latestSummaryBundleDigest(t), "a repeat publish keeps the summary digest")
	assertSingleActiveBundle(t, run, runID, first)

	retained := nonBundleProofIDs(t, run)
	require.NotEmpty(t, retained, "the campaign has active scenario fixture entries")

	out, err = env.run(t, "runs", "publish", runID, "--force")
	require.NoError(t, err, out)
	assert.Contains(t, out, "forced republish")
	second := latestSummaryBundleDigest(t)
	assertSingleActiveBundle(t, run, runID, second)

	active := mirrorProofCatalogEntries(t, run)
	activeIDs := map[string]bool{}
	for _, entry := range active {
		activeIDs[entry.ArtifactID] = true
	}
	for id, filename := range retained {
		assert.True(t, activeIDs[id], "the forced republish keeps the non-bundle artifact active: %s", filename)
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
	assertSingleActiveBundle(t, run, runID, published)

	out, err := verifyCLI(t, env, runID)
	require.NoError(t, err, out)
	assert.Equal(t, "passed", latestSummary(t).VerifierState, "the verification published its summary revision")
	verified := latestSummaryBundleDigest(t)
	assert.Equal(t, published, verified, "a verification that changes no result records keeps the bundle digest")
	assertSingleActiveBundle(t, run, runID, verified)
}
