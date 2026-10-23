// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestValidatePublicAssignmentRecordAcceptsHistoricalAndEnrichedRecords(t *testing.T) {
	t.Parallel()
	projection := validDisclosureProjection(t)

	tests := []struct {
		name       string
		version    string
		extensions PublicAssignmentRecordExtensions
	}{
		{name: "historical", version: campaignProjectionEnvelopeHistoricalVersion},
		{
			name:    "enriched",
			version: campaignProjectionEnvelopeEnrichedVersion,
			extensions: PublicAssignmentRecordExtensions{
				ResourceSummary: &PublicResourceSummary{
					LatencyMS: PublicResourceMetric{Value: float64Pointer(0)},
					Retries: PublicResourceMetric{
						UnavailableReason: evalv1.PublicUnavailableReason_PUBLIC_UNAVAILABLE_REASON_NO_SCORED_CALLS,
					},
				},
			},
		},
		{
			// Grader detail is published verbatim, like model text, so a
			// detail quoting a path or an over-long line is still accepted.
			name:    "grade detail",
			version: campaignProjectionEnvelopeEnrichedVersion,
			extensions: PublicAssignmentRecordExtensions{
				BenchmarkObservations: &PublicBenchmarkObservations{
					GradeSummaries: []PublicGradeSummary{{
						CriterionID: "criterion",
						Detail:      "expected ../checkout.log to be cited; " + strings.Repeat("x", 600),
					}},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := assignmentEnvelope(t, tt.version, projection, tt.extensions)
			require.NoError(t, ValidatePublicAssignmentRecord("", body))
		})
	}
}

func TestValidatePublicAssignmentRecordRejectsMalformedAssignmentGrammar(t *testing.T) {
	t.Parallel()
	projection := validDisclosureProjection(t)
	tests := []struct {
		name       string
		version    string
		extensions PublicAssignmentRecordExtensions
		mutate     func(map[string]any)
		want       error
	}{
		{
			name:    "unknown projection field",
			version: campaignProjectionEnvelopeEnrichedVersion,
			mutate: func(record map[string]any) {
				record["private_detail"] = "should reject"
			},
			want: constants.ErrEvidenceSchemaMismatch,
		},
		{
			name:    "historical enriched extension",
			version: campaignProjectionEnvelopeHistoricalVersion,
			extensions: PublicAssignmentRecordExtensions{
				ResourceSummary: &PublicResourceSummary{LatencyMS: PublicResourceMetric{Value: float64Pointer(1)}},
			},
			want: constants.ErrEvidenceSchemaMismatch,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := assignmentEnvelope(t, tt.version, projection, tt.extensions)
			var envelope CampaignProjectionEnvelope
			require.NoError(t, json.Unmarshal(body, &envelope))
			var record map[string]any
			require.NoError(t, json.Unmarshal(envelope.Record, &record))
			if tt.mutate != nil {
				tt.mutate(record)
			}
			recordBody, err := json.Marshal(record)
			require.NoError(t, err)
			envelope.Record = recordBody
			body, err = json.Marshal(envelope)
			require.NoError(t, err)
			err = ValidatePublicAssignmentRecord("", body)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

func TestValidatePublicAssignmentRecordLeavesOtherPublicProjectionsToGeneralValidation(t *testing.T) {
	t.Parallel()
	require.NoError(t, ValidatePublicAssignmentRecord("", []byte(`{"kind":"model_summary","detail":"not an assignment envelope"}`)))
	assert.Error(t, ValidatePublicAssignmentRecord("", []byte(`{"schema_version":"1.1.0","message_type":"UnknownCampaignRecord","record":{}}`)))
}

func validDisclosureProjection(t *testing.T) *evalv1.PublicAssignmentResultProjection {
	t.Helper()
	digest := sha256.Sum256([]byte("result"))
	projection := &evalv1.PublicAssignmentResultProjection{
		AssignmentId:     "assignment-1",
		RunId:            "run-1",
		ScenarioId:       "scenario-1",
		ScenarioCategory: evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE,
		Lane:             evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus:  evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		SummaryStatus:    evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		ResultDigest:     hex.EncodeToString(digest[:]),
	}
	return projection
}

func assignmentEnvelope(t *testing.T, version string, projection *evalv1.PublicAssignmentResultProjection, extensions PublicAssignmentRecordExtensions) []byte {
	t.Helper()
	projectionBody, err := evalv1.MarshalCanonical(projection)
	require.NoError(t, err)
	fields := make(map[string]json.RawMessage)
	require.NoError(t, json.Unmarshal(projectionBody, &fields))
	extensionBody, err := json.Marshal(extensions)
	require.NoError(t, err)
	extensionFields := make(map[string]json.RawMessage)
	require.NoError(t, json.Unmarshal(extensionBody, &extensionFields))
	for key, value := range extensionFields {
		fields[key] = value
	}
	recordBody, err := json.Marshal(fields)
	require.NoError(t, err)
	body, err := json.Marshal(CampaignProjectionEnvelope{
		SchemaVersion:  version,
		MessageType:    publicMessageTypeAssignmentResult,
		IdempotencyKey: "run-1:assignment-1:result",
		Record:         recordBody,
	})
	require.NoError(t, err)
	return body
}

func float64Pointer(value float64) *float64 {
	return &value
}
