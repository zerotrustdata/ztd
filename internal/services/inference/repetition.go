// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// repetitionDetector recognizes a generation that has degenerated into a
// loop. It is the stop for runaway repetition and is independent of the
// loaded context window, which bounds output length: a generation that never
// repeats is never stopped by it. The thresholds are the
// constants.InferenceRepetition* values.
type repetitionDetector struct {
	tail       []byte
	sinceCheck int
	border     []int
}

func newRepetitionDetector() *repetitionDetector {
	return &repetitionDetector{
		tail:   make([]byte, 0, constants.InferenceRepetitionWindowBytes),
		border: make([]int, constants.InferenceRepetitionWindowBytes),
	}
}

// observe consumes the text and thinking of newly decoded parts and reports
// whether the output now ends in a loop.
func (d *repetitionDetector) observe(parts []*operatorv1.InferenceResponsePart) bool {
	for _, part := range parts {
		switch content := part.GetPart().(type) {
		case *operatorv1.InferenceResponsePart_Text:
			d.append(content.Text)
		case *operatorv1.InferenceResponsePart_Thinking:
			d.append(content.Thinking)
		}
	}
	if d.sinceCheck < constants.InferenceRepetitionCheckIntervalBytes {
		return false
	}
	d.sinceCheck = 0
	return d.endsInLoop()
}

func (d *repetitionDetector) append(text string) {
	d.sinceCheck += len(text)
	if len(text) >= constants.InferenceRepetitionWindowBytes {
		d.tail = append(d.tail[:0], text[len(text)-constants.InferenceRepetitionWindowBytes:]...)
		return
	}
	if excess := len(d.tail) + len(text) - constants.InferenceRepetitionWindowBytes; excess > 0 {
		d.tail = d.tail[:copy(d.tail, d.tail[excess:])]
	}
	d.tail = append(d.tail, text...)
}

// endsInLoop reports whether some suffix of the tail of at least
// InferenceRepetitionMinSpanBytes has a period it repeats at least
// InferenceRepetitionMinRepeats times. A string and its reverse share their
// periods, so the prefix function of the reversed tail gives, for each suffix
// length n, that suffix's smallest period n - border[n-1].
func (d *repetitionDetector) endsInLoop() bool {
	n := len(d.tail)
	if n < constants.InferenceRepetitionMinSpanBytes {
		return false
	}
	at := func(i int) byte { return d.tail[n-1-i] }
	border := d.border[:n]
	border[0] = 0
	for i := 1; i < n; i++ {
		k := border[i-1]
		for k > 0 && at(i) != at(k) {
			k = border[k-1]
		}
		if at(i) == at(k) {
			k++
		}
		border[i] = k
	}
	for length := n; length >= constants.InferenceRepetitionMinSpanBytes; length-- {
		period := length - border[length-1]
		if length >= constants.InferenceRepetitionMinRepeats*period {
			return true
		}
	}
	return false
}
