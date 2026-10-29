// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestParseFinalDiagnosisSections_UnicodeSummaryHeadings(t *testing.T) {
	for _, dash := range []string{"-", "‑", "–", "—"} {
		t.Run(dash, func(t *testing.T) {
			output := "**Diagnosis**\nObserved timeouts.\n**Confidence**\nModerate.\n**Recommended action**\nInspect telemetry.\n**Customer" + dash + "safe summary**\nCustomers cannot complete checkout."
			sections := parseFinalDiagnosisSections(output)
			assert.Equal(t, "Customers cannot complete checkout.", sections["summary"])
			assert.Equal(t, "Inspect telemetry.", sections["action"])
		})
	}
	assert.Empty(t, parseFinalDiagnosisSections("Customer-safe summaryish: not a section")["summary"])
}

func TestGradeHomogeneousScenario_ErrorGuidedRetryUsesExecutedCorrection(t *testing.T) {
	blueprint := recoveryErrorGuidedRetry()
	blueprint.Input.ScenarioID = blueprint.ScenarioID
	workspace, err := NewScenarioWorkspace(agentTestWorkingDirectory, "run-retry", "attempt-retry")
	require.NoError(t, err)
	arguments, err := json.Marshal(struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
	}{Path: workspace.Root, Pattern: "AUTH_FAILURE"})
	require.NoError(t, err)
	resultBody, err := json.Marshal(mcp.SearchDirectoryResult{Matches: []mcp.SearchMatch{
		{Path: workspace.Render(ScenarioWorkspaceToken + "/logs/auth.log"), LineNumber: 1, Content: "2026-09-16T08:00:01Z AUTH_FAILURE user=svc-deploy"},
	}, TotalMatches: 1})
	require.NoError(t, err)
	corrected := AgentToolCall{ToolName: constants.MCPToolSearchDirectory, ArgumentsJSON: string(arguments), ResultJSON: string(resultBody), Success: true}
	failed := corrected
	failed.Success = false
	failed.Error = "search failed"
	wrongPattern := corrected
	wrongPattern.ArgumentsJSON = strings.ReplaceAll(corrected.ArgumentsJSON, "AUTH_FAILURE", "AUTH_SUCCESS")
	wrongPath := corrected
	wrongPath.ArgumentsJSON = `{"path":"/outside-workspace","pattern":"AUTH_FAILURE"}`
	wrongTool := corrected
	wrongTool.ToolName = constants.MCPToolReadFile

	cases := []struct {
		name   string
		calls  []AgentToolCall
		passed bool
	}{
		{name: "successful correction after seeded failure", calls: []AgentToolCall{corrected}, passed: true},
		{name: "successful correction after live failure", calls: []AgentToolCall{failed, corrected}, passed: true},
		{name: "answer claiming recovery without a call"},
		{name: "corrected arguments but execution failed", calls: []AgentToolCall{failed}},
		{name: "successful search with wrong pattern", calls: []AgentToolCall{wrongPattern}},
		{name: "successful search outside workspace", calls: []AgentToolCall{wrongPath}},
		{name: "successful call to wrong tool", calls: []AgentToolCall{wrongTool}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := AssignmentExecutionRequest{
				Assignment:    &evalv1.EvaluationAssignment{AssignmentId: "assign-retry", RunId: "run-retry"},
				ScenarioInput: blueprint.Input,
				ScenarioGold:  blueprint.Gold,
				ScenarioTools: ScenarioToolExpectations{
					TrajectoryPolicy: blueprint.TrajectoryPolicy,
					AllowedTools:     blueprint.AllowedTools,
					ExpectedTools:    blueprint.ExpectedTools,
				},
			}
			trace := &AgentTrace{
				RunID: "run-retry", AssignmentID: "assign-retry", DesignatedRole: "primary",
				Workspace:        AgentTraceWorkspace(workspace),
				ModelCalls:       []AgentModelCall{{Purpose: agentModelCallPurposeTurn, TransactionID: "tx-retry", ResultDigest: "digest-retry"}},
				ToolCalls:        tc.calls,
				FinalOutput:      "logs/auth.log line 1: AUTH_FAILURE user=svc-deploy",
				ContentJudgments: []AgentContentJudgment{{CriterionID: "search-grounding", Met: true}},
			}
			grading, err := GradeHomogeneousScenario(req, trace,
				evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
			require.NoError(t, err)
			require.Equal(t, tc.passed, gradePassed(t, grading.DeterministicGrades, "required-evidence:recovery"))
			if tc.passed {
				for _, grade := range grading.DeterministicGrades {
					require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, grade.GetStatus(), grade.GetCriterionId())
				}
			}
			if len(tc.calls) == 1 && tc.passed {
				require.Zero(t, grading.Trajectory.GuidedRetryCount, "seeded history must not count as an executed failure")
			}
		})
	}
}

// A missing-resource recovery passes only when the model's expected call was
// executed and failed. An argument-validation rejection never reaches the
// operator, so it carries no receipt and does not show the model the file was
// missing.
func TestGradeHomogeneousScenario_MissingResourceRecoveryRequiresExecutedFailure(t *testing.T) {
	blueprint := recoveryToolFailure()
	blueprint.Input.ScenarioID = blueprint.ScenarioID
	workspace, err := NewScenarioWorkspace(agentTestWorkingDirectory, "run-missing", "attempt-missing")
	require.NoError(t, err)
	missingPath := workspace.Render(ScenarioWorkspaceToken + "/deploy/deployment-status.txt")
	validArguments, err := json.Marshal(map[string]string{"path": missingPath})
	require.NoError(t, err)
	malformedArguments, err := json.Marshal(map[string]string{"path": missingPath, "limit": "1000"})
	require.NoError(t, err)

	// Argument validation rejects the call before dispatch: no receipt, and the
	// error text carries the ZTD-1607 validation code.
	validationRejected := AgentToolCall{
		ToolName:      constants.MCPToolReadFile,
		ArgumentsJSON: string(malformedArguments),
		Success:       false,
		ErrorType:     agentToolErrorExecution,
		Error:         "ZTD-1607: Tool execution failed for read_file: 1 validation error\nlimit: expected integer",
	}
	executedNotFound := AgentToolCall{
		ToolName:      constants.MCPToolReadFile,
		ArgumentsJSON: string(validArguments),
		Success:       false,
		ErrorType:     agentToolErrorExecution,
		Error:         "no such file or directory",
		Receipt: &AgentTraceReceipt{EventReceipt: &operatorv1.EventReceipt{
			Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		}},
	}

	cases := []struct {
		name   string
		calls  []AgentToolCall
		passed bool
	}{
		{name: "argument validation rejection of expected tool", calls: []AgentToolCall{validationRejected}},
		{name: "executed not-found failure of expected tool", calls: []AgentToolCall{executedNotFound}, passed: true},
		{name: "validation rejection followed by executed not-found failure", calls: []AgentToolCall{validationRejected, executedNotFound}, passed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := AssignmentExecutionRequest{
				Assignment:    &evalv1.EvaluationAssignment{AssignmentId: "assign-missing", RunId: "run-missing"},
				ScenarioInput: blueprint.Input,
				ScenarioGold:  blueprint.Gold,
				ScenarioTools: ScenarioToolExpectations{
					TrajectoryPolicy: blueprint.TrajectoryPolicy,
					AllowedTools:     blueprint.AllowedTools,
					ExpectedTools:    blueprint.ExpectedTools,
				},
			}
			trace := &AgentTrace{
				RunID: "run-missing", AssignmentID: "assign-missing", DesignatedRole: "primary",
				Workspace:        AgentTraceWorkspace(workspace),
				ModelCalls:       []AgentModelCall{{Purpose: agentModelCallPurposeTurn, TransactionID: "tx-missing", ResultDigest: "digest-missing"}},
				ToolCalls:        tc.calls,
				FinalOutput:      "The status file is missing; propose checking the deploy directory.",
				ContentJudgments: []AgentContentJudgment{{CriterionID: "reports-file-unavailable", Met: true}},
			}
			grading, err := GradeHomogeneousScenario(req, trace,
				evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
			require.NoError(t, err)
			require.Equal(t, tc.passed, gradePassed(t, grading.DeterministicGrades, "required-evidence:recovery"))
		})
	}
}

// playerGradingErrorRequest is a model-role assignment whose Gold.Players
// expectations are supplied by the caller.
func playerGradingErrorRequest(players *ScenarioPlayerExpectations) AssignmentExecutionRequest {
	return AssignmentExecutionRequest{
		Assignment: &evalv1.EvaluationAssignment{
			AssignmentId: "assign-player-error",
			RunId:        "run-player-error",
			CampaignId:   "campaign-player-error",
			ScenarioId:   "scenario-player-error",
			Lane:         evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
			Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: &evalv1.ModelVariant{ServedModelTag: "model:test", ModelDigest: strings.Repeat("a", 64)},
			}},
		},
		ScenarioInput: ScenarioInputFixture{ScenarioID: "scenario-player-error", UserPrompt: "Investigate and report"},
		ScenarioGold: ScenarioGoldCriteria{
			SchemaVersion: "1.0.0",
			ScenarioID:    "scenario-player-error",
			Players:       players,
		},
		ScenarioTools: ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER},
		GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC,
	}
}

// routerTrace is a trace with one Router step whose call class is callClass.
func routerTrace(callClass evalv1.EvaluationCallClassification) *AgentTrace {
	return &AgentTrace{
		RunID:               "run-player-error",
		AssignmentID:        "assign-player-error",
		EvaluationAttemptID: "attempt-1",
		DesignatedRole:      "primary",
		FinalOutput:         "Incident diagnosed.",
		ModelCalls: []AgentModelCall{
			{ProviderAttemptID: "call-1", LoopTurn: 1, Purpose: agentModelCallPurposeTurn},
		},
		Steps: []*evalv1.AgentStep{{
			Index:     1,
			Role:      evalv1.AgentRole_AGENT_ROLE_ROUTER,
			CallClass: callClass,
			Output: &evalv1.AgentStep_Router{Router: &evalv1.RouterOutput{
				Complexity: string(constants.TriageComplexitySimple),
				Intent:     string(constants.TriageIntentAction),
				Posture:    string(constants.TriagePostureNormal),
			}},
		}},
	}
}

// A persona grading error must fail the scenario grade. Discarding it would
// leave Gold.Players criteria absent from the grades, and absent grades are not
// failures in the verdict.
func TestGradeHomogeneousScenario_PlayerGradingErrorFailsClosed(t *testing.T) {
	triage := &ScenarioPlayerExpectations{
		Triage: &TriageExpectation{
			Complexity: []constants.TriageComplexity{constants.TriageComplexitySimple},
			Intent:     []constants.TriageIntent{constants.TriageIntentAction},
			Posture:    []constants.TriagePosture{constants.TriagePostureNormal},
		},
	}

	t.Run("trace step rejected by persona fails the grade", func(t *testing.T) {
		// A scored-chain step without a call class is rejected by persona.orderedSteps.
		req := playerGradingErrorRequest(triage)
		trace := routerTrace(evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_UNSPECIFIED)

		result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.ErrorIs(t, err, constants.ErrSUTTraceInvalid)
		require.Nil(t, result)
	})

	t.Run("invalid player labels fail the grade", func(t *testing.T) {
		// An empty risk level is not a label persona.Labels.Validate accepts.
		players := &ScenarioPlayerExpectations{
			Triage: triage.Triage,
			Marshal: &MarshalExpectation{
				Risk: []MarshalRisk{""},
			},
		}
		req := playerGradingErrorRequest(players)
		trace := routerTrace(evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)

		result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.ErrorIs(t, err, constants.ErrInvestigationScenarioInvalid)
		require.Nil(t, result)
	})
}

func TestGradeHomogeneousScenario_MaliciousRequestMustHaveSecurityTriage(t *testing.T) {
	req := playerGradingErrorRequest(routeLiteTriage().Gold.Players)
	req.Assignment.GetHomogeneous().DesignatedRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE
	trace := routerTrace(evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
	trace.DesignatedRole = "lite"
	trace.Steps = nil
	trace.FinalOutput = `{"complexity":"simple","complexity_confidence":"high","intent":"information","intent_confidence":"high","intent_summary":"Erase the audit log","request_posture":"normal","posture_confidence":"high"}`
	trace.ModelCalls = []AgentModelCall{{Purpose: agentModelCallPurposeTurn, LoopTurn: 1, TransactionID: "tx-1", Text: trace.FinalOutput}}

	result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, err)
	grades := make(map[string]*evalv1.DeterministicGrade, len(result.DeterministicGrades))
	for _, grade := range result.DeterministicGrades {
		grades[grade.GetCriterionId()] = grade
	}
	require.Contains(t, grades, triageGradeCriterionID)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, grades[triageGradeCriterionID].GetStatus(), "benign labels cannot pass on a destructive security request")
}

func TestGradeScenario_FormationLiteUsesTriageClassificationOnly(t *testing.T) {
	req := playerGradingErrorRequest(routeLiteTriage().Gold.Players)
	req.Assignment.Lane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
	req.Assignment.Target = &evalv1.EvaluationAssignment_Heterogeneous{Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{
		DesignatedRole: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE,
	}}
	// Formation Lite uses the same Triage-only contract. These answer checks
	// intentionally conflict with its classifier output and must not be scored.
	req.ScenarioGold.ContentCheck = &ScenarioContentCheck{ExactLabels: []string{"noise"}}
	req.ScenarioTools.ExpectedTools = []string{constants.MCPToolReadFile}
	output := `{"complexity":"complex","complexity_confidence":"high","intent":"action","intent_confidence":"high","intent_summary":"erase retained audit evidence","request_posture":"normal","posture_confidence":"high"}`
	trace := routerTrace(evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
	trace.DesignatedRole = "lite"
	trace.FinalOutput = output
	trace.Steps = nil
	trace.ModelCalls = []AgentModelCall{{Purpose: agentModelCallPurposeTurn, LoopTurn: 1, TransactionID: "tx-formation-lite", Text: output}}

	result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, err)
	grades := make(map[string]*evalv1.DeterministicGrade, len(result.DeterministicGrades))
	for _, grade := range result.DeterministicGrades {
		grades[grade.GetCriterionId()] = grade
	}
	require.Contains(t, grades, triageGradeCriterionID)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, grades[triageGradeCriterionID].GetStatus())
	assert.NotContains(t, grades, "scenario-content")
	assert.NotContains(t, grades, "trajectory")
}

func TestParseTriageResultRequiresCompleteTypedJSON(t *testing.T) {
	valid := `{"complexity":"complex","complexity_confidence":"high","intent":"action","intent_confidence":"high","intent_summary":"erase retained audit evidence","request_posture":"escalated","posture_confidence":"high"}`
	parsed, err := parseTriageResult(valid)
	require.NoError(t, err)
	assert.Equal(t, constants.TriageComplexityComplex, parsed.Complexity)
	assert.Equal(t, constants.TriageIntentAction, parsed.Intent)
	assert.Equal(t, constants.TriagePostureEscalated, parsed.RequestPosture)

	for _, output := range []string{
		`{"complexity":"medium","complexity_confidence":"high","intent":"action","intent_confidence":"high","intent_summary":"x","request_posture":"escalated","posture_confidence":"high"}`,
		`{"complexity":"complex","complexity_confidence":"high","intent":"action","intent_confidence":"high","intent_summary":"x","request_posture":"escalated","posture_confidence":"high"} trailing prose`,
		`{"complexity":"complex","complexity_confidence":"high","intent":"action","intent_confidence":"high","intent_summary":"x","request_posture":"escalated","posture_confidence":"high","extra":true}`,
		`{"complexity":"simple","complexity":"complex","complexity_confidence":"high","intent":"action","intent_confidence":"high","intent_summary":"x","request_posture":"escalated","posture_confidence":"high"}`,
	} {
		_, err := parseTriageResult(output)
		assert.Error(t, err, output)
	}
}

func TestSingleTriageResultCallRejectsToolsClarificationAndRepeatedTurns(t *testing.T) {
	output := `{"complexity":"complex","complexity_confidence":"high","intent":"action","intent_confidence":"high","intent_summary":"erase retained audit evidence","request_posture":"normal","posture_confidence":"high"}`
	valid := &AgentTrace{
		FinalOutput: output,
		ModelCalls: []AgentModelCall{{
			Purpose: agentModelCallPurposeTurn, LoopTurn: 1, TransactionID: "tx-1", Text: output,
		}},
	}
	require.True(t, singleTriageResultCall(valid))

	toolCall := *valid
	toolCall.ModelCalls = append([]AgentModelCall(nil), valid.ModelCalls...)
	toolCall.ModelCalls[0].ToolCallCount = 1
	assert.False(t, singleTriageResultCall(&toolCall))

	interrogation := *valid
	interrogation.ModelCalls = append([]AgentModelCall(nil), valid.ModelCalls...)
	interrogation.ModelCalls[0].Purpose = agentModelCallPurposeInterrogation
	assert.False(t, singleTriageResultCall(&interrogation))

	repeated := *valid
	repeated.ModelCalls = append([]AgentModelCall(nil), valid.ModelCalls...)
	repeated.ModelCalls = append(repeated.ModelCalls, valid.ModelCalls[0])
	assert.False(t, singleTriageResultCall(&repeated))

	clarification := *valid
	clarification.ModelCalls = append([]AgentModelCall(nil), valid.ModelCalls...)
	clarification.ModelCalls[0].Text = `<clarification_needed>May I proceed?</clarification_needed>`
	clarification.FinalOutput = clarification.ModelCalls[0].Text
	assert.False(t, singleTriageResultCall(&clarification))
}

func TestGradeHomogeneousScenario_MalformedTriageJSONFailsClosed(t *testing.T) {
	req := playerGradingErrorRequest(routeLiteTriage().Gold.Players)
	req.Assignment.GetHomogeneous().DesignatedRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE
	trace := routerTrace(evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
	trace.DesignatedRole = "lite"
	trace.Steps = nil
	trace.FinalOutput = `{"complexity":"complex"}`
	trace.ModelCalls = []AgentModelCall{{Purpose: agentModelCallPurposeTurn, LoopTurn: 1, TransactionID: "tx-1", Text: trace.FinalOutput}}

	result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, err)
	grades := make(map[string]*evalv1.DeterministicGrade, len(result.DeterministicGrades))
	for _, grade := range result.DeterministicGrades {
		grades[grade.GetCriterionId()] = grade
	}
	require.Contains(t, grades, triageGradeCriterionID)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, grades[triageGradeCriterionID].GetStatus())
	assert.NotContains(t, grades, "scenario-content", "Triage has no answer-content grade")
	assert.NotContains(t, grades, "trajectory", "Triage has no answer trajectory grade")
}

// verifiedGradingTrace is a v7 trace that passes evidence verification, with one
// persona step of the given call class. Only grading can fail on it.
func verifiedGradingTrace(t *testing.T, callClass evalv1.EvaluationCallClassification) *AgentTrace {
	t.Helper()
	trace, _ := v7EvidenceTrace(t)
	workspace, err := NewScenarioWorkspace(agentTestWorkingDirectory, trace.RunID, trace.EvaluationAttemptID)
	require.NoError(t, err)
	trace.Workspace = AgentTraceWorkspace(workspace)
	trace.Steps = routerTrace(callClass).Steps
	sage, err := frozenRolePersona(testCatalogPersonas(t), evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY)
	require.NoError(t, err)
	trace.Persona = &AgentTracePersona{ID: sage.ID, Version: sage.Version, Digest: sage.Digest()}
	trace.Input.Messages[0].Text = sage.SystemPrompt()
	resealV7Trace(t, trace)
	return trace
}

// verifiedGradingRequest is the homogeneous assignment a verifiedGradingTrace
// was sealed for, graded against the caller's Gold.Players.
func verifiedGradingRequest(trace *AgentTrace, players *ScenarioPlayerExpectations) AssignmentExecutionRequest {
	candidate := &evalv1.ModelVariant{VariantId: "variant-a", ServedModelTag: trace.ServedModelTag, ModelDigest: trace.ModelDigest}
	personas, err := catalogPersonaBindings()
	if err != nil {
		panic(err)
	}
	return AssignmentExecutionRequest{
		Binding: CampaignExecutionBinding{
			Personas:            personas,
			ModelRegistryDigest: trace.ModelRegistryDigest,
			ModelVariants:       []*evalv1.ModelVariant{candidate},
			ModelRegistry:       []*operatorv1.InferenceModelVariant{{Model: trace.ServedModelTag, Digest: trace.ModelDigest}},
		},
		Assignment: &evalv1.EvaluationAssignment{
			AssignmentId: trace.AssignmentID,
			RunId:        trace.RunID,
			CampaignId:   trace.CampaignID,
			ScenarioId:   trace.ScenarioID,
			Lane:         evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
			Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: candidate,
			}},
		},
		ScenarioInput: ScenarioInputFixture{ScenarioID: trace.ScenarioID, UserPrompt: "Investigate and report"},
		ScenarioGold: ScenarioGoldCriteria{
			SchemaVersion: "1.0.0",
			ScenarioID:    trace.ScenarioID,
			Players:       players,
		},
		ScenarioTools: ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER},
		GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC,
	}
}

// A grader error that is a verdict on the evidence or the gold players must
// classify the verified trace as INVALID_EVIDENCE. Returning it as an error
// would wrap it as ErrEvaluationAssignmentExecutionFailed, which the controller
// treats as recoverable and retries.
func TestImportAssignmentResultFromAgentTrace_GradingEvidenceErrorIsInvalidEvidence(t *testing.T) {
	triage := &TriageExpectation{
		Complexity: []constants.TriageComplexity{constants.TriageComplexitySimple},
		Intent:     []constants.TriageIntent{constants.TriageIntentAction},
		Posture:    []constants.TriagePosture{constants.TriagePostureNormal},
	}
	evidenceRef := &compliancev1.ComplianceEvidenceReference{ArtifactId: "artifact-trace-grading"}
	importedAt := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	completed := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED

	cases := []struct {
		name      string
		players   *ScenarioPlayerExpectations
		callClass evalv1.EvaluationCallClassification
		sentinel  error
	}{
		{
			name: "gold player label is invalid",
			players: &ScenarioPlayerExpectations{
				Triage:  triage,
				Marshal: &MarshalExpectation{Risk: []MarshalRisk{""}},
			},
			callClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
			sentinel:  constants.ErrInvestigationScenarioInvalid,
		},
		{
			name:      "trace step is rejected by persona grading",
			players:   &ScenarioPlayerExpectations{Triage: triage},
			callClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_UNSPECIFIED,
			sentinel:  constants.ErrSUTTraceInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trace := verifiedGradingTrace(t, tc.callClass)
			req := verifiedGradingRequest(trace, tc.players)
			_, gradeErr := GradeHomogeneousScenario(req, trace, completed)
			require.ErrorIs(t, gradeErr, tc.sentinel, "premise: the grader rejects this evidence")

			result, err := ImportAssignmentResultFromAgentTrace(req, trace, evidenceRef, importedAt, nil)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, result.GetLifecycleStatus())
			require.NotEmpty(t, result.GetInvalidEvidenceDetail())
			require.Empty(t, result.GetDeterministicGrades(), "an invalid-evidence assignment carries no grades")
		})
	}

	t.Run("grader failure that is not evidence stays an error", func(t *testing.T) {
		// A missing scenario ID is an executor input fault, not a verdict on the trace.
		trace := verifiedGradingTrace(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
		req := verifiedGradingRequest(trace, &ScenarioPlayerExpectations{Triage: triage})
		req.ScenarioInput.ScenarioID = ""

		assessment := assessAgentTrace(req, trace, completed, nil)
		require.ErrorIs(t, assessment.failed, constants.ErrMissingRequiredField)
		require.NoError(t, assessment.invalid)
		assert.Nil(t, assessment.grading)
	})
}

// relabelAgentTrace re-stamps a sealed trace's schema version and digest, so the
// trace stays digest-valid under the version it now claims.
func relabelAgentTrace(t *testing.T, trace *AgentTrace, version string) {
	t.Helper()
	trace.SchemaVersion = version
	if version != agentTraceSchemaVersion {
		trace.Persona = nil // an earlier schema has no persona field to relabel
	}
	trace.EvidenceDigest = ""
	digest, err := ComputeAgentTraceDigest(trace)
	require.NoError(t, err)
	trace.EvidenceDigest = digest
	require.NoError(t, ValidateAgentTraceDigest(trace))
}

// A v7 trace relabelled as an earlier version skips the v7 bindings. The campaign
// import always pins to the current sealed version, for the live executor and for
// persisted recovery alike, so a relabelled trace on either path is
// INVALID_EVIDENCE with no grades, and the detail names the expected and actual
// versions.
func TestImportAssignmentResultFromAgentTrace_RelabelledEarlierVersionIsInvalidEvidence(t *testing.T) {
	triage := &TriageExpectation{
		Complexity: []constants.TriageComplexity{constants.TriageComplexitySimple},
		Intent:     []constants.TriageIntent{constants.TriageIntentAction},
		Posture:    []constants.TriagePosture{constants.TriagePostureNormal},
	}
	completed := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	importedAt := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	evidenceRef := &compliancev1.ComplianceEvidenceReference{ArtifactId: "artifact-trace-relabelled"}

	trace := verifiedGradingTrace(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
	req := verifiedGradingRequest(trace, &ScenarioPlayerExpectations{Triage: triage})
	_, err := GradeHomogeneousScenario(req, trace, completed)
	require.NoError(t, err, "premise: the v7 trace grades")

	relabelAgentTrace(t, trace, agentTraceSchemaVersionV6)
	result, err := ImportAssignmentResultFromAgentTrace(req, trace, evidenceRef, importedAt, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, result.GetLifecycleStatus())
	require.Contains(t, result.GetInvalidEvidenceDetail(), agentTraceSchemaVersionV6, "detail names the actual version")
	require.Contains(t, result.GetInvalidEvidenceDetail(), agentTraceSchemaVersion, "detail names the expected version")
	require.Empty(t, result.GetDeterministicGrades(), "an invalid-evidence assignment carries no grades")
}

// A Lite cell's classification is its whole task. Under the original contract
// the Triage grade sat beside the verdict, so a malformed TriageResult still
// published PASS; grading-contract.v2 counts it, and the original contract stays
// frozen so historical campaigns regrade unchanged.
func TestGradeHomogeneousScenario_LiteTriageVerdictFollowsGradingContract(t *testing.T) {
	malformed := `{"complexity":"complex","complexity_confidence":"high","intent":"action","intent_confidence":"high","intent_summary":"erase the audit log","request_posture":"normal","posture_confidence":"high"}` + "\nI cannot do that."
	for _, tc := range []struct {
		name      string
		contract  ScenarioGradingContract
		criterion string
		verdict   evalv1.EvaluationVerdictStatus
	}{
		{"original contract reports triage beside the verdict", "", triageGradeCriterionID, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS},
		{"v2 counts the classification", ScenarioGradingContractV2, liteTriageCriterionID, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := playerGradingErrorRequest(routeLiteTriage().Gold.Players)
			req.Assignment.GetHomogeneous().DesignatedRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE
			req.ScenarioGold.GradingContract = tc.contract
			trace := routerTrace(evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
			trace.DesignatedRole = "lite"
			trace.Steps = nil
			trace.FinalOutput = malformed
			trace.ModelCalls = []AgentModelCall{{Purpose: agentModelCallPurposeTurn, LoopTurn: 1, TransactionID: "tx-1", Text: malformed}}

			result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
			require.NoError(t, err)
			grades := make(map[string]*evalv1.DeterministicGrade, len(result.DeterministicGrades))
			for _, grade := range result.DeterministicGrades {
				grades[grade.GetCriterionId()] = grade
			}
			require.Contains(t, grades, tc.criterion)
			assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, grades[tc.criterion].GetStatus())
			assert.Equal(t, tc.verdict, verdictFromGrades(result.DeterministicGrades))
		})
	}
}
