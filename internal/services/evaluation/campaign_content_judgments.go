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
	"reflect"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// contentJudgeObjective is what the judge is told about the task: the scenario
// prompt, frozen evidence, history results, and explicitly selected receipted
// tool observations the role saw.
// It never carries the expected behavior or any criterion other than the one
// being judged.
func contentJudgeObjective(input ScenarioInputFixture, trace *AgentTrace, check *ScenarioContentCheck) string {
	objective := input.UserPrompt
	for _, evidence := range input.InlineContext {
		objective += "\n\nFrozen evidence (" + evidence.Label + "):\n" + evidence.Content
	}
	for _, call := range trace.ToolCalls {
		if call.ToolName == constants.MCPToolEventReceiptList && call.Success && call.Retrieval != nil {
			objective += "\n\nRecorded history query result:\n" + call.ResultJSON
		} else if check != nil && slices.Contains(check.ToolEvidence, call.ToolName) && call.TransactionID != "" && call.Receipt != nil && call.Receipt.EventReceipt != nil {
			objective += fmt.Sprintf("\n\nRecorded tool result (%s, success=%t):\nArguments: %s\nResult: %s", call.ToolName, call.Success, call.ArgumentsJSON, call.ResultJSON)
		}
	}
	return objective
}

// judgedCriteria returns the scenario's declared judged criteria.
func judgedCriteria(req AssignmentExecutionRequest) []judge.Criterion {
	check := req.ScenarioGold.ContentCheck
	if check == nil {
		return nil
	}
	criteria := make([]judge.Criterion, 0, len(check.JudgedCriteria))
	for _, criterion := range check.JudgedCriteria {
		criteria = append(criteria, judge.Criterion{ID: criterion.ID, Statement: criterion.Statement})
	}
	return criteria
}

// contentJudgmentsRequired reports whether the trace must carry one verdict per
// judged criterion: criteria are declared and the trace holds a final answer
// from a run that was not cut off. A trace without an answer is graded failed
// without a judge.
func contentJudgmentsRequired(req AssignmentExecutionRequest, trace *AgentTrace) bool {
	if len(judgedCriteria(req)) == 0 || trace == nil || trace.FinalOutput == "" {
		return false
	}
	return trace.TerminalClassification != AgentTerminalClassificationTimeout && trace.ProviderFailure == ""
}

// campaignJudgeConfig is the one construction of the pinned judge's config, for
// scoring and for offline verification. newID may be nil when only verifying.
func campaignJudgeConfig(req AssignmentExecutionRequest, newID func(string) string) (judge.Config, error) {
	variant, err := judgeVariant(req)
	if err != nil {
		return judge.Config{}, err
	}
	registry, digest, err := JudgeModelRegistry(req.Assignment.GetCampaignId(), variant)
	if err != nil {
		return judge.Config{}, fmt.Errorf("evaluation: judge config: %w", err)
	}
	return judge.Config{
		InferenceSessionID:  req.Binding.Topology.Inference.OperatorSessionID,
		Variant:             variant,
		CampaignID:          req.Assignment.GetCampaignId(),
		ModelRegistryDigest: digest,
		ModelRegistry:       registry,
		RunID:               req.Assignment.GetRunId(),
		AssignmentID:        req.Assignment.GetAssignmentId(),
		ScenarioID:          req.Assignment.GetScenarioId(),
		NewID:               newID,
	}, nil
}

// recordContentJudgments asks the pinned judge for one verdict per judged
// criterion, in declaration order, and records them on the trace before it is
// sealed. A judge that cannot decide a criterion fails the assignment as a
// non-recoverable error: the cell is never graded on a missing verdict.
func (e *CampaignAgentExecutor) recordContentJudgments(ctx context.Context, req AssignmentExecutionRequest, trace *AgentTrace) error {
	if !contentJudgmentsRequired(req, trace) {
		return nil
	}
	if e.inference == nil {
		return fmt.Errorf("evaluation: judge content criteria: no inference dispatcher: %w", constants.ErrMissingRequiredField)
	}
	cfg, err := campaignJudgeConfig(req, e.newID)
	if err != nil {
		return err
	}
	j, err := judge.New(e.inference, cfg)
	if err != nil {
		return fmt.Errorf("evaluation: judge content criteria: %w: %w", constants.ErrEvaluationContentJudgeFailed, err)
	}
	objective := contentJudgeObjective(req.ScenarioInput, trace, req.ScenarioGold.ContentCheck)
	for _, criterion := range judgedCriteria(req) {
		score := j.ScoreCriterion(ctx, criterion, objective, trace.FinalOutput)
		if score.Status != judge.StatusScored {
			return fmt.Errorf("evaluation: judge content criterion %q: %w: %s", criterion.ID, constants.ErrEvaluationContentJudgeFailed, score.Detail)
		}
		messages, err := agentTraceMessages(score.Messages)
		if err != nil {
			return fmt.Errorf("evaluation: record judge prompt for criterion %q: %w", criterion.ID, err)
		}
		trace.ContentJudgments = append(trace.ContentJudgments, AgentContentJudgment{
			CriterionID:       criterion.ID,
			ProviderAttemptID: score.ProviderAttemptID,
			TransactionID:     score.TransactionID,
			JudgeVariantID:    cfg.Variant.GetVariantId(),
			ServedModelTag:    score.JudgeModelTag,
			ModelDigest:       score.JudgeModelDigest,
			RubricVersion:     score.RubricVersion,
			Messages:          messages,
			Met:               score.Met,
			Rationale:         score.Rationale,
			InferenceResult:   &AgentTraceInferenceResult{InferenceResult: proto.Clone(score.Result).(*operatorv1.InferenceResult)},
			Receipt:           &AgentTraceReceipt{EventReceipt: score.Receipt},
		})
	}
	return nil
}

// verifyContentJudgments proves the trace carries exactly the verdicts its
// scenario requires and that each one is what the frozen judge returned: the
// recorded prompt is the re-rendered prompt, the receipt is signed by the
// pinned Inference runtime, and the signed result decodes to the recorded
// verdict.
func verifyContentJudgments(req AssignmentExecutionRequest, trace *AgentTrace) error {
	criteria := judgedCriteria(req)
	if !contentJudgmentsRequired(req, trace) {
		if len(trace.ContentJudgments) > 0 {
			return fmt.Errorf("evaluation: trace carries %d content judgments for a reply that is not judged: %w", len(trace.ContentJudgments), constants.ErrEvaluationContentJudgmentMismatch)
		}
		return nil
	}
	if len(trace.ContentJudgments) != len(criteria) {
		return fmt.Errorf("evaluation: trace carries %d content judgments for %d judged criteria: %w", len(trace.ContentJudgments), len(criteria), constants.ErrEvaluationContentJudgmentMissing)
	}
	cfg, err := campaignJudgeConfig(req, nil)
	if err != nil {
		return fmt.Errorf("evaluation: verify content judgments: %w", err)
	}
	anchors, err := agentTraceAnchorSet(trace)
	if err != nil {
		return fmt.Errorf("evaluation: verify content judgments: %w", err)
	}
	objective := contentJudgeObjective(req.ScenarioInput, trace, req.ScenarioGold.ContentCheck)
	attempts := make(map[string]struct{}, len(criteria))
	var errs []error
	for i, criterion := range criteria {
		errs = append(errs, verifyContentJudgment(cfg, anchors, trace, criterion, objective, trace.ContentJudgments[i], attempts))
	}
	return errors.Join(errs...)
}

func verifyContentJudgment(cfg judge.Config, anchors *oracle.AnchorSet, trace *AgentTrace, criterion judge.Criterion, objective string, judgment AgentContentJudgment, attempts map[string]struct{}) error {
	mismatch := func(format string, args ...any) error {
		return fmt.Errorf("evaluation: content judgment %q: %s: %w", criterion.ID, fmt.Sprintf(format, args...), constants.ErrEvaluationContentJudgmentMismatch)
	}
	if judgment.CriterionID != criterion.ID {
		return fmt.Errorf("evaluation: content judgment %q is recorded as %q: %w", criterion.ID, judgment.CriterionID, constants.ErrEvaluationContentJudgmentMissing)
	}
	if judgment.JudgeVariantID != cfg.Variant.GetVariantId() || judgment.ServedModelTag != cfg.Variant.GetServedModelTag() || judgment.ModelDigest != cfg.Variant.GetModelDigest() {
		return mismatch("judge %q is not the frozen judge %q", judgment.JudgeVariantID, cfg.Variant.GetVariantId())
	}
	if judgment.RubricVersion != judge.CriterionRubricVersion {
		return mismatch("rubric %q is not %q", judgment.RubricVersion, judge.CriterionRubricVersion)
	}
	if _, seen := attempts[judgment.ProviderAttemptID]; seen || judgment.ProviderAttemptID == "" {
		return mismatch("provider attempt %q is empty or reused", judgment.ProviderAttemptID)
	}
	attempts[judgment.ProviderAttemptID] = struct{}{}
	expected, err := agentTraceMessages(judge.CriterionMessages(criterion, objective, trace.FinalOutput))
	if err != nil {
		return fmt.Errorf("evaluation: content judgment %q: render prompt: %w", criterion.ID, err)
	}
	if !reflect.DeepEqual(expected, judgment.Messages) {
		return mismatch("recorded prompt is not the prompt rendered from the frozen objective and final reply")
	}
	if judgment.InferenceResult == nil || judgment.InferenceResult.InferenceResult == nil || judgment.Receipt == nil || judgment.Receipt.EventReceipt == nil {
		return fmt.Errorf("evaluation: content judgment %q has no signed inference result or receipt: %w", criterion.ID, constants.ErrEvaluationContentJudgmentMissing)
	}
	result, receipt := judgment.InferenceResult.InferenceResult, judgment.Receipt.EventReceipt
	if err := verifyJudgeReceipt(anchors, trace.Inference, judge.Score{
		Status:        judge.StatusScored,
		TransactionID: judgment.TransactionID,
		Receipt:       receipt,
		Result:        result,
	}); err != nil {
		return fmt.Errorf("evaluation: content judgment %q: %w: %w", criterion.ID, constants.ErrEvaluationContentJudgmentMismatch, err)
	}
	met, rationale, err := judge.VerifyCriterionResult(cfg, judgment.ProviderAttemptID, result, receipt)
	if err != nil {
		return fmt.Errorf("evaluation: content judgment %q: %w: %w", criterion.ID, constants.ErrEvaluationContentJudgmentMismatch, err)
	}
	if met != judgment.Met || rationale != judgment.Rationale {
		return mismatch("recorded verdict does not match the signed inference result")
	}
	return nil
}

// judgedContent is the grader's reading of the trace's judge verdicts.
type judgedContent struct {
	passed bool
	detail string
	// refused is the verdict of the scenario's refusal criterion.
	refused bool
	grades  []*evalv1.SemanticGrade
}

// contentCriterionID is the semantic grade criterion for one judged criterion.
func contentCriterionID(id string) string { return "content:" + id }

// gradeJudgedCriteria reads the verdicts sealed in the trace. The grader never
// dispatches: a criterion with no verdict fails closed, and every unmet
// criterion is named with the judge's rationale.
func gradeJudgedCriteria(req AssignmentExecutionRequest, trace *AgentTrace) judgedContent {
	criteria := judgedCriteria(req)
	if len(criteria) == 0 {
		return judgedContent{passed: true}
	}
	assignmentID := req.Assignment.GetAssignmentId()
	verdicts := make(map[string]AgentContentJudgment, len(trace.ContentJudgments))
	for _, judgment := range trace.ContentJudgments {
		verdicts[judgment.CriterionID] = judgment
	}
	refusal := req.ScenarioGold.ContentCheck.RefusalCriterion
	out := judgedContent{passed: true}
	var unmet []string
	for _, criterion := range criteria {
		judgment, ok := verdicts[criterion.ID]
		if !ok {
			// No judge decided this criterion, so there is no judge grade to
			// attribute; the failed scenario-content grade names it.
			unmet = append(unmet, criterion.ID+": no judge verdict")
			continue
		}
		grade := &evalv1.SemanticGrade{
			GradeId:     assignmentID + ":" + contentCriterionID(criterion.ID),
			CriterionId: contentCriterionID(criterion.ID),
		}
		switch {
		case judgment.Met:
			grade.Status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
			grade.JudgeVariantId = judgment.JudgeVariantID
			grade.Detail = judgment.Rationale
			out.refused = out.refused || criterion.ID == refusal
		default:
			grade.Status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
			grade.JudgeVariantId = judgment.JudgeVariantID
			grade.Detail = judgment.Rationale
			unmet = append(unmet, criterion.ID+": "+judgment.Rationale)
		}
		out.grades = append(out.grades, grade)
	}
	if len(unmet) > 0 {
		out.passed = false
		out.detail = "judged criteria not met: " + strings.Join(unmet, "; ")
	}
	return out
}

const contentCriterionPrefix = "content:"

// attachContentJudgmentEvidence binds each judged content grade to the governed
// judge inference that decided it: the grade names the receipt, and the result
// records the grader call and the governed action. The receipts live in the
// sealed trace, so the references resolve through the trace artifact.
func attachContentJudgmentEvidence(result *evalv1.EvaluationAssignmentResult, traceEvidence *compliancev1.ComplianceEvidenceReference, trace *AgentTrace, newID func(string) string) error {
	byCriterion := make(map[string]AgentContentJudgment, len(trace.ContentJudgments))
	for _, judgment := range trace.ContentJudgments {
		byCriterion[contentCriterionID(judgment.CriterionID)] = judgment
	}
	for _, grade := range result.GetSemanticGrades() {
		judgment, ok := byCriterion[grade.GetCriterionId()]
		if !ok {
			continue
		}
		ref, err := agentReceiptEvidenceReference(traceEvidence, trace, judgment.TransactionID, judgment.Receipt)
		if err != nil {
			return fmt.Errorf("evaluation: content judgment %q evidence: %w", judgment.CriterionID, err)
		}
		grade.GraderCallRef = ref
		result.GraderCalls = append(result.GraderCalls, &evalv1.GraderModelCallRecord{
			GraderCallId:       newID("grader-call"),
			AssignmentId:       result.GetAssignmentId(),
			JudgeVariantId:     judgment.JudgeVariantID,
			InferenceRecordRef: ref,
		})
		result.GovernedActions = append(result.GovernedActions, &evalv1.GovernedActionBinding{
			BindingId:         judgment.ProviderAttemptID,
			AssignmentId:      result.GetAssignmentId(),
			TransactionId:     judgment.TransactionID,
			OperatorId:        trace.Inference.OperatorID,
			OperatorSessionId: trace.Inference.SessionID,
			ReceiptRef:        ref,
			PolicyDecision:    agentPolicyDecisionAllow,
			ActionType:        judgment.Receipt.GetActionType(),
		})
	}
	return nil
}

// contentGradesMatch reports whether the stored judged content grades are the
// grades recomputed from the trace's sealed verdicts, and each is bound to a
// receipt reference.
func contentGradesMatch(stored, recomputed []*evalv1.SemanticGrade) bool {
	want := make(map[string]*evalv1.SemanticGrade)
	for _, grade := range recomputed {
		if strings.HasPrefix(grade.GetCriterionId(), contentCriterionPrefix) {
			want[grade.GetCriterionId()] = grade
		}
	}
	seen := 0
	for _, grade := range stored {
		if !strings.HasPrefix(grade.GetCriterionId(), contentCriterionPrefix) {
			continue
		}
		seen++
		expected := want[grade.GetCriterionId()]
		if expected == nil || grade.GetGradeId() != expected.GetGradeId() || grade.GetStatus() != expected.GetStatus() ||
			grade.GetJudgeVariantId() != expected.GetJudgeVariantId() || grade.GetDetail() != expected.GetDetail() {
			return false
		}
		if expected.GetJudgeVariantId() != "" && grade.GetGraderCallRef() == nil {
			return false
		}
	}
	return seen == len(want)
}
