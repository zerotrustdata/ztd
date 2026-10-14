// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocumentResponse_GetString_AbsentField(t *testing.T) {
	doc := DocumentResponse{"case_title": json.RawMessage(`"hello"`)}
	title, ok := doc.GetString("case_title")
	require.True(t, ok)
	assert.Equal(t, "hello", title)
	_, ok = doc.GetString("absent_field")
	assert.False(t, ok)
}

func TestDocumentResponse_GetBool_AbsentField(t *testing.T) {
	doc := DocumentResponse{"case_flag": json.RawMessage(`true`)}
	flag, ok := doc.GetBool("case_flag")
	require.True(t, ok)
	assert.True(t, flag)
	_, ok = doc.GetBool("absent_field")
	assert.False(t, ok)
}

func TestDocumentResponse_GetString_WrongType(t *testing.T) {
	doc := DocumentResponse{"case_title": json.RawMessage(`42`), "case_flag": json.RawMessage(`"true"`)}
	_, ok := doc.GetString("case_title")
	assert.False(t, ok, "a number must not read as a string")
	_, ok = doc.GetBool("case_flag")
	assert.False(t, ok, "a string must not read as a bool")
}

func TestInvestigationUpdate_OmitsUnsetFields(t *testing.T) {
	title := "Patched Title"
	updates, err := documentUpdateStruct(InvestigationUpdate{CaseTitle: &title})
	require.NoError(t, err)
	require.NotNil(t, updates)
	assert.Equal(t, "Patched Title", updates.Fields["case_title"].GetStringValue())
	_, hasStatus := updates.Fields["status"]
	assert.False(t, hasStatus)
}

func strPtr(value string) *string {
	return &value
}

// assertJSONField unmarshals a JSON string field from the envelope map and
// asserts it equals the expected value.
func assertJSONField(t *testing.T, env map[string]json.RawMessage, field, expected string) {
	t.Helper()
	raw, ok := env[field]
	require.True(t, ok, "envelope must contain field %q", field)
	var got string
	require.NoError(t, json.Unmarshal(raw, &got), "field %q must be a JSON string", field)
	assert.Equal(t, expected, got, "field %q mismatch", field)
}
