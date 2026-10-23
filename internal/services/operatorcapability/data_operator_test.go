// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcapability

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestIsDataOperator_ExcludesOtherRoles(t *testing.T) {
	t.Parallel()

	data := &operatorv1.OperatorDocument{
		Id:                "data-1",
		OperatorSessionId: "sess-data-1",
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
	}
	assert.True(t, IsDataOperator(data))

	cases := []*operatorv1.OperatorDocument{
		{
			Id:                "infer-1",
			OperatorSessionId: "sess-infer-1",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityInference)}},
		},
		{
			Id:                "observer-1",
			OperatorSessionId: "sess-observer-1",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityObserver)}},
		},
		{
			Id:                "prov-1",
			OperatorSessionId: "sess-prov-1",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityProvenance)}},
		},
		{
			Id:                   "role-1",
			OperatorSessionId:    "sess-role-1",
			Status:               string(constants.OperatorStatusActive),
			OperatorType:         string(constants.OperatorTypeRemote),
			OperatorCapabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityObserver}),
		},
		{
			Id:                "offline-1",
			OperatorSessionId: "sess-offline-1",
			Status:            string(constants.OperatorStatusOffline),
			OperatorType:      string(constants.OperatorTypeRemote),
		},
		{
			Id:                "embedded-1",
			OperatorSessionId: "sess-embedded-1",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeEmbedded),
		},
	}
	for _, op := range cases {
		assert.False(t, IsDataOperator(op), op.Id)
	}
}

func dataOperatorDoc(id, sessionID string) *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id:                id,
		OperatorSessionId: sessionID,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
	}
}

func TestSelectDataOperator_ResolvesSingleDedicatedOperator(t *testing.T) {
	t.Parallel()
	selected, err := SelectDataOperator([]*operatorv1.OperatorDocument{dataOperatorDoc("load-1", "sess-load-1")})
	require.NoError(t, err)
	assert.Equal(t, "load-1", selected.OperatorID)
	assert.Equal(t, "sess-load-1", selected.OperatorSessionID)

	_, err = SelectDataOperator(nil)
	require.ErrorIs(t, err, constants.ErrDataOperatorNotFound)
}

func TestSelectDataOperator_AmbiguousWhenTwoDedicatedOperatorsAreActive(t *testing.T) {
	t.Parallel()
	_, err := SelectDataOperator([]*operatorv1.OperatorDocument{
		dataOperatorDoc("a", "sess-a"),
		dataOperatorDoc("b", "sess-b"),
	})
	require.ErrorIs(t, err, constants.ErrDataOperatorAmbiguous)
}

func TestExtractWorkingDirectory_FromEnvironmentDetails(t *testing.T) {
	t.Parallel()

	hr := &operatorv1.HeartbeatResult{
		Environment: &operatorv1.EnvironmentDetails{Pwd: "/home/deploy"},
	}

	pwd := extractWorkingDirectory(hr)
	assert.Equal(t, "/home/deploy", pwd)
}

func TestExtractWorkingDirectory_FallbackToSystemIdentity(t *testing.T) {
	t.Parallel()

	hr := &operatorv1.HeartbeatResult{
		SystemIdentity: &operatorv1.SystemIdentity{Pwd: "/root"},
	}

	pwd := extractWorkingDirectory(hr)
	assert.Equal(t, "/root", pwd)
}

func TestExtractWorkingDirectory_EnvironmentTakesPrecedence(t *testing.T) {
	t.Parallel()

	hr := &operatorv1.HeartbeatResult{
		Environment:    &operatorv1.EnvironmentDetails{Pwd: "/home/deploy"},
		SystemIdentity: &operatorv1.SystemIdentity{Pwd: "/root"},
	}

	pwd := extractWorkingDirectory(hr)
	assert.Equal(t, "/home/deploy", pwd)
}

func TestExtractWorkingDirectory_EmptyHeartbeat(t *testing.T) {
	t.Parallel()
	pwd := extractWorkingDirectory(nil)
	assert.Empty(t, pwd)
}

func TestExtractWorkingDirectory_NoPwdInHeartbeat(t *testing.T) {
	t.Parallel()

	hr := &operatorv1.HeartbeatResult{
		SystemIdentity: &operatorv1.SystemIdentity{Hostname: "test"},
	}

	pwd := extractWorkingDirectory(hr)
	assert.Empty(t, pwd)
}

func TestActiveDataOperators_IncludesWorkingDirectory(t *testing.T) {
	t.Parallel()

	operators := []*operatorv1.OperatorDocument{
		{
			Id:                "data-1",
			OperatorSessionId: "sess-data-1",
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(constants.OperatorTypeRemote),
			LatestHeartbeatSnapshot: &operatorv1.HeartbeatResult{
				Environment: &operatorv1.EnvironmentDetails{Pwd: "/home/deploy"},
			},
		},
	}

	statuses := ActiveDataOperators(operators)
	require.Len(t, statuses, 1)
	assert.Equal(t, "data-1", statuses[0].OperatorID)
	assert.Equal(t, "/home/deploy", statuses[0].WorkingDirectory)
}

func TestActiveDataOperators_EmptyHeartbeatLeavesWorkingDirectoryEmpty(t *testing.T) {
	t.Parallel()

	operators := []*operatorv1.OperatorDocument{
		{
			Id:                      "data-1",
			OperatorSessionId:       "sess-data-1",
			Status:                  string(constants.OperatorStatusActive),
			OperatorType:            string(constants.OperatorTypeRemote),
			LatestHeartbeatSnapshot: nil,
		},
	}

	statuses := ActiveDataOperators(operators)
	require.Len(t, statuses, 1)
	assert.Empty(t, statuses[0].WorkingDirectory)
}

func tierOp(id string, kind constants.OperatorType, hostname string, capabilities ...constants.OperatorCapability) *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id:                id,
		OperatorSessionId: "sess-" + id,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(kind),
		CurrentHostname:   hostname,
		RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: models.OperatorCapabilitiesToProto(capabilities)},
	}
}

func TestEmbeddedTypeImpliesDataAndWitnessCommands(t *testing.T) {
	op := tierOp("embedded-operator", constants.OperatorTypeEmbedded, "", constants.OperatorCapabilityObserver)
	require.Equal(t, constants.OperatorCapabilities{constants.OperatorCapabilityData, constants.OperatorCapabilityInference, constants.OperatorCapabilityObserver}, GetOperatorCapabilities(op))
	require.NoError(t, ValidateWitnessCommand(GetOperatorCapabilities(op), "pwd"))
	require.True(t, IsDataOperator(op))
	require.False(t, IsDedicatedDataOperator(op))
}

func TestDataOperatorTiering(t *testing.T) {
	embedded := tierOp("embedded-operator", constants.OperatorTypeEmbedded, "")
	remote := tierOp("data", constants.OperatorTypeRemote, "host", constants.OperatorCapabilityData)
	remote2 := tierOp("data-2", constants.OperatorTypeRemote, "host-2", constants.OperatorCapabilityData)

	sel, err := SelectDataOperator([]*operatorv1.OperatorDocument{remote, embedded, remote2})
	require.NoError(t, err)
	require.Equal(t, "embedded-operator", sel.OperatorID)

	sel, err = SelectDataOperator([]*operatorv1.OperatorDocument{remote})
	require.NoError(t, err)
	require.Equal(t, "data", sel.OperatorID)

	_, err = SelectDataOperator([]*operatorv1.OperatorDocument{remote, remote2})
	require.ErrorIs(t, err, constants.ErrDataOperatorAmbiguous)

	require.Equal(t, []*operatorv1.OperatorDocument{embedded}, PreferEmbeddedDataOperator([]*operatorv1.OperatorDocument{remote, embedded}))
	require.Equal(t, []*operatorv1.OperatorDocument{remote, remote2}, PreferEmbeddedDataOperator([]*operatorv1.OperatorDocument{remote, remote2}))
}

func TestActiveDataOperators_EmbeddedWorkingDirectoryFromRuntimeConfig(t *testing.T) {
	t.Parallel()

	embedded := &operatorv1.OperatorDocument{
		Id:                   "embedded-operator",
		OperatorSessionId:    "sess-embedded",
		Status:               string(constants.OperatorStatusActive),
		OperatorType:         string(constants.OperatorTypeEmbedded),
		OperatorCapabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities{constants.OperatorCapabilityData}),
		RuntimeConfig:        &operatorv1.OperatorRuntimeConfig{LocalDir: "/srv/ztd"},
	}
	matches := ActiveDataOperators([]*operatorv1.OperatorDocument{embedded})
	require.Len(t, matches, 1)
	assert.Equal(t, "/srv/ztd", matches[0].WorkingDirectory)

	remote := &operatorv1.OperatorDocument{
		Id:                "remote-1",
		OperatorSessionId: "sess-remote",
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{LocalDir: "/srv/other"},
	}
	matches = ActiveDataOperators([]*operatorv1.OperatorDocument{remote})
	require.Len(t, matches, 1)
	assert.Empty(t, matches[0].WorkingDirectory, "only the embedded Operator falls back to its registered directory")
}
