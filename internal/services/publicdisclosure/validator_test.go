// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.

package publicdisclosure

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestValidatePublicDisclosureMetricsAndStrings(t *testing.T) {
	tests := []struct {
		name string
		fn   func() error
		want error
	}{
		{name: "observed metric", fn: func() error { return validateMetric(json.RawMessage(`{"value":1}`)) }},
		{name: "unavailable metric", fn: func() error { return validateMetric(json.RawMessage(`{"unavailable_reason":"source_unavailable"}`)) }},
		{name: "missing metric value", fn: func() error { return validateMetric(json.RawMessage(`{}`)) }, want: constants.ErrEvidenceSchemaMismatch},
		{name: "negative metric", fn: func() error { return validateMetric(json.RawMessage(`{"value":-1}`)) }, want: constants.ErrEvidenceArtifactMalformed},
		{name: "unknown unavailable reason", fn: func() error { return validateMetric(json.RawMessage(`{"unavailable_reason":"unknown"}`)) }, want: constants.ErrEvidenceSchemaMismatch},
		{name: "valid strings", fn: func() error { return validateStrings([]byte(`{"a":["safe",{"b":"value"}]}`)) }},
		{name: "spiffe string carried verbatim", fn: func() error { return validateStrings([]byte(`{"a":"spiffe://ztd.local/operator/x"}`)) }},
		{name: "path string carried verbatim", fn: func() error { return validateStrings([]byte(`{"a":"../secret /.ztd/x"}`)) }},
		{name: "structural string bounded", fn: func() error { return validateStrings([]byte(`{"a":"` + strings.Repeat("x", 513) + `"}`)) }, want: constants.ErrEvidenceArtifactTooLarge},
		{name: "unknown allowed field", fn: func() error { return allowed(map[string]json.RawMessage{"unexpected": json.RawMessage(`1`)}, "known") }, want: constants.ErrEvidenceSchemaMismatch},
		{name: "valid benchmark", fn: func() error {
			return validateBenchmark(json.RawMessage(`{"grade_summaries":[{"criterion_id":"c","status":"pass"}],"tool_scorecard":{"tool_selection":{"value":1}},"timing":{"latency":{"value":2}},"gpu":{"power":{"unavailable_reason":"source_unavailable"}},"unavailable_reasons":[]}`))
		}},
		{name: "benchmark grade detail", fn: func() error {
			return validateBenchmark(json.RawMessage(`{"grade_summaries":[{"criterion_id":"c","detail":"cited ../app.log"}],"unavailable_reasons":[]}`))
		}},
		{name: "unknown benchmark grade field", fn: func() error { return validateBenchmark(json.RawMessage(`{"grade_summaries":[{"note":"x"}]}`)) }, want: constants.ErrEvidenceSchemaMismatch},
		{name: "grade detail carried verbatim", fn: func() error {
			return validateStrings([]byte(`{"semantic_grade_summaries":[{"criterion_id":"c","detail":"read /.ztd/x and ../y ` + strings.Repeat("x", 600) + `"}]}`))
		}},
		{name: "grade identifier carried verbatim", fn: func() error {
			return validateStrings([]byte(`{"grade_summaries":[{"criterion_id":"../c","detail":"ok"}]}`))
		}},
		{name: "grade detail bounded", fn: func() error {
			return validateStrings([]byte(`{"grade_summaries":[{"detail":"` + strings.Repeat("x", 256*1024+1) + `"}]}`))
		}, want: constants.ErrEvidenceArtifactTooLarge},
		{name: "valid resource", fn: func() error { return validateResource(json.RawMessage(`{"latency_ms":{"value":2}}`)) }},
		{name: "unknown resource field", fn: func() error { return validateResource(json.RawMessage(`{"private":{"value":2}}`)) }, want: constants.ErrEvidenceSchemaMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if tt.want == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

func TestValidateAssignmentAndFeedRecordsRejectMalformedInput(t *testing.T) {
	tests := []struct {
		name string
		fn   func() error
		want error
	}{
		{name: "empty assignment", fn: func() error { return ValidateAssignmentRecord("", nil) }, want: constants.ErrEvidenceSchemaMismatch},
		{name: "unknown message type", fn: func() error { return ValidateAssignmentRecord("", []byte(`{"message_type":"Unknown"}`)) }, want: constants.ErrEvidenceSchemaMismatch},
		{name: "invalid feed type", fn: func() error { return ValidatePublicFeedRecord(models.PublicFeedRecordType("invalid"), []byte(`{}`)) }, want: constants.ErrPublicFeedRecordTypeInvalid},
		{name: "invalid projection envelope", fn: func() error {
			return ValidatePublicFeedRecord(models.PublicFeedRecordTypeProjection, []byte(`{"schema_version":"9.0.0","kind":"evaluation_summary","dataset_id":"ds","quality_state":"unavailable","observed_at":"2026-01-01T00:00:00Z"}`))
		}, want: constants.ErrPublicFeedRecordSchemaInvalid},
		{name: "invalid event envelope", fn: func() error {
			return ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, []byte(`{"schema_version":"1.5.0","kind":"assignment_completed","dataset_id":"ds","quality_state":"live_in_progress","observed_at":"2026-01-01T00:00:00Z"}`))
		}, want: constants.ErrPublicFeedRecordSchemaInvalid},
		{name: "malformed manifest", fn: func() error { return ValidatePublicFeedRecord(models.PublicFeedRecordTypeProofManifest, []byte(`{}`)) }, want: constants.ErrPublicFeedRecordSchemaInvalid},
		{name: "malformed revocation", fn: func() error { return ValidatePublicFeedRecord(models.PublicFeedRecordTypeKeyRevocation, []byte(`{}`)) }, want: constants.ErrPublicFeedRecordSchemaInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

func TestValidatePublicDisclosureResultsAndBindings(t *testing.T) {
	validBinding := &evalv1.PublicEvidenceBinding{Sha256: strings.Repeat("a", 64), SchemaRef: "schema@1.0.0", Kind: "proof"}
	require.NoError(t, validateBindings([]*evalv1.PublicEvidenceBinding{validBinding}))
	require.ErrorIs(t, validateBindings([]*evalv1.PublicEvidenceBinding{{Sha256: "bad"}}), constants.ErrEvidenceArtifactMalformed)
	require.ErrorIs(t, validateBindings([]*evalv1.PublicEvidenceBinding{validBinding, validBinding}), constants.ErrEvidenceArtifactMalformed)
	require.NoError(t, validateResult("1.0.0", json.RawMessage(`{"assignment_id":"assignment-1","run_id":"run-1","scenario_id":"scenario-1","result_digest":"`+strings.Repeat("b", 64)+`"}`)))
	assert.ErrorIs(t, validateResult("1.0.0", json.RawMessage(`{"assignment_id":"assignment-1"}`)), constants.ErrEvidenceSchemaMismatch)
}

func TestValidateAssignmentRecordAcceptsCurrentSchemas(t *testing.T) {
	lifecycle := []byte(`{"schema_version":"1.0.0","message_type":"PublicAssignmentLifecycleRecord","idempotency_key":"run-1:assignment-1:lifecycle","record":{"assignment_id":"assignment-1","run_id":"run-1","scenario_id":"scenario-1"}}`)
	require.NoError(t, ValidateAssignmentRecord("1.0.0", lifecycle))
	result := []byte(`{"schema_version":"1.1.0","message_type":"PublicAssignmentResultProjection","idempotency_key":"run-1:assignment-1:result","record":{"assignment_id":"assignment-1","run_id":"run-1","scenario_id":"scenario-1","result_digest":"` + strings.Repeat("a", 64) + `","benchmark_observations":{"grade_summaries":[]},"resource_summary":{"latency_ms":{"value":1}}}}`)
	require.NoError(t, ValidateAssignmentRecord("1.1.0", result))
}

func TestValidateAssignmentRecordPreservesArchivedGradeFieldTypes(t *testing.T) {
	// Archived grade records predate a single typed contract for these fields.
	// The validator closes the field-name set but must retain their raw JSON
	// values rather than imposing string-only types.
	record := []byte(`{"schema_version":"1.1.0","message_type":"PublicAssignmentResultProjection","idempotency_key":"run-1:assignment-1:result","record":{"assignment_id":"assignment-1","run_id":"run-1","scenario_id":"scenario-1","benchmark_observations":{"grade_summaries":[{"criterion_id":"c","status":"pass","method":{"name":"manual","revision":2},"judge_variant_id":["judge-a",3]}],"unavailable_reasons":[]}}}`)
	require.NoError(t, ValidateAssignmentRecord("1.1.0", record))
}

func TestValidateRoleTranscriptsAcceptsApprovedArguments(t *testing.T) {
	raw := json.RawMessage(`[{"role":"lite","tool_calls":[{"tool_name":"run_shell_command","arguments_json":"{\"command\":\"cat app/service.conf\"}","approved_arguments_json":"{\"args\":[\"app/service.conf\"],\"command\":\"cat\"}","success":true}]}]`)
	require.NoError(t, ValidateRoleTranscripts(raw))
}

func TestValidateAssignmentRecordTrajectoryFieldsRequireEnvelope120(t *testing.T) {
	record := func(version, extra string) []byte {
		return []byte(`{"schema_version":"` + version + `","message_type":"PublicAssignmentResultProjection","idempotency_key":"run-1:assignment-1:result","record":{"assignment_id":"assignment-1","run_id":"run-1","scenario_id":"scenario-1","result_digest":"` + strings.Repeat("a", 64) + `"` + extra + `}}`)
	}
	tests := []struct {
		name    string
		version string
		extra   string
		wantErr bool
	}{
		{name: "1.2.0 accepts trajectory outcome", version: "1.2.0", extra: `,"trajectory_outcome":"EVALUATION_TRAJECTORY_OUTCOME_IGNORED_GUIDANCE","guided_retry_count":1,"failure_reason":"The model made no tool call.","tools_declared":["recursive_grep_search"]`},
		{name: "1.2.0 accepts a record without trajectory fields", version: "1.2.0"},
		{name: "1.1.0 rejects trajectory outcome", version: "1.1.0", extra: `,"trajectory_outcome":"EVALUATION_TRAJECTORY_OUTCOME_NO_TOOL_CALL"`, wantErr: true},
		{name: "1.1.0 rejects failure reason", version: "1.1.0", extra: `,"failure_reason":"The model made no tool call."`, wantErr: true},
		{name: "1.1.0 rejects tools declared", version: "1.1.0", extra: `,"tools_declared":["file_read_on_operator"]`, wantErr: true},
		{name: "1.1.0 rejects tool call guidance fields", version: "1.1.0", extra: `,"activity_summary":{"tool_calls":{"availability":"PUBLIC_ACTIVITY_AVAILABILITY_OBSERVED","records":[{"tool_label":"recursive_grep_search","guidance_shown":true}]}}`, wantErr: true},
		{name: "1.1.0 still accepts a record without trajectory fields", version: "1.1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAssignmentRecord(tt.version, record(tt.version, tt.extra))
			if tt.wantErr {
				assert.ErrorIs(t, err, constants.ErrEvidenceSchemaMismatch)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidatePublicDisclosureEvents(t *testing.T) {
	event := []byte(`{"schema_version":"1.5.0","kind":"assignment_completed","dataset_id":"ds","quality_state":"live_in_progress","observed_at":"2026-01-01T00:00:00Z","event_id":"event-1","run_id":"run-1","lifecycle_status":"completed","completed":1,"total":1}`)
	require.NoError(t, ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, event))
	require.ErrorIs(t, ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, []byte(`{"schema_version":"1.5.0","kind":"assignment_completed","dataset_id":"ds","quality_state":"live_in_progress","observed_at":"bad","event_id":"event-1","run_id":"run-1","lifecycle_status":"completed","completed":1,"total":1}`)), constants.ErrPublicFeedRecordSchemaInvalid)
	assert.ErrorIs(t, ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, []byte(`{"schema_version":"1.5.0","kind":"assignment_completed","dataset_id":"ds","quality_state":"live_in_progress","observed_at":"2026-01-01T00:00:00Z","event_id":"event-1","run_id":"run-1","lifecycle_status":"completed","completed":2,"total":1}`)), constants.ErrPublicFeedRecordSchemaInvalid)
}

func TestValidatePublicLiveEventIsTypedAndFailsClosed(t *testing.T) {
	const head = `{"schema_version":"1.6.0","kind":"metric_updated","dataset_id":"ds","quality_state":"live_in_progress","observed_at":"2026-10-02T12:00:00Z","event_id":"event-1","run_id":"run-1","lifecycle_status":"running"`
	tests := []struct {
		name string
		body string
		ok   bool
	}{
		{name: "recorded release on the wire", ok: true, body: head + `,"release":"v2.3.0","release_basis":"PUBLIC_RELEASE_BASIS_RECORDED","completed":1,"total":2,"metric_delta":{"pass":{"value":1}}}`},
		{name: "zero progress is a value", ok: true, body: head + `,"completed":0,"total":1}`},
		{name: "measured zero metric is a value", ok: true, body: head + `,"completed":0,"total":1,"metric_delta":{"pass":{"value":0}}}`},
		{name: "unavailable metric names its reason", ok: true, body: head + `,"completed":0,"total":1,"metric_delta":{"pass":{"unavailable_reason":"no_scored_calls"}}}`},
		{name: "missing completed is not zero", body: head + `,"total":1}`},
		{name: "missing total is not zero", body: head + `,"completed":0}`},
		{name: "view-form release basis", body: head + `,"release":"v2.3.0","release_basis":"recorded","completed":0,"total":1}`},
		{name: "client-stamped feed sequence is not a wire field", body: head + `,"completed":0,"total":1,"feed_sequence":3}`},
		{name: "unknown field", body: head + `,"completed":0,"total":1,"served_model_tag":"qwen"}`},
		{name: "role outside the vocabulary", body: head + `,"role":"sage","completed":0,"total":1}`},
		{name: "metric with a value and a reason", body: head + `,"completed":0,"total":1,"metric_delta":{"pass":{"value":1,"unavailable_reason":"no_scored_calls"}}}`},
		{name: "metric with neither", body: head + `,"completed":0,"total":1,"metric_delta":{"pass":{}}}`},
		{name: "metric value is not finite", body: head + `,"completed":0,"total":1,"metric_delta":{"pass":{"value":"NaN"}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, []byte(tt.body))
			if tt.ok {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, constants.ErrPublicFeedRecordSchemaInvalid)
		})
	}
}

func TestValidatePublicLiveJudgeEventsKeepTheirShape(t *testing.T) {
	const row = `"schema_version":"1.11.0","dataset_id":"ds","quality_state":"live_in_progress","observed_at":"2026-10-30T12:00:00Z","event_id":"run-1:a-1:judge:judge-criterion-1:event","run_id":"run-1","assignment_id":"a-1","task_id":"tool-select","variant_id":"gemma4-e2b","role":"primary","lifecycle_status":"running","completed":0,"total":4`
	const identity = `"judge_variant_id":"qwen3-5-4b","served_model_tag":"qwen3.5:4b","model_digest":"abc","rubric_version":"criterion-v2","index":1,"count":2,"provider_attempt_id":"judge-criterion-1"`
	event := func(kind, judge string) string {
		return `{` + row + `,"kind":"` + kind + `","judge":{` + identity + judge + `}}`
	}
	criterion := `,"subject":"JUDGE_SUBJECT_CRITERION","criterion_id":"refusal"`
	reply := `,"subject":"JUDGE_SUBJECT_FINAL_REPLY"`
	verdict := `,"transaction_id":"tx-1","usage_reported":true,"prompt_tokens":812,"completion_tokens":40,"total_duration_ns":"1200000000","executed_at_unix_ms":"1793361600000","dispatch_elapsed_ns":"1300000000"`
	tests := []struct {
		name string
		body string
		ok   bool
	}{
		{name: "judging a criterion", ok: true, body: event("judge_started", criterion+`,"state":"JUDGE_ACTIVITY_STATE_JUDGING"`)},
		{name: "criterion met with signed usage", ok: true, body: event("judge_completed", criterion+`,"state":"JUDGE_ACTIVITY_STATE_MET","rationale":"refuses"`+verdict)},
		{name: "final reply scored", ok: true, body: event("judge_completed", reply+`,"state":"JUDGE_ACTIVITY_STATE_SCORED","score":4,"rationale":"clear"`+verdict)},
		{name: "failed judgment names its code", ok: true, body: event("judge_completed", reply+`,"state":"JUDGE_ACTIVITY_STATE_FAILED","failure":"JUDGE_FAILURE_VERDICT_INVALID","transaction_id":"tx-1"`)},
		{name: "unsent request has no attempt", ok: true, body: `{` + row + `,"kind":"judge_completed","judge":{"judge_variant_id":"j","served_model_tag":"t","model_digest":"d","rubric_version":"r","index":1,"count":1` + reply + `,"state":"JUDGE_ACTIVITY_STATE_FAILED","failure":"JUDGE_FAILURE_REQUEST_INVALID"}}`},
		{name: "judge on another kind", body: `{` + row + `,"kind":"stage_updated","judge":{` + identity + reply + `,"state":"JUDGE_ACTIVITY_STATE_JUDGING"}}`},
		{name: "judge kind without judge", body: `{` + row + `,"kind":"judge_started"}`},
		{name: "metric on a judge event", body: strings.Replace(event("judge_started", criterion+`,"state":"JUDGE_ACTIVITY_STATE_JUDGING"`), `"total":4`, `"total":4,"metric_delta":{"latency_ms":{"value":1}}`, 1)},
		{name: "stage label on a judge event", body: strings.Replace(event("judge_started", criterion+`,"state":"JUDGE_ACTIVITY_STATE_JUDGING"`), `"total":4`, `"total":4,"stage_label":"judge"`, 1)},
		{name: "judge event without the subject variant", body: strings.Replace(event("judge_started", criterion+`,"state":"JUDGE_ACTIVITY_STATE_JUDGING"`), `"variant_id":"gemma4-e2b",`, ``, 1)},
		{name: "started with a verdict", body: event("judge_started", criterion+`,"state":"JUDGE_ACTIVITY_STATE_MET"`)},
		{name: "started with usage", body: event("judge_started", criterion+`,"state":"JUDGE_ACTIVITY_STATE_JUDGING","total_duration_ns":"1"`)},
		{name: "completed still judging", body: event("judge_completed", criterion+`,"state":"JUDGE_ACTIVITY_STATE_JUDGING"`+verdict)},
		{name: "scored criterion", body: event("judge_completed", criterion+`,"state":"JUDGE_ACTIVITY_STATE_SCORED","score":4`+verdict)},
		{name: "met quality subject", body: event("judge_completed", reply+`,"state":"JUDGE_ACTIVITY_STATE_MET"`+verdict)},
		{name: "score outside 1..5", body: event("judge_completed", reply+`,"state":"JUDGE_ACTIVITY_STATE_SCORED","score":6`+verdict)},
		{name: "failed without a code", body: event("judge_completed", reply+`,"state":"JUDGE_ACTIVITY_STATE_FAILED"`)},
		{name: "verdict with a failure code", body: event("judge_completed", reply+`,"state":"JUDGE_ACTIVITY_STATE_SCORED","score":4,"failure":"JUDGE_FAILURE_DISPATCH_FAILED"`+verdict)},
		{name: "verdict without a transaction", body: event("judge_completed", reply+`,"state":"JUDGE_ACTIVITY_STATE_SCORED","score":4`)},
		{name: "tokens without usage_reported", body: event("judge_completed", reply+`,"state":"JUDGE_ACTIVITY_STATE_SCORED","score":4,"transaction_id":"tx-1","prompt_tokens":3`)},
		{name: "criterion_id on a quality subject", body: event("judge_started", reply+`,"criterion_id":"refusal","state":"JUDGE_ACTIVITY_STATE_JUDGING"`)},
		{name: "index past count", body: strings.Replace(event("judge_started", criterion+`,"state":"JUDGE_ACTIVITY_STATE_JUDGING"`), `"index":1`, `"index":3`, 1)},
		{name: "long rationale is verbatim evidence", ok: true, body: event("judge_completed", reply+`,"state":"JUDGE_ACTIVITY_STATE_SCORED","score":4,"rationale":"`+strings.Repeat("x", 4096)+`"`+verdict)},
		{name: "unknown judge field", body: event("judge_started", criterion+`,"state":"JUDGE_ACTIVITY_STATE_JUDGING","prompt":"secret"`)},
		{name: "negative duration", body: event("judge_completed", reply+`,"state":"JUDGE_ACTIVITY_STATE_SCORED","score":4,"transaction_id":"tx-1","total_duration_ns":"-1"`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, []byte(tt.body))
			if tt.ok {
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, constants.ErrPublicFeedRecordSchemaInvalid)
		})
	}
}

func TestValidatePublicProofManifestAndKeyRevocationRecords(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	manifest, err := json.Marshal(models.PublicProofManifest{SchemaVersion: "1.0.0", ProofRootSHA256: strings.Repeat("a", 64), CampaignID: "campaign-1", CampaignRevision: "revision-1", VerifiedIndexGenerationHash: strings.Repeat("b", 64), ArtifactCount: 1, Artifacts: []models.PublicProofCatalogEntry{{ArtifactID: "artifact-1"}}, VerifierInstructions: "offline", GeneratedAt: now, SigningKeyID: "key-1", Signature: "signature"})
	require.NoError(t, err)
	require.NoError(t, ValidatePublicFeedRecord(models.PublicFeedRecordTypeProofManifest, manifest))
	revocation, err := json.Marshal(models.PublicKeyRevocationRecord{SourceID: "source-1", RevokedKeyID: "old-key", RevokedAt: now, NewKeyID: "new-key", RevocationSignature: "signature"})
	require.NoError(t, err)
	require.NoError(t, ValidatePublicFeedRecord(models.PublicFeedRecordTypeKeyRevocation, revocation))
}

func TestValidatePublicDisclosureEnvelopeAndJSONDecoding(t *testing.T) {
	projection := []byte(`{"schema_version":"1.5.0","kind":"evaluation_summary","dataset_id":"ds","quality_state":"unavailable","observed_at":"2026-01-01T00:00:00Z"}`)
	require.NoError(t, ValidatePublicFeedRecord(models.PublicFeedRecordTypeProjection, projection))
	fields, err := decodeObject(projection)
	require.NoError(t, err)
	assert.Equal(t, `"evaluation_summary"`, string(fields["kind"]))
	require.Error(t, func() error { _, decodeErr := decodeObject([]byte(`null`)); return decodeErr }())
	assert.Error(t, func() error { var value map[string]any; return decodeStrict([]byte(`{} {}`), &value) }())
}

func TestPublicFeedVocabularyAcceptsKnownValues(t *testing.T) {
	assert.True(t, toolDimension("tool_selection"))
	assert.False(t, toolDimension("unknown"))
	assert.True(t, unavailableReason("no_scored_calls"))
	assert.False(t, unavailableReason("unknown"))
	assert.True(t, bounded("value", 5))
	assert.False(t, bounded("", 5))
	assert.False(t, bounded("a\nb", 5))
	assert.True(t, publicFeedViewSchemaVersion("1.5.0"))
	assert.True(t, publicFeedViewSchemaVersion("1.7.0"))
	assert.True(t, publicFeedViewSchemaVersion("1.8.0"))
	assert.True(t, publicFeedViewSchemaVersion("1.9.0"))
	assert.True(t, publicFeedViewSchemaVersion("1.10.0"))
	assert.True(t, publicFeedViewSchemaVersion(constants.ExplorerViewSchemaVersion))
	assert.True(t, publicFeedEventKind("judge_started"))
	assert.True(t, publicFeedEventKind("judge_completed"))
	assert.False(t, publicFeedViewSchemaVersion("9.0.0"))
	assert.True(t, publicFeedSnapshotKind("evaluation_summary"))
	assert.False(t, publicFeedSnapshotKind("unknown"))
	assert.True(t, publicFeedEventKind("assignment_completed"))
	assert.False(t, publicFeedEventKind("unknown"))
	assert.True(t, publicFeedQualityState("unavailable"))
	assert.False(t, publicFeedQualityState("unknown"))
	assert.True(t, publicFeedLifecycleStatus("completed"))
	assert.False(t, publicFeedLifecycleStatus("unknown"))
	assert.True(t, publicFeedRole("primary"))
	assert.False(t, publicFeedRole("unknown"))
	assert.Error(t, validateStrings([]byte(strings.Repeat("x", maxStringBytes+1))))
}
