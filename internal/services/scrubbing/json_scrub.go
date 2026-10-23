// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package scrubbing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// jsonScrubFrame tracks one open JSON object or array while scrubbing.
type jsonScrubFrame struct {
	object    bool
	expectKey bool
	// key is the most recent member key of an object frame.
	key string
	// sensitive marks an array frame whose scalar elements are tokenized
	// whole because the array is the value of a sensitive key.
	sensitive bool
}

// jsonTokenSeparators are the bytes json.Decoder consumes between tokens.
const jsonTokenSeparators = " \t\r\n,:"

// scrubJSONText scrubs a JSON object or array token by token and splices each
// scrubbed scalar back in place, so the result is still valid JSON with its key
// order and layout unchanged. Text scrubbing over the raw document would
// instead write bare UEI tokens where JSON expects a value.
//
// An object member is scrubbed as `key: value` text, so key-anchored patterns
// still see their key; a member whose key the scrubbers rewrite is tokenized
// whole. A string that is itself a JSON document is scrubbed structurally. In
// strict mode the scalar value of a sensitive key, and every scalar element of
// an array under one, is tokenized whole; a number tokenized that way becomes a
// JSON string holding its token. ok is false when input is not a JSON object or
// array; the caller then scrubs it as text.
func (s *ScrubbingService) scrubJSONText(ctx context.Context, input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') || !json.Valid([]byte(input)) {
		return "", false
	}

	dec := json.NewDecoder(strings.NewReader(input))
	dec.UseNumber()
	var stack []jsonScrubFrame
	var out strings.Builder
	last := 0
	for {
		prev := dec.InputOffset()
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", false
		}
		start, end := jsonTokenSpan(input, int(prev), int(dec.InputOffset()))

		var raw, scrubbed string
		switch value := tok.(type) {
		case json.Delim:
			switch value {
			case '{', '[':
				sensitive := s.jsonValueSensitive(stack)
				markJSONValueRead(stack)
				stack = append(stack, jsonScrubFrame{object: value == '{', expectKey: value == '{', sensitive: value == '[' && sensitive})
			default:
				stack = stack[:len(stack)-1]
			}
			continue
		case string:
			if top := len(stack) - 1; top >= 0 && stack[top].object && stack[top].expectKey {
				stack[top].key, stack[top].expectKey = value, false
				raw, scrubbed = value, s.ScrubText(ctx, value)
				break
			}
			raw, scrubbed = value, s.scrubJSONScalar(ctx, stack, value)
			markJSONValueRead(stack)
		case json.Number:
			// A JSON number is machine data (a counter, size, timestamp or
			// enum), never one of the text formats the patterns describe, so
			// only a sensitive key tokenizes it. A 10- or 16-digit nanosecond
			// counter would otherwise read as a phone or card number.
			raw, scrubbed = value.String(), value.String()
			if s.jsonValueSensitive(stack) {
				scrubbed = s.tokenize(ctx, raw)
			}
			markJSONValueRead(stack)
		default:
			// true, false and null carry no data to scrub.
			markJSONValueRead(stack)
			continue
		}
		if scrubbed == raw {
			continue
		}
		out.WriteString(input[last:start])
		out.WriteString(jsonQuote(scrubbed))
		last = end
	}
	if last == 0 {
		return input, true
	}
	out.WriteString(input[last:])
	return out.String(), true
}

// scrubJSONScalar scrubs one string value at the current position.
func (s *ScrubbingService) scrubJSONScalar(ctx context.Context, stack []jsonScrubFrame, value string) string {
	if s.jsonValueSensitive(stack) {
		return s.tokenize(ctx, value)
	}
	if nested, ok := s.scrubJSONText(ctx, value); ok {
		return nested
	}
	top := len(stack) - 1
	if top < 0 || !stack[top].object {
		return s.ScrubText(ctx, value)
	}
	prefix := stack[top].key + ": "
	member := s.ScrubText(ctx, prefix+value)
	if !strings.HasPrefix(member, prefix) {
		return s.tokenize(ctx, value)
	}
	return member[len(prefix):]
}

// jsonValueSensitive reports whether the next value is tokenized whole: in
// strict mode, the value of a sensitive key or an element of an array that is.
func (s *ScrubbingService) jsonValueSensitive(stack []jsonScrubFrame) bool {
	if !s.config.StrictMode || len(stack) == 0 {
		return false
	}
	top := stack[len(stack)-1]
	if top.object {
		return s.isLikelySensitiveKey(top.key)
	}
	return top.sensitive
}

// markJSONValueRead records that an object frame's member value was read, so
// its next string token is a key.
func markJSONValueRead(stack []jsonScrubFrame) {
	if top := len(stack) - 1; top >= 0 && stack[top].object {
		stack[top].expectKey = true
	}
}

// jsonTokenSpan returns the byte range of the token json.Decoder read between
// offsets prev and end, without the separators and whitespace around it.
func jsonTokenSpan(input string, prev, end int) (int, int) {
	start := prev
	for start < end && strings.IndexByte(jsonTokenSeparators, input[start]) >= 0 {
		start++
	}
	for end > start && strings.IndexByte(" \t\r\n", input[end-1]) >= 0 {
		end--
	}
	return start, end
}

// jsonQuote encodes value as a JSON string without HTML escaping, so scrubbed
// text keeps its characters as written.
func jsonQuote(value string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// Encoding a Go string cannot fail.
	_ = enc.Encode(value)
	return strings.TrimSuffix(buf.String(), "\n")
}
