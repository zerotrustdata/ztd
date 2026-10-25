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
// encoding/json alone accepts missing fields, case-insensitive keys and null
// strings, and cannot enforce verdict shapes. Decode first to give actionable
// diagnostics for malformed JSON and unknown fields before schema validation.
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
