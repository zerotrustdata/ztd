// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	"github.com/zerotrustdata/ztd/internal/services/publicdisclosure"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// ModelRoleInvocationIdempotencyKey returns the publication key for one
// disclosure-safe model-role invocation live event emitted when an assignment
// transitions to running.
func ModelRoleInvocationIdempotencyKey(runID, assignmentID, role string) string {
	return runID + ":" + assignmentID + ":invocation:" + role
}

// ScoredModelRoleInvocationIdempotencyKey returns the publication key for one
// disclosure-safe model-role invocation live event emitted when a scored model
// inference is first observed for the assignment.
func ScoredModelRoleInvocationIdempotencyKey(runID, assignmentID, role string) string {
	return runID + ":" + assignmentID + ":invocation-scored:" + role
}

// MetricAvailabilityIdempotencyKey returns the publication key for one
// per-assignment metric availability live event.
func MetricAvailabilityIdempotencyKey(runID, assignmentID, metricID string) string {
	return runID + ":" + assignmentID + ":metric:" + metricID
}

func (c *CampaignPublicationCoordinator) buildAssignmentLiveEventPublishRequests(
	ctx context.Context,
	assignment *evalv1.EvaluationAssignment,
	result *evalv1.EvaluationAssignmentResult,
) ([]campaignFeedPublishRequest, error) {
	if c == nil || assignment == nil || result == nil {
		return nil, fmt.Errorf("evaluation: build assignment live event publish requests: %w", constants.ErrMissingRequiredField)
	}
	completed, total, err := c.assignmentLiveEventProgress(ctx, assignment.GetRunId())
	if err != nil {
		return nil, err
	}
	observedAt := assignmentLiveEventObservedAt(assignment, result)
	release, err := c.loadRunRelease(ctx, assignment.GetRunId())
	if err != nil {
		return nil, err
	}
	// Grading completes before the assignment's terminal events, so its events
	// are exported first.
	requests, err := c.buildJudgeActivityPublishRequests(ctx, assignment, result, release, completed, total, observedAt)
	if err != nil {
		return nil, err
	}
	deterministic, ok, err := buildDeterministicGradingPublishRequest(assignment, result, release, completed, total, observedAt)
	if err != nil {
		return nil, err
	}
	if ok {
		requests = append(requests, deterministic)
	}
	scored, err := c.buildScoredModelRoleInvocationPublishRequests(assignment, result, observedAt, completed, total, release)
	if err != nil {
		return nil, err
	}
	requests = append(requests, scored...)
	signal, ok, err := buildAssignmentPassMetricSignal(assignment, result, observedAt, completed, total)
	if err != nil {
		return nil, err
	}
	if ok {
		signal.Release = release
		event, err := ProjectMetricAvailabilityEvent(signal)
		if err != nil {
			return nil, err
		}
		body, err := MarshalPublicLiveEvent(event)
		if err != nil {
			return nil, err
		}
		if err := publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, body); err != nil {
			return nil, fmt.Errorf("evaluation: build assignment live event publish requests: validate metric event: %w", err)
		}
		requests = append(requests, campaignFeedPublishRequest{
			IdempotencyKey: MetricAvailabilityIdempotencyKey(signal.RunID, signal.AssignmentID, signal.MetricID),
			RecordType:     models.PublicFeedRecordTypeEvent,
			Body:           body,
		})
	}
	return requests, nil
}

// PublishAssignmentInvocationLiveEvents emits the planned model-role invocation
// stage_updated event when an assignment transitions to running.
func (c *CampaignPublicationCoordinator) PublishAssignmentInvocationLiveEvents(ctx context.Context, assignment *evalv1.EvaluationAssignment) error {
	if c == nil || c.store == nil || c.files == nil || c.exporter == nil || assignment == nil {
		return fmt.Errorf("evaluation: publish assignment invocation live events: %w", constants.ErrMissingRequiredField)
	}
	completed, total, err := c.assignmentLiveEventProgress(ctx, assignment.GetRunId())
	if err != nil {
		return err
	}
	observedAt := assignmentLiveEventObservedAt(assignment, nil)
	release, err := c.loadRunRelease(ctx, assignment.GetRunId())
	if err != nil {
		return err
	}
	requests, err := c.buildPlannedModelRoleInvocationPublishRequests(assignment, observedAt, completed, total, release)
	if err != nil {
		return err
	}
	if len(requests) == 0 {
		return nil
	}
	err = c.exportFeedRecordsOneAtATime(ctx, assignment.GetRunId(), requests)
	return err
}

func (c *CampaignPublicationCoordinator) exportFeedRecordsOneAtATime(ctx context.Context, runID string, requests []campaignFeedPublishRequest) error {
	for _, request := range requests {
		if _, err := c.exportFeedRecords(ctx, runID, []campaignFeedPublishRequest{request}); err != nil {
			return err
		}
	}
	return nil
}

func (c *CampaignPublicationCoordinator) buildPlannedModelRoleInvocationPublishRequests(
	assignment *evalv1.EvaluationAssignment,
	observedAt string,
	completed int,
	total int,
	release CampaignRelease,
) ([]campaignFeedPublishRequest, error) {
	requests := make([]campaignFeedPublishRequest, 0, 1)
	for _, signal := range buildPlannedModelRoleInvocationSignals(assignment, observedAt, completed, total) {
		signal.Release = release
		event, err := ProjectModelRoleInvocationEvent(signal)
		if err != nil {
			return nil, err
		}
		body, err := MarshalPublicLiveEvent(event)
		if err != nil {
			return nil, err
		}
		if err := publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, body); err != nil {
			return nil, fmt.Errorf("evaluation: build planned model role invocation publish requests: validate invocation event: %w", err)
		}
		requests = append(requests, campaignFeedPublishRequest{
			IdempotencyKey: ModelRoleInvocationIdempotencyKey(signal.RunID, signal.AssignmentID, string(signal.Role)),
			RecordType:     models.PublicFeedRecordTypeEvent,
			Body:           body,
		})
	}
	return requests, nil
}

func (c *CampaignPublicationCoordinator) buildScoredModelRoleInvocationPublishRequests(
	assignment *evalv1.EvaluationAssignment,
	result *evalv1.EvaluationAssignmentResult,
	observedAt string,
	completed int,
	total int,
	release CampaignRelease,
) ([]campaignFeedPublishRequest, error) {
	requests := make([]campaignFeedPublishRequest, 0, len(result.GetModelInferences()))
	for _, signal := range buildScoredModelRoleInvocationSignals(assignment, result, observedAt, completed, total) {
		signal.Release = release
		event, err := ProjectModelRoleInvocationEvent(signal)
		if err != nil {
			return nil, err
		}
		body, err := MarshalPublicLiveEvent(event)
		if err != nil {
			return nil, err
		}
		if err := publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, body); err != nil {
			return nil, fmt.Errorf("evaluation: build scored model role invocation publish requests: validate invocation event: %w", err)
		}
		requests = append(requests, campaignFeedPublishRequest{
			IdempotencyKey: ScoredModelRoleInvocationIdempotencyKey(signal.RunID, signal.AssignmentID, string(signal.Role)),
			RecordType:     models.PublicFeedRecordTypeEvent,
			Body:           body,
		})
	}
	return requests, nil
}

// assignmentLiveEventProgress returns the run's settled and scheduled assignment
// counts. A catch-up builds events for every assignment of a run it has already
// loaded, so it answers from that state instead of reading every result again
// for each assignment.
func (c *CampaignPublicationCoordinator) assignmentLiveEventProgress(ctx context.Context, runID string) (completed int, total int, err error) {
	if cached := c.activeAggregate(runID); cached != nil {
		return int(cached.state.Terminal), int(cached.state.Scheduled), nil
	}
	assignments, err := c.store.ListAssignments(ctx, runID)
	if err != nil {
		return 0, 0, err
	}
	results := make(map[string]*evalv1.EvaluationAssignmentResult, len(assignments))
	for _, assignment := range assignments {
		exists, err := c.store.AssignmentResultExists(ctx, runID, assignment.GetAssignmentId())
		if err != nil {
			return 0, 0, err
		}
		if !exists {
			continue
		}
		result, err := c.store.LoadAssignmentResult(ctx, runID, assignment.GetAssignmentId())
		if err != nil {
			return 0, 0, err
		}
		results[assignment.GetAssignmentId()] = result
	}
	state, err := CollectRunAggregateState(assignments, results)
	if err != nil {
		return 0, 0, err
	}
	return int(state.Terminal), int(state.Scheduled), nil
}

func assignmentLiveEventObservedAt(assignment *evalv1.EvaluationAssignment, result *evalv1.EvaluationAssignmentResult) string {
	if result.GetCompletedAt() != nil {
		return result.GetCompletedAt().AsTime().UTC().Format(time.RFC3339Nano)
	}
	if assignment.GetCompletedAt() != nil {
		return assignment.GetCompletedAt().AsTime().UTC().Format(time.RFC3339Nano)
	}
	if assignment.GetStartedAt() != nil {
		return assignment.GetStartedAt().AsTime().UTC().Format(time.RFC3339Nano)
	}
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func buildPlannedModelRoleInvocationSignals(
	assignment *evalv1.EvaluationAssignment,
	observedAt string,
	completed int,
	total int,
) []PublicModelRoleInvocationSignal {
	pairs, err := assignmentVariantRolePairs(assignment)
	if err != nil {
		return nil
	}
	signals := make([]PublicModelRoleInvocationSignal, 0, len(pairs))
	for _, pair := range pairs {
		signals = append(signals, PublicModelRoleInvocationSignal{
			RunID:        assignment.GetRunId(),
			AssignmentID: assignment.GetAssignmentId(),
			VariantID:    pair.variantID,
			Role:         models.ModelRole(pair.role),
			TaskID:       assignment.GetScenarioId(),
			ObservedAt:   observedAt,
			EventID:      ModelRoleInvocationIdempotencyKey(assignment.GetRunId(), assignment.GetAssignmentId(), pair.role) + ":event",
			Completed:    completed,
			Total:        total,
		})
	}
	return signals
}

func buildScoredModelRoleInvocationSignals(
	assignment *evalv1.EvaluationAssignment,
	result *evalv1.EvaluationAssignmentResult,
	observedAt string,
	completed int,
	total int,
) []PublicModelRoleInvocationSignal {
	seen := make(map[string]struct{})
	signals := make([]PublicModelRoleInvocationSignal, 0, len(result.GetModelInferences()))
	for _, record := range reportedModelInferences(result) {
		if record == nil {
			continue
		}
		roleLabel, err := modelCampaignRoleLabel(record.GetModelRole())
		if err != nil {
			continue
		}
		role := models.ModelRole(roleLabel)
		if _, ok := seen[roleLabel]; ok {
			continue
		}
		seen[roleLabel] = struct{}{}
		variantID := ""
		if record.GetModelVariant() != nil {
			variantID = record.GetModelVariant().GetVariantId()
		}
		if variantID == "" {
			variantID, err = variantIDForAssignmentModelRole(assignment, record.GetModelRole())
			if err != nil {
				continue
			}
		}
		signals = append(signals, PublicModelRoleInvocationSignal{
			RunID:        assignment.GetRunId(),
			AssignmentID: assignment.GetAssignmentId(),
			VariantID:    variantID,
			Role:         role,
			TaskID:       assignment.GetScenarioId(),
			ObservedAt:   observedAt,
			EventID:      ScoredModelRoleInvocationIdempotencyKey(assignment.GetRunId(), assignment.GetAssignmentId(), roleLabel) + ":event",
			Completed:    completed,
			Total:        total,
			MetricDelta:  modelInferenceMetricDelta(record),
		})
	}
	return signals
}

func modelInferenceMetricDelta(record *evalv1.ModelInferenceRecord) PublicMetricDelta {
	if record == nil || record.GetUsageAvailability() != evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_REPORTED {
		return nil
	}
	delta := PublicMetricDelta{
		"input_tokens":  liveMetricValue(float64(record.GetPromptTokens())),
		"output_tokens": liveMetricValue(float64(record.GetCompletionTokens())),
	}
	if generationNanos := record.GetGenerationDurationNanos(); generationNanos > 0 {
		delta["latency_ms"] = liveMetricValue(float64(generationNanos) / float64(time.Millisecond))
		if completionTokens := record.GetCompletionTokens(); completionTokens > 0 {
			delta["tokens_per_second"] = liveMetricValue(float64(completionTokens) / (float64(generationNanos) / float64(time.Second)))
		}
	}
	if record.RetryCount != nil {
		delta["retries"] = liveMetricValue(float64(record.GetRetryCount()))
	}
	return delta
}

func reportedModelInferences(result *evalv1.EvaluationAssignmentResult) []*evalv1.ModelInferenceRecord {
	records := make([]*evalv1.ModelInferenceRecord, 0, len(result.GetModelInferences()))
	for _, record := range result.GetModelInferences() {
		if record == nil {
			continue
		}
		if record.GetUsageAvailability() != evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_REPORTED {
			continue
		}
		records = append(records, record)
	}
	return records
}

// assignmentPassMetricID names the binary per-assignment verdict metric (1 pass,
// 0 fail). It is not a rate: run-level `pass_rate` is a different metric.
const assignmentPassMetricID = "pass"

func buildAssignmentPassMetricSignal(
	assignment *evalv1.EvaluationAssignment,
	result *evalv1.EvaluationAssignmentResult,
	observedAt string,
	completed int,
	total int,
) (PublicMetricAvailabilitySignal, bool, error) {
	status := DerivePublicSummaryStatus(result)
	// An unspecified or invalid-evidence verdict is not a model pass or fail, so
	// it emits no binary metric.
	if status == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSPECIFIED ||
		status == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE {
		return PublicMetricAvailabilitySignal{}, false, nil
	}
	variantID, err := designatedVariantIDForAssignment(assignment)
	if err != nil {
		return PublicMetricAvailabilitySignal{}, false, fmt.Errorf("evaluation: build assignment pass metric signal: %w", err)
	}
	role, err := designatedRoleLabelForAssignment(assignment)
	if err != nil {
		return PublicMetricAvailabilitySignal{}, false, fmt.Errorf("evaluation: build assignment pass metric signal: %w", err)
	}
	rate := 0.0
	numerator := 0
	if status == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS {
		rate = 1
		numerator = 1
	}
	return PublicMetricAvailabilitySignal{
		RunID:        assignment.GetRunId(),
		AssignmentID: assignment.GetAssignmentId(),
		VariantID:    variantID,
		Role:         models.ModelRole(role),
		MetricID:     assignmentPassMetricID,
		Numerator:    numerator,
		Denominator:  1,
		Rate:         &rate,
		ObservedAt:   observedAt,
		EventID:      MetricAvailabilityIdempotencyKey(assignment.GetRunId(), assignment.GetAssignmentId(), assignmentPassMetricID) + ":event",
		Completed:    completed,
		Total:        total,
	}, true, nil
}

// gradingActivityPublishTimeout bounds one live grading event export, so a slow
// or unreachable mirror cannot stall grading.
const gradingActivityPublishTimeout = 30 * time.Second

// JudgeStartedIdempotencyKey returns the publication key of the live event
// that a judgment started.
func JudgeStartedIdempotencyKey(runID, assignmentID, judgmentID string) string {
	return runID + ":" + assignmentID + ":judge-started:" + judgmentID
}

// JudgeCompletedIdempotencyKey returns the publication key of a judgment's
// completion, live or rebuilt from the persisted result.
func JudgeCompletedIdempotencyKey(runID, assignmentID, judgmentID string) string {
	return runID + ":" + assignmentID + ":judge:" + judgmentID
}

// DeterministicGradingIdempotencyKey returns the publication key of a completed
// deterministic grading, live or rebuilt from the persisted result. The key names
// the graded content, so a rebuild of the published grades is not published twice
// and an attempt that graded differently supersedes them.
func DeterministicGradingIdempotencyKey(runID, assignmentID string, grades []*evalv1.PublicLiveDeterministicGrade) (string, error) {
	body, err := evalv1.MarshalCanonical(&evalv1.PublicLiveEvent{DeterministicGrades: grades})
	if err != nil {
		return "", fmt.Errorf("evaluation: deterministic grading idempotency key: %w", err)
	}
	digest := sha256.Sum256(body)
	return runID + ":" + assignmentID + ":deterministic-grading:" + hex.EncodeToString(digest[:]), nil
}

// judgmentID names one judgment: its provider attempt, or, for a request that
// was never built, its subject and criterion.
func judgmentID(score judge.Score) string {
	if score.ProviderAttemptID != "" {
		return score.ProviderAttemptID
	}
	return "unsent:" + string(score.Subject) + ":" + score.Criterion
}

// gradingActivityRow is the assignment row grading events attach to: the graded
// subject's variant and role, the run's release, and the run's progress.
type gradingActivityRow struct {
	assignment *evalv1.EvaluationAssignment
	variantID  string
	role       models.ModelRole
	release    CampaignRelease
	completed  int
	total      int
}

func newGradingActivityRow(assignment *evalv1.EvaluationAssignment, release CampaignRelease, completed, total int) (gradingActivityRow, error) {
	variantID, err := designatedVariantIDForAssignment(assignment)
	if err != nil {
		return gradingActivityRow{}, err
	}
	role, err := designatedRoleLabelForAssignment(assignment)
	if err != nil {
		return gradingActivityRow{}, err
	}
	return gradingActivityRow{assignment: assignment, variantID: variantID, role: models.ModelRole(role), release: release, completed: completed, total: total}, nil
}

// publishRequest projects, encodes, and validates one judge event.
func (r gradingActivityRow) publishRequest(activity JudgeActivity, observedAt string) (campaignFeedPublishRequest, error) {
	runID, assignmentID := r.assignment.GetRunId(), r.assignment.GetAssignmentId()
	key := JudgeStartedIdempotencyKey(runID, assignmentID, judgmentID(activity.Score))
	if activity.Completed {
		key = JudgeCompletedIdempotencyKey(runID, assignmentID, judgmentID(activity.Score))
	}
	event, err := ProjectJudgeActivityEvent(PublicJudgeActivitySignal{
		Release:      r.release,
		RunID:        runID,
		AssignmentID: assignmentID,
		VariantID:    r.variantID,
		Role:         r.role,
		TaskID:       r.assignment.GetScenarioId(),
		ObservedAt:   observedAt,
		EventID:      key + ":event",
		Completed:    r.completed,
		Total:        r.total,
		Activity:     activity,
	})
	if err != nil {
		return campaignFeedPublishRequest{}, err
	}
	body, err := MarshalPublicLiveEvent(event)
	if err != nil {
		return campaignFeedPublishRequest{}, err
	}
	if err := publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, body); err != nil {
		return campaignFeedPublishRequest{}, fmt.Errorf("evaluation: validate judge activity event: %w", err)
	}
	return campaignFeedPublishRequest{IdempotencyKey: key, RecordType: models.PublicFeedRecordTypeEvent, Body: body}, nil
}

// deterministicGradingRequest projects, encodes, and validates one completed
// deterministic grading event. An assignment with no grades has no event.
func (r gradingActivityRow) deterministicGradingRequest(grades []*evalv1.DeterministicGrade, observedAt string) (campaignFeedPublishRequest, bool, error) {
	public := publicLiveDeterministicGrades(grades)
	if len(public) == 0 {
		return campaignFeedPublishRequest{}, false, nil
	}
	runID, assignmentID := r.assignment.GetRunId(), r.assignment.GetAssignmentId()
	key, err := DeterministicGradingIdempotencyKey(runID, assignmentID, public)
	if err != nil {
		return campaignFeedPublishRequest{}, false, err
	}
	event, err := ProjectDeterministicGradingEvent(PublicDeterministicGradingSignal{
		Release:      r.release,
		RunID:        runID,
		AssignmentID: assignmentID,
		VariantID:    r.variantID,
		Role:         r.role,
		TaskID:       r.assignment.GetScenarioId(),
		ObservedAt:   observedAt,
		EventID:      key + ":event",
		Completed:    r.completed,
		Total:        r.total,
		Grades:       public,
	})
	if err != nil {
		return campaignFeedPublishRequest{}, false, err
	}
	body, err := MarshalPublicLiveEvent(event)
	if err != nil {
		return campaignFeedPublishRequest{}, false, err
	}
	if err := publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, body); err != nil {
		return campaignFeedPublishRequest{}, false, fmt.Errorf("evaluation: validate deterministic grading event: %w", err)
	}
	return campaignFeedPublishRequest{IdempotencyKey: key, RecordType: models.PublicFeedRecordTypeEvent, Body: body}, true, nil
}

// buildDeterministicGradingPublishRequest rebuilds the assignment's deterministic
// grading event from its persisted result, so the persisted grades are the last
// ones a row states and they reach assignment-<id>.db.
func buildDeterministicGradingPublishRequest(assignment *evalv1.EvaluationAssignment, result *evalv1.EvaluationAssignmentResult, release CampaignRelease, completed, total int, observedAt string) (campaignFeedPublishRequest, bool, error) {
	if len(result.GetDeterministicGrades()) == 0 {
		return campaignFeedPublishRequest{}, false, nil
	}
	row, err := newGradingActivityRow(assignment, release, completed, total)
	if err != nil {
		return campaignFeedPublishRequest{}, false, err
	}
	return row.deterministicGradingRequest(result.GetDeterministicGrades(), observedAt)
}

// GradingActivityPublisher publishes one assignment's grading activity as it
// happens. A publish failure never reaches grading: the first one is kept and
// reported with the assignment's terminal result.
type GradingActivityPublisher struct {
	publication *CampaignPublicationCoordinator
	row         gradingActivityRow
	now         func() time.Time
	err         error
}

// NewGradingActivityPublisher prepares the live grading events of one running
// assignment: its row identity, release, and run progress are read once.
func (c *CampaignPublicationCoordinator) NewGradingActivityPublisher(ctx context.Context, assignment *evalv1.EvaluationAssignment, now func() time.Time) (*GradingActivityPublisher, error) {
	if c == nil || c.store == nil || c.exporter == nil || assignment == nil || now == nil {
		return nil, fmt.Errorf("evaluation: judge activity publisher: %w", constants.ErrMissingRequiredField)
	}
	completed, total, err := c.assignmentLiveEventProgress(ctx, assignment.GetRunId())
	if err != nil {
		return nil, err
	}
	release, err := c.loadRunRelease(ctx, assignment.GetRunId())
	if err != nil {
		return nil, err
	}
	row, err := newGradingActivityRow(assignment, release, completed, total)
	if err != nil {
		return nil, err
	}
	return &GradingActivityPublisher{publication: c, row: row, now: now}, nil
}

// JudgeActivity exports one judge event under its own bounded deadline. After
// the first failure it publishes nothing more for the assignment, so an
// unreachable mirror costs judging one timeout; the terminal publication
// rebuilds every completed judgment from the persisted result.
func (p *GradingActivityPublisher) JudgeActivity(ctx context.Context, activity JudgeActivity) {
	if p.err != nil {
		return
	}
	request, err := p.row.publishRequest(activity, p.now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		err = p.export(ctx, request)
	}
	if err != nil {
		p.err = fmt.Errorf("%w: assignment %s judge activity: %w", constants.ErrEvaluationPublicationFailed, p.row.assignment.GetAssignmentId(), err)
	}
}

// DeterministicGrading exports the attempt's completed deterministic grading
// under the same budget as judge activity; the terminal publication rebuilds it
// from the persisted result.
func (p *GradingActivityPublisher) DeterministicGrading(ctx context.Context, grades []*evalv1.DeterministicGrade) {
	if p.err != nil {
		return
	}
	request, ok, err := p.row.deterministicGradingRequest(grades, p.now().UTC().Format(time.RFC3339Nano))
	if err == nil && ok {
		err = p.export(ctx, request)
	}
	if err != nil {
		p.err = fmt.Errorf("%w: assignment %s deterministic grading: %w", constants.ErrEvaluationPublicationFailed, p.row.assignment.GetAssignmentId(), err)
	}
}

// export sends one grading event under its own bounded deadline.
func (p *GradingActivityPublisher) export(ctx context.Context, request campaignFeedPublishRequest) error {
	publishCtx, cancel := context.WithTimeout(assignmentPersistContext(ctx), gradingActivityPublishTimeout)
	defer cancel()
	_, err := p.publication.exportFeedRecords(publishCtx, p.row.assignment.GetRunId(), []campaignFeedPublishRequest{request})
	return err
}

// Err is the first publish failure, or nil.
func (p *GradingActivityPublisher) Err() error {
	return p.err
}

// buildJudgeActivityPublishRequests rebuilds one judge_completed event per
// persisted grader call, deriving each verdict from the call's signed result.
// The keys equal the live keys, so a judgment published live is never
// published twice. A judgment that never returned a verified receipt left no
// grader call and is only ever a live event.
func (c *CampaignPublicationCoordinator) buildJudgeActivityPublishRequests(ctx context.Context, assignment *evalv1.EvaluationAssignment, result *evalv1.EvaluationAssignmentResult, release CampaignRelease, completed, total int, fallbackObservedAt string) ([]campaignFeedPublishRequest, error) {
	if len(result.GetGraderCalls()) == 0 {
		return nil, nil
	}
	hasRebuildable := false
	for _, call := range result.GetGraderCalls() {
		if isRebuildableGraderCall(call) {
			hasRebuildable = true
			break
		}
	}
	if !hasRebuildable {
		return nil, nil
	}
	row, err := newGradingActivityRow(assignment, release, completed, total)
	if err != nil {
		return nil, err
	}
	spec, err := c.store.LoadCampaignSpec(ctx, assignment.GetCampaignId())
	if err != nil {
		return nil, err
	}
	cfg, err := campaignJudgeConfig(AssignmentExecutionRequest{Assignment: row.assignment, Binding: CampaignExecutionBinding{JudgeVariant: spec.GetJudgeVariant()}}, nil)
	if err != nil {
		return nil, err
	}
	positions := judgedGradePositions(result)
	requests := make([]campaignFeedPublishRequest, 0, len(result.GetGraderCalls()))
	for _, call := range result.GetGraderCalls() {
		if !isRebuildableGraderCall(call) {
			continue
		}
		if call.GetJudgeVariantId() != cfg.Variant.GetVariantId() {
			return nil, fmt.Errorf("evaluation: rebuild judge activity: grader call %s: %w", call.GetGraderCallId(), constants.ErrEvaluationCampaignJudgeInvalid)
		}
		score := recordedJudgeScore(cfg, call, positions[call.GetCriterionId()])
		observedAt := fallbackObservedAt
		if executedAt := call.GetReceipt().GetExecutedAtUnixMs(); executedAt > 0 {
			observedAt = time.UnixMilli(executedAt).UTC().Format(time.RFC3339Nano)
		}
		request, err := row.publishRequest(JudgeActivity{Judge: cfg.Variant, Score: score, Completed: true}, observedAt)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, nil
}

func isRebuildableGraderCall(call *evalv1.GraderModelCallRecord) bool {
	return call != nil &&
		call.GetRubricVersion() != "" &&
		call.GetInferenceResult() != nil &&
		call.GetReceipt() != nil
}

// recordedJudgeScore is the score one persisted grader call records, read
// from its signed result exactly as the live judge resolved it.
func recordedJudgeScore(cfg judge.Config, call *evalv1.GraderModelCallRecord, position judge.Position) judge.Score {
	score := judge.Score{
		Subject:           judge.SubjectCriterion,
		Position:          position,
		Status:            judge.StatusScored,
		JudgeModelTag:     cfg.Variant.GetServedModelTag(),
		JudgeModelDigest:  cfg.Variant.GetModelDigest(),
		ProviderAttemptID: call.GetProviderAttemptId(),
		TransactionID:     call.GetTransactionId(),
		Receipt:           call.GetReceipt(),
		Result:            call.GetInferenceResult(),
		RubricVersion:     call.GetRubricVersion(),
	}
	var err error
	if criterion, ok := strings.CutPrefix(call.GetCriterionId(), contentCriterionPrefix); ok {
		score.Criterion = criterion
		score.Met, score.Rationale, err = judge.VerifyCriterionResult(cfg, call.GetProviderAttemptId(), call.GetInferenceResult(), call.GetReceipt())
	} else {
		score.Subject = judge.SubjectFinalReply
		if call.GetCriterionId() == memoryQualityCriterionID {
			score.Subject = judge.SubjectMemory
		}
		score.Value, score.Rationale, err = judge.VerifyQualityResult(cfg, call.GetProviderAttemptId(), call.GetInferenceResult(), call.GetReceipt())
	}
	if err != nil {
		score.Status, score.Failure, score.Detail = judge.StatusFailed, judge.ResultFailure(err), err.Error()
	}
	return score
}

// judgedGradePositions places each judged semantic grade among the grades of
// its phase, the judged criteria or the quality subjects, in result order:
// the order the live judge dispatched them.
func judgedGradePositions(result *evalv1.EvaluationAssignmentResult) map[string]judge.Position {
	var criteria, quality []string
	for _, grade := range result.GetSemanticGrades() {
		switch id := grade.GetCriterionId(); {
		case strings.HasPrefix(id, contentCriterionPrefix):
			criteria = append(criteria, id)
		case id == diagnosisQualityCriterionID || id == memoryQualityCriterionID:
			quality = append(quality, id)
		}
	}
	positions := make(map[string]judge.Position, len(criteria)+len(quality))
	for _, phase := range [][]string{criteria, quality} {
		for i, id := range phase {
			positions[id] = judge.Position{Index: i + 1, Count: len(phase)}
		}
	}
	return positions
}
