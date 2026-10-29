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
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// splitInlineThink feeds content chunks through rewrite the way decodeChatStream
// does, marking the last chunk as the terminal event.
func splitInlineThink(chunks ...string) (thinking, text string) {
	var splitter inlineThinkSplitter
	for i, chunk := range chunks {
		message := ollamaChatMessage{Content: chunk}
		splitter.rewrite(&message, i == len(chunks)-1)
		thinking += message.Thinking
		text += message.Content
	}
	return thinking, text
}

func TestInlineThinkSplitter_TerminatedBlockInOneChunk(t *testing.T) {
	thinking, text := splitInlineThink("<think>plan the answer</think>answer")
	assert.Equal(t, "plan the answer", thinking)
	assert.Equal(t, "answer", text)
}

func TestInlineThinkSplitter_TagsSplitAcrossChunks(t *testing.T) {
	thinking, text := splitInlineThink("<thi", "nk>pla", "n</th", "ink>ans", "wer")
	assert.Equal(t, "plan", thinking)
	assert.Equal(t, "answer", text)
}

func TestInlineThinkSplitter_StripsWhitespaceAfterCloseTagAcrossChunks(t *testing.T) {
	thinking, text := splitInlineThink("<think>a</think>", "\n", "\n", "answer")
	assert.Equal(t, "a", thinking)
	assert.Equal(t, "answer", text)
}

func TestInlineThinkSplitter_UnterminatedBlockIsAllThinking(t *testing.T) {
	thinking, text := splitInlineThink("<think>reasoning", " that never ends</th")
	assert.Equal(t, "reasoning that never ends</th", thinking)
	assert.Empty(t, text)
}

func TestInlineThinkSplitter_NoTagPassesThroughAsText(t *testing.T) {
	thinking, text := splitInlineThink("hello ", "world")
	assert.Empty(t, thinking)
	assert.Equal(t, "hello world", text)
}

func TestInlineThinkSplitter_UndecidedPrefixThatResolvesToTextIsNotDropped(t *testing.T) {
	thinking, text := splitInlineThink("<th", "e cat")
	assert.Empty(t, thinking)
	assert.Equal(t, "<the cat", text)
}

func TestInlineThinkSplitter_TagNotAtStartIsText(t *testing.T) {
	thinking, text := splitInlineThink("answer <think>x</think>")
	assert.Empty(t, thinking)
	assert.Equal(t, "answer <think>x</think>", text)
}

func TestInlineThinkSplitter_LeadingWhitespaceBeforeTagIsSplit(t *testing.T) {
	thinking, text := splitInlineThink("  \n", "<think>r</think>ans")
	assert.Equal(t, "r", thinking)
	assert.Equal(t, "ans", text)
}

func TestInlineThinkSplitter_LeadingWhitespaceWithoutTagIsKept(t *testing.T) {
	thinking, text := splitInlineThink(" ", "hi")
	assert.Empty(t, thinking)
	assert.Equal(t, " hi", text)
}

func TestInlineThinkSplitter_ProviderThinkingDisablesSplit(t *testing.T) {
	var splitter inlineThinkSplitter

	first := ollamaChatMessage{Thinking: "provider reasoning", Content: "<think>x</think>a"}
	splitter.rewrite(&first, false)
	assert.Equal(t, "provider reasoning", first.Thinking)
	assert.Equal(t, "<think>x</think>a", first.Content)

	second := ollamaChatMessage{Content: "<think>y"}
	splitter.rewrite(&second, true)
	assert.Empty(t, second.Thinking)
	assert.Equal(t, "<think>y", second.Content)
}

func TestInlineThinkSplitter_ProviderThinkingFlushesBufferedAsText(t *testing.T) {
	var splitter inlineThinkSplitter

	first := ollamaChatMessage{Content: "<thi"}
	splitter.rewrite(&first, false)
	assert.Empty(t, first.Content)

	second := ollamaChatMessage{Thinking: "provider reasoning", Content: "nk>x"}
	splitter.rewrite(&second, true)
	assert.Equal(t, "provider reasoning", second.Thinking)
	assert.Equal(t, "<think>x", second.Content)
}

func TestDecodeChatStream_InlineThinkSplitsIntoThinkingAndText(t *testing.T) {
	backend, err := NewOllamaBackend("http://localhost:11434", testutil.NewTestLogger())
	require.NoError(t, err)
	body := strings.Join([]string{
		`{"model":"lfm2.5:8b","message":{"role":"assistant","content":"<think>pl"},"done":false}`,
		`{"model":"lfm2.5:8b","message":{"role":"assistant","content":"an</think>\n"},"done":false}`,
		`{"model":"lfm2.5:8b","message":{"role":"assistant","content":"answer"},"done":false}`,
		`{"model":"lfm2.5:8b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`,
	}, "\n") + "\n"
	arrival := func(offset int64) *int64 { return &offset }

	_, parts, _, err := backend.decodeChatStream(t.Context(), strings.NewReader(body), arrival, true, nil, "attempt", nil)
	require.NoError(t, err)

	// Each stream event yields its own delta part, so collapse adjacent parts of the same kind.
	var kinds []string
	var thinking, text strings.Builder
	for _, part := range parts {
		kind := "text"
		if part.GetThinking() != "" {
			kind = "thinking"
			thinking.WriteString(part.GetThinking())
		} else {
			text.WriteString(part.GetText())
		}
		if len(kinds) == 0 || kinds[len(kinds)-1] != kind {
			kinds = append(kinds, kind)
		}
	}
	assert.Equal(t, []string{"thinking", "text"}, kinds)
	assert.Equal(t, "plan", thinking.String())
	assert.Equal(t, "answer", text.String())
}
