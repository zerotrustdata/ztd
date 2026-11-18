// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
)

// The harness roster is the frozen, ordered set of Data Operators one run
// addresses. The model reads it in its system prompt and names the targets of
// each tool call with target_operators; the harness resolves the selectors
// against the frozen roster and fans the call out as one governed action per
// target, so each target runs its own L1-L5 pass and signs its own receipt.
// Resolution happens in one place (resolveRosterTargets) that both the
// executor and the evidence verifier call, so a recorded fan-out is always
// recomputed from the signed model request.
const (
	// harnessTargetOperatorsArgument is the harness-owned argument every
	// Operator tool declares. It never reaches an Operator.
	harnessTargetOperatorsArgument = "target_operators"
	// harnessTargetAll selects every roster member.
	harnessTargetAll = "all"
)

// harnessTargetOperatorsProperty is the declared schema of target_operators.
var harnessTargetOperatorsProperty = &mcp.PropertySchema{
	Type: "array",
	Description: `Operators to run this call on, from the operator roster in your instructions. Each entry is a roster index ("1"), an Operator ID, or a hostname; ["all"] runs it on every roster Operator. ` +
		`The call runs once per target, in parallel, and returns one result per target. Name only the Operators the request covers. Required when the roster holds more than one Operator.`,
}

// newAgentTraceRoster records the topology's fleet as the trace roster, with
// each member's attempt workspace under its own working directory.
func newAgentTraceRoster(topology CampaignOperatorTopology, runID, attemptID string) ([]AgentTraceRosterMember, error) {
	if len(topology.Fleet) == 0 {
		return nil, fmt.Errorf("evaluation: harness roster: %w", constants.ErrHarnessRosterEmpty)
	}
	roster := make([]AgentTraceRosterMember, 0, len(topology.Fleet))
	for i, member := range topology.Fleet {
		workspace, err := NewScenarioWorkspace(member.WorkingDirectory, runID, attemptID)
		if err != nil {
			return nil, fmt.Errorf("evaluation: harness roster [%d] %s: %w", i+1, member.OperatorID, err)
		}
		roster = append(roster, AgentTraceRosterMember{
			Index:        i + 1,
			Runtime:      agentTraceRuntime(member.CampaignOperatorRuntime),
			Hostname:     member.Hostname,
			OS:           member.OS,
			Architecture: member.Architecture,
			Workspace:    AgentTraceWorkspace(workspace),
		})
	}
	return roster, nil
}

// renderRosterPrompt renders the operator roster block of the system prompt
// from the frozen roster alone, so the verifier can re-render it.
func renderRosterPrompt(roster []AgentTraceRosterMember) string {
	var b strings.Builder
	b.WriteString("<operator_roster>\n")
	fmt.Fprintf(&b, "Operators: %d bound.\n", len(roster))
	for _, member := range roster {
		fmt.Fprintf(&b, "[%d] operator_id=%s", member.Index, member.Runtime.OperatorID)
		for _, field := range []struct{ name, value string }{
			{"hostname", member.Hostname}, {"os", member.OS}, {"arch", member.Architecture},
		} {
			if field.value != "" {
				fmt.Fprintf(&b, " %s=%s", field.name, field.value)
			}
		}
		fmt.Fprintf(&b, " workspace=%s\n", member.Workspace.Root)
	}
	if len(roster) == 1 {
		b.WriteString("Every tool call runs on [1]; target_operators may be omitted.\n")
	} else {
		b.WriteString(`Every tool call must name its targets in target_operators: roster indexes, Operator IDs, or hostnames, or ["all"]. The call runs once on each target and returns one result per target. Target only the Operators the request covers.` + "\n")
	}
	b.WriteString("Each Operator holds this investigation's files in its own workspace. Relative paths, and the working directory of run_shell_command, resolve inside the workspace of the Operator that runs the call, so one relative path names the same file on every target.\n")
	b.WriteString("</operator_roster>\n")
	return b.String()
}

// splitTargetOperators removes target_operators from raw tool arguments. It
// returns the remaining arguments, the selectors, and whether the argument was
// present. An explicit null is absent. problem is the model-visible reason a
// present value is not an array of non-empty strings. Arguments that are not
// exactly one JSON object are returned unchanged for the contract decoder
// to reject.
func splitTargetOperators(raw string) (rest string, selectors []string, present bool, problem string) {
	if strings.TrimSpace(raw) == "" {
		return raw, nil, false, ""
	}
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return raw, nil, false, ""
	}
	value, ok := values[harnessTargetOperatorsArgument]
	if !ok {
		return raw, nil, false, ""
	}
	delete(values, harnessTargetOperatorsArgument)
	encoded, err := json.Marshal(values)
	if err != nil {
		return raw, nil, false, ""
	}
	if isJSONNull(value) {
		return string(encoded), nil, false, ""
	}
	strict := json.NewDecoder(bytes.NewReader(value))
	if err := strict.Decode(&selectors); err != nil || len(selectors) == 0 || slices.Contains(selectors, "") {
		return string(encoded), nil, true, constants.ErrHarnessTargetsInvalid.Error()
	}
	return string(encoded), selectors, true, ""
}

// resolveRosterTargets resolves model selectors to 1-based roster indexes in
// roster order, without duplicates. Absent selectors resolve to the only member
// of a roster of one. An unknown selector, a hostname two members share, or a
// missing selector on a larger roster fails closed: the harness never falls
// back to a default Operator.
func resolveRosterTargets(roster []AgentTraceRosterMember, selectors []string, present bool) ([]int, error) {
	if len(roster) == 0 {
		return nil, constants.ErrHarnessRosterEmpty
	}
	if !present {
		if len(roster) == 1 {
			return []int{1}, nil
		}
		return nil, fmt.Errorf("%w (roster holds %d Operators)", constants.ErrHarnessTargetRequired, len(roster))
	}
	chosen := make(map[int]bool, len(roster))
	for _, selector := range selectors {
		index, err := resolveRosterSelector(roster, selector)
		if err != nil {
			return nil, err
		}
		if index == 0 {
			for _, member := range roster {
				chosen[member.Index] = true
			}
			continue
		}
		chosen[index] = true
	}
	targets := make([]int, 0, len(chosen))
	for _, member := range roster {
		if chosen[member.Index] {
			targets = append(targets, member.Index)
		}
	}
	return targets, nil
}

// resolveRosterSelector resolves one selector to a roster index, or to zero
// for "all". A selector is tried as "all", then a roster index, then an
// Operator ID, then a hostname.
func resolveRosterSelector(roster []AgentTraceRosterMember, selector string) (int, error) {
	trimmed := strings.TrimSpace(selector)
	if strings.EqualFold(trimmed, harnessTargetAll) {
		return 0, nil
	}
	if index, err := strconv.Atoi(trimmed); err == nil {
		if index < 1 || index > len(roster) {
			return 0, fmt.Errorf("%w: index %q; the roster holds [1] to [%d]", constants.ErrHarnessTargetUnknown, selector, len(roster))
		}
		return index, nil
	}
	for _, member := range roster {
		if member.Runtime.OperatorID == trimmed {
			return member.Index, nil
		}
	}
	match := 0
	for _, member := range roster {
		if member.Hostname != "" && strings.EqualFold(member.Hostname, trimmed) {
			if match != 0 {
				return 0, fmt.Errorf("%w: hostname %q; name the roster index or Operator ID instead", constants.ErrHarnessTargetAmbiguous, selector)
			}
			match = member.Index
		}
	}
	if match == 0 {
		return 0, fmt.Errorf("%w: %q", constants.ErrHarnessTargetUnknown, selector)
	}
	return match, nil
}

// targetResolutionFailure is the model-visible rejection of a call whose
// targets do not resolve. No governed action is dispatched for it.
func targetResolutionFailure(tool string, err error) *agentToolFailure {
	return agentToolValidationFailure(tool, []string{harnessTargetOperatorsArgument + "\n  " + err.Error()})
}

// fanOutTargetResult is one target's entry in a fanned-out call's result.
type fanOutTargetResult struct {
	RosterIndex int             `json:"roster_index"`
	OperatorID  string          `json:"operator_id"`
	Hostname    string          `json:"hostname,omitempty"`
	Result      json.RawMessage `json:"result"`
}

// fanOutResult is the one tool result a fanned-out call returns to the model.
type fanOutResult struct {
	Targets []fanOutTargetResult `json:"targets"`
}

// fanOutResultJSON is the tool result the model receives for one group of
// dispatches: a single dispatch's own result, or one entry per target in
// roster order. The executor sends it and the verifier rebuilds the
// conversation with it, so the model-visible form has one owner.
func fanOutResultJSON(roster []AgentTraceRosterMember, group []AgentToolCall) (string, error) {
	if len(group) == 0 {
		return "", fmt.Errorf("evaluation: fan-out result: %w", constants.ErrMissingRequiredField)
	}
	if len(group) == 1 {
		return group[0].ResultJSON, nil
	}
	result := fanOutResult{Targets: make([]fanOutTargetResult, 0, len(group))}
	for _, call := range group {
		if call.TargetIndex < 1 || call.TargetIndex > len(roster) {
			return "", fmt.Errorf("evaluation: fan-out result: target [%d]: %w", call.TargetIndex, constants.ErrHarnessFanOutMismatch)
		}
		member := roster[call.TargetIndex-1]
		result.Targets = append(result.Targets, fanOutTargetResult{
			RosterIndex: member.Index, OperatorID: member.Runtime.OperatorID, Hostname: member.Hostname, Result: json.RawMessage(call.ResultJSON),
		})
	}
	return canonicalToolJSON(result)
}

// toolCallGroups splits recorded tool calls into their fan-out groups, in
// trace order. Each group is one model tool call.
func toolCallGroups(calls []AgentToolCall) [][]AgentToolCall {
	var groups [][]AgentToolCall
	for _, call := range calls {
		if n := len(groups); n > 0 && call.GroupID != "" && groups[n-1][0].GroupID == call.GroupID {
			groups[n-1] = append(groups[n-1], call)
			continue
		}
		groups = append(groups, []AgentToolCall{call})
	}
	return groups
}

// harnessDispatchArguments returns the arguments the harness dispatches for
// one recorded tool call on one roster member. The executor dispatches them
// and the verifier recomputes them, so the per-target form has one owner:
//   - run_shell_command runs in the member's workspace root, or a directory
//     inside it the model named; a working directory outside it is a security
//     failure without a governed action;
//   - event_receipt_list keeps its investigation scope and harness defaults;
//   - a confined path resolves inside the member's workspace and is sent as
//     its absolute path there (agentToolArguments.ForWorkspace).
//
// argumentsJSON is the recorded form (AgentToolCall.ArgumentsJSON, or the
// Tribunal-approved arguments of a reviewed shell command).
func harnessDispatchArguments(spec agentToolSpec, argumentsJSON string, member AgentTraceRosterMember, investigationID string) (string, *agentToolFailure, error) {
	request := decodeAgentToolRequest(spec, true, argumentsJSON)
	if request.Failure != nil {
		return "", request.Failure, nil
	}
	workspace := member.ScenarioWorkspace()
	if workspace.Root == "" {
		return "", nil, fmt.Errorf("evaluation: roster member [%d] workspace: %w", member.Index, constants.ErrEvaluationWorkspaceUnavailable)
	}
	if request.Shell != nil {
		shell := *request.Shell
		workingDir, failure := memberShellWorkingDir(workspace, shell.WorkingDir)
		if failure != nil {
			return "", failure, nil
		}
		shell.WorkingDir = workingDir
		return shell.CanonicalJSON(), nil, nil
	}
	args := request.Arguments
	if spec.Name == constants.MCPToolEventReceiptList {
		if failure := args.EventReceiptListScopeFailure(investigationID); failure != nil {
			return "", failure, nil
		}
		// The model cannot turn the audit-read exclusion off: a context query
		// must never return the earlier audit reads of its own investigation.
		args.SetEventReceiptListDefaults(investigationID)
	}
	args, failure, err := args.ForWorkspace(workspace)
	if err != nil || failure != nil {
		return "", failure, err
	}
	canonical, err := args.CanonicalJSON()
	if err != nil {
		return "", nil, fmt.Errorf("evaluation: encode %s arguments: %w", spec.Name, err)
	}
	return canonical, nil, nil
}

// memberShellWorkingDir is the directory a run_shell_command runs in on one
// roster member: its attempt workspace, or a directory within it the model
// named. It is never the Operator directory, which holds every other
// attempt's workspace, so a model cannot list or read another attempt's
// fixtures through it.
func memberShellWorkingDir(workspace ScenarioWorkspace, requested string) (string, *agentToolFailure) {
	if requested == "" {
		return workspace.Root, nil
	}
	resolved := workspace.Resolve(requested)
	if !pathWithin(resolved, workspace.Root) {
		return "", &agentToolFailure{
			ErrorType: agentToolErrorSecurity,
			Error:     fmt.Sprintf("security violation: working directory %q is outside the investigation workspace %s", requested, workspace.Root),
		}
	}
	return resolved, nil
}

// memberTarget is the dispatch target of one roster member.
func memberTarget(member AgentTraceRosterMember) Target {
	return Target{OperatorID: member.Runtime.OperatorID, SessionID: member.Runtime.SessionID}
}

// workspaceFilesFor returns the frozen workspace files one roster member
// holds: every file whose Operators list is empty or names the member.
func workspaceFilesFor(files []ScenarioWorkspaceFile, index int) []ScenarioWorkspaceFile {
	var held []ScenarioWorkspaceFile
	for _, file := range files {
		if len(file.Operators) == 0 || slices.Contains(file.Operators, index) {
			held = append(held, file)
		}
	}
	return held
}

// heldRoster returns the roster a held-context repetition continues on: the
// previous repetition's members and workspaces, in order, bound to this run's
// sessions. A fleet whose members or working directories changed cannot see
// the held workspaces, so that is a harness failure.
func heldRoster(previous *AgentTrace, topology CampaignOperatorTopology) ([]AgentTraceRosterMember, error) {
	unavailable := func(err error) error {
		return assignmentExecutionError("evaluation: held context", fmt.Errorf("%w: %w", constants.ErrEvaluationHeldContextUnavailable, err))
	}
	if len(previous.Roster) != len(topology.Fleet) {
		return nil, unavailable(fmt.Errorf("repetition %s ran on %d Operators, this repetition is bound to %d: %w", previous.AssignmentID, len(previous.Roster), len(topology.Fleet), constants.ErrHarnessRosterMismatch))
	}
	roster := make([]AgentTraceRosterMember, 0, len(previous.Roster))
	for i, held := range previous.Roster {
		member := topology.Fleet[i]
		workingDirectory, err := cleanOperatorWorkingDirectory(member.WorkingDirectory)
		if err != nil {
			return nil, err
		}
		workspace := held.ScenarioWorkspace()
		if held.Runtime.OperatorID != member.OperatorID || workspace.OperatorWorkingDirectory != workingDirectory {
			return nil, unavailable(fmt.Errorf("repetition %s ran [%d] on Data Operator %s in %q, this repetition is bound to %s in %q",
				previous.AssignmentID, held.Index, held.Runtime.OperatorID, workspace.OperatorWorkingDirectory, member.OperatorID, workingDirectory))
		}
		if err := workspace.ValidateShape(); err != nil {
			return nil, unavailable(err)
		}
		roster = append(roster, AgentTraceRosterMember{
			Index: held.Index, Runtime: agentTraceRuntime(member.CampaignOperatorRuntime),
			Hostname: member.Hostname, OS: member.OS, Architecture: member.Architecture, Workspace: held.Workspace,
		})
	}
	return roster, nil
}

var (
	harnessCatalogOnce sync.Once
	harnessCatalog     *agentToolCatalog
	harnessCatalogErr  error
)

// catalogToolSpec returns the production native tool spec the harness
// declares for name. Verification recomputes dispatch arguments with it.
func catalogToolSpec(name string) (agentToolSpec, error) {
	harnessCatalogOnce.Do(func() { harnessCatalog, harnessCatalogErr = newAgentToolCatalog() })
	if harnessCatalogErr != nil {
		return agentToolSpec{}, harnessCatalogErr
	}
	spec, ok := harnessCatalog.Tool(name)
	if !ok {
		return agentToolSpec{}, fmt.Errorf("evaluation: tool %q: %w", name, constants.ErrMCPNativeToolUnknown)
	}
	return spec, nil
}

// verifyFanOutGroup proves one recorded model tool call against the signed
// turn that requested it. The group's first record must match a requested
// call exactly; every record must share its identity and arguments; and the
// records' targets must be exactly the targets the signed request resolves
// to against the frozen roster, in roster order. A request that fails its
// contract or its target resolution is one record with no target.
func verifyFanOutGroup(trace *AgentTrace, turns map[string]*AgentModelCall, specs map[string]agentToolSpec, group []AgentToolCall) error {
	mismatch := func(reason string) error {
		return fmt.Errorf("tool call group %s %s: %w", group[0].GroupID, reason, constants.ErrHarnessFanOutMismatch)
	}
	lead := group[0]
	if lead.GroupID == "" || lead.GroupID != lead.CallID {
		return mismatch("does not start at its group's first dispatch")
	}
	turn := turns[lead.ModelCallID]
	if turn == nil {
		return fmt.Errorf("tool call has no signed agent turn: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	_, _, calls := agentResponseParts(turn.InferenceResult.InferenceResult)
	var request *agentToolRequest
	declared := false
	for _, requested := range calls {
		spec, ok := specs[requested.GetName()]
		decoded := decodeAgentToolRequest(spec, ok, requested.GetArgumentsJson())
		if requested.GetCallId() == lead.ProviderCallID && requested.GetProviderSignature() == lead.ProviderSignature && requested.GetName() == lead.ToolName && decoded.ArgumentsJSON == lead.ArgumentsJSON {
			request, declared = &decoded, ok
			break
		}
	}
	if request == nil {
		return fmt.Errorf("tool call differs from the signed model request: %w", constants.ErrEvaluationEvidenceTraceMismatch)
	}
	if !slices.Equal(lead.TargetOperators, request.TargetSelectors) {
		return mismatch("records other target selectors than the signed request")
	}
	var targets []int
	if declared && request.Failure == nil {
		targets, _ = resolveRosterTargets(trace.Roster, request.TargetSelectors, request.TargetsPresent)
	}
	if len(targets) == 0 {
		if len(group) != 1 || lead.TargetIndex != 0 {
			return mismatch("dispatched a request that resolves to no target")
		}
		return nil
	}
	if len(group) != len(targets) {
		return mismatch(fmt.Sprintf("dispatched %d targets, the signed request resolves to %d", len(group), len(targets)))
	}
	for i, call := range group {
		if call.TargetIndex != targets[i] {
			return mismatch(fmt.Sprintf("dispatch %d ran on [%d], the signed request resolves it to [%d]", i+1, call.TargetIndex, targets[i]))
		}
		if call.GroupID != lead.GroupID || call.ModelCallID != lead.ModelCallID || call.ProviderCallID != lead.ProviderCallID ||
			call.ProviderSignature != lead.ProviderSignature || call.ToolName != lead.ToolName || call.ArgumentsJSON != lead.ArgumentsJSON ||
			call.LoopTurn != lead.LoopTurn || !slices.Equal(call.TargetOperators, lead.TargetOperators) {
			return mismatch(fmt.Sprintf("dispatch %d differs from the group's call", i+1))
		}
	}
	return nil
}
