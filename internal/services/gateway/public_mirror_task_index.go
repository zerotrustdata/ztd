// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

package gateway

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// publicAssignmentResultEnvelope decodes the fields of an assignment result
// projection envelope that the task index renders. Unknown fields are ignored.
type publicAssignmentResultEnvelope struct {
	Record publicAssignmentResultWire `json:"record"`
}

// publicAssignmentResultWire uses the envelope's own field names.
type publicAssignmentResultWire struct {
	RunID              string `json:"run_id"`
	AssignmentID       string `json:"assignment_id"`
	ScenarioID         string `json:"scenario_id"`
	LifecycleStatus    string `json:"lifecycle_status"`
	SummaryStatus      string `json:"summary_status"`
	VerificationStatus string `json:"verification_status"`
	CompletedAt        string `json:"completed_at"`
}

// publicTaskCandidate is one assignment result awaiting the per-task bound.
type publicTaskCandidate struct {
	sequence    int64
	completedAt time.Time
	result      models.PublicTaskResult
}

// isPublicAssignmentResultRecord reports whether a snapshot record is an
// assignment result projection. The decision uses the snapshot identity parser
// that also keys the snapshot rows.
func isPublicAssignmentResultRecord(record models.PublicFeedRecord) (bool, error) {
	_, identity, _, err := parseSnapshotRecordIdentity(record)
	if err != nil {
		return false, err
	}
	return identity.Kind == publicSnapshotAssignmentResultKind, nil
}

// buildPublicTaskIndex groups the assignment result records among records by
// task. The task ID is the scenario ID without its @version suffix. Each task
// keeps its constants.PublicTaskIndexResultLimit newest results by completed_at,
// ties broken by higher sequence. Entries are sorted by task ID. The result is
// never nil.
func buildPublicTaskIndex(records []models.PublicFeedRecord) ([]models.PublicTaskIndexEntry, error) {
	candidatesByTask := make(map[string][]publicTaskCandidate)
	for _, record := range records {
		isResult, err := isPublicAssignmentResultRecord(record)
		if err != nil {
			return nil, fmt.Errorf("mirror task index: identify record %d: %w", record.Sequence, err)
		}
		if !isResult {
			continue
		}
		var envelope publicAssignmentResultEnvelope
		if err := json.Unmarshal([]byte(record.RecordBytes), &envelope); err != nil {
			return nil, fmt.Errorf("mirror task index: decode assignment result %d: %w", record.Sequence, err)
		}
		wire := envelope.Record
		taskID, _, _ := strings.Cut(wire.ScenarioID, "@")
		if taskID == "" {
			return nil, fmt.Errorf("mirror task index: assignment result %d scenario_id: %w", record.Sequence, constants.ErrMissingRequiredField)
		}
		completedAt, err := time.Parse(time.RFC3339Nano, wire.CompletedAt)
		if err != nil {
			return nil, fmt.Errorf("mirror task index: assignment result %d completed_at: %w", record.Sequence, err)
		}
		candidatesByTask[taskID] = append(candidatesByTask[taskID], publicTaskCandidate{
			sequence:    record.Sequence,
			completedAt: completedAt,
			result: models.PublicTaskResult{
				RunID:              wire.RunID,
				AssignmentID:       wire.AssignmentID,
				LifecycleStatus:    wire.LifecycleStatus,
				SummaryStatus:      wire.SummaryStatus,
				VerificationStatus: wire.VerificationStatus,
				CompletedAt:        wire.CompletedAt,
			},
		})
	}

	entries := make([]models.PublicTaskIndexEntry, 0, len(candidatesByTask))
	for taskID, candidates := range candidatesByTask {
		sort.Slice(candidates, func(i, j int) bool {
			if !candidates[i].completedAt.Equal(candidates[j].completedAt) {
				return candidates[i].completedAt.After(candidates[j].completedAt)
			}
			return candidates[i].sequence > candidates[j].sequence
		})
		if len(candidates) > constants.PublicTaskIndexResultLimit {
			candidates = candidates[:constants.PublicTaskIndexResultLimit]
		}
		results := make([]models.PublicTaskResult, len(candidates))
		for i := range candidates {
			results[i] = candidates[i].result
		}
		entries = append(entries, models.PublicTaskIndexEntry{TaskID: taskID, Results: results})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].TaskID < entries[j].TaskID
	})
	return entries, nil
}
