// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// External package: the Tribunal consensus runs through the production Gateway,
// Tribunal deliberator, and Inference Operator. Only the model provider is scripted.
package evaluation_test

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	"github.com/zerotrustdata/ztd/protocol"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// tribunalConsensusVoteAnswer is the only vote answer shape the decoder accepts as approve.
const tribunalConsensusVoteAnswer = `{"decision":"approve","reason":"ok"}`

// tribunalConsensusQuorum is the approval count the Tribunal binding requires.
const tribunalConsensusQuorum = 3

// tribunalConsensusMemberSigner is the test Gateway host's member signer: each
// enrolled Tribunal member signs with its own ed25519 key.
type tribunalConsensusMemberSigner struct {
	keys map[string]ed25519.PrivateKey
}

func (s *tribunalConsensusMemberSigner) HasKey(appID string) bool {
	_, ok := s.keys[appID]
	return ok
}

func (s *tribunalConsensusMemberSigner) Sign(appID string, payload []byte) ([]byte, error) {
	key, ok := s.keys[appID]
	if !ok {
		return nil, constants.ErrConsensusMemberKeyUnavailable
	}
	return ed25519.Sign(key, payload), nil
}

// tribunalConsensusVotes scripts n approving vote answers, one per member inference.
func tribunalConsensusVotes(n int) []fixtures.OllamaReply {
	replies := make([]fixtures.OllamaReply, 0, n)
	for range n {
		replies = append(replies, fixtures.OllamaReply{Content: tribunalConsensusVoteAnswer})
	}
	return replies
}

// bindTribunalConsensus replaces the fixture's application binding with the
// Tribunal panel: each member gets a trusted signer, an app policy owned by
// identity, and a key in the Gateway's member signer. It runs before Operators
// enroll, so their enrollment bundles carry the Tribunal L2 trust.
func bindTribunalConsensus(t *testing.T, gateway *fixtures.GatewayFixture, identity *fixtures.ClientIdentity) {
	ctx := t.Context()
	gw := gateway.Service
	consensusStore := gw.GetConsensusStore()

	// The fixture's binding carries the HTTPS endpoint the application provider
	// used. AddConsensus validates the endpoint of the binding document, so the
	// Tribunal binding keeps that endpoint; the Tribunal provider never dials it.
	fixtureBinding, err := consensusStore.GetBoundConsensusApp(ctx)
	require.NoError(t, err)
	require.NotNil(t, fixtureBinding)
	require.NoError(t, consensusStore.ClearConsensusBinding(ctx, ""))

	signer := &tribunalConsensusMemberSigner{keys: map[string]ed25519.PrivateKey{}}
	memberAppIDs := make([]string, 0, len(tribunal.AllMembers))
	for _, member := range tribunal.AllMembers {
		appID := tribunal.ConsensusMemberAppID(member)
		memberAppIDs = append(memberAppIDs, appID)

		publicKey, privateKey, err := ed25519.GenerateKey(nil)
		require.NoError(t, err)
		signer.keys[appID] = privateKey
		require.NoError(t, gw.GetSignerStore().AddTrustedSigner(ctx, models.TrustedSigner{
			ID: appID, PublicKey: hex.EncodeToString(publicKey), AddedAt: time.Now().UTC(), Enabled: true,
		}))

		// The Tribunal deliberator reads each member's owner from its app policy
		// document, so the member votes under the owner the test identity holds.
		// Enrollment keys app policies by SPIFFE ID; the seed matches it.
		spiffeID := protocol.NewWorkloadIdentity().AppSPIFFEID(appID)
		policy, err := json.Marshal(models.AppPolicy{AppID: spiffeID, OwnerUserID: identity.UserID})
		require.NoError(t, err)
		require.NoError(t, gw.GetDocStore().DocSet(ctx, marshaler.CollectionName(constants.CollectionAppPolicies), spiffeID, policy))
	}

	require.NoError(t, consensusStore.AddConsensus(ctx, models.ConsensusPolicy{
		ID:              string(constants.DocIDConsensusBinding),
		Provider:        string(constants.ConsensusProviderTribunal),
		EndpointURL:     fixtureBinding.EndpointURL,
		MemberAppIDs:    memberAppIDs,
		Quorum:          tribunalConsensusQuorum,
		RequireDistinct: true,
		ModelTag:        realStackModelTag,
		ModelDigest:     fixtures.ScriptedModelDigest(t, realStackModelTag),
		Enabled:         true,
	}))
	gw.SetConsensusMemberSigner(signer)
}

func TestTribunalConsensusRealStack(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{
		posture: constants.PostureConsensus, singleRole: true, suffix: "tribunal-consensus",
		replies:       tribunalConsensusVotes(tribunalConsensusQuorum),
		bindConsensus: bindTribunalConsensus,
	})

	// A read-only tool is not a state change: the Operator signs its receipt
	// with L2 NOT_REQUIRED and no member inference runs. The path is absent,
	// so the Actuator signs a FAILED receipt; only its L2 status matters here.
	readArgs, err := json.Marshal(map[string]string{"path": filepath.Join(env.workingDirectory, "tribunal-consensus-absent.txt")})
	require.NoError(t, err)
	readPayload, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: constants.MCPToolReadFile, ArgumentsJson: string(readArgs)})
	require.NoError(t, err)
	read, err := evaluation.NewCommandLane(env.gateway, env.persona).DispatchAgentTool(
		ctx, evaluation.Target{OperatorID: env.dataOperatorID, SessionID: env.dataSessionID},
		env.req.RunID, "tribunal-consensus-investigation", "tribunal-consensus-attempt-0",
		constants.ActionTypeMcpCall, constants.MCPToolReadFile, readPayload,
	)
	require.NoError(t, err)
	readReceipt := read.Outcome.Observation.GetReceipt()
	require.NotNil(t, readReceipt, "read_file must answer with a signed receipt, got %s: rejection=%q refusal=%q",
		read.Outcome.Observation.GetKind(), read.Outcome.Observation.GetRejection().GetError(), read.Outcome.Observation.GetRefusal().GetError())
	assert.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, readReceipt.GetL2Status(), "a read-only tool must not be voted on")
	assert.Zero(t, env.stack.Provider.ChatCount(), "a read-only tool must run no vote inference")

	// One governed state change through the governed HTTP dispatch path the eval lane uses.
	args := `{"command":"echo","args":["HEALTHY"]}`
	payload, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: constants.MCPToolRunShellCommand, ArgumentsJson: args})
	require.NoError(t, err)
	out, err := evaluation.NewCommandLane(env.gateway, env.persona).DispatchAgentTool(
		ctx, evaluation.Target{OperatorID: env.dataOperatorID, SessionID: env.dataSessionID},
		env.req.RunID, "tribunal-consensus-investigation", "tribunal-consensus-attempt-1",
		constants.ActionTypeMcpCall, constants.MCPToolRunShellCommand, payload,
	)
	require.NoError(t, err)
	require.NoError(t, out.Outcome.Typed())

	receipt := out.Outcome.Observation.GetReceipt()
	require.NotNil(t, receipt, "the governed action must answer with a signed receipt, got %s: rejection=%q refusal=%q",
		out.Outcome.Observation.GetKind(), out.Outcome.Observation.GetRejection().GetError(), out.Outcome.Observation.GetRefusal().GetError())
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus(), "the action must execute")
	assert.Equal(t, operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, receipt.GetL2Status(), "the Tribunal quorum proof must be valid")

	// The binding stops at quorum, so exactly three member inferences ran, each on the pinned model.
	assert.Equal(t, tribunalConsensusQuorum, env.stack.Provider.ChatCount(), "exactly quorum vote inferences must run")
	assert.Equal(t, []string{realStackModelTag, realStackModelTag, realStackModelTag}, tribunalWireModels(t, env))
}
