// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"testing"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func remoteOperator(id, session string, cfg *operatorv1.OperatorRuntimeConfig) *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id:                id,
		OperatorSessionId: session,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		RuntimeConfig:     cfg,
	}
}

func inferenceOperatorFixture(id, session string) *operatorv1.OperatorDocument {
	return remoteOperator(id, session, &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}, InferenceOllamaEndpoint: "http://provider.example:11434"})
}

// dataOperatorFixture is a dedicated data Operator (no explicit capability flags, so
// it defaults to the data role).
func dataOperatorFixture(id, session string) *operatorv1.OperatorDocument {
	return remoteOperator(id, session, &operatorv1.OperatorRuntimeConfig{})
}

func TestResolveOperatorSessionsFrom(t *testing.T) {
	single := []*operatorv1.OperatorDocument{inferenceOperatorFixture("i1", "infer-1"), dataOperatorFixture("d1", "data-1")}
	withDecoys := []*operatorv1.OperatorDocument{
		inferenceOperatorFixture("i1", "infer-1"),
		remoteOperator("obs", "obs-1", &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}}),
		dataOperatorFixture("d1", "data-1"),
	}
	manyData := []*operatorv1.OperatorDocument{
		inferenceOperatorFixture("i1", "infer-1"),
		dataOperatorFixture("d1", "data-1"), dataOperatorFixture("d2", "data-2"), dataOperatorFixture("d3", "data-3"),
	}
	manyInference := []*operatorv1.OperatorDocument{inferenceOperatorFixture("i1", "infer-1"), inferenceOperatorFixture("i2", "infer-2")}
	observer := remoteOperator("obs", "obs-1", &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}})
	inactive := inferenceOperatorFixture("i1", "infer-1")
	inactive.Status = string(constants.OperatorStatusOffline)

	tests := []struct {
		name         string
		operators    []*operatorv1.OperatorDocument
		capabilities []constants.OperatorCapability
		want         operatorSessions
		wantErr      error
	}{
		{
			name: "one session per capability", operators: single,
			capabilities: []constants.OperatorCapability{constants.OperatorCapabilityInference, constants.OperatorCapabilityData},
			want:         operatorSessions{InferenceSessionID: "infer-1", DataSessionID: "data-1", DataOperatorID: "d1"},
		},
		{
			name: "only requested capabilities are resolved", operators: []*operatorv1.OperatorDocument{inferenceOperatorFixture("i1", "infer-1")},
			capabilities: []constants.OperatorCapability{constants.OperatorCapabilityInference},
			want:         operatorSessions{InferenceSessionID: "infer-1"},
		},
		{
			name: "one dedicated data operator resolves past non-data decoys", operators: withDecoys,
			capabilities: []constants.OperatorCapability{constants.OperatorCapabilityInference, constants.OperatorCapabilityData},
			want:         operatorSessions{InferenceSessionID: "infer-1", DataSessionID: "data-1", DataOperatorID: "d1"},
		},
		{
			name: "several dedicated data operators are ambiguous and fail closed", operators: manyData,
			capabilities: []constants.OperatorCapability{constants.OperatorCapabilityInference, constants.OperatorCapabilityData},
			wantErr:      constants.ErrDataOperatorAmbiguous,
		},
		{
			name: "several inference sessions are ambiguous", operators: manyInference,
			capabilities: []constants.OperatorCapability{constants.OperatorCapabilityInference},
			wantErr:      constants.ErrInferenceOperatorAmbiguous,
		},
		{
			name: "no inference session", operators: []*operatorv1.OperatorDocument{dataOperatorFixture("d1", "data-1")},
			capabilities: []constants.OperatorCapability{constants.OperatorCapabilityInference},
			wantErr:      constants.ErrInferenceOperatorNotFound,
		},
		{
			name: "resolves a single dedicated data operator", operators: []*operatorv1.OperatorDocument{dataOperatorFixture("x1", "other-1")},
			capabilities: []constants.OperatorCapability{constants.OperatorCapabilityData},
			want:         operatorSessions{DataSessionID: "other-1", DataOperatorID: "x1"},
		},
		{
			name: "multiple dedicated data operators are ambiguous", operators: []*operatorv1.OperatorDocument{dataOperatorFixture("x1", "other-1"), dataOperatorFixture("x2", "other-2")},
			capabilities: []constants.OperatorCapability{constants.OperatorCapabilityData},
			wantErr:      constants.ErrDataOperatorAmbiguous,
		},
		{
			name: "observers are not data-operators", operators: []*operatorv1.OperatorDocument{observer},
			capabilities: []constants.OperatorCapability{constants.OperatorCapabilityData},
			wantErr:      constants.ErrDataOperatorNotFound,
		},
		{
			name: "inactive operators are ignored", operators: []*operatorv1.OperatorDocument{inactive},
			capabilities: []constants.OperatorCapability{constants.OperatorCapabilityInference},
			wantErr:      constants.ErrInferenceOperatorNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveOperatorSessionsFrom(test.operators, test.capabilities...)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestResolveOperatorSessionsFromRejectsUnsupportedCapability(t *testing.T) {
	_, err := resolveOperatorSessionsFrom(nil, constants.OperatorCapabilityObserver)
	require.ErrorIs(t, err, constants.ErrOperatorCapabilityInvalid)
}

func TestEvalGroupHasNoSessionFlags(t *testing.T) {
	group := evalCmdWithConfig(nativeEvalDeps{})
	assert.Nil(t, group.PersistentFlags().Lookup("data-session"))
	assert.Nil(t, group.PersistentFlags().Lookup("inference-session"))
}
