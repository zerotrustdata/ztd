// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// The Triage response schema must be canonical (the Inference Operator refuses
// anything else) and must admit exactly the labels the strict parser accepts.
func TestTriageResultJSONSchemaIsCanonicalAndMatchesParser(t *testing.T) {
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(triageResultJSONSchema), &decoded))
	canonical, err := json.Marshal(decoded)
	require.NoError(t, err)
	assert.Equal(t, triageResultJSONSchema, string(canonical))

	properties := decoded["properties"].(map[string]any)
	enum := func(field string) []string {
		var values []string
		for _, value := range properties[field].(map[string]any)["enum"].([]any) {
			values = append(values, value.(string))
		}
		return values
	}
	for _, value := range enum("complexity") {
		assert.True(t, triageComplexityValid(constants.TriageComplexity(value)), value)
	}
	for _, value := range enum("intent") {
		assert.True(t, triageIntentValid(constants.TriageIntent(value)), value)
	}
	for _, value := range enum("request_posture") {
		assert.True(t, triagePostureValid(constants.TriagePosture(value)), value)
	}
	required := decoded["required"].([]any)
	assert.Len(t, required, reflect.TypeOf(TriageResult{}).NumField(), "every TriageResult field is required")
}
