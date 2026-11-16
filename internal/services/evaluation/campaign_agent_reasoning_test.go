// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestAgentAssistantMessage_PreservesProviderOrderAndExecutedArguments(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{constants.InferenceProviderAnthropic, constants.InferenceProviderOpenAI} {
		t.Run(provider, func(t *testing.T) {
			first := &operatorv1.InferenceProviderReasoning{ProviderClass: provider, ItemJson: `{"opaque":"first"}`}
			last := &operatorv1.InferenceProviderReasoning{ProviderClass: provider, ItemJson: `{"opaque":"last"}`}
			result := &operatorv1.InferenceResult{Parts: []*operatorv1.InferenceResponsePart{
				{Part: &operatorv1.InferenceResponsePart_ProviderReasoning{ProviderReasoning: first}},
				{Part: &operatorv1.InferenceResponsePart_Text{Text: " before "}},
				{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "call-1", Name: "inspect", ArgumentsJson: `{"path":"relative"}`, ProviderSignature: "signature-1"}}},
				{Part: &operatorv1.InferenceResponsePart_Text{Text: " between "}},
				{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "call-2", Name: "inspect", ArgumentsJson: `{"path":"other"}`, ProviderSignature: "signature-2"}}},
				{Part: &operatorv1.InferenceResponsePart_ProviderReasoning{ProviderReasoning: last}},
				{Part: &operatorv1.InferenceResponsePart_Text{Text: ""}},
			}}
			original := proto.Clone(result)
			text, _, calls := agentResponseParts(result)
			args := []string{`{"path":"/workspace/relative"}`, `{"path":"/workspace/other"}`}
			assistant := agentAssistantMessage(result, text, calls, args)
			require.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT, assistant.GetRole())
			require.Len(t, assistant.GetParts(), len(result.GetParts()))
			assert.True(t, proto.Equal(first, assistant.Parts[0].GetProviderReasoning()))
			assert.Equal(t, " before ", assistant.Parts[1].GetText())
			assert.Equal(t, " between ", assistant.Parts[3].GetText())
			assert.True(t, proto.Equal(last, assistant.Parts[5].GetProviderReasoning()))
			assert.IsType(t, &operatorv1.InferenceMessagePart_Text{}, assistant.Parts[6].GetPart(), "empty text keeps its position")
			for i, partIndex := range []int{2, 4} {
				call := assistant.Parts[partIndex].GetToolCall()
				assert.Equal(t, args[i], call.GetArgumentsJson())
				assert.Equal(t, calls[i].GetCallId(), call.GetCallId())
				assert.Equal(t, calls[i].GetName(), call.GetName())
				assert.Equal(t, calls[i].GetProviderSignature(), call.GetProviderSignature())
			}
			assistant.Parts[0].GetProviderReasoning().ItemJson = `{"opaque":"changed"}`
			assistant.Parts[2].GetToolCall().ArgumentsJson = `{}`
			assert.True(t, proto.Equal(original, result), "continuation cannot mutate the signed result")
		})
	}
}

func TestAgentAssistantMessage_WithoutProviderReasoningKeepsTextThenCalls(t *testing.T) {
	t.Parallel()
	for _, textPart := range []string{" after ", ""} {
		result := &operatorv1.InferenceResult{Parts: []*operatorv1.InferenceResponsePart{
			{Part: &operatorv1.InferenceResponsePart_Thinking{Thinking: "readable thinking is not replayed"}},
			{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "call-1", Name: "inspect", ArgumentsJson: `{}`, ProviderSignature: "gemini-signature"}}},
			{Part: &operatorv1.InferenceResponsePart_Text{Text: textPart}},
		}}
		text, _, calls := agentResponseParts(result)
		assistant := agentAssistantMessage(result, text, calls, []string{`{"path":"executed"}`})
		want := &operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT}
		if text != "" {
			want.Parts = append(want.Parts, &operatorv1.InferenceMessagePart{Part: &operatorv1.InferenceMessagePart_Text{Text: "after"}})
		}
		want.Parts = append(want.Parts, &operatorv1.InferenceMessagePart{Part: &operatorv1.InferenceMessagePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId: "call-1", Name: "inspect", ArgumentsJson: `{"path":"executed"}`, ProviderSignature: "gemini-signature",
		}}})
		assert.True(t, proto.Equal(want, assistant))
	}
}

func TestAgentTraceConversation_OmitsReasoningButSealedResultRetainsIt(t *testing.T) {
	t.Parallel()
	const opaque = `{"encrypted_content":"opaque-state","id":"rs_1","summary":[],"type":"reasoning"}`
	trace := &AgentTrace{
		Input: &AgentTraceInput{Messages: []AgentTraceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String(), Text: "prompt"}}},
		ModelCalls: []AgentModelCall{{
			Purpose: agentModelCallPurposeTurn, LoopTurn: 1, ProviderAttemptID: "provider-1", TransactionID: "tx-1", Text: "answer",
			InferenceResult: &AgentTraceInferenceResult{InferenceResult: &operatorv1.InferenceResult{Parts: []*operatorv1.InferenceResponsePart{
				{Part: &operatorv1.InferenceResponsePart_ProviderReasoning{ProviderReasoning: &operatorv1.InferenceProviderReasoning{ProviderClass: constants.InferenceProviderOpenAI, ItemJson: opaque}}},
				{Part: &operatorv1.InferenceResponsePart_Text{Text: "answer"}},
			}}},
		}},
	}
	encoded, err := json.Marshal(trace.ModelCalls[0].InferenceResult)
	require.NoError(t, err)
	var decoded AgentTraceInferenceResult
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, opaque, decoded.GetParts()[0].GetProviderReasoning().GetItemJson()) //nolint:testifylint // Provider state requires exact bytes, not JSON equivalence.
	held, err := trace.Conversation()
	require.NoError(t, err)
	require.Len(t, held, 2)
	for _, message := range held {
		replayed, err := message.inferenceMessage()
		require.NoError(t, err)
		for _, part := range replayed.GetParts() {
			assert.Nil(t, part.GetProviderReasoning(), "held context can change persona or prompt")
		}
	}
	assert.Equal(t, "answer", held[1].Text)
}
