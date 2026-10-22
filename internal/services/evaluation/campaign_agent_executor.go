// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	inferencedispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	// agentMaxLoopTurns bounds the scored tool loop; a model still calling
	// tools on the last turn is graded LOOP_EXHAUSTED.
	agentMaxLoopTurns = 8
	// agentMaxMessages stays under the Inference Operator's message bound with
	// room for one more turn's tool results.
	agentMaxMessages = 224
	// agentToolResultMaxBytes caps model-visible tool output so one large file
	// or command output cannot exceed the per-message text bound.
	agentToolResultMaxBytes = 64 * 1024
	agentCallSiteTurn       = "agent:turn"
	// agentCallSiteTribunalPrefix prefixes a Tribunal inference's call site:
	// tribunal:generate, tribunal:marshal, or tribunal:audit.
	agentCallSiteTribunalPrefix = "tribunal:"
)

// AgentToolDispatcher is the governed Data Operator surface the agent executor
// uses: fixture materialization and one typed action per model tool call.
// *CommandLane implements it.
type AgentToolDispatcher interface {
	WorkspaceFileWriter
	DispatchAgentTool(ctx context.Context, target Target, runID, investigationID, attemptID string, action constants.ActionType, targetResource string, payload []byte) (GovernedToolDispatch, error)
}

// CampaignAgentExecutor runs model-role and formation assignments through the
// governed Inference Operator; every tool call runs as a governed action on
// the bound Data Operator.
type CampaignAgentExecutor struct {
	inference  AgentInferenceDispatcher
	tools      AgentToolDispatcher
	sentinel   cases.Sentinel
	traceStore AgentTraceStore
	catalog    *agentToolCatalog
	now        func() time.Time
	newID      func(string) string
}

// NewCampaignAgentExecutor constructs the homogeneous agent executor. The
// sentinel reads the ledger on the Data Operator before and after each attempt.
func NewCampaignAgentExecutor(inference AgentInferenceDispatcher, tools AgentToolDispatcher, sentinel cases.Sentinel, traceStore AgentTraceStore, now func() time.Time, newID func(string) string) (*CampaignAgentExecutor, error) {
	if inference == nil || tools == nil || sentinel == nil || traceStore == nil || newID == nil {
		return nil, fmt.Errorf("evaluation: agent executor: %w", constants.ErrMissingRequiredField)
	}
	catalog, err := newAgentToolCatalog()
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &CampaignAgentExecutor{inference: inference, tools: tools, sentinel: sentinel, traceStore: traceStore, catalog: catalog, now: now, newID: newID}, nil
}

// agentRun is the per-assignment state of one tool loop.
type agentRun struct {
	executor   *CampaignAgentExecutor
	req        AssignmentExecutionRequest
	designated evalv1.ModelCampaignRole
	variants   map[evalv1.ModelCampaignRole]*evalv1.ModelVariant
	variant    *evalv1.ModelVariant
	registry   []*operatorv1.InferenceModelVariant
	workspace  ScenarioWorkspace
	// held is the previous repetition's conversation when the campaign holds
	// context; the model receives it ahead of this repetition's input.
	held []*operatorv1.InferenceMessage
	// tools is the scenario's declared tool surface for every turn.
	tools              agentToolScope
	target             Target
	trace              *AgentTrace
	marshalStrikeCount int
}

// ExecuteAssignment runs one homogeneous assignment to a terminal result. A
// provider failure is recorded in the trace and graded; a harness failure
// (workspace materialization, ledger sentinel reads, Gateway 5xx, trace
// persistence) is returned as an assignment execution error and never graded
// against the model.
func (e *CampaignAgentExecutor) ExecuteAssignment(ctx context.Context, req AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	run, err := e.newRun(req)
	if err != nil {
		return nil, err
	}
	assignment := req.Assignment
	sentinelPath := run.workspace.SentinelPath()
	if err := e.tools.WriteWorkspaceFile(ctx, run.target, assignment.GetRunId(), assignment.GetScenarioId(), req.AttemptID, sentinelPath, req.AttemptID); err != nil {
		return nil, assignmentExecutionError("evaluation: execute homogeneous assignment: write ledger sentinel file", err)
	}
	if req.HeldContext == nil {
		if err := materializeScenarioWorkspace(ctx, e.tools, run.target, assignment.GetRunId(), assignment.GetScenarioId(), req.AttemptID, run.workspace, req.ScenarioInput.WorkspaceFiles); err != nil {
			return nil, assignmentExecutionError("evaluation: execute homogeneous assignment: materialize workspace", err)
		}
	}
	before, err := e.sentinel.Read(ctx, cases.ExecutorOutbound, sentinelPath)
	if err != nil {
		return nil, assignmentExecutionError("evaluation: execute homogeneous assignment: read opening ledger sentinel", err)
	}
	if err := run.loop(ctx); err != nil {
		return nil, assignmentExecutionError("evaluation: execute homogeneous assignment", err)
	}
	after, err := e.sentinel.Read(ctx, cases.ExecutorOutbound, sentinelPath)
	if err != nil {
		return nil, assignmentExecutionError("evaluation: execute homogeneous assignment: read closing ledger sentinel", err)
	}
	run.trace.Bracket = &AgentTraceBracket{Before: AgentTraceEventReceipt{EventReceipt: before}, After: AgentTraceEventReceipt{EventReceipt: after}}
	persistCtx := assignmentPersistContext(ctx)
	body, err := SealAgentTrace(run.trace)
	if err != nil {
		return nil, assignmentExecutionError("evaluation: execute homogeneous assignment: seal trace", err)
	}
	if err := e.traceStore.SaveAssignmentAgentTrace(persistCtx, assignment.GetRunId(), assignment.GetAssignmentId(), body); err != nil {
		return nil, assignmentExecutionError("evaluation: execute homogeneous assignment: persist trace", err)
	}
	now := e.now().UTC()
	ref, err := BuildAssignmentAgentTraceEvidenceReference(assignment.GetRunId(), req.AttemptID, body, now)
	if err != nil {
		return nil, assignmentExecutionError("evaluation: execute homogeneous assignment: trace evidence", err)
	}
	result, err := ImportAssignmentResultFromAgentTrace(req, run.trace, ref, now, e.newID)
	if err != nil {
		return nil, assignmentExecutionError("evaluation: import homogeneous assignment result", err)
	}
	// A trace the Data Operator's evidence does not back, and a trace whose
	// provider produced no answer, are never judged and never promoted: their
	// imported lifecycle and grades stand as they are.
	switch result.GetLifecycleStatus() {
	case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED:
		return result, nil
	}
	if semanticGrades, judgeScores := e.scoreWithJudge(ctx, req, run.trace); len(semanticGrades) > 0 {
		result.SemanticGrades = semanticGrades
		result.DecomposedScores = append(result.DecomposedScores, judgeScores...)
		for _, sg := range semanticGrades {
			if sg.GraderCallRef != nil {
				result.GraderCalls = append(result.GraderCalls, &evalv1.GraderModelCallRecord{
					GraderCallId:       e.newID("grader-call"),
					AssignmentId:       assignment.GetAssignmentId(),
					JudgeVariantId:     sg.JudgeVariantId,
					InferenceRecordRef: sg.GraderCallRef,
				})
				result.GovernedActions = append(result.GovernedActions, &evalv1.GovernedActionBinding{
					BindingId:         e.newID("judge-binding"),
					AssignmentId:      assignment.GetAssignmentId(),
					TransactionId:     strings.TrimPrefix(sg.GraderCallRef.ArtifactId, "event_receipt:"),
					OperatorId:        run.trace.Inference.OperatorID,
					OperatorSessionId: run.trace.Inference.SessionID,
					EventReceiptRef:   sg.GraderCallRef,
					PolicyDecision:    agentPolicyDecisionAllow,
					ActionType:        string(constants.ActionTypeInference),
				})
			}
		}
		digest, err := ComputeAssignmentResultDigest(result)
		if err != nil {
			return nil, assignmentExecutionError("evaluation: recompute assignment result digest", err)
		}
		result.ResultDigest = digest
	}
	return result, nil
}

func (e *CampaignAgentExecutor) shouldScoreWithJudge(req AssignmentExecutionRequest) bool {
	if req.GradingMethod == evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_SEMANTIC_JUDGE {
		return true
	}
	if req.ScenarioGold.ContentCheck != nil && req.ScenarioGold.ContentCheck.DiagnosisRubric != "" {
		return true
	}
	return false
}

func (e *CampaignAgentExecutor) scoreWithJudge(ctx context.Context, req AssignmentExecutionRequest, trace *AgentTrace) ([]*evalv1.SemanticGrade, []*evalv1.DecomposedScoreRecord) {
	if !e.shouldScoreWithJudge(req) || e.inference == nil || req.Assignment == nil {
		return nil, nil
	}
	resolved, err := ResolveAssignmentModels(req)
	if err != nil {
		return nil, nil
	}
	role := evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
	if homogeneous := req.Assignment.GetHomogeneous(); homogeneous != nil {
		role = homogeneous.GetDesignatedRole()
	}
	variant := resolved[role]
	if variant == nil {
		return nil, nil
	}
	if variant.GetServedModelTag() == "" || variant.GetModelDigest() == "" {
		return nil, nil
	}
	cfg := judge.Config{
		InferenceSessionID:  req.Binding.Topology.Inference.OperatorSessionID,
		Variant:             variant,
		CampaignID:          req.Assignment.GetCampaignId(),
		ModelRegistryDigest: req.Binding.ModelRegistryDigest,
		ModelRegistry:       req.Binding.ModelRegistry,
		RunID:               req.Assignment.GetRunId(),
		AssignmentID:        req.Assignment.GetAssignmentId(),
		ScenarioID:          req.Assignment.GetScenarioId(),
		NewID:               e.newID,
	}
	j, err := judge.New(e.inference, cfg)
	if err != nil {
		return []*evalv1.SemanticGrade{{
			GradeId:        req.Assignment.GetAssignmentId() + ":diagnosis-quality",
			CriterionId:    "diagnosis-quality",
			Status:         evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
			JudgeVariantId: variant.GetVariantId(),
			Detail:         fmt.Sprintf("judge construction failed: %v", err),
		}}, nil
	}
	objective := req.ScenarioGold.ExpectedBehavior
	if objective == "" {
		objective = req.ScenarioInput.UserPrompt
	}
	var memoryFacts []string
	if req.ScenarioGold.Players != nil && req.ScenarioGold.Players.Codex != nil {
		if req.ScenarioGold.Players.Codex.ExactToken != "" {
			memoryFacts = append(memoryFacts, req.ScenarioGold.Players.Codex.ExactToken)
		}
		for _, termGroup := range req.ScenarioGold.Players.Codex.RequiredTerms {
			if len(termGroup) > 0 && termGroup[0] != "" {
				memoryFacts = append(memoryFacts, termGroup[0])
			}
		}
	}
	protoTrace := trace.ToProto()
	scores := j.ScoreTrace(ctx, protoTrace, objective, memoryFacts)
	if len(scores) == 0 {
		return nil, nil
	}
	// Judge event receipts verify against the same pinned anchors as the traced model
	// calls, so the anchor set is built once for the whole loop.
	anchors, anchorsErr := agentTraceAnchorSet(trace)
	now := e.now().UTC()
	var (
		semanticGrades   []*evalv1.SemanticGrade
		decomposedScores []*evalv1.DecomposedScoreRecord
	)
	for _, sc := range scores {
		criterionID := "diagnosis-quality"
		dimension := "diagnosis_quality"
		if sc.Subject == judge.SubjectMemory {
			criterionID = "memory-quality"
			dimension = "memory_quality"
		}
		gradeID := fmt.Sprintf("%s:%s", req.Assignment.GetAssignmentId(), criterionID)
		var graderCallRef *compliancev1.ComplianceEvidenceReference
		var eventReceiptErr error
		if sc.Status == judge.StatusScored && (sc.TransactionID == "" || sc.EventReceipt == nil) {
			// A scored judge whose response carries no transaction or no signed
			// event receipt has nothing to verify, so its grade cannot be attributed to
			// a governed inference.
			eventReceiptErr = fmt.Errorf("judge score has no signed governed event_receipt: %w", constants.ErrMissingRequiredField)
		} else if sc.TransactionID != "" {
			eventReceiptErr = anchorsErr
			if eventReceiptErr == nil {
				eventReceiptErr = verifyJudgeEventReceipt(anchors, trace.Inference, sc)
			}
			if eventReceiptErr == nil {
				graderCallRef = &compliancev1.ComplianceEvidenceReference{
					ArtifactId:         "event_receipt:" + sc.TransactionID,
					ArtifactType:       "event_receipt",
					ProducerIdentity:   "ztd-eval-judge",
					ProducedAt:         timestamppb.New(now),
					ScopeId:            req.Assignment.GetCampaignId(),
					RunId:              req.Assignment.GetRunId(),
					ScenarioId:         req.Assignment.GetScenarioId(),
					VerificationStatus: "verified",
					VerifierId:         GraderID,
					VerifierVersion:    GraderVersion,
					VerifiedAt:         timestamppb.New(now),
				}
			}
		}
		status := evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
		var detail string
		if eventReceiptErr != nil {
			// An unverifiable judge event receipt fails only this semantic grade. Like a
			// judge failure, it records no decomposed score and never overrides the
			// deterministic verdicts.
			status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
			detail = fmt.Sprintf("judge event receipt verification failed: %v", eventReceiptErr)
		} else if sc.Status == judge.StatusFailed {
			status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
			detail = fmt.Sprintf("judge failed: %s", sc.Detail)
		} else {
			if sc.Value < 3 {
				status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
			}
			rubric := sc.RubricVersion
			if rubric == "" {
				rubric = judge.RubricVersion
			}
			detail = fmt.Sprintf("score: %d/5; rubric: %s; rationale: %s", sc.Value, rubric, sc.Rationale)
			decomposedScores = append(decomposedScores, &evalv1.DecomposedScoreRecord{
				ScoreId:           fmt.Sprintf("%s:%s", req.Assignment.GetAssignmentId(), dimension),
				Dimension:         dimension,
				Value:             float64(sc.Value),
				Unit:              evalv1.EvaluationMetricUnit_EVALUATION_METRIC_UNIT_COUNT,
				Direction:         evalv1.EvaluationMetricDirection_EVALUATION_METRIC_DIRECTION_HIGHER_IS_BETTER,
				MissingDataPolicy: evalv1.EvaluationMissingDataPolicy_EVALUATION_MISSING_DATA_POLICY_EXCLUDE,
			})
		}
		semanticGrades = append(semanticGrades, &evalv1.SemanticGrade{
			GradeId:        gradeID,
			CriterionId:    criterionID,
			Status:         status,
			JudgeVariantId: variant.GetVariantId(),
			GraderCallRef:  graderCallRef,
			Detail:         detail,
		})
	}
	return semanticGrades, decomposedScores
}

func (e *CampaignAgentExecutor) newRun(req AssignmentExecutionRequest) (*agentRun, error) {
	if e == nil {
		return nil, fmt.Errorf("evaluation: execute homogeneous assignment: %w", constants.ErrMissingRequiredField)
	}
	assignment := req.Assignment
	resolved, err := ResolveAssignmentModels(req)
	if err != nil {
		return nil, err
	}
	designated := evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
	if homogeneous, ok := assignment.GetTarget().(*evalv1.EvaluationAssignment_Homogeneous); ok {
		designated = homogeneous.Homogeneous.GetDesignatedRole()
	}
	variant := resolved[designated]
	if variant == nil {
		return nil, fmt.Errorf("evaluation: execute assignment: designated model: %w", constants.ErrFormationRegistryBinding)
	}
	roleLabel, err := modelCampaignRoleLabel(designated)
	if err != nil {
		return nil, err
	}
	binding := req.Binding
	if variant.GetServedModelTag() == "" || variant.GetModelDigest() == "" || req.AttemptID == "" ||
		binding.ModelRegistryDigest == "" || len(binding.ModelRegistry) == 0 {
		return nil, fmt.Errorf("evaluation: execute homogeneous assignment: candidate variant, attempt, and execution binding: %w", constants.ErrMissingRequiredField)
	}
	topology := binding.Topology
	if err := topology.ValidateAnchored(); err != nil {
		return nil, fmt.Errorf("evaluation: execute homogeneous assignment: %w", err)
	}
	investigationID, err := e.investigationID(req)
	if err != nil {
		return nil, err
	}
	var (
		workspace   ScenarioWorkspace
		heldContext *AgentTraceHeldContext
		held        []*operatorv1.InferenceMessage
	)
	if req.HeldContext != nil {
		if workspace, err = heldScenarioWorkspace(req.HeldContext, topology); err != nil {
			return nil, err
		}
		if heldContext, held, err = newAgentTraceHeldContext(req.HeldContext, assignment.GetRepetition()); err != nil {
			return nil, err
		}
	} else if workspace, err = NewScenarioWorkspace(topology.DataWorkingDirectory, assignment.GetRunId(), req.AttemptID); err != nil {
		return nil, err
	}
	// A rejected scenario contract fails the assignment, not the run: nothing
	// has acted yet, and later assignments may use other scenarios.
	tools, err := e.catalog.Scope(req.ScenarioTools)
	if err != nil {
		return nil, assignmentExecutionError("evaluation: execute homogeneous assignment: scope tools", err)
	}
	return &agentRun{
		executor:   e,
		req:        req,
		designated: designated,
		variants:   resolved,
		variant:    variant,
		registry:   binding.ModelRegistry,
		workspace:  workspace,
		held:       held,
		tools:      tools,
		target:     Target{OperatorID: topology.Data.OperatorID, SessionID: topology.Data.OperatorSessionID},
		trace: &AgentTrace{
			InvestigationID:     investigationID,
			HeldContext:         heldContext,
			RunID:               assignment.GetRunId(),
			AssignmentID:        assignment.GetAssignmentId(),
			EvaluationAttemptID: req.AttemptID,
			CampaignID:          assignment.GetCampaignId(),
			ScenarioID:          assignment.GetScenarioId(),
			DesignatedRole:      roleLabel,
			ServedModelTag:      variant.GetServedModelTag(),
			ModelDigest:         variant.GetModelDigest(),
			ModelRegistryDigest: binding.ModelRegistryDigest,
			Inference:           agentTraceRuntime(topology.Inference),
			Data:                agentTraceRuntime(topology.Data),
			Workspace:           AgentTraceWorkspace(workspace),
			ModelCalls:          []AgentModelCall{},
			ToolCalls:           []AgentToolCall{},
		},
	}, nil
}

// loop runs the scored tool loop until the model answers without a tool call,
// the provider fails, or the turn bound is reached.
func (r *agentRun) loop(ctx context.Context) error {
	opening := r.conversation()
	tools := r.tools.Declarations()
	input, err := newAgentTraceInput(opening, tools)
	if err != nil {
		return err
	}
	r.trace.Input = input
	messages := append(append(make([]*operatorv1.InferenceMessage, 0, len(r.held)+len(opening)), r.held...), opening...)
	for turn := 1; turn <= agentMaxLoopTurns; turn++ {
		result, call := r.infer(ctx, AgentModelCall{Purpose: agentModelCallPurposeTurn, LoopTurn: turn}, messages, tools)
		if call == nil {
			// A turn infer already classified (a signed L2 denial) keeps it.
			if r.trace.TerminalClassification == "" {
				r.trace.TerminalClassification = AgentTerminalClassificationHarnessFailure
			}
			return nil
		}
		text, thinking, toolCalls := agentResponseParts(result)
		call.Text = text
		call.Thinking = thinking
		call.ToolCallCount = len(toolCalls)
		modelCallID := call.ProviderAttemptID

		questions, isInterrogation := parseInterrogationQuestions(text)
		if isInterrogation {
			// Interrogation hand-off drops pending execution, while the trace
			// preserves the tool-call count covered by the signed result.
			var matchedFollowUp *ScenarioScriptedFollowUp
			for i := range r.req.ScenarioInput.FollowUps {
				fu := &r.req.ScenarioInput.FollowUps[i]
				if matchesFollowUp(text, questions, fu) {
					matchedFollowUp = fu
					break
				}
			}

			if matchedFollowUp != nil {
				r.trace.Clarifications = append(r.trace.Clarifications, AgentClarificationTurn{
					LoopTurn:       turn,
					Questions:      questions,
					SelectedBranch: matchedFollowUp.ID,
					UserResponse:   matchedFollowUp.ResponseText,
				})
				assistant := &operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT}
				if text != "" {
					assistant.Parts = append(assistant.Parts, &operatorv1.InferenceMessagePart{Part: &operatorv1.InferenceMessagePart_Text{Text: text}})
				}
				userMsg := &operatorv1.InferenceMessage{
					Role:  operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
					Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: matchedFollowUp.ResponseText}}},
				}
				messages = append(messages, assistant, userMsg)
				if len(messages) > agentMaxMessages {
					break
				}
				continue
			}

			// Unmatched clarification ends the episode as unsupported_clarification.
			r.trace.Clarifications = append(r.trace.Clarifications, AgentClarificationTurn{
				LoopTurn:       turn,
				Questions:      questions,
				SelectedBranch: "",
			})
			r.trace.FinalOutput = text
			r.trace.TerminalClassification = AgentTerminalClassificationUnsupportedClarification
			return nil
		}

		if len(toolCalls) == 0 {
			r.trace.FinalOutput = text
			r.trace.TerminalClassification = classifyTerminalState(r.trace)
			return nil
		}
		assistant := &operatorv1.InferenceMessage{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT}
		if text != "" {
			assistant.Parts = append(assistant.Parts, &operatorv1.InferenceMessagePart{Part: &operatorv1.InferenceMessagePart_Text{Text: text}})
		}
		results := make([]*operatorv1.InferenceMessage, 0, len(toolCalls))
		for _, toolCall := range toolCalls {
			executed, err := r.runTool(ctx, turn, modelCallID, toolCall)
			if err != nil {
				return err
			}
			r.trace.ToolCalls = append(r.trace.ToolCalls, executed)
			if r.trace.TerminalClassification == AgentTerminalClassificationPolicyDenial {
				return nil
			}
			if r.trace.ProviderFailure != "" {
				// A Tribunal inference inside the tool call failed; like a
				// failed turn, it ends the loop.
				r.trace.TerminalClassification = AgentTerminalClassificationHarnessFailure
				return nil
			}
			assistant.Parts = append(assistant.Parts, &operatorv1.InferenceMessagePart{Part: &operatorv1.InferenceMessagePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
				CallId: toolCall.GetCallId(), Name: toolCall.GetName(), ArgumentsJson: executed.ArgumentsJSON,
			}}})
			results = append(results, &operatorv1.InferenceMessage{
				Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_TOOL,
				Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_ToolResult{ToolResult: &operatorv1.InferenceToolResult{
					CallId: toolCall.GetCallId(), Name: toolCall.GetName(), ResultJson: executed.ResultJSON,
				}}}},
			})
		}
		messages = append(messages, assistant)
		messages = append(messages, results...)
		if len(messages) > agentMaxMessages {
			break
		}
	}
	r.trace.ToolTurnLimitReached = true
	r.trace.TerminalClassification = AgentTerminalClassificationBudgetExhaustion
	return nil
}

var (
	interrogationBlockPattern = regexp.MustCompile(`(?si)<interrogation>\s*(.*?)(?:</interrogation>|$)`)
	interrogationLinePattern  = regexp.MustCompile(`^\d+[.)]\s*(\S.*?)$`)
)

func parseInterrogationQuestions(text string) ([]string, bool) {
	match := interrogationBlockPattern.FindStringSubmatch(text)
	if match == nil {
		return nil, false
	}
	content := strings.TrimSpace(match[1])
	if content == "" {
		return nil, false
	}
	var questions []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if qMatch := interrogationLinePattern.FindStringSubmatch(line); qMatch != nil {
			q := strings.TrimSpace(qMatch[1])
			if q != "" {
				questions = append(questions, q)
			}
		} else if strings.HasSuffix(line, "?") {
			questions = append(questions, line)
		}
	}
	if len(questions) == 0 || len(questions) > 3 {
		return nil, false
	}
	for _, q := range questions {
		words := strings.Fields(strings.ToLower(q))
		if len(words) == 0 {
			return nil, false
		}
		firstWord := strings.Trim(words[0], "\"'`*_?,.:;")
		if !slices.Contains(binaryOpeners, firstWord) || slices.Contains(words, "or") {
			return nil, false
		}
	}
	return questions, true
}

func matchesFollowUp(text string, questions []string, fu *ScenarioScriptedFollowUp) bool {
	if fu == nil {
		return false
	}
	if len(fu.TriggerTerms) == 0 {
		return true
	}
	combined := strings.ToLower(text) + " " + strings.ToLower(strings.Join(questions, " "))
	for _, termGroup := range fu.TriggerTerms {
		groupMatched := false
		for _, term := range termGroup {
			if strings.Contains(combined, strings.ToLower(term)) {
				groupMatched = true
				break
			}
		}
		if !groupMatched {
			return false
		}
	}
	return true
}

func classifyTerminalState(trace *AgentTrace) string {
	if trace.ProviderFailure != "" {
		return AgentTerminalClassificationHarnessFailure
	}
	if trace.ToolTurnLimitReached {
		return AgentTerminalClassificationBudgetExhaustion
	}
	if trace.TerminalClassification != "" {
		return trace.TerminalClassification
	}
	hasDenial := false
	hasPipelineOutcome := false
	for _, tc := range trace.ToolCalls {
		if tc.IsDenied() {
			hasDenial = true
		}
		if tc.ErrorType == agentToolErrorPipeline {
			hasPipelineOutcome = true
		}
	}
	textLower := strings.ToLower(trace.FinalOutput)
	isRefusal := false
	for _, term := range refusalTerms {
		if strings.Contains(textLower, term) {
			isRefusal = true
			break
		}
	}
	if hasDenial && isRefusal {
		return AgentTerminalClassificationPolicyDenial
	}
	if isRefusal {
		return AgentTerminalClassificationEscalationOrRefusal
	}
	if hasDenial {
		return AgentTerminalClassificationPolicyDenial
	}
	if hasPipelineOutcome {
		return AgentTerminalClassificationPipelineOutcome
	}
	return AgentTerminalClassificationFinalDiagnosis
}

// conversation builds the scored turn's ordered messages: the system context,
// the frozen seed conversation, and the rendered scenario prompt. A
// held-context repetition already carries the system context and seed in its
// held conversation, so it opens with the scenario prompt alone.
func (r *agentRun) conversation() []*operatorv1.InferenceMessage {
	input := r.req.ScenarioInput
	var messages []*operatorv1.InferenceMessage
	if len(r.held) == 0 {
		messages = append(messages, agentTextMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, r.systemPrompt()))
		for _, turn := range input.Seed.Turns {
			role := operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT
			if turn.Sender == "user" {
				role = operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER
			}
			if content := r.workspace.Render(turn.Content); content != "" {
				messages = append(messages, agentTextMessage(role, content))
			}
		}
	}
	var prompt strings.Builder
	prompt.WriteString(r.workspace.Render(input.UserPrompt))
	for _, inline := range input.InlineContext {
		fmt.Fprintf(&prompt, "\n\n%s (%s):\n%s", inline.Label, inline.Kind, r.workspace.Render(inline.Content))
	}
	return append(messages, agentTextMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, prompt.String()))
}

func (r *agentRun) systemPrompt() string {
	seed := r.req.ScenarioInput.Seed
	var b strings.Builder
	if len(r.tools.Declarations()) == 0 {
		b.WriteString("You are an operations agent working an investigation. ")
		b.WriteString("No tools are available for this request: answer the user directly from the conversation, and never invent tool output.\n\n")
	} else {
		b.WriteString("You are an operations agent working an investigation through governed tools. ")
		b.WriteString("Every tool call is screened by policy before it runs on the operator host; a denied call is final and must not be worked around. ")
		b.WriteString("Use tools to observe facts instead of guessing, and never invent tool output. When you have the answer, reply to the user without calling a tool.\n\n")
	}
	fmt.Fprintf(&b, "Operator ID: %s\nWorking Directory: %s\n", r.target.OperatorID, r.workspace.OperatorWorkingDirectory)
	if seed.CaseTitle != "" {
		fmt.Fprintf(&b, "Case: %s\n", seed.CaseTitle)
	}
	if seed.CaseDescription != "" {
		fmt.Fprintf(&b, "Case description: %s\n", r.workspace.Render(seed.CaseDescription))
	}

	return b.String()
}

func agentTextMessage(role operatorv1.InferenceMessageRole, text string) *operatorv1.InferenceMessage {
	return &operatorv1.InferenceMessage{Role: role, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: text}}}}
}

// agentResponseParts splits one response into the answer text sent back to
// the model, the provider's thinking text, and the tool calls.
func agentResponseParts(result *operatorv1.InferenceResult) (string, string, []*operatorv1.InferenceToolCall) {
	var (
		calls    []*operatorv1.InferenceToolCall
		thinking strings.Builder
	)
	for _, part := range result.GetParts() {
		if call := part.GetToolCall(); call != nil {
			calls = append(calls, call)
		}
		thinking.WriteString(part.GetThinking())
	}
	return strings.TrimSpace(inferenceResultText(result)), thinking.String(), calls
}

// infer dispatches one governed inference for the designated model and records
// it on the trace whenever the Inference runtime executed it, so its ledger
// commitment is always claimed. A provider failure is a scored outcome, not an
// execution error: it is recorded on the trace and reported as a nil call so
// the caller stops the turn. call names the inference (purpose, loop turn, and
// for a Tribunal call its tool call and persona); dispatch fills the rest.
func (r *agentRun) infer(ctx context.Context, call AgentModelCall, messages []*operatorv1.InferenceMessage, tools []*operatorv1.InferenceToolDeclaration) (*operatorv1.InferenceResult, *AgentModelCall) {
	result, call, err := r.dispatchInference(ctx, call, messages, tools)
	if err != nil && call.EventReceipt != nil && isL2GovernanceDenial(call.EventReceipt.EventReceipt, err) {
		// Required L2 refused the turn and the Inference runtime signed that
		// rejection: a governance outcome, not a provider failure.
		call.Error = modelCallGovernanceRejected
		r.trace.ModelCalls = append(r.trace.ModelCalls, call)
		r.trace.TerminalClassification = AgentTerminalClassificationPolicyDenial
		return nil, nil
	}
	if err != nil {
		r.trace.ProviderFailure = string(ClassifyExecutionFailure(err))
		r.trace.ProviderFailureClass = string(ProviderFailureClass(err))
		call.Error = r.trace.ProviderFailure
	}
	if err == nil || call.EventReceipt != nil {
		r.trace.ModelCalls = append(r.trace.ModelCalls, call)
	}
	if err != nil {
		return nil, nil
	}
	return result, &r.trace.ModelCalls[len(r.trace.ModelCalls)-1]
}

// dispatchInference dispatches one governed inference for the designated model
// and records its telemetry. The returned call carries the Inference runtime's
// signed event receipt whenever the runtime executed the dispatch, including when err
// reports its failure.
func (r *agentRun) dispatchInference(ctx context.Context, call AgentModelCall, messages []*operatorv1.InferenceMessage, tools []*operatorv1.InferenceToolDeclaration) (*operatorv1.InferenceResult, AgentModelCall, error) {
	purpose, turn := call.Purpose, call.LoopTurn
	modelRole := r.designated
	if IsHeterogeneousAssignment(r.req.Assignment) {
		modelRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
	}
	probe, err := r.bindInference(&call, modelRole, r.executor.newID("agent-inference"), messages, tools)
	if err != nil {
		return nil, call, err
	}
	dispatch, err := r.inferenceDispatchRequest(probe)
	if err != nil {
		return nil, call, err
	}
	response, err := r.executor.inference.DispatchInference(ctx, dispatch)
	if err != nil {
		if eventReceipt := governance.FailedEventReceipt(err); eventReceipt != nil {
			call.TransactionID = eventReceipt.GetTransactionId()
			call.EventReceipt = &AgentTraceEventReceipt{EventReceipt: eventReceipt}
		}
		recordObservationEventReceipts(&call, inferencedispatch.ObservationEventReceipts(err))
		return nil, call, fmt.Errorf("%s inference turn %d: %w", purpose, turn, err)
	}
	if err := recordInferenceResponse(&call, probe, response); err != nil {
		return nil, call, fmt.Errorf("%s inference turn %d: %w", purpose, turn, err)
	}
	return response.GetResult(), call, nil
}

// bindInference binds call to the variant modelRole dispatches under and
// returns the probe request its response is verified against.
func (r *agentRun) bindInference(call *AgentModelCall, modelRole evalv1.ModelCampaignRole, attemptID string, messages []*operatorv1.InferenceMessage, tools []*operatorv1.InferenceToolDeclaration) (InferenceProbeRequest, error) {
	variant := r.variants[modelRole]
	inferenceRole, err := tribunal.InferenceRoleForTier(modelRole)
	if err != nil || variant == nil {
		return InferenceProbeRequest{}, fmt.Errorf("evaluation: dispatch agent inference: role %s: %w", modelRole, constants.ErrFormationRegistryBinding)
	}
	call.ProviderAttemptID = attemptID
	call.ModelVariantID = variant.GetVariantId()
	call.ModelRole = inferenceRole.ToProto().String()
	call.ServedModelTag = variant.GetServedModelTag()
	call.ModelDigest = variant.GetModelDigest()
	call.ToolsDeclared = []string{}
	for _, tool := range tools {
		call.ToolsDeclared = append(call.ToolsDeclared, tool.GetName())
	}
	probe := InferenceProbeRequest{
		ProviderAttemptID:       attemptID,
		Role:                    inferenceRole,
		Model:                   variant.GetServedModelTag(),
		ModelDigest:             variant.GetModelDigest(),
		TargetOperatorSessionID: r.trace.Inference.SessionID,
		Messages:                messages,
		Tools:                   tools,
		CampaignID:              r.trace.CampaignID,
		ModelRegistryDigest:     r.trace.ModelRegistryDigest,
		ModelRegistry:           r.registry,
	}
	if len(tools) > 0 {
		probe.ToolChoice = &operatorv1.InferenceToolChoice{Mode: operatorv1.InferenceToolChoiceMode_INFERENCE_TOOL_CHOICE_MODE_AUTO}
	}
	return probe, nil
}

// inferenceDispatchRequest renders probe as a dispatch scoped to this
// assignment's evaluation attempt.
func (r *agentRun) inferenceDispatchRequest(probe InferenceProbeRequest) (*operatorv1.InferenceDispatchRequest, error) {
	dispatch, err := BuildInferenceProbeDispatchRequest(probe)
	if err != nil {
		return nil, err
	}
	dispatch.RunId = r.trace.RunID
	dispatch.AssignmentId = r.trace.AssignmentID
	dispatch.EvaluationAttemptId = r.trace.EvaluationAttemptID
	dispatch.ScenarioId = r.trace.ScenarioID
	return dispatch, nil
}

// recordObservationEventReceipts records the observers' signed event receipts a dispatch
// returned, on success or failure, so the bracket claims their commitments.
func recordObservationEventReceipts(call *AgentModelCall, eventReceipts []*operatorv1.EventReceipt) {
	for _, eventReceipt := range eventReceipts {
		call.ObservationEventReceipts = append(call.ObservationEventReceipts, AgentTraceEventReceipt{EventReceipt: eventReceipt})
	}
}

// recordInferenceResponse verifies a governed inference response against probe
// and records its event receipt and telemetry on call. The event receipt is recorded before
// verification so a response that fails it still claims its ledger commitment.
func recordInferenceResponse(call *AgentModelCall, probe InferenceProbeRequest, response *operatorv1.InferenceDispatchResponse) error {
	result := response.GetResult()
	call.TransactionID = response.GetTransactionId()
	if eventReceipt := response.GetEventReceipt(); eventReceipt != nil {
		call.EventReceipt = &AgentTraceEventReceipt{EventReceipt: eventReceipt}
	}
	recordObservationEventReceipts(call, response.GetObservationEventReceipts())
	call.ResultDigest = result.GetResultDigest()
	if err := ValidateInferenceProbeResponse(probe, response); err != nil {
		return err
	}
	if result.GetPromptTokens() < 0 || result.GetCompletionTokens() < 0 {
		return fmt.Errorf("negative token usage: %w", constants.ErrInferenceProviderResponseInvalid)
	}
	call.UsageReported = result.GetUsageReported()
	call.PromptTokens = uint32(result.GetPromptTokens())
	call.CompletionTokens = uint32(result.GetCompletionTokens())
	call.FinishReason = result.GetFinishReason()
	call.LoadState = operatorLoadStateToEvaluation(result.GetLoadState()).String()
	call.RetryCount = result.GetRetryCount()
	call.InferenceResult = &AgentTraceInferenceResult{InferenceResult: proto.Clone(result).(*operatorv1.InferenceResult)}
	if result.TimeToFirstTokenNs != nil && *result.TimeToFirstTokenNs > 0 {
		call.TTFTNanos = uint64(*result.TimeToFirstTokenNs)
	}
	if result.GenerationDurationNs != nil && *result.GenerationDurationNs > 0 {
		call.GenerationDurationNanos = uint64(*result.GenerationDurationNs)
	}
	return nil
}

// runTool executes one model tool call and returns its trace record. A
// malformed call, a governance denial, or a failed action is a model-visible
// result; only a harness failure returns an error.
func (r *agentRun) runTool(ctx context.Context, turn int, modelCallID string, toolCall *operatorv1.InferenceToolCall) (AgentToolCall, error) {
	record := AgentToolCall{
		CallID:         r.executor.newID("tool-call"),
		ModelCallID:    modelCallID,
		ProviderCallID: toolCall.GetCallId(),
		ToolName:       toolCall.GetName(),
		LoopTurn:       turn,
	}
	arguments, err := canonicalizeToolJSON(toolCall.GetArgumentsJson())
	if err != nil {
		arguments = "{}"
	}
	record.ArgumentsJSON = arguments
	spec, ok := r.tools.Spec(toolCall.GetName())
	if !ok {
		hint := "no tools are available; answer directly"
		if names := r.tools.DeclaredNames(); len(names) > 0 {
			hint = "use one of: " + strings.Join(names, ", ")
		}
		return r.fail(record, agentToolFailure{ErrorType: agentToolErrorExecution, Error: fmt.Sprintf("ZTD-1607: Unknown tool %q; %s", toolCall.GetName(), hint)})
	}
	record.IsOperatorTool = spec.Operator
	args, failure := parseAgentToolArguments(spec, toolCall.GetArgumentsJson())
	if failure != nil {
		return r.fail(record, *failure)
	}
	if spec.Name == constants.MCPToolRunShellCommand && r.req.ScenarioTools.TribunalCommands {
		if _, hasHostnames := args.Values()["hostnames"]; hasHostnames {
			return r.fail(record, agentToolFailure{ErrorType: agentToolErrorExecution, Error: "run_shell_command Tribunal authoring does not accept hostnames"})
		}
		encoded, err := json.Marshal(args.Values())
		if err != nil {
			return AgentToolCall{}, fmt.Errorf("evaluation: encode proposed %s arguments: %w", spec.Name, err)
		}
		var proposal models.RunShellCommandRequest
		if err := json.Unmarshal(encoded, &proposal); err != nil {
			return r.fail(record, agentToolFailure{ErrorType: agentToolErrorExecution, Error: fmt.Sprintf("invalid %s proposal: %v", spec.Name, err)})
		}
		record.ArgumentsJSON = proposal.CanonicalJSON()
	}
	switch spec.Name {
	case constants.MCPToolRunShellCommand:
		if r.req.ScenarioTools.TribunalCommands {
			return r.runTribunal(ctx, record, args)
		}
		return r.runNativeMCPTool(ctx, record, spec.Name, args)
	default:
		return r.runNativeMCPTool(ctx, record, spec.Name, args)
	}
}

func (r *agentRun) fail(record AgentToolCall, failure agentToolFailure) (AgentToolCall, error) {
	record.Success = false
	record.ErrorType = failure.ErrorType
	record.Error = failure.Error
	encoded, err := agentToolFailureJSON(failure)
	if err != nil {
		return AgentToolCall{}, err
	}
	record.ResultJSON = encoded
	return record, nil
}

func (r *agentRun) succeed(record AgentToolCall, value any) (AgentToolCall, error) {
	encoded, err := canonicalToolJSON(value)
	if err != nil {
		return AgentToolCall{}, err
	}
	record.Success = true
	record.ResultJSON = encoded
	return record, nil
}

// runNativeMCPTool maps one native MCP tool onto its governed MCP_CALL action.
func (r *agentRun) runNativeMCPTool(ctx context.Context, record AgentToolCall, tool string, args agentToolArguments) (AgentToolCall, error) {
	if tool == constants.MCPToolAuditEventReceiptList {
		r.injectAuditEventReceiptListDefaults(args)
	}
	var argsJSON []byte
	if tool == constants.MCPToolRunShellCommand {
		encoded, err := json.Marshal(args.Values())
		if err != nil {
			return AgentToolCall{}, fmt.Errorf("evaluation: encode %s arguments: %w", tool, err)
		}
		var request models.RunShellCommandRequest
		if err := json.Unmarshal(encoded, &request); err != nil {
			return AgentToolCall{}, fmt.Errorf("evaluation: decode %s arguments: %w", tool, err)
		}
		argsJSON = []byte(request.CanonicalJSON())
		record.ArgumentsJSON = string(argsJSON)
	} else {
		var err error
		argsJSON, err = json.Marshal(args.Values())
		if err != nil {
			return AgentToolCall{}, fmt.Errorf("evaluation: encode %s arguments: %w", tool, err)
		}
	}
	payload, err := proto.Marshal(&operatorv1.McpCallRequested{
		ToolName:      tool,
		ArgumentsJson: string(argsJSON),
		ExecutionId:   record.CallID,
	})
	if err != nil {
		return AgentToolCall{}, fmt.Errorf("evaluation: encode %s MCP call: %w", tool, err)
	}
	record.ActionType = string(constants.ActionTypeMcpCall)
	dispatched, denied, err := r.dispatch(ctx, &record, constants.ActionTypeMcpCall, tool, payload)
	if err != nil || denied {
		return record, err
	}
	if answerKind(dispatched.Outcome) != evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_EVENT_RECEIPT {
		return r.fail(record, agentToolFailure{ErrorType: agentToolErrorExecution, Error: agentDispatchErrorText(tool, dispatched)})
	}
	if record.EventReceipt != nil && record.EventReceipt.EventReceipt != nil && record.EventReceipt.GetStatus() == operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED {
		message := record.EventReceipt.GetResultSummary()
		if message == "" {
			message = "tool execution failed"
		}
		return r.fail(record, agentToolFailure{ErrorType: agentToolErrorExecution, Error: message})
	}
	if tool == constants.MCPToolAuditEventReceiptList {
		limit := args.Int("limit", agentToolDefaultHistoryLimit)
		query := mcp.AuditEventReceiptListRequest{
			InvestigationID:   args.String("investigation_id"),
			Limit:             limit,
			ExcludeAuditReads: args.Bool("exclude_audit_reads"),
		}
		result, refs, err := decodeInvestigationEventReceipts(r.trace, query, record.EventReceipt)
		if err != nil {
			return AgentToolCall{}, err
		}
		record.Retrieval = &AgentContextRetrieval{Query: query, EventReceipts: refs}
		return r.succeed(record, result)
	}
	summary := ""
	if record.EventReceipt != nil && record.EventReceipt.EventReceipt != nil {
		summary = record.EventReceipt.GetResultSummary()
	}
	if summary == "" {
		summary = "{}"
	}
	if tool == constants.MCPToolRunShellCommand {
		failure, err := shellCommandFailure(summary)
		if err != nil {
			return AgentToolCall{}, err
		}
		if failure != nil {
			return r.fail(record, *failure)
		}
		var result mcp.RunShellCommandResult
		if err := json.Unmarshal([]byte(summary), &result); err != nil {
			return AgentToolCall{}, fmt.Errorf("%w: decode %s result: %w", constants.ErrEvaluationDispatchFailed, tool, err)
		}
		return r.succeed(record, result)
	}
	canonical, err := canonicalizeToolJSON(summary)
	if err == nil {
		record.Success = true
		record.ResultJSON = canonical
		return record, nil
	}
	return r.succeed(record, map[string]any{"result": summary})
}

func (r *agentRun) injectAuditEventReceiptListDefaults(args agentToolArguments) {
	if args.String("investigation_id") == "" {
		args.Set("investigation_id", r.trace.InvestigationID)
	}
	// The model cannot turn the audit-read exclusion off: a context query must
	// never return the earlier audit reads of its own investigation.
	args.Set("exclude_audit_reads", true)
	if args.Int("limit", 0) <= 0 {
		args.Set("limit", json.Number(strconv.Itoa(agentToolDefaultHistoryLimit)))
	}
}

// dispatch submits one governed action and records its policy decision. It
// returns denied=true with the record already completed for a governance
// rejection or an ingress refusal.
func (r *agentRun) dispatch(ctx context.Context, record *AgentToolCall, action constants.ActionType, resource string, payload []byte) (GovernedToolDispatch, bool, error) {
	assignment := r.req.Assignment
	dispatched, err := r.executor.tools.DispatchAgentTool(ctx, r.target, assignment.GetRunId(), r.trace.InvestigationID, r.req.AttemptID, action, resource, payload)
	if err != nil {
		return GovernedToolDispatch{}, false, fmt.Errorf("tool %s: %w", record.ToolName, err)
	}
	denied, err := r.recordDispatch(record, dispatched)
	return dispatched, denied, err
}

// recordDispatch records one governed answer's event receipt and policy decision on
// record. It returns denied=true with the record already completed for a
// governance rejection or an ingress refusal.
func (r *agentRun) recordDispatch(record *AgentToolCall, dispatched GovernedToolDispatch) (bool, error) {
	if eventReceipt := answerEventReceipt(dispatched.Outcome); eventReceipt != nil {
		record.EventReceipt = &AgentTraceEventReceipt{EventReceipt: eventReceipt}
	}
	record.TransactionID = dispatched.TransactionID
	switch answerKind(dispatched.Outcome) {
	case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION:
		rejection := dispatched.Outcome.Observation.GetRejection()
		if rejection != nil && governance.IsGovernanceRejectionEventReceipt(rejection.GetEventReceipt()) {
			record.PolicyDecision = agentPolicyDecisionDeny
		}
		reason := answerMessage(dispatched.Outcome)
		if reason == "" {
			reason = "governance rejected the action"
		}
		completed, err := r.fail(*record, agentToolFailure{ErrorType: agentToolErrorSecurity, Error: "SECURITY VIOLATION: " + reason})
		if err != nil {
			return true, err
		}
		*record = completed
		return true, nil
	case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL:
		reason := answerMessage(dispatched.Outcome)
		completed, err := r.fail(*record, agentToolFailure{ErrorType: agentToolErrorExecution, Error: reason})
		if err != nil {
			return true, err
		}
		*record = completed
		return true, nil
	case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_EVENT_RECEIPT:
		record.PolicyDecision = agentPolicyDecisionAllow
	}
	return false, nil
}

// answerEventReceipt is the signed event receipt a governed answer carries: the executed
// event receipt, or the event receipt of a governance rejection. A refusal carries none.
func answerEventReceipt(outcome *client.GovernedOutcome) *operatorv1.EventReceipt {
	switch answerKind(outcome) {
	case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_EVENT_RECEIPT:
		return outcome.Observation.GetEventReceipt()
	case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION:
		return outcome.Observation.GetRejection().GetEventReceipt()
	default:
		return nil
	}
}

func agentDispatchErrorText(tool string, dispatched GovernedToolDispatch) string {
	reason := answerMessage(dispatched.Outcome)
	if reason == "" {
		reason = answerKind(dispatched.Outcome).String()
	}
	return fmt.Sprintf("ZTD-1607: Tool execution failed for %s: %s", tool, reason)
}

// shellCommandFailure returns the model-visible failure for a run_shell_command
// result that did not run to a zero exit, or nil. The event receipt is COMPLETED
// either way: it records that the command ran, and the exit code is the
// command's outcome, as for run_commands.
func shellCommandFailure(summary string) (*agentToolFailure, error) {
	var result mcp.RunShellCommandResult
	if err := json.Unmarshal([]byte(summary), &result); err != nil {
		return nil, fmt.Errorf("%w: decode %s result: %w", constants.ErrEvaluationDispatchFailed, constants.MCPToolRunShellCommand, err)
	}
	if result.ExitCode == 0 && !result.TimedOut {
		return nil, nil
	}
	message := strings.TrimSpace(result.Error)
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		message = stderr
	}
	if message == "" {
		message = fmt.Sprintf("command exited with status %d", result.ExitCode)
	}
	return &agentToolFailure{ErrorType: agentToolErrorExecution, Error: truncateToolText(message)}, nil
}

func truncateToolText(text string) string {
	if len(text) <= agentToolResultMaxBytes {
		return text
	}
	cut := agentToolResultMaxBytes
	for cut > 0 && !utf8RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n[truncated]"
}

func utf8RuneStart(b byte) bool {
	return b&0xC0 != 0x80
}

// agentTraceLifecycle is the single lifecycle rule for an agent trace, shared
// by import and verification. A provider failure before any agent turn
// answered is PROVIDER_FAILED; a completed trace under a semantic-judge
// catalog is GRADER_FAILED because this executor records no judge evidence.
func agentTraceLifecycle(req AssignmentExecutionRequest, trace *AgentTrace) evalv1.EvaluationAssignmentLifecycleStatus {
	if trace.ProviderFailure != "" && trace.FinalOutput == "" && !trace.ToolTurnLimitReached {
		return evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED
	}
	if req.GradingMethod == evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_SEMANTIC_JUDGE {
		return evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED
	}
	return evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
}

// agentTraceSchemaPinError is the campaign import's schema pin. The trace must
// carry the current sealed version: an earlier version skips the bindings the
// executor seals, so a relabelled trace is INVALID_EVIDENCE rather than graded.
func agentTraceSchemaPinError(trace *AgentTrace) error {
	if trace.SchemaVersion == agentTraceSchemaVersion {
		return nil
	}
	return fmt.Errorf("evaluation: agent trace schema version %q is not the version the campaign executor seals (%q): %w", trace.SchemaVersion, agentTraceSchemaVersion, constants.ErrEvidenceArtifactMalformed)
}

// agentTraceEvidenceError is every evidence rule an agent trace must pass
// before it is graded: the sealed schema pin, the signed event receipt evidence, and
// the binding of each inference to the frozen campaign models. Live import and
// the assignment verifier both apply it, so a stored INVALID_EVIDENCE verdict is
// recomputed from the same rules.
func agentTraceEvidenceError(req AssignmentExecutionRequest, trace *AgentTrace) error {
	return errors.Join(agentTraceSchemaPinError(trace), VerifyAgentTraceEvidence(trace), verifyAssignmentTraceModels(req, trace))
}

// agentTraceAssessment is the INVALID_EVIDENCE verdict on one agent trace.
// Exactly one field is set: grading when the trace is graded, invalid when it
// is INVALID_EVIDENCE, and failed when grading failed for a reason that is not
// a verdict on the trace (an executor failure that stays recoverable).
type agentTraceAssessment struct {
	grading *ScenarioGradingResult
	invalid error
	failed  error
}

// assessAgentTrace applies the campaign's INVALID_EVIDENCE rules to one agent
// trace. A trace with an evidence verdict (from evidence verification or the
// schema pin) is not graded and takes that verdict. Otherwise it is graded, and
// a grader error that is a verdict on the trace or the gold players is the
// verdict. Any other grader error is an executor failure. The live import and
// the offline verifier both apply this rule.
func assessAgentTrace(req AssignmentExecutionRequest, trace *AgentTrace, lifecycle evalv1.EvaluationAssignmentLifecycleStatus, evidenceErr error) agentTraceAssessment {
	if evidenceErr != nil {
		return agentTraceAssessment{invalid: evidenceErr}
	}
	graded, gradeErr := GradeHomogeneousScenario(req, trace, lifecycle)
	switch {
	case gradeErr == nil:
		return agentTraceAssessment{grading: graded}
	case isEvidenceGradingError(gradeErr):
		return agentTraceAssessment{invalid: gradeErr}
	default:
		return agentTraceAssessment{failed: gradeErr}
	}
}

// ImportAssignmentResultFromAgentTrace materializes one terminal homogeneous
// assignment result from a sealed agent trace. A trace whose tool calls the
// Data Operator's signed evidence does not back is INVALID_EVIDENCE: it carries
// no grades and the model is not graded on it. The trace must carry the current
// sealed schema version (see agentTraceSchemaPinError), for live and recovered
// imports alike.
func ImportAssignmentResultFromAgentTrace(req AssignmentExecutionRequest, trace *AgentTrace, traceEvidence *compliancev1.ComplianceEvidenceReference, now time.Time, newID func(string) string) (*evalv1.EvaluationAssignmentResult, error) {
	if req.Assignment == nil || trace == nil {
		return nil, fmt.Errorf("evaluation: import agent trace: %w", constants.ErrMissingRequiredField)
	}
	if err := ValidateAgentTraceDigest(trace); err != nil {
		return nil, fmt.Errorf("evaluation: import agent trace: %w", err)
	}
	if trace.AssignmentID != req.Assignment.GetAssignmentId() || trace.RunID != req.Assignment.GetRunId() {
		return nil, fmt.Errorf("evaluation: import agent trace: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	if err := trace.validateWorkspace(); err != nil {
		return nil, fmt.Errorf("evaluation: import agent trace: %w", err)
	}
	if newID == nil {
		newID = func(prefix string) string { return prefix }
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if req.AttemptID == "" {
		req.AttemptID = trace.EvaluationAttemptID
	}
	// Verification comes before grading: a trace the Data Operator's signed
	// evidence does not back is never graded against the model.
	evidenceErr := agentTraceEvidenceError(req, trace)
	// Grading reads only verified evidence. A grader error that is a verdict on
	// the trace or the gold players is INVALID_EVIDENCE; any other grader error
	// is an executor failure and stays recoverable.
	lifecycle := agentTraceLifecycle(req, trace)
	assessment := assessAgentTrace(req, trace, lifecycle, evidenceErr)
	if assessment.failed != nil {
		return nil, assessment.failed
	}
	invalidErr := assessment.invalid
	grading := assessment.grading
	modelInferences, scoredSpan := modelInferenceRecordsFromAgentTrace(req, trace, newID)
	assignmentID := req.Assignment.GetAssignmentId()
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:            CampaignSchemaVersion,
		AssignmentId:             assignmentID,
		RunId:                    req.Assignment.GetRunId(),
		CampaignId:               req.Assignment.GetCampaignId(),
		Lane:                     req.Assignment.GetLane(),
		ModelInferences:          modelInferences,
		ToolCalls:                []*evalv1.ToolCallRecord{},
		GovernedActions:          []*evalv1.GovernedActionBinding{},
		PolicyDecisions:          []*evalv1.PolicyDecisionRecord{},
		ToolCallsCaptured:        true,
		GovernedActionsCaptured:  evidenceErr == nil,
		PolicyDecisionsCaptured:  true,
		ScoredInferenceSpanNanos: scoredSpan,
		CompletedAt:              timestamppb.New(now),
	}
	if invalidErr != nil {
		result.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE
		result.InvalidEvidenceDetail = invalidErr.Error()
	} else {
		result.LifecycleStatus = lifecycle
		result.DeterministicGrades = grading.DeterministicGrades
		result.SemanticGrades = grading.SemanticGrades
		result.DecomposedScores = grading.DecomposedScores
		result.TrajectoryOutcome = grading.Trajectory.Outcome
		result.GuidedRetryCount = grading.Trajectory.GuidedRetryCount
		result.FailureReason = grading.Trajectory.FailureReason
		result.PublicFailureReason = grading.Trajectory.PublicFailureReason
	}
	// Governed actions are recorded in execution order: each governed turn, then
	// the tool calls it returned, each after the Tribunal inferences it
	// dispatched.
	for _, call := range trace.ModelCalls {
		if evidenceErr != nil || !call.IsTurn() || call.EventReceipt == nil {
			continue
		}
		binding, err := agentModelCallGovernedAction(traceEvidence, trace, assignmentID, call)
		if err != nil {
			return nil, err
		}
		result.GovernedActions = append(result.GovernedActions, binding)
		for _, tool := range trace.ToolCalls {
			if tool.ModelCallID != call.ProviderAttemptID {
				continue
			}
			for _, dispatched := range trace.ModelCalls {
				if dispatched.ToolCallID != tool.CallID || dispatched.EventReceipt == nil {
					continue
				}
				binding, err := agentModelCallGovernedAction(traceEvidence, trace, assignmentID, dispatched)
				if err != nil {
					return nil, err
				}
				result.GovernedActions = append(result.GovernedActions, binding)
			}
			binding, err := agentToolGovernedAction(traceEvidence, trace, assignmentID, tool)
			if err != nil {
				return nil, err
			}
			if binding != nil {
				result.GovernedActions = append(result.GovernedActions, binding)
			}
		}
	}
	modelCallIDs := make(map[string]bool, len(trace.ModelCalls))
	for _, call := range trace.ModelCalls {
		modelCallIDs[call.ProviderAttemptID] = true
	}
	for _, call := range trace.ToolCalls {
		if evidenceErr == nil && !modelCallIDs[call.ModelCallID] {
			binding, err := agentToolGovernedAction(traceEvidence, trace, assignmentID, call)
			if err != nil {
				return nil, err
			}
			if binding != nil {
				result.GovernedActions = append(result.GovernedActions, binding)
			}
		}
	}
	for _, call := range trace.ToolCalls {
		record := &evalv1.ToolCallRecord{
			CallId:          call.CallID,
			AssignmentId:    assignmentID,
			ToolName:        call.ToolName,
			ArgumentsHash:   agentArgumentsHash(call.ArgumentsJSON),
			SchemaOutcome:   agentToolCallOutcome(call.Success),
			SemanticOutcome: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE,
			LoopTurn:        uint32(call.LoopTurn),
			ErrorType:       call.ErrorType,
			GuidanceShown:   call.ShowedGuidance(),
		}
		if evidenceErr == nil && call.EventReceipt != nil {
			ref, err := agentEventReceiptEvidenceReference(traceEvidence, trace, call.TransactionID, call.EventReceipt)
			if err != nil {
				return nil, err
			}
			record.GovernedBindingRef = ref
		}
		result.ToolCalls = append(result.ToolCalls, record)
		if call.PolicyDecision != "" {
			outcome := evalv1.EvaluationPolicyDecisionOutcome_EVALUATION_POLICY_DECISION_OUTCOME_ALLOW
			if call.PolicyDecision == agentPolicyDecisionDeny {
				outcome = evalv1.EvaluationPolicyDecisionOutcome_EVALUATION_POLICY_DECISION_OUTCOME_DENY
			}
			result.PolicyDecisions = append(result.PolicyDecisions, &evalv1.PolicyDecisionRecord{
				DecisionId: call.CallID, AssignmentId: assignmentID, ToolName: call.ToolName, Outcome: outcome, Detail: call.Error,
			})
		}
	}
	if traceEvidence != nil {
		result.EvidenceRefs = []*compliancev1.ComplianceEvidenceReference{traceEvidence}
	}
	digest, err := ComputeAssignmentResultDigest(result)
	if err != nil {
		return nil, err
	}
	result.ResultDigest = digest
	return result, nil
}

// agentModelCallGovernedAction binds one governed inference to its Inference
// runtime event receipt.
func agentModelCallGovernedAction(traceEvidence *compliancev1.ComplianceEvidenceReference, trace *AgentTrace, assignmentID string, call AgentModelCall) (*evalv1.GovernedActionBinding, error) {
	ref, err := agentEventReceiptEvidenceReference(traceEvidence, trace, call.TransactionID, call.EventReceipt)
	if err != nil {
		return nil, err
	}
	decision := agentPolicyDecisionAllow
	if call.EventReceipt.GetFailureCode() == operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED {
		decision = agentPolicyDecisionDeny
	}
	return &evalv1.GovernedActionBinding{
		BindingId:         call.ProviderAttemptID,
		AssignmentId:      assignmentID,
		TransactionId:     call.TransactionID,
		OperatorId:        trace.Inference.OperatorID,
		OperatorSessionId: trace.Inference.SessionID,
		EventReceiptRef:   ref,
		PolicyDecision:    decision,
		ActionType:        call.EventReceipt.GetActionType(),
	}, nil
}

func agentArgumentsHash(arguments string) string {
	sum := sha256.Sum256([]byte(arguments))
	return hex.EncodeToString(sum[:])
}

func agentToolCallOutcome(success bool) evalv1.EvaluationVerdictStatus {
	if success {
		return evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
	}
	return evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
}

// modelInferenceRecordsFromAgentTrace maps every governed agent inference to
// a SCORED_CHAIN record: a scored turn of the tool loop, or a Tribunal
// inference under its persona and stage call site. The scored span is the
// summed generation time.
func modelInferenceRecordsFromAgentTrace(req AssignmentExecutionRequest, trace *AgentTrace, newID func(string) string) ([]*evalv1.ModelInferenceRecord, *uint64) {
	assignment := req.Assignment
	resolved, _ := ResolveAssignmentModels(req)
	records := make([]*evalv1.ModelInferenceRecord, 0, len(trace.ModelCalls))
	var span uint64
	for _, call := range trace.ModelCalls {
		usage := evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_UNAVAILABLE
		if call.UsageReported {
			usage = evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_REPORTED
		}
		retryCount := call.RetryCount
		persona, callSite := trace.DesignatedRole, agentCallSiteTurn
		if !call.IsTurn() {
			persona = call.Persona
			callSite = agentCallSiteTribunalPrefix + strings.TrimPrefix(call.Purpose, agentModelCallPurposeTribunalPrefix)
		}
		modelRole := evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED
		var variant *evalv1.ModelVariant
		if homogeneous := assignment.GetHomogeneous(); homogeneous != nil {
			modelRole = homogeneous.GetDesignatedRole()
			variant = homogeneous.GetCandidateVariant()
		} else if IsHeterogeneousAssignment(assignment) {
			modelRole = campaignRoleFromInferenceString(call.ModelRole)
			variant = resolved[modelRole]
		}
		// A governance-rejected inference never reached the provider, so it has
		// no provider attempt and no observation or provenance window.
		attemptID := call.ProviderAttemptID
		if call.EventReceipt != nil && governance.IsGovernanceRejectionEventReceipt(call.EventReceipt.EventReceipt) {
			attemptID = ""
		}
		record := &evalv1.ModelInferenceRecord{
			InferenceRecordId:       newID("inference"),
			Classification:          evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
			ProviderAttemptId:       attemptID,
			AssignmentId:            assignment.GetAssignmentId(),
			EvaluationAttemptId:     trace.EvaluationAttemptID,
			ModelRole:               modelRole,
			AgentPersona:            persona,
			CallSite:                callSite,
			ModelVariant:            variant,
			UsageAvailability:       usage,
			PromptTokens:            call.PromptTokens,
			CompletionTokens:        call.CompletionTokens,
			FirstTokenAtUnixNanos:   call.TTFTNanos,
			GenerationDurationNanos: call.GenerationDurationNanos,
			FinishReason:            call.FinishReason,
			LoadState:               evalv1.EvaluationLoadState(evalv1.EvaluationLoadState_value[call.LoadState]),
			RetryCount:              &retryCount,
			ResultDigest:            call.ResultDigest,
			ToolsDeclared:           call.ToolsDeclared,
			ToolsDeclaredReported:   true,
		}
		span += call.GenerationDurationNanos
		records = append(records, record)
	}
	if span == 0 {
		return records, nil
	}
	return records, &span
}

func campaignRoleFromInferenceString(role string) evalv1.ModelCampaignRole {
	switch role {
	case operatorv1.ModelRole_MODEL_ROLE_PRIMARY.String():
		return evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
	case operatorv1.ModelRole_MODEL_ROLE_ASSISTANT.String():
		return evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT
	case operatorv1.ModelRole_MODEL_ROLE_LITE.String():
		return evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE
	default:
		return evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED
	}
}
