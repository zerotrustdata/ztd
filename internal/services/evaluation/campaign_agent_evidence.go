// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	inferencedispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// agentReceiptSchemaRef names the message a receipt evidence reference points at.
const agentReceiptSchemaRef = "ztd.operator.v1.EventReceipt"

// VerifyAgentTraceEvidence proves, from the sealed trace alone, that every
// governed inference is backed by a receipt the Inference runtime signed,
// every governed tool dispatch by a receipt its roster target signed, and that
// each roster member's ledger between its two sentinels holds exactly the
// executions the trace claims on that member. Each member is bracketed
// whether or not a call targeted it, so executions outside the claimed work
// cannot go unnoticed. When one Operator holds both capabilities its
// ledger also records every inference, so that member's bracket claims the
// model calls as well as its tool dispatches, in ledger order. An observer on
// a roster member commits its BEGIN and FINALIZE there too, and that member's
// bracket claims those receipts as well.
func VerifyAgentTraceEvidence(trace *AgentTrace) error {
	if trace == nil {
		return agentEvidenceError(agentEvidenceStageRuntime, "trace", constants.ErrMissingRequiredField)
	}
	if err := validateAgentTraceRoster(trace.Roster); err != nil {
		return agentEvidenceError(agentEvidenceStageRuntime, "roster", err)
	}
	if err := validateAgentTraceRosterJoins(trace); err != nil {
		return agentEvidenceError(agentEvidenceStageRuntime, "roster joins", err)
	}
	if err := verifyRosterPrompt(trace); err != nil {
		return agentEvidenceError(agentEvidenceStageRuntime, "roster prompt", err)
	}
	anchors, err := agentTraceAnchorSet(trace)
	if err != nil {
		return err
	}
	ledgers := make([]*memberLedger, len(trace.Roster))
	for i, member := range trace.Roster {
		subject := fmt.Sprintf("roster [%d] ledger bracket", member.Index)
		if member.Bracket == nil {
			return agentEvidenceError(agentEvidenceStageLedger, subject, constants.ErrMissingRequiredField)
		}
		before, err := verifiedSentinelOutcome(anchors, member.Runtime, member.Bracket.Before.EventReceipt)
		if err != nil {
			return agentEvidenceError(agentEvidenceStageLedger, subject+" before", err)
		}
		after, err := verifiedSentinelOutcome(anchors, member.Runtime, member.Bracket.After.EventReceipt)
		if err != nil {
			return agentEvidenceError(agentEvidenceStageLedger, subject+" after", err)
		}
		ledgers[i] = &memberLedger{member: member, before: before, after: after, phases: make(map[string]uint8)}
	}
	claim := func(claims []rosterClaim, add func(*memberLedger, oracle.Outcome)) {
		for _, c := range claims {
			add(ledgers[c.index-1], c.outcome)
		}
	}
	shared, sharedOK := trace.SharedMember()
	for _, call := range trace.ModelCalls {
		outcome, err := verifyModelCallEvidence(anchors, trace, call)
		if err != nil {
			return agentEvidenceError(agentEvidenceStageModelCall, "model call "+call.ProviderAttemptID, err)
		}
		if sharedOK && outcome.Commitment != nil {
			ledgers[shared.Index-1].model = append(ledgers[shared.Index-1].model, outcome)
		}
		observed, err := verifyObservationEvidence(anchors, trace.Roster, call)
		if err != nil {
			return agentEvidenceError(agentEvidenceStageModelCall, "model call "+call.ProviderAttemptID+" observation", err)
		}
		claim(observed, (*memberLedger).addObservation)
	}
	if err := verifyRetryChains(trace); err != nil {
		return agentEvidenceError(agentEvidenceStageModelCall, "retry chain", err)
	}
	if err := verifyV7EpisodeOutputs(trace); err != nil {
		return agentEvidenceError(agentEvidenceStageModelCall, "episode output", err)
	}
	for _, call := range trace.ToolCalls {
		if call.Receipt != nil && !call.IsDenied() {
			if err := verifyNativeToolCallRequest(trace, call); err != nil {
				return agentEvidenceError(agentEvidenceStageToolCall, "tool call "+call.CallID+" request", err)
			}
			if err := verifyNativeToolCallResult(trace, call); err != nil {
				return agentEvidenceError(agentEvidenceStageToolCall, "tool call "+call.CallID+" result", err)
			}
		}
		votes, err := verifyConsensusInferenceEvidence(anchors, trace, call)
		if err != nil {
			return agentEvidenceError(agentEvidenceStageToolCall, "tool call "+call.CallID+" consensus", err)
		}
		claim(votes, (*memberLedger).addConsensus)
		if err := verifyContextRetrieval(trace, call); err != nil {
			return agentEvidenceError(agentEvidenceStageToolCall, "context retrieval "+call.CallID, err)
		}
		outcome, err := verifyTargetedCallEvidence(anchors, trace, call)
		if err != nil {
			return agentEvidenceError(agentEvidenceStageToolCall, "tool call "+call.CallID, err)
		}
		if outcome != nil && outcome.Commitment != nil {
			ledgers[call.TargetIndex-1].tool = append(ledgers[call.TargetIndex-1].tool, *outcome)
		}
	}
	for _, observation := range trace.Observations {
		subject := "scenario observation " + string(observation.Kind) + " " + observation.Call.ToolName
		if err := verifyScenarioObservation(trace, observation); err != nil {
			return agentEvidenceError(agentEvidenceStageObservation, subject, err)
		}
		votes, err := verifyConsensusInferenceEvidence(anchors, trace, observation.Call)
		if err != nil {
			return agentEvidenceError(agentEvidenceStageObservation, subject+" consensus", err)
		}
		outcome, err := verifyTargetedCallEvidence(anchors, trace, observation.Call)
		if err != nil {
			return agentEvidenceError(agentEvidenceStageObservation, subject+" receipt", err)
		}
		// Seed evidence precedes agent activity; final checks follow it. The
		// observation's own consensus calls sit in the same phase.
		phase := uint8(2)
		if observation.Kind == scenarioObservationSeed {
			phase = 0
		}
		for _, vote := range votes {
			ledger := ledgers[vote.index-1]
			ledger.consensus = append(ledger.consensus, vote.outcome)
			ledger.phases[vote.outcome.Commitment.Hash] = phase
		}
		if outcome != nil && outcome.Commitment != nil {
			ledger := ledgers[observation.Call.TargetIndex-1]
			ledger.scenario = append(ledger.scenario, *outcome)
			ledger.phases[outcome.Commitment.Hash] = phase
		}
	}
	for _, ledger := range ledgers {
		if err := ledger.verify(); err != nil {
			return agentEvidenceError(agentEvidenceStageLedger, fmt.Sprintf("roster [%d] ledger bracket", ledger.member.Index), err)
		}
	}
	return nil
}

// verifyRosterPrompt requires the system prompt of a trace that declared
// tools to carry the roster block rendered from the frozen roster, so the
// roster the model read is the roster its calls resolved against. A held
// repetition's system prompt sits in its held context, verified with it.
func verifyRosterPrompt(trace *AgentTrace) error {
	if trace.HeldContext != nil || trace.Input == nil || len(trace.Input.Tools) == 0 {
		return nil
	}
	system := operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM.String()
	if len(trace.Input.Messages) == 0 || trace.Input.Messages[0].Role != system || !strings.Contains(trace.Input.Messages[0].Text, renderRosterPrompt(trace.InitialRoster())) {
		return constants.ErrHarnessRosterMismatch
	}
	return nil
}

// rosterClaim is one verified execution claimed on a roster member's ledger.
type rosterClaim struct {
	index   int
	outcome oracle.Outcome
}

// memberLedger collects the executions the trace claims on one roster member
// between its two sentinels.
type memberLedger struct {
	member        AgentTraceRosterMember
	before, after oracle.Outcome
	model, tool   []oracle.Outcome
	scenario      []oracle.Outcome
	consensus     []oracle.Outcome
	observation   []oracle.Outcome
	// phases orders scenario observations around agent activity: 0 for seed
	// evidence, 2 for final checks; everything else is 1.
	phases map[string]uint8
}

func (l *memberLedger) addObservation(outcome oracle.Outcome) {
	l.observation = append(l.observation, outcome)
}

func (l *memberLedger) addConsensus(outcome oracle.Outcome) {
	l.consensus = append(l.consensus, outcome)
}

// verify proves the member's bracket holds exactly the claimed executions,
// that each role's claims are in ledger order, and that seed observations
// precede and final checks follow agent activity.
func (l *memberLedger) verify() error {
	claims := append(slices.Clone(l.model), l.tool...)
	claims = append(claims, l.consensus...)
	claims = append(claims, l.scenario...)
	ordered, err := oracle.OrderLedger(l.before, l.after, append(claims, l.observation...))
	if err == nil {
		err = oracle.VerifyBracket(l.before, l.after, ordered)
	}
	if err != nil {
		return err
	}
	// Grading reads each role's calls in trace order, so that order must be
	// the order the ledger committed them in.
	for _, claims := range [][]oracle.Outcome{l.model, l.tool, l.scenario} {
		if !inLedgerOrder(ordered, claims) {
			return fmt.Errorf("trace records claimed executions out of ledger order: %w", constants.ErrConformanceLedgerGap)
		}
	}
	var phase uint8
	for _, outcome := range ordered {
		next, observation := l.phases[outcome.Commitment.Hash]
		if !observation {
			next = 1
		}
		if next < phase {
			return fmt.Errorf("scenario observations do not bracket agent activity: %w", constants.ErrConformanceLedgerGap)
		}
		phase = next
	}
	return nil
}

// verifyTargetedCallEvidence verifies one recorded dispatch against the
// roster member it targeted. A call rejected before any target resolved has
// no target and must carry no receipt.
func verifyTargetedCallEvidence(anchors *oracle.AnchorSet, trace *AgentTrace, call AgentToolCall) (*oracle.Outcome, error) {
	if call.TargetIndex == 0 {
		if call.Receipt != nil || call.TransactionID != "" || call.RequestEnvelope != nil || call.Success {
			return nil, fmt.Errorf("dispatch without a roster target carries execution evidence: %w", constants.ErrHarnessFanOutMismatch)
		}
		return nil, nil
	}
	member, err := trace.TargetMember(call)
	if err != nil {
		return nil, err
	}
	return verifyToolCallEvidence(anchors, member.Runtime, call)
}

// verifyRetryChains proves every retried model call against the attempt it
// names in retry_of. The retried attempt must be the model call immediately
// before it in the trace, the same call (purpose, loop turn, tool call,
// persona) one retry ordinal lower on the same model and request, not have
// completed, and carry a receipt the Inference runtime signed as a FAILED
// execution whose provider asked to retry later
// (RECEIPT_FAILURE_CODE_BACKEND_TRANSIENT). No call retries past the transient
// retry policy's bound. Receipts are verified by verifyModelCallEvidence before
// this runs.
//
// Adjacency holds for every producer: the executor's turn and Interrogator
// calls (agentRun.infer) and the Gateway's Tribunal member calls
// (memberInferer.Infer, recorded in order by recordMemberCalls) append an
// attempt and its retry back to back. It is what keeps a chain from linking
// across Tribunal rounds, stages, or tool calls: a model call does not carry
// its Tribunal round, so the same persona's calls in two rounds are otherwise
// indistinguishable, and no later call can name an earlier attempt over the
// calls between them.
func verifyRetryChains(trace *AgentTrace) error {
	fail := func(call AgentModelCall, reason string) error {
		return fmt.Errorf("model call %q %s: %w", call.ProviderAttemptID, reason, constants.ErrEvaluationEvidenceTraceMismatch)
	}
	for i, call := range trace.ModelCalls {
		if (call.RetryOf == "") != (call.RetryCount == 0) {
			return fail(call, "retry ordinal and retried attempt disagree")
		}
		if call.RetryCount > inferencedispatch.MaxTransientRetries() {
			return fail(call, "retries past the transient retry bound")
		}
		if call.RetryOf == "" {
			continue
		}
		if i == 0 || trace.ModelCalls[i-1].ProviderAttemptID != call.RetryOf {
			return fail(call, "does not retry the model call immediately before it")
		}
		previous := trace.ModelCalls[i-1]
		if previous.Purpose != call.Purpose || previous.LoopTurn != call.LoopTurn || previous.ToolCallID != call.ToolCallID || previous.Persona != call.Persona {
			return fail(call, "retries a different call")
		}
		if previous.ModelVariantID != call.ModelVariantID || previous.ModelRole != call.ModelRole ||
			previous.ServedModelTag != call.ServedModelTag || previous.ModelDigest != call.ModelDigest {
			return fail(call, "retries on a different model")
		}
		if !slices.Equal(previous.ToolsDeclared, call.ToolsDeclared) || !slices.Equal(previous.Input, call.Input) {
			return fail(call, "retries with a different request")
		}
		if previous.RetryCount+1 != call.RetryCount {
			return fail(call, "does not follow its retried attempt's ordinal")
		}
		if previous.Completed() || previous.Receipt == nil || !governance.IsTransientFailureReceipt(previous.Receipt.EventReceipt) {
			return fail(call, "retries an attempt the provider did not ask to retry")
		}
	}
	return nil
}

// The final diagnosis and requested tool arguments are grader inputs too. Bind
// them to the signed responses rather than trusting the trace's repeated copy.
func verifyV7EpisodeOutputs(trace *AgentTrace) error {
	var lastTurn, lastInterrogation *AgentModelCall
	turns := make(map[string]*AgentModelCall)
	seenAttempts := make(map[string]bool)
	seenTransactions := make(map[string]bool)
	for i := range trace.ModelCalls {
		call := &trace.ModelCalls[i]
		if seenAttempts[call.ProviderAttemptID] || seenTransactions[call.TransactionID] {
			return fmt.Errorf("model call repeats an inference identity: %w", constants.ErrEvaluationEvidenceTraceMismatch)
		}
		seenAttempts[call.ProviderAttemptID] = true
		seenTransactions[call.TransactionID] = true
		if call.IsTurn() && call.Completed() && call.InferenceResult != nil {
			lastTurn = call
			turns[call.ProviderAttemptID] = call
		}
		if call.Purpose == agentModelCallPurposeInterrogation && call.Completed() && call.InferenceResult != nil {
			lastInterrogation = call
		}
	}
	// An unanswered clarification ends on the Interrogator's signed questions,
	// dispatched after the reasoner's last signalling turn.
	if trace.FinalOutput != "" && trace.TerminalClassification == AgentTerminalClassificationUnsupportedClarification &&
		lastTurn != nil && lastInterrogation != nil && lastInterrogation.LoopTurn == lastTurn.LoopTurn {
		lastTurn = lastInterrogation
	}
	if trace.FinalOutput != "" && (lastTurn == nil || lastTurn.Text != trace.FinalOutput) {
		return fmt.Errorf("final output differs from the last signed model turn: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	if len(trace.ToolCalls) > 0 && trace.Input == nil {
		return fmt.Errorf("tool calls have no recorded tool declarations: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	specs := make(map[string]agentToolSpec)
	if trace.Input != nil {
		for _, declaration := range trace.Input.Tools {
			spec, err := agentToolSpecFromDeclaration(declaration)
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrEvaluationEvidenceTraceMismatch, err)
			}
			specs[declaration.Name] = spec
		}
	}
	if err := validateAgentTraceRosterJoins(trace); err != nil {
		return err
	}
	activeCount := len(trace.InitialRoster())
	pendingCount := activeCount
	currentModelCallID := ""
	activatedJoins := make([]bool, len(trace.RosterJoins))
	for _, group := range toolCallGroups(trace.ToolCalls) {
		modelCallID := group[0].ModelCallID
		if currentModelCallID != "" && modelCallID != currentModelCallID {
			activeCount = pendingCount
		}
		currentModelCallID = modelCallID
		if err := verifyFanOutGroup(trace.Roster[:activeCount], turns, specs, group); err != nil {
			return err
		}
		for _, call := range group {
			for j, join := range trace.RosterJoins {
				if join.JoinedAfterCall != call.CallID {
					continue
				}
				if call.ToolName != constants.MCPToolOperatorDeploy || !call.Success {
					return fmt.Errorf("roster join [%d] references call %q which is not a successful %s: %w", join.Index, call.CallID, constants.MCPToolOperatorDeploy, constants.ErrHarnessRosterMismatch)
				}
				if join.Index != pendingCount+1 {
					return fmt.Errorf("roster join [%d] activated out of order (expected [%d]): %w", join.Index, pendingCount+1, constants.ErrHarnessRosterMismatch)
				}
				pendingCount++
				activatedJoins[j] = true
			}
		}
	}
	for j, activated := range activatedJoins {
		if !activated {
			return fmt.Errorf("roster join [%d] after call %q was never activated: %w", trace.RosterJoins[j].Index, trace.RosterJoins[j].JoinedAfterCall, constants.ErrHarnessRosterMismatch)
		}
	}
	for _, tool := range trace.ToolCalls {
		if tool.ApprovedArgumentsJSON != "" {
			var approved models.RunShellCommandRequest
			if err := json.Unmarshal([]byte(tool.ApprovedArgumentsJSON), &approved); err != nil || approved.Command == "" || approved.CanonicalJSON() != tool.ApprovedArgumentsJSON {
				return fmt.Errorf("approved shell arguments are not canonical typed JSON: %w", constants.ErrEvaluationEvidenceTraceMismatch)
			}
			matchedReview := false
			for _, step := range trace.Steps {
				if review := step.GetReview(); review != nil && models.RunShellCommandRequestFromProto(review.GetArguments()).CanonicalJSON() == tool.ApprovedArgumentsJSON {
					matchedReview = true
					break
				}
			}
			if !matchedReview {
				return fmt.Errorf("approved shell arguments differ from every typed Tribunal review: %w", constants.ErrEvaluationEvidenceTraceMismatch)
			}
		}
	}
	return nil
}

// verifyObservationEvidence returns the ledger claims for the observer receipts
// a model call carries that a roster member signed. An observer on a roster
// member commits its BEGIN and FINALIZE inside that member's bracket, so the
// bracket must claim them; an observer on any other runtime commits to a ledger
// no bracket covers, so its receipt claims nothing. A claimed receipt must be a
// verified observation with a commitment, whether its handler completed or
// failed: its signed action type binds it, so no other execution on the member
// can be passed off as one. A rejected observation committed nothing and
// claims nothing.
func verifyObservationEvidence(anchors *oracle.AnchorSet, roster []AgentTraceRosterMember, call AgentModelCall) ([]rosterClaim, error) {
	var claims []rosterClaim
	for _, receipt := range call.ObservationReceipts {
		if receipt.EventReceipt == nil {
			return nil, constants.ErrMissingRequiredField
		}
		member, ok := rosterSigner(roster, receipt.GetSignerKeyId())
		if !ok {
			continue
		}
		switch action := constants.ActionType(receipt.GetActionType()); action {
		case constants.ActionTypeProviderBoundaryObservation, constants.ActionTypeModelProvenanceObservation:
		default:
			return nil, fmt.Errorf("receipt action %q is not an observation: %w", action, constants.ErrEvaluationEvidenceTraceMismatch)
		}
		outcome, err := verifiedOutcome(anchors, member.Runtime.OperatorID, receipt.EventReceipt)
		if err != nil {
			return nil, err
		}
		if err := verifyReceiptSession(receipt.EventReceipt, member.Runtime.SessionID); err != nil {
			return nil, err
		}
		if outcome.Kind == oracle.OutcomeRejected {
			continue
		}
		if outcome.Commitment == nil {
			return nil, fmt.Errorf("observation receipt carries no commitment: %w", constants.ErrConformanceLedgerGap)
		}
		claims = append(claims, rosterClaim{index: member.Index, outcome: outcome})
	}
	return claims, nil
}

// rosterSigner returns the roster member whose pinned actuator key is keyID.
func rosterSigner(roster []AgentTraceRosterMember, keyID string) (AgentTraceRosterMember, bool) {
	for _, member := range roster {
		if keyID != "" && member.Runtime.Anchor.GetKeyId() == keyID {
			return member, true
		}
	}
	return AgentTraceRosterMember{}, false
}

// inLedgerOrder reports whether claims appear in ordered in the same relative
// order.
func inLedgerOrder(ordered, claims []oracle.Outcome) bool {
	next := 0
	for _, outcome := range ordered {
		if next < len(claims) && outcome.Commitment.Hash == claims[next].Commitment.Hash {
			next++
		}
	}
	return next == len(claims)
}

// agentTraceAnchorSet pins each traced runtime's key: the Inference runtime
// and every roster member. Each must carry an anchor bound to its own runtime;
// a runtime shared by the Inference role and a member must pin one key for
// both, so a receipt verifies the same whichever role claims it.
func agentTraceAnchorSet(trace *AgentTrace) (*oracle.AnchorSet, error) {
	check := func(name string, runtime AgentTraceRuntime) error {
		if runtime.OperatorID == "" || runtime.Anchor == nil || runtime.Anchor.ConformanceAnchor == nil {
			return agentEvidenceError(agentEvidenceStageRuntime, name, constants.ErrConformanceAnchorMissing)
		}
		if runtimeID := runtime.Anchor.GetRuntimeId(); runtimeID != runtime.OperatorID {
			return agentEvidenceError(agentEvidenceStageRuntime, name, fmt.Errorf("anchor runtime %q is not the traced runtime %q: %w", runtimeID, runtime.OperatorID, constants.ErrConformanceAnchorUnknown))
		}
		return nil
	}
	if err := check("inference runtime anchor", trace.Inference); err != nil {
		return nil, err
	}
	var pinned []*evalv1.ConformanceAnchor
	for _, member := range trace.Roster {
		if err := check(fmt.Sprintf("roster [%d] runtime anchor", member.Index), member.Runtime); err != nil {
			return nil, err
		}
		pinned = append(pinned, member.Runtime.Anchor.ConformanceAnchor)
	}
	if shared, ok := trace.SharedMember(); ok {
		if !proto.Equal(trace.Inference.Anchor.ConformanceAnchor, shared.Runtime.Anchor.ConformanceAnchor) {
			return nil, agentEvidenceError(agentEvidenceStageRuntime, "inference runtime anchor", fmt.Errorf("shared runtime %q pins two keys: %w", shared.Runtime.OperatorID, constants.ErrConformanceAnchorInvalid))
		}
	} else {
		pinned = append(pinned, trace.Inference.Anchor.ConformanceAnchor)
	}
	anchors, err := oracle.NewAnchorSet(pinned)
	if err != nil {
		return nil, agentEvidenceError(agentEvidenceStageRuntime, "runtime anchors", err)
	}
	return anchors, nil
}

// verifyModelCallEvidence checks one governed inference against its receipt.
// Every recorded call must carry a receipt the Inference runtime signed for
// its own transaction. A completed call's receipt must be an execution whose
// result_summary is the call's result digest; a failed call's receipt may be
// a failed execution, which still holds a ledger commitment, or a governance
// rejection, which holds none.
func verifyModelCallEvidence(anchors *oracle.AnchorSet, trace *AgentTrace, call AgentModelCall) (oracle.Outcome, error) {
	if call.Receipt == nil || call.Receipt.EventReceipt == nil {
		return oracle.Outcome{}, fmt.Errorf("governed inference has no signed receipt: %w", constants.ErrEventReceiptMissing)
	}
	receipt := call.Receipt.EventReceipt
	outcome, err := verifiedOutcome(anchors, trace.Inference.OperatorID, receipt)
	if err != nil {
		return oracle.Outcome{}, err
	}
	if err := verifyReceiptSession(receipt, trace.Inference.SessionID); err != nil {
		return oracle.Outcome{}, err
	}
	if call.TransactionID == "" || receipt.GetTransactionId() != call.TransactionID {
		return oracle.Outcome{}, fmt.Errorf("receipt transaction %q is not the call transaction %q: %w", receipt.GetTransactionId(), call.TransactionID, constants.ErrEvaluationEvidenceTraceMismatch)
	}
	if err := verifyV7ModelCallIdentity(trace, call, outcome); err != nil {
		return oracle.Outcome{}, err
	}
	switch {
	case call.Completed():
		if outcome.Kind != oracle.OutcomeExecuted {
			return oracle.Outcome{}, fmt.Errorf("completed inference has a receipt that is not an execution: %w", constants.ErrEvaluationEvidenceTraceMismatch)
		}
		if receipt.GetResultSummary() != call.ResultDigest {
			return oracle.Outcome{}, fmt.Errorf("receipt result summary is not the call result digest: %w", constants.ErrEvaluationEvidenceTraceMismatch)
		}
	case outcome.Kind == oracle.OutcomeExecuted, outcome.Kind == oracle.OutcomeExecutionFailed, outcome.Kind == oracle.OutcomeRejected:
	default:
		return oracle.Outcome{}, fmt.Errorf("receipt outcome %d is not an inference outcome: %w", outcome.Kind, constants.ErrConformanceReceiptUnclassifiable)
	}
	return outcome, nil
}

// verifyV7ModelCallIdentity proves the per-call transcript and model identity
// are the ones returned by the signed Inference Operator result. The frozen
// assignment-to-variant mapping is checked by the campaign verifier that has
// the run and campaign records; this layer ensures the trace's repeated claim
// is complete and internally bound to the result and its receipt.
func verifyV7ModelCallIdentity(trace *AgentTrace, call AgentModelCall, outcome oracle.Outcome) error {
	fail := func(reason string) error {
		return fmt.Errorf("%s: %w", reason, constants.ErrEvaluationEvidenceTraceMismatch)
	}
	var result *operatorv1.InferenceResult
	if call.InferenceResult != nil {
		result = call.InferenceResult.InferenceResult
	}
	if call.ModelVariantID == "" || call.ModelRole == "" || call.ServedModelTag == "" || call.ModelDigest == "" {
		return fmt.Errorf("v7 inference model identity is incomplete: %w", constants.ErrMissingRequiredField)
	}
	if call.ProviderAttemptID == "" {
		return fmt.Errorf("v7 inference provider attempt is missing: %w", constants.ErrMissingRequiredField)
	}
	if trace.CampaignID == "" || trace.RunID == "" || trace.AssignmentID == "" || trace.EvaluationAttemptID == "" || trace.ScenarioID == "" || trace.DesignatedRole == "" || trace.ServedModelTag == "" || trace.ModelDigest == "" || trace.ModelRegistryDigest == "" {
		return fmt.Errorf("v7 trace model identity is incomplete: %w", constants.ErrMissingRequiredField)
	}
	if !models.IsSHA256Hex(trace.ModelDigest) || !models.IsSHA256Hex(trace.ModelRegistryDigest) {
		return fail("trace model or registry digest is malformed")
	}
	if !validInferenceModelRole(call.ModelRole) {
		return fail("model role is not a recognized inference role")
	}
	traceRole, roleKnown := inferenceModelRoleForTraceRole(trace.DesignatedRole)
	if roleKnown && call.ModelRole == traceRole {
		if call.ServedModelTag != trace.ServedModelTag || call.ModelDigest != trace.ModelDigest {
			return fail("per-call designated variant identity differs from the trace identity")
		}
	} else if campaignRoleFromInferenceString(call.ModelRole) != formationSupportCallRole(trace, call.Purpose) {
		// A heterogeneous system assignment dispatches the Interrogator and each
		// Tribunal stage to its own formation slot while trace-level identity names
		// the designated role. Exact variant-to-slot binding is checked by verifier.
		return fail(fmt.Sprintf("%s call on model role %s is neither the designated role nor its formation slot", call.Purpose, call.ModelRole))
	}
	if result == nil {
		if call.Completed() || outcome.Kind == oracle.OutcomeExecuted {
			return fmt.Errorf("completed v7 inference has no complete result: %w", constants.ErrMissingRequiredField)
		}
		// Failed providers may have a signed FAILED receipt and no result. The
		// campaign verifier checks this call's per-role variant against the
		// frozen assignment even though there is no response to bind.
		return nil
	}
	if call.ProviderAttemptID == "" ||
		result.GetProviderAttemptId() != call.ProviderAttemptID ||
		result.GetCampaignId() != trace.CampaignID ||
		result.GetRunId() != trace.RunID ||
		result.GetAssignmentId() != trace.AssignmentID ||
		result.GetEvaluationAttemptId() != trace.EvaluationAttemptID ||
		result.GetScenarioId() != trace.ScenarioID ||
		result.GetModelRegistryDigest() != trace.ModelRegistryDigest {
		return fail("signed inference result identity differs from the trace")
	}
	if result.GetRequestedModel() != call.ServedModelTag || result.GetModel() != call.ServedModelTag ||
		result.GetRequestedModelDigest() != call.ModelDigest || result.GetServedModelDigest() != call.ModelDigest {
		return fail("signed inference result model differs from the per-call model identity")
	}
	resultDigest, err := models.ComputeInferenceResultDigest(result)
	if err != nil {
		return fmt.Errorf("compute signed inference result digest: %w", err)
	}
	if result.GetResultDigest() == "" || result.GetResultDigest() != resultDigest || call.ResultDigest != resultDigest || call.Receipt.GetResultSummary() != resultDigest {
		return fmt.Errorf("signed inference result digest does not match the call and receipt: %w", constants.ErrInferenceResultDigest)
	}
	if err := verifyV7ModelCallTranscript(call, result); err != nil {
		return err
	}
	return nil
}

func inferenceModelRoleForTraceRole(role string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "primary":
		return operatorv1.ModelRole_MODEL_ROLE_PRIMARY.String(), true
	case "assistant":
		return operatorv1.ModelRole_MODEL_ROLE_ASSISTANT.String(), true
	case "lite":
		return operatorv1.ModelRole_MODEL_ROLE_LITE.String(), true
	default:
		return "", false
	}
}

func validInferenceModelRole(role string) bool {
	switch role {
	case operatorv1.ModelRole_MODEL_ROLE_PRIMARY.String(),
		operatorv1.ModelRole_MODEL_ROLE_ASSISTANT.String(),
		operatorv1.ModelRole_MODEL_ROLE_LITE.String():
		return true
	default:
		return false
	}
}

// verifyV7ModelCallTranscript binds grader-visible output and telemetry to the
// complete result covered by the signed receipt.
func verifyV7ModelCallTranscript(call AgentModelCall, result *operatorv1.InferenceResult) error {
	text, thinking, toolCalls := agentResponseParts(result)
	if call.Text != text || call.Thinking != thinking || call.ToolCallCount != len(toolCalls) {
		return fmt.Errorf("model transcript differs from the signed inference result: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	ttft, generation := uint64(0), uint64(0)
	if result.TimeToFirstTokenNs != nil && *result.TimeToFirstTokenNs > 0 {
		ttft = uint64(*result.TimeToFirstTokenNs)
	}
	if result.GenerationDurationNs != nil && *result.GenerationDurationNs > 0 {
		generation = uint64(*result.GenerationDurationNs)
	}
	if result.GetPromptTokens() < 0 || result.GetCompletionTokens() < 0 ||
		call.PromptTokens != uint32(result.GetPromptTokens()) || call.CompletionTokens != uint32(result.GetCompletionTokens()) ||
		call.UsageReported != result.GetUsageReported() || call.FinishReason != result.GetFinishReason() ||
		call.LoadState != operatorLoadStateToEvaluation(result.GetLoadState()).String() || call.RetryCount != result.GetRetryCount() ||
		call.TTFTNanos != ttft || call.GenerationDurationNanos != generation {
		return fmt.Errorf("model telemetry differs from the signed inference result: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	return nil
}

// verifyToolCallEvidence checks one tool call against its receipt. A call the
// Gateway admitted must carry a verified receipt for its own transaction, from
// the Data session the trace records. A refusal carries none. It returns the
// verified outcome, or nil when the call has no receipt.
func verifyToolCallEvidence(anchors *oracle.AnchorSet, data AgentTraceRuntime, call AgentToolCall) (*oracle.Outcome, error) {
	if call.Receipt == nil || call.Receipt.EventReceipt == nil {
		if call.Success || call.TransactionID != "" || call.PolicyDecision == agentPolicyDecisionAllow {
			return nil, fmt.Errorf("admitted call has no signed receipt: %w", constants.ErrEventReceiptMissing)
		}
		return nil, nil
	}
	receipt := call.Receipt.EventReceipt
	if call.ApprovedArgumentsJSON != "" && (call.ActionType != string(constants.ActionTypeMcpCall) || receipt.GetActionType() != string(constants.ActionTypeMcpCall)) {
		return nil, fmt.Errorf("approved Tribunal arguments are not bound to an MCP_CALL receipt: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	outcome, err := verifiedOutcome(anchors, data.OperatorID, receipt)
	if err != nil {
		return nil, err
	}
	if err := verifyReceiptSession(receipt, data.SessionID); err != nil {
		return nil, err
	}
	if receipt.GetTransactionId() != call.TransactionID {
		return nil, fmt.Errorf("receipt transaction %q is not the call transaction %q: %w", receipt.GetTransactionId(), call.TransactionID, constants.ErrEvaluationEvidenceTraceMismatch)
	}
	switch outcome.Kind {
	case oracle.OutcomeExecuted, oracle.OutcomeExecutionFailed:
		if call.PolicyDecision != agentPolicyDecisionAllow {
			return nil, fmt.Errorf("executed receipt on a call recorded as %q: %w", call.PolicyDecision, constants.ErrEvaluationEvidenceTraceMismatch)
		}
	case oracle.OutcomeRejected:
		if call.Success || call.PolicyDecision != agentPolicyDecisionDeny {
			return nil, fmt.Errorf("rejected receipt on a call recorded as %q: %w", call.PolicyDecision, constants.ErrEvaluationEvidenceTraceMismatch)
		}
	default:
		return nil, fmt.Errorf("receipt outcome %d is not a tool call outcome: %w", outcome.Kind, constants.ErrConformanceReceiptUnclassifiable)
	}
	return &outcome, nil
}

// verifyReceiptSession requires every stage of the receipt to name the Operator
// session the trace records for the role that claimed it. The ledger bracket
// cannot make this check: a receipt from another session on the same runtime
// commits to the same ledger and still connects. The Warden stamps every stage
// from the envelope and rejects an envelope with no session, so a receipt with
// no stages, or a stage that names no session, is never a governed execution.
func verifyReceiptSession(receipt *operatorv1.EventReceipt, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("trace records no operator session: %w", constants.ErrMissingRequiredField)
	}
	stages := receipt.GetDeterministicStageEvidence()
	if len(stages) == 0 {
		return fmt.Errorf("receipt has no deterministic stage evidence: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	for _, stage := range stages {
		if named := stage.GetOperatorSessionId(); named != sessionID {
			return fmt.Errorf("receipt names operator session %q, want %q: %w", named, sessionID, constants.ErrEvaluationEvidenceTraceMismatch)
		}
	}
	return nil
}

// verifiedSentinelOutcome verifies one harness sentinel receipt. A sentinel
// must be an executed read in the Data session: anything else leaves the
// bracket unconnected.
func verifiedSentinelOutcome(anchors *oracle.AnchorSet, data AgentTraceRuntime, receipt *operatorv1.EventReceipt) (oracle.Outcome, error) {
	if receipt == nil {
		return oracle.Outcome{}, fmt.Errorf("sentinel receipt is missing: %w", constants.ErrEventReceiptMissing)
	}
	outcome, err := verifiedOutcome(anchors, data.OperatorID, receipt)
	if err != nil {
		return oracle.Outcome{}, err
	}
	if err := verifyReceiptSession(receipt, data.SessionID); err != nil {
		return oracle.Outcome{}, err
	}
	if outcome.Kind != oracle.OutcomeExecuted {
		return oracle.Outcome{}, fmt.Errorf("sentinel receipt is not an executed read: %w", constants.ErrConformanceLedgerGap)
	}
	return outcome, nil
}

// verifiedOutcome verifies a receipt against the runtime's pinned key and
// classifies what it says happened.
func verifiedOutcome(anchors *oracle.AnchorSet, runtimeID string, receipt *operatorv1.EventReceipt) (oracle.Outcome, error) {
	if _, err := oracle.VerifyReceipt(anchors, runtimeID, receipt); err != nil {
		return oracle.Outcome{}, err
	}
	return oracle.Classify(receipt)
}

// verifyJudgeReceipt proves a judge score's governed inference receipt was
// signed by the Inference runtime's pinned key in the Inference session and is
// the transaction the score names. The judge's response checks do not verify the signature, so a judge
// score is labelled verified only through this check. The receipt's result
// summary binds the result digest, so the result the score was read from is
// recomputed and must match it: an altered score body under an intact digest is
// never verified. A failed judge has no scored body to bind, so it keeps only
// the receipt check when it carries no result.
func verifyJudgeReceipt(anchors *oracle.AnchorSet, inference AgentTraceRuntime, sc judge.Score) error {
	if _, err := oracle.VerifyReceipt(anchors, inference.OperatorID, sc.Receipt); err != nil {
		return err
	}
	if err := verifyReceiptSession(sc.Receipt, inference.SessionID); err != nil {
		return err
	}
	if sc.Receipt.GetTransactionId() != sc.TransactionID {
		return fmt.Errorf("receipt transaction %q is not the judge transaction %q: %w", sc.Receipt.GetTransactionId(), sc.TransactionID, constants.ErrEvaluationEvidenceTraceMismatch)
	}
	if sc.Result == nil {
		if sc.Status == judge.StatusScored {
			return fmt.Errorf("scored judge response has no inference result: %w", constants.ErrMissingRequiredField)
		}
		return nil
	}
	resultDigest, err := models.ComputeInferenceResultDigest(sc.Result)
	if err != nil {
		return fmt.Errorf("compute judge inference result digest: %w", err)
	}
	if sc.Result.GetResultDigest() != resultDigest || sc.Receipt.GetResultSummary() != resultDigest {
		return fmt.Errorf("judge inference result digest does not match the result and receipt: %w", constants.ErrInferenceResultDigest)
	}
	return nil
}

// agentReceiptEvidenceReference names one verified receipt. The receipt lives
// inside the sealed trace, so the reference carries the trace artifact's ID and
// the digest of the receipt's canonical protojson.
func agentReceiptEvidenceReference(traceEvidence *compliancev1.ComplianceEvidenceReference, trace *AgentTrace, transactionID string, receipt *AgentTraceReceipt) (*compliancev1.ComplianceEvidenceReference, error) {
	if traceEvidence == nil || receipt == nil || receipt.EventReceipt == nil {
		return nil, fmt.Errorf("evaluation: agent receipt reference: %w", constants.ErrMissingRequiredField)
	}
	encoded, err := evalv1.MarshalCanonical(receipt.EventReceipt)
	if err != nil {
		return nil, fmt.Errorf("evaluation: agent receipt reference: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return &compliancev1.ComplianceEvidenceReference{
		ArtifactId:    traceEvidence.GetArtifactId(),
		ArtifactType:  string(complianceevidence.ArtifactTypeEvalReceipt),
		SchemaRef:     agentReceiptSchemaRef,
		MediaType:     constants.MediaTypeJSON,
		Sha256:        hex.EncodeToString(digest[:]),
		TransactionId: transactionID,
		RunId:         trace.RunID,
		AttemptId:     trace.EvaluationAttemptID,
		ScenarioId:    trace.ScenarioID,
	}, nil
}

// agentToolGovernedAction binds one tool dispatch's signed receipt as a
// governed action on the roster member it targeted. A refused call has no receipt and binds nothing.
func agentToolGovernedAction(traceEvidence *compliancev1.ComplianceEvidenceReference, trace *AgentTrace, assignmentID string, call AgentToolCall) (*evalv1.GovernedActionBinding, error) {
	if call.Receipt == nil {
		return nil, nil
	}
	member, err := trace.TargetMember(call)
	if err != nil {
		return nil, err
	}
	ref, err := agentReceiptEvidenceReference(traceEvidence, trace, call.TransactionID, call.Receipt)
	if err != nil {
		return nil, err
	}
	return &evalv1.GovernedActionBinding{
		BindingId:         call.CallID,
		AssignmentId:      assignmentID,
		TransactionId:     call.TransactionID,
		OperatorId:        member.Runtime.OperatorID,
		OperatorSessionId: member.Runtime.SessionID,
		ReceiptRef:        ref,
		PolicyDecision:    call.PolicyDecision,
		ActionType:        call.Receipt.GetActionType(),
	}, nil
}

// agentEvidenceStage is the part of an agent trace evidence verification failed
// on. Its value is the fixed public sentence an INVALID_EVIDENCE result carries:
// the detail names call identifiers and stays local (INV-STOR-EGRESS-01).
type agentEvidenceStage string

const (
	agentEvidenceStageRuntime     agentEvidenceStage = "The trace's runtime identity could not be verified."
	agentEvidenceStageModelCall   agentEvidenceStage = "A model call's signed evidence does not match the trace."
	agentEvidenceStageToolCall    agentEvidenceStage = "A tool call's signed evidence does not match the trace."
	agentEvidenceStageObservation agentEvidenceStage = "A harness check's signed evidence does not match the trace."
	agentEvidenceStageLedger      agentEvidenceStage = "The Operator ledger does not bracket the recorded activity."

	invalidEvidenceReasonModelBinding = "A model call is not bound to the frozen campaign models."
	invalidEvidenceReasonUnclassified = "The agent trace failed evidence verification."
)

// agentEvidenceStageError carries the stage of one evidence failure.
type agentEvidenceStageError struct {
	stage agentEvidenceStage
	err   error
}

func (e *agentEvidenceStageError) Error() string { return e.err.Error() }

func (e *agentEvidenceStageError) Unwrap() error { return e.err }

func agentEvidenceError(stage agentEvidenceStage, subject string, cause error) error {
	return &agentEvidenceStageError{stage: stage, err: fmt.Errorf("%w: %s: %w", constants.ErrCampaignToolEvidenceInvalid, subject, cause)}
}

// invalidEvidenceReason is the fixed public sentence for an INVALID_EVIDENCE
// verdict. The import records it and the verifier recomputes it.
func invalidEvidenceReason(err error) string {
	var staged *agentEvidenceStageError
	switch {
	case errors.As(err, &staged):
		return string(staged.stage)
	case errors.Is(err, constants.ErrFormationRegistryBinding):
		return invalidEvidenceReasonModelBinding
	default:
		return invalidEvidenceReasonUnclassified
	}
}
