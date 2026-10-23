// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// schemaDoc is the subset of protocol/models/event_registry.schema.json that
// the parity test pins against constgen's Go checks.
type schemaDoc struct {
	Required   []string `json:"required"`
	Properties struct {
		GoConst     schemaProperty `json:"_go_const"`
		Value       schemaProperty `json:"value"`
		Kind        schemaProperty `json:"kind"`
		Transport   schemaProperty `json:"transport"`
		Producers   schemaProperty `json:"producers"`
		Persistence schemaProperty `json:"persistence"`
		Governance  struct {
			Required []string `json:"required"`
		} `json:"governance"`
	} `json:"properties"`
}

type schemaProperty struct {
	Enum    []string        `json:"enum"`
	Pattern string          `json:"pattern"`
	Items   *schemaProperty `json:"items"`
}

// allowed returns the enum for a property, reading through items for arrays.
func (p schemaProperty) allowed() []string {
	if p.Items != nil {
		return p.Items.Enum
	}
	return p.Enum
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestEventRegistrySchema_PinsConstgenChecks fails when event_registry.schema.json
// and constgen's Go checks disagree on any enum, required list, or pattern.
// Changing one side without the other is a test failure, not silent drift.
func TestEventRegistrySchema_PinsConstgenChecks(t *testing.T) {
	root, err := findRepoRoot()
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, relPathEventSchema))
	require.NoError(t, err)
	var schema schemaDoc
	require.NoError(t, json.Unmarshal(data, &schema))
	props := schema.Properties

	t.Run("kind enum", func(t *testing.T) {
		require.Equal(t, sortedMapKeys(allowedKinds), sortedCopy(props.Kind.allowed()))
	})
	t.Run("transport enum", func(t *testing.T) {
		require.Equal(t, sortedMapKeys(allowedTransport), sortedCopy(props.Transport.allowed()))
	})
	t.Run("producers enum", func(t *testing.T) {
		require.Equal(t, sortedMapKeys(allowedProducers), sortedCopy(props.Producers.allowed()))
	})
	t.Run("persistence enum", func(t *testing.T) {
		require.Equal(t, sortedMapKeys(allowedPersistence), sortedCopy(props.Persistence.allowed()))
	})
	t.Run("entry required fields", func(t *testing.T) {
		require.Equal(t, sortedCopy(requiredEventFields), sortedCopy(schema.Required))
	})
	t.Run("governance required fields", func(t *testing.T) {
		require.Equal(t, sortedCopy(requiredGovernanceFields), sortedCopy(props.Governance.Required))
	})
	t.Run("_go_const pattern", func(t *testing.T) {
		require.Equal(t, goConstPat.String(), props.GoConst.Pattern)
	})
	t.Run("wire value pattern", func(t *testing.T) {
		require.Equal(t, wirePattern.String(), props.Value.Pattern)
	})
}
