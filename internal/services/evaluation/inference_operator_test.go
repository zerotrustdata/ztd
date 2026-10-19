// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

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
	selected, err := SelectInferenceOperator(operators, "sess-inf-1")
	require.NoError(t, err)
	assert.Equal(t, "sess-inf-1", selected.OperatorSessionID)

	_, err = SelectInferenceOperator(operators, "sess-data-1")
	require.ErrorIs(t, err, constants.ErrInferenceOperatorNotCapable)
}

func TestGovernedInferenceOllamaEndpoint_UsesExactOperatorRuntimeConfig(t *testing.T) {
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
	endpoint, err := GovernedInferenceOllamaEndpoint(operators, "sess-inf-1")
	require.NoError(t, err)
	assert.Equal(t, "http://192.168.1.2:11434", endpoint)
}

func TestGovernedInferenceOllamaEndpoint_RejectsMissingOperatorEndpoint(t *testing.T) {
	t.Parallel()
	operators := []*operatorv1.OperatorDocument{
		{
			Id:                "inf-1",
			OperatorSessionId: "sess-inf-1",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}},
		},
	}
	_, err := GovernedInferenceOllamaEndpoint(operators, "sess-inf-1")
	require.ErrorIs(t, err, constants.ErrInferenceEndpointInvalid)
}

func TestSelectInferenceOperator_FailsClosedOnAmbiguity(t *testing.T) {
	t.Parallel()
	operators := []*operatorv1.OperatorDocument{
		{Id: "inf-1", OperatorSessionId: "sess-inf-1", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}}},
		{Id: "inf-2", OperatorSessionId: "sess-inf-2", Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}}},
	}
	_, err := SelectInferenceOperator(operators, "")
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
		selected, err := SelectInferenceOperatorForHardware(operators, "", "fp-host2")
		require.NoError(t, err)
		assert.Equal(t, "inf-host2", selected.OperatorID)
	})

	t.Run("returns not found for non-matching fingerprint", func(t *testing.T) {
		_, err := SelectInferenceOperatorForHardware(operators, "", "fp-nonexistent")
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
			selected, err := SelectInferenceOperatorForHardware(operators, op.OperatorSessionId, op.SystemFingerprint)
			require.NoError(t, err)
			assert.Equal(t, op.OperatorSessionId, selected.OperatorSessionID)
			endpoint, err := GovernedInferenceOllamaEndpoint(operators, op.OperatorSessionId)
			require.NoError(t, err)
			assert.Equal(t, op.RuntimeConfig.InferenceOllamaEndpoint, endpoint)
			_, err = SelectInferenceOperatorForHardware(operators, op.OperatorSessionId, "other-hardware")
			require.ErrorIs(t, err, constants.ErrInferenceOperatorNotCapable)
			// When only the embedded operator is present, it is selected as the default.
			if kind == constants.OperatorTypeEmbedded {
				lone, loneErr := SelectInferenceOperator(operators, "")
				require.NoError(t, loneErr)
				assert.Equal(t, op.OperatorSessionId, lone.OperatorSessionID)
			}
			selected, err = SelectInferenceOperator(append(operators, &operatorv1.OperatorDocument{
				Id: "other", OperatorSessionId: "other-session", Status: string(constants.OperatorStatusActive),
				OperatorType: string(constants.OperatorTypeRemote), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}},
			}), "")
			if kind == constants.OperatorTypeEmbedded {
				// Dedicated remote inference Operators take precedence over the embedded fallback.
				require.NoError(t, err)
				assert.Equal(t, "other-session", selected.OperatorSessionID)
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
