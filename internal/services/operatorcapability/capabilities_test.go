// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcapability

import (
	"testing"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestResolveOperatorCapabilities(t *testing.T) {
	t.Parallel()

	assert.Equal(t, constants.OperatorCapabilities{constants.OperatorCapabilityData}, ResolveOperatorCapabilities(nil))
	assert.Equal(t, constants.OperatorCapabilities{constants.OperatorCapabilityData}, ResolveOperatorCapabilities(&operatorv1.OperatorRuntimeConfig{}))

	assert.Equal(t, constants.OperatorCapabilities{constants.OperatorCapabilityInference}, ResolveOperatorCapabilities(&operatorv1.OperatorRuntimeConfig{
		Capabilities: []string{string(constants.OperatorCapabilityInference)},
	}))

	assert.Equal(t, constants.OperatorCapabilities{constants.OperatorCapabilityProvenance}, ResolveOperatorCapabilities(&operatorv1.OperatorRuntimeConfig{
		Capabilities: []string{string(constants.OperatorCapabilityProvenance)},
	}))

	assert.Equal(t, constants.OperatorCapabilities{constants.OperatorCapabilityObserver}, ResolveOperatorCapabilities(&operatorv1.OperatorRuntimeConfig{
		Capabilities: []string{string(constants.OperatorCapabilityObserver)},
	}))

	assert.Equal(t, constants.OperatorCapabilities{constants.OperatorCapabilityData}, ResolveOperatorCapabilities(&operatorv1.OperatorRuntimeConfig{
		Capabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityData}),
	}))
}

func TestGetOperatorCapabilities(t *testing.T) {
	t.Parallel()

	opDoc := &operatorv1.OperatorDocument{
		OperatorCapabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityInference}),
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
			Capabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityData}), // OperatorCapability takes precedence
		},
	}
	assert.Equal(t, constants.OperatorCapabilities{constants.OperatorCapabilityData}, GetOperatorCapabilities(opDoc))

	opDoc2 := &operatorv1.OperatorDocument{
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
			Capabilities: []string{string(constants.OperatorCapabilityObserver)},
		},
	}
	assert.Equal(t, constants.OperatorCapabilities{constants.OperatorCapabilityObserver}, GetOperatorCapabilities(opDoc2))
}

func TestIsActiveOperatorSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		op   *operatorv1.OperatorDocument
		want bool
	}{
		{"active remote", &operatorv1.OperatorDocument{Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote), OperatorSessionId: "s"}, true},
		{"active embedded with runtime config", &operatorv1.OperatorDocument{Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeEmbedded), OperatorSessionId: "s", RuntimeConfig: &operatorv1.OperatorRuntimeConfig{}}, true},
		{"active embedded without runtime config", &operatorv1.OperatorDocument{Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeEmbedded), OperatorSessionId: "s"}, false},
		{"stale remote", &operatorv1.OperatorDocument{Status: string(constants.OperatorStatusStale), OperatorType: string(constants.OperatorTypeRemote), OperatorSessionId: "s"}, false},
		{"active remote without session", &operatorv1.OperatorDocument{Status: string(constants.OperatorStatusActive), OperatorType: string(constants.OperatorTypeRemote)}, false},
		{"active with unset type", &operatorv1.OperatorDocument{Status: string(constants.OperatorStatusActive), OperatorSessionId: "s"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsActiveOperatorSession(tt.op))
		})
	}
}

func TestPreferDedicated(t *testing.T) {
	t.Parallel()

	embedded := &operatorv1.OperatorDocument{OperatorSessionId: "embedded", OperatorType: string(constants.OperatorTypeEmbedded)}
	remoteA := &operatorv1.OperatorDocument{OperatorSessionId: "remote-a", OperatorType: string(constants.OperatorTypeRemote)}
	remoteB := &operatorv1.OperatorDocument{OperatorSessionId: "remote-b", OperatorType: string(constants.OperatorTypeRemote)}

	assert.Equal(t, []*operatorv1.OperatorDocument{remoteA}, PreferDedicated([]*operatorv1.OperatorDocument{embedded, remoteA}))
	assert.Equal(t, []*operatorv1.OperatorDocument{remoteA, remoteB}, PreferDedicated([]*operatorv1.OperatorDocument{remoteA, embedded, remoteB}))
	assert.Equal(t, []*operatorv1.OperatorDocument{embedded}, PreferDedicated([]*operatorv1.OperatorDocument{embedded}))
	assert.Empty(t, PreferDedicated(nil))
}

func TestHasActiveCapabilityRequiresActiveSessionAndRole(t *testing.T) {
	t.Parallel()

	inference := &operatorv1.OperatorDocument{
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		OperatorSessionId: "s",
		RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityInference})},
	}
	assert.True(t, HasActiveCapability(inference, constants.OperatorCapabilityInference))
	assert.False(t, HasActiveCapability(inference, constants.OperatorCapabilityData))

	inference.Status = string(constants.OperatorStatusStopped)
	assert.False(t, HasActiveCapability(inference, constants.OperatorCapabilityInference))
}
