// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package gateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func runBundleTestRecord(sequence int64, body string) models.PublicFeedRecord {
	sum := sha256.Sum256([]byte(body))
	return models.PublicFeedRecord{
		Sequence:    sequence,
		RecordType:  models.PublicFeedRecordTypeProjection,
		RecordHash:  hex.EncodeToString(sum[:]),
		RecordBytes: body,
	}
}

func gunzipRunBundle(t *testing.T, content []byte) (*gzip.Reader, []models.PublicRunBundleLine) {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(content))
	require.NoError(t, err)
	var lines []models.PublicRunBundleLine
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		var line models.PublicRunBundleLine
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &line))
		lines = append(lines, line)
	}
	require.NoError(t, scanner.Err())
	return reader, lines
}

func TestRunBundle_BuildIsDeterministicWithZeroGzipHeader(t *testing.T) {
	records := []models.PublicFeedRecord{
		runBundleTestRecord(3, `{"assignment_id":"c"}`),
		runBundleTestRecord(1, `{"assignment_id":"a"}`),
	}

	first, err := buildRunBundle(records)
	require.NoError(t, err)
	second, err := buildRunBundle(records)
	require.NoError(t, err)

	assert.Equal(t, first.content, second.content)
	assert.Equal(t, sha256.Sum256(first.content), sha256.Sum256(second.content))
	assert.Equal(t, 2, first.recordCount)

	reader, _ := gunzipRunBundle(t, first.content)
	assert.True(t, reader.ModTime.IsZero(), "gzip modification time must be zero")
	assert.Empty(t, reader.Name)
	assert.Empty(t, reader.Comment)
}

func TestRunBundle_LinesCarryRecordBytesUnchangedInSequenceOrder(t *testing.T) {
	bodyB := `{"assignment_id":"b","note":"<quoted> \"value\""}`
	bodyA := `{"assignment_id":"a"}`
	records := []models.PublicFeedRecord{
		runBundleTestRecord(7, bodyB),
		runBundleTestRecord(2, bodyA),
	}

	bundle, err := buildRunBundle(records)
	require.NoError(t, err)
	_, lines := gunzipRunBundle(t, bundle.content)

	require.Len(t, lines, 2)
	assert.Equal(t, int64(2), lines[0].Sequence)
	assert.Equal(t, int64(7), lines[1].Sequence)
	assert.Equal(t, bodyA, lines[0].RecordBytes)
	assert.Equal(t, bodyB, lines[1].RecordBytes)
	for index, line := range lines {
		sum := sha256.Sum256([]byte(line.RecordBytes))
		assert.Equal(t, hex.EncodeToString(sum[:]), line.RecordHash, "line %d", index)
	}
}

func TestRunBundle_RejectsRecordWhoseHashDoesNotMatchItsBytes(t *testing.T) {
	record := runBundleTestRecord(1, `{"assignment_id":"a"}`)
	record.RecordBytes = `{"assignment_id":"tampered"}`

	_, err := buildRunBundle([]models.PublicFeedRecord{record})

	require.ErrorIs(t, err, constants.ErrPublicFeedRecordHashMismatch)
}

func TestRunBundle_EmptyRecordsFailClosed(t *testing.T) {
	bundle, err := buildRunBundle(nil)

	require.ErrorIs(t, err, constants.ErrPublicRunBundleEmpty)
	assert.Empty(t, bundle.content)
}

func TestRunBundle_FilenameIsOnePerRun(t *testing.T) {
	assert.Equal(t, "eval-run-1.assignments.jsonl.gz", runBundleFilename("eval-run-1"))
}
