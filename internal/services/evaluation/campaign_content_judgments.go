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
	"time"

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
	if trace.DesignatedRole == "lite" {
		for _, file := range input.WorkspaceFiles {
			objective += "\n\nSupplied file evidence (" + file.Label + ", " + file.RelPath + "):\n" + file.Content
		}
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

// contentJudgeTypedObjective is the grading-contract.v2 objective: the
// rendered request, the seed conversation, the evidence supplied with the
// request, every seeded platform record the conversation showed, and every
// governed tool call the role made with the result it observed. A chain-only
// seed is absent unless the role retrieved its receipt, exactly as for the role.
func contentJudgeTypedObjective(req AssignmentExecutionRequest, trace *AgentTrace) judge.Objective {
	input := req.ScenarioInput
	ws := trace.ScenarioWorkspace()
	objective := judge.Objective{Task: ws.Render(input.UserPrompt)}
	for _, turn := range input.Seed.Turns {
		objective.Conversation = append(objective.Conversation, judge.ObjectiveTurn{Sender: turn.Sender, Text: ws.Render(turn.Content)})
	}
	for _, inline := range input.InlineContext {
		objective.Evidence = append(objective.Evidence, judge.ObjectiveEvidence{Label: inline.Label, Source: inline.Kind, Content: ws.Render(inline.Content)})
	}
	if trace.DesignatedRole == "lite" {
		for _, file := range input.WorkspaceFiles {
			objective.Evidence = append(objective.Evidence, judge.ObjectiveEvidence{Label: file.Label, Source: "supplied file " + ws.Render(ScenarioWorkspaceToken+"/"+file.RelPath), Content: ws.Render(file.Content)})
		}
	}
	for _, observation := range trace.Observations {
		if observation.Kind == scenarioObservationSeed && observation.Disclosure != ScenarioSeedDisclosureChainOnly {
			objective.Records = append(objective.Records, objectiveRecord(judge.ObjectiveRecordPlatform, observation.Call))
		}
	}
	for _, call := range trace.ToolCalls {
		objective.Records = append(objective.Records, objectiveRecord(judge.ObjectiveRecordAgent, call))
	}
	return objective
}

func objectiveRecord(source judge.ObjectiveRecordSource, call AgentToolCall) judge.ObjectiveRecord {
	return judge.ObjectiveRecord{Source: source, Tool: call.ToolName, Arguments: call.ArgumentsJSON, Outcome: scenarioCallOutcome(call), Result: call.ResultJSON}
}

// contentJudgeMessages renders the criterion prompt and names the rubric
// version the scenario's frozen grading contract requires.
func contentJudgeMessages(req AssignmentExecutionRequest, trace *AgentTrace, criterion judge.Criterion) (string, []*operatorv1.InferenceMessage) {
	if req.ScenarioGold.gradingContractV2() {
		return judge.ObjectiveCriterionRubricVersion, judge.ObjectiveCriterionMessages(criterion, contentJudgeTypedObjective(req, trace), trace.FinalOutput)
	}
	return judge.CriterionRubricVersion, judge.CriterionMessages(criterion, contentJudgeObjective(req.ScenarioInput, trace, req.ScenarioGold.ContentCheck), trace.FinalOutput)
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
// from a run that was not cut off by its wall clock or stopped for repetition.
// A trace without an answer is graded failed without a judge.
func contentJudgmentsRequired(req AssignmentExecutionRequest, trace *AgentTrace) bool {
	if liteTriageCell(req) || len(judgedCriteria(req)) == 0 || trace == nil || trace.FinalOutput == "" {
		return false
	}
	switch trace.TerminalClassification {
	case AgentTerminalClassificationTimeout, AgentTerminalClassificationRepetition:
		return false
	}
	return trace.ProviderFailure == ""
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

// judgeActivityObserver reports each judge dispatch to the request's sink. A
// completed judgment is reported only after its receipt is checked, so the
// verdict it reports is the one the grade records.
type judgeActivityObserver struct {
	ctx      context.Context
	sink     GradingActivitySink
	variant  *evalv1.ModelVariant
	receipts judgeReceiptCheck
}

func (o judgeActivityObserver) Started(score judge.Score) {
	o.sink.JudgeActivity(o.ctx, JudgeActivity{Judge: o.variant, Score: score})
}

func (o judgeActivityObserver) Completed(score judge.Score, elapsed time.Duration) {
	if err := o.receipts.verify(score); err != nil {
		score.Status, score.Failure, score.Detail = judge.StatusFailed, evalv1.JudgeFailure_JUDGE_FAILURE_RECEIPT_UNVERIFIED, err.Error()
	}
	o.sink.JudgeActivity(o.ctx, JudgeActivity{Judge: o.variant, Score: score, Completed: true, Elapsed: elapsed})
}

// judgeReceiptCheck verifies judge receipts against the trace's pinned
// Inference anchors, built once per assignment.
type judgeReceiptCheck struct {
	anchors    *oracle.AnchorSet
	anchorsErr error
	inference  AgentTraceRuntime
}

func newJudgeReceiptCheck(trace *AgentTrace) judgeReceiptCheck {
	anchors, err := agentTraceAnchorSet(trace)
	return judgeReceiptCheck{anchors: anchors, anchorsErr: err, inference: trace.Inference}
}

// verify is the one receipt rule for a judge score: a scored judgment must
// carry a signed receipt, and any receipt it carries must verify. A failed
// judgment the runtime never executed has nothing to verify.
func (c judgeReceiptCheck) verify(score judge.Score) error {
	switch {
	case score.Status == judge.StatusScored && (score.TransactionID == "" || score.Receipt == nil):
		return fmt.Errorf("judge score has no signed governed receipt: %w", constants.ErrMissingRequiredField)
	case score.TransactionID == "":
		return nil
	case c.anchorsErr != nil:
		return c.anchorsErr
	default:
		return verifyJudgeReceipt(c.anchors, c.inference, score)
	}
}

// executionJudgeConfig is campaignJudgeConfig for a live execution: when the
// request observes judge activity, every dispatch reports to its sink.
func (e *CampaignAgentExecutor) executionJudgeConfig(ctx context.Context, req AssignmentExecutionRequest, receipts judgeReceiptCheck) (judge.Config, error) {
	cfg, err := campaignJudgeConfig(req, e.newID)
	if err != nil || req.GradingActivity == nil {
		return cfg, err
	}
	cfg.Observer = judgeActivityObserver{ctx: ctx, sink: req.GradingActivity, variant: cfg.Variant, receipts: receipts}
	cfg.Clock = e.now
	return cfg, nil
}

// recordContentJudgments asks the pinned judge for one verdict per judged
// criterion, in declaration order, and records them on the trace before it is
// sealed. A judge that cannot decide a criterion records that failed judgment
// with its typed failure and ends the judgments: the trace is still sealed and
// persisted, the assignment is GRADER_FAILED, and the cell is never graded on
// a missing verdict (INV-EVAL-CAMP-54).
func (e *CampaignAgentExecutor) recordContentJudgments(ctx context.Context, req AssignmentExecutionRequest, trace *AgentTrace) error {
	if !contentJudgmentsRequired(req, trace) {
		return nil
	}
	if e.inference == nil {
		return fmt.Errorf("evaluation: judge content criteria: no inference dispatcher: %w", constants.ErrMissingRequiredField)
	}
	cfg, err := e.executionJudgeConfig(ctx, req, newJudgeReceiptCheck(trace))
	if err != nil {
		return err
	}
	criteria := judgedCriteria(req)
	j, err := judge.New(e.inference, cfg)
	//nolint:nilerr // A judge that cannot be built is the failed judgment the trace records, not an execution error.
	if err != nil {
		trace.ContentJudgments = append(trace.ContentJudgments, AgentContentJudgment{
			CriterionID:    criteria[0].ID,
			JudgeVariantID: cfg.Variant.GetVariantId(),
			ServedModelTag: cfg.Variant.GetServedModelTag(),
			ModelDigest:    cfg.Variant.GetModelDigest(),
			Failure:        evalv1.JudgeFailure_JUDGE_FAILURE_REQUEST_INVALID.String(),
		})
		return nil
	}
	objective := contentJudgeObjective(req.ScenarioInput, trace, req.ScenarioGold.ContentCheck)
	typed := contentJudgeTypedObjective(req, trace)
	for i, criterion := range criteria {
		position := judge.Position{Index: i + 1, Count: len(criteria)}
		var score judge.Score
		if req.ScenarioGold.gradingContractV2() {
			score = j.ScoreObjectiveCriterion(ctx, criterion, position, typed, trace.FinalOutput)
		} else {
			score = j.ScoreCriterion(ctx, criterion, position, objective, trace.FinalOutput)
		}
		messages, err := agentTraceMessages(score.Messages)
		if err != nil {
			return fmt.Errorf("evaluation: record judge prompt for criterion %q: %w", criterion.ID, err)
		}
		if score.Status != judge.StatusScored {
			trace.ContentJudgments = append(trace.ContentJudgments, failedContentJudgment(cfg, criterion, score, messages))
			return nil
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

// judgeFailureFromOutput reports whether a judge failure is read from the
// judge's own signed output, so the failed judgment carries that result and
// receipt and verification re-derives the failure from them. Every other
// failure has no trustworthy result to carry.
func judgeFailureFromOutput(failure evalv1.JudgeFailure) bool {
	return failure == evalv1.JudgeFailure_JUDGE_FAILURE_VERDICT_INVALID || failure == evalv1.JudgeFailure_JUDGE_FAILURE_REPETITION
}

// failedContentJudgment records a judgment the pinned judge failed to decide.
// The failure is the typed code, never the error text, which can echo the
// judged reply.
func failedContentJudgment(cfg judge.Config, criterion judge.Criterion, score judge.Score, messages []AgentTraceMessage) AgentContentJudgment {
	judgment := AgentContentJudgment{
		CriterionID:       criterion.ID,
		ProviderAttemptID: score.ProviderAttemptID,
		TransactionID:     score.TransactionID,
		JudgeVariantID:    cfg.Variant.GetVariantId(),
		ServedModelTag:    score.JudgeModelTag,
		ModelDigest:       score.JudgeModelDigest,
		RubricVersion:     score.RubricVersion,
		Messages:          messages,
		Failure:           score.Failure.String(),
	}
	if judgeFailureFromOutput(score.Failure) && score.Result != nil && score.Receipt != nil {
		judgment.InferenceResult = &AgentTraceInferenceResult{InferenceResult: proto.Clone(score.Result).(*operatorv1.InferenceResult)}
		judgment.Receipt = &AgentTraceReceipt{EventReceipt: score.Receipt}
	}
	return judgment
}

// judgeFailureReasons is the fixed public sentence for each judge failure. A
// GRADER_FAILED result publishes it instead of the failure's error text.
var judgeFailureReasons = map[evalv1.JudgeFailure]string{
	evalv1.JudgeFailure_JUDGE_FAILURE_REQUEST_INVALID:    "Grading failed: the judge request could not be built.",
	evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED:    "Grading failed: the judge inference returned no result.",
	evalv1.JudgeFailure_JUDGE_FAILURE_RESPONSE_MISMATCH:  "Grading failed: the judge result answered a different request.",
	evalv1.JudgeFailure_JUDGE_FAILURE_RESULT_DIGEST:      "Grading failed: the judge result did not match its signed receipt.",
	evalv1.JudgeFailure_JUDGE_FAILURE_VERDICT_INVALID:    "Grading failed: the judge's reply was not a verdict in the rubric's format.",
	evalv1.JudgeFailure_JUDGE_FAILURE_RECEIPT_UNVERIFIED: "Grading failed: the judge receipt did not verify.",
	evalv1.JudgeFailure_JUDGE_FAILURE_REPETITION:         "Grading failed: the judge's output degenerated into a repeating loop and was stopped.",
}

// contentJudgeFailure returns the typed failure of the trace's failed content
// judgment, if it has one.
func contentJudgeFailure(trace *AgentTrace) (evalv1.JudgeFailure, bool) {
	judgment, failed := trace.failedContentJudgment()
	if !failed {
		return evalv1.JudgeFailure_JUDGE_FAILURE_UNSPECIFIED, false
	}
	return evalv1.JudgeFailure(evalv1.JudgeFailure_value[judgment.Failure]), true
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
	// A failed judgment ends the judgments, so only the criteria up to it are
	// judged.
	judged := len(criteria)
	if _, failed := trace.failedContentJudgment(); failed {
		judged = len(trace.ContentJudgments)
	}
	if len(trace.ContentJudgments) != judged || judged > len(criteria) {
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
	attempts := make(map[string]struct{}, len(criteria))
	var errs []error
	for i, criterion := range criteria[:judged] {
		rubricVersion, expected := contentJudgeMessages(req, trace, criterion)
		judgment := trace.ContentJudgments[i]
		if judgment.Failure != "" && i != judged-1 {
			errs = append(errs, fmt.Errorf("evaluation: content judgment %q fails before the last judgment: %w", criterion.ID, constants.ErrEvaluationContentJudgmentMismatch))
			continue
		}
		errs = append(errs, verifyContentJudgment(cfg, anchors, trace, criterion, rubricVersion, expected, judgment, attempts))
	}
	return errors.Join(errs...)
}

func verifyContentJudgment(cfg judge.Config, anchors *oracle.AnchorSet, trace *AgentTrace, criterion judge.Criterion, rubricVersion string, rendered []*operatorv1.InferenceMessage, judgment AgentContentJudgment, attempts map[string]struct{}) error {
	mismatch := func(format string, args ...any) error {
		return fmt.Errorf("evaluation: content judgment %q: %s: %w", criterion.ID, fmt.Sprintf(format, args...), constants.ErrEvaluationContentJudgmentMismatch)
	}
	if judgment.CriterionID != criterion.ID {
		return fmt.Errorf("evaluation: content judgment %q is recorded as %q: %w", criterion.ID, judgment.CriterionID, constants.ErrEvaluationContentJudgmentMissing)
	}
	if judgment.JudgeVariantID != cfg.Variant.GetVariantId() || judgment.ServedModelTag != cfg.Variant.GetServedModelTag() || judgment.ModelDigest != cfg.Variant.GetModelDigest() {
		return mismatch("judge %q is not the frozen judge %q", judgment.JudgeVariantID, cfg.Variant.GetVariantId())
	}
	if judgment.Failure != "" {
		return verifyFailedContentJudgment(cfg, anchors, trace, criterion, rubricVersion, rendered, judgment, attempts)
	}
	if judgment.RubricVersion != rubricVersion {
		return mismatch("rubric %q is not %q", judgment.RubricVersion, rubricVersion)
	}
	if _, seen := attempts[judgment.ProviderAttemptID]; seen || judgment.ProviderAttemptID == "" {
		return mismatch("provider attempt %q is empty or reused", judgment.ProviderAttemptID)
	}
	attempts[judgment.ProviderAttemptID] = struct{}{}
	expected, err := agentTraceMessages(rendered)
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

// verifyFailedContentJudgment proves a failed judgment is the failure the
// frozen judge's evidence produces. A failure read from the judge's output
// carries the dispatched prompt, a signed result and receipt, and is
// re-derived from them exactly; any other failure carries no verdict and no
// result, and only its typed code is recorded.
func verifyFailedContentJudgment(cfg judge.Config, anchors *oracle.AnchorSet, trace *AgentTrace, criterion judge.Criterion, rubricVersion string, rendered []*operatorv1.InferenceMessage, judgment AgentContentJudgment, attempts map[string]struct{}) error {
	mismatch := func(format string, args ...any) error {
		return fmt.Errorf("evaluation: failed content judgment %q: %s: %w", criterion.ID, fmt.Sprintf(format, args...), constants.ErrEvaluationContentJudgmentMismatch)
	}
	failure, known := evalv1.JudgeFailure_value[judgment.Failure]
	if !known || evalv1.JudgeFailure(failure) == evalv1.JudgeFailure_JUDGE_FAILURE_UNSPECIFIED {
		return mismatch("failure %q is not a judge failure", judgment.Failure)
	}
	if judgment.Met || judgment.Rationale != "" {
		return mismatch("a failed judgment carries a verdict")
	}
	hasResult := judgment.InferenceResult != nil || judgment.Receipt != nil
	if !judgeFailureFromOutput(evalv1.JudgeFailure(failure)) {
		if hasResult {
			return mismatch("failure %s carries a result it cannot vouch for", judgment.Failure)
		}
		return nil
	}
	if judgment.RubricVersion != rubricVersion {
		return mismatch("rubric %q is not %q", judgment.RubricVersion, rubricVersion)
	}
	if _, seen := attempts[judgment.ProviderAttemptID]; seen || judgment.ProviderAttemptID == "" {
		return mismatch("provider attempt %q is empty or reused", judgment.ProviderAttemptID)
	}
	attempts[judgment.ProviderAttemptID] = struct{}{}
	expected, err := agentTraceMessages(rendered)
	if err != nil {
		return fmt.Errorf("evaluation: failed content judgment %q: render prompt: %w", criterion.ID, err)
	}
	if !reflect.DeepEqual(expected, judgment.Messages) {
		return mismatch("recorded prompt is not the prompt rendered from the frozen objective and final reply")
	}
	if judgment.InferenceResult == nil || judgment.InferenceResult.InferenceResult == nil || judgment.Receipt == nil || judgment.Receipt.EventReceipt == nil {
		return fmt.Errorf("evaluation: failed content judgment %q has no signed inference result or receipt: %w", criterion.ID, constants.ErrEvaluationContentJudgmentMissing)
	}
	result, receipt := judgment.InferenceResult.InferenceResult, judgment.Receipt.EventReceipt
	if err := verifyJudgeReceipt(anchors, trace.Inference, judge.Score{Status: judge.StatusScored, TransactionID: judgment.TransactionID, Receipt: receipt, Result: result}); err != nil {
		return fmt.Errorf("evaluation: failed content judgment %q: %w: %w", criterion.ID, constants.ErrEvaluationContentJudgmentMismatch, err)
	}
	_, _, err = judge.VerifyCriterionResult(cfg, judgment.ProviderAttemptID, result, receipt)
	if err == nil {
		return mismatch("the signed result decodes to a verdict")
	}
	if derived := judge.ResultFailure(err); derived != evalv1.JudgeFailure(failure) {
		return mismatch("recorded failure %s is not %s, the failure of the signed result", judgment.Failure, derived)
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
		case judgment.Failure != "":
			// The frozen judge did not decide this criterion: the grade is
			// unmeasured and binds no grader call.
			reason := judgeFailureReasons[evalv1.JudgeFailure(evalv1.JudgeFailure_value[judgment.Failure])]
			grade.Status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE
			grade.JudgeVariantId = judgment.JudgeVariantID
			grade.Detail = reason
			unmet = append(unmet, criterion.ID+": "+reason)
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
		if !ok || judgment.Failure != "" {
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
			CriterionId:        grade.GetCriterionId(),
			RubricVersion:      judgment.RubricVersion,
			ProviderAttemptId:  judgment.ProviderAttemptID,
			TransactionId:      judgment.TransactionID,
			InferenceResult:    proto.Clone(judgment.InferenceResult.InferenceResult).(*operatorv1.InferenceResult),
			Receipt:            proto.Clone(judgment.Receipt.EventReceipt).(*operatorv1.EventReceipt),
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
// grades recomputed from the trace's sealed verdicts, and each decided grade is
// bound to a receipt reference. A criterion the judge failed to decide is
// UNAVAILABLE and binds none.
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
		decided := expected.GetJudgeVariantId() != "" && expected.GetStatus() != evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE
		if decided != (grade.GetGraderCallRef() != nil) {
			return false
		}
	}
	return seen == len(want)
}
