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
)

// decodeAnswer decodes a model's JSON answer into v. The answer must be one
// JSON object with only known fields, optionally inside a single markdown
// code fence. Anything else is an invalid answer; no keyword is ever read
// out of free text.
func decodeAnswer(text string, v any) error {
	body := strings.TrimSpace(text)
	if strings.HasPrefix(body, "```") && strings.HasSuffix(body, "```") && len(body) >= 6 {
		body = strings.TrimSpace(body[3 : len(body)-3])
		body = strings.TrimSpace(strings.TrimPrefix(body, "json"))
	}
	if !strings.HasPrefix(body, "{") {
		return fmt.Errorf("%w: answer is not a JSON object", constants.ErrTribunalAnswerInvalid)
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("%w: %v", constants.ErrTribunalAnswerInvalid, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("%w: trailing content after JSON object", constants.ErrTribunalAnswerInvalid)
	}
	return nil
}
