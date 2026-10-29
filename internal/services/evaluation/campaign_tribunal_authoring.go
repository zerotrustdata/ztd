// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

func tribunalPersonaBindings(frozen []*evalv1.AgentPersonaBinding) ([]*evalv1.AgentPersonaBinding, error) {
	ids := []string{string(tribunal.MemberAxiom), string(tribunal.MemberConcord), string(tribunal.MemberVariance), string(tribunal.MemberPragma), string(tribunal.MemberNemesis), tribunal.PersonaMarshal, tribunal.PersonaAuditor}
	bindings := make([]*evalv1.AgentPersonaBinding, 0, len(ids))
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	found := make(map[string]*evalv1.AgentPersonaBinding, len(ids))
	counts := make(map[string]int, len(ids))
	for _, binding := range frozen {
		if binding != nil && want[binding.GetPersonaId()] {
			counts[binding.GetPersonaId()]++
			found[binding.GetPersonaId()] = binding
		}
	}
	for _, id := range ids {
		if counts[id] != 1 || found[id] == nil {
			return nil, fmt.Errorf("evaluation: frozen Tribunal persona %s count is %d: %w", id, counts[id], constants.ErrPersonaDigestMismatch)
		}
		bindings = append(bindings, proto.Clone(found[id]).(*evalv1.AgentPersonaBinding))
	}
	return bindings, nil
}

// Model-visible answers for a deliberation that wrote no approved command.
const (
	tribunalNoConsensusError   = "ZTD-1607: Tool execution failed for run_shell_command: the Tribunal reached no consensus on a command for this request; restate the request more specifically"
	tribunalAuditFailedError   = "ZTD-1607: Tool execution failed for run_shell_command: the Auditor could not review the Tribunal's command (%s)"
	tribunalMarshalBlocked     = "MARSHAL BLOCKED: %s (feedback: %s)"
	tribunalMarshalConflict    = "AGENT CONFLICT: Marshal blocked command twice. Human intervention required."
	tribunalInferenceFailedMsg = "ZTD-1607: Tool execution failed for run_shell_command: Tribunal inference failed (%s)"
)

// modelCallGovernanceRejected is the error a model call records when required
// L2 refused its inference (isL2GovernanceDenial): the provider never ran.
const modelCallGovernanceRejected = "governance_rejected"

const (
	formationTribunalMemberRole  = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE
	formationTribunalMarshalRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT
	formationTribunalAuditorRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
)

func formationTribunalRoleForPurpose(purpose string) evalv1.ModelCampaignRole {
	switch strings.TrimPrefix(purpose, agentModelCallPurposeTribunalPrefix) {
	case string(tribunal.StageGenerate):
		return formationTribunalMemberRole
	case string(tribunal.StageMarshal):
		return formationTribunalMarshalRole
	case string(tribunal.StageAudit):
		return formationTribunalAuditorRole
	default:
		return evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED
	}
}

func formationTribunalRoleForTrace(trace *AgentTrace, purpose string) evalv1.ModelCampaignRole {
	if trace != nil && trace.SchemaVersion != agentTraceSchemaVersion {
		// v7-v10 recorded every formation Tribunal stage on the Lite slot.
		return formationTribunalMemberRole
	}
	return formationTribunalRoleForPurpose(purpose)
}

// formationInterrogatorRole is the stack slot the Interrogator persona runs on
// in a formation assignment; a homogeneous assignment's candidate runs it.
const formationInterrogatorRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE

// runTribunal routes one run_shell_command proposal through Tribunal authoring
// on the Gateway (POST /api/v1/operators/intents). The Gateway
// deliberates over governed member inferences and dispatches only an approved
// command, as an ordinary governed MCP_CALL. Every member inference joins
// the trace in deliberation order under the tool call that owns it, every
// persona step joins the trace, and the command's governed answer is recorded
// as a direct command's is. A deliberation that approves no command is a
// model-visible failure; a member inference failure is also recorded as the
// trace's provider failure, which ends the loop.
func (r *agentRun) runTribunal(ctx context.Context, record AgentToolCall, proposal models.RunShellCommandRequest) (AgentToolCall, error) {
	if len(proposal.Hostnames) > 0 {
		return r.fail(record, agentToolFailure{ErrorType: agentToolErrorExecution, Error: "run_shell_command Tribunal authoring does not accept hostnames"})
	}
	workingDir := r.workspace.Root
	if proposal.WorkingDir != "" {
		workingDir = r.workspace.Resolve(proposal.WorkingDir)
		if !pathWithin(workingDir, r.workspace.OperatorWorkingDirectory) {
			return r.fail(record, agentToolFailure{ErrorType: agentToolErrorSecurity,
				Error: fmt.Sprintf("security violation: working directory %q is outside operator workspace", proposal.WorkingDir)})
		}
	}
	tiers := map[tribunal.Stage]evalv1.ModelCampaignRole{tribunal.StageGenerate: r.designated, tribunal.StageMarshal: r.designated, tribunal.StageAudit: r.designated}
	if IsHeterogeneousAssignment(r.req.Assignment) {
		tiers[tribunal.StageGenerate], tiers[tribunal.StageMarshal], tiers[tribunal.StageAudit] = formationTribunalMemberRole, formationTribunalMarshalRole, formationTribunalAuditorRole
	}
	prefix := r.executor.newID("tribunal-inference")
	templates := make(map[tribunal.Stage]*operatorv1.InferenceDispatchRequest, 3)
	for stage, tier := range tiers {
		probe, err := r.bindInference(&AgentModelCall{}, tier, prefix, nil, nil)
		if err != nil {
			return AgentToolCall{}, err
		}
		template, err := r.inferenceDispatchRequest(probe)
		if err != nil {
			return AgentToolCall{}, err
		}
		template.Messages, template.ProviderAttemptId, template.ActingAppId = nil, "", ""
		templates[stage] = template
	}
	memberTemplate, marshalTemplate, auditorTemplate := templates[tribunal.StageGenerate], templates[tribunal.StageMarshal], templates[tribunal.StageAudit]
	personaBindings, err := tribunalPersonaBindings(r.req.Binding.Personas)
	if err != nil {
		return AgentToolCall{}, err
	}
	result, err := r.executor.inference.DispatchIntent(ctx, &tribunalv1.TribunalIntentRequest{
		Request:                 record.ArgumentsJSON,
		Guidelines:              "The agent proposed this run_shell_command call. Preserve its goal using one executable and argv; shell pipelines, redirects, glob expansion, and substitutions are unavailable. Preserve literal utility arguments, including regex patterns and find -exec terminators.",
		WorkingDirectory:        workingDir,
		TimeoutSeconds:          constants.DefaultShellCommandTimeout,
		TargetOperatorSessionId: r.target.SessionID,
		InvestigationId:         r.trace.InvestigationID,
		TaskId:                  r.req.AttemptID,
		ExecutionId:             record.CallID,
		MemberInference:         memberTemplate,
		MarshalInference:        marshalTemplate,
		AuditorInference:        auditorTemplate,
		Personas:                personaBindings,
		ProviderAttemptPrefix:   prefix,
		Tier:                    r.designated,
		Homogeneous:             !IsHeterogeneousAssignment(r.req.Assignment),
		StepStartIndex:          uint32(len(r.trace.Steps)),
	})
	if err != nil {
		return AgentToolCall{}, fmt.Errorf("tool %s: %w", record.ToolName, err)
	}
	record.ConsensusInferences = governance.WrapConsensusInferences(result.GetConsensusInferences())
	if failure := r.recordMemberCalls(record, r.designated, result.GetMemberCalls()); failure != "" {
		if failure == modelCallGovernanceRejected {
			record.PolicyDecision = agentPolicyDecisionDeny
			r.trace.TerminalClassification = AgentTerminalClassificationPolicyDenial
			return r.fail(record, agentToolFailure{ErrorType: agentToolErrorSecurity, Error: "SECURITY VIOLATION: Tribunal inference was rejected by required governance"})
		}
		return r.fail(record, agentToolFailure{ErrorType: agentToolErrorExecution, Error: fmt.Sprintf(tribunalInferenceFailedMsg, failure)})
	}
	r.trace.Steps = append(r.trace.Steps, result.GetSteps()...)
	switch result.GetOutcome() {
	case tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED:
		r.marshalStrikeCount = 0
		return r.runApprovedShellCommand(record, result)
	case tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_BLOCKED:
		r.marshalStrikeCount++
		if r.marshalStrikeCount >= 2 {
			r.marshalStrikeCount = 0
			return r.fail(record, agentToolFailure{ErrorType: agentToolErrorPipeline, Error: tribunalMarshalConflict})
		}
		return r.fail(record, agentToolFailure{ErrorType: agentToolErrorPipeline, Error: fmt.Sprintf(tribunalMarshalBlocked, result.GetRiskRationale(), result.GetRiskFeedback())})
	case tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED:
		return r.fail(record, agentToolFailure{ErrorType: agentToolErrorExecution, Error: fmt.Sprintf(tribunalAuditFailedError, result.GetAuditFailure())})
	case tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_NO_CONSENSUS:
		return r.fail(record, agentToolFailure{ErrorType: agentToolErrorExecution, Error: tribunalNoConsensusError})
	default:
		// INFERENCE_FAILED always carries a failed member call, recorded above.
		return AgentToolCall{}, fmt.Errorf("%w: tribunal intent outcome %s", constants.ErrEvaluationDispatchFailed, result.GetOutcome())
	}
}

// runApprovedShellCommand records the governed answer to the Tribunal's approved
// command on record, exactly as a direct command's answer is recorded.
func (r *agentRun) runApprovedShellCommand(record AgentToolCall, result *tribunalv1.TribunalIntentResult) (AgentToolCall, error) {
	record.ApprovedArgumentsJSON = result.GetArgumentsJson()
	record.ActionType = string(constants.ActionTypeMcpCall)
	dispatched, err := classifyToolDispatch(constants.ActionTypeMcpCall, client.IntentCommandOutcome(result))
	if err != nil {
		return AgentToolCall{}, fmt.Errorf("tool %s: %w", record.ToolName, err)
	}
	denied, err := r.recordDispatch(&record, dispatched)
	if len(result.GetConsensusInferences()) > 0 {
		record.ConsensusInferences = governance.WrapConsensusInferences(result.GetConsensusInferences())
	}
	if err != nil {
		return record, err
	}
	if denied {
		if record.ArgumentsJSON != record.ApprovedArgumentsJSON {
			return r.fail(record, agentToolFailure{ErrorType: record.ErrorType, Error: record.Error + "\nApproved arguments: " + record.ApprovedArgumentsJSON})
		}
		return record, nil
	}
	if answerKind(dispatched.Outcome) != evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT {
		return r.fail(record, agentToolFailure{ErrorType: agentToolErrorExecution, Error: agentDispatchErrorText(constants.MCPToolRunShellCommand, dispatched)})
	}
	return r.completeNativeToolCall(record)
}

// recordMemberCalls records each Tribunal member inference on the trace, as
// infer records an agent turn: every call the Inference runtime executed is
// recorded, verified against the model it was dispatched under. It returns
// the trace's provider failure when a member call failed or did not verify;
// the deliberation stopped there.
func (r *agentRun) recordMemberCalls(record AgentToolCall, tier evalv1.ModelCampaignRole, calls []*tribunalv1.TribunalMemberCall) string {
	for _, member := range calls {
		callTier := tier
		if IsHeterogeneousAssignment(r.req.Assignment) {
			// The live trace is unsealed (no schema version yet) and will seal
			// as the current version, so it takes the per-stage slot.
			callTier = formationTribunalRoleForPurpose(agentModelCallPurposeTribunalPrefix + string(tribunal.StageFromProto(member.GetStage())))
		}
		call := AgentModelCall{
			Purpose:    agentModelCallPurposeTribunalPrefix + string(tribunal.StageFromProto(member.GetStage())),
			LoopTurn:   record.LoopTurn,
			ToolCallID: record.CallID,
			Persona:    member.GetPersona(),
		}
		probe, err := r.bindInference(&call, callTier, member.GetProviderAttemptId(), nil, nil)
		if err == nil {
			err = memberCallError(member)
		}
		if err == nil {
			err = recordInferenceResponse(&call, probe, &operatorv1.InferenceDispatchResponse{
				TransactionId:       member.GetTransactionId(),
				Result:              member.GetResult(),
				Receipt:             member.GetReceipt(),
				ObservationReceipts: member.GetObservationReceipts(),
			})
		}
		if err != nil {
			if len(call.ObservationReceipts) == 0 {
				recordObservationReceipts(&call, member.GetObservationReceipts())
			}
			if call.Receipt == nil && member.GetReceipt() != nil {
				call.TransactionID = member.GetReceipt().GetTransactionId()
				call.Receipt = &AgentTraceReceipt{EventReceipt: member.GetReceipt()}
			}
			if isL2GovernanceDenial(member.GetReceipt(), err) {
				call.Error = modelCallGovernanceRejected
				if call.Receipt != nil {
					r.trace.ModelCalls = append(r.trace.ModelCalls, call)
				}
				return modelCallGovernanceRejected
			}
			r.trace.ProviderFailure = string(ClassifyExecutionFailure(err))
			r.trace.ProviderFailureClass = string(ProviderFailureClass(err))
			call.Error = r.trace.ProviderFailure
			if call.Receipt != nil {
				r.trace.ModelCalls = append(r.trace.ModelCalls, call)
			}
			return r.trace.ProviderFailure
		}
		text, thinking, toolCalls := agentResponseParts(member.GetResult())
		call.Text, call.Thinking, call.ToolCallCount = text, thinking, len(toolCalls)
		r.trace.ModelCalls = append(r.trace.ModelCalls, call)
	}
	return ""
}

// isL2GovernanceDenial recognizes only the typed inference rejection
// represented by a signed failed INFERENCE receipt for a required L2 quorum
// failure, whether the inference was a Tribunal member call or a scored agent
// turn. Receipt authenticity is verified by canonical trace evidence
// verification before the outcome is accepted.
func isL2GovernanceDenial(receipt *operatorv1.EventReceipt, err error) bool {
	if !errors.Is(err, constants.ErrInferenceGovernanceRejected) {
		return false
	}
	return governance.IsGovernanceRejectionReceipt(receipt) &&
		receipt.GetActionType() == string(constants.ActionTypeInference) &&
		receipt.GetRejectionCode() == operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET &&
		receipt.GetL2Status() == operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED &&
		receipt.GetSignerKeyId() != "" && receipt.GetSignature() != ""
}

// memberCallError is the failure a member call carries, mapped to its public
// sentinel as a non-2xx inference dispatch answer is; nil for a completed call.
func memberCallError(member *tribunalv1.TribunalMemberCall) error {
	failure := member.GetFailure()
	if failure == nil {
		return nil
	}
	err := fmt.Errorf("%w: %s %s round %d: %s", constants.ErrTribunalInferenceFailed, member.GetStage(), member.GetPersona(), member.GetRound(), failure.GetCode())
	if sentinel := dispatch.FailureSentinel(failure.GetCode()); sentinel != nil {
		err = fmt.Errorf("%w: %w", err, sentinel)
	}
	return err
}
