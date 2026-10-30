// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// PublicModelRoleInvocationSignal is the disclosure-safe input for projecting a
// model-role invocation into a public live event. Provider-boundary fields from
// observe payloads (served_model_tag, backend_name, exact quantization) are
// excluded by design.
type PublicModelRoleInvocationSignal struct {
	Release      CampaignRelease
	RunID        string
	AssignmentID string
	VariantID    string
	Role         models.ModelRole
	TaskID       string
	ObservedAt   string
	EventID      string
	Completed    int
	Total        int
	MetricDelta  PublicMetricDelta
}

// PublicMetricDelta is the explorer live-event metric_delta object.
// Keys are metric ids (pass_rate, input_tokens, latency_ms, tokens_per_second, retries).
// Values are the disclosure-safe evalv1.PublicLiveMetricValue shape.
type PublicMetricDelta map[string]*evalv1.PublicLiveMetricValue

// PublicMetricAvailabilitySignal is the disclosure-safe input for projecting
// per-assignment metric availability into a public metric_updated event. Role
// is the assignment's designated role: the metric grades that role.
type PublicMetricAvailabilitySignal struct {
	Release      CampaignRelease
	RunID        string
	AssignmentID string
	VariantID    string
	Role         models.ModelRole
	MetricID     string
	Numerator    int
	Denominator  int
	Rate         *float64
	ObservedAt   string
	EventID      string
	Completed    int
	Total        int
}

// ProjectModelRoleInvocationEvent maps a disclosure-safe invocation signal to
// a stage_updated live event body for record_type "event" publication.
func ProjectModelRoleInvocationEvent(signal PublicModelRoleInvocationSignal) (*evalv1.PublicLiveEvent, error) {
	if signal.RunID == "" || signal.AssignmentID == "" || signal.VariantID == "" || signal.EventID == "" || signal.ObservedAt == "" {
		return nil, fmt.Errorf("evaluation: project model role invocation event: %w", constants.ErrMissingRequiredField)
	}
	if signal.Role == "" {
		return nil, fmt.Errorf("evaluation: project model role invocation event: role: %w", constants.ErrMissingRequiredField)
	}
	if signal.Completed < 0 || signal.Total < 0 || signal.Completed > signal.Total {
		return nil, fmt.Errorf("evaluation: project model role invocation event: %w", constants.ErrEvaluationLiveEventProgressInvalid)
	}
	stageParts := []string{"model role invoked", string(signal.Role), signal.VariantID}
	if signal.TaskID != "" {
		stageParts = append(stageParts, signal.TaskID)
	}
	return &evalv1.PublicLiveEvent{
		SchemaVersion:       constants.ExplorerViewSchemaVersion,
		Kind:                "stage_updated",
		DatasetId:           CampaignDatasetID(signal.RunID),
		QualityState:        "live_in_progress",
		ObservedAt:          signal.ObservedAt,
		SourceRevisionLabel: campaignSourceRevision,
		Release:             signal.Release.Release,
		ReleaseBasis:        signal.Release.publicBasis(),
		SourceRevision:      signal.Release.SourceRevision,
		EventId:             signal.EventID,
		RunId:               signal.RunID,
		AssignmentId:        signal.AssignmentID,
		VariantId:           signal.VariantID,
		Role:                string(signal.Role),
		LifecycleStatus:     "running",
		Completed:           proto.Uint32(uint32(signal.Completed)),
		Total:               proto.Uint32(uint32(signal.Total)),
		StageLabel:          strings.Join(stageParts, " · "),
		TaskId:              signal.TaskID,
		MetricDelta:         signal.MetricDelta,
	}, nil
}

// ProjectMetricAvailabilityEvent maps a disclosure-safe metric availability
// signal to a metric_updated live event body for record_type "event"
// publication.
func ProjectMetricAvailabilityEvent(signal PublicMetricAvailabilitySignal) (*evalv1.PublicLiveEvent, error) {
	if signal.RunID == "" || signal.AssignmentID == "" || signal.VariantID == "" || signal.MetricID == "" || signal.EventID == "" || signal.ObservedAt == "" {
		return nil, fmt.Errorf("evaluation: project metric availability event: %w", constants.ErrMissingRequiredField)
	}
	if signal.Role == "" {
		return nil, fmt.Errorf("evaluation: project metric availability event: role: %w", constants.ErrMissingRequiredField)
	}
	if signal.Completed < 0 || signal.Total < 0 || signal.Completed > signal.Total {
		return nil, fmt.Errorf("evaluation: project metric availability event: %w", constants.ErrEvaluationLiveEventProgressInvalid)
	}
	return &evalv1.PublicLiveEvent{
		SchemaVersion:       constants.ExplorerViewSchemaVersion,
		Kind:                "metric_updated",
		DatasetId:           CampaignDatasetID(signal.RunID),
		QualityState:        "live_in_progress",
		ObservedAt:          signal.ObservedAt,
		SourceRevisionLabel: campaignSourceRevision,
		Release:             signal.Release.Release,
		ReleaseBasis:        signal.Release.publicBasis(),
		SourceRevision:      signal.Release.SourceRevision,
		EventId:             signal.EventID,
		RunId:               signal.RunID,
		AssignmentId:        signal.AssignmentID,
		VariantId:           signal.VariantID,
		Role:                string(signal.Role),
		LifecycleStatus:     "running",
		Completed:           proto.Uint32(uint32(signal.Completed)),
		Total:               proto.Uint32(uint32(signal.Total)),
		MetricDelta: PublicMetricDelta{
			signal.MetricID: metricAvailabilityValue(signal.Numerator, signal.Denominator, signal.Rate),
		},
	}, nil
}

// MarshalPublicLiveEvent serializes a projected live event for signed export.
// It is the one encoder of live event bodies: protobuf-owned JSON goes through
// the canonical owner, so a zero progress count or metric value is emitted
// (both fields track presence) instead of being dropped.
func MarshalPublicLiveEvent(event *evalv1.PublicLiveEvent) ([]byte, error) {
	if event == nil {
		return nil, fmt.Errorf("evaluation: marshal public live event: %w", constants.ErrMissingRequiredField)
	}
	body, err := evalv1.MarshalCanonical(event)
	if err != nil {
		return nil, fmt.Errorf("evaluation: marshal public live event: %w", err)
	}
	return body, nil
}

func liveMetricValue(value float64) *evalv1.PublicLiveMetricValue {
	return &evalv1.PublicLiveMetricValue{Value: &value}
}

func metricAvailabilityValue(numerator, denominator int, rate *float64) *evalv1.PublicLiveMetricValue {
	if denominator <= 0 {
		return &evalv1.PublicLiveMetricValue{UnavailableReason: publicUnavailableReasonString(evalv1.PublicUnavailableReason_PUBLIC_UNAVAILABLE_REASON_NO_SCORED_CALLS)}
	}
	if rate != nil {
		return liveMetricValue(*rate)
	}
	return liveMetricValue(float64(numerator) / float64(denominator))
}

// Live judge event kinds.
const (
	liveEventKindJudgeStarted   = "judge_started"
	liveEventKindJudgeCompleted = "judge_completed"
)

// PublicJudgeActivitySignal is one judge inference on an assignment, placed on
// the assignment's row: VariantID and Role are the graded subject's, never
// the judge's.
type PublicJudgeActivitySignal struct {
	Release      CampaignRelease
	RunID        string
	AssignmentID string
	VariantID    string
	Role         models.ModelRole
	TaskID       string
	ObservedAt   string
	EventID      string
	Completed    int
	Total        int
	Activity     JudgeActivity
}

// ProjectJudgeActivityEvent maps one judge activity to a judge_started or
// judge_completed live event. Judge prompts and raw failure text are never
// disclosed: a failure is its closed code.
func ProjectJudgeActivityEvent(signal PublicJudgeActivitySignal) (*evalv1.PublicLiveEvent, error) {
	if signal.RunID == "" || signal.AssignmentID == "" || signal.VariantID == "" || signal.EventID == "" || signal.ObservedAt == "" || signal.Role == "" {
		return nil, fmt.Errorf("evaluation: project judge activity event: %w", constants.ErrMissingRequiredField)
	}
	if signal.Completed < 0 || signal.Total < 0 || signal.Completed > signal.Total {
		return nil, fmt.Errorf("evaluation: project judge activity event: %w", constants.ErrEvaluationLiveEventProgressInvalid)
	}
	if signal.Activity.Judge == nil {
		return nil, fmt.Errorf("evaluation: project judge activity event: judge: %w", constants.ErrMissingRequiredField)
	}
	kind := liveEventKindJudgeStarted
	if signal.Activity.Completed {
		kind = liveEventKindJudgeCompleted
	}
	return &evalv1.PublicLiveEvent{
		SchemaVersion:       constants.ExplorerViewSchemaVersion,
		Kind:                kind,
		DatasetId:           CampaignDatasetID(signal.RunID),
		QualityState:        "live_in_progress",
		ObservedAt:          signal.ObservedAt,
		SourceRevisionLabel: campaignSourceRevision,
		Release:             signal.Release.Release,
		ReleaseBasis:        signal.Release.publicBasis(),
		SourceRevision:      signal.Release.SourceRevision,
		EventId:             signal.EventID,
		RunId:               signal.RunID,
		AssignmentId:        signal.AssignmentID,
		VariantId:           signal.VariantID,
		Role:                string(signal.Role),
		LifecycleStatus:     "running",
		Completed:           proto.Uint32(uint32(signal.Completed)),
		Total:               proto.Uint32(uint32(signal.Total)),
		TaskId:              signal.TaskID,
		Judge:               publicJudgeActivity(signal.Activity),
	}, nil
}

// publicJudgeActivity is the disclosure-safe judge block: the judge's
// identity, the judgment's place and verdict, and the usage and timing of its
// signed inference result.
func publicJudgeActivity(activity JudgeActivity) *evalv1.PublicLiveJudgeActivity {
	score := activity.Score
	block := &evalv1.PublicLiveJudgeActivity{
		JudgeVariantId:    activity.Judge.GetVariantId(),
		ServedModelTag:    activity.Judge.GetServedModelTag(),
		ModelDigest:       activity.Judge.GetModelDigest(),
		Subject:           score.Subject.Proto(),
		Index:             uint32(score.Position.Index),
		Count:             uint32(score.Position.Count),
		RubricVersion:     score.RubricVersion,
		ProviderAttemptId: score.ProviderAttemptID,
		State:             evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_JUDGING,
	}
	if score.Subject == judge.SubjectCriterion {
		block.CriterionId = score.Criterion
	}
	if !activity.Completed {
		return block
	}
	switch {
	case score.Status == judge.StatusFailed:
		block.State, block.Failure = evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_FAILED, score.Failure
	case score.Subject == judge.SubjectCriterion && score.Met:
		block.State = evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_MET
	case score.Subject == judge.SubjectCriterion:
		block.State = evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_NOT_MET
	default:
		block.State, block.Score = evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_SCORED, proto.Uint32(uint32(score.Value))
	}
	if score.Status != judge.StatusFailed {
		block.Rationale = score.Rationale
	}
	block.TransactionId = score.TransactionID
	if executedAt := score.Receipt.GetExecutedAtUnixMs(); executedAt > 0 {
		block.ExecutedAtUnixMs = proto.Int64(executedAt)
	}
	if activity.Elapsed > 0 {
		block.DispatchElapsedNs = proto.Uint64(uint64(activity.Elapsed))
	}
	if result := score.Result; result != nil {
		block.FinishReason = result.GetFinishReason()
		block.UsageReported = result.GetUsageReported()
		if block.UsageReported {
			block.PromptTokens = nonNegativeUint32(proto.Int32(result.GetPromptTokens()))
			block.CompletionTokens = nonNegativeUint32(proto.Int32(result.GetCompletionTokens()))
			block.ThinkingTokens = nonNegativeUint32(result.ThinkingTokens)
			block.CacheTokens = nonNegativeUint32(result.CacheTokens)
		}
		block.LoadDurationNs = nonNegativeUint64(result.LoadDurationNs)
		block.PromptEvalDurationNs = nonNegativeUint64(result.PromptEvalDurationNs)
		block.GenerationDurationNs = nonNegativeUint64(result.GenerationDurationNs)
		block.TotalDurationNs = nonNegativeUint64(result.TotalDurationNs)
		block.TimeToFirstTokenNs = nonNegativeUint64(result.TimeToFirstTokenNs)
	}
	return block
}

// nonNegativeUint32 and nonNegativeUint64 carry a reported provider count
// onto the public block; an absent or negative report stays absent.
func nonNegativeUint32(value *int32) *uint32 {
	if value == nil || *value < 0 {
		return nil
	}
	return proto.Uint32(uint32(*value))
}

func nonNegativeUint64(value *int64) *uint64 {
	if value == nil || *value < 0 {
		return nil
	}
	return proto.Uint64(uint64(*value))
}
