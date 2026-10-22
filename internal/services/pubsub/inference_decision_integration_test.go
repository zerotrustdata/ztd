// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package pubsub

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type governedDecisionBackend struct {
	stubInferenceBackend
	calledDecision bool
}

func (b *governedDecisionBackend) Decide(_ context.Context, req models.DecisionRequest) (*models.DecisionResponse, error) {
	b.calledDecision = true
	result := &operatorv1.InferenceDecisionResult{Answers: []*operatorv1.InferenceDecisionAnswer{{Name: "safe", Kind: &operatorv1.InferenceDecisionAnswer_Noul{Noul: .99}}}}
	hash, err := models.ComputeDecisionOutputHash(result)
	if err != nil {
		return nil, err
	}
	return &models.DecisionResponse{Model: req.Model, Decision: result, OutputHash: hash, NormalizedRequestHash: models.SHA256Hex([]byte("decision request")), FinishReason: "stop", PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11, UsageReported: true}, nil
}

func TestDecisionInferenceGovernedEventReceiptBindsTypedResult(t *testing.T) {
	svc, _, auditStore, results := newInferenceIntegrationFixture(t)
	cfg := testutil.NewTestConfig(t)
	cfg.Inference.Enabled = true
	cfg.Inference.DecisionModels = []string{"tev1:0.8b"}
	backend := &governedDecisionBackend{}
	logger := testutil.NewTestLogger()
	handler, err := inference.NewInferenceExecutionHandler(backend, cfg, mustNewScrubbingSvc(t, logger), logger)
	require.NoError(t, err)
	svc.inference = handler

	var envelope govpkg.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(buildInferenceEnvelope(t, operatorv1.ModelRole_MODEL_ROLE_PRIMARY, "tev1:0.8b", "read only"), &envelope))
	var request operatorv1.InferenceRequested
	require.NoError(t, proto.Unmarshal(envelope.Payload, &request))
	request.Messages = nil
	request.Decision = &operatorv1.InferenceDecisionRequest{State: "Read the requested inventory", Questions: []*operatorv1.InferenceDecisionQuestion{{Name: "safe", Instructions: "Is this read only?", Kind: &operatorv1.InferenceDecisionQuestion_Noul{Noul: &operatorv1.InferenceNoulQuestion{}}}}}
	envelope.Payload, err = proto.Marshal(&request)
	require.NoError(t, err)
	hash, err := govpkg.GenerateMessageID(&envelope)
	require.NoError(t, err)
	envelope.Id, envelope.TransactionHash = hash, hash
	wire, err := protojson.Marshal(&envelope)
	require.NoError(t, err)

	eventReceipt, err := svc.ProcessEnvelope(t.Context(), wire)
	require.NoError(t, err)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, eventReceipt.Status)
	require.NotEmpty(t, eventReceipt.Signature)
	require.True(t, backend.calledDecision)
	require.False(t, backend.called, "decision must not call chat generation")
	require.Len(t, results.inferenceCompletions, 1)
	result := results.inferenceCompletions[0].Result
	require.NotNil(t, result)
	require.Empty(t, result.Parts)
	require.NoError(t, models.ValidateDecisionCompletion(request.Decision, result))
	digest, err := models.ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	require.Equal(t, digest, result.ResultDigest)
	require.Equal(t, digest, eventReceipt.ResultSummary)
	eventReceipts, err := auditStore.ListEventReceipts(models.AuditScope{}, 10, 0)
	require.NoError(t, err)
	require.Len(t, eventReceipts, 1)
	require.Equal(t, digest, eventReceipts[0].ResultSummary)
}
