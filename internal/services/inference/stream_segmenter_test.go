// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	segmenterSecretKey   = "AKIAIOSFODNN7EXAMPLE"
	segmenterSecretEmail = "admin@example.com"
)

func segmenterTextPart(text string) *operatorv1.InferenceResponsePart {
	return &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Text{Text: text}}
}

func segmenterThinkingPart(text string) *operatorv1.InferenceResponsePart {
	return &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Thinking{Thinking: text}}
}

// segmenterRun drives a segmenter over one provider event per entry, the way decodeChatStream does, and
// returns every published progress event plus the terminal parts.
func segmenterRun(t *testing.T, s models.EgressScrubber, events [][]*operatorv1.InferenceResponsePart) ([]*operatorv1.InferenceProgressEvent, []*operatorv1.InferenceResponsePart) {
	t.Helper()
	var published []*operatorv1.InferenceProgressEvent
	stream, err := newStreamSegmenter(s, func(event *operatorv1.InferenceProgressEvent) error {
		published = append(published, event)
		return nil
	}, "attempt-segmenter")
	require.NoError(t, err)

	var terminal []*operatorv1.InferenceResponsePart
	for _, parts := range events {
		released, err := stream.accept(context.Background(), parts)
		require.NoError(t, err)
		require.NoError(t, stream.publish(released, "test-model", nil))
		terminal = append(terminal, released...)
	}
	tail := stream.finish(context.Background())
	require.NoError(t, stream.publish(tail, "test-model", nil))
	terminal = append(terminal, tail...)
	return published, terminal
}

// segmenterRunDeltas feeds each string as one text delta of one provider event.
func segmenterRunDeltas(t *testing.T, s models.EgressScrubber, deltas []string) ([]*operatorv1.InferenceProgressEvent, []*operatorv1.InferenceResponsePart) {
	t.Helper()
	events := make([][]*operatorv1.InferenceResponsePart, 0, len(deltas))
	for _, d := range deltas {
		events = append(events, []*operatorv1.InferenceResponsePart{segmenterTextPart(d)})
	}
	return segmenterRun(t, s, events)
}

func progressPartsText(events []*operatorv1.InferenceProgressEvent) string {
	var b strings.Builder
	for _, event := range events {
		for _, part := range event.GetParts() {
			b.WriteString(part.GetText())
			b.WriteString(part.GetThinking())
		}
	}
	return b.String()
}

// terminalResult builds the InferenceResult the Operator would return for terminal parts.
func terminalResult(t *testing.T, parts []*operatorv1.InferenceResponsePart) *operatorv1.InferenceResult {
	t.Helper()
	hash, err := models.ComputeInferenceOutputHash(parts, "stop")
	require.NoError(t, err)
	return &operatorv1.InferenceResult{Parts: parts, FinishReason: "stop", OutputHash: hash, ProviderAttemptId: "attempt-segmenter"}
}

func TestStreamSegmenter_SecretSplitAcrossDeltasIsNeverEmittedRaw(t *testing.T) {
	t.Parallel()
	s := mustNewScrubbingSvc(t)
	deltas := []string{"key ", segmenterSecretKey[:9], segmenterSecretKey[9:] + " mail\n", segmenterSecretEmail[:6], segmenterSecretEmail[6:] + " done\n"}

	published, terminal := segmenterRunDeltas(t, s, deltas)

	streamed := progressPartsText(published)
	assert.NotContains(t, streamed, segmenterSecretKey)
	assert.NotContains(t, streamed, segmenterSecretEmail)
	assert.Equal(t, s.ScrubText(context.Background(), strings.Join(deltas, "")), streamed, "secrets are scrubbed whole, not in fragments")
	rehydrated, err := s.RehydrateText(context.Background(), streamed)
	require.NoError(t, err, "every streamed token must resolve")
	assert.Equal(t, strings.Join(deltas, ""), rehydrated, "the streamed tokens rehydrate to the provider output")
	// The terminal parts are exactly the emitted segments, so the hashes match by construction.
	require.NoError(t, models.ReconcileInferenceProgress(published, terminalResult(t, terminal)))
}

func TestStreamSegmenter_ReleasesOnlyThroughTheLastNewline(t *testing.T) {
	t.Parallel()
	s := mustNewScrubbingSvc(t)

	// "hello world!\n" is released once the next non-blank line ("and\n") has arrived as lookahead.
	published, terminal := segmenterRunDeltas(t, s, []string{"hello wor", "ld!\nand\n"})

	require.Len(t, published, 2)
	for i, event := range published {
		assert.Equal(t, uint32(i+1), event.GetSequence(), "progress sequence is contiguous from 1")
	}
	assert.Equal(t, "hello world!\n", published[0].GetParts()[0].GetText())
	assert.Equal(t, "and\n", published[1].GetParts()[0].GetText())
	require.Len(t, terminal, 2)
	require.NoError(t, models.ReconcileInferenceProgress(published, terminalResult(t, terminal)))
}

func TestStreamSegmenter_LongLineWithoutNewlineIsHeldUntilFinish(t *testing.T) {
	t.Parallel()
	s := mustNewScrubbingSvc(t)
	stream, err := newStreamSegmenter(s, func(*operatorv1.InferenceProgressEvent) error { return nil }, "attempt-hold")
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		released, err := stream.accept(context.Background(), []*operatorv1.InferenceResponsePart{segmenterTextPart("word ")})
		require.NoError(t, err)
		require.Empty(t, released, "no release without a newline, whatever the length")
	}
	assert.Len(t, stream.finish(context.Background()), 1)
}

func TestStreamSegmenter_ThinkingAndTextKeepTheirOrder(t *testing.T) {
	t.Parallel()
	s := mustNewScrubbingSvc(t)

	published, terminal := segmenterRun(t, s, [][]*operatorv1.InferenceResponsePart{
		{segmenterThinkingPart("plan\n")},
		{segmenterTextPart("answer\n")},
		{segmenterTextPart("ok\nmore\n")},
	})

	require.NotEmpty(t, terminal)
	assert.Equal(t, "plan\n", terminal[0].GetThinking(), "thinking comes first and keeps its kind")
	var text strings.Builder
	for _, part := range terminal[1:] {
		require.NotNil(t, part.GetText(), "every later part is text")
		text.WriteString(part.GetText())
	}
	assert.Equal(t, "answer\nok\nmore\n", text.String())
	require.NoError(t, models.ReconcileInferenceProgress(published, terminalResult(t, terminal)))
}

func TestStreamSegmenter_ToolCallIsScrubbedWholeAfterTheTextBeforeIt(t *testing.T) {
	t.Parallel()
	s := mustNewScrubbingSvc(t)
	toolCall := &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
		CallId:        "call-1",
		Name:          "send",
		ArgumentsJson: `{"to":"` + segmenterSecretEmail + `"}`,
	}}}

	published, terminal := segmenterRun(t, s, [][]*operatorv1.InferenceResponsePart{
		{segmenterTextPart("sending ")},
		{toolCall},
	})

	require.Len(t, terminal, 2)
	assert.Equal(t, "sending ", terminal[0].GetText(), "text before a tool call is released first, keeping the order")
	call := terminal[1].GetToolCall()
	assert.Equal(t, "call-1", call.GetCallId())
	assert.Regexp(t, `^\{"to":"\{\{UEI_\d+\}\}"\}$`, call.GetArgumentsJson())
	arguments, err := s.RehydrateText(context.Background(), call.GetArgumentsJson())
	require.NoError(t, err)
	assert.Equal(t, `{"to":"`+segmenterSecretEmail+`"}`, arguments)
	for _, event := range published {
		for _, part := range event.GetParts() {
			assert.NotContains(t, part.GetToolCall().GetArgumentsJson(), segmenterSecretEmail)
		}
	}
	require.NoError(t, models.ReconcileInferenceProgress(published, terminalResult(t, terminal)))
}

// The PEM body must not be released until its END line is complete, even though it contains newlines.
func TestStreamSegmenter_PEMBlockIsHeldUntilItsEndLineCompletes(t *testing.T) {
	t.Parallel()
	s := mustNewScrubbingSvc(t)
	stream, err := newStreamSegmenter(s, func(*operatorv1.InferenceProgressEvent) error { return nil }, "attempt-pem")
	require.NoError(t, err)

	body := "MIIEpAIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gun"
	released, err := stream.accept(context.Background(), []*operatorv1.InferenceResponsePart{segmenterTextPart("intro\n-----BEGIN RSA PRIVATE KEY-----\n" + body + "\n")})
	require.NoError(t, err)
	assert.Empty(t, released, "nothing at or after the open BEGIN marker is released")

	released, err = stream.accept(context.Background(), []*operatorv1.InferenceResponsePart{segmenterTextPart("-----END RSA PRIVATE KEY-----")})
	require.NoError(t, err)
	assert.Empty(t, released, "the block stays held until its END line has a newline")

	released, err = stream.accept(context.Background(), []*operatorv1.InferenceResponsePart{segmenterTextPart("\nafter\n")})
	require.NoError(t, err)
	require.Len(t, released, 1)
	assert.NotContains(t, released[0].GetText(), body)
	assert.Contains(t, released[0].GetText(), "intro")
}

// Every scrubber pattern that can match across internal whitespace or a newline is driven through every
// whitespace split point, and through one character per delta. The raw secret must never be emitted and
// the terminal result must reconcile with the progress stream.
func TestStreamSegmenter_WhitespacePatternsNeverLeakAcrossAnySplit(t *testing.T) {
	t.Parallel()
	s := mustNewScrubbingSvc(t)
	cases := []struct {
		name    string
		text    string
		secrets []string
	}{
		{
			name:    "password_config with spaces",
			text:    "set password = hunter2hunter2 now\n",
			secrets: []string{"hunter2hunter2"},
		},
		{
			name:    "password_config colon",
			text:    "password: correcthorsebattery\n",
			secrets: []string{"correcthorsebattery"},
		},
		{
			name:    "aws_secret_key",
			text:    "aws secret access key: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY\n",
			secrets: []string{"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		},
		{
			name:    "oauth_secret with spaces",
			text:    "client_secret = abcdefghijklmnopqrstuvwxyz0123\n",
			secrets: []string{"abcdefghijklmnopqrstuvwxyz0123"},
		},
		{
			name:    "azure_secret",
			text:    `azure secret: "abcdefghijklmnopqrstuvwxyz0123456789ABCD"` + "\n",
			secrets: []string{"abcdefghijklmnopqrstuvwxyz0123456789ABCD"},
		},
		{
			name:    "heroku_key",
			text:    "heroku api key = 0123abcd-0123-4567-89ab-0123456789ab\n",
			secrets: []string{"0123abcd-0123-4567-89ab-0123456789ab"},
		},
		{
			name:    "credit_card with spaces",
			text:    "card 4111 1111 1111 1111 ok\n",
			secrets: []string{"4111 1111 1111 1111"},
		},
		{
			name:    "phone with spaces",
			text:    "call 555 123 4567 today\n",
			secrets: []string{"555 123 4567"},
		},
		{
			name:    "bearer_token",
			text:    "Authorization: bearer abc.def-ghi_jkl\n",
			secrets: []string{"abc.def-ghi_jkl"},
		},
		{
			name: "private_key PEM spanning lines",
			text: "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gun\n" +
				"VTLw7onLRnrq0IzW7yWR7QkrmBL7jTKEn5u+qKhbwKfBstIs+bMY2Zkp18gnTxKLxoS2tFczGkPLPgizskuemMghRniWaoLcyeh\n" +
				"-----END RSA PRIVATE KEY-----\nafter\n",
			secrets: []string{"MIIEpAIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gun", "VTLw7onLRnrq0IzW7yWR7QkrmBL7jTKEn5u"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A trailing line gives the single-line cases the lookahead that lets them release before finish.
			assertNoLeakAcrossSplits(t, s, tc.text+"ok\n", tc.secrets)
		})
	}
}

// The value of password_config, oauth_secret and bearer_token can sit on the line after the keyword, after
// any number of blank lines. The raw value must never be emitted at any split.
func TestStreamSegmenter_ValueOnNextLineNeverLeaks(t *testing.T) {
	t.Parallel()
	s := mustNewScrubbingSvc(t)
	cases := []struct {
		name    string
		text    string
		secrets []string
	}{
		{name: "password_config after one line", text: "password:\nhunter2\nmore\n", secrets: []string{"hunter2"}},
		{name: "password_config after blank lines", text: "password:\n\n\nhunter2\nmore\n", secrets: []string{"hunter2"}},
		{
			name:    "oauth_secret after one line",
			text:    "client_secret:\nabcdefghijklmnopqrstuvwxyz0123\nmore\n",
			secrets: []string{"abcdefghijklmnopqrstuvwxyz0123"},
		},
		{
			name:    "oauth_secret after blank lines",
			text:    "client_secret:\n\n\nabcdefghijklmnopqrstuvwxyz0123\nmore\n",
			secrets: []string{"abcdefghijklmnopqrstuvwxyz0123"},
		},
		{name: "bearer_token after one line", text: "Authorization: bearer\nabc.def-ghi_jkl\nmore\n", secrets: []string{"abc.def-ghi_jkl"}},
		{name: "bearer_token after blank lines", text: "Authorization: bearer\n\n\nabc.def-ghi_jkl\nmore\n", secrets: []string{"abc.def-ghi_jkl"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertNoLeakAcrossSplits(t, s, tc.text, tc.secrets)
		})
	}
}

// assertNoLeakAcrossSplits streams text through every whitespace split and every single-byte split. It checks
// that no secret is emitted, that the streamed output equals the whole-text scrub, and that the terminal result
// reconciles with the progress stream.
func assertNoLeakAcrossSplits(t *testing.T, s *scrubbing.ScrubbingService, text string, secrets []string) {
	t.Helper()
	whole := s.ScrubText(context.Background(), text)
	for _, secret := range secrets {
		require.NotContains(t, whole, secret, "the pattern must match the secret on whole text")
	}

	splits := whitespaceSplits(text)
	splits = append(splits, perCharacterSplit(text))
	for i, deltas := range splits {
		published, terminal := segmenterRunDeltas(t, s, deltas)
		streamed := progressPartsText(published)
		for _, secret := range secrets {
			require.NotContains(t, streamed, secret, "split %d leaked the secret", i)
		}
		assert.Equal(t, whole, streamed, "split %d: streamed output must equal whole-text scrub", i)
		require.NoError(t, models.ReconcileInferenceProgress(published, terminalResult(t, terminal)), "split %d", i)
	}
}

// whitespaceSplits returns one two-delta split just before and just after every whitespace rune.
func whitespaceSplits(text string) [][]string {
	var out [][]string
	for i, r := range text {
		if !unicode.IsSpace(r) {
			continue
		}
		out = append(out, []string{text[:i], text[i:]})
		out = append(out, []string{text[:i+1], text[i+1:]})
	}
	return out
}

// perCharacterSplit returns one delta per byte of text, the finest split a stream can produce.
func perCharacterSplit(text string) []string {
	out := make([]string, 0, len(text))
	for i := 0; i < len(text); i++ {
		out = append(out, text[i:i+1])
	}
	return out
}

func TestStreamSegmenter_FailsClosedWithoutAnEnabledScrubber(t *testing.T) {
	t.Parallel()
	disabled, err := scrubbing.NewScrubbingService(context.Background(), &scrubbing.Config{Enabled: false}, testutil.NewTestLogger(), nil)
	require.NoError(t, err)

	for name, s := range map[string]models.EgressScrubber{
		"nil scrubber":      nil,
		"disabled scrubber": disabled,
	} {
		t.Run(name, func(t *testing.T) {
			stream, err := newStreamSegmenter(s, func(*operatorv1.InferenceProgressEvent) error { return nil }, "attempt-x")
			require.ErrorIs(t, err, constants.ErrEgressScrubberUnavailable)
			assert.Nil(t, stream)
		})
	}
}

// A nil *ScrubbingService must read as disabled, never panic, so a typed-nil scrubber fails closed.
func TestScrubbingService_NilReceiverIsDisabled(t *testing.T) {
	t.Parallel()
	var s *scrubbing.ScrubbingService
	assert.False(t, s.IsEnabled())
	_, err := newStreamSegmenter(s, func(*operatorv1.InferenceProgressEvent) error { return nil }, "attempt-nil")
	require.ErrorIs(t, err, constants.ErrEgressScrubberUnavailable)
}

// ScrubText must be a no-op on text it already produced, because the handler scrubs the terminal parts
// again and must not change what the progress stream published.
func TestScrubText_IsIdempotentOnItsOwnOutput(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"contact " + segmenterSecretEmail + " and " + segmenterSecretKey + " now",
		"Connect to 10.0.0.1:5432 and 192.168.1.1",
		"password: hunter2 token=abc123 ",
		"bearer abc.def.ghi and https://user:pass@example.com/x",
		"plain text with no sensitive values\n\nand a second line",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAA\n-----END RSA PRIVATE KEY-----\n",
	}
	for _, cfg := range []*scrubbing.Config{
		{Enabled: true, StrictMode: false, RequirePersistence: true},
		{Enabled: true, StrictMode: true, RequirePersistence: true},
	} {
		s, err := scrubbing.NewScrubbingService(context.Background(), cfg, testutil.NewTestLogger(), storagetest.NewTestTokenStore())
		require.NoError(t, err)
		for _, input := range inputs {
			once := s.ScrubText(context.Background(), input)
			assert.Equal(t, once, s.ScrubText(context.Background(), once), "ScrubText must be idempotent for %q (strict=%t)", input, cfg.StrictMode)
		}
	}
}
