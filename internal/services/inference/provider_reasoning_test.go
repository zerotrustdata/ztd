// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestInferenceHandler_ProviderReasoningRequiresIssuingHostedClassAndAssistantRole(t *testing.T) {
	t.Parallel()
	const item = `{"signature":"AKIAIOSFODNN7EXAMPLE","thinking":"","type":"thinking"}`
	for _, tc := range []struct {
		name, provider, issuer, item string
		role                         operatorv1.InferenceMessageRole
		wantErr                      error
	}{
		{"anthropic assistant", constants.InferenceProviderAnthropic, constants.InferenceProviderAnthropic, item, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, nil},
		{"openai assistant", constants.InferenceProviderOpenAI, constants.InferenceProviderOpenAI, `{"encrypted_content":"opaque","id":"rs_1","summary":[],"type":"reasoning"}`, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, nil},
		{"different provider", constants.InferenceProviderOpenAI, constants.InferenceProviderAnthropic, item, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, constants.ErrInferenceMessageInvalid},
		{"local request", "", constants.InferenceProviderAnthropic, item, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, constants.ErrInferenceMessageInvalid},
		{"local issuer", constants.InferenceProviderOllama, constants.InferenceProviderOllama, item, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, constants.ErrInferenceMessageInvalid},
		{"unknown issuer", "unknown", "unknown", item, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, constants.ErrInferenceMessageInvalid},
		{"user role", constants.InferenceProviderAnthropic, constants.InferenceProviderAnthropic, item, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, constants.ErrInferenceMessageInvalid},
		{"system role", constants.InferenceProviderAnthropic, constants.InferenceProviderAnthropic, item, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, constants.ErrInferenceMessageInvalid},
		{"tool role", constants.InferenceProviderAnthropic, constants.InferenceProviderAnthropic, item, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL, constants.ErrInferenceMessageInvalid},
		{"noncanonical JSON", constants.InferenceProviderAnthropic, constants.InferenceProviderAnthropic, " " + item, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, constants.ErrInferenceJSONNonCanonical},
		{"nonobject JSON", constants.InferenceProviderAnthropic, constants.InferenceProviderAnthropic, `[]`, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, constants.ErrInferenceJSONInvalid},
		{"empty JSON", constants.InferenceProviderAnthropic, constants.InferenceProviderAnthropic, "", operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, constants.ErrInferenceJSONInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newHostedTestHandler(t, &stubBackend{}, nil)
			reasoning := &operatorv1.InferenceProviderReasoning{ProviderClass: tc.issuer, ItemJson: tc.item}
			req := models.InferenceRequestPayload{
				RequestSchemaVersion: constants.InferenceRequestSchemaVersion,
				ProviderAttemptID:    "attempt-reasoning",
				ProviderClass:        tc.provider,
				Messages: []*operatorv1.InferenceMessage{{Role: tc.role, Parts: []*operatorv1.InferenceMessagePart{{
					Part: &operatorv1.InferenceMessagePart_ProviderReasoning{ProviderReasoning: reasoning},
				}}}},
			}
			err := handler.normalizeInferenceInput(testutil.Context(t), &req)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.item, reasoning.GetItemJson(), "provider state survives input scrubbing byte for byte")
		})
	}
}

func TestInferenceHandler_ProviderReasoningBounds(t *testing.T) {
	t.Parallel()
	item := `{"data":"` + strings.Repeat("x", maxInferenceProviderReasoningBytes-len(`{"data":"","type":"redacted_thinking"}`)) + `","type":"redacted_thinking"}`
	reasoning := &operatorv1.InferenceProviderReasoning{ProviderClass: constants.InferenceProviderAnthropic, ItemJson: item}
	req := &operatorv1.InferenceRequested{
		RequestSchemaVersion: constants.InferenceRequestSchemaVersion,
		Role:                 operatorv1.ModelRole_MODEL_ROLE_PRIMARY,
		ProviderClass:        constants.InferenceProviderAnthropic,
		Messages: []*operatorv1.InferenceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, Parts: []*operatorv1.InferenceMessagePart{{
			Part: &operatorv1.InferenceMessagePart_ProviderReasoning{ProviderReasoning: reasoning},
		}}}},
	}
	require.Len(t, item, maxInferenceProviderReasoningBytes)
	require.NoError(t, validateInferenceRequestBounds(req), "an item exactly at the limit is allowed")
	local := &stubBackend{}
	handler := newHostedTestHandler(t, local, nil)
	reasoning.ItemJson += " "
	_, err := handler.ExecuteInference(testutil.Context(t), &testCommandMessage{payload: mustMarshalInferenceRequested(t, req)})
	require.ErrorIs(t, err, constants.ErrInferenceRequestTooLarge)
	reasoning.ItemJson = item
	for range 16 {
		req.Messages = append(req.Messages, req.Messages[0])
	}
	require.NoError(t, validateInferenceRequestBounds(req), "each individual item is within bounds")
	_, err = handler.ExecuteInference(testutil.Context(t), &testCommandMessage{payload: mustMarshalInferenceRequested(t, req)})
	require.ErrorIs(t, err, constants.ErrInferenceRequestTooLarge, "the whole conversation still has a payload bound")
	assert.Zero(t, local.calls)
}

func TestScrubResponseParts_PreservesProviderReasoningAndBindsItsDigest(t *testing.T) {
	t.Parallel()
	ctx := testutil.Context(t)
	reasoning := &operatorv1.InferenceProviderReasoning{
		ProviderClass: constants.InferenceProviderAnthropic,
		ItemJson:      `{"signature":"AKIAIOSFODNN7EXAMPLE","thinking":"","type":"thinking"}`,
	}
	parts := []*operatorv1.InferenceResponsePart{
		segmenterTextPart(segmenterSecretEmail),
		{Part: &operatorv1.InferenceResponsePart_ProviderReasoning{ProviderReasoning: reasoning}},
	}
	scrubbed, err := scrubResponseParts(ctx, parts, mustNewScrubbingSvc(t))
	require.NoError(t, err)
	assert.NotContains(t, scrubbed[0].GetText(), segmenterSecretEmail)
	require.True(t, proto.Equal(reasoning, scrubbed[1].GetProviderReasoning()))
	assert.NotSame(t, reasoning, scrubbed[1].GetProviderReasoning())
	result := terminalResult(t, scrubbed)
	digest, err := models.ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	scrubbed[1].GetProviderReasoning().ItemJson = `{"data":"changed","type":"redacted_thinking"}`
	changed, err := models.ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	assert.NotEqual(t, digest, changed, "opaque state is covered by the signed result digest")
	assert.Equal(t, segmenterSecretEmail, parts[0].GetText(), "scrubbing never changes the provider result")
	assert.NotEqual(t, reasoning.GetItemJson(), scrubbed[1].GetProviderReasoning().GetItemJson())
}

func TestStreamSegmenter_ProviderReasoningKeepsInterleavedOrder(t *testing.T) {
	t.Parallel()
	reasoning := func(item string) *operatorv1.InferenceResponsePart {
		return &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_ProviderReasoning{ProviderReasoning: &operatorv1.InferenceProviderReasoning{
			ProviderClass: constants.InferenceProviderOpenAI, ItemJson: item,
		}}}
	}
	first := reasoning(`{"encrypted_content":"AKIAIOSFODNN7EXAMPLE","id":"rs_1","summary":[],"type":"reasoning"}`)
	last := reasoning(`{"encrypted_content":"opaque","id":"rs_2","summary":[],"type":"reasoning"}`)
	call := &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "call-1", Name: "inspect", ArgumentsJson: `{}`}}}
	events := [][]*operatorv1.InferenceResponsePart{{first}, {segmenterTextPart("before ")}, {call}, {last}, {segmenterTextPart("after")}}
	published, terminal := segmenterRun(t, mustNewScrubbingSvc(t), events)
	require.Len(t, terminal, 5)
	for i, event := range events {
		assert.True(t, proto.Equal(event[0], terminal[i]), "part %d retains its kind, bytes and position", i)
	}
	require.NoError(t, models.ReconcileInferenceProgress(published, terminalResult(t, terminal)))
	stream, err := newStreamSegmenter(mustNewScrubbingSvc(t), nil, "attempt")
	require.NoError(t, err)
	_, err = stream.accept(testutil.Context(t), []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_ProviderReasoning{}}})
	require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
}
