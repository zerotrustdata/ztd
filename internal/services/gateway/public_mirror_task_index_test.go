// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// taskIndexTestEnvelope is the assignment result envelope shape written to
// snapshot records.
type taskIndexTestEnvelope struct {
	SchemaVersion  string                     `json:"schema_version"`
	MessageType    string                     `json:"message_type"`
	IdempotencyKey string                     `json:"idempotency_key"`
	Record         publicAssignmentResultWire `json:"record"`
}

func taskIndexTestResultRecord(t *testing.T, sequence int64, assignmentID, scenarioID, completedAt string) models.PublicFeedRecord {
	t.Helper()
	body, err := json.Marshal(taskIndexTestEnvelope{
		SchemaVersion:  "1.0.0",
		MessageType:    publicSnapshotAssignmentResultKind,
		IdempotencyKey: "run-1:" + assignmentID,
		Record: publicAssignmentResultWire{
			RunID:           "run-1",
			AssignmentID:    assignmentID,
			ScenarioID:      scenarioID,
			LifecycleStatus: "EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED",
			SummaryStatus:   "EVALUATION_VERDICT_STATUS_PASS",
			CompletedAt:     completedAt,
		},
	})
	require.NoError(t, err)
	return taskIndexTestRawRecord(sequence, body)
}

// taskIndexTestRawRecord wraps a projection body with the hash the batch
// builder records for it.
func taskIndexTestRawRecord(sequence int64, body []byte) models.PublicFeedRecord {
	sum := sha256.Sum256(body)
	return models.PublicFeedRecord{
		Sequence:    sequence,
		RecordType:  models.PublicFeedRecordTypeProjection,
		RecordHash:  hex.EncodeToString(sum[:]),
		RecordBytes: string(body),
	}
}

func taskIndexTestLifecycleRecord(t *testing.T, sequence int64, assignmentID string) models.PublicFeedRecord {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"message_type": "PublicAssignmentLifecycleRecord",
		"record":       map[string]string{"run_id": "run-1", "assignment_id": assignmentID, "scenario_id": "lifecycle-task"},
	})
	require.NoError(t, err)
	return taskIndexTestRawRecord(sequence, body)
}

func taskIndexTestAssignmentIDs(results []models.PublicTaskResult) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.AssignmentID)
	}
	return ids
}

func TestBuildPublicTaskIndexOrdersResultsNewestFirst(t *testing.T) {
	records := []models.PublicFeedRecord{
		taskIndexTestResultRecord(t, 1, "a-old", "task-alpha@1.0.0", "2026-09-01T10:00:00Z"),
		taskIndexTestResultRecord(t, 2, "a-new", "task-alpha@1.0.0", "2026-09-03T10:00:00Z"),
		taskIndexTestResultRecord(t, 3, "a-mid", "task-alpha@1.0.0", "2026-09-02T10:00:00Z"),
	}

	index, err := buildPublicTaskIndex(records)
	require.NoError(t, err)
	require.Len(t, index, 1)
	assert.Equal(t, "task-alpha", index[0].TaskID)
	assert.Equal(t, []string{"a-new", "a-mid", "a-old"}, taskIndexTestAssignmentIDs(index[0].Results))
	assert.Equal(t, "run-1", index[0].Results[0].RunID)
	assert.Equal(t, "EVALUATION_VERDICT_STATUS_PASS", index[0].Results[0].SummaryStatus)
}

func TestBuildPublicTaskIndexKeepsOnlyLimitNewestResultsPerTask(t *testing.T) {
	total := constants.PublicTaskIndexResultLimit + 2
	records := make([]models.PublicFeedRecord, 0, total)
	for i := 0; i < total; i++ {
		records = append(records, taskIndexTestResultRecord(t, int64(i+1), fmt.Sprintf("a-%02d", i), "task-bound@1.0.0",
			fmt.Sprintf("2026-09-%02dT00:00:00Z", i+1)))
	}

	index, err := buildPublicTaskIndex(records)
	require.NoError(t, err)
	require.Len(t, index, 1)
	require.Len(t, index[0].Results, constants.PublicTaskIndexResultLimit)
	assert.Equal(t, fmt.Sprintf("a-%02d", total-1), index[0].Results[0].AssignmentID)
	assert.Equal(t, fmt.Sprintf("a-%02d", total-constants.PublicTaskIndexResultLimit), index[0].Results[constants.PublicTaskIndexResultLimit-1].AssignmentID)
}

func TestBuildPublicTaskIndexStripsVersionSuffixFromScenarioID(t *testing.T) {
	records := []models.PublicFeedRecord{
		taskIndexTestResultRecord(t, 1, "a-1", "instruction-exact-format@1.0.0", "2026-09-01T00:00:00Z"),
		taskIndexTestResultRecord(t, 2, "a-2", "instruction-exact-format", "2026-09-02T00:00:00Z"),
		taskIndexTestResultRecord(t, 3, "a-3", "instruction-exact-format@2.0.0", "2026-09-03T00:00:00Z"),
	}

	index, err := buildPublicTaskIndex(records)
	require.NoError(t, err)
	require.Len(t, index, 1)
	assert.Equal(t, "instruction-exact-format", index[0].TaskID)
	assert.Equal(t, []string{"a-3", "a-2", "a-1"}, taskIndexTestAssignmentIDs(index[0].Results))
}

func TestBuildPublicTaskIndexBreaksCompletedAtTiesByHigherSequence(t *testing.T) {
	records := []models.PublicFeedRecord{
		taskIndexTestResultRecord(t, 5, "a-low-seq", "task-tie@1.0.0", "2026-09-01T00:00:00Z"),
		taskIndexTestResultRecord(t, 9, "a-high-seq", "task-tie@1.0.0", "2026-09-01T00:00:00Z"),
	}

	index, err := buildPublicTaskIndex(records)
	require.NoError(t, err)
	require.Len(t, index, 1)
	assert.Equal(t, []string{"a-high-seq", "a-low-seq"}, taskIndexTestAssignmentIDs(index[0].Results))
}

func TestBuildPublicTaskIndexSortsEntriesByTaskID(t *testing.T) {
	records := []models.PublicFeedRecord{
		taskIndexTestResultRecord(t, 1, "a-1", "task-zulu@1.0.0", "2026-09-01T00:00:00Z"),
		taskIndexTestResultRecord(t, 2, "a-2", "task-alpha@1.0.0", "2026-09-01T00:00:00Z"),
		taskIndexTestResultRecord(t, 3, "a-3", "task-mike@1.0.0", "2026-09-01T00:00:00Z"),
	}

	index, err := buildPublicTaskIndex(records)
	require.NoError(t, err)
	ids := make([]string, 0, len(index))
	for _, entry := range index {
		ids = append(ids, entry.TaskID)
	}
	assert.Equal(t, []string{"task-alpha", "task-mike", "task-zulu"}, ids)
}

func TestBuildPublicTaskIndexIgnoresNonResultRecordsAndReturnsEmptyNonNil(t *testing.T) {
	index, err := buildPublicTaskIndex(nil)
	require.NoError(t, err)
	require.NotNil(t, index)
	assert.Empty(t, index)

	index, err = buildPublicTaskIndex([]models.PublicFeedRecord{taskIndexTestLifecycleRecord(t, 1, "a-queued")})
	require.NoError(t, err)
	require.NotNil(t, index)
	assert.Empty(t, index)

	body, err := json.Marshal(models.PublicFeedBootstrap{TaskIndex: index})
	require.NoError(t, err)
	assert.Contains(t, string(body), `"task_index":[]`)
}

func TestIsPublicAssignmentResultRecordSeparatesResultsFromLifecycle(t *testing.T) {
	isResult, err := isPublicAssignmentResultRecord(taskIndexTestResultRecord(t, 1, "a-1", "task-alpha", "2026-09-01T00:00:00Z"))
	require.NoError(t, err)
	assert.True(t, isResult)

	isResult, err = isPublicAssignmentResultRecord(taskIndexTestLifecycleRecord(t, 2, "a-2"))
	require.NoError(t, err)
	assert.False(t, isResult)
}
