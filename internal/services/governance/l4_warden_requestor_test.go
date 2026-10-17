// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type requestorNotary struct {
	userID string
}

func (n *requestorNotary) VerifyL3Proof(_ context.Context, userID, _, _ string, _ *commonv1.L3Proof) (bool, error) {
	n.userID = userID
	return userID == "requesting-user", nil
}

func TestL4Warden_L3VerifiesRequestorInsteadOfExecutionTarget(t *testing.T) {
	for _, target := range []string{string(constants.DocIDEmbeddedOperator), "outbound-operator"} {
		t.Run(target, func(t *testing.T) {
			notary := &requestorNotary{}
			warden := &L4Warden{l3Notary: notary, logger: testutil.NewTestLogger()}
			env := &govtypes.GovernanceEnvelope{
				OperatorId: target, RequestorUserId: "requesting-user",
				ActionType: string(constants.ActionTypeExecuteBash),
				Governance: &commonv1.GovernanceMetadata{L3: &commonv1.L3Metadata{Proof: &commonv1.L3Proof{CredentialId: "credential-1"}}},
			}
			valid, err := warden.verifyL3Posture(context.Background(), env, constants.PostureRatify)
			require.NoError(t, err)
			require.True(t, valid)
			require.Equal(t, env.RequestorUserId, notary.userID)
		})
	}
}

func TestL4Warden_L3DenyRejectsInEveryPosture(t *testing.T) {
	postures := []constants.Posture{constants.PostureDoctrine, constants.PostureConsensus, constants.PostureRatify, constants.PostureNotary}
	for _, posture := range postures {
		t.Run(string(posture), func(t *testing.T) {
			notary := &requestorNotary{}
			warden := &L4Warden{l3Notary: notary, logger: testutil.NewTestLogger()}
			env := &govtypes.GovernanceEnvelope{
				OperatorId: "outbound-operator", RequestorUserId: "requesting-user",
				ActionType: string(constants.ActionTypeExecuteBash),
				Governance: &commonv1.GovernanceMetadata{L3: &commonv1.L3Metadata{Denied: true}},
			}
			valid, err := warden.verifyL3Posture(context.Background(), env, posture)
			require.ErrorIs(t, err, constants.ErrTxL3Denied)
			require.False(t, valid)
			require.Empty(t, notary.userID, "a deny must never reach the notary")
			require.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED, ClassifyGovernanceRejection(err))
		})
	}
}
