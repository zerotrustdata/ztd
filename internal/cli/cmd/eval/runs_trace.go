// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// traceClipLength bounds one model-input message or tool result in the text
// view without --full. Thinking, answers, and the final output always print in
// full: they are what the model produced.
const traceClipLength = 240

// runTraceJSON is one assignment's sealed agent trace, decoded and
// digest-checked, beside the assignment fields that locate it.
type runTraceJSON struct {
	AssignmentID string                 `json:"assignment_id"`
	ScenarioID   string                 `json:"scenario_id"`
	Target       string                 `json:"target"`
	Repetition   uint32                 `json:"repetition"`
	Trace        *evaluation.AgentTrace `json:"trace"`
}

type runTracesJSON struct {
	RunID  string         `json:"run_id"`
	Traces []runTraceJSON `json:"traces"`
}

// runTraceView is one assignment with the agent trace it sealed.
type runTraceView struct {
	assignment *evalv1.EvaluationAssignment
	trace      *evaluation.AgentTrace
}

// selectTraceAssignments resolves the named assignments, in the order given,
// or every assignment of --scenario ordered by target and repetition.
func selectTraceAssignments(views []runAssignmentView, assignmentIDs []string, scenarioID string) ([]*evalv1.EvaluationAssignment, error) {
	if len(assignmentIDs) == 0 && scenarioID == "" {
		return nil, fmt.Errorf("evaluation: runs trace: name assignments or --scenario: %w", constants.ErrEvaluationFlagsInvalid)
	}
	byID := make(map[string]*evalv1.EvaluationAssignment, len(views))
	var selected []*evalv1.EvaluationAssignment
	for _, view := range views {
		byID[view.assignment.GetAssignmentId()] = view.assignment
		if scenarioID != "" && view.assignment.GetScenarioId() == scenarioID {
			selected = append(selected, view.assignment)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if ti, tj := assignmentTarget(selected[i]), assignmentTarget(selected[j]); ti != tj {
			return ti < tj
		}
		return selected[i].GetRepetition() < selected[j].GetRepetition()
	})
	for _, id := range assignmentIDs {
		assignment, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("evaluation: runs trace: assignment %q is not in this run: %w", id, constants.ErrNotFound)
		}
		selected = append(selected, assignment)
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("evaluation: runs trace: scenario %q has no assignments in this run: %w", scenarioID, constants.ErrNotFound)
	}
	return selected, nil
}

// loadRunTraces reads each selected assignment's sealed trace. With --scenario
// an assignment that has not sealed one yet (queued or running) is skipped; a
// named assignment without a trace is an error.
func loadRunTraces(cmd *cobra.Command, store *evaluation.Store, runID string, assignments []*evalv1.EvaluationAssignment, skipMissing bool) ([]runTraceView, error) {
	traces := make([]runTraceView, 0, len(assignments))
	for _, assignment := range assignments {
		trace, err := store.LoadAssignmentAgentTrace(cmd.Context(), runID, assignment.GetAssignmentId())
		if errors.Is(err, constants.ErrNotFound) && skipMissing {
			continue
		}
		if errors.Is(err, constants.ErrNotFound) {
			return nil, fmt.Errorf("evaluation: runs trace: assignment %s sealed no agent trace (not yet run, or not a model-role assignment): %w", assignment.GetAssignmentId(), err)
		}
		if err != nil {
			return nil, fmt.Errorf("evaluation: runs trace: assignment %s: %w", assignment.GetAssignmentId(), err)
		}
		traces = append(traces, runTraceView{assignment: assignment, trace: trace})
	}
	if len(traces) == 0 {
		return nil, fmt.Errorf("evaluation: runs trace: no selected assignment has sealed an agent trace yet: %w", constants.ErrNotFound)
	}
	return traces, nil
}

// traceRole renders an InferenceMessageRole enum name as its short role.
func traceRole(role string) string {
	return strings.ToLower(strings.TrimPrefix(role, "INFERENCE_MESSAGE_ROLE_"))
}

func traceClip(text string, full bool) string {
	if full || len(text) <= traceClipLength {
		return text
	}
	return strings.ReplaceAll(text[:traceClipLength], "\n", " ") + fmt.Sprintf("… (%d chars, --full shows all)", len(text))
}

func writeTraceBlock(out io.Writer, label, text string) {
	_, _ = fmt.Fprintf(out, "  %s:\n", label)
	if text == "" {
		_, _ = fmt.Fprintln(out, "    (none)")
		return
	}
	for _, line := range strings.Split(text, "\n") {
		_, _ = fmt.Fprintf(out, "    %s\n", line)
	}
}

// writeRunTrace prints what the model received and produced for one
// assignment: the held conversation it continued, its own input, and per
// governed inference the token counts, thinking, answer, and tool calls.
func writeRunTrace(out io.Writer, view runTraceView, full bool) {
	trace := view.trace
	_, _ = fmt.Fprintf(out, "=== %s  %s  %s  rep %d\n", view.assignment.GetAssignmentId(), view.assignment.GetScenarioId(), assignmentTarget(view.assignment), view.assignment.GetRepetition())
	_, _ = fmt.Fprintf(out, "Model: %s (%s)  Trace: %s  Digest: %s\n", trace.ServedModelTag, trace.ModelDigest, trace.SchemaVersion, trace.EvidenceDigest)
	if held := trace.HeldContext; held != nil {
		_, _ = fmt.Fprintf(out, "Held context: repetition %d (%s), %d messages\n", held.Repetition, held.AssignmentID, len(held.Messages))
		for i, message := range held.Messages {
			_, _ = fmt.Fprintf(out, "  [%d] %s: %s\n", i, traceRole(message.Role), traceClip(message.Text, full))
			for _, call := range message.ToolCalls {
				_, _ = fmt.Fprintf(out, "      tool call %s %s\n", call.Name, traceClip(call.ArgumentsJSON, full))
			}
			if result := message.ToolResult; result != nil {
				_, _ = fmt.Fprintf(out, "      tool result %s %s\n", result.Name, traceClip(result.ResultJSON, full))
			}
		}
	} else {
		_, _ = fmt.Fprintln(out, "Held context: none")
	}
	if trace.Input != nil {
		_, _ = fmt.Fprintf(out, "Input: %d messages, %d tools declared\n", len(trace.Input.Messages), len(trace.Input.Tools))
		for i, message := range trace.Input.Messages {
			_, _ = fmt.Fprintf(out, "  [%d] %s: %s\n", i, traceRole(message.Role), traceClip(message.Text, full))
		}
	}
	for _, call := range trace.ModelCalls {
		if !call.IsTurn() {
			continue
		}
		_, _ = fmt.Fprintf(out, "Turn %d  %s  prompt=%d completion=%d finish=%s tool_calls=%d\n", call.LoopTurn, call.ProviderAttemptID, call.PromptTokens, call.CompletionTokens, call.FinishReason, call.ToolCallCount)
		if call.Error != "" {
			_, _ = fmt.Fprintf(out, "  Error: %s\n", call.Error)
		}
		writeTraceBlock(out, "Thinking", call.Thinking)
		writeTraceBlock(out, "Text", call.Text)
		for _, tool := range trace.ToolCalls {
			if tool.ModelCallID != call.ProviderAttemptID {
				continue
			}
			writeTribunalCalls(out, trace, tool.CallID)
			status := "ok"
			if !tool.Success {
				status = "failed " + tool.ErrorType
			}
			_, _ = fmt.Fprintf(out, "  Tool %s %s -> %s\n", tool.ToolName, traceClip(tool.ArgumentsJSON, full), status)
			_, _ = fmt.Fprintf(out, "    result: %s\n", traceClip(tool.ResultJSON, full))
		}
	}
	if trace.ProviderFailure != "" {
		_, _ = fmt.Fprintf(out, "Provider failure: %s %s\n", trace.ProviderFailure, trace.ProviderFailureClass)
	}
	if trace.ToolTurnLimitReached {
		_, _ = fmt.Fprintln(out, "Tool turn limit reached")
	}
	writeTraceBlock(out, "Final output", trace.FinalOutput)
	_, _ = fmt.Fprintln(out)
}

// writeTribunalCalls prints the Tribunal inferences one intent-only
// run_commands_with_operator call dispatched before its command ran.
func writeTribunalCalls(out io.Writer, trace *evaluation.AgentTrace, toolCallID string) {
	for _, call := range trace.ModelCalls {
		if call.IsTurn() || call.ToolCallID != toolCallID {
			continue
		}
		_, _ = fmt.Fprintf(out, "  Tribunal %s %s  %s  prompt=%d completion=%d\n", call.Purpose, call.Persona, call.ProviderAttemptID, call.PromptTokens, call.CompletionTokens)
		if call.Error != "" {
			_, _ = fmt.Fprintf(out, "    Error: %s\n", call.Error)
		}
		writeTraceBlock(out, "  Thinking", call.Thinking)
		writeTraceBlock(out, "  Text", call.Text)
	}
}

// writeTraceComparison tabulates the listed traces so repetitions of one cell
// compare at a glance: token counts, thinking and answer length, the final
// output's digest, and the earliest listed repetition of the same cell whose
// final output was byte-identical.
func writeTraceComparison(out io.Writer, views []runTraceView) error {
	type cellOutput struct{ cell, digest string }
	first := make(map[cellOutput]uint32)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ASSIGNMENT\tSCENARIO\tTARGET\tREP\tHELD\tCALLS\tTOOLS\tPROMPT\tCOMPLETION\tTHINKING\tFINAL\tFINAL_SHA256\tSAME_AS_REP")
	for _, view := range views {
		trace := view.trace
		var prompt, completion uint32
		thinking := 0
		for _, call := range trace.ModelCalls {
			prompt += call.PromptTokens
			completion += call.CompletionTokens
			thinking += len(call.Thinking)
		}
		held := "-"
		if trace.HeldContext != nil {
			held = fmt.Sprintf("%d msgs", len(trace.HeldContext.Messages))
		}
		digest := models.SHA256Hex([]byte(trace.FinalOutput))
		key := cellOutput{cell: view.assignment.GetScenarioId() + "|" + assignmentTarget(view.assignment), digest: digest}
		same := "-"
		if rep, ok := first[key]; ok {
			same = fmt.Sprintf("%d", rep)
		} else {
			first[key] = view.assignment.GetRepetition()
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n",
			view.assignment.GetAssignmentId(), view.assignment.GetScenarioId(), assignmentTarget(view.assignment), view.assignment.GetRepetition(),
			held, len(trace.ModelCalls), len(trace.ToolCalls), prompt, completion, thinking, len(trace.FinalOutput), digest[:12], same)
	}
	return w.Flush()
}

func runsTraceCmd(deps nativeEvalDeps) *cobra.Command {
	var (
		scenarioID  string
		full        bool
		summaryOnly bool
	)
	cmd := &cobra.Command{
		Use:   "trace <run> [assignment...]",
		Short: "Show what the model received and produced for assignments of a run",
		Long: `Read the sealed, digest-checked agent trace of model-role assignments and show
exactly what the model saw and did: the held context a repetition continued
(with --hold-context), the input it was sent, and per governed inference the
prompt and completion tokens, thinking, answer text, and every tool call with
its arguments and result, then the final output.

Name assignments, or pass --scenario to select every assignment of that
scenario in the run (ordered by target and repetition; assignments that have
not sealed a trace yet are skipped). With more than one trace a comparison
table follows: SAME_AS_REP names the earliest listed repetition of the same
cell whose final output was byte-identical.

Model-input messages and tool payloads are clipped unless --full; thinking,
answers, and the final output always print in full. --summary prints only the
comparison table. --json emits each decoded trace beside its assignment.`,
		Example: `  ztd eval runs trace <run-id> --scenario final-response-diagnosis
  ztd eval runs trace <run-id> --scenario final-response-diagnosis --summary
  ztd eval runs trace <run-id> <assignment-id> --full`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID := args[0]
			_, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			store, _, err := evaluation.LocateRun(cmd.Context(), fileSvc, runID)
			if err != nil {
				return fmt.Errorf("evaluation: runs trace: %w", err)
			}
			views, err := loadRunAssignmentViews(cmd, store, runID)
			if err != nil {
				return fmt.Errorf("evaluation: runs trace: %w", err)
			}
			selected, err := selectTraceAssignments(views, args[1:], scenarioID)
			if err != nil {
				return err
			}
			traces, err := loadRunTraces(cmd, store, runID, selected, len(args) == 1)
			if err != nil {
				return err
			}
			if output.JSONEnabled(cmd) {
				payload := runTracesJSON{RunID: runID, Traces: make([]runTraceJSON, 0, len(traces))}
				for _, view := range traces {
					payload.Traces = append(payload.Traces, runTraceJSON{
						AssignmentID: view.assignment.GetAssignmentId(),
						ScenarioID:   view.assignment.GetScenarioId(),
						Target:       assignmentTarget(view.assignment),
						Repetition:   view.assignment.GetRepetition(),
						Trace:        view.trace,
					})
				}
				return output.WriteJSON(cmd.OutOrStdout(), payload)
			}
			out := cmd.OutOrStdout()
			if !summaryOnly {
				for _, view := range traces {
					writeRunTrace(out, view, full)
				}
			}
			if summaryOnly || len(traces) > 1 {
				return writeTraceComparison(out, traces)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scenarioID, "scenario", "", "Every assignment of this scenario in the run")
	cmd.Flags().BoolVar(&full, "full", false, "Print model-input messages and tool payloads unclipped")
	cmd.Flags().BoolVar(&summaryOnly, "summary", false, "Print only the comparison table")
	return cmd
}
