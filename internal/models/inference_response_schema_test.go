// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestValidatePortableResponseSchema(t *testing.T) {
	accepted := map[string]string{
		"flat object": `{"additionalProperties":false,"properties":{"answer":{"type":"string"}},"required":["answer"],"type":"object"}`,
		"enum and items": `{"additionalProperties":false,"properties":{"args":{"items":{"type":"string"},"type":"array"},` +
			`"score":{"enum":[1,2,3],"type":"integer"}},"required":["score","args"],"type":"object"}`,
		"nullable object": `{"additionalProperties":false,"properties":{"revision":{"anyOf":[{"additionalProperties":false,` +
			`"properties":{"command":{"type":"string"}},"required":["command"],"type":"object"},{"type":"null"}]}},"required":["revision"],"type":"object"}`,
		"description": `{"additionalProperties":false,"properties":{"x":{"description":"why","type":"boolean"}},"required":["x"],"type":"object"}`,
		"precise numeric enums": `{"additionalProperties":false,"properties":{"a":{"enum":[9007199254740992,9007199254740993],"type":"integer"},` +
			`"b":{"enum":[1.5,2e-3],"type":"number"},"c":{"enum":[true,false],"type":"boolean"}},"required":["a","b","c"],"type":"object"}`,
	}
	for name, schema := range accepted {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, ValidatePortableResponseSchema(schema))
		})
	}

	rejected := map[string]string{
		"root array":                   `{"items":{"type":"string"},"type":"array"}`,
		"root anyOf":                   `{"anyOf":[{"additionalProperties":false,"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"},{"type":"null"}]}`,
		"root oneOf":                   `{"oneOf":[{"additionalProperties":false,"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}]}`,
		"minLength":                    `{"additionalProperties":false,"properties":{"a":{"minLength":1,"type":"string"}},"required":["a"],"type":"object"}`,
		"minimum":                      `{"additionalProperties":false,"properties":{"a":{"minimum":1,"type":"integer"}},"required":["a"],"type":"object"}`,
		"pattern":                      `{"additionalProperties":false,"properties":{"a":{"pattern":"^x$","type":"string"}},"required":["a"],"type":"object"}`,
		"const":                        `{"additionalProperties":false,"properties":{"a":{"const":"x"}},"required":["a"],"type":"object"}`,
		"optional property":            `{"additionalProperties":false,"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a"],"type":"object"}`,
		"duplicate required":           `{"additionalProperties":false,"properties":{"a":{"type":"string"}},"required":["a","a"],"type":"object"}`,
		"unknown required":             `{"additionalProperties":false,"properties":{"a":{"type":"string"}},"required":["a","b"],"type":"object"}`,
		"open object":                  `{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
		"nested open object":           `{"additionalProperties":false,"properties":{"a":{"properties":{"b":{"type":"string"}},"required":["b"],"type":"object"}},"required":["a"],"type":"object"}`,
		"array without items":          `{"additionalProperties":false,"properties":{"a":{"type":"array"}},"required":["a"],"type":"object"}`,
		"type list":                    `{"additionalProperties":false,"properties":{"a":{"type":["string","null"]}},"required":["a"],"type":"object"}`,
		"bare null property":           `{"additionalProperties":false,"properties":{"a":{"type":"null"}},"required":["a"],"type":"object"}`,
		"single-branch anyOf":          `{"additionalProperties":false,"properties":{"a":{"anyOf":[{"type":"string"}]}},"required":["a"],"type":"object"}`,
		"anyOf beside type":            `{"additionalProperties":false,"properties":{"a":{"anyOf":[{"type":"string"},{"type":"null"}],"type":"string"}},"required":["a"],"type":"object"}`,
		"empty enum":                   `{"additionalProperties":false,"properties":{"a":{"enum":[],"type":"string"}},"required":["a"],"type":"object"}`,
		"non-string description":       `{"additionalProperties":false,"properties":{"a":{"description":42,"type":"string"}},"required":["a"],"type":"object"}`,
		"non-string union description": `{"additionalProperties":false,"properties":{"a":{"description":false,"anyOf":[{"type":"string"},{"type":"null"}]}},"required":["a"],"type":"object"}`,
		"enum type mismatch":           `{"additionalProperties":false,"properties":{"a":{"enum":[1],"type":"string"}},"required":["a"],"type":"object"}`,
		"mixed enum types":             `{"additionalProperties":false,"properties":{"a":{"enum":["yes",true],"type":"string"}},"required":["a"],"type":"object"}`,
		"fractional integer enum":      `{"additionalProperties":false,"properties":{"a":{"enum":[1.5],"type":"integer"}},"required":["a"],"type":"object"}`,
		"duplicate enum values":        `{"additionalProperties":false,"properties":{"a":{"enum":[1,1.0],"type":"number"}},"required":["a"],"type":"object"}`,
		"duplicate schema keyword":     `{"additionalProperties":false,"properties":{"a":{"type":"integer","type":"string"}},"required":["a"],"type":"object"}`,
		"items on a string":            `{"additionalProperties":false,"properties":{"a":{"items":{"type":"string"},"type":"string"}},"required":["a"],"type":"object"}`,
		"no properties object":         `{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`,
	}
	for name, schema := range rejected {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, ValidatePortableResponseSchema(schema), constants.ErrInferenceCapabilityUnsupported)
		})
	}

	require.ErrorIs(t, ValidatePortableResponseSchema(`{`), constants.ErrInferenceJSONInvalid)
	require.ErrorIs(t, ValidatePortableResponseSchema(accepted["flat object"]+` true`), constants.ErrInferenceJSONInvalid)
	require.ErrorIs(t, ValidatePortableResponseSchema(accepted["flat object"]+` trailing`), constants.ErrInferenceJSONInvalid)
}
