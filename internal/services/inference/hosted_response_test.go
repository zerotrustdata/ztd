// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type hostedRawResponseRecorder struct {
	AttemptStore
	id    string
	raw   []byte
	calls int
	err   error
}

func (s *hostedRawResponseRecorder) RecordRawResponse(_ context.Context, id string, raw []byte) error {
	s.id = id
	s.raw = append([]byte(nil), raw...)
	s.calls++
	return s.err
}

func TestGeminiBackend_HostedCaptureRecordsExactStreamEvenOnDecodeFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, stream string
		wantErr      error
	}{
		{"complete stream", geminiToolCallStream(geminiTestModel), nil},
		{"malformed event with trailing bytes", "data: not-json\r\n\r\n" + geminiToolCallStream(geminiTestModel), constants.ErrInferenceProviderResponseInvalid},
		{"provider stream error with trailing bytes", "data: {\"error\":{\"code\":400}}\r\n\r\n" + geminiToolCallStream(geminiTestModel), constants.ErrInferenceProviderStreamError},
		{"transient stream error with trailing bytes", "data: {\"error\":{\"code\":503}}\r\n\r\n" + geminiToolCallStream(geminiTestModel), constants.ErrInferenceBackendTransient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &hostedRawResponseRecorder{}
			fake := &geminiFake{stream: tc.stream}
			backend := newFakeGeminiBackend(t, fake)
			req := geminiRequest(geminiTestModel)
			resp, err := backend.Generate(WithAttemptStore(testutil.Context(t), store), req)
			if tc.wantErr == nil {
				require.NoError(t, err)
				assert.Equal(t, uint64(len(tc.stream)), resp.HostedEgress.GetRawResponseSizeBytes())
			} else {
				require.ErrorIs(t, err, tc.wantErr)
			}
			require.Equal(t, 1, store.calls)
			assert.Equal(t, req.ProviderAttemptID, store.id)
			assert.Equal(t, tc.stream, string(store.raw), "decoding does not truncate or canonicalize the captured bytes")
			assert.Equal(t, 1, fake.posts(), "a failed attempt is never retried in the client")
		})
	}
}

func TestGeminiBackend_HostedCaptureExcludesHTTPErrorBodies(t *testing.T) {
	t.Parallel()
	store := &hostedRawResponseRecorder{}
	fake := &geminiFake{status: http.StatusTooManyRequests, errorBody: `{"error":{"message":"prompt-or-key-must-not-escape"}}`}
	backend := newFakeGeminiBackend(t, fake)
	_, err := backend.Generate(WithAttemptStore(testutil.Context(t), store), geminiRequest(geminiTestModel))
	require.ErrorIs(t, err, constants.ErrInferenceBackendTransient)
	assert.NotContains(t, err.Error(), "prompt-or-key-must-not-escape")
	assert.Zero(t, store.calls)
	assert.Equal(t, 1, fake.posts())
}

func TestGeminiBackend_HostedCaptureFailsWhenEvidenceCannotBeRecorded(t *testing.T) {
	t.Parallel()
	store := &hostedRawResponseRecorder{err: constants.ErrInferenceProviderAttemptConflict}
	backend := newFakeGeminiBackend(t, &geminiFake{stream: geminiToolCallStream(geminiTestModel)})
	resp, err := backend.Generate(WithAttemptStore(testutil.Context(t), store), geminiRequest(geminiTestModel))
	require.ErrorIs(t, err, constants.ErrInferenceProviderAttemptConflict)
	assert.Nil(t, resp)
	assert.Equal(t, 1, store.calls)
}

func TestGeminiBackend_HostedCaptureBoundsTheRawResponse(t *testing.T) {
	t.Parallel()
	store := &hostedRawResponseRecorder{}
	stream := "data: " + strings.Repeat("x", 2048) + "\r\n\r\n"
	backend := newFakeGeminiBackend(t, &geminiFake{stream: stream})
	backend.maxResponseBytes = 1024
	_, err := backend.Generate(WithAttemptStore(testutil.Context(t), store), geminiRequest(geminiTestModel))
	require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid)
	assert.Equal(t, stream[:1025], string(store.raw), "the capture keeps only the bounded prefix and overflow byte")
}

// assertHostedTiming checks the Operator-observed timing every hosted client
// reports: one span from the request start to the last response byte, as both
// the generation and total duration, never shorter than time to first token.
func assertHostedTiming(t *testing.T, resp *models.GenerateResponse) {
	t.Helper()
	require.NotNil(t, resp.TimeToFirstTokenNS)
	require.NotNil(t, resp.GenerationDurationNS, "a hosted generation reports its egress span")
	require.NotNil(t, resp.TotalDurationNS)
	assert.Equal(t, *resp.GenerationDurationNS, *resp.TotalDurationNS)
	assert.GreaterOrEqual(t, *resp.GenerationDurationNS, *resp.TimeToFirstTokenNS, "the span starts at the request, before the first part")
	assert.Nil(t, resp.PromptEvalDurationNS, "a hosted provider reports no prompt evaluation time")
	assert.Equal(t, operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER, resp.TimingSource)
}

func TestProviderResponseCapture_FinalArrivalIsTheLastRead(t *testing.T) {
	t.Parallel()
	empty := newProviderResponseCapture(1024, time.Now())
	empty.receive(strings.NewReader(""))
	_, err := empty.result()
	require.NoError(t, err)
	assert.Nil(t, empty.finalArrival(), "no bytes, no arrival")

	capture := newProviderResponseCapture(1024, time.Now())
	capture.receive(strings.NewReader("data: one\r\n\r\n"))
	_, err = capture.result()
	require.NoError(t, err)
	require.NotNil(t, capture.finalArrival())
	assert.Equal(t, capture.reads[len(capture.reads)-1].elapsedNS, *capture.finalArrival())
}

func TestNewHostedTiming_SourceOnlyWithTiming(t *testing.T) {
	t.Parallel()
	none := newHostedTiming(nil, nil)
	assert.Equal(t, operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_UNSPECIFIED, none.source)
	assert.Nil(t, none.generation)
	assert.Nil(t, none.total)

	first, complete := int64(5), int64(9)
	timing := newHostedTiming(&first, &complete)
	assert.Equal(t, operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER, timing.source)
	assert.Same(t, &first, timing.firstToken)
	assert.Equal(t, complete, *timing.generation)
	assert.Equal(t, complete, *timing.total)
}
