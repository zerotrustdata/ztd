// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package compliancecmd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/inference/model_provenance"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestBuildCampaignReportSource_HostedInferenceRequiresOnlySealedHostedWindow(t *testing.T) {
	ctx := context.Background()
	fileSvc, _ := cmdtest.NewCmdTestEnv(t)
	store := evaluation.NewStore(fileSvc)

	now := time.Unix(1_700_000_000, 0).UTC()
	providerClass := constants.InferenceProviderGemini
	origin, ok := constants.HostedInferenceProviderOrigin(providerClass)
	require.True(t, ok)

	hostedModelTag := "gemini-3.6-flash"
	providerModelVersion := "gemini-3.6-flash"
	hostedIdentityDigest, err := models.ComputeHostedModelIdentityDigest(providerClass, origin, hostedModelTag, providerModelVersion)
	require.NoError(t, err)

	hostedVariant := &evalv1.ModelVariant{
		VariantId:      "gemini-3-6-flash",
		ServedModelTag: hostedModelTag,
		ModelDigest:    hostedIdentityDigest,
		ProviderClass:  providerClass,
		ModelFormat:    constants.HostedModelFormat,
	}
	req := cmdtest.EvaluationTestCampaignInitRequest(t)
	inventory, err := evaluation.MaterializeModelRegistry(req.CampaignID, []*evalv1.ModelVariant{hostedVariant})
	require.NoError(t, err)
	req.Inventory = inventory
	req.Judge = hostedVariant

	controller := evaluation.NewCampaignController(store, nil, func() time.Time { return now }, func(prefix string) string { return prefix + "-1" })
	_, err = controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)

	assignments, err := store.ListAssignments(ctx, req.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	assignment := assignments[0]

	const hostedAttemptID = "attempt-hosted-1"
	hostedInferenceResult := &operatorv1.InferenceResult{
		ProviderAttemptId: hostedAttemptID,
		RequestedModel:    hostedModelTag,
		ResultDigest:      models.SHA256Hex([]byte("hosted-result")),
		HostedEgress: &operatorv1.InferenceHostedEgressAttestation{
			ProviderClass:        providerClass,
			ProviderOrigin:       origin,
			RequestedModel:       hostedModelTag,
			ProviderModelVersion: providerModelVersion,
			ModelVersion:         providerModelVersion,
			ModelIdentityDigest:  hostedIdentityDigest,
			ProviderResponseId:   "resp-hosted-1",
			RawResponseDigest:    models.SHA256Hex([]byte("raw-hosted-response")),
		},
	}
	hostedWindow, err := model_provenance.HostedAttestationWindow(model_provenance.HostedAttempt{
		InferenceOperatorID:    "inf-operator-1",
		InferenceTransactionID: "tx-hosted-1",
		ProviderAttemptID:      hostedAttemptID,
		ExpectedModelDigest:    hostedIdentityDigest,
		StartedAtUnixMs:        now.Add(-time.Second).UnixMilli(),
		CompletedAtUnixMs:      now.UnixMilli(),
		Result:                 hostedInferenceResult,
	}, now.UnixMilli())
	require.NoError(t, err)
	require.NoError(t, store.SaveModelProvenanceWindow(ctx, req.RunID, hostedWindow))

	hostedRecord := &evalv1.ModelInferenceRecord{
		InferenceRecordId: "inf-hosted-1",
		ProviderAttemptId: hostedAttemptID,
		ModelVariant:      hostedVariant,
		ResultDigest:      hostedInferenceResult.GetResultDigest(),
	}
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   evaluation.CampaignSchemaVersion,
		AssignmentId:    assignment.GetAssignmentId(),
		RunId:           req.RunID,
		CampaignId:      req.CampaignID,
		Lane:            assignment.GetLane(),
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		CompletedAt:     timestamppb.New(now),
		ModelInferences: []*evalv1.ModelInferenceRecord{hostedRecord},
	}
	result.ResultDigest, err = evaluation.ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	require.NoError(t, evaluation.ValidateAssignmentResultDigest(result))
	require.NoError(t, store.SaveAssignmentResult(ctx, result))

	admission := &compliancev1.AssessmentSourceAdmission{
		AdmissionId:               "campaign-hosted",
		RunId:                     req.RunID,
		SourceKind:                constants.EvaluationSourceKindCampaign,
		VerifierRef:               &compliancev1.VersionedReference{Id: constants.CampaignVerifierID, Version: constants.CampaignVerifierVersion},
		ProviderObservationPolicy: compliancev1.AssessmentWitnessPolicy_ASSESSMENT_WITNESS_POLICY_STRICT,
		ModelProvenancePolicy:     compliancev1.AssessmentWitnessPolicy_ASSESSMENT_WITNESS_POLICY_STRICT,
	}

	source, artifacts, err := buildCampaignReportSource(ctx, fileSvc, admission, now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, admission.GetAdmissionId(), source.AdmissionID)
	assert.NotEmpty(t, artifacts)

	saveInferences := func(records ...*evalv1.ModelInferenceRecord) {
		t.Helper()
		result.ModelInferences = records
		result.ResultDigest = ""
		result.ResultDigest, err = evaluation.ComputeAssignmentResultDigest(result)
		require.NoError(t, err)
		require.NoError(t, store.SaveAssignmentResult(ctx, result))
	}

	// A hosted attempt that returned no result (a throttled call) carries no
	// signed attestation, so the report requires no window for it.
	failedHostedRecord := &evalv1.ModelInferenceRecord{
		InferenceRecordId: "inf-hosted-throttled",
		ProviderAttemptId: "attempt-hosted-throttled",
		ModelVariant:      hostedVariant,
	}
	saveInferences(failedHostedRecord, hostedRecord)
	_, _, err = buildCampaignReportSource(ctx, fileSvc, admission, now.Add(time.Minute))
	require.NoError(t, err)

	// A hosted attempt with a result and no sealed window fails closed.
	unsealedHostedRecord := &evalv1.ModelInferenceRecord{
		InferenceRecordId: "inf-hosted-unsealed",
		ProviderAttemptId: "attempt-hosted-unsealed",
		ModelVariant:      hostedVariant,
		ResultDigest:      models.SHA256Hex([]byte("unsealed-hosted-result")),
	}
	saveInferences(failedHostedRecord, hostedRecord, unsealedHostedRecord)
	_, _, err = buildCampaignReportSource(ctx, fileSvc, admission, now.Add(time.Minute))
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
	saveInferences(failedHostedRecord, hostedRecord)

	// Adding a local inference without sealed provider-observation records must
	// fail closed during report source construction.
	localRecord := &evalv1.ModelInferenceRecord{
		InferenceRecordId: "inf-local-1",
		ProviderAttemptId: "attempt-local-1",
		ModelVariant: &evalv1.ModelVariant{
			VariantId:      "qwen3-4b",
			ServedModelTag: "qwen3:4b",
			ModelDigest:    models.SHA256Hex([]byte("qwen3-4b")),
			ProviderClass:  constants.InferenceProviderOllama,
		},
	}
	result.ModelInferences = append(result.ModelInferences, localRecord)
	result.ResultDigest, err = evaluation.ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	require.NoError(t, evaluation.ValidateAssignmentResultDigest(result))
	require.NoError(t, store.SaveAssignmentResult(ctx, result))

	_, _, err = buildCampaignReportSource(ctx, fileSvc, admission, now.Add(time.Minute))
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed)
}
