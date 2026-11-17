// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"slices"
	"sort"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/jsonschema"
)

// ValidatePortableResponseSchema reports whether a structured-output response
// schema uses only the subset every inference provider applies, so one schema
// constrains every model the same way:
//   - the root is an object schema;
//   - every object sets additionalProperties false and lists every property in
//     required, exactly once;
//   - every array has items;
//   - type is one name, never a list; {"type":"null"} appears only as an anyOf
//     branch, and anyOf never appears at the root;
//   - enum holds distinct scalars matching the declared type;
//   - description is a string;
//   - no other keyword (const, oneOf, allOf, pattern, format, length or range
//     constraints) appears.
//
// A schema outside the subset fails with ErrInferenceCapabilityUnsupported;
// malformed JSON fails with ErrInferenceJSONInvalid.
func ValidatePortableResponseSchema(schema string) error {
	// Inspect the source JSON tree so unknown keywords and invalid keyword
	// values survive decoding and can be reported at their source paths
	// (INV-TYPE-01). UseNumber preserves enum values without float64 rounding.
	var root any
	decoder := json.NewDecoder(strings.NewReader(schema))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return fmt.Errorf("response schema: %w: %w", constants.ErrInferenceJSONInvalid, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return fmt.Errorf("response schema: %w: %w", constants.ErrInferenceJSONInvalid, err)
		}
		return fmt.Errorf("response schema: trailing JSON value: %w", constants.ErrInferenceJSONInvalid)
	}
	// Reuse the shared compiler's strict JSON decoding and resource bounds;
	// encoding/json alone silently accepts duplicate schema keys.
	if _, err := jsonschema.NewCompiler().Compile([]byte(schema)); err != nil {
		return fmt.Errorf("response schema: %w: %w", constants.ErrInferenceCapabilityUnsupported, err)
	}
	node, ok := root.(map[string]any)
	if !ok || node["type"] != "object" {
		return portableSchemaError("", "root must be an object schema")
	}
	return validatePortableSchemaNode(node, "", false)
}

func validatePortableSchemaNode(node map[string]any, path string, anyOfBranch bool) error {
	for keyword := range node {
		switch keyword {
		case "additionalProperties", "anyOf", "description", "enum", "items", "properties", "required", "type":
		default:
			return portableSchemaError(path, fmt.Sprintf("keyword %q is not portable", keyword))
		}
	}
	if description, present := node["description"]; present {
		if _, ok := description.(string); !ok {
			return portableSchemaError(path, "description must be a string")
		}
	}
	if branches, ok := node["anyOf"]; ok {
		return validatePortableAnyOf(node, branches, path)
	}
	kind, ok := node["type"].(string)
	if !ok {
		return portableSchemaError(path, "type must be one JSON type name")
	}
	switch kind {
	case "array", "boolean", "integer", "null", "number", "object", "string":
	default:
		return portableSchemaError(path, "type must be one JSON type name")
	}
	if kind == "null" && (!anyOfBranch || len(node) != 1) {
		return portableSchemaError(path, `{"type":"null"} is allowed only as an anyOf branch`)
	}
	if err := validatePortableEnum(node, path); err != nil {
		return err
	}
	for _, keyword := range []string{"properties", "required", "additionalProperties"} {
		if _, ok := node[keyword]; ok && kind != "object" {
			return portableSchemaError(path, fmt.Sprintf("%s applies only to an object", keyword))
		}
	}
	if _, ok := node["items"]; ok && kind != "array" {
		return portableSchemaError(path, "items applies only to an array")
	}
	switch kind {
	case "object":
		return validatePortableObject(node, path)
	case "array":
		items, ok := node["items"].(map[string]any)
		if !ok {
			return portableSchemaError(path, "an array must declare items")
		}
		return validatePortableSchemaNode(items, path+"/items", false)
	}
	return nil
}

func validatePortableAnyOf(node map[string]any, branches any, path string) error {
	if path == "" {
		return portableSchemaError(path, "anyOf is not allowed at the root")
	}
	for keyword := range node {
		if keyword != "anyOf" && keyword != "description" {
			return portableSchemaError(path, fmt.Sprintf("anyOf cannot sit beside %q", keyword))
		}
	}
	list, ok := branches.([]any)
	if !ok || len(list) < 2 {
		return portableSchemaError(path, "anyOf must list at least two schemas")
	}
	for i, branch := range list {
		child, ok := branch.(map[string]any)
		if !ok {
			return portableSchemaError(fmt.Sprintf("%s/anyOf/%d", path, i), "branch must be a schema object")
		}
		if err := validatePortableSchemaNode(child, fmt.Sprintf("%s/anyOf/%d", path, i), true); err != nil {
			return err
		}
	}
	return nil
}

func validatePortableEnum(node map[string]any, path string) error {
	raw, ok := node["enum"]
	if !ok {
		return nil
	}
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return portableSchemaError(path, "enum must list at least one value")
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		key := fmt.Sprintf("%T:%v", value, value)
		var matchesType bool
		switch value := value.(type) {
		case string:
			matchesType = node["type"] == "string"
		case bool:
			matchesType = node["type"] == "boolean"
		case json.Number:
			// Compare exact numeric values: 1 and 1.0 are duplicates, while
			// adjacent integers above float64's precision remain distinct.
			number, ok := new(big.Rat).SetString(string(value))
			if !ok {
				return portableSchemaError(path, "enum contains an invalid number")
			}
			matchesType = node["type"] == "number" || (node["type"] == "integer" && number.IsInt())
			key = "number:" + number.RatString()
		default:
			return portableSchemaError(path, "enum values must be scalars")
		}
		if !matchesType {
			return portableSchemaError(path, "enum values must match the declared type")
		}
		if seen[key] {
			return portableSchemaError(path, "enum values must be distinct")
		}
		seen[key] = true
	}
	return nil
}

func validatePortableObject(node map[string]any, path string) error {
	if node["additionalProperties"] != false {
		return portableSchemaError(path, "an object must set additionalProperties false")
	}
	properties, ok := node["properties"].(map[string]any)
	if !ok || len(properties) == 0 {
		return portableSchemaError(path, "an object must declare properties")
	}
	required, ok := node["required"].([]any)
	if !ok {
		return portableSchemaError(path, "an object must list required")
	}
	names := make([]string, 0, len(required))
	for _, name := range required {
		text, ok := name.(string)
		if !ok || slices.Contains(names, text) {
			return portableSchemaError(path, "required must list property names once each")
		}
		names = append(names, text)
	}
	declared := make([]string, 0, len(properties))
	for name := range properties {
		declared = append(declared, name)
	}
	sort.Strings(declared)
	sort.Strings(names)
	if !slices.Equal(declared, names) {
		return portableSchemaError(path, "required must list every property")
	}
	for _, name := range declared {
		child, ok := properties[name].(map[string]any)
		if !ok {
			return portableSchemaError(path+"/properties/"+name, "property must be a schema object")
		}
		if err := validatePortableSchemaNode(child, path+"/properties/"+name, false); err != nil {
			return err
		}
	}
	return nil
}

func portableSchemaError(path, reason string) error {
	if path == "" {
		path = "/"
	}
	return fmt.Errorf("response schema %s: %s: %w", path, reason, constants.ErrInferenceCapabilityUnsupported)
}
