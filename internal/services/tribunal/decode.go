// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/jsonschema"
)

// decodeAnswer decodes a model's JSON answer into v. The answer must be one
// JSON object with only known fields. Anything else is an invalid answer; no keyword is ever read
// out of free text.
func decodeAnswer(text string, v any) error {
	text = strings.Trim(text, " \t\r\n")
	if len(text) == 0 || text[0] != '{' {
		return fmt.Errorf("%w: answer is not a JSON object", constants.ErrTribunalAnswerInvalid)
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrTribunalAnswerInvalid, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("%w: trailing content after JSON object", constants.ErrTribunalAnswerInvalid)
	}
	return nil
}

// decodeAuthoringAnswer enforces the same schema sent to the provider.
// encoding/json alone accepts missing fields and case-insensitive keys.
// Decode first to give actionable diagnostics for malformed JSON and unknown
// fields before schema validation. The schema holds only constraints every
// provider applies; each caller checks the rest (a valid executable name, the
// fields a verdict carries).
func decodeAuthoringAnswer(text, schemaJSON string, answer any) error {
	if err := decodeAnswer(text, answer); err != nil {
		return err
	}
	schema, err := jsonschema.NewCompiler().Compile([]byte(schemaJSON))
	if err != nil {
		return fmt.Errorf("tribunal: compile answer schema: %w", err)
	}
	if failures := jsonschema.NewValidator().Validate(schema, []byte(text)); len(failures) > 0 {
		return fmt.Errorf("%w: %w", constants.ErrTribunalAnswerInvalid, failures)
	}
	return nil
}

// authoredText returns a model's trimmed explanation field. The output
// contracts describe these fields by type, never by an example value, and an
// answer that copies the template instead of writing is invalid: an empty
// value, a value with no letter or digit (such as "..."), the bare type word
// "string", or a lone angle-bracket placeholder (such as "<why>").
func authoredText(field, value string) (string, error) {
	text := strings.TrimSpace(value)
	switch {
	case text == "":
		return "", fmt.Errorf("%w: %s is empty", constants.ErrTribunalAnswerInvalid, field)
	case !strings.ContainsFunc(text, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }):
		return "", fmt.Errorf("%w: %s %q has no words", constants.ErrTribunalAnswerInvalid, field, text)
	case strings.EqualFold(text, "string"), templatePlaceholder(text):
		return "", fmt.Errorf("%w: %s %q is a template placeholder, not an explanation", constants.ErrTribunalAnswerInvalid, field, text)
	}
	return text, nil
}

// templatePlaceholder reports whether text is one <...> placeholder and
// nothing else.
func templatePlaceholder(text string) bool {
	return len(text) >= 2 && text[0] == '<' && text[len(text)-1] == '>' && !strings.ContainsAny(text[1:len(text)-1], "<>")
}
