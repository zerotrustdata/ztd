// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestSelectInferenceOperator_RequiresInferenceCapableSession(t *testing.T) {
	t.Parallel()
	operators := []*operatorv1.OperatorDocument{
		{Id: "inf-1", OperatorSessionId: "sess-inf-1", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}}},
		{Id: "data-1", OperatorSessionId: "sess-data-1", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{}},
	}
	selected, err := SelectInferenceOperator(operators, "sess-inf-1", nil)
	require.NoError(t, err)
	assert.Equal(t, "sess-inf-1", selected.OperatorSessionID)

	_, err = SelectInferenceOperator(operators, "sess-data-1", nil)
	require.ErrorIs(t, err, constants.ErrInferenceOperatorNotCapable)
}

func TestRequireOllamaInferenceOperator_AcceptsOllamaOperator(t *testing.T) {
	t.Parallel()
	operators := []*operatorv1.OperatorDocument{
		{
			Id:                "inf-1",
			OperatorSessionId: "sess-inf-1",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
				Capabilities:            []string{string(constants.OperatorCapabilityInference)},
				InferenceOllamaEndpoint: "http://192.168.1.2:11434",
			},
		},
	}
	require.NoError(t, RequireOllamaInferenceOperator(operators, "sess-inf-1"))
}

func TestRequireOllamaInferenceOperator_RejectsLlamaCppOperator(t *testing.T) {
	t.Parallel()
	operators := []*operatorv1.OperatorDocument{
		{
			Id:                "inf-1",
			OperatorSessionId: "sess-inf-1",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
				Capabilities:              []string{string(constants.OperatorCapabilityInference)},
				InferenceLlamacppEndpoint: "http://192.168.1.2:8080",
			},
		},
	}
	selected, err := SelectInferenceOperator(operators, "sess-inf-1", nil)
	require.NoError(t, err)
	assert.Equal(t, constants.InferenceProviderLlamaCpp, selected.Provider)
	assert.Equal(t, "http://192.168.1.2:8080", selected.ProviderEndpoint)
	require.ErrorIs(t, RequireOllamaInferenceOperator(operators, "sess-inf-1"), constants.ErrInferenceProviderOllamaRequired)
}

func TestSelectInferenceOperator_FailsClosedOnAmbiguity(t *testing.T) {
	t.Parallel()
	operators := []*operatorv1.OperatorDocument{
		{Id: "inf-1", OperatorSessionId: "sess-inf-1", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}}},
		{Id: "inf-2", OperatorSessionId: "sess-inf-2", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}}},
	}
	_, err := SelectInferenceOperator(operators, "", nil)
	require.ErrorIs(t, err, constants.ErrInferenceOperatorAmbiguous)
	assert.Contains(t, err.Error(), "inf-1 (session sess-inf-1)")
	assert.Contains(t, err.Error(), "inf-2 (session sess-inf-2)")
}

func TestSelectInferenceOperatorForHardware(t *testing.T) {
	t.Parallel()
	operators := []*operatorv1.OperatorDocument{
		{
			Id:                "inf-host1",
			OperatorSessionId: "sess-inf-host1",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			SystemFingerprint: "fp-host1",
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}},
		},
		{
			Id:                "inf-host2",
			OperatorSessionId: "sess-inf-host2",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			SystemFingerprint: "fp-host2",
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}},
		},
	}

	t.Run("resolves by system fingerprint", func(t *testing.T) {
		selected, err := SelectInferenceOperatorForHardware(operators, "", "fp-host2", nil)
		require.NoError(t, err)
		assert.Equal(t, "inf-host2", selected.OperatorID)
	})

	t.Run("returns not found for non-matching fingerprint", func(t *testing.T) {
		_, err := SelectInferenceOperatorForHardware(operators, "", "fp-nonexistent", nil)
		require.ErrorIs(t, err, constants.ErrInferenceOperatorNotFound)
	})
}

func TestSelectInferenceOperator_BlendedDeploymentTypes(t *testing.T) {
	for _, kind := range []constants.OperatorType{constants.OperatorTypeRemote, constants.OperatorTypeEmbedded} {
		t.Run(string(kind), func(t *testing.T) {
			op := &operatorv1.OperatorDocument{
				Id: "blend", OperatorSessionId: "blend-session", Status: string(constants.OperatorStatusActive),
				OperatorType: string(kind), SystemFingerprint: "blend-hardware",
				RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
					Capabilities:            models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityData, constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver}),
					InferenceOllamaEndpoint: "http://127.0.0.1:11434",
				},
			}
			operators := []*operatorv1.OperatorDocument{op}
			selected, err := SelectInferenceOperatorForHardware(operators, op.OperatorSessionId, op.SystemFingerprint, nil)
			require.NoError(t, err)
			assert.Equal(t, op.OperatorSessionId, selected.OperatorSessionID)
			require.NoError(t, RequireOllamaInferenceOperator(operators, op.OperatorSessionId))
			_, err = SelectInferenceOperatorForHardware(operators, op.OperatorSessionId, "other-hardware", nil)
			require.ErrorIs(t, err, constants.ErrInferenceOperatorNotCapable)
			// When only the embedded operator is present, it is selected as the default.
			if kind == constants.OperatorTypeEmbedded {
				lone, loneErr := SelectInferenceOperator(operators, "", nil)
				require.NoError(t, loneErr)
				assert.Equal(t, op.OperatorSessionId, lone.OperatorSessionID)
			}
			selected, err = SelectInferenceOperator(append(operators, &operatorv1.OperatorDocument{
				Id: "other", OperatorSessionId: "other-session", Status: string(constants.OperatorStatusActive),
				OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}},
			}), "", nil)
			if kind == constants.OperatorTypeEmbedded {
				// The embedded Operator takes precedence over remote inference when untargeted,
				// while an explicit remote session remains selectable.
				require.NoError(t, err)
				assert.Equal(t, "blend-session", selected.OperatorSessionID)
				explicit, explicitErr := SelectInferenceOperator(append(operators, &operatorv1.OperatorDocument{
					Id: "other", OperatorSessionId: "other-session", Status: string(constants.OperatorStatusActive),
					OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}},
				}), "other-session", nil)
				require.NoError(t, explicitErr)
				assert.Equal(t, "other-session", explicit.OperatorSessionID)
				return
			}
			require.ErrorIs(t, err, constants.ErrInferenceOperatorAmbiguous)
		})
	}
}

func TestActiveInferenceOperators_RejectsUnavailableOrIncapableSessions(t *testing.T) {
	capable := &operatorv1.OperatorRuntimeConfig{Capabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityInference})}
	operators := []*operatorv1.OperatorDocument{
		{Id: "offline", OperatorSessionId: "offline", Status: string(constants.OperatorStatusOffline), OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: capable},
		{Id: "unknown-type", OperatorSessionId: "unknown", Status: string(constants.OperatorStatusActive), OperatorType: "unknown", RuntimeConfig: capable},
		{Id: "missing-session", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: capable},
		{Id: "missing-config", OperatorSessionId: "missing-config", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeEmbedded), OperatorCapabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityInference})},
		{Id: "data", OperatorSessionId: "data", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityData})}},
	}
	assert.Empty(t, ActiveInferenceOperators(operators))
}

// hostedInferenceOperator is an active Inference Operator whose enrolled
// runtime config lists hosted, as key discovery at enrollment records it.
func hostedInferenceOperator(id, session string, operatorType constants.OperatorType, hosted ...string) *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id:                id,
		OperatorSessionId: session,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(operatorType),
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
			Capabilities:             []string{string(constants.OperatorCapabilityInference)},
			InferenceHostedProviders: hosted,
		},
	}
}

func TestSelectInferenceOperator_PicksTheOperatorThatServesEveryHostedClass(t *testing.T) {
	t.Parallel()
	embedded := hostedInferenceOperator("embedded", "sess-embedded", constants.OperatorTypeEmbedded)
	geminiOnly := hostedInferenceOperator("inf-gemini", "sess-gemini", constants.OperatorTypeRemote, constants.InferenceProviderGemini)
	both := hostedInferenceOperator("inf-both", "sess-both", constants.OperatorTypeRemote, constants.InferenceProviderGemini, constants.InferenceProviderOpenAI)

	t.Run("no hosted classes prefers the embedded Operator", func(t *testing.T) {
		selected, err := SelectInferenceOperator([]*operatorv1.OperatorDocument{embedded, geminiOnly}, "", nil)
		require.NoError(t, err)
		assert.Equal(t, "sess-embedded", selected.OperatorSessionID)
	})
	t.Run("skips the embedded Operator that lacks the class", func(t *testing.T) {
		selected, err := SelectInferenceOperator([]*operatorv1.OperatorDocument{embedded, geminiOnly}, "", []string{constants.InferenceProviderGemini})
		require.NoError(t, err)
		assert.Equal(t, "sess-gemini", selected.OperatorSessionID)
	})
	t.Run("needs every class on one Operator", func(t *testing.T) {
		selected, err := SelectInferenceOperator([]*operatorv1.OperatorDocument{embedded, geminiOnly, both}, "", []string{constants.InferenceProviderGemini, constants.InferenceProviderOpenAI})
		require.NoError(t, err)
		assert.Equal(t, "sess-both", selected.OperatorSessionID)
	})
	t.Run("two Operators serving the classes stay ambiguous", func(t *testing.T) {
		_, err := SelectInferenceOperator([]*operatorv1.OperatorDocument{geminiOnly, both}, "", []string{constants.InferenceProviderGemini})
		require.ErrorIs(t, err, constants.ErrInferenceOperatorAmbiguous)
	})
	t.Run("no Operator serves the classes", func(t *testing.T) {
		_, err := SelectInferenceOperator([]*operatorv1.OperatorDocument{embedded, geminiOnly}, "", []string{constants.InferenceProviderGemini, constants.InferenceProviderAnthropic})
		require.ErrorIs(t, err, constants.ErrInferenceOperatorNotFound)
		assert.Contains(t, err.Error(), "gemini, anthropic")
	})
	t.Run("a pinned session must serve the classes", func(t *testing.T) {
		selected, err := SelectInferenceOperator([]*operatorv1.OperatorDocument{embedded, geminiOnly}, "sess-gemini", []string{constants.InferenceProviderGemini})
		require.NoError(t, err)
		assert.Equal(t, "sess-gemini", selected.OperatorSessionID)

		_, err = SelectInferenceOperator([]*operatorv1.OperatorDocument{embedded, geminiOnly}, "sess-embedded", []string{constants.InferenceProviderGemini})
		require.ErrorIs(t, err, constants.ErrInferenceOperatorNotCapable)
		assert.Contains(t, err.Error(), "session sess-embedded does not serve hosted providers gemini")
	})
}

func TestHostedProviderClasses(t *testing.T) {
	t.Parallel()
	variant := func(class string) *evalv1.ModelVariant {
		return &evalv1.ModelVariant{ProviderClass: class}
	}
	tests := []struct {
		name     string
		registry []*evalv1.ModelVariant
		judge    *evalv1.ModelVariant
		want     []string
	}{
		{name: "local only", registry: []*evalv1.ModelVariant{variant(constants.InferenceProviderOllama), variant(constants.InferenceProviderLlamaCpp)}, judge: variant(constants.InferenceProviderOllama)},
		{name: "no judge", registry: []*evalv1.ModelVariant{variant(constants.InferenceProviderGemini)}, want: []string{constants.InferenceProviderGemini}},
		{name: "gemini and local formation", registry: []*evalv1.ModelVariant{variant(constants.InferenceProviderOllama), variant(constants.InferenceProviderGemini), variant(constants.InferenceProviderGemini)}, judge: variant(constants.InferenceProviderOllama), want: []string{constants.InferenceProviderGemini}},
		{name: "judge adds its class in canonical order", registry: []*evalv1.ModelVariant{variant(constants.InferenceProviderOpenAI), nil}, judge: variant(constants.InferenceProviderGemini), want: []string{constants.InferenceProviderGemini, constants.InferenceProviderOpenAI}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, HostedProviderClasses(tt.registry, tt.judge))
		})
	}
}
