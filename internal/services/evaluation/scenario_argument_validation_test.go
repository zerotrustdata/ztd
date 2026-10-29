// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestValidateScenarioContract_RejectsUngradableArgumentRules(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	tests := []struct {
		name   string
		base   func() ScenarioBlueprint
		mutate func(*ScenarioBlueprint)
	}{
		{"regex used as its own sample", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Input.UserPrompt += ` Use the pattern ^AUTH_FAILURE$.`
			b.Gold.PromptHint.Arguments[0].Source = sourcePrompt
			b.Gold.PromptHint.Arguments[0].Value = `^AUTH_FAILURE$`
			b.Gold.ArgumentValidators[0].Arguments[0].RegexMatches = []string{`^AUTH_FAILURE$`}
		}},
		{"reference pattern matches a rejected sample", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.ArgumentValidators[0].Arguments[0].RegexRejects = []string{"AUTH_FAILURE"}
		}},
		{"reference pattern does not compile", recoveryErrorGuidedRetry, func(b *ScenarioBlueprint) {
			b.Input.Seed.Turns[0].Content += " Use the pattern [."
			b.Gold.PromptHint.Arguments[0].Value = "["
		}},
		{"runtime token regex fails its search objective", securityScrubbedTokenPassthrough, func(b *ScenarioBlueprint) {
			b.Gold.ArgumentValidators[1].Arguments[0].RegexMatches = []string{`\{\{UEI_[0-9a-f]{12}_\d+\}\}`}
			b.Gold.ArgumentValidators[1].Arguments[0].RegexRejects = []string{"4111", "charge"}
		}},
		{"regex argument has no hint", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.ArgumentValidators = append(b.Gold.ArgumentValidators, ToolArgumentValidator{
				ToolName:  constants.MCPToolReadFile,
				Arguments: []ToolArgumentConstraint{{Name: "path", RegexMatches: []string{"x"}, RegexRejects: []string{"y"}}},
			})
		}},
		{"model authored regex has no search objective", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.ContentCheck.SearchPattern = ""
		}},
		{"concrete value violates another constraint", toolArgGrepPattern, func(b *ScenarioBlueprint) {
			b.Gold.ArgumentValidators[0].Arguments[0].ForbiddenTerms = []string{"AUTH_FAILURE"}
		}},
		{"workspace reference violates its path rule", toolSelectFileRead, func(b *ScenarioBlueprint) {
			b.Gold.ArgumentValidators[0].Arguments[0].PathEquals = ScenarioWorkspaceToken + "/other.txt"
		}},
		{"regex samples contain an unrendered workspace token", toolSelectFileRead, func(b *ScenarioBlueprint) {
			b.Gold.ArgumentValidators[0].Arguments[0].RegexMatches = []string{ScenarioWorkspaceToken + "/config/retry-config.env"}
			b.Gold.ArgumentValidators[0].Arguments[0].RegexRejects = []string{"other.txt"}
		}},
		{"seed reference violates its equality rule", recoveryErrorGuidedRetry, func(b *ScenarioBlueprint) {
			b.Gold.ArgumentValidators[0].Arguments[0].Equals = "PAYMENT_TIMEOUT"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blueprint := tt.base()
			require.NoError(t, validateScenarioContract(blueprint, catalog))
			tt.mutate(&blueprint)
			assert.ErrorIs(t, validateScenarioContract(blueprint, catalog), constants.ErrEvaluationScenarioContractInvalid)
		})
	}
}

func TestValidateScenarioContract_AcceptsGroundedArgumentRules(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	t.Run("escaped equivalent regex", func(t *testing.T) {
		blueprint := toolArgGrepPattern()
		blueprint.Input.UserPrompt += ` Use the pattern AUTH[_]FAILURE.`
		blueprint.Gold.PromptHint.Arguments[0].Source = sourcePrompt
		blueprint.Gold.PromptHint.Arguments[0].Value = "AUTH[_]FAILURE"
		require.NoError(t, validateScenarioContract(blueprint, catalog))
	})
	t.Run("model authored regex uses canonical search objective", func(t *testing.T) {
		require.NoError(t, validateScenarioContract(toolArgGrepPattern(), catalog))
	})
	t.Run("runtime token uses free text constraints", func(t *testing.T) {
		require.NoError(t, validateScenarioContract(securityScrubbedTokenPassthrough(), catalog))
	})
	t.Run("model authored command hint describes the task", func(t *testing.T) {
		require.NoError(t, validateScenarioContract(toolArgShellCommand(), catalog))
	})
}
