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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/agentpersona"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

const (
	testPrefix         = "tribunal-run"
	testFailureMessage = "fake member dispatch failure"
)

// fakeMemberDispatcher answers each governed member dispatch the way
// fakeInferer answers a deliberation call, routing on the system prompt, and
// records every request in order. Each answered dispatch gets the transaction
// id "txn-<n>" and a receipt carrying it.
type fakeMemberDispatcher struct {
	generate   func(round uint32, member Member) string
	marshal    string
	audit      []string
	fail       Stage
	auditCalls int
	requests   []*operatorv1.InferenceDispatchRequest
}

func (f *fakeMemberDispatcher) DispatchMember(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, *operatorv1.InferenceDispatchFailure) {
	f.requests = append(f.requests, req)
	txn := fmt.Sprintf("txn-%d", len(f.requests))
	receipt := &operatorv1.EventReceipt{TransactionId: txn}
	stage, answer := f.answer(req)
	if f.fail != "" && stage == f.fail {
		return nil, &operatorv1.InferenceDispatchFailure{Error: testFailureMessage, Receipt: receipt, ObservationReceipts: []*operatorv1.EventReceipt{{TransactionId: "observation"}}}
	}
	return &operatorv1.InferenceDispatchResponse{
		TransactionId: txn,
		Result: &operatorv1.InferenceResult{Parts: []*operatorv1.InferenceResponsePart{
			{Part: &operatorv1.InferenceResponsePart_Text{Text: answer}},
		}},
		Receipt: receipt,
	}, nil
}

// answer routes one dispatch by its system prompt and returns the stage it
// serves with that stage's answer text.
func (f *fakeMemberDispatcher) answer(req *operatorv1.InferenceDispatchRequest) (Stage, string) {
	system := messageText(req, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM)
	user := messageText(req, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER)
	switch system {
	case testPersonaPrompt(PersonaMarshal):
		return StageMarshal, f.marshal
	case testPersonaPrompt(PersonaAuditor):
		f.auditCalls++
		return StageAudit, f.audit[min(f.auditCalls, len(f.audit))-1]
	}
	for _, member := range AllMembers {
		if system == testPersonaPrompt(string(member)) {
			round := uint32(1)
			if strings.Contains(user, "did not agree in round 1") {
				round = 2
			}
			return StageGenerate, proposalAnswer(f.generate(round, member))
		}
	}
	return "", ""
}

// messageText returns the concatenated text of the first message with role.
func messageText(req *operatorv1.InferenceDispatchRequest, role operatorv1.InferenceMessageRole) string {
	var text strings.Builder
	for _, message := range req.GetMessages() {
		if message.GetRole() != role {
			continue
		}
		for _, part := range message.GetParts() {
			text.WriteString(part.GetText())
		}
		break
	}
	return text.String()
}

// memberTemplate is a valid caller-supplied member template: it sets only
// what the caller may choose, leaving the per-call fields to the Gateway.
func memberTemplate() *operatorv1.InferenceDispatchRequest {
	return &operatorv1.InferenceDispatchRequest{
		Role:        operatorv1.ModelRole_MODEL_ROLE_PRIMARY,
		Model:       "model:tag",
		ModelDigest: "sha256:digest",
	}
}

// intentRequest is a valid intent request over the given member template.
func intentRequest(template *operatorv1.InferenceDispatchRequest) *tribunalv1.TribunalIntentRequest {
	registry, _ := agentpersona.NewRegistry()
	bindings := make([]*evalv1.AgentPersonaBinding, 0, len(AllMembers)+2)
	for _, id := range []string{string(MemberAxiom), string(MemberConcord), string(MemberVariance), string(MemberPragma), string(MemberNemesis), PersonaMarshal, PersonaAuditor} {
		p, _ := registry.Get(id)
		bindings = append(bindings, &evalv1.AgentPersonaBinding{PersonaId: p.ID, Version: p.Version, Digest: p.Digest(), VoteDigest: p.VoteDigest()})
	}
	return &tribunalv1.TribunalIntentRequest{
		Request:                 testRequest,
		TargetOperatorSessionId: "operator-session-1",
		ProviderAttemptPrefix:   testPrefix,
		Tier:                    evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
		MemberInference:         template,
		MarshalInference:        proto.Clone(template).(*operatorv1.InferenceDispatchRequest),
		AuditorInference:        proto.Clone(template).(*operatorv1.InferenceDispatchRequest),
		Personas:                bindings,
		Homogeneous:             true,
	}
}

func TestDeliberateApproved(t *testing.T) {
	template := memberTemplate()
	original := proto.Clone(template)
	dispatcher := &fakeMemberDispatcher{
		generate: proposals(map[Member]string{
			MemberAxiom: "ss -tlpn", MemberConcord: "ss -tlpn", MemberVariance: "ss -tlpn", MemberPragma: "netstat -tlpn", MemberNemesis: "netstat -tlpn",
		}),
		marshal: lowRisk,
		audit:   []string{approve},
	}
	service, err := NewService(dispatcher, governance.NewL1Doctrine())
	require.NoError(t, err)
	result, err := service.Deliberate(context.Background(), intentRequest(template))
	require.NoError(t, err)
	assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, result.GetOutcome())
	assert.Equal(t, "ss -tlpn", result.GetArguments().GetCommand()+" "+strings.Join(result.GetArguments().GetArgs(), " "))
	assert.NotEmpty(t, result.GetSteps())

	// Five generations, one marshal call, one audit call.
	wantCalls := len(AllMembers) + 2
	require.Len(t, dispatcher.requests, wantCalls)
	calls := result.GetMemberCalls()
	require.Len(t, calls, wantCalls)
	for i, request := range dispatcher.requests {
		attemptID := fmt.Sprintf("%s-%d", testPrefix, i+1)
		assert.Equal(t, attemptID, request.GetProviderAttemptId())
		assert.Equal(t, attemptID, calls[i].GetProviderAttemptId())
		assert.Equal(t, template.GetRole(), request.GetRole())
		assert.Equal(t, "model:tag", request.GetModel())
		assert.Equal(t, "sha256:digest", request.GetModelDigest())
		assert.Empty(t, request.GetActingAppId())
		require.Len(t, request.GetMessages(), 2)
		assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, request.GetMessages()[0].GetRole())
		assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, request.GetMessages()[1].GetRole())
		assert.Contains(t, messageText(request, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER), testRequest)

		var stage tribunalv1.TribunalStage
		var persona, schema string
		system := messageText(request, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM)
		switch {
		case i < len(AllMembers):
			stage, persona, schema = tribunalv1.TribunalStage_TRIBUNAL_STAGE_GENERATE, string(AllMembers[i]), generationJSONSchema
			assert.Equal(t, testPersonaPrompt(string(AllMembers[i])), system)
		case i == len(AllMembers):
			stage, persona, schema = tribunalv1.TribunalStage_TRIBUNAL_STAGE_MARSHAL, PersonaMarshal, marshalJSONSchema
			assert.Equal(t, testPersonaPrompt(PersonaMarshal), system)
		default:
			stage, persona, schema = tribunalv1.TribunalStage_TRIBUNAL_STAGE_AUDIT, PersonaAuditor, auditorJSONSchema([]string{"cluster_b"})
			assert.Equal(t, testPersonaPrompt(PersonaAuditor), system)
		}
		assert.Equal(t, constants.MediaTypeJSON, request.GetResponseFormat().GetMediaType())
		assert.Equal(t, schema, request.GetResponseFormat().GetJsonSchema())
		assert.Equal(t, stage, calls[i].GetStage())
		assert.Equal(t, persona, calls[i].GetPersona())
		assert.Equal(t, uint32(1), calls[i].GetRound())
		txn := fmt.Sprintf("txn-%d", i+1)
		assert.Equal(t, txn, calls[i].GetTransactionId())
		assert.Equal(t, txn, calls[i].GetReceipt().GetTransactionId())
		assert.Nil(t, calls[i].GetFailure())
	}
	assert.True(t, proto.Equal(original, template), "member template must not be mutated")
}

func TestDeliberateDispatchesEachPersonaOnItsStageTemplate(t *testing.T) {
	request := intentRequest(memberTemplate())
	request.Homogeneous = false
	request.MemberInference.Role = operatorv1.ModelRole_MODEL_ROLE_LITE
	request.MarshalInference.Role = operatorv1.ModelRole_MODEL_ROLE_ASSISTANT
	request.AuditorInference.Role = operatorv1.ModelRole_MODEL_ROLE_PRIMARY
	dispatcher := &fakeMemberDispatcher{
		generate: proposals(map[Member]string{MemberAxiom: "ss -l", MemberConcord: "ss -l", MemberVariance: "ss -l", MemberPragma: "ss -l", MemberNemesis: "ss -l"}),
		marshal:  lowRisk, audit: []string{approve},
	}
	service, err := NewService(dispatcher, governance.NewL1Doctrine())
	require.NoError(t, err)
	_, err = service.Deliberate(context.Background(), request)
	require.NoError(t, err)
	for _, dispatched := range dispatcher.requests {
		system := messageText(dispatched, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM)
		want := operatorv1.ModelRole_MODEL_ROLE_LITE
		switch system {
		case testPersonaPrompt(PersonaMarshal):
			want = operatorv1.ModelRole_MODEL_ROLE_ASSISTANT
		case testPersonaPrompt(PersonaAuditor):
			want = operatorv1.ModelRole_MODEL_ROLE_PRIMARY
		}
		assert.Equal(t, want, dispatched.GetRole(), "persona prompt selects stage's frozen inference template")
	}
}

func TestDeliberateMemberFailure(t *testing.T) {
	for _, stage := range []Stage{StageGenerate, StageMarshal, StageAudit} {
		t.Run(string(stage), func(t *testing.T) {
			dispatcher := &fakeMemberDispatcher{
				generate: proposals(map[Member]string{MemberAxiom: "pwd", MemberConcord: "pwd", MemberVariance: "x", MemberPragma: "y", MemberNemesis: "z"}),
				marshal:  lowRisk,
				audit:    []string{approve},
				fail:     stage,
			}
			service, err := NewService(dispatcher, governance.NewL1Doctrine())
			require.NoError(t, err)
			result, err := service.Deliberate(context.Background(), intentRequest(memberTemplate()))
			require.NoError(t, err)
			assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED, result.GetOutcome())
			assert.Empty(t, result.GetArguments().GetCommand())

			calls := result.GetMemberCalls()
			require.Len(t, calls, len(dispatcher.requests))
			last := calls[len(calls)-1]
			assert.Equal(t, stage.Proto(), last.GetStage())
			assert.Equal(t, dispatcher.requests[len(calls)-1].GetProviderAttemptId(), last.GetProviderAttemptId())
			assert.Equal(t, testFailureMessage, last.GetFailure().GetError())
			assert.Equal(t, fmt.Sprintf("txn-%d", len(calls)), last.GetReceipt().GetTransactionId())
			assert.Equal(t, last.GetReceipt().GetTransactionId(), last.GetTransactionId())
			assert.Len(t, last.GetObservationReceipts(), 1)
		})
	}
}

func TestDeliberateBlocked(t *testing.T) {
	dispatcher := &fakeMemberDispatcher{
		generate: proposals(map[Member]string{MemberAxiom: "rm ./build/file", MemberConcord: "rm ./build/file", MemberVariance: "x", MemberPragma: "y", MemberNemesis: "z"}),
		marshal:  `{"risk": "HIGH", "rationale": "deletes /", "feedback": "scope the path"}`,
		audit:    []string{approve},
	}
	service, err := NewService(dispatcher, governance.NewL1Doctrine())
	require.NoError(t, err)
	result, err := service.Deliberate(context.Background(), intentRequest(memberTemplate()))
	require.NoError(t, err)
	assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_BLOCKED, result.GetOutcome())
	assert.Empty(t, result.GetArguments().GetCommand())
	assert.Equal(t, "deletes /", result.GetRiskRationale())
	assert.Equal(t, "scope the path", result.GetRiskFeedback())
	assert.Empty(t, result.GetAuditFailure())
	// A blocked command is never audited.
	assert.Len(t, result.GetMemberCalls(), len(AllMembers)+1)
}

func TestDeliberateAuditFailed(t *testing.T) {
	dispatcher := &fakeMemberDispatcher{
		generate: proposals(map[Member]string{MemberAxiom: "pwd", MemberConcord: "pwd", MemberVariance: "x", MemberPragma: "y", MemberNemesis: "z"}),
		marshal:  lowRisk,
		audit:    []string{"looks fine"},
	}
	service, err := NewService(dispatcher, governance.NewL1Doctrine())
	require.NoError(t, err)
	result, err := service.Deliberate(context.Background(), intentRequest(memberTemplate()))
	require.NoError(t, err)
	assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED, result.GetOutcome())
	assert.Empty(t, result.GetArguments().GetCommand())
	assert.NotEmpty(t, result.GetAuditFailure())
	assert.Empty(t, result.GetRiskRationale())
	// The Auditor gets auditorMaxAttempts answers before the audit fails closed.
	assert.Len(t, result.GetMemberCalls(), len(AllMembers)+1+auditorMaxAttempts)
}

func TestValidateIntentRequest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(req *tribunalv1.TribunalIntentRequest)
		want   error
	}{
		{
			name: "empty request",
			mutate: func(req *tribunalv1.TribunalIntentRequest) {
				req.Request = ""
				req.TargetOperatorSessionId = ""
				req.ProviderAttemptPrefix = ""
				req.MemberInference = nil
			},
			want: constants.ErrMissingRequiredField,
		},
		{name: "blank request text", mutate: func(req *tribunalv1.TribunalIntentRequest) { req.Request = "   " }, want: constants.ErrMissingRequiredField},
		{name: "missing target_operator_session_id", mutate: func(req *tribunalv1.TribunalIntentRequest) { req.TargetOperatorSessionId = "" }, want: constants.ErrMissingRequiredField},
		{name: "missing provider_attempt_prefix", mutate: func(req *tribunalv1.TribunalIntentRequest) { req.ProviderAttemptPrefix = "" }, want: constants.ErrMissingRequiredField},
		{name: "nil member_inference", mutate: func(req *tribunalv1.TribunalIntentRequest) { req.MemberInference = nil }, want: constants.ErrMissingRequiredField},
		{name: "nil marshal_inference", mutate: func(req *tribunalv1.TribunalIntentRequest) { req.MarshalInference = nil }, want: constants.ErrMissingRequiredField},
		{name: "nil auditor_inference", mutate: func(req *tribunalv1.TribunalIntentRequest) { req.AuditorInference = nil }, want: constants.ErrMissingRequiredField},
		{name: "stale expected persona digest", mutate: func(req *tribunalv1.TribunalIntentRequest) { req.Personas[0].Digest = strings.Repeat("0", 64) }, want: constants.ErrPersonaDigestMismatch},
		{name: "stale expected vote digest", mutate: func(req *tribunalv1.TribunalIntentRequest) { req.Personas[0].VoteDigest = strings.Repeat("0", 64) }, want: constants.ErrPersonaDigestMismatch},
		{
			name: "unspecified role",
			mutate: func(req *tribunalv1.TribunalIntentRequest) {
				req.MemberInference.Role = operatorv1.ModelRole_MODEL_ROLE_UNSPECIFIED
			},
			want: constants.ErrMissingRequiredField,
		},
		{
			name: "template sets messages",
			mutate: func(req *tribunalv1.TribunalIntentRequest) {
				req.MemberInference.Messages = []*operatorv1.InferenceMessage{{}}
			},
			want: constants.ErrTribunalTemplateInvalid,
		},
		{
			name: "template sets tools",
			mutate: func(req *tribunalv1.TribunalIntentRequest) {
				req.MemberInference.Tools = []*operatorv1.InferenceToolDeclaration{{}}
			},
			want: constants.ErrTribunalTemplateInvalid,
		},
		{
			name: "template sets provider_attempt_id",
			mutate: func(req *tribunalv1.TribunalIntentRequest) {
				req.MemberInference.ProviderAttemptId = "tribunal-run-1"
			},
			want: constants.ErrTribunalTemplateInvalid,
		},
		{
			name: "template sets acting_app_id",
			mutate: func(req *tribunalv1.TribunalIntentRequest) {
				req.MemberInference.ActingAppId = "app-1"
			},
			want: constants.ErrTribunalTemplateInvalid,
		},
		{
			name: "template sets stream",
			mutate: func(req *tribunalv1.TribunalIntentRequest) {
				req.MemberInference.Stream = true
			},
			want: constants.ErrTribunalTemplateInvalid,
		},
		{
			name: "template sets response_format",
			mutate: func(req *tribunalv1.TribunalIntentRequest) {
				req.MemberInference.ResponseFormat = &operatorv1.InferenceResponseFormat{MediaType: constants.MediaTypeJSON, JsonSchema: `{"type":"object"}`}
			},
			want: constants.ErrTribunalTemplateInvalid,
		},
		{
			name: "unspecified tier",
			mutate: func(req *tribunalv1.TribunalIntentRequest) {
				req.Tier = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED
			},
			want: constants.ErrMissingRequiredField,
		},
		{
			name:   "tier is not a campaign role",
			mutate: func(req *tribunalv1.TribunalIntentRequest) { req.Tier = evalv1.ModelCampaignRole(99) },
			want:   constants.ErrInferenceRoleInvalid,
		},
		{
			name: "stage templates may use different model roles",
			mutate: func(req *tribunalv1.TribunalIntentRequest) {
				req.Homogeneous = false
				req.MemberInference.Role = operatorv1.ModelRole_MODEL_ROLE_LITE
				req.MarshalInference.Role = operatorv1.ModelRole_MODEL_ROLE_ASSISTANT
				req.AuditorInference.Role = operatorv1.ModelRole_MODEL_ROLE_PRIMARY
			},
			want: nil,
		},
		{name: "formation member must use Lite", mutate: func(req *tribunalv1.TribunalIntentRequest) {
			req.Homogeneous = false
			req.MemberInference.Role = operatorv1.ModelRole_MODEL_ROLE_ASSISTANT
		}, want: constants.ErrTribunalTierMismatch},
		{name: "formation Marshal must use Assistant", mutate: func(req *tribunalv1.TribunalIntentRequest) {
			req.Homogeneous = false
			req.MarshalInference.Role = operatorv1.ModelRole_MODEL_ROLE_LITE
		}, want: constants.ErrTribunalTierMismatch},
		{name: "formation Auditor must use Primary", mutate: func(req *tribunalv1.TribunalIntentRequest) {
			req.Homogeneous = false
			req.AuditorInference.Role = operatorv1.ModelRole_MODEL_ROLE_LITE
		}, want: constants.ErrTribunalTierMismatch},
		{name: "homogeneous stages must bind the same candidate", mutate: func(req *tribunalv1.TribunalIntentRequest) {
			req.AuditorInference.ModelDigest = "other"
		}, want: constants.ErrTribunalTierMismatch},
		{
			name: "assistant tier with assistant member role",
			mutate: func(req *tribunalv1.TribunalIntentRequest) {
				req.Tier = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT
				req.MemberInference.Role = operatorv1.ModelRole_MODEL_ROLE_ASSISTANT
				req.MarshalInference.Role = operatorv1.ModelRole_MODEL_ROLE_ASSISTANT
				req.AuditorInference.Role = operatorv1.ModelRole_MODEL_ROLE_ASSISTANT
			},
			want: nil,
		},
		{name: "valid request", mutate: func(*tribunalv1.TribunalIntentRequest) {}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := intentRequest(memberTemplate())
			tt.mutate(req)
			err := ValidateIntentRequest(req)
			if tt.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.want)
		})
	}
}
