// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// streamUntilLoop feeds output to a detector in token-sized deltas, the way a
// provider stream arrives, and returns how many bytes it consumed before the
// detector stopped it, or -1 when it never did.
func streamUntilLoop(output string, thinking bool) int {
	detector := newRepetitionDetector()
	const delta = 7
	for start := 0; start < len(output); start += delta {
		chunk := output[start:min(start+delta, len(output))]
		part := &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Text{Text: chunk}}
		if thinking {
			part = &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Thinking{Thinking: chunk}}
		}
		if detector.observe([]*operatorv1.InferenceResponsePart{part}) {
			return start + len(chunk)
		}
	}
	return -1
}

func TestRepetitionDetector_StopsALoopingVerdict(t *testing.T) {
	// A judge verdict whose rationale degenerates into one repeated sentence.
	output := `{"met": false, "rationale": "The agent did not read logs/auth.log before answering. ` +
		strings.Repeat("The agent searched for the pattern but did not report the matching line. ", 400)

	consumed := streamUntilLoop(output, false)

	assert.Positive(t, consumed, "a looping verdict is stopped")
	assert.Less(t, consumed, constants.InferenceRepetitionMinSpanBytes+2*constants.InferenceRepetitionCheckIntervalBytes, "the loop stops soon after it spans the minimum")
}

func TestRepetitionDetector_StopsALoopInThinking(t *testing.T) {
	output := "The user wants the failing login. Let me search the log.\n" +
		strings.Repeat("Wait, I should check the file again before I answer.\nLet me re-read the search result.\n", 200)

	assert.Positive(t, streamUntilLoop(output, true))
}

func TestRepetitionDetector_StopsTheLongestDetectablePeriod(t *testing.T) {
	period := constants.InferenceRepetitionWindowBytes / constants.InferenceRepetitionMinRepeats
	paragraph := variedText(period)

	assert.Positive(t, streamUntilLoop(strings.Repeat(paragraph, constants.InferenceRepetitionMinRepeats+1), false))
	assert.Equal(t, -1, streamUntilLoop(strings.Repeat(variedText(period+64), constants.InferenceRepetitionMinRepeats+1), false),
		"a period beyond the window cannot be confirmed")
}

func TestRepetitionDetector_LetsVariedOutputRun(t *testing.T) {
	for name, output := range map[string]string{
		"long varied prose": variedText(4 * constants.InferenceRepetitionWindowBytes),
		"numbered list with identical items": func() string {
			var b strings.Builder
			for i := range 2000 {
				fmt.Fprintf(&b, "%d. AUTH_FAILURE user=svc-deploy\n", i+1)
			}
			return b.String()
		}(),
		"repeated span below the minimum": strings.Repeat("A short paragraph that repeats a few times. ", 3*constants.InferenceRepetitionMinSpanBytes/45/4),
		"repeats below the minimum count": strings.Repeat(variedText(constants.InferenceRepetitionMinSpanBytes), constants.InferenceRepetitionMinRepeats-1),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, -1, streamUntilLoop(output, false))
		})
	}
}

// variedText is n bytes of text with no repeated span longer than a word.
func variedText(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "w%x ", i*2654435761%1000003)
	}
	return b.String()[:n]
}
