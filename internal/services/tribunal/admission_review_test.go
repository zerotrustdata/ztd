// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

const (
	admittedVoteReason   = "SAFE: lists sockets read-only"
	admittedVoteSigner   = "tribunal-axiom"
	admittedL3Signature  = "cli-signature-marker"
	admittedRevision     = `{"verdict":"REVISE","revised_arguments":{"command":"ss","args":["-tlnp"]},"rationale":"numeric ports"}`
	admittedL1Revision   = `{"verdict":"REVISE","revised_arguments":{"command":"rm","args":["-rf","/"]},"rationale":"cleanup"}`
	admittedHighRisk     = `{"risk": "HIGH", "rationale": "unexpected", "feedback": "keep the approved command"}`
	admittedWorkingDir   = "/workspace"
	admittedTimeoutSecs  = 17
	admittedTransaction  = "txn-admitted"
	admittedAuditCallIdx = 0
)

// admittedEnvelope is an admitted envelope carrying one recorded L2 vote and,
// when l3 is set, the given L3 decision.
func admittedEnvelope(l3 *commonv1.L3Metadata, votes ...*commonv1.L2Vote) *commonv1.GovernanceEnvelope {
	return &commonv1.GovernanceEnvelope{
		Id:        admittedTransaction,
		ExpiresAt: timestamppb.New(time.Now().Add(time.Minute)),
		Governance: &commonv1.GovernanceMetadata{
			L1: &commonv1.L1Metadata{},
			L2: &commonv1.L2Metadata{Votes: votes},
			L3: l3,
		},
	}
}

func admittedVote() *commonv1.L2Vote {
	return &commonv1.L2Vote{SignerKeyId: admittedVoteSigner, ConsensusSignature: "sig", Decision: true}
}

func admittedL3Approval() *commonv1.L3Metadata {
	return &commonv1.L3Metadata{Proof: &commonv1.L3Proof{CliSignature: admittedL3Signature}}
}

// admittedVoteInference is the recorded inference behind admittedVote.
func admittedVoteInference() []*operatorv1.ConsensusVoteInference {
	return []*operatorv1.ConsensusVoteInference{{
		SubjectTransactionId: admittedTransaction,
		MemberAppId:          admittedVoteSigner,
		Result: &operatorv1.InferenceResult{Parts: []*operatorv1.InferenceResponsePart{
			{Part: &operatorv1.InferenceResponsePart_Text{Text: admittedVoteReason}},
		}},
	}}
}

// admittedRequest is an intent request whose Gateway-owned fields are set, so
// a revision visibly keeps them.
func admittedRequest() *tribunalv1.TribunalIntentRequest {
	req := intentRequest(memberTemplate())
	req.WorkingDirectory = admittedWorkingDir
	req.TimeoutSeconds = admittedTimeoutSecs
	return req
}

func admittedArguments() models.RunShellCommandRequest {
	return models.RunShellCommandRequest{Command: "ss", Args: []string{"-tlpn"}, Timeout: admittedTimeoutSecs, WorkingDir: admittedWorkingDir}
}

// admittedResult is an approved deliberation result for admittedArguments.
func admittedResult() *tribunalv1.TribunalIntentResult {
	arguments := admittedArguments()
	return &tribunalv1.TribunalIntentResult{
		Outcome:       tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED,
		Arguments:     arguments.Proto(),
		ArgumentsJson: arguments.CanonicalJSON(),
	}
}

func admittedService(t *testing.T, dispatcher *fakeMemberDispatcher) *Service {
	t.Helper()
	service, err := NewService(dispatcher, governance.NewL1Doctrine())
	require.NoError(t, err)
	return service
}

func TestReviewAdmittedGivesAuditorActualGovernanceEvidence(t *testing.T) {
	dispatcher := &fakeMemberDispatcher{
		generate: proposals(map[Member]string{
			MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "ss -tlpn", MemberPragma: "netstat -tlpn", MemberNemesis: "lsof -i",
		}),
		marshal: lowRisk,
		audit:   []string{approve, approve},
	}
	service := admittedService(t, dispatcher)
	req := admittedRequest()
	result, err := service.Deliberate(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, result.GetOutcome())
	approvedJSON := result.GetArgumentsJson()
	authoringSteps, authoringCalls := len(result.GetSteps()), len(result.GetMemberCalls())

	env := admittedEnvelope(admittedL3Approval(), admittedVote())
	original := proto.Clone(env)
	require.NoError(t, service.ReviewAdmitted(context.Background(), req, result, env, admittedVoteInference()))

	assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, result.GetOutcome())
	assert.Equal(t, approvedJSON, result.GetArgumentsJson(), "an approving review keeps the authorized bytes")
	assert.True(t, proto.Equal(original, env), "the review never edits the admitted envelope")

	require.Len(t, result.GetMemberCalls(), authoringCalls+1, "the contextual review is one more recorded member call")
	call := result.GetMemberCalls()[authoringCalls]
	assert.Equal(t, tribunalv1.TribunalStage_TRIBUNAL_STAGE_AUDIT, call.GetStage())
	assert.Equal(t, fmt.Sprintf("%s-%d", testPrefix, authoringCalls+1), call.GetProviderAttemptId())
	require.Len(t, result.GetSteps(), authoringSteps+1)
	assert.NotNil(t, result.GetSteps()[authoringSteps].GetReview())

	prompt := messageText(dispatcher.requests[len(dispatcher.requests)-1], operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER)
	for _, evidence := range []string{
		"POST-GOVERNANCE REVIEW",
		governance.NewL1Doctrine().DoctrineBundleHash(),
		admittedVoteSigner,
		admittedVoteReason,
		admittedL3Signature,
		admittedTransaction,
		"<authoring_step>",
		testRequest,
	} {
		assert.Contains(t, prompt, evidence, "the Auditor receives the actual recorded decision")
	}
	assert.NotContains(t, prompt, "(support:", "post-governance review shows the authorized command, not authoring support counts")
}

func TestReviewAdmittedRevisionRequiresFreshDecisions(t *testing.T) {
	dispatcher := &fakeMemberDispatcher{marshal: lowRisk, audit: []string{admittedRevision}}
	result := admittedResult()
	env := admittedEnvelope(admittedL3Approval(), admittedVote())
	original := proto.Clone(env)

	err := admittedService(t, dispatcher).ReviewAdmitted(context.Background(), admittedRequest(), result, env, admittedVoteInference())
	require.ErrorIs(t, err, constants.ErrTribunalCommandRevised)

	revised := models.RunShellCommandRequest{Command: "ss", Args: []string{"-tlnp"}, Timeout: admittedTimeoutSecs, WorkingDir: admittedWorkingDir}
	assert.Equal(t, revised.CanonicalJSON(), result.GetArgumentsJson(), "the revision keeps the Gateway-owned timeout and working directory")
	assert.Equal(t, "ss", result.GetArguments().GetCommand())
	assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, result.GetOutcome())
	assert.True(t, proto.Equal(original, env), "earlier decisions stay bound to the earlier envelope")
	require.Len(t, dispatcher.requests, 2, "Marshal assesses the revision before it can be dispatched")
	assert.Equal(t, marshalSystemPrompt, messageText(dispatcher.requests[1], operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM))
	require.Len(t, result.GetSteps(), 2)
	assert.NotNil(t, result.GetSteps()[0].GetReview())
}

func TestReviewAdmittedSkipsDeniedAndUnevidencedEnvelopes(t *testing.T) {
	for name, env := range map[string]*commonv1.GovernanceEnvelope{
		"human denial":         admittedEnvelope(&commonv1.L3Metadata{Denied: true}, admittedVote()),
		"no L2 or L3 evidence": admittedEnvelope(nil),
	} {
		t.Run(name, func(t *testing.T) {
			dispatcher := &fakeMemberDispatcher{marshal: lowRisk, audit: []string{admittedRevision}}
			result := admittedResult()
			want := proto.Clone(result)
			require.NoError(t, admittedService(t, dispatcher).ReviewAdmitted(context.Background(), admittedRequest(), result, env, admittedVoteInference()))
			assert.Empty(t, dispatcher.requests, "no model reviews a denied or already-screened command")
			assert.True(t, proto.Equal(want, result))
		})
	}
}

func TestReviewAdmittedFailsClosed(t *testing.T) {
	expired := admittedEnvelope(admittedL3Approval())
	expired.ExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
	cases := []struct {
		name       string
		dispatcher *fakeMemberDispatcher
		env        *commonv1.GovernanceEnvelope
		cancel     bool
		outcome    tribunalv1.TribunalOutcome
		wantErr    error
		failure    string
		requests   int
	}{
		{
			name:       "L1-blocked revision is never approved",
			dispatcher: &fakeMemberDispatcher{marshal: lowRisk, audit: []string{admittedL1Revision}},
			outcome:    tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED, wantErr: constants.ErrTribunalAnswerInvalid,
			failure: "L1 doctrine", requests: auditorMaxAttempts,
		},
		{
			name:       "revision Marshal rates HIGH is blocked",
			dispatcher: &fakeMemberDispatcher{marshal: admittedHighRisk, audit: []string{admittedRevision}},
			outcome:    tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_BLOCKED, wantErr: constants.ErrTribunalAnswerInvalid, requests: 2,
		},
		{
			name:       "Auditor inference failure",
			dispatcher: &fakeMemberDispatcher{marshal: lowRisk, audit: []string{approve}, fail: StageAudit},
			outcome:    tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED, wantErr: constants.ErrTribunalInferenceFailed, requests: 1,
		},
		{
			name:       "envelope lifetime exhausted",
			dispatcher: &fakeMemberDispatcher{marshal: lowRisk, audit: []string{approve}},
			env:        expired,
			outcome:    tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED, wantErr: context.DeadlineExceeded,
			failure: "lifetime",
		},
		{
			name:       "caller cancellation",
			dispatcher: &fakeMemberDispatcher{marshal: lowRisk, audit: []string{approve}},
			cancel:     true,
			outcome:    tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, wantErr: context.Canceled,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.env
			if env == nil {
				env = admittedEnvelope(admittedL3Approval(), admittedVote())
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			result := admittedResult()
			approvedJSON := result.GetArgumentsJson()
			err := admittedService(t, tc.dispatcher).ReviewAdmitted(ctx, admittedRequest(), result, env, admittedVoteInference())
			require.ErrorIs(t, err, tc.wantErr)
			require.NotErrorIs(t, err, constants.ErrTribunalCommandRevised)
			assert.Equal(t, tc.outcome, result.GetOutcome())
			assert.Equal(t, approvedJSON, result.GetArgumentsJson(), "a failed review never replaces the authorized command")
			assert.Contains(t, result.GetAuditFailure(), tc.failure)
			assert.Len(t, tc.dispatcher.requests, tc.requests)
		})
	}
}
