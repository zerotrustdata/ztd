// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

// Shared fixtures for the harness roster. A test that ran on one Data
// Operator before the roster existed runs on a roster of one.

// rosterOfOne is a one-member roster on runtime with workspace; tests set
// its bracket when they need one.
func rosterOfOne(runtime AgentTraceRuntime, workspace AgentTraceWorkspace) []AgentTraceRosterMember {
	return []AgentTraceRosterMember{{Index: 1, Runtime: runtime, Workspace: workspace}}
}

// testDataMember is one fleet member with a working directory.
func testDataMember(operatorID, sessionID, workingDirectory string) CampaignDataOperator {
	return CampaignDataOperator{
		CampaignOperatorRuntime: CampaignOperatorRuntime{OperatorID: operatorID, OperatorSessionID: sessionID},
		WorkingDirectory:        workingDirectory,
	}
}

// fleetOf is a topology with one Inference runtime and the given fleet.
func fleetOf(inference CampaignOperatorRuntime, fleet ...CampaignDataOperator) CampaignOperatorTopology {
	return CampaignOperatorTopology{Inference: inference, Fleet: fleet}
}

// firstMember returns the trace's first roster member, for tests on a roster
// of one.
func firstMember(trace *AgentTrace) *AgentTraceRosterMember {
	return &trace.Roster[0]
}

// onTarget returns call recorded as a group of one on roster member [1].
func onTarget(call AgentToolCall) AgentToolCall {
	if call.GroupID == "" {
		call.GroupID = call.CallID
	}
	call.TargetIndex = 1
	return call
}

// withRosterPrompt appends the roster block rendered from the trace's own
// roster to its system message, as the executor renders it whenever tools
// are declared, so a hand-built current-version trace verifies.
func withRosterPrompt(trace *AgentTrace) {
	if trace.Input != nil && len(trace.Input.Messages) > 0 {
		trace.Input.Messages[0].Text += renderRosterPrompt(trace.Roster)
	}
}
