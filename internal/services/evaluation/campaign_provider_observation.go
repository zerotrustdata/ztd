// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/inference/provider_observer"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ProviderObservationPolicy controls whether missing provider-boundary windows
// are verification failures or interim unavailable telemetry.
type ProviderObservationPolicy int

const (
	// ProviderObservationPolicyInterim records missing windows as unavailable
	// reasons without failing assignment verification.
	ProviderObservationPolicyInterim ProviderObservationPolicy = 0
	// ProviderObservationPolicyStrict requires complete provider-boundary
	// observation coverage for every scored inference.
	ProviderObservationPolicyStrict ProviderObservationPolicy = 1
)

// PublicMetricValue is one disclosure-safe scalar metric for explorer views.
type PublicMetricValue struct {
	Value             *float64 `json:"value,omitempty"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
}

// PublicGradeSummary is one disclosure-safe deterministic grade for explorer views.
type PublicGradeSummary struct {
	CriterionID     string `json:"criterion_id"`
	Status          string `json:"status"`
	ExplanationCode string `json:"explanation_code"`
	Detail          string `json:"detail,omitempty"`
}

// PublicBenchmarkTiming carries assignment-level timing observations.
type PublicBenchmarkTiming struct {
	ModelLoadMS        *PublicMetricValue `json:"model_load_ms,omitempty"`
	TimeToFirstTokenMS *PublicMetricValue `json:"time_to_first_token_ms,omitempty"`
	GenerationMS       *PublicMetricValue `json:"generation_ms,omitempty"`
	WholeTaskMS        *PublicMetricValue `json:"whole_task_ms,omitempty"`
}

// PublicGPUObservation carries provider-boundary GPU and host RAM peaks.
type PublicGPUObservation struct {
	VRAMBeforeBytes    *PublicMetricValue `json:"vram_before_bytes,omitempty"`
	VRAMPeakBytes      *PublicMetricValue `json:"vram_peak_bytes,omitempty"`
	SystemRAMPeakBytes *PublicMetricValue `json:"system_ram_peak_bytes,omitempty"`
	UtilizationPercent *PublicMetricValue `json:"utilization_percent,omitempty"`
	TemperatureCelsius *PublicMetricValue `json:"temperature_celsius,omitempty"`
	PowerWatts         *PublicMetricValue `json:"power_watts,omitempty"`
	ClockMHz           *PublicMetricValue `json:"clock_mhz,omitempty"`
}

// PublicBenchmarkObservations is the explorer-safe benchmark projection for
// one terminal assignment.
type PublicBenchmarkObservations struct {
	GradeSummaries []PublicGradeSummary          `json:"grade_summaries,omitempty"`
	ToolScorecard  map[string]*PublicMetricValue `json:"tool_scorecard,omitempty"`
	// SecurityPrivacyEvents is set only for security-policy scenarios.
	SecurityPrivacyEvents map[string]uint32      `json:"security_privacy_events,omitempty"`
	Timing                *PublicBenchmarkTiming `json:"timing,omitempty"`
	GPU                   *PublicGPUObservation  `json:"gpu,omitempty"`
	UnavailableReasons    []string               `json:"unavailable_reasons"`
}

var toolScorecardDimensions = []string{
	"tool_recognition",
	"tool_selection",
	"argument_schema",
	"argument_semantics",
	"permission_compliance",
	"result_interpretation",
	"follow_up_decision",
	"unnecessary_tool_calls",
	"looping",
	"recovery",
}

var gradeToToolScorecardDimension = map[string]string{
	"tool-selection":     "tool_selection",
	"policy-expectation": "permission_compliance",
	"role-invoked":       "tool_recognition",
	"governed-inference": "follow_up_decision",
}

// CampaignProviderObservationReader loads provider-boundary observation windows
// and governed provider-attempt records for campaign verification and export. It
// reads only the sealed copies in the run's evidence directory; the Gateway is
// never consulted here, and capture belongs to CampaignWitnessCapture.
type CampaignProviderObservationReader struct {
	store *Store
}

// NewCampaignProviderObservationReader constructs one provider observation
// accessor over the evaluation store that owns the run's sealed evidence.
func NewCampaignProviderObservationReader(store *Store) (*CampaignProviderObservationReader, error) {
	if store == nil || store.files == nil {
		return nil, fmt.Errorf("evaluation: provider observation reader: %w", constants.ErrMissingRequiredField)
	}
	return &CampaignProviderObservationReader{store: store}, nil
}

func isProviderEvidenceNotFound(err error) bool {
	return isGatewayEvidenceNotFound(err)
}

// SaveProviderObservationWindow seals one provider observation window in the
// run's evidence directory and records its reference under the attempt.
func (s *Store) SaveProviderObservationWindow(ctx context.Context, runID string, window *evalv1.ProviderBoundaryObservationWindow) error {
	if window == nil || !complianceevidence.ValidPathElement(runID) || !complianceevidence.ValidPathElement(window.GetProviderAttemptId()) {
		return fmt.Errorf("%w: run ID, provider attempt ID, and observation window are required", constants.ErrEvaluationReportPersistFailed)
	}
	attemptID := window.GetProviderAttemptId()
	reference, err := s.SaveProtoArtifact(ctx, EvidenceScope{RunID: runID, AttemptID: attemptID}, complianceevidence.ArtifactTypeProviderObservationWindow, window)
	if err != nil {
		return err
	}
	return s.writeEvidenceRef(ctx, s.layout.providerWindowRefPath(runID, attemptID), reference)
}

// SaveProviderAttemptRecord seals one governed provider-attempt record in the
// run's evidence directory and records its reference under the attempt.
func (s *Store) SaveProviderAttemptRecord(ctx context.Context, runID string, attempt *operatorv1.InferenceProviderAttemptRecord) error {
	if attempt == nil || !complianceevidence.ValidPathElement(runID) || !complianceevidence.ValidPathElement(attempt.GetProviderAttemptId()) {
		return fmt.Errorf("%w: run ID, provider attempt ID, and attempt record are required", constants.ErrEvaluationReportPersistFailed)
	}
	attemptID := attempt.GetProviderAttemptId()
	reference, err := s.SaveProtoArtifact(ctx, EvidenceScope{RunID: runID, AttemptID: attemptID}, complianceevidence.ArtifactTypeProviderAttemptRecord, attempt)
	if err != nil {
		return err
	}
	return s.writeEvidenceRef(ctx, s.layout.providerAttemptRefPath(runID, attemptID), reference)
}

// LoadProviderObservationWindow returns the sealed window recorded for one
// provider attempt of a run. A missing record reports constants.ErrNotFound.
func (s *Store) LoadProviderObservationWindow(ctx context.Context, runID, providerAttemptID string) (*evalv1.ProviderBoundaryObservationWindow, error) {
	if s == nil || !complianceevidence.ValidPathElement(runID) || !complianceevidence.ValidPathElement(providerAttemptID) {
		return nil, fmt.Errorf("%w: run ID and provider attempt ID are required", constants.ErrEvidenceArtifactMalformed)
	}
	reference, err := s.readEvidenceRef(ctx, s.layout.providerWindowRefPath(runID, providerAttemptID), runID, providerAttemptID)
	if err != nil {
		return nil, err
	}
	window := &evalv1.ProviderBoundaryObservationWindow{}
	if err := s.loadProtoArtifact(ctx, runID, reference, window); err != nil {
		return nil, err
	}
	if window.GetProviderAttemptId() != providerAttemptID {
		return nil, fmt.Errorf("%w: provider observation window binding does not match requested attempt", constants.ErrEvidenceScopeMismatch)
	}
	return window, nil
}

// LoadProviderAttemptRecord returns the sealed provider-attempt record recorded
// for one provider attempt of a run. A missing record reports constants.ErrNotFound.
func (s *Store) LoadProviderAttemptRecord(ctx context.Context, runID, providerAttemptID string) (*operatorv1.InferenceProviderAttemptRecord, error) {
	if s == nil || !complianceevidence.ValidPathElement(runID) || !complianceevidence.ValidPathElement(providerAttemptID) {
		return nil, fmt.Errorf("%w: run ID and provider attempt ID are required", constants.ErrEvidenceArtifactMalformed)
	}
	reference, err := s.readEvidenceRef(ctx, s.layout.providerAttemptRefPath(runID, providerAttemptID), runID, providerAttemptID)
	if err != nil {
		return nil, err
	}
	attempt := &operatorv1.InferenceProviderAttemptRecord{}
	if err := s.loadProtoArtifact(ctx, runID, reference, attempt); err != nil {
		return nil, err
	}
	if attempt.GetProviderAttemptId() != providerAttemptID {
		return nil, fmt.Errorf("%w: provider attempt record binding does not match requested attempt", constants.ErrEvidenceScopeMismatch)
	}
	return attempt, nil
}

// writeEvidenceRef records the reference to one sealed body under a provider
// attempt. The record is canonical and read-only, and it is written once per
// attempt; capture only saves a body that is not yet recorded.
func (s *Store) writeEvidenceRef(ctx context.Context, path string, reference *compliancev1.ComplianceEvidenceReference) error {
	body, err := compliancev1.MarshalCanonical(reference)
	if err != nil {
		return fmt.Errorf("%w: canonicalize evidence reference: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	if err := s.files.MkdirAll(ctx, filepath.Dir(path), constants.PermDirPrivate); err != nil {
		return fmt.Errorf("%w: create evidence reference directory: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	if err := s.files.WriteFile(ctx, path, body, constants.PermFileReadOnly); err != nil {
		return fmt.Errorf("%w: write evidence reference: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	return nil
}

// readEvidenceRef reads one provider attempt's evidence reference record and
// checks that it is bound to the requested run and attempt.
func (s *Store) readEvidenceRef(ctx context.Context, path, runID, providerAttemptID string) (*compliancev1.ComplianceEvidenceReference, error) {
	body, err := s.files.ReadFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read evidence reference: %w", err)
	}
	reference := &compliancev1.ComplianceEvidenceReference{}
	if err := compliancev1.UnmarshalCanonical(body, reference); err != nil {
		return nil, fmt.Errorf("%w: canonical evidence reference: %v", constants.ErrEvidenceArtifactMalformed, err)
	}
	if reference.GetRunId() != runID || reference.GetAttemptId() != providerAttemptID {
		return nil, fmt.Errorf("%w: evidence reference binding does not match requested run and attempt", constants.ErrEvidenceScopeMismatch)
	}
	return reference, nil
}

// VerifyAssignmentProviderObservations independently checks provider-boundary
// observation coverage for every scored inference in one assignment result.
func (r *CampaignProviderObservationReader) VerifyAssignmentProviderObservations(
	ctx context.Context,
	result *evalv1.EvaluationAssignmentResult,
	policy ProviderObservationPolicy,
) ([]string, []string) {
	if r == nil || result == nil {
		return nil, nil
	}
	failures := make([]string, 0)
	unavailable := make([]string, 0)
	for _, inferenceRecord := range scoredModelInferences(result) {
		attemptID := inferenceRecord.GetProviderAttemptId()
		if attemptID == "" {
			continue
		}
		window, err := r.store.LoadProviderObservationWindow(ctx, result.GetRunId(), attemptID)
		if err != nil {
			if errors.Is(err, constants.ErrNotFound) {
				unavailable = appendUniqueStrings(unavailable, "source_not_captured")
				if policy == ProviderObservationPolicyStrict {
					failures = append(failures, fmt.Sprintf("inference %s missing provider-boundary observation window", inferenceRecord.GetInferenceRecordId()))
				}
				continue
			}
			failures = append(failures, fmt.Sprintf("inference %s observation window load failed: %v", inferenceRecord.GetInferenceRecordId(), err))
			continue
		}
		attempt, err := r.store.LoadProviderAttemptRecord(ctx, result.GetRunId(), attemptID)
		if err != nil {
			failures = append(failures, fmt.Sprintf("inference %s provider attempt load failed: %v", inferenceRecord.GetInferenceRecordId(), err))
			continue
		}
		report, err := provider_observer.VerifyObservationCoverage(window, attempt)
		if err != nil {
			failures = append(failures, fmt.Sprintf("inference %s observation coverage verification failed: %v", inferenceRecord.GetInferenceRecordId(), err))
			continue
		}
		if report == nil || report.Complete {
			continue
		}
		unavailable = appendUniqueStrings(unavailable, "incomplete_contributor_evidence")
		for _, reason := range report.FailureReasons {
			failures = append(failures, fmt.Sprintf("inference %s: %s", inferenceRecord.GetInferenceRecordId(), reason))
		}
	}
	return failures, unavailable
}

func publicUnavailableMetricReasons(result *evalv1.EvaluationAssignmentResult) []string {
	for _, inference := range result.GetModelInferences() {
		if inference.GetUsageAvailability() == evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_UNAVAILABLE {
			return []string{"source_unavailable"}
		}
	}
	return nil
}

// BuildPublicBenchmarkObservations derives disclosure-safe benchmark telemetry
// from canonical assignment result records and provider-boundary windows.
func (r *CampaignProviderObservationReader) BuildPublicBenchmarkObservations(ctx context.Context, result *evalv1.EvaluationAssignmentResult) (*PublicBenchmarkObservations, error) {
	return r.buildPublicBenchmarkObservations(ctx, result, nil)
}

// BuildPublicBenchmarkObservationsForScenario applies the frozen scenario
// requirements when producing the tool scorecard.
func (r *CampaignProviderObservationReader) BuildPublicBenchmarkObservationsForScenario(ctx context.Context, result *evalv1.EvaluationAssignmentResult, scenario *PublicScenarioContext) (*PublicBenchmarkObservations, error) {
	return r.buildPublicBenchmarkObservations(ctx, result, scenario)
}

func (r *CampaignProviderObservationReader) buildPublicBenchmarkObservations(ctx context.Context, result *evalv1.EvaluationAssignmentResult, scenario *PublicScenarioContext) (*PublicBenchmarkObservations, error) {
	if result == nil {
		return nil, fmt.Errorf("evaluation: build public benchmark observations: %w", constants.ErrMissingRequiredField)
	}
	observations := &PublicBenchmarkObservations{
		UnavailableReasons: publicUnavailableMetricReasons(result),
	}
	timing := deriveBenchmarkTiming(result)
	if timing.hasValues() {
		observations.Timing = timing.toPublic()
	}
	if gradeSummaries := buildPublicGradeSummaries(result); len(gradeSummaries) > 0 {
		observations.GradeSummaries = gradeSummaries
	}
	var scorecard map[string]*PublicMetricValue
	if scenario == nil {
		scorecard = buildToolScorecardObservations(result)
	} else {
		scorecard = buildToolScorecardForScenario(result, scenario)
	}
	if len(scorecard) > 0 {
		observations.ToolScorecard = scorecard
	}
	if scenario != nil && scenario.Category == evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_SECURITY_POLICY {
		observations.SecurityPrivacyEvents = map[string]uint32{
			securityEventModelAttemptedUnauthorizedAccess: unauthorizedAttemptCount(result, scenario.ForbiddenTools),
		}
	}
	if r == nil {
		observations.UnavailableReasons = appendUniqueStrings(observations.UnavailableReasons, "source_not_captured")
		return observations, nil
	}
	gpu := &gpuAggregate{}
	for _, inferenceRecord := range scoredModelInferences(result) {
		attemptID := inferenceRecord.GetProviderAttemptId()
		if attemptID == "" {
			continue
		}
		window, err := r.store.LoadProviderObservationWindow(ctx, result.GetRunId(), attemptID)
		if err != nil {
			if isProviderEvidenceNotFound(err) {
				observations.UnavailableReasons = appendUniqueStrings(observations.UnavailableReasons, "source_not_captured")
				continue
			}
			return nil, fmt.Errorf("evaluation: build public benchmark observations: %w", err)
		}
		gpu.observeWindow(window)
	}
	if gpu.hasValues() {
		observations.GPU = gpu.toPublic()
	}
	if len(observations.UnavailableReasons) == 0 {
		observations.UnavailableReasons = nil
	}
	return observations, nil
}

func buildToolScorecardForScenario(result *evalv1.EvaluationAssignmentResult, scenario *PublicScenarioContext) map[string]*PublicMetricValue {
	scorecard := make(map[string]*PublicMetricValue, len(toolScorecardDimensions))
	required := make(map[string]bool, len(scenario.ToolScoreDimensions))
	for _, requirement := range scenario.ToolScoreDimensions {
		if requirement != nil {
			required[requirement.GetDimension().String()] = requirement.GetRequired()
		}
	}
	for _, dimension := range toolScorecardDimensions {
		if !required[toolScoreDimensionEnum(dimension).String()] {
			scorecard[dimension] = &PublicMetricValue{UnavailableReason: "scenario_not_applicable"}
			continue
		}
		scorecard[dimension] = &PublicMetricValue{UnavailableReason: "source_not_captured"}
	}
	for _, grade := range result.GetDeterministicGrades() {
		if grade == nil {
			continue
		}
		dimension, ok := gradeToToolScorecardDimension[grade.GetCriterionId()]
		if ok && required[toolScoreDimensionEnum(dimension).String()] {
			scorecard[dimension] = toolScorecardMetricFromGrade(grade)
		}
	}
	return scorecard
}

func toolScoreDimensionEnum(dimension string) evalv1.PublicToolScoreDimension {
	for _, candidate := range []evalv1.PublicToolScoreDimension{
		evalv1.PublicToolScoreDimension_PUBLIC_TOOL_SCORE_DIMENSION_TOOL_RECOGNITION,
		evalv1.PublicToolScoreDimension_PUBLIC_TOOL_SCORE_DIMENSION_TOOL_SELECTION,
		evalv1.PublicToolScoreDimension_PUBLIC_TOOL_SCORE_DIMENSION_ARGUMENT_SCHEMA,
		evalv1.PublicToolScoreDimension_PUBLIC_TOOL_SCORE_DIMENSION_ARGUMENT_SEMANTICS,
		evalv1.PublicToolScoreDimension_PUBLIC_TOOL_SCORE_DIMENSION_PERMISSION_COMPLIANCE,
		evalv1.PublicToolScoreDimension_PUBLIC_TOOL_SCORE_DIMENSION_RESULT_INTERPRETATION,
		evalv1.PublicToolScoreDimension_PUBLIC_TOOL_SCORE_DIMENSION_FOLLOW_UP_DECISION,
		evalv1.PublicToolScoreDimension_PUBLIC_TOOL_SCORE_DIMENSION_UNNECESSARY_TOOL_CALLS,
		evalv1.PublicToolScoreDimension_PUBLIC_TOOL_SCORE_DIMENSION_LOOPING,
		evalv1.PublicToolScoreDimension_PUBLIC_TOOL_SCORE_DIMENSION_RECOVERY,
	} {
		if candidate.String() == "PUBLIC_TOOL_SCORE_DIMENSION_"+strings.ToUpper(dimension) {
			return candidate
		}
	}
	return evalv1.PublicToolScoreDimension_PUBLIC_TOOL_SCORE_DIMENSION_UNSPECIFIED
}

func buildToolScorecardObservations(result *evalv1.EvaluationAssignmentResult) map[string]*PublicMetricValue {
	scorecard := make(map[string]*PublicMetricValue, len(toolScorecardDimensions))
	for _, dimension := range toolScorecardDimensions {
		scorecard[dimension] = &PublicMetricValue{
			UnavailableReason: "scenario_not_applicable",
		}
	}
	if result == nil {
		return scorecard
	}
	for _, grade := range result.GetDeterministicGrades() {
		if grade == nil {
			continue
		}
		dimension, ok := gradeToToolScorecardDimension[grade.GetCriterionId()]
		if !ok {
			continue
		}
		scorecard[dimension] = toolScorecardMetricFromGrade(grade)
	}
	return scorecard
}

func toolScorecardMetricFromGrade(grade *evalv1.DeterministicGrade) *PublicMetricValue {
	switch grade.GetStatus() {
	case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL:
		return publicMetricValue(grade.GetScore())
	default:
		return &PublicMetricValue{UnavailableReason: "source_unavailable"}
	}
}

func publicVerdictStatus(status evalv1.EvaluationVerdictStatus) string {
	switch status {
	case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS:
		return "pass"
	case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL:
		return "fail"
	case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE:
		return "unavailable"
	case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSUPPORTED:
		return "unsupported"
	case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE:
		return "invalid_evidence"
	default:
		return "unspecified"
	}
}

func scoredModelInferences(result *evalv1.EvaluationAssignmentResult) []*evalv1.ModelInferenceRecord {
	if result == nil {
		return nil
	}
	records := make([]*evalv1.ModelInferenceRecord, 0, len(result.GetModelInferences()))
	for _, record := range result.GetModelInferences() {
		if record == nil {
			continue
		}
		records = append(records, record)
	}
	return records
}

type benchmarkTimingAggregate struct {
	modelLoadMS        *float64
	timeToFirstTokenMS *float64
	generationMS       *float64
	wholeTaskMS        *float64
}

func deriveBenchmarkTiming(result *evalv1.EvaluationAssignmentResult) benchmarkTimingAggregate {
	aggregate := benchmarkTimingAggregate{}
	for _, record := range scoredModelInferences(result) {
		if record.GetLoadDurationNanos() > 0 {
			aggregate.modelLoadMS = maxFloatPtr(aggregate.modelLoadMS, nanosToMillis(record.GetLoadDurationNanos()))
		}
		if record.GetRequestStartedAtUnixNanos() > 0 && record.GetFirstTokenAtUnixNanos() > record.GetRequestStartedAtUnixNanos() {
			aggregate.timeToFirstTokenMS = maxFloatPtr(
				aggregate.timeToFirstTokenMS,
				nanosToMillis(record.GetFirstTokenAtUnixNanos()-record.GetRequestStartedAtUnixNanos()),
			)
		}
		if record.GetGenerationDurationNanos() > 0 {
			aggregate.generationMS = maxFloatPtr(aggregate.generationMS, nanosToMillis(record.GetGenerationDurationNanos()))
		}
		if record.GetTotalDurationNanos() > 0 {
			aggregate.wholeTaskMS = maxFloatPtr(aggregate.wholeTaskMS, nanosToMillis(record.GetTotalDurationNanos()))
		}
	}
	return aggregate
}

func (aggregate benchmarkTimingAggregate) hasValues() bool {
	return aggregate.modelLoadMS != nil || aggregate.timeToFirstTokenMS != nil || aggregate.generationMS != nil || aggregate.wholeTaskMS != nil
}

func (aggregate benchmarkTimingAggregate) toPublic() *PublicBenchmarkTiming {
	timing := &PublicBenchmarkTiming{}
	if aggregate.modelLoadMS != nil {
		timing.ModelLoadMS = publicMetricValue(*aggregate.modelLoadMS)
	}
	if aggregate.timeToFirstTokenMS != nil {
		timing.TimeToFirstTokenMS = publicMetricValue(*aggregate.timeToFirstTokenMS)
	}
	if aggregate.generationMS != nil {
		timing.GenerationMS = publicMetricValue(*aggregate.generationMS)
	}
	if aggregate.wholeTaskMS != nil {
		timing.WholeTaskMS = publicMetricValue(*aggregate.wholeTaskMS)
	}
	return timing
}

type gpuMetricProjection struct {
	value             *float64
	unavailableReason string
}

type gpuAggregate struct {
	vramBefore       *uint64
	vramPeak         *uint64
	hostRAMPeak      *uint64
	utilizationPeak  *float64
	temperaturePeak  *float64
	powerPeak        *float64
	clockPeak        *uint32
	seenVRAMBefore   bool
	windowObserved   bool
	vramBeforeState  gpuMetricProjection
	vramPeakState    gpuMetricProjection
	hostRAMState     gpuMetricProjection
	utilizationState gpuMetricProjection
	temperatureState gpuMetricProjection
	powerState       gpuMetricProjection
	clockState       gpuMetricProjection
}

func (aggregate *gpuAggregate) observeWindow(window *evalv1.ProviderBoundaryObservationWindow) {
	if aggregate == nil || window == nil {
		return
	}
	aggregate.windowObserved = true
	for index, sample := range window.GetSamples() {
		if sample == nil {
			continue
		}
		aggregate.observeVRAMBefore(sample, index)
		aggregate.observeVRAMPeak(sample)
		aggregate.observeHostRAM(sample)
		aggregate.observeUtilization(sample)
		aggregate.observeTemperature(sample)
		aggregate.observePower(sample)
		aggregate.observeClock(sample)
	}
}

func (aggregate *gpuAggregate) observeVRAMBefore(sample *evalv1.ProviderBoundaryHardwareSample, index int) {
	switch sample.GetVramBytesAvailability() {
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED:
		if !aggregate.seenVRAMBefore {
			aggregate.vramBefore = uint64Ptr(sample.GetVramUsedBytes())
			aggregate.vramBeforeState.value = float64Ptr(float64(sample.GetVramUsedBytes()))
			aggregate.seenVRAMBefore = true
		}
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_UNAVAILABLE:
		aggregate.vramBeforeState.unavailableReason = gpuMetricUnavailableReason("vram_before_bytes")
	}
	if index == 0 && !aggregate.seenVRAMBefore && sample.GetVramBytesAvailability() != evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED {
		aggregate.seenVRAMBefore = true
	}
}

func (aggregate *gpuAggregate) observeVRAMPeak(sample *evalv1.ProviderBoundaryHardwareSample) {
	switch sample.GetVramBytesAvailability() {
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED:
		aggregate.vramPeak = maxUint64Ptr(aggregate.vramPeak, sample.GetVramUsedBytes())
		aggregate.vramPeakState.value = maxFloatPtr(aggregate.vramPeakState.value, float64(sample.GetVramUsedBytes()))
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_UNAVAILABLE:
		aggregate.vramPeakState.unavailableReason = gpuMetricUnavailableReason("vram_peak_bytes")
	}
}

func (aggregate *gpuAggregate) observeHostRAM(sample *evalv1.ProviderBoundaryHardwareSample) {
	switch sample.GetHostRamAvailability() {
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED:
		aggregate.hostRAMPeak = maxUint64Ptr(aggregate.hostRAMPeak, sample.GetHostRamUsedBytes())
		aggregate.hostRAMState.value = maxFloatPtr(aggregate.hostRAMState.value, float64(sample.GetHostRamUsedBytes()))
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_UNAVAILABLE:
		aggregate.hostRAMState.unavailableReason = gpuMetricUnavailableReason("system_ram_peak_bytes")
	}
}

func (aggregate *gpuAggregate) observeUtilization(sample *evalv1.ProviderBoundaryHardwareSample) {
	switch sample.GetGpuUtilizationAvailability() {
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED:
		aggregate.utilizationPeak = maxFloatPtr(aggregate.utilizationPeak, float64(sample.GetGpuUtilizationPercent()))
		aggregate.utilizationState.value = maxFloatPtr(aggregate.utilizationState.value, float64(sample.GetGpuUtilizationPercent()))
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_UNAVAILABLE:
		aggregate.utilizationState.unavailableReason = gpuMetricUnavailableReason("utilization_percent")
	}
}

func (aggregate *gpuAggregate) observeTemperature(sample *evalv1.ProviderBoundaryHardwareSample) {
	switch sample.GetTemperatureAvailability() {
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED:
		aggregate.temperaturePeak = maxFloatPtr(aggregate.temperaturePeak, float64(sample.GetTemperatureCelsius()))
		aggregate.temperatureState.value = maxFloatPtr(aggregate.temperatureState.value, float64(sample.GetTemperatureCelsius()))
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_UNAVAILABLE:
		aggregate.temperatureState.unavailableReason = gpuMetricUnavailableReason("temperature_celsius")
	}
}

func (aggregate *gpuAggregate) observePower(sample *evalv1.ProviderBoundaryHardwareSample) {
	switch sample.GetPowerAvailability() {
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED:
		aggregate.powerPeak = maxFloatPtr(aggregate.powerPeak, float64(sample.GetPowerWatts()))
		aggregate.powerState.value = maxFloatPtr(aggregate.powerState.value, float64(sample.GetPowerWatts()))
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_UNAVAILABLE:
		aggregate.powerState.unavailableReason = gpuMetricUnavailableReason("power_watts")
	}
}

func (aggregate *gpuAggregate) observeClock(sample *evalv1.ProviderBoundaryHardwareSample) {
	switch sample.GetClockAvailability() {
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED:
		aggregate.clockPeak = maxUint32Ptr(aggregate.clockPeak, sample.GetClockMhz())
		if aggregate.clockState.value == nil || float64(sample.GetClockMhz()) > *aggregate.clockState.value {
			aggregate.clockState.value = float64Ptr(float64(sample.GetClockMhz()))
		}
	case evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_UNAVAILABLE:
		aggregate.clockState.unavailableReason = gpuMetricUnavailableReason("clock_mhz")
	}
}

func gpuMetricUnavailableReason(metric string) string {
	return "source_unavailable"
}

func (aggregate *gpuAggregate) hasValues() bool {
	return aggregate.vramBefore != nil || aggregate.vramPeak != nil || aggregate.hostRAMPeak != nil ||
		aggregate.utilizationPeak != nil || aggregate.temperaturePeak != nil || aggregate.powerPeak != nil || aggregate.clockPeak != nil ||
		aggregate.windowObserved && (aggregate.vramBeforeState.unavailableReason != "" || aggregate.vramPeakState.unavailableReason != "" ||
			aggregate.hostRAMState.unavailableReason != "" || aggregate.utilizationState.unavailableReason != "" ||
			aggregate.temperatureState.unavailableReason != "" || aggregate.powerState.unavailableReason != "" ||
			aggregate.clockState.unavailableReason != "")
}

func (aggregate *gpuAggregate) toPublic() *PublicGPUObservation {
	gpu := &PublicGPUObservation{}
	gpu.VRAMBeforeBytes = aggregate.projectMetric(aggregate.vramBeforeState)
	gpu.VRAMPeakBytes = aggregate.projectMetric(aggregate.vramPeakState)
	gpu.SystemRAMPeakBytes = aggregate.projectMetric(aggregate.hostRAMState)
	gpu.UtilizationPercent = aggregate.projectMetric(aggregate.utilizationState)
	gpu.TemperatureCelsius = aggregate.projectMetric(aggregate.temperatureState)
	gpu.PowerWatts = aggregate.projectMetric(aggregate.powerState)
	gpu.ClockMHz = aggregate.projectMetric(aggregate.clockState)
	return gpu
}

func (aggregate *gpuAggregate) projectMetric(state gpuMetricProjection) *PublicMetricValue {
	if state.value != nil {
		return publicMetricValue(*state.value)
	}
	if aggregate.windowObserved && state.unavailableReason != "" {
		return &PublicMetricValue{UnavailableReason: state.unavailableReason}
	}
	return nil
}

func publicMetricValue(value float64) *PublicMetricValue {
	copied := value
	return &PublicMetricValue{Value: &copied}
}

func float64Ptr(value float64) *float64 {
	return &value
}

func nanosToMillis(value uint64) float64 {
	return float64(value) / 1_000_000
}

func maxFloatPtr(current *float64, candidate float64) *float64 {
	if math.IsNaN(candidate) || math.IsInf(candidate, 0) {
		return current
	}
	if current == nil || candidate > *current {
		value := candidate
		return &value
	}
	return current
}

func maxUint64Ptr(current *uint64, candidate uint64) *uint64 {
	if current == nil || candidate > *current {
		value := candidate
		return &value
	}
	return current
}

func maxUint32Ptr(current *uint32, candidate uint32) *uint32 {
	if current == nil || candidate > *current {
		value := candidate
		return &value
	}
	return current
}

func uint64Ptr(value uint64) *uint64 {
	return &value
}

func appendUniqueStrings(values []string, candidate string) []string {
	for _, existing := range values {
		if existing == candidate {
			return values
		}
	}
	return append(values, candidate)
}
