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
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	cliop "github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/drive"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	"github.com/zerotrustdata/ztd/protocol"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// tribunalConsensusVoteAnswer is the only vote answer shape the decoder accepts as approve.
const tribunalConsensusVoteAnswer = `{"decision":"approve","reason":"ok"}`

// tribunalConsensusQuorum is the approval count the Tribunal binding requires.
const tribunalConsensusQuorum = 3

func TestConformanceDeliberatesWithInternalTribunalRealStack(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		posture: constants.PostureConsensus, singleRole: true, suffix: "conformance-tribunal",
		replies: tribunalConsensusVotes(len(tribunal.AllMembers)), bindConsensus: bindTribunalConsensus,
	})
	ctx := t.Context()
	stateRoot, err := env.gateway.StateRoot(ctx)
	require.NoError(t, err)
	subject, err := drive.Baseline(cases.Attempt{
		RunID: "conformance-tribunal", AttemptID: "direct-baseline",
		Case:      cases.Case{ID: "direct", Action: cases.ActionMutation},
		TargetAbs: filepath.Join(env.workingDirectory, "conformance-target.txt"), Marker: "conformance marker",
	}, drive.Identity{Persona: env.persona, Target: cliop.Target{OperatorID: env.dataOperatorID, OperatorSessionID: env.dataSessionID}}, stateRoot, time.Now())
	require.NoError(t, err)
	var exchanges []harnessclient.Exchange
	env.gateway.Record(&exchanges)
	proof, err := env.gateway.Deliberate(ctx, env.persona, subject)
	require.NoError(t, err)
	require.Len(t, proof.GetVotes(), tribunalConsensusQuorum, "fault mutations operate on the required quorum")
	require.Len(t, exchanges, 2)
	require.Equal(t, http.StatusOK, exchanges[1].Status)
	var result operatorv1.GovernanceDeliberationResult
	require.NoError(t, protojson.Unmarshal(exchanges[1].RespBody, &result))
	require.Len(t, result.GetConsensusInferences(), len(tribunal.AllMembers), "all five vote executions remain available offline")
	anchors, err := oracle.NewAnchorSet([]*evalv1.ConformanceAnchor{env.topology.Inference.Anchor})
	require.NoError(t, err)
	for _, call := range result.GetConsensusInferences() {
		require.Equal(t, subject.GetTransactionHash(), call.GetSubjectTransactionHash())
		require.NoError(t, oracle.ValidateConsensusInference(call))
		_, err := oracle.VerifyReceipt(anchors, env.topology.Inference.OperatorID, call.GetReceipt())
		require.NoError(t, err)
	}
}

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
	t.Helper()
	ctx := t.Context()
	gw := gateway.Service
	consensusStore := gw.GetConsensusStore()

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
		replies:       append([]fixtures.OllamaReply{{Content: "inference complete"}}, tribunalConsensusVotes(len(tribunal.AllMembers))...),
		bindConsensus: bindTribunalConsensus,
	})

	// A no-tool inference is not a state change. The receipt records L2 NOT_REQUIRED,
	// and only its own provider request runs: no Tribunal vote inference is added.
	inference, _, err := env.appClient.DispatchInference(ctx, &operatorv1.InferenceDispatchRequest{
		RequestSchemaVersion: constants.InferenceRequestSchemaVersion,
		Role:                 operatorv1.ModelRole_MODEL_ROLE_PRIMARY,
		Model:                realStackModelTag,
		Messages: []*operatorv1.InferenceMessage{{
			Role:  operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
			Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "answer without tools"}}},
		}},
		ProviderAttemptId:       "tribunal-consensus-no-tool-attempt",
		TargetOperatorSessionId: env.inferenceSessionID,
		ActingAppId:             protocol.NewWorkloadIdentity().AppSPIFFEID(constants.EvaluationAppName),
	})
	require.NoError(t, err)
	require.NotNil(t, inference)
	require.NotNil(t, inference.GetReceipt())
	assert.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, inference.GetReceipt().GetL2Status(), "a no-tool inference must not be voted on")
	assert.Equal(t, 1, env.stack.Provider.ChatCount(), "the no-tool inference must run without Tribunal vote inferences")

	// A read-only tool is not a state change: the Operator signs its receipt
	// with L2 NOT_REQUIRED and no member inference runs. The path is absent,
	// so the Actuator signs a FAILED receipt; only its L2 status matters here.
	readArgs, err := json.Marshal(map[string]string{"path": filepath.Join(env.workingDirectory, "tribunal-consensus-absent.txt")})
	require.NoError(t, err)
	readPayload, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: constants.MCPToolReadFile, ArgumentsJson: string(readArgs)})
	require.NoError(t, err)
	read, err := evaluation.NewCommandLane(env.gateway, env.persona).DispatchAgentTool(
		ctx, evaluation.Target{OperatorID: env.dataOperatorID, SessionID: env.dataSessionID},
		env.req.RunID, "assignment-0", "tribunal-consensus-investigation", "tribunal-consensus-attempt-0",
		constants.ActionTypeMcpCall, constants.MCPToolReadFile, readPayload,
	)
	require.NoError(t, err)
	readReceipt := read.Outcome.Observation.GetReceipt()
	require.NotNil(t, readReceipt, "read_file must answer with a signed receipt, got %s: rejection=%q refusal=%q",
		read.Outcome.Observation.GetKind(), read.Outcome.Observation.GetRejection().GetError(), read.Outcome.Observation.GetRefusal().GetError())
	assert.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, readReceipt.GetL2Status(), "a read-only tool must not be voted on")
	assert.Equal(t, 1, env.stack.Provider.ChatCount(), "a read-only tool must run no additional vote inference")

	// One governed state change through the governed HTTP dispatch path the eval lane uses.
	args := `{"command":"echo","args":["HEALTHY"]}`
	payload, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: constants.MCPToolRunShellCommand, ArgumentsJson: args})
	require.NoError(t, err)
	out, err := evaluation.NewCommandLane(env.gateway, env.persona).DispatchAgentTool(
		ctx, evaluation.Target{OperatorID: env.dataOperatorID, SessionID: env.dataSessionID},
		env.req.RunID, "assignment-1", "tribunal-consensus-investigation", "tribunal-consensus-attempt-1",
		constants.ActionTypeMcpCall, constants.MCPToolRunShellCommand, payload,
	)
	require.NoError(t, err)
	require.NoError(t, out.Outcome.Typed())

	receipt := out.Outcome.Observation.GetReceipt()
	require.NotNil(t, receipt, "the governed action must answer with a signed receipt, got %s: rejection=%q refusal=%q",
		out.Outcome.Observation.GetKind(), out.Outcome.Observation.GetRejection().GetError(), out.Outcome.Observation.GetRefusal().GetError())
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus(), "the action must execute")
	assert.Equal(t, operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, receipt.GetL2Status(), "the Tribunal quorum proof must be valid")

	// The no-tool inference used one provider request, while read_file added none.
	// The shell mutation adds exactly five member inferences, each on the pinned model.
	assert.Equal(t, len(tribunal.AllMembers)+1, env.stack.Provider.ChatCount(), "the mutation must collect the full Tribunal panel")
	assert.Equal(t, []string{realStackModelTag, realStackModelTag, realStackModelTag, realStackModelTag, realStackModelTag, realStackModelTag}, tribunalWireModels(t, env))
}

func TestTribunalConsensusRealStack_SharedRuntimeTraceClaimsVoteReceipts(t *testing.T) {
	for _, test := range []struct {
		posture  constants.Posture
		approved bool
	}{
		{posture: constants.PostureConsensus, approved: true},
		{posture: constants.PostureConsensus, approved: false},
		{posture: constants.PostureNotary, approved: true},
	} {
		t.Run(fmt.Sprintf("%s/approved=%t", test.posture, test.approved), func(t *testing.T) {
			posture, approved := test.posture, test.approved
			replies := []fixtures.OllamaReply{{ToolCalls: []fixtures.OllamaToolCall{{ID: "call-1", Name: constants.MCPToolRunShellCommand, Arguments: json.RawMessage(`{"command":"echo","args":["HEALTHY"]}`)}}}}
			votes := tribunalConsensusVotes(len(tribunal.AllMembers))
			if !approved {
				for i := range votes {
					votes[i].Content = `{"decision":"deny","reason":"not approved"}`
				}
			}
			replies = append(replies, votes...)
			replies = append(replies, fixtures.OllamaReply{Content: "HEALTHY"})
			env := newRealStackAgentRun(t, realStackOptions{
				posture: posture, shared: true, singleRole: true, suffix: "shared-tribunal-" + string(posture), replies: replies, bindConsensus: bindTribunalConsensus,
				scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
					scenario.AllowedTools = []string{constants.MCPToolRunShellCommand}
					scenario.ForbiddenTools = nil
					scenario.ExpectedTools = nil
					scenario.TribunalCommands = false
				},
			})
			res := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
			trace := executorLoadTrace(t, env, res)
			require.True(t, trace.SharedRuntime())
			require.Len(t, trace.ToolCalls, 1)
			assert.Equal(t, approved, trace.ToolCalls[0].Success, "%s", trace.ToolCalls[0].Error)
			if !approved {
				assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET, trace.ToolCalls[0].Receipt.GetRejectionCode())
			}
			inferences := trace.ToolCalls[0].ConsensusInferences
			require.Len(t, inferences, len(tribunal.AllMembers))
			for _, vote := range inferences {
				require.NotNil(t, vote.GetReceipt())
				assert.Equal(t, trace.Data.Anchor.GetKeyId(), vote.GetReceipt().GetSignerKeyId())
				assert.Equal(t, trace.ToolCalls[0].TransactionID, vote.GetSubjectTransactionId())
				local := tribunalVerifyLocalReceipt(t, env, vote.GetReceipt())
				assert.True(t, proto.Equal(local.EventReceipt, vote.GetReceipt()), "vote receipt must match the authoritative local record")
			}
			require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
			// Removing any member's execution must break the ledger bracket.
			trace.ToolCalls[0].ConsensusInferences = inferences[:len(inferences)-1]
			require.Error(t, evaluation.VerifyAgentTraceEvidence(trace))
			trace.ToolCalls[0].ConsensusInferences = inferences
			inferences[0].Receipt.Signature = "00"
			require.Error(t, evaluation.VerifyAgentTraceEvidence(trace))
		})
	}
}
