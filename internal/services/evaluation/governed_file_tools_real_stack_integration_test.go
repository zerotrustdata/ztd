// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/investigation"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

const governedFileToolsScenarioID = "tool-select-file-read"

var (
	fileDiffIDPattern       = regexp.MustCompile(`"id"\s*:\s*"([^\"]+)"`)
	fileLedgerBeforePattern = regexp.MustCompile(`"ledger_hash_before"\s*:\s*"([a-f0-9]{40,64})"`)
)

func governedFileToolsCatalog() (*evalv1.EvaluationScenarioCatalog, map[string]evaluation.ScenarioArtifacts, error) {
	catalog, artifacts, err := evaluation.LoadScenarioCatalog()
	if err != nil {
		return nil, nil, err
	}
	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetScenarioId() != governedFileToolsScenarioID {
			continue
		}
		scenario.AllowedTools = []string{constants.MCPToolFileEdit, constants.MCPToolFileDiff, constants.MCPToolRestoreFile}
		scenario.ExpectedTools = []string{constants.MCPToolFileEdit}
		scenario.ForbiddenTools = nil
		scenario.TrajectoryPolicy = evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_FIRST_CHOICE
		scenario.GradingMethod = evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC
		pair := artifacts[governedFileToolsScenarioID]
		var input evaluation.ScenarioInputFixture
		if err := json.Unmarshal(pair.Input.Body, &input); err != nil {
			return nil, nil, err
		}
		input.UserPrompt = "Review the recorded change to the checkout TLS setting and undo it if it disabled verification."
		input.WorkspaceFiles = []evaluation.ScenarioWorkspaceFile{{
			Label: "checkout TLS configuration", RelPath: "services/checkout/config.yaml",
			Content: "tls_verify: true\nendpoint: https://payments.internal\n",
		}}
		inputBody, err := json.Marshal(input)
		if err != nil {
			return nil, nil, err
		}
		pair.Input.Body = inputBody
		pair.Input.Reference = updateScenarioArtifactReference(pair.Input.Reference, evidence.ArtifactTypeEvaluationScenarioInput, inputBody)
		scenario.InputFixtureRef = proto.Clone(pair.Input.Reference).(*compliancev1.ComplianceEvidenceReference)

		var gold evaluation.ScenarioGoldCriteria
		if err := json.Unmarshal(pair.Gold.Body, &gold); err != nil {
			return nil, nil, err
		}
		gold.ExpectedBehavior = "Use the recorded file diff to restore the checkout TLS configuration to its pre-change content."
		gold.RoleCriteria = []evaluation.ScenarioRoleCriteria{{Role: "primary", Criteria: []evaluation.ScenarioCriterion{{
			CriterionID: "restored-checkout-tls-config", Description: "The final checkout configuration matches its pre-change state.", Deterministic: true,
		}}}}
		gold.PipelineCriteria = nil
		gold.ContentCheck = nil
		gold.FileState = []investigation.FileState{{Path: "services/checkout/config.yaml", Contains: []string{"tls_verify: true", "endpoint: https://payments.internal"}, NotContains: []string{"tls_verify: false"}}}
		gold.GovernedProbe = nil
		gold.ArgumentValidators = nil
		gold.RequiredEvidenceTypes = nil
		goldBody, err := json.Marshal(gold)
		if err != nil {
			return nil, nil, err
		}
		pair.Gold.Body = goldBody
		pair.Gold.Reference = updateScenarioArtifactReference(pair.Gold.Reference, evidence.ArtifactTypeEvaluationScenarioGold, goldBody)
		scenario.GoldCriteriaRef = proto.Clone(pair.Gold.Reference).(*compliancev1.ComplianceEvidenceReference)
		artifacts[governedFileToolsScenarioID] = pair
		return catalog, artifacts, nil
	}
	return nil, nil, fmt.Errorf("scenario %q not found", governedFileToolsScenarioID)
}

func updateScenarioArtifactReference(reference *compliancev1.ComplianceEvidenceReference, artifactType evidence.ArtifactType, body []byte) *compliancev1.ComplianceEvidenceReference {
	updated := proto.Clone(reference).(*compliancev1.ComplianceEvidenceReference)
	hash := sha256.Sum256(body)
	updated.ArtifactId = evidence.ContentAddress(artifactType, body)
	updated.ArtifactType = string(artifactType)
	updated.Sha256 = hex.EncodeToString(hash[:])
	return updated
}

func fileToolArguments(v any) json.RawMessage {
	encoded, _ := json.Marshal(v)
	return encoded
}

func toolReply(name string, arguments json.RawMessage) fixtures.OllamaReply {
	return fixtures.OllamaReply{ToolCalls: []fixtures.OllamaToolCall{{Name: name, Arguments: arguments}}}
}

func TestCampaignAgentExecutorIntegration_GovernedFileEditDiffRestoreVerifyAndGrade(t *testing.T) {
	var env *realStackAgentRun
	filePath := func() (string, error) { return env.inFlightWorkspaceFile("services/checkout/config.yaml") }
	replies := []fixtures.OllamaReply{
		{Respond: func(fixtures.OllamaChatRequest) fixtures.OllamaReply {
			path, err := filePath()
			if err != nil {
				return fixtures.OllamaReply{Failure: err.Error()}
			}
			return toolReply(constants.MCPToolFileEdit, fileToolArguments(mcp.FileEditToolRequest{
				FilePath: path, Operation: "replace", OldContent: "tls_verify: true", NewContent: "tls_verify: false",
				Justification: "undo the verification bypass recorded in the change",
			}))
		}},
		{Respond: func(fixtures.OllamaChatRequest) fixtures.OllamaReply {
			path, err := filePath()
			if err != nil {
				return fixtures.OllamaReply{Failure: err.Error()}
			}
			return toolReply(constants.MCPToolFileDiff, fileToolArguments(mcp.FileDiffRequest{FilePath: path}))
		}},
		{Respond: func(request fixtures.OllamaChatRequest) fixtures.OllamaReply {
			text := strings.Join(func() []string {
				messages := make([]string, 0, len(request.Messages))
				for _, message := range request.Messages {
					messages = append(messages, message.Content)
				}
				return messages
			}(), "\n")
			match := fileDiffIDPattern.FindStringSubmatch(text)
			if len(match) != 2 {
				return fixtures.OllamaReply{Failure: "scripted provider: file_diff listing did not expose a diff_id"}
			}
			return toolReply(constants.MCPToolFileDiff, fileToolArguments(mcp.FileDiffRequest{DiffID: match[1]}))
		}},
		{Respond: func(request fixtures.OllamaChatRequest) fixtures.OllamaReply {
			text := strings.Join(func() []string {
				messages := make([]string, 0, len(request.Messages))
				for _, message := range request.Messages {
					messages = append(messages, message.Content)
				}
				return messages
			}(), "\n")
			match := fileLedgerBeforePattern.FindStringSubmatch(text)
			path, err := filePath()
			if err != nil {
				return fixtures.OllamaReply{Failure: err.Error()}
			}
			if len(match) != 2 {
				return fixtures.OllamaReply{Failure: "scripted provider: file_diff detail did not expose ledger_hash_before"}
			}
			return toolReply(constants.MCPToolRestoreFile, fileToolArguments(mcp.RestoreFileToolRequest{FilePath: path, CommitHash: match[1]}))
		}},
		{Content: "I inspected the change and restored the checkout configuration to its prior verified state."},
	}
	env = newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "governed-file-edit-diff-restore", scenarioID: governedFileToolsScenarioID,
		catalog: governedFileToolsCatalog, replies: replies,
	})

	result := env.executeNext(t)
	if result.GetLifecycleStatus() != evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED {
		trace, err := env.store.LoadAssignmentAgentTrace(t.Context(), env.req.RunID, result.GetAssignmentId())
		if err == nil {
			modelTurns := make([]string, 0, len(trace.ModelCalls))
			for _, call := range trace.ModelCalls {
				modelTurns = append(modelTurns, fmt.Sprintf("turn=%d purpose=%s text=%q finish=%s", call.LoopTurn, call.Purpose, call.Text, call.FinishReason))
			}
			toolCalls := make([]string, 0, len(trace.ToolCalls))
			for _, call := range trace.ToolCalls {
				toolCalls = append(toolCalls, fmt.Sprintf("tool=%s action=%s success=%t error=%q result=%q", call.ToolName, call.ActionType, call.Success, call.Error, call.ResultJSON))
			}
			t.Logf("failed proof trace provider_failure=%q model_turns=%v tool_calls=%v", trace.ProviderFailure, modelTurns, toolCalls)
		}
	}
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "failure=%s invalid=%s", result.GetFailureReason(), result.GetInvalidEvidenceDetail())
	require.Equal(t, 5, env.stack.Provider.ChatCount())
	trace := executorLoadTrace(t, env, result)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace), "trace verification is offline and checks the signed typed action payloads")
	tampered := *trace
	tampered.ToolCalls = append([]evaluation.AgentToolCall(nil), trace.ToolCalls...)
	tampered.ToolCalls[0].ResultJSON += "tampered"
	require.Error(t, evaluation.VerifyAgentTraceEvidence(&tampered), "offline verification rejects tool output that no longer matches its signed receipt")
	require.Len(t, trace.ToolCalls, 4)
	require.Equal(t, []string{constants.MCPToolFileEdit, constants.MCPToolFileDiff, constants.MCPToolFileDiff, constants.MCPToolRestoreFile}, []string{
		trace.ToolCalls[0].ToolName, trace.ToolCalls[1].ToolName, trace.ToolCalls[2].ToolName, trace.ToolCalls[3].ToolName,
	})
	wantActions := []string{string(constants.ActionTypeFileEdit), string(constants.ActionTypeFetchFileDiff), string(constants.ActionTypeFetchFileDiff), string(constants.ActionTypeRestoreFile)}
	operator, err := env.stack.Gateway.Service.GetRegistrationService().GetOperator(t.Context(), env.dataOperatorID)
	require.NoError(t, err)
	publicKey, err := models.DecodeActuatorPublicKey(operator.GetActuatorPublicKey())
	require.NoError(t, err)
	for i, call := range trace.ToolCalls {
		require.True(t, call.Success, "tool %s: %s", call.ToolName, call.Error)
		require.NotNil(t, call.RequestEnvelope)
		require.NotNil(t, call.Receipt)
		assert.Equal(t, wantActions[i], call.ActionType)
		assert.Equal(t, wantActions[i], call.Receipt.GetActionType())
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, call.Receipt.GetStatus())
		require.NoError(t, governance.VerifyEventReceiptSignature(call.Receipt.EventReceipt, publicKey), "typed action receipt verifies offline")
		require.NoError(t, governance.VerifyReceiptPersistenceAttestation(call.Receipt.EventReceipt, publicKey), "local persistence attestation verifies offline")
	}
	var editResult operatorv1.FileEditResult
	require.NoError(t, protojson.Unmarshal([]byte(trace.ToolCalls[0].Receipt.GetResultSummary()), &editResult))
	assert.Equal(t, "replace", editResult.GetOperation())
	assert.Equal(t, trace.Workspace.Root+"/services/checkout/config.yaml", editResult.GetFilePath())
	assert.Contains(t, trace.ToolCalls[1].Receipt.GetResultSummary(), "ledger_hash_before")
	assert.Contains(t, trace.ToolCalls[2].Receipt.GetResultSummary(), "diff_content")
	var restoreResult operatorv1.RestoreFileResult
	require.NoError(t, protojson.Unmarshal([]byte(trace.ToolCalls[3].Receipt.GetResultSummary()), &restoreResult))
	assert.Equal(t, trace.Workspace.Root+"/services/checkout/config.yaml", restoreResult.GetFilePath())

	var fileStateGrade, roleGrade *evalv1.DeterministicGrade
	for _, grade := range result.GetDeterministicGrades() {
		switch grade.GetCriterionId() {
		case "file-state":
			fileStateGrade = grade
		case "restored-checkout-tls-config":
			roleGrade = grade
		}
	}
	require.NotNil(t, fileStateGrade)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, fileStateGrade.GetStatus())
	assert.Contains(t, fileStateGrade.GetDetail(), "workspace file state matches scenario objective")
	require.NotNil(t, roleGrade)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, roleGrade.GetStatus())

	report, err := evaluation.NewCampaignRunVerifier(env.clock).VerifyRun(t.Context(), env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), strings.Join(report.GetFailureReasons(), "\n"))
}
