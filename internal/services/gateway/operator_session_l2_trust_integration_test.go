// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/governance/governancetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestOperatorSessionValidationRefreshesL2TrustAfterUnbind(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	const (
		userID       = "trust-refresh-user"
		opID         = "trust-refresh-operator"
		sessionID    = "trust-refresh-session"
		cliSessionID = "trust-refresh-cli-session"
		memberID     = "removed-consensus-member"
	)

	oldPublicKey, oldPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, infra.SignerStore.AddTrustedSigner(t.Context(), models.TrustedSigner{
		ID: memberID, PublicKey: hex.EncodeToString(oldPublicKey), Enabled: true, AddedAt: time.Now().UTC(),
	}))
	policy := models.ConsensusPolicy{
		ID: string(constants.DocIDConsensusBinding), BindingVersion: 1,
		AppID: memberID, MemberAppIDs: []string{memberID}, Quorum: 1,
		RequireDistinct: true, Provider: string(constants.ConsensusProviderApplication), Enabled: true,
	}
	policyJSON, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionConsensus), policy.ID, policyJSON))
	oldTrust, err := infra.ConsensusStore.OperatorL2Trust(t.Context())
	require.NoError(t, err)
	require.NotNil(t, oldTrust)
	operatorTrust, err := governance.NewL2TrustStore(oldTrust)
	require.NoError(t, err)
	warden := governance.NewL4Warden(
		testutil.NewTestLogger(), testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root"),
		operatorTrust, operatorTrust, nil, governance.NewL1Doctrine(), constants.AllActionTypes, nil,
		&testutil.MockExecutionTarget{OperatorIDs: []string{opID}, SessionID: sessionID}, governancetest.UnlockedVault{},
	)

	userJSON, err := json.Marshal(&models.User{ID: userID, Status: constants.UserStatusActive})
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionUsers), userID, userJSON))
	opJSON, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
		Id: opID, UserId: userID, OperatorSessionId: sessionID,
		Status:    string(constants.OperatorStatusActive),
		CreatedAt: timestamppb.Now(), UpdatedAt: timestamppb.Now(),
	})
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), opID, opJSON))
	cliJSON, err := json.Marshal(models.CLISession{
		ID: cliSessionID, UserID: userID, OperatorSessionID: sessionID, IsActive: true,
		ExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(time.Hour), IdleExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, cliJSON))

	controller := newOperatorController(OperatorControllerDeps{
		Cfg: infra.Cfg, Logger: infra.Logger, Reg: infra.Reg, Auth: infra.Auth,
		Trust: infra.ConsensusStore, Dispatch: nil, Responder: infra.Responder,
	})
	validate := func() *models.OperatorSessionValidationResponse {
		t.Helper()
		body, marshalErr := json.Marshal(models.OperatorSessionValidationRequest{OperatorSessionID: sessionID})
		require.NoError(t, marshalErr)
		req := httptest.NewRequest(http.MethodPost, constants.APIPaths.OperatorsValidate, strings.NewReader(string(body)))
		req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyOperatorSessionID, sessionID))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		controller.handleValidateOperatorSession(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var response models.OperatorSessionValidationResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		return &response
	}

	// The still-connected Operator accepts a valid quorum vote from this member
	// before unbind, proving the subsequent rejection comes from refreshed trust.
	_, err = warden.VerifyEnvelope(t.Context(), staleMemberProof(t, oldPrivateKey, memberID, "before-unbind"))
	require.NoError(t, err)

	policy.BindingVersion++
	policy.Enabled = false
	policy.MemberAppIDs = nil
	policy.Quorum = 0
	policyJSON, err = json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionConsensus), policy.ID, policyJSON))

	validated := validate()
	require.NotNil(t, validated.L2Trust, "connected Operator validation must return the current L2 trust snapshot")
	require.Equal(t, policy.BindingVersion, validated.L2Trust.BindingVersion)
	require.NoError(t, operatorTrust.Replace(validated.L2Trust))
	_, err = warden.VerifyEnvelope(t.Context(), staleMemberProof(t, oldPrivateKey, memberID, "after-unbind"))
	require.Error(t, err, "L4 must reject a quorum proof from a member removed by unbind")
}

func staleMemberProof(t *testing.T, privateKey ed25519.PrivateKey, memberID, nonce string) *govtypes.GovernanceEnvelope {
	t.Helper()
	eventType, err := constants.RequestEventForAction(constants.ActionTypeFileEdit)
	require.NoError(t, err)
	payload, err := proto.Marshal(&operatorv1.FileEditRequested{FilePath: constants.TestPathShortData, Content: "new content", ExecutionId: nonce})
	require.NoError(t, err)
	envelope := &commonv1.GovernanceEnvelope{
		ProtocolVersion: govtypes.GovernanceProtocolVersionV2,
		Timestamp:       timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().UTC().Add(time.Hour)),
		SourceComponent: commonv1.Component_COMPONENT_CLIENT, OperatorId: "trust-refresh-operator",
		OperatorSessionId: "trust-refresh-session", EventType: string(eventType),
		ActionType: string(constants.ActionTypeFileEdit), TargetResource: "localhost", Payload: payload,
		StateMerkleRoot: "root", Nonce: nonce, Posture: string(constants.PostureConsensus),
	}
	hash, err := govtypes.GenerateMessageID(envelope)
	require.NoError(t, err)
	envelope.Id, envelope.TransactionHash = hash, hash
	signature := ed25519.Sign(privateKey, governance.L2VotePayload(hash, true))
	envelope.Governance = &commonv1.GovernanceMetadata{L2: &commonv1.L2Metadata{
		ConsensusSetId: string(constants.DocIDConsensusBinding),
		Votes:          []*commonv1.L2Vote{{SignerKeyId: memberID, Decision: true, ConsensusSignature: hex.EncodeToString(signature)}},
	}}
	return envelope
}
