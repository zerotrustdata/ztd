// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func v7EvidenceTrace(t *testing.T) (*AgentTrace, oracletest.Signer) {
	t.Helper()
	data := newEvidenceLedger(t, oracletest.NewSigner(t))
	inference := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
	before := data.executed("tx-before", string(constants.ActionTypeFsRead))
	after := data.executed("tx-after", string(constants.ActionTypeFsRead))
	receipt := inference.inference("tx-model")
	trace := evidenceTrace(inference.runtime(), data.runtime())
	trace.SchemaVersion = agentTraceSchemaVersion
	trace.CampaignID = "campaign-1"
	trace.DesignatedRole = "primary"
	trace.ServedModelTag = "model-a:latest"
	trace.ModelDigest = strings.Repeat("a", 64)
	trace.ModelRegistryDigest = strings.Repeat("b", 64)
	trace.FinalOutput = "answer"
	trace.Bracket = bracketOf(before, after)

	result := &operatorv1.InferenceResult{
		PromptTokens:         2,
		CompletionTokens:     3,
		TotalTokens:          5,
		FinishReason:         "stop",
		Model:                trace.ServedModelTag,
		Parts:                []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: "answer"}}, {Part: &operatorv1.InferenceResponsePart_Thinking{Thinking: "reasoning"}}},
		UsageReported:        true,
		ProviderAttemptId:    "attempt-model",
		RequestedModel:       trace.ServedModelTag,
		RequestedModelDigest: trace.ModelDigest,
		ServedModelDigest:    trace.ModelDigest,
		CampaignId:           trace.CampaignID,
		RunId:                trace.RunID,
		AssignmentId:         trace.AssignmentID,
		EvaluationAttemptId:  trace.EvaluationAttemptID,
		ScenarioId:           trace.ScenarioID,
		ModelRegistryDigest:  trace.ModelRegistryDigest,
		LoadState:            operatorv1.InferenceLoadState_INFERENCE_LOAD_STATE_UNSPECIFIED,
		RetryClassification:  operatorv1.InferenceRetryClassification_INFERENCE_RETRY_CLASSIFICATION_UNSPECIFIED,
	}
	digest, err := models.ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	receipt.ResultSummary = digest
	inference.signer.Resign(t, receipt)
	trace.ModelCalls = []AgentModelCall{{
		ProviderAttemptID: "attempt-model",
		TransactionID:     receipt.GetTransactionId(),
		Purpose:           agentModelCallPurposeTurn,
		LoopTurn:          1,
		ModelVariantID:    "variant-a",
		ModelRole:         operatorv1.ModelRole_MODEL_ROLE_PRIMARY.String(),
		ServedModelTag:    trace.ServedModelTag,
		ModelDigest:       trace.ModelDigest,
		ToolsDeclared:     []string{},
		ResultDigest:      digest,
		UsageReported:     true,
		PromptTokens:      2,
		CompletionTokens:  3,
		FinishReason:      "stop",
		LoadState:         operatorLoadStateToEvaluation(result.GetLoadState()).String(),
		Text:              "answer",
		Thinking:          "reasoning",
		Receipt:           &AgentTraceReceipt{ActionReceipt: receipt},
		InferenceResult:   &AgentTraceInferenceResult{InferenceResult: result},
	}}
	return trace, inference.signer
}

func v7EvidenceTraceWithToolCalls(t *testing.T) *AgentTrace {
	t.Helper()
	trace, _ := v7EvidenceTrace(t)
	legacy, _ := validEvidenceTrace(t)
	trace.Data = legacy.Data
	trace.Bracket = legacy.Bracket
	trace.ToolCalls = legacy.ToolCalls
	return trace
}

func resealV7Trace(t *testing.T, trace *AgentTrace) {
	t.Helper()
	_, err := SealAgentTrace(trace)
	require.NoError(t, err)
	require.NoError(t, ValidateAgentTraceDigest(trace))
}

func TestAgentTraceV7_CanonicalInferenceResultRoundTrips(t *testing.T) {
	trace, _ := v7EvidenceTrace(t)
	body, err := SealAgentTrace(trace)
	require.NoError(t, err)
	decoded := &AgentTrace{}
	require.NoError(t, json.Unmarshal(body, decoded))
	require.NoError(t, ValidateAgentTraceDigest(decoded))
	require.NoError(t, VerifyAgentTraceEvidence(decoded))
	assert.Equal(t, agentTraceSchemaVersion, decoded.SchemaVersion)
	assert.Equal(t, trace.ModelCalls[0].ModelVariantID, decoded.ModelCalls[0].ModelVariantID)
	assert.Equal(t, trace.ModelCalls[0].ModelRole, decoded.ModelCalls[0].ModelRole)
	assert.True(t, decoded.ModelCalls[0].InferenceResult.InferenceResult.ProtoReflect().IsValid())

	again, err := SealAgentTrace(decoded)
	require.NoError(t, err)
	assert.Equal(t, body, again)
}

func TestVerifyAgentTraceEvidenceV7_RejectsResealedModelIdentityAndResultTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *AgentTrace, oracletest.Signer)
	}{
		{
			name: "per-call variant differs from designated trace identity",
			mutate: func(t *testing.T, trace *AgentTrace, signer oracletest.Signer) {
				t.Helper()
				call := &trace.ModelCalls[0]
				call.ServedModelTag = "model-b:latest"
				call.ModelDigest = strings.Repeat("c", 64)
				result := call.InferenceResult.InferenceResult
				result.Model = call.ServedModelTag
				result.RequestedModel = call.ServedModelTag
				result.RequestedModelDigest = call.ModelDigest
				result.ServedModelDigest = call.ModelDigest
				digest, err := models.ComputeInferenceResultDigest(result)
				require.NoError(t, err)
				result.ResultDigest = digest
				call.ResultDigest = digest
				call.Receipt.ResultSummary = digest
				signer.Resign(t, call.Receipt.ActionReceipt)
			},
		},
		{
			name: "signed result from another variant",
			mutate: func(t *testing.T, trace *AgentTrace, signer oracletest.Signer) {
				t.Helper()
				call := &trace.ModelCalls[0]
				result := call.InferenceResult.InferenceResult
				result.Model = "model-b:latest"
				result.RequestedModel = result.Model
				result.RequestedModelDigest = strings.Repeat("c", 64)
				result.ServedModelDigest = result.RequestedModelDigest
				digest, err := models.ComputeInferenceResultDigest(result)
				require.NoError(t, err)
				result.ResultDigest = digest
				call.Receipt.ResultSummary = digest
				signer.Resign(t, call.Receipt.ActionReceipt)
			},
		},
		{
			name: "role swapped",
			mutate: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
				trace.ModelCalls[0].ModelRole = operatorv1.ModelRole_MODEL_ROLE_ASSISTANT.String()
			},
		},
		{
			name: "missing result",
			mutate: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
				trace.ModelCalls[0].InferenceResult = nil
			},
		},
		{
			name: "forged final diagnosis",
			mutate: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
				trace.FinalOutput = "forged diagnosis"
			},
		},
		{
			name: "forged transcript",
			mutate: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
				trace.ModelCalls[0].Text = "forged answer"
			},
		},
		{
			name: "result changed after dispatch",
			mutate: func(t *testing.T, trace *AgentTrace, signer oracletest.Signer) {
				t.Helper()
				call := &trace.ModelCalls[0]
				result := call.InferenceResult.InferenceResult
				result.Parts[0].Part = &operatorv1.InferenceResponsePart_Text{Text: "other answer"}
				digest, err := models.ComputeInferenceResultDigest(result)
				require.NoError(t, err)
				result.ResultDigest = digest
				call.ResultDigest = digest
				call.Receipt.ResultSummary = digest
				signer.Resign(t, call.Receipt.ActionReceipt)
			},
		},
		{
			name: "provider attempt swapped",
			mutate: func(_ *testing.T, trace *AgentTrace, _ oracletest.Signer) {
				trace.ModelCalls[0].InferenceResult.ProviderAttemptId = "attempt-other"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trace, signer := v7EvidenceTrace(t)
			test.mutate(t, trace, signer)
			resealV7Trace(t, trace)

			err := VerifyAgentTraceEvidence(trace)
			require.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
			assert.Error(t, err)
		})
	}
}

func TestValidateAgentTraceDigest_PreservesV6AndEarlierSchemaVersions(t *testing.T) {
	for _, version := range []string{agentTraceSchemaVersionV7, agentTraceSchemaVersionV6, agentTraceSchemaVersionV5, agentTraceSchemaVersionV4} {
		t.Run(version, func(t *testing.T) {
			trace, _ := v7EvidenceTrace(t)
			trace.SchemaVersion = version
			trace.ModelCalls[0].ModelVariantID = ""
			trace.ModelCalls[0].ModelRole = ""
			trace.ModelCalls[0].ServedModelTag = ""
			trace.ModelCalls[0].ModelDigest = ""
			trace.ModelCalls[0].InferenceResult = nil
			trace.EvidenceDigest = ""
			digest, err := ComputeAgentTraceDigest(trace)
			require.NoError(t, err)
			trace.EvidenceDigest = digest
			require.NoError(t, ValidateAgentTraceDigest(trace))
		})
	}
	trace, _ := v7EvidenceTrace(t)
	trace.SchemaVersion = agentTraceSchemaVersionV3
	trace.Input = nil
	trace.EvidenceDigest = ""
	digest, err := ComputeAgentTraceDigest(trace)
	require.NoError(t, err)
	trace.EvidenceDigest = digest
	require.NoError(t, ValidateAgentTraceDigest(trace))
}

// relabelV7 restamps trace as a v7 history trace, which SealAgentTrace refuses
// to produce.
func relabelV7(t *testing.T, trace *AgentTrace) {
	t.Helper()
	trace.SchemaVersion = agentTraceSchemaVersionV7
	trace.EvidenceDigest = ""
	digest, err := ComputeAgentTraceDigest(trace)
	require.NoError(t, err)
	trace.EvidenceDigest = digest
}

func TestVerifyAgentTraceEvidence_PreservesV7HistoryTraces(t *testing.T) {
	trace, _ := v7EvidenceTrace(t)
	relabelV7(t, trace)

	require.NoError(t, ValidateAgentTraceDigest(trace))
	require.NoError(t, VerifyAgentTraceEvidence(trace))
	assert.True(t, trace.CarriesModelIdentity())

	trace.ModelCalls[0].ModelVariantID = ""
	relabelV7(t, trace)
	assert.Error(t, VerifyAgentTraceEvidence(trace), "v7 history still binds per-call model identity")
}

func TestValidateAgentTraceDigest_RejectsApprovedArgumentsBeforeV8(t *testing.T) {
	trace := v7EvidenceTraceWithToolCalls(t)
	require.NotEmpty(t, trace.ToolCalls)
	trace.ToolCalls[0].ApprovedArgumentsJSON = `{"args":["app/service.conf"],"command":"cat"}`
	resealV7Trace(t, trace)

	relabelV7(t, trace)
	assert.ErrorIs(t, ValidateAgentTraceDigest(trace), constants.ErrEvidenceArtifactMalformed)
}

// v1ShellTextSteps are Tribunal persona steps in the shell-text shape that
// v1.2.0 sealed into agent-trace.v7 before typed run_shell_command arguments.
const v1ShellTextSteps = `[{"index":2,"role":"AGENT_ROLE_CANDIDATE_GENERATOR","candidate":{"command":"cat app/service.conf"}},` +
	`{"index":5,"role":"AGENT_ROLE_REVIEWER","review":{"verdict":"AGENT_REVIEW_VERDICT_APPROVE","command":"cat app/service.conf"}}]`

func TestVerifyAgentTraceEvidence_DecodesV7ShellTextTribunalSteps(t *testing.T) {
	trace, _ := v7EvidenceTrace(t)
	require.NoError(t, json.Unmarshal([]byte(v1ShellTextSteps), &trace.Steps))
	relabelV7(t, trace)
	body, err := json.Marshal(trace)
	require.NoError(t, err)
	require.Contains(t, string(body), `"candidate":{"command":"cat app/service.conf"}`)

	var decoded AgentTrace
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.NoError(t, ValidateAgentTraceDigest(&decoded))
	require.NoError(t, VerifyAgentTraceEvidence(&decoded))
	assert.Equal(t, "cat app/service.conf", decoded.Steps[1].GetReview().GetCommand())
}

func TestValidateAgentTraceDigest_RejectsShellTextStepsInV8(t *testing.T) {
	trace, _ := v7EvidenceTrace(t)
	require.NoError(t, json.Unmarshal([]byte(v1ShellTextSteps), &trace.Steps))
	trace.EvidenceDigest = ""
	digest, err := ComputeAgentTraceDigest(trace)
	require.NoError(t, err)
	trace.EvidenceDigest = digest

	require.ErrorIs(t, ValidateAgentTraceDigest(trace), constants.ErrEvidenceArtifactMalformed)
	_, err = SealAgentTrace(trace)
	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}

func TestAgentTraceInferenceResult_RejectsUnknownFields(t *testing.T) {
	var result AgentTraceInferenceResult
	err := result.UnmarshalJSON([]byte(`{"resultDigest":"d","unknownField":"x"}`))
	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}
