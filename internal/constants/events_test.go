// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOtherNewEventConstants(t *testing.T) {
	cases := []struct {
		goConst EventType
		value   string
	}{
		{EventOperatorReputationStakeResolutionCreated, "ztd.v1.operator.reputation.stake.resolution.created"},
		{EventAppCaseDeleted, "ztd.v1.app.case.deleted"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestObservabilityDashboardEventConstants(t *testing.T) {
	cases := []struct {
		goConst EventType
		value   string
	}{
		{EventAppAgentStatusUpdated, "ztd.v1.app.agent.status.updated"},
		{EventAppRunStatusUpdated, "ztd.v1.app.run.status.updated"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestOperatorIntentEventConstants(t *testing.T) {
	cases := []struct {
		goConst EventType
		value   string
	}{
		{EventOperatorIntentApprovalGranted, "ztd.v1.operator.intent.approval.granted"},
		{EventOperatorIntentApprovalRejected, "ztd.v1.operator.intent.approval.rejected"},
		{EventOperatorIntentApprovalRequested, "ztd.v1.operator.intent.approval.requested"},
		{EventOperatorIntentDenied, "ztd.v1.operator.intent.denied"},
		{EventOperatorIntentGranted, "ztd.v1.operator.intent.granted"},
		{EventOperatorIntentRequested, "ztd.v1.operator.intent.requested"},
		{EventOperatorIntentRevokeRequested, "ztd.v1.operator.intent.revoke.requested"},
		{EventOperatorIntentRevoked, "ztd.v1.operator.intent.revoked"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.value, string(tc.goConst))
	}
}

func TestEventsJSONValidity(t *testing.T) {
	data, err := os.ReadFile("../../protocol/constants/events.json")
	require.NoError(t, err)
	assert.True(t, json.Valid(data), "events.json is valid JSON")
}

func TestEventsJSONGoConstPresence(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile("../../protocol/constants/events.json")
	require.NoError(t, err)
	var raw struct {
		Events map[string]struct {
			GoConst string `json:"_go_const"`
			Value   string `json:"value"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(data, &raw))
	for key, meta := range raw.Events {
		assert.NotEmpty(t, meta.GoConst, "event %s missing _go_const", key)
		assert.NotEmpty(t, meta.Value, "event %s missing value", key)
	}
}

func loadEventsJSONRegistry(t *testing.T) map[string]struct {
	GoConst string `json:"_go_const"`
	Value   string `json:"value"`
} {
	t.Helper()
	data, err := os.ReadFile("../../protocol/constants/events.json")
	require.NoError(t, err)
	var raw struct {
		Events map[string]struct {
			GoConst string `json:"_go_const"`
			Value   string `json:"value"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(data, &raw))
	return raw.Events
}

func loadGoEventConstants(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("events_gen.go")
	require.NoError(t, err)
	re := regexp.MustCompile(`^const (Event[A-Za-z0-9]+) EventType = "(ztd\.v1\.[^"]+)"`)
	goConsts := make(map[string]string)
	for _, line := range regexp.MustCompile(`\r?\n`).Split(string(data), -1) {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		goConsts[m[1]] = m[2]
	}
	require.NotEmpty(t, goConsts, "failed to parse EventType constants from events_gen.go")
	return goConsts
}

func TestEventsJSONGoRegistryParity(t *testing.T) {
	jsonEvents := loadEventsJSONRegistry(t)
	goEvents := loadGoEventConstants(t)

	jsonByGoConst := make(map[string]string, len(jsonEvents))
	valueOwners := make(map[string]string, len(jsonEvents))
	for key, meta := range jsonEvents {
		jsonByGoConst[meta.GoConst] = meta.Value
		if owner, exists := valueOwners[meta.Value]; exists {
			t.Errorf("duplicate wire value %q: %s and %s", meta.Value, owner, key)
			continue
		}
		valueOwners[meta.Value] = key
	}

	for goConst, goValue := range goEvents {
		jsonValue, ok := jsonByGoConst[goConst]
		if !ok {
			t.Errorf("Go constant %s missing from events.json", goConst)
			continue
		}
		assert.Equal(t, goValue, jsonValue, "wire value mismatch for %s", goConst)
	}

	for goConst := range jsonByGoConst {
		if _, ok := goEvents[goConst]; !ok {
			t.Errorf("events.json _go_const %s missing from events_gen.go", goConst)
		}
	}

	assert.Equal(t, len(goEvents), len(jsonEvents), "Go and JSON event counts must match")
}
