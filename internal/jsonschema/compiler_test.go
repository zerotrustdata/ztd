// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version  2.0.

package jsonschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	oscalschema "github.com/zerotrustdata/ztd/protocol/schemas/oscal"
)

func TestCompiler_CompilesOSCALSchema(t *testing.T) { //nolint:paralleltest // mutates package-level state

	schemaBytes, err := oscalschema.SchemaBytes()
	require.NoError(t, err)
	c := NewCompiler()
	s, err := c.Compile(schemaBytes)
	require.NoError(t, err, "compiling the official OSCAL 1.1.2 schema must succeed")
	assert.NotNil(t, s)
	assert.Equal(t, "object", s.Type)
	assert.NotNil(t, s.Properties)
}

func TestCompiler_CompilesBooleanTrueSchema(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	s, err := c.Compile([]byte(`true`))
	require.NoError(t, err)
	assert.True(t, s.AcceptsAll())
}

func TestCompiler_CompilesBooleanFalseSchema(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	s, err := c.Compile([]byte(`false`))
	require.NoError(t, err)
	assert.True(t, s.RejectsAll())
}

func TestCompiler_RejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	_, err := c.Compile([]byte(`{invalid`))
	require.Error(t, err)
	ce, ok := err.(*CompileError)
	require.True(t, ok)
	assert.True(t, ce.Failures.Has(ReasonCompileInvalidJSON))
}

func TestCompiler_RejectsDuplicateObjectKeys(t *testing.T) {
	t.Parallel()
	compiler := NewCompiler()
	_, err := compiler.Compile([]byte(`{"type":"object","type":"array"}`))
	require.Error(t, err)
	compileErr, ok := err.(*CompileError)
	require.True(t, ok)
	assert.True(t, compileErr.Failures.Has(ReasonCompileDuplicateKey))
}

func TestCompiler_RejectsTrailingData(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	_, err := c.Compile([]byte(`{}` + "\n" + `{}`))
	require.Error(t, err)
}

func TestCompiler_RejectsUnresolvedRef(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	_, err := c.Compile([]byte(`{"$ref": "#/definitions/NonExistent"}`))
	require.Error(t, err)
	ce, ok := err.(*CompileError)
	require.True(t, ok)
	assert.True(t, ce.Failures.Has(ReasonCompileUnresolvedRef))
}

func TestCompiler_RejectsDuplicateID(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	_, err := c.Compile([]byte(`{
		"definitions": {
			"a": {"$id": "#dup", "type": "string"},
			"b": {"$id": "#dup", "type": "number"}
		}
	}`))
	require.Error(t, err)
	ce, ok := err.(*CompileError)
	require.True(t, ok)
	assert.True(t, ce.Failures.Has(ReasonCompileDuplicateID))
}

func TestCompiler_RejectsInvalidRegex(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	_, err := c.Compile([]byte(`{"pattern": "[invalid"}`))
	require.Error(t, err)
	ce, ok := err.(*CompileError)
	require.True(t, ok)
	assert.True(t, ce.Failures.Has(ReasonCompileInvalidRegex))
}

func TestCompiler_RejectsUnsupportedFormat(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	_, err := c.Compile([]byte(`{"type": "string", "format": "unknown-format"}`))
	require.Error(t, err)
	ce, ok := err.(*CompileError)
	require.True(t, ok)
	assert.True(t, ce.Failures.Has(ReasonCompileUnsupportedFormat))
}

func TestCompiler_ResolvesFragmentRef(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	s, err := c.Compile([]byte(`{
		"$id": "http://example.com/root.json",
		"definitions": {
			"foo": {
				"$id": "#foo",
				"type": "string"
			}
		},
		"properties": {
			"x": {"$ref": "#foo"}
		}
	}`))
	require.NoError(t, err)
	propX := s.Properties["x"].Resolved()
	require.NotNil(t, propX)
	assert.Equal(t, "string", propX.Type)
}

func TestCompiler_ResolvesJSONPointerRef(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	s, err := c.Compile([]byte(`{
		"definitions": {
			"foo": {"type": "integer"}
		},
		"properties": {
			"x": {"$ref": "#/definitions/foo"}
		}
	}`))
	require.NoError(t, err)
	propX := s.Properties["x"].Resolved()
	require.NotNil(t, propX)
	assert.Equal(t, "integer", propX.Type)
}

func TestCompiler_HandlesRecursiveDefinition(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	s, err := c.Compile([]byte(`{
		"definitions": {
			"node": {
				"$id": "#node",
				"type": "object",
				"properties": {
					"children": {
						"type": "array",
						"items": {"$ref": "#node"}
					}
				}
			}
		},
		"properties": {
			"root": {"$ref": "#node"}
		}
	}`))
	require.NoError(t, err)
	rootSchema := s.Properties["root"].Resolved()
	require.NotNil(t, rootSchema)
	assert.Equal(t, "object", rootSchema.Type)
	// The recursive ref should resolve without infinite loop
	childrenItems := rootSchema.Properties["children"].Items.Resolved()
	require.NotNil(t, childrenItems)
	assert.Equal(t, "object", childrenItems.Type)
}

func TestCompiler_RefOverridesAllOtherKeywords(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	s, err := c.Compile([]byte(`{
		"definitions": {
			"foo": {"type": "string"}
		},
		"properties": {
			"x": {"$ref": "#/definitions/foo", "type": "integer"}
		}
	}`))
	require.NoError(t, err)
	// Per Draft-07, $ref overrides all other keywords
	propX := s.Properties["x"]
	assert.True(t, propX.IsRef())
	resolved := propX.Resolved()
	assert.Equal(t, "string", resolved.Type)
}

func TestCompiler_SecondCompileUsesTheNewSchema(t *testing.T) {
	t.Parallel()
	c := NewCompiler()
	v := NewValidator()

	first, err := c.Compile([]byte(`{"type":"string"}`))
	require.NoError(t, err)
	second, err := c.Compile([]byte(`{"type":"number"}`))
	require.NoError(t, err)

	assert.Empty(t, v.Validate(first, []byte(`"a"`)))
	assert.NotEmpty(t, v.Validate(first, []byte(`1`)))
	assert.Empty(t, v.Validate(second, []byte(`1`)))
	assert.NotEmpty(t, v.Validate(second, []byte(`"a"`)))
}

func TestCompiler_NeverPanicsOnMalformedInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input   string
		wantErr bool
	}{
		{input: ``, wantErr: true},
		{input: `null`, wantErr: true},
		{input: `[]`, wantErr: true},
		{input: `{"type": 123}`, wantErr: true},
		{input: `{"enum": "not-an-array"}`, wantErr: true},
		// These keyword values are the wrong JSON type. Compile currently
		// ignores them and returns a schema; the assertion locks that result.
		{input: `{"properties": "not-an-object"}`},
		{input: `{"required": "not-an-array"}`},
		{input: `{"allOf": "not-an-array"}`},
		{input: `{"$ref": 123}`},
	}
	for _, tt := range cases {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			schema, err := NewCompiler().Compile([]byte(tt.input))
			if tt.wantErr {
				require.Error(t, err)
				var ce *CompileError
				require.ErrorAs(t, err, &ce)
				assert.NotEmpty(t, ce.Failures)
				assert.Nil(t, schema)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, schema)
		})
	}
}

func TestCompiler_DoesNotPerformNetworkLoading(t *testing.T) {
	t.Parallel()
	// An absolute URI ref must fail (no network loading)

	c := NewCompiler()
	_, err := c.Compile([]byte(`{"$ref": "http://example.com/schema.json"}`))
	require.Error(t, err)
	ce, ok := err.(*CompileError)
	require.True(t, ok)
	assert.True(t, ce.Failures.Has(ReasonCompileUnresolvedRef))
}
