// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostureRequirements(t *testing.T) {
	t.Parallel()

	cases := []struct {
		posture    Posture
		requiresL2 bool
		requiresL3 bool
	}{
		{PostureDoctrine, false, false},
		{PostureConsensus, true, false},
		{PostureRatify, false, true},
		{PostureNotary, true, true},
	}
	require.Len(t, AllPostures, len(cases), "every posture has a requirement row")
	for _, tc := range cases {
		t.Run(string(tc.posture), func(t *testing.T) {
			t.Parallel()
			assert.True(t, tc.posture.Valid())
			assert.Equal(t, tc.requiresL2, tc.posture.RequiresL2())
			assert.Equal(t, tc.requiresL3, tc.posture.RequiresL3())
			assert.Contains(t, tc.posture.Description(), string(tc.posture))
		})
	}
}

func TestPostureUnknownFailsClosed(t *testing.T) {
	t.Parallel()

	for _, p := range []Posture{"", "Doctrine", "strict", " notary"} {
		assert.False(t, p.Valid(), "%q", p)
		assert.True(t, p.RequiresL2(), "an unrecognized posture %q gates on L2", p)
		assert.True(t, p.RequiresL3(), "an unrecognized posture %q gates on L3", p)
	}
}

func TestParsePosture(t *testing.T) {
	t.Parallel()

	for _, p := range AllPostures {
		got, err := ParsePosture(string(p))
		require.NoError(t, err)
		assert.Equal(t, p, got)
	}
	for _, name := range []string{"", "NOTARY", "strict"} {
		_, err := ParsePosture(name)
		assert.True(t, errors.Is(err, ErrInvalidPosture), "%q: %v", name, err)
	}
}

func TestDefaultPostureIsDoctrine(t *testing.T) {
	t.Parallel()

	assert.Equal(t, PostureDoctrine, DefaultPosture)
}

func TestPostureDescription_AllPostures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		posture  Posture
		expected string
	}{
		{PostureDoctrine, "doctrine (L1 enforced, L2/L3 audited)"},
		{PostureConsensus, "consensus (L1/L2 enforced, L3 audited)"},
		{PostureRatify, "ratify (L1/L3 enforced, L2 audited)"},
		{PostureNotary, "notary (L1/L2/L3 strictly enforced)"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, tc.posture.Description(), "%s", tc.posture)
	}
}

func TestPostureHumanApprovalReasonNamesPosture(t *testing.T) {
	t.Parallel()

	for _, p := range AllPostures {
		reason := p.HumanApprovalReason()
		if !p.RequiresL3() {
			assert.Empty(t, reason, "%s requires no human approval", p)
			continue
		}
		assert.Contains(t, reason, string(p))
		assert.Contains(t, reason, "passkey")
	}
}
