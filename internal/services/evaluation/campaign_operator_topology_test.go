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
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// testOperatorTopology is a distinct-runtime topology without anchors, for
// tests whose executor reads only the role sessions.
func testOperatorTopology(inferenceSessionID, dataSessionID string) CampaignOperatorTopology {
	return CampaignOperatorTopology{
		Inference: CampaignOperatorRuntime{OperatorID: "inf-op", OperatorSessionID: inferenceSessionID},
		Data:      CampaignOperatorRuntime{OperatorID: "data-op", OperatorSessionID: dataSessionID},
	}
}

func topologyOperator(id, session string, capabilities ...constants.OperatorCapability) *operatorv1.OperatorDocument {
	config := &operatorv1.OperatorRuntimeConfig{}
	for _, capability := range capabilities {
		config.Capabilities = append(config.Capabilities, string(capability))
	}
	return &operatorv1.OperatorDocument{
		Id:                id,
		OperatorSessionId: session,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		RuntimeConfig:     config,
	}
}

// anchoredOperator binds signer's key to the operator document, as enrollment does.
func anchoredOperator(doc *operatorv1.OperatorDocument, signer oracletest.Signer) *operatorv1.OperatorDocument {
	anchor := signer.Anchor(doc.GetId())
	doc.ActuatorKeyId = anchor.GetKeyId()
	doc.ActuatorPublicKey = anchor.GetPublicKeyHex()
	return doc
}

func TestResolveCampaignOperatorTopology(t *testing.T) {
	t.Parallel()
	inference := topologyOperator("i1", "infer-1", constants.OperatorCapabilityInference)
	data := topologyOperator("d1", "data-1", constants.OperatorCapabilityData)
	observer := topologyOperator("obs", "obs-1", constants.OperatorCapabilityObserver)
	both := topologyOperator("embedded", "embedded-1", constants.OperatorCapabilityData, constants.OperatorCapabilityInference)
	inactive := topologyOperator("i1", "infer-1", constants.OperatorCapabilityInference)
	inactive.Status = string(constants.OperatorStatusOffline)

	tests := []struct {
		name       string
		operators  []*operatorv1.OperatorDocument
		wantShared bool
		want       CampaignOperatorTopology
		wantErr    error
	}{
		{
			name:      "dedicated runtimes per role",
			operators: []*operatorv1.OperatorDocument{inference, data},
			want: CampaignOperatorTopology{
				Inference: CampaignOperatorRuntime{OperatorID: "i1", OperatorSessionID: "infer-1"},
				Data:      CampaignOperatorRuntime{OperatorID: "d1", OperatorSessionID: "data-1"},
			},
		},
		{
			name:      "the data role resolves past non-data decoys",
			operators: []*operatorv1.OperatorDocument{inference, observer, data},
			want: CampaignOperatorTopology{
				Inference: CampaignOperatorRuntime{OperatorID: "i1", OperatorSessionID: "infer-1"},
				Data:      CampaignOperatorRuntime{OperatorID: "d1", OperatorSessionID: "data-1"},
			},
		},
		{
			name:       "one operator holding both capabilities serves both roles on one runtime",
			operators:  []*operatorv1.OperatorDocument{both},
			wantShared: true,
			want: CampaignOperatorTopology{
				Inference: CampaignOperatorRuntime{OperatorID: "embedded", OperatorSessionID: "embedded-1"},
				Data:      CampaignOperatorRuntime{OperatorID: "embedded", OperatorSessionID: "embedded-1"},
			},
		},
		{
			name:      "several data operators are ambiguous",
			operators: []*operatorv1.OperatorDocument{inference, data, topologyOperator("d2", "data-2", constants.OperatorCapabilityData)},
			wantErr:   constants.ErrDataOperatorAmbiguous,
		},
		{
			name:      "several inference operators are ambiguous",
			operators: []*operatorv1.OperatorDocument{inference, topologyOperator("i2", "infer-2", constants.OperatorCapabilityInference), data},
			wantErr:   constants.ErrInferenceOperatorAmbiguous,
		},
		{
			name:      "no inference operator",
			operators: []*operatorv1.OperatorDocument{data},
			wantErr:   constants.ErrInferenceOperatorNotFound,
		},
		{
			name:      "observers are not data operators",
			operators: []*operatorv1.OperatorDocument{inference, observer},
			wantErr:   constants.ErrDataOperatorNotFound,
		},
		{
			name:      "inactive operators are ignored",
			operators: []*operatorv1.OperatorDocument{inactive, data},
			wantErr:   constants.ErrInferenceOperatorNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveCampaignOperatorTopology(test.operators, "")
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want.Inference, got.Inference)
			assert.Equal(t, test.want.Data, got.Data)
			assert.Equal(t, test.wantShared, got.SharedRuntime())
			require.NoError(t, got.Validate())
			require.ErrorIs(t, got.ValidateAnchored(), constants.ErrConformanceAnchorMissing)
		})
	}
}

func TestResolveCampaignOperatorTopologyForDataOperatorFollowsStableIdentityAcrossSessions(t *testing.T) {
	t.Parallel()
	inference := topologyOperator("inference", "inference-session", constants.OperatorCapabilityInference)
	oldSession := topologyOperator("data-stable", "old-session", constants.OperatorCapabilityData)
	oldSession.Status = string(constants.OperatorStatusOffline)
	newSession := topologyOperator("data-stable", "new-session", constants.OperatorCapabilityData)
	otherData := topologyOperator("other-data", "other-session", constants.OperatorCapabilityData)
	embedded := topologyOperator("embedded", "embedded-session", constants.OperatorCapabilityData)
	embedded.OperatorType = string(constants.OperatorTypeEmbedded)

	topology, err := ResolveCampaignOperatorTopologyForDataOperator(
		[]*operatorv1.OperatorDocument{inference, oldSession, newSession, otherData, embedded},
		"",
		"data-stable",
	)

	require.NoError(t, err)
	assert.Equal(t, "data-stable", topology.Data.OperatorID)
	assert.Equal(t, "new-session", topology.Data.OperatorSessionID)
}

func TestResolveCampaignOperatorTopologyForDataOperatorFailsClosed(t *testing.T) {
	t.Parallel()
	inference := topologyOperator("inference", "inference-session", constants.OperatorCapabilityInference)
	wrongCapability := topologyOperator("stable-id", "wrong-capability", constants.OperatorCapabilityObserver)
	inactive := topologyOperator("stable-id", "offline", constants.OperatorCapabilityData)
	inactive.Status = string(constants.OperatorStatusOffline)
	otherEmbedded := topologyOperator("other-embedded", "embedded-session", constants.OperatorCapabilityData)
	otherEmbedded.OperatorType = string(constants.OperatorTypeEmbedded)
	first := topologyOperator("stable-id", "session-1", constants.OperatorCapabilityData)
	second := topologyOperator("stable-id", "session-2", constants.OperatorCapabilityData)

	for _, test := range []struct {
		name      string
		operators []*operatorv1.OperatorDocument
		wantErr   error
	}{
		{name: "missing stable operator does not fall back to embedded", operators: []*operatorv1.OperatorDocument{inference, otherEmbedded}, wantErr: constants.ErrDataOperatorNotFound},
		{name: "stable operator lacks data capability", operators: []*operatorv1.OperatorDocument{inference, wrongCapability, otherEmbedded}, wantErr: constants.ErrDataOperatorNotFound},
		{name: "stable operator is offline", operators: []*operatorv1.OperatorDocument{inference, inactive, otherEmbedded}, wantErr: constants.ErrDataOperatorNotFound},
		{name: "several active sessions are ambiguous", operators: []*operatorv1.OperatorDocument{inference, first, second}, wantErr: constants.ErrDataOperatorAmbiguous},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolveCampaignOperatorTopologyForDataOperator(test.operators, "", "stable-id")
			require.ErrorIs(t, err, test.wantErr)
		})
	}
}

func TestCampaignOperatorTopology_WithAnchorsBindsEachRuntime(t *testing.T) {
	t.Parallel()
	inferenceSigner, dataSigner := oracletest.NewSigner(t), oracletest.NewSigner(t)
	operators := []*operatorv1.OperatorDocument{
		anchoredOperator(topologyOperator("i1", "infer-1", constants.OperatorCapabilityInference), inferenceSigner),
		anchoredOperator(topologyOperator("d1", "data-1", constants.OperatorCapabilityData), dataSigner),
	}
	topology, err := ResolveCampaignOperatorTopology(operators, "")
	require.NoError(t, err)

	anchored, err := topology.WithAnchors(operators)

	require.NoError(t, err)
	require.NoError(t, anchored.ValidateAnchored())
	assert.Equal(t, inferenceSigner.KeyID, anchored.Inference.Anchor.GetKeyId())
	assert.Equal(t, dataSigner.KeyID, anchored.Data.Anchor.GetKeyId())
	assert.Nil(t, topology.Data.Anchor, "WithAnchors must not mutate the receiver")
}

func TestCampaignOperatorTopology_SharedRuntimePinsOneKeyForBothRoles(t *testing.T) {
	t.Parallel()
	signer := oracletest.NewSigner(t)
	operators := []*operatorv1.OperatorDocument{
		anchoredOperator(topologyOperator("embedded", "embedded-1", constants.OperatorCapabilityData, constants.OperatorCapabilityInference), signer),
	}
	topology, err := ResolveCampaignOperatorTopology(operators, "")
	require.NoError(t, err)

	anchored, err := topology.WithAnchors(operators)

	require.NoError(t, err)
	require.True(t, anchored.SharedRuntime())
	require.NoError(t, anchored.ValidateAnchored())
	assert.Equal(t, anchored.Inference.Anchor.GetKeyId(), anchored.Data.Anchor.GetKeyId())

	anchored.Inference.Anchor = oracletest.NewSigner(t).Anchor("embedded")
	require.ErrorIs(t, anchored.ValidateAnchored(), constants.ErrConformanceAnchorInvalid)
}

func TestCampaignOperatorTopology_WithAnchorsFailsClosed(t *testing.T) {
	t.Parallel()
	signer := oracletest.NewSigner(t)
	unbound := []*operatorv1.OperatorDocument{
		anchoredOperator(topologyOperator("i1", "infer-1", constants.OperatorCapabilityInference), signer),
		topologyOperator("d1", "data-1", constants.OperatorCapabilityData),
	}
	topology, err := ResolveCampaignOperatorTopology(unbound, "")
	require.NoError(t, err)

	_, err = topology.WithAnchors(unbound)
	require.ErrorIs(t, err, constants.ErrConformanceAnchorInvalid, "a document that binds no key fails closed")

	_, err = topology.WithAnchors(unbound[:1])
	require.ErrorIs(t, err, constants.ErrConformanceAnchorMissing, "a session absent from the registry fails closed")
}

func TestCampaignOperatorTopology_ValidateAnchoredRejectsAnchorForAnotherRuntime(t *testing.T) {
	t.Parallel()
	signer := oracletest.NewSigner(t)
	topology := testOperatorTopology("infer-1", "data-1")
	topology.Inference.Anchor = signer.Anchor(topology.Inference.OperatorID)
	topology.Data.Anchor = signer.Anchor("operator-other")

	require.ErrorIs(t, topology.ValidateAnchored(), constants.ErrConformanceAnchorMissing)
}
