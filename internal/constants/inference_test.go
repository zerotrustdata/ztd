// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAgentHostedInferenceProvider(t *testing.T) {
	cases := []struct {
		name      string
		agent     AgentBinary
		wantClass string
		wantOK    bool
	}{
		{
			name:      "claude maps to anthropic",
			agent:     AgentBinaryClaude,
			wantClass: InferenceProviderAnthropic,
			wantOK:    true,
		},
		{
			name:      "codex maps to openai",
			agent:     AgentBinaryCodex,
			wantClass: InferenceProviderOpenAI,
			wantOK:    true,
		},
		{
			name:      "gemini maps to gemini",
			agent:     AgentBinaryGemini,
			wantClass: InferenceProviderGemini,
			wantOK:    true,
		},
		{
			name:   "devin has no matching hosted provider",
			agent:  AgentBinaryDevin,
			wantOK: false,
		},
		{
			name:   "goose has no matching hosted provider",
			agent:  AgentBinaryGoose,
			wantOK: false,
		},
		{
			name:   "unknown agent has no matching hosted provider",
			agent:  AgentBinary("cursor"),
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotClass, gotOK := AgentHostedInferenceProvider(tc.agent)
			assert.Equal(t, tc.wantOK, gotOK)
			assert.Equal(t, tc.wantClass, gotClass)
			if gotOK {
				assert.True(t, IsHostedInferenceProvider(gotClass))
			}
		})
	}
}
