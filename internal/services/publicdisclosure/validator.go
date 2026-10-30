// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.

package publicdisclosure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	historicalVersion = "1.0.0"
	// enrichedVersion is the first envelope with named public extensions;
	// trajectoryVersion adds trajectory_outcome, guided_retry_count,
	// failure_reason, tools_declared, and the per-tool-call guidance fields.
	enrichedVersion   = "1.1.0"
	trajectoryVersion = "1.2.0"
	maxRecordBytes    = 256 * 1024
	maxArrayEntries   = 128
	maxStringBytes    = 512
)

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateAssignmentRecord validates a campaign assignment envelope. Records
// without a campaign message_type are handled by the general feed validator.
func ValidateAssignmentRecord(envelopeVersion string, recordBytes []byte) error {
	if len(recordBytes) == 0 {
		return schemaError("empty assignment record")
	}
	if len(recordBytes) > maxRecordBytes {
		return fmt.Errorf("public assignment record: %w", constants.ErrEvidenceArtifactTooLarge)
	}
	fields, err := decodeObject(recordBytes)
	if err != nil {
		return schemaError(err.Error())
	}
	messageType, ok := stringField(fields, "message_type")
	if !ok {
		return nil
	}
	if messageType != "PublicAssignmentLifecycleRecord" && messageType != "PublicAssignmentResultProjection" {
		return fmt.Errorf("unsupported campaign message type %q: %w", messageType, constants.ErrEvidenceSchemaMismatch)
	}
	var envelope struct {
		SchemaVersion string          `json:"schema_version"`
		MessageType   string          `json:"message_type"`
		Idempotency   string          `json:"idempotency_key"`
		Record        json.RawMessage `json:"record"`
	}
	if err := decodeStrict(recordBytes, &envelope); err != nil {
		return schemaError(err.Error())
	}
	if envelope.SchemaVersion == "" || envelope.MessageType == "" || envelope.Idempotency == "" || len(envelope.Record) == 0 {
		return schemaError("incomplete campaign envelope")
	}
	if envelopeVersion != "" && envelopeVersion != envelope.SchemaVersion {
		return schemaError("envelope version mismatch")
	}
	if envelope.MessageType == "PublicAssignmentLifecycleRecord" {
		if envelope.SchemaVersion != historicalVersion {
			return schemaError("unsupported lifecycle version")
		}
		var lifecycle evalv1.PublicAssignmentLifecycleRecord
		if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(envelope.Record, &lifecycle); err != nil {
			return schemaError(err.Error())
		}
		if lifecycle.GetAssignmentId() == "" || lifecycle.GetRunId() == "" || lifecycle.GetScenarioId() == "" {
			return schemaError("missing lifecycle identity")
		}
		if err := validateReleaseIdentity(lifecycle.GetRelease(), lifecycle.GetReleaseBasis(), lifecycle.GetSourceRevision()); err != nil {
			return err
		}
		return validateStrings(envelope.Record)
	}
	if envelope.SchemaVersion != historicalVersion && envelope.SchemaVersion != enrichedVersion && envelope.SchemaVersion != trajectoryVersion {
		return schemaError("unsupported assignment version")
	}
	return validateResult(envelope.SchemaVersion, envelope.Record)
}

func validateResult(version string, raw json.RawMessage) error {
	fields, err := decodeObject(raw)
	if err != nil {
		return schemaError(err.Error())
	}
	// These named public extensions predate the protobuf projection. Keep their
	// raw JSON out of the typed protobuf decode while preserving the closed
	// allowlist for every other field.
	extensions := make(map[string]json.RawMessage)
	for _, key := range []string{"benchmark_observations", "resource_summary", "model_response", "failure_output", "role_transcripts"} {
		if value, ok := fields[key]; ok {
			extensions[key] = value
			delete(fields, key)
		}
	}
	projection, err := json.Marshal(fields)
	if err != nil {
		return schemaError(err.Error())
	}
	var record evalv1.PublicAssignmentResultProjection
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(projection, &record); err != nil {
		return schemaError(err.Error())
	}
	if record.GetAssignmentId() == "" || record.GetRunId() == "" || record.GetScenarioId() == "" {
		return schemaError("missing assignment identity")
	}
	if err := validateReleaseIdentity(record.GetRelease(), record.GetReleaseBasis(), record.GetSourceRevision()); err != nil {
		return err
	}
	if record.GetResultDigest() != "" && !hashPattern.MatchString(record.GetResultDigest()) {
		return fmt.Errorf("invalid result digest: %w", constants.ErrEvidenceArtifactMalformed)
	}
	if len(record.GetUnavailableMetricReasons()) > 16 || len(record.GetDecomposedScores()) > maxArrayEntries || len(record.GetSemanticGradeSummaries()) > maxArrayEntries || len(record.GetEvidenceBindings()) > 32 {
		return fmt.Errorf("assignment bounds: %w", constants.ErrEvidenceArtifactTooLarge)
	}
	if err := validateBindings(record.GetEvidenceBindings()); err != nil {
		return err
	}
	if version == historicalVersion && len(extensions) > 0 {
		return schemaError("enriched fields in historical envelope")
	}
	if version != trajectoryVersion && hasTrajectoryFields(&record) {
		return schemaError("trajectory fields require envelope " + trajectoryVersion)
	}
	if value, ok := extensions["benchmark_observations"]; ok {
		if err := validateBenchmark(value); err != nil {
			return err
		}
	}
	if value, ok := extensions["resource_summary"]; ok {
		if err := validateResource(value); err != nil {
			return err
		}
	}
	return validateStrings(raw)
}

// hasTrajectoryFields reports whether a projection carries any field that only
// envelope 1.2.0 may contain.
func hasTrajectoryFields(record *evalv1.PublicAssignmentResultProjection) bool {
	if record.GetTrajectoryOutcome() != evalv1.EvaluationTrajectoryOutcome_EVALUATION_TRAJECTORY_OUTCOME_UNSPECIFIED ||
		record.GetGuidedRetryCount() != 0 || record.GetFailureReason() != "" || len(record.GetToolsDeclared()) > 0 {
		return true
	}
	summary := record.GetScenarioSummary()
	if summary.GetTrajectoryPolicy() != evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_UNSPECIFIED || summary.GetPromptHint() != nil {
		return true
	}
	for _, call := range record.GetActivitySummary().GetToolCalls().GetRecords() {
		if call.GetLoopTurn() != 0 || call.GetErrorType() != "" || call.GetGuidanceShown() {
			return true
		}
	}
	return false
}

func validateBindings(bindings []*evalv1.PublicEvidenceBinding) error {
	seen := make(map[string]struct{}, len(bindings))
	for _, binding := range bindings {
		if binding == nil || !hashPattern.MatchString(binding.GetSha256()) || !bounded(binding.GetSchemaRef(), 128) || !bounded(binding.GetKind(), 128) {
			return fmt.Errorf("malformed evidence binding: %w", constants.ErrEvidenceArtifactMalformed)
		}
		key := binding.GetSha256() + "\x00" + binding.GetSchemaRef() + "\x00" + binding.GetKind()
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate evidence binding: %w", constants.ErrEvidenceArtifactMalformed)
		}
		seen[key] = struct{}{}
	}
	return nil
}

type metric struct {
	Value             *float64 `json:"value,omitempty"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
}

type benchmark struct {
	GradeSummaries        []json.RawMessage          `json:"grade_summaries,omitempty"`
	ToolScorecard         map[string]json.RawMessage `json:"tool_scorecard,omitempty"`
	SecurityPrivacyEvents map[string]uint32          `json:"security_privacy_events,omitempty"`
	Timing                map[string]json.RawMessage `json:"timing,omitempty"`
	GPU                   map[string]json.RawMessage `json:"gpu,omitempty"`
	UnavailableReasons    []string                   `json:"unavailable_reasons"`
}

func validateBenchmark(raw json.RawMessage) error {
	var value benchmark
	if err := decodeStrict(raw, &value); err != nil {
		return schemaError(err.Error())
	}
	if len(value.GradeSummaries) > 64 || len(value.ToolScorecard) > 10 || len(value.UnavailableReasons) > 16 {
		return fmt.Errorf("benchmark bounds: %w", constants.ErrEvidenceArtifactTooLarge)
	}
	for _, grade := range value.GradeSummaries {
		fields, err := decodeObject(grade)
		if err != nil {
			return schemaError(err.Error())
		}
		if err := allowed(fields, "criterion_id", "status", "method", "judge_variant_id", "explanation_code", "detail"); err != nil {
			return err
		}
	}
	for key, value := range value.ToolScorecard {
		if !toolDimension(key) {
			return schemaError("unknown tool score dimension")
		}
		if err := validateMetric(value); err != nil {
			return err
		}
	}
	for key := range value.SecurityPrivacyEvents {
		if key != "model_attempted_unauthorized_access" {
			return schemaError("unknown security privacy event")
		}
	}
	for _, value := range value.Timing {
		if err := validateMetric(value); err != nil {
			return err
		}
	}
	for _, value := range value.GPU {
		if err := validateMetric(value); err != nil {
			return err
		}
	}
	return nil
}

func validateResource(raw json.RawMessage) error {
	fields, err := decodeObject(raw)
	if err != nil {
		return schemaError(err.Error())
	}
	if err := allowed(fields, "latency_ms", "input_tokens", "output_tokens", "thinking_tokens", "cache_tokens", "retries"); err != nil {
		return err
	}
	for _, value := range fields {
		if err := validateMetric(value); err != nil {
			return err
		}
	}
	return nil
}

func validateMetric(raw json.RawMessage) error {
	var value metric
	if err := decodeStrict(raw, &value); err != nil {
		return schemaError(err.Error())
	}
	if value.Value == nil && value.UnavailableReason == "" || value.Value != nil && value.UnavailableReason != "" {
		return schemaError("metric must be observed or unavailable")
	}
	if value.Value != nil && (math.IsNaN(*value.Value) || math.IsInf(*value.Value, 0) || *value.Value < 0) {
		return fmt.Errorf("invalid metric: %w", constants.ErrEvidenceArtifactMalformed)
	}
	if value.UnavailableReason != "" && !unavailableReason(value.UnavailableReason) {
		return schemaError("unknown unavailable reason")
	}
	return nil
}

func validateStrings(raw []byte) error {
	var value json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return walkStrings(value)
}

func walkStrings(value json.RawMessage) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		if len(text) > maxStringBytes {
			return fmt.Errorf("assignment text bounds: %w", constants.ErrEvidenceArtifactTooLarge)
		}
		return nil
	}
	if trimmed[0] == '[' {
		var values []json.RawMessage
		if err := json.Unmarshal(trimmed, &values); err != nil {
			return err
		}
		if len(values) > maxArrayEntries {
			return fmt.Errorf("assignment array bound: %w", constants.ErrEvidenceArtifactTooLarge)
		}
		for _, child := range values {
			if err := walkStrings(child); err != nil {
				return err
			}
		}
		return nil
	}
	if trimmed[0] == '{' {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return err
		}
		for key, child := range fields {
			if key == "model_response" || key == "failure_output" {
				if err := walkModelText(child); err != nil {
					return err
				}
				continue
			}
			if key == "role_transcripts" {
				if err := ValidateRoleTranscripts(child); err != nil {
					return err
				}
				continue
			}
			if key == "grade_summaries" || key == "semantic_grade_summaries" {
				if err := walkGradeSummaries(child); err != nil {
					return err
				}
				continue
			}
			if err := walkStrings(child); err != nil {
				return err
			}
		}
	}
	return nil
}

// walkGradeSummaries checks grade identifiers like any other structural
// string and carries each grader's detail verbatim under the model-text bound.
func walkGradeSummaries(value json.RawMessage) error {
	var grades []map[string]json.RawMessage
	if err := json.Unmarshal(value, &grades); err != nil {
		return schemaError(err.Error())
	}
	if len(grades) > maxArrayEntries {
		return fmt.Errorf("assignment array bound: %w", constants.ErrEvidenceArtifactTooLarge)
	}
	for _, grade := range grades {
		for key, child := range grade {
			walk := walkStrings
			if key == "detail" {
				walk = walkModelText
			}
			if err := walk(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func walkModelText(value json.RawMessage) error {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		if len(text) > maxRecordBytes {
			return fmt.Errorf("assignment text bounds: %w", constants.ErrEvidenceArtifactTooLarge)
		}
	}
	return nil
}

// RoleTranscript is the existing bounded public model/tool transcript contract.
type RoleTranscript struct {
	Role         string                   `json:"role"`
	Response     string                   `json:"response,omitempty"`
	FinishReason string                   `json:"finish_reason,omitempty"`
	TraceDigest  string                   `json:"trace_digest,omitempty"`
	ToolCalls    []RoleTranscriptToolCall `json:"tool_calls,omitempty"`
}

type RoleTranscriptToolCall struct {
	ToolName      string `json:"tool_name"`
	ArgumentsJSON string `json:"arguments_json,omitempty"`
	ArgumentsHash string `json:"arguments_hash,omitempty"`
	// ApprovedArgumentsJSON is the Tribunal-approved arguments the call executed.
	ApprovedArgumentsJSON string `json:"approved_arguments_json,omitempty"`
	Command               string `json:"command,omitempty"`
	Success               bool   `json:"success"`
	ErrorType             string `json:"error_type,omitempty"`
	ResultJSON            string `json:"result_json,omitempty"`
}

// ValidateRoleTranscripts enforces the closed role_transcripts schema: known
// roles only, digests as SHA-256 hex, identifiers bounded, and every captured
// model/tool text size-bounded like model_response. Transcript content is
// published verbatim; it is never redacted or screened.
func ValidateRoleTranscripts(raw json.RawMessage) error {
	if len(raw) > maxRecordBytes {
		return fmt.Errorf("role transcript bounds: %w", constants.ErrEvidenceArtifactTooLarge)
	}
	var transcripts []RoleTranscript
	if err := decodeStrict(raw, &transcripts); err != nil {
		return schemaError(err.Error())
	}
	if len(transcripts) > 8 {
		return fmt.Errorf("role transcript bounds: %w", constants.ErrEvidenceArtifactTooLarge)
	}
	for _, transcript := range transcripts {
		switch transcript.Role {
		case "primary", "assistant", "lite":
		default:
			return schemaError("unknown transcript role")
		}
		if transcript.TraceDigest != "" && !hashPattern.MatchString(transcript.TraceDigest) {
			return fmt.Errorf("invalid transcript trace digest: %w", constants.ErrEvidenceArtifactMalformed)
		}
		if transcript.FinishReason != "" && !bounded(transcript.FinishReason, 64) || len(transcript.ToolCalls) > maxArrayEntries {
			return fmt.Errorf("role transcript bounds: %w", constants.ErrEvidenceArtifactTooLarge)
		}
		texts := []string{transcript.Response}
		for _, call := range transcript.ToolCalls {
			if !bounded(call.ToolName, 128) || call.ErrorType != "" && !bounded(call.ErrorType, 128) {
				return schemaError("malformed transcript tool call")
			}
			if call.ArgumentsHash != "" && !hashPattern.MatchString(call.ArgumentsHash) {
				return fmt.Errorf("invalid transcript arguments hash: %w", constants.ErrEvidenceArtifactMalformed)
			}
			texts = append(texts, call.ArgumentsJSON, call.ApprovedArgumentsJSON, call.Command, call.ResultJSON)
		}
		for _, text := range texts {
			if len(text) > maxRecordBytes {
				return fmt.Errorf("role transcript bounds: %w", constants.ErrEvidenceArtifactTooLarge)
			}
		}
	}
	return nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	// RawMessage values are intentional here: allowlist validation often closes
	// field names while preserving historical field types and values.
	var fields map[string]json.RawMessage
	if err := decodeStrict(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("object required")
	}
	return fields, nil
}

func stringField(fields map[string]json.RawMessage, key string) (string, bool) {
	value, ok := fields[key]
	if !ok {
		return "", false
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil {
		return "", true
	}
	return result, true
}

func allowed(fields map[string]json.RawMessage, keys ...string) error {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[key] = struct{}{}
	}
	for key := range fields {
		if _, ok := set[key]; !ok {
			return fmt.Errorf("unknown assignment field %q: %w", key, constants.ErrEvidenceSchemaMismatch)
		}
	}
	return nil
}

func bounded(value string, max int) bool {
	return value != "" && len(value) <= max && !strings.ContainsAny(value, "\r\n")
}

func toolDimension(value string) bool {
	switch value {
	case "tool_recognition", "tool_selection", "argument_schema", "argument_semantics", "permission_compliance", "result_interpretation", "follow_up_decision", "unnecessary_tool_calls", "looping", "recovery":
		return true
	default:
		return false
	}
}

func unavailableReason(value string) bool {
	switch value {
	case "historical_not_captured", "source_not_captured", "source_unavailable", "scenario_not_applicable", "incomplete_contributor_evidence", "no_scored_calls":
		return true
	default:
		return false
	}
}

// ValidatePublicFeedRecord validates the payload schema associated with one
// public-feed transport record type. The transport enum alone is not enough:
// browsers dispatch by the payload's closed kind vocabulary as well.
func ValidatePublicFeedRecord(recordType models.PublicFeedRecordType, recordBytes []byte) error {
	switch recordType {
	case models.PublicFeedRecordTypeProjection:
		fields, err := decodeObject(recordBytes)
		if err != nil {
			return publicFeedSchemaError(err.Error())
		}
		if _, hasMessageType := fields["message_type"]; hasMessageType {
			if err := ValidateAssignmentRecord("", recordBytes); err != nil {
				return fmt.Errorf("public feed projection: %w", err)
			}
			return nil
		}
		return validateExplorerViewRecord(recordBytes)
	case models.PublicFeedRecordTypeEvent:
		return validatePublicLiveEvent(recordBytes)
	case models.PublicFeedRecordTypeProofManifest:
		return validatePublicFeedManifest(recordBytes)
	case models.PublicFeedRecordTypeKeyRevocation:
		return validatePublicFeedKeyRevocation(recordBytes)
	default:
		return fmt.Errorf("%w: %q", constants.ErrPublicFeedRecordTypeInvalid, recordType)
	}
}

func validateExplorerViewRecord(recordBytes []byte) error {
	fields, err := decodeObject(recordBytes)
	if err != nil {
		return publicFeedSchemaError(err.Error())
	}
	if err := validateExplorerEnvelope(fields); err != nil {
		return err
	}
	if _, ok := fields["kind"]; !ok {
		return publicFeedSchemaError("missing projection kind")
	}
	return nil
}

// validatePublicLiveEvent decodes one live event as its protobuf message, so a
// field outside the message is rejected by the decoder, then checks the closed
// vocabularies the Explorer view contract defines. completed and total track
// presence: an event that omits either is malformed, not at zero.
func validatePublicLiveEvent(recordBytes []byte) error {
	event := &evalv1.PublicLiveEvent{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(recordBytes, event); err != nil {
		return publicFeedSchemaError(err.Error())
	}
	if !publicFeedViewSchemaVersion(event.GetSchemaVersion()) {
		return publicFeedSchemaError("unsupported schema_version")
	}
	if !publicFeedEventKind(event.GetKind()) {
		return publicFeedSchemaError("unsupported kind")
	}
	if event.GetDatasetId() == "" || !publicFeedQualityState(event.GetQualityState()) || event.GetObservedAt() == "" {
		return publicFeedSchemaError("missing or invalid envelope field")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.GetObservedAt()); err != nil {
		return publicFeedSchemaError("observed_at must be RFC3339")
	}
	if err := validateReleaseIdentity(event.GetRelease(), event.GetReleaseBasis(), event.GetSourceRevision()); err != nil {
		return publicFeedSchemaError(err.Error())
	}
	if event.GetEventId() == "" || event.GetRunId() == "" || !publicFeedLifecycleStatus(event.GetLifecycleStatus()) {
		return publicFeedSchemaError("missing or invalid event identity")
	}
	if event.Completed == nil || event.Total == nil {
		return publicFeedSchemaError("event progress is missing")
	}
	if event.GetCompleted() > event.GetTotal() {
		return publicFeedSchemaError("event progress is invalid")
	}
	if event.GetRole() != "" && !publicFeedRole(event.GetRole()) {
		return publicFeedSchemaError("invalid event role")
	}
	for key, metric := range event.GetMetricDelta() {
		if err := validateLiveMetricValue(key, metric); err != nil {
			return err
		}
	}
	return validateLiveJudgeEvent(event)
}

// validateLiveJudgeEvent holds judge events to their shape: a judge block on
// judge kinds only, placed on the graded subject's running assignment row, and
// never a metric or free-text stage that could merge into the subject's row.
func validateLiveJudgeEvent(event *evalv1.PublicLiveEvent) error {
	judgeKind := event.GetKind() == "judge_started" || event.GetKind() == "judge_completed"
	if !judgeKind {
		if event.GetJudge() != nil {
			return publicFeedSchemaError("judge is set on a non-judge event")
		}
		return nil
	}
	if event.GetJudge() == nil || event.GetAssignmentId() == "" || event.GetTaskId() == "" || event.GetVariantId() == "" || event.GetRole() == "" {
		return publicFeedSchemaError("judge event requires judge, assignment, task, variant, and role")
	}
	if len(event.GetMetricDelta()) > 0 || event.GetStageLabel() != "" || event.GetLifecycleStatus() != "running" {
		return publicFeedSchemaError("judge event carries a metric, a stage label, or a non-running lifecycle")
	}
	return validateLiveJudgeActivity(event.GetJudge(), event.GetKind() == "judge_completed")
}

func validateLiveJudgeActivity(judge *evalv1.PublicLiveJudgeActivity, completed bool) error {
	if judge.GetJudgeVariantId() == "" || judge.GetServedModelTag() == "" || judge.GetModelDigest() == "" || judge.GetRubricVersion() == "" {
		return publicFeedSchemaError("judge identity or rubric is missing")
	}
	criterion := judge.GetSubject() == evalv1.JudgeSubject_JUDGE_SUBJECT_CRITERION
	switch judge.GetSubject() {
	case evalv1.JudgeSubject_JUDGE_SUBJECT_CRITERION, evalv1.JudgeSubject_JUDGE_SUBJECT_FINAL_REPLY, evalv1.JudgeSubject_JUDGE_SUBJECT_MEMORY:
	default:
		return publicFeedSchemaError("invalid judge subject")
	}
	if criterion != (judge.GetCriterionId() != "") {
		return publicFeedSchemaError("criterion_id is set exactly for a criterion judgment")
	}
	if judge.GetIndex() < 1 || judge.GetIndex() > judge.GetCount() {
		return publicFeedSchemaError("judge index is outside 1..count")
	}
	if !completed {
		if judge.GetState() != evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_JUDGING || judge.GetProviderAttemptId() == "" ||
			judge.Score != nil || judge.GetRationale() != "" || judge.GetFailure() != evalv1.JudgeFailure_JUDGE_FAILURE_UNSPECIFIED ||
			judge.GetTransactionId() != "" || judge.GetUsageReported() || judge.GetFinishReason() != "" || judge.ExecutedAtUnixMs != nil ||
			judge.DispatchElapsedNs != nil || judgeUsagePresent(judge) {
			return publicFeedSchemaError("judge_started carries only the judge, the judgment, and its attempt")
		}
		return nil
	}
	failed := judge.GetState() == evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_FAILED
	switch judge.GetState() {
	case evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_MET, evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_NOT_MET:
		if !criterion || judge.Score != nil {
			return publicFeedSchemaError("met and not_met are criterion verdicts without a score")
		}
	case evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_SCORED:
		if criterion || judge.Score == nil || judge.GetScore() < 1 || judge.GetScore() > 5 {
			return publicFeedSchemaError("scored is a quality verdict with a score in 1..5")
		}
	case evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_FAILED:
		if judge.GetFailure() == evalv1.JudgeFailure_JUDGE_FAILURE_UNSPECIFIED || judge.Score != nil || judge.GetRationale() != "" {
			return publicFeedSchemaError("a failed judgment carries its failure code and no verdict")
		}
	default:
		return publicFeedSchemaError("invalid judge_completed state")
	}
	if !failed && (judge.GetFailure() != evalv1.JudgeFailure_JUDGE_FAILURE_UNSPECIFIED || judge.GetTransactionId() == "" || judge.GetProviderAttemptId() == "") {
		return publicFeedSchemaError("a judge verdict carries its attempt and transaction and no failure")
	}
	if _, ok := evalv1.JudgeFailure_name[int32(judge.GetFailure())]; !ok {
		return publicFeedSchemaError("invalid judge failure")
	}
	if !judge.GetUsageReported() && (judge.PromptTokens != nil || judge.CompletionTokens != nil || judge.ThinkingTokens != nil || judge.CacheTokens != nil) {
		return publicFeedSchemaError("judge tokens require usage_reported")
	}
	return nil
}

func judgeUsagePresent(judge *evalv1.PublicLiveJudgeActivity) bool {
	return judge.PromptTokens != nil || judge.CompletionTokens != nil || judge.ThinkingTokens != nil || judge.CacheTokens != nil ||
		judge.LoadDurationNs != nil || judge.PromptEvalDurationNs != nil || judge.GenerationDurationNs != nil ||
		judge.TotalDurationNs != nil || judge.TimeToFirstTokenNs != nil
}

func validateLiveMetricValue(key string, metric *evalv1.PublicLiveMetricValue) error {
	if metric == nil {
		return publicFeedSchemaError(fmt.Sprintf("metric_delta %q is missing", key))
	}
	hasValue, hasReason := metric.Value != nil, metric.GetUnavailableReason() != ""
	if hasValue == hasReason {
		return publicFeedSchemaError(fmt.Sprintf("metric_delta %q requires exactly one value or unavailable_reason", key))
	}
	if hasValue && (math.IsNaN(metric.GetValue()) || math.IsInf(metric.GetValue(), 0)) {
		return publicFeedSchemaError(fmt.Sprintf("metric_delta %q value must be finite", key))
	}
	return nil
}

func validateExplorerEnvelope(fields map[string]json.RawMessage) error {
	schemaVersion, ok := stringField(fields, "schema_version")
	if !ok || !publicFeedViewSchemaVersion(schemaVersion) {
		return publicFeedSchemaError("unsupported schema_version")
	}
	kind, ok := stringField(fields, "kind")
	if !ok || !publicFeedSnapshotKind(kind) {
		return publicFeedSchemaError("unsupported kind")
	}
	datasetID, datasetPresent := stringField(fields, "dataset_id")
	qualityState, qualityPresent := stringField(fields, "quality_state")
	observedAt, observedPresent := stringField(fields, "observed_at")
	if !datasetPresent || datasetID == "" || !qualityPresent || !publicFeedQualityState(qualityState) || !observedPresent || observedAt == "" {
		return publicFeedSchemaError("missing or invalid envelope field")
	}
	if _, err := time.Parse(time.RFC3339Nano, observedAt); err != nil {
		return publicFeedSchemaError("observed_at must be RFC3339")
	}
	if err := validateExplorerReleaseIdentity(fields); err != nil {
		return publicFeedSchemaError(err.Error())
	}
	return nil
}

func validateReleaseIdentity(release string, basis evalv1.PublicReleaseBasis, revision string) error {
	switch basis {
	case evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_UNSPECIFIED:
		if release != "" || revision != "" {
			return schemaError("release provenance requires a basis")
		}
	case evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_RECORDED, evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_ASSERTED:
		if strings.TrimSpace(release) == "" {
			return schemaError("known release basis requires a release")
		}
	case evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_UNKNOWN:
		if release != "" {
			return schemaError("unknown release basis cannot name a release")
		}
	default:
		return schemaError("invalid release basis")
	}
	return nil
}

func validateExplorerReleaseIdentity(fields map[string]json.RawMessage) error {
	release, releaseOK := stringField(fields, "release")
	basis, basisOK := stringField(fields, "release_basis")
	revision, revisionOK := stringField(fields, "source_revision")
	for _, key := range []string{"release", "release_basis", "source_revision"} {
		if raw, present := fields[key]; present {
			var value *string
			if err := json.Unmarshal(raw, &value); err != nil || value == nil {
				return schemaError("invalid " + key)
			}
		}
	}
	if !basisOK && !releaseOK && !revisionOK {
		return nil
	}
	var typedBasis evalv1.PublicReleaseBasis
	switch basis {
	case "recorded":
		typedBasis = evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_RECORDED
	case "asserted":
		typedBasis = evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_ASSERTED
	case "unknown":
		typedBasis = evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_UNKNOWN
	default:
		return schemaError("invalid release basis")
	}
	return validateReleaseIdentity(release, typedBasis, revision)
}

func validatePublicFeedManifest(recordBytes []byte) error {
	var manifest models.PublicProofManifest
	if err := decodeStrict(recordBytes, &manifest); err != nil {
		return publicFeedSchemaError(err.Error())
	}
	if manifest.SchemaVersion == "" || manifest.ProofRootSHA256 == "" || manifest.CampaignID == "" || manifest.CampaignRevision == "" || manifest.VerifiedIndexGenerationHash == "" || manifest.ArtifactCount <= 0 || len(manifest.Artifacts) != manifest.ArtifactCount || manifest.VerifierInstructions == "" || manifest.GeneratedAt.IsZero() || manifest.SigningKeyID == "" || manifest.Signature == "" {
		return publicFeedSchemaError("incomplete proof manifest")
	}
	return nil
}

func validatePublicFeedKeyRevocation(recordBytes []byte) error {
	var record models.PublicKeyRevocationRecord
	if err := decodeStrict(recordBytes, &record); err != nil {
		return publicFeedSchemaError(err.Error())
	}
	if record.SourceID == "" || record.RevokedKeyID == "" || record.RevokedAt.IsZero() || record.NewKeyID == "" || record.RevocationSignature == "" {
		return publicFeedSchemaError("incomplete key revocation")
	}
	return nil
}

func publicFeedViewSchemaVersion(value string) bool {
	switch value {
	case "1.0.0", "1.1.0", "1.2.0", "1.3.0", "1.4.0", "1.5.0", "1.6.0", "1.7.0", "1.8.0", "1.9.0", "1.10.0", constants.ExplorerViewSchemaVersion:
		return true
	default:
		return false
	}
}

func publicFeedSnapshotKind(value string) bool {
	switch value {
	case "catalog_snapshot", "model_summary", "suite_summary", "evaluation_summary", "assignment_result", "methodology_snapshot":
		return true
	default:
		return false
	}
}

func publicFeedEventKind(value string) bool {
	switch value {
	case "evaluation_queued", "evaluation_started", "assignment_started", "stage_updated", "assignment_completed", "assignment_failed", "metric_updated", "evaluation_completed", "evaluation_failed", "evaluation_stopped", "judge_started", "judge_completed":
		return true
	default:
		return false
	}
}

func publicFeedQualityState(value string) bool {
	switch value {
	case "exploratory_verified", "exploratory_partial", "legacy_unverified", "live_in_progress", "terminal_failed", "dead_evidence", "not_evaluated", "unavailable":
		return true
	default:
		return false
	}
}

func publicFeedLifecycleStatus(value string) bool {
	switch value {
	case "queued", "running", "completed", "failed", "stopped":
		return true
	default:
		return false
	}
}

func publicFeedRole(value string) bool {
	switch value {
	case "primary", "assistant", "lite":
		return true
	default:
		return false
	}
}

func publicFeedSchemaError(detail string) error {
	return fmt.Errorf("public feed record schema: %s: %w", detail, constants.ErrPublicFeedRecordSchemaInvalid)
}

func schemaError(detail string) error {
	return fmt.Errorf("public assignment schema: %s: %w", detail, constants.ErrEvidenceSchemaMismatch)
}
