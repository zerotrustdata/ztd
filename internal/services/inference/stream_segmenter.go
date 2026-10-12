// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// segmentKind is the part kind of one run of buffered text, or segmentToolCall for a tool-call item.
type segmentKind uint8

const (
	segmentNone segmentKind = iota
	segmentText
	segmentThinking
	segmentToolCall
)

var (
	pemBeginMarker = []byte("-----BEGIN")
	pemEndMarker   = []byte("-----END")
)

// streamItem is one ordered entry of the stream: a run of text of one kind, or a tool call. A run
// starts at offset in the buffer and ends where the next item starts. A tool call occupies no bytes.
type streamItem struct {
	offset int
	kind   segmentKind
	call   *operatorv1.InferenceToolCall
}

// streamSegmenter is the source-side egress of a streamed inference response. It holds the stream
// until a boundary at which every value a scrubber can match is complete:
//
//   - Text and thinking are released at the latest newline cut that keeps one non-blank line as lookahead
//     and splits no scrubber match (see releaseCut). Values that sit on the line after a keyword, after any
//     number of blank lines, are therefore scrubbed whole.
//   - While a PEM block is open (a BEGIN marker with no END line followed by a newline), everything
//     from its BEGIN marker onward is held. The block is released once its END line completes.
//   - A tool call is released after all text before it, so the order of parts is kept.
//   - Nothing is ever released on a timer or by size. A stream that never emits a newline is held until
//     its terminal event. That costs streaming latency, never safety.
//
// Each released segment is scrubbed once here and published as one InferenceProgressEvent. The terminal
// parts are exactly the concatenation of the released segments, in order, so the output hash of the
// terminal result equals the hash over the concatenated progress parts by construction. Nothing is released
// raw: a missing or disabled scrubber is refused by newStreamSegmenter before the provider stream is opened.
type streamSegmenter struct {
	scrubber          models.EgressScrubber
	reporter          ProgressReporter
	providerAttemptID string
	sequence          uint32

	buf      []byte
	items    []streamItem
	lastNL   int // index in buf of the last newline, -1 when none is buffered
	scanFrom int // index in buf where the PEM BEGIN search resumes
}

// newStreamSegmenter returns a segmenter for one streamed attempt. It fails closed with
// constants.ErrEgressScrubberUnavailable when no enabled scrubber is available.
func newStreamSegmenter(scrubber models.EgressScrubber, reporter ProgressReporter, providerAttemptID string) (*streamSegmenter, error) {
	if scrubber == nil || !scrubber.IsEnabled() {
		return nil, fmt.Errorf("inference stream: %w", constants.ErrEgressScrubberUnavailable)
	}
	return &streamSegmenter{
		scrubber:          scrubber,
		reporter:          reporter,
		providerAttemptID: providerAttemptID,
		lastNL:            -1,
	}, nil
}

// accept takes the parts of one provider event and returns the scrubbed segments that are complete now.
// Everything else stays buffered until a later event or finish.
func (g *streamSegmenter) accept(parts []*operatorv1.InferenceResponsePart) ([]*operatorv1.InferenceResponsePart, error) {
	var out []*operatorv1.InferenceResponsePart
	for _, part := range parts {
		switch v := part.GetPart().(type) {
		case *operatorv1.InferenceResponsePart_Text:
			out = g.appendText(out, segmentText, v.Text)
		case *operatorv1.InferenceResponsePart_Thinking:
			out = g.appendText(out, segmentThinking, v.Thinking)
		case *operatorv1.InferenceResponsePart_ToolCall:
			if v.ToolCall == nil {
				return nil, fmt.Errorf("inference stream: tool call: %w", constants.ErrInferenceProviderResponseInvalid)
			}
			g.items = append(g.items, streamItem{offset: len(g.buf), kind: segmentToolCall, call: v.ToolCall})
			out = g.release(out, false)
		default:
			return nil, fmt.Errorf("inference stream: response part: %w", constants.ErrInferenceProviderResponseInvalid)
		}
	}
	return out, nil
}

// finish releases the whole buffered tail once the provider stream has ended with its terminal event.
func (g *streamSegmenter) finish() []*operatorv1.InferenceResponsePart {
	return g.release(nil, true)
}

// appendText adds one text or thinking delta to the stream and releases whatever is now complete.
func (g *streamSegmenter) appendText(out []*operatorv1.InferenceResponsePart, kind segmentKind, text string) []*operatorv1.InferenceResponsePart {
	if text == "" {
		return out
	}
	if n := len(g.items); n == 0 || g.items[n-1].kind != kind {
		g.items = append(g.items, streamItem{offset: len(g.buf), kind: kind})
	}
	base := len(g.buf)
	g.buf = append(g.buf, text...)
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		g.lastNL = base + i
	}
	return g.release(out, false)
}

// release emits every item that is complete at the release cut, scrubbing each piece once, and drops the
// emitted prefix of the stream. The cut is the byte after the last newline, moved back to an open PEM
// block's BEGIN marker when one is held. When final is set the whole buffer is released.
func (g *streamSegmenter) release(out []*operatorv1.InferenceResponsePart, final bool) []*operatorv1.InferenceResponsePart {
	cut := len(g.buf)
	if !final {
		cut = g.releaseCut()
	}

	consumed := 0
	for consumed < len(g.items) {
		item := g.items[consumed]
		if item.kind == segmentToolCall {
			if item.offset > cut {
				break
			}
			out = append(out, g.toolCallPart(item.call))
			consumed++
			continue
		}
		end := len(g.buf)
		if consumed+1 < len(g.items) {
			end = g.items[consumed+1].offset
		}
		if end > cut {
			// The run continues past the cut: release its released piece and keep the rest.
			if cut > item.offset {
				out = append(out, g.segment(item.kind, string(g.buf[item.offset:cut])))
			}
			break
		}
		if end > item.offset {
			out = append(out, g.segment(item.kind, string(g.buf[item.offset:end])))
		}
		consumed++
	}

	// Drop the released prefix [0, cut) and rebase what remains.
	g.buf = append([]byte(nil), g.buf[cut:]...)
	g.items = g.items[consumed:]
	for i := range g.items {
		g.items[i].offset = max(0, g.items[i].offset-cut)
	}
	if g.lastNL >= 0 {
		g.lastNL -= cut
		if g.lastNL < 0 {
			g.lastNL = -1
		}
	}
	g.scanFrom = max(0, g.scanFrom-cut)
	return out
}

// releaseCut returns the latest cut c for a non-final release, or 0 when no position qualifies. c is always
// just after a newline, and it must satisfy all of:
//
//  1. buf[c:] holds a complete non-blank line, so one line of lookahead stays held. A keyword line is
//     never released before the line that may carry its value.
//  2. c is before any open PEM BEGIN marker.
//  3. No scrubber match crosses c: ScrubText(buf[:c]) + ScrubText(buf[c:]) == ScrubText(buf). Tokens are
//     stable per value, so equality holds exactly when no match spans c.
//
// Rule 3 is checked on the buffered text. Text that is not yet buffered cannot start a match before c, because
// every value that a whitespace-bearing pattern can match ends on the line that rule 1 already holds.
func (g *streamSegmenter) releaseCut() int {
	end := len(g.buf)
	limit := len(g.buf)
	if hold := g.openBlockStart(); hold >= 0 {
		limit = hold
		end = hold
	}
	var whole string
	haveWhole := false
	for end > 0 {
		i := bytes.LastIndexByte(g.buf[:end], '\n')
		if i < 0 {
			break
		}
		end = i
		c := i + 1
		if c >= limit {
			continue
		}
		if bytes.IndexFunc(g.buf[c:g.lastNL+1], isNotBlank) < 0 {
			continue
		}
		if !haveWhole {
			whole = g.scrubber.ScrubText(string(g.buf))
			haveWhole = true
		}
		if g.scrubber.ScrubText(string(g.buf[:c]))+g.scrubber.ScrubText(string(g.buf[c:])) == whole {
			return c
		}
	}
	return 0
}

func isNotBlank(r rune) bool {
	return !unicode.IsSpace(r)
}

// openBlockStart returns the buffer index of the BEGIN marker of a PEM block that is still open, or -1.
// A block is closed when its END marker is followed by a newline, which is the line boundary the release
// cut already covers. Closed blocks are skipped; the search state is kept between calls.
func (g *streamSegmenter) openBlockStart() int {
	g.scanFrom = min(g.scanFrom, len(g.buf))
	for {
		rel := bytes.Index(g.buf[g.scanFrom:], pemBeginMarker)
		if rel < 0 {
			// A BEGIN marker may be split across deltas; keep its last bytes in the next search.
			g.scanFrom = max(g.scanFrom, len(g.buf)-(len(pemBeginMarker)-1))
			return -1
		}
		begin := g.scanFrom + rel
		if endRel := bytes.Index(g.buf[begin:], pemEndMarker); endRel >= 0 {
			if endLine := bytes.IndexByte(g.buf[begin+endRel:], '\n'); endLine >= 0 {
				g.scanFrom = begin + endRel + endLine + 1
				continue
			}
		}
		g.scanFrom = begin
		return begin
	}
}

// segment scrubs one released run and wraps it as a response part of its kind. Accepted residual: a secret
// spanning a thinking-to-text kind change is scrubbed in two pieces and can leak.
func (g *streamSegmenter) segment(kind segmentKind, raw string) *operatorv1.InferenceResponsePart {
	scrubbed := g.scrubber.ScrubText(raw)
	if kind == segmentThinking {
		return &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Thinking{Thinking: scrubbed}}
	}
	return &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Text{Text: scrubbed}}
}

// toolCallPart scrubs the name and arguments of one tool call and keeps its call ID.
func (g *streamSegmenter) toolCallPart(call *operatorv1.InferenceToolCall) *operatorv1.InferenceResponsePart {
	return &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_ToolCall{
		ToolCall: &operatorv1.InferenceToolCall{
			CallId:        call.GetCallId(),
			Name:          g.scrubValue(call.GetName()),
			ArgumentsJson: g.scrubValue(call.GetArgumentsJson()),
		},
	}}
}

// scrubValue scrubs one non-empty string field; an empty field stays empty.
func (g *streamSegmenter) scrubValue(value string) string {
	if value == "" {
		return ""
	}
	return g.scrubber.ScrubText(value)
}

// publish sends the released segments as one progress event with the next contiguous sequence. Progress
// is delivery telemetry: a reporter error stops further progress for this attempt and never changes the
// terminal result, which is still the concatenation of the released segments.
func (g *streamSegmenter) publish(segments []*operatorv1.InferenceResponsePart, servedModel string, timeToFirstToken *int64) error {
	if len(segments) == 0 || g.reporter == nil {
		return nil
	}
	g.sequence++
	event := &operatorv1.InferenceProgressEvent{
		ProviderAttemptId: g.providerAttemptID,
		Sequence:          g.sequence,
		Parts:             segments,
		ServedModel:       servedModel,
	}
	if g.sequence == 1 && timeToFirstToken != nil {
		ttft := *timeToFirstToken
		event.TimeToFirstTokenNs = &ttft
	}
	if err := g.reporter(event); err != nil {
		g.reporter = nil
		return err
	}
	return nil
}
