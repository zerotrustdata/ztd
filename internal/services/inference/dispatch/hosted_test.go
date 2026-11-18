// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package dispatch

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/inference/model_provenance"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	hostedDispatchModel   = "gemini-3.6-flash"
	hostedDispatchVersion = "3.6-flash-07-2026"
)

type recordingHostedRecorder struct {
	attempts []model_provenance.HostedAttempt
	err      error
}

func (r *recordingHostedRecorder) RecordHostedAttempt(_ context.Context, _ string, attempt model_provenance.HostedAttempt) error {
	r.attempts = append(r.attempts, attempt)
	return r.err
}

func hostedIdentityDigest(t *testing.T) string {
	t.Helper()
	origin, _ := constants.HostedInferenceProviderOrigin(constants.InferenceProviderGemini)
	digest, err := models.ComputeHostedModelIdentityDigest(constants.InferenceProviderGemini, origin, hostedDispatchModel, hostedDispatchVersion)
	require.NoError(t, err)
	return digest
}

func hostedDispatchRequest(t *testing.T, pinned bool) DispatchInferenceRequest {
	t.Helper()
	req := baseRequest()
	req.ProviderClass = constants.InferenceProviderGemini
	req.Model = hostedDispatchModel
	if pinned {
		configureCampaignRequest(t, &req)
		req.ModelDigest = hostedIdentityDigest(t)
		req.ModelRegistry = []*operatorv1.InferenceModelVariant{{Model: req.Model, Digest: req.ModelDigest}}
		registryDigest, err := models.ComputeInferenceModelRegistryDigest(req.CampaignID, req.ModelRegistry)
		require.NoError(t, err)
		req.ModelRegistryDigest = registryDigest
	}
	return req
}

func hostedDispatchResult(t *testing.T, req DispatchInferenceRequest, attest bool) *CommandDispatchResult {
	t.Helper()
	origin, _ := constants.HostedInferenceProviderOrigin(constants.InferenceProviderGemini)
	requestHash := models.SHA256Hex([]byte("hosted request"))
	result := &operatorv1.InferenceResult{
		Parts:                 []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: "ok"}}},
		Model:                 req.Model,
		RequestedModel:        req.Model,
		NormalizedRequestHash: requestHash,
		OutputHash:            models.SHA256Hex([]byte("output")),
		ResultDigest:          models.SHA256Hex([]byte("result")),
	}
	if attest {
		result.HostedEgress = &operatorv1.InferenceHostedEgressAttestation{
			ProviderOrigin:        origin,
			ModelVersion:          req.Model,
			Provenance:            constants.HostedModelProvenance,
			NormalizedRequestHash: requestHash,
			ExplicitConsent:       true,
			ProviderClass:         constants.InferenceProviderGemini,
			RequestedModel:        req.Model,
			ProviderModelVersion:  hostedDispatchVersion,
			ModelIdentityDigest:   hostedIdentityDigest(t),
			ProviderResponseId:    "r1",
			RawResponseDigest:     models.SHA256Hex([]byte("raw")),
			RawResponseSizeBytes:  3,
		}
	}
	stampResultIdentity(result, req)
	payload, err := proto.Marshal(result)
	require.NoError(t, err)
	return &CommandDispatchResult{
		TransactionID: "tx-hosted",
		ResultPayload: payload,
		Receipt:       &operatorv1.EventReceipt{TransactionId: "tx-hosted", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED},
	}
}

func hostedOp(sessionID string, classes ...string) *operatorv1.OperatorDocument {
	op := capableOp(sessionID)
	op.RuntimeConfig.InferenceHostedProviders = classes
	return op
}

func TestDispatchInference_HostedAttemptRecordsHostedProvenanceWithoutWitnesses(t *testing.T) {
	t.Parallel()
	req := hostedDispatchRequest(t, true)
	dispatcher := &stubCommandDispatcher{result: hostedDispatchResult(t, req, true)}
	svc := NewDispatchService(dispatcher, &stubOperatorLister{ops: []*operatorv1.OperatorDocument{hostedOp("sess-inf", constants.InferenceProviderGemini)}}, testLogger())
	observer := &stubObservationNotifier{}
	provenance := &recordingProvenanceNotifier{}
	recorder := &recordingHostedRecorder{}
	svc.SetProviderObservationNotifier(observer)
	svc.SetProvenanceObservationNotifier(provenance)
	svc.SetHostedProvenanceRecorder(recorder)

	result, err := svc.DispatchInference(t.Context(), req)
	require.NoError(t, err)
	assert.Empty(t, result.ObservationReceipts)

	assert.Zero(t, observer.beginCalls+observer.finalizeCalls, "no observer window opens for a hosted provider")
	assert.Empty(t, provenance.commands, "no storage provenance window opens for a hosted provider")
	require.Len(t, recorder.attempts, 1)
	attempt := recorder.attempts[0]
	assert.Equal(t, "op-sess-inf", attempt.InferenceOperatorID)
	assert.Equal(t, "tx-hosted", attempt.InferenceTransactionID)
	assert.Equal(t, req.ProviderAttemptID, attempt.ProviderAttemptID)
	assert.Equal(t, req.ModelDigest, attempt.ExpectedModelDigest)
	assert.NotNil(t, attempt.Result.GetHostedEgress())

	sent := &operatorv1.InferenceRequested{}
	require.NoError(t, proto.Unmarshal(dispatcher.lastReq.Payload, sent))
	assert.Equal(t, constants.InferenceProviderGemini, sent.GetProviderClass(), "the provider class rides in the governed envelope")
}

func TestDispatchInference_HostedProvenanceRecording(t *testing.T) {
	t.Parallel()
	t.Run("an unpinned attempt records no window", func(t *testing.T) {
		req := hostedDispatchRequest(t, false)
		recorder := &recordingHostedRecorder{}
		svc := NewDispatchService(&stubCommandDispatcher{result: hostedDispatchResult(t, req, true)}, &stubOperatorLister{ops: []*operatorv1.OperatorDocument{hostedOp("sess-inf", constants.InferenceProviderGemini)}}, testLogger())
		svc.SetHostedProvenanceRecorder(recorder)
		_, err := svc.DispatchInference(t.Context(), req)
		require.NoError(t, err)
		assert.Empty(t, recorder.attempts)
	})
	t.Run("a campaign attempt whose window cannot be recorded fails closed", func(t *testing.T) {
		req := hostedDispatchRequest(t, true)
		recorder := &recordingHostedRecorder{err: constants.ErrChecksumMismatch}
		svc := NewDispatchService(&stubCommandDispatcher{result: hostedDispatchResult(t, req, true)}, &stubOperatorLister{ops: []*operatorv1.OperatorDocument{hostedOp("sess-inf", constants.InferenceProviderGemini)}}, testLogger())
		svc.SetHostedProvenanceRecorder(recorder)
		_, err := svc.DispatchInference(t.Context(), req)
		require.ErrorIs(t, err, constants.ErrChecksumMismatch)
	})
	t.Run("a hosted result without its attestation fails closed", func(t *testing.T) {
		req := hostedDispatchRequest(t, true)
		recorder := &recordingHostedRecorder{}
		svc := NewDispatchService(&stubCommandDispatcher{result: hostedDispatchResult(t, req, false)}, &stubOperatorLister{ops: []*operatorv1.OperatorDocument{hostedOp("sess-inf", constants.InferenceProviderGemini)}}, testLogger())
		svc.SetHostedProvenanceRecorder(recorder)
		_, err := svc.DispatchInference(t.Context(), req)
		require.ErrorIs(t, err, constants.ErrInferenceIdentityMismatch)
		assert.Empty(t, recorder.attempts)
	})
	t.Run("a local result carrying an attestation fails closed", func(t *testing.T) {
		req := hostedDispatchRequest(t, false)
		local := req
		local.ProviderClass = ""
		svc := NewDispatchService(&stubCommandDispatcher{result: hostedDispatchResult(t, req, true)}, &stubOperatorLister{ops: []*operatorv1.OperatorDocument{capableOp("sess-inf")}}, testLogger())
		_, err := svc.DispatchInference(t.Context(), local)
		require.ErrorIs(t, err, constants.ErrInferenceIdentityMismatch)
	})
}

func TestDispatchInference_HostedRoutesOnlyToAnOperatorServingTheClass(t *testing.T) {
	t.Parallel()
	req := hostedDispatchRequest(t, false)
	local := capableOp("sess-local")
	other := hostedOp("sess-openai", constants.InferenceProviderOpenAI)
	serving := hostedOp("sess-gemini", constants.InferenceProviderGemini)

	svc := NewDispatchService(&stubCommandDispatcher{}, &stubOperatorLister{ops: []*operatorv1.OperatorDocument{local, other, serving}}, testLogger())
	selected, err := svc.resolveInferenceOperator(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, "sess-gemini", selected.GetOperatorSessionId())

	svc = NewDispatchService(&stubCommandDispatcher{}, &stubOperatorLister{ops: []*operatorv1.OperatorDocument{local, other}}, testLogger())
	_, err = svc.resolveInferenceOperator(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrInferenceOperatorNotFound)

	req.TargetOperatorSessionID = "sess-local"
	_, err = svc.resolveInferenceOperator(t.Context(), req)
	require.ErrorIs(t, err, constants.ErrInferenceOperatorNotCapable)
}

func TestDispatchInference_HostedProvenanceFailureDoesNotLogContent(t *testing.T) {
	t.Parallel()
	const sensitive = "private-runtime-path"
	req := hostedDispatchRequest(t, false)
	req.ModelDigest = hostedIdentityDigest(t)
	logger, sink := testutil.NewBufferLogger(t)
	svc := NewDispatchService(&stubCommandDispatcher{result: hostedDispatchResult(t, req, true)}, &stubOperatorLister{ops: []*operatorv1.OperatorDocument{hostedOp("sess-inf", constants.InferenceProviderGemini)}}, logger)
	svc.SetHostedProvenanceRecorder(&recordingHostedRecorder{err: fmt.Errorf("persist %s: %w", sensitive, constants.ErrEnforcePermissions)})

	_, err := svc.DispatchInference(testutil.Context(t), req)
	require.NoError(t, err, "provenance recording is best effort outside a campaign")
	assert.Contains(t, sink.String(), "hosted model provenance window not recorded")
	assert.Contains(t, sink.String(), "provider_attempt_id="+req.ProviderAttemptID)
	assert.NotContains(t, sink.String(), sensitive)
}

func TestDispatchInference_HostedRequestValidation(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		mutate func(*DispatchInferenceRequest)
		want   error
	}{
		"an unknown class": {func(r *DispatchInferenceRequest) { r.ProviderClass = "acme" }, constants.ErrInferenceHostedNotAllowed},
		"a local class":    {func(r *DispatchInferenceRequest) { r.ProviderClass = constants.InferenceProviderOllama }, constants.ErrInferenceHostedNotAllowed},
		"a decision":       {func(r *DispatchInferenceRequest) { r.Decision = &operatorv1.InferenceDecisionRequest{} }, constants.ErrInferenceDecisionUnsupported},
		"no model":         {func(r *DispatchInferenceRequest) { r.Model = "" }, constants.ErrInferenceModelRefInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			req := hostedDispatchRequest(t, false)
			tc.mutate(&req)
			dispatcher := &stubCommandDispatcher{}
			svc := NewDispatchService(dispatcher, &stubOperatorLister{ops: []*operatorv1.OperatorDocument{hostedOp("sess-inf", constants.InferenceProviderGemini)}}, testLogger())
			_, err := svc.DispatchInference(t.Context(), req)
			require.ErrorIs(t, err, tc.want)
			assert.Zero(t, dispatcher.calls)
		})
	}
}
