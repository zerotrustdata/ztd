// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// longitudinalInvestigationLaneClient handles commands, file reads, and context queries
// backed by an in-memory ledger representing the Data Operator's audit store.
type longitudinalInvestigationLaneClient struct {
	base       *agentLaneClient
	records    []*models.ActionReceiptRecord
	queries    []models.OperatorCommandRequest
	fsReads    map[string]string
	commands   map[string]string
	dispatches int
}

func newLongitudinalInvestigationLaneClient(base *agentLaneClient) *longitudinalInvestigationLaneClient {
	return &longitudinalInvestigationLaneClient{
		base:     base,
		fsReads:  make(map[string]string),
		commands: make(map[string]string),
	}
}

func (c *longitudinalInvestigationLaneClient) DispatchCommand(ctx context.Context, persona client.Persona, req models.OperatorCommandRequest) (*client.GovernedOutcome, error) {
	action, err := constants.ValidateGovernedRequest(constants.EventType(req.EventType))
	if err != nil {
		return nil, err
	}
	if action == constants.ActionTypeMcpCall {
		call := &operatorv1.McpCallRequested{}
		if err := proto.Unmarshal(req.Payload, call); err != nil {
			return nil, err
		}
		if call.GetToolName() == constants.MCPToolAuditReceiptList || call.GetToolName() == agentToolInvestigationCtx {
			c.queries = append(c.queries, req)
			var query struct {
				InvestigationID   string `json:"investigation_id"`
				ExcludeActionType string `json:"exclude_action_type"`
			}
			if err := json.Unmarshal([]byte(call.GetArgumentsJson()), &query); err != nil {
				return nil, err
			}
			var rows []*operatorv1.ActionReceipt
			for _, row := range c.records {
				if row.InvestigationID == query.InvestigationID {
					if query.ExcludeActionType != "" && string(row.ActionType) == query.ExcludeActionType {
						continue
					}
					rows = append(rows, row.ActionReceipt)
				}
			}
			result := models.OperatorAuditReceiptListResult{
				OperatorID:      c.base.fixture.ledger.operatorID,
				Authority:       "operator_local_ledger",
				Receipts:        rows,
				Count:           len(rows),
				InvestigationID: query.InvestigationID,
			}
			body, err := json.Marshal(result)
			if err != nil {
				return nil, err
			}
			txID := fmt.Sprintf("tx-context-query-%d", len(c.queries))
			r := c.base.fixture.ledger.executed(txID, string(constants.ActionTypeMcpCall))
			r.ResultSummary = string(body)
			c.base.fixture.ledger.signer.Resign(c.base.fixture.t, r)
			return &client.GovernedOutcome{
				Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, Receipt: r},
				Dispatch:    &operatorv1.GovernedDispatchResult{TransactionId: txID, Receipt: r},
			}, nil
		}
		if call.GetToolName() == "read_file" {
			var args struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal([]byte(call.GetArgumentsJson()), &args)
			content := ""
			for pathSub, data := range c.fsReads {
				if strings.Contains(args.Path, pathSub) {
					content = data
					break
				}
			}
			c.dispatches++
			txID := fmt.Sprintf("tx-dispatch-%d", c.dispatches)
			receipt := c.base.fixture.ledger.executed(txID, string(constants.ActionTypeFsRead))
			receipt.ResultSummary = content
			c.base.fixture.ledger.signer.Resign(c.base.fixture.t, receipt)

			c.records = append(c.records, &models.ActionReceiptRecord{
				TransactionID:     receipt.GetTransactionId(),
				OperatorID:        c.base.fixture.ledger.operatorID,
				OperatorSessionID: c.base.fixture.ledger.sessionID,
				InvestigationID:   req.InvestigationID,
				ActionType:        constants.ActionTypeFsRead,
				ResultSummary:     receipt.GetResultSummary(),
				Status:            receipt.GetStatus(),
				ActionReceipt:     receipt,
			})
			return &client.GovernedOutcome{
				Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, Receipt: receipt},
				Dispatch:    &operatorv1.GovernedDispatchResult{TransactionId: txID, Receipt: receipt},
			}, nil
		}
	}

	c.dispatches++
	txID := fmt.Sprintf("tx-dispatch-%d", c.dispatches)
	receipt := c.base.fixture.ledger.executed(txID, string(action))

	var result proto.Message
	switch action {
	case constants.ActionTypeExecuteBash:
		cmdReq := &operatorv1.CommandRequested{}
		cmdStr := req.TargetResource
		if err := proto.Unmarshal(req.Payload, cmdReq); err == nil && cmdReq.GetCommand() != "" {
			cmdStr = cmdReq.GetCommand()
		}
		stdout := "OK\n"
		for cmdSub, out := range c.commands {
			if strings.Contains(cmdStr, cmdSub) {
				stdout = out
				break
			}
		}
		result = &operatorv1.CommandResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, Stdout: stdout}
		receipt.ResultSummary = fmt.Sprintf("%s: %s", cmdStr, stdout)
		c.base.fixture.ledger.signer.Resign(c.base.fixture.t, receipt)
	case constants.ActionTypeFsRead:
		content := ""
		for pathSub, data := range c.fsReads {
			if strings.Contains(req.TargetResource, pathSub) {
				content = data
				break
			}
		}
		result = &operatorv1.FsReadResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, Path: req.TargetResource, Content: content}
		receipt.ResultSummary = content
		c.base.fixture.ledger.signer.Resign(c.base.fixture.t, receipt)
	case constants.ActionTypeFileEdit:
		result = &operatorv1.FileEditResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, FilePath: req.TargetResource, Operation: string(constants.FileOperationWrite)}
	default:
		return nil, fmt.Errorf("longitudinal test client: unscripted action %s", action)
	}

	payload, err := proto.Marshal(result)
	if err != nil {
		return nil, err
	}

	// Record in persistent audit store
	c.records = append(c.records, &models.ActionReceiptRecord{
		TransactionID:     receipt.GetTransactionId(),
		OperatorID:        c.base.fixture.ledger.operatorID,
		OperatorSessionID: c.base.fixture.ledger.sessionID,
		InvestigationID:   req.InvestigationID,
		ActionType:        action,
		ResultSummary:     receipt.GetResultSummary(),
		Status:            receipt.GetStatus(),
		ActionReceipt:     receipt,
	})

	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, Receipt: receipt},
		Dispatch:    &operatorv1.GovernedDispatchResult{TransactionId: txID, Receipt: receipt, ResultPayload: payload},
	}, nil
}

func TestInvestigationCatalog_LoadsAndValidatesAllPlantedFaultScenarios(t *testing.T) {
	catalog, artifacts, err := LoadInvestigationsScenarioCatalog()
	require.NoError(t, err)
	require.NotNil(t, catalog)
	require.Equal(t, InvestigationsSuiteID, catalog.GetCatalogRef().GetId())
	require.Len(t, catalog.GetScenarios(), 3)

	expectedIDs := []string{"cron-disk-fill", "wrong-path-check-script", "wrong-port-config"}
	for i, expectedID := range expectedIDs {
		scenario := catalog.GetScenarios()[i]
		assert.Equal(t, expectedID, scenario.GetScenarioId())
		assert.Equal(t, "1.0.0", scenario.GetScenarioVersion())
		assert.NotEmpty(t, scenario.GetPublicDescription())
		assert.Equal(t, evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_TECHNICAL_ANALYSIS, scenario.GetCategory())

		artifact, exists := artifacts[expectedID]
		require.True(t, exists, "artifacts must exist for scenario %s", expectedID)
		assert.NotEmpty(t, artifact.Input.Body)
		assert.NotEmpty(t, artifact.Gold.Body)
	}

	// Verify the suite authoring record round-trips
	suite, err := InvestigationsScenarioSuite()
	require.NoError(t, err)
	assert.Equal(t, InvestigationsSuiteID, suite.ID)
	assert.Len(t, suite.Scenarios, 3)
}

func TestLongitudinalCondition_ImprovementCurveAcrossSequentialRuns(t *testing.T) {
	blueprints, err := InvestigationScenarioBlueprints()
	require.NoError(t, err)
	var bp ScenarioBlueprint
	for _, b := range blueprints {
		if b.ScenarioID == "wrong-port-config" {
			bp = b
			break
		}
	}
	require.Equal(t, "wrong-port-config", bp.ScenarioID)

	persistentInvestigationID := "inv-persistent-wrong-port"

	// --- RUN 1 (Initial Run on Persistent Investigation) ---
	// Turn 1: Model reads app/service.conf
	turn1Parts := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId:        "call-read-conf",
			Name:          "read_file",
			ArgumentsJson: `{"path":"app/service.conf"}`,
		}}},
	}
	// Turn 2: Model reads app/logs/service.log
	turn2Parts := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId:        "call-read-log",
			Name:          "read_file",
			ArgumentsJson: `{"path":"app/logs/service.log"}`,
		}}},
	}
	// Turn 3: Model runs health check script
	turn3Parts := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId:        "call-run-check",
			Name:          agentToolRunCommands,
			ArgumentsJson: `{"command":"sh ./check.sh","request":"verify health check"}`,
		}}},
	}
	// Turn 4: Model diagnoses the issue
	turn4Parts := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{
			Text: "Diagnosis: upstream health check expects port 8080, but inventory-api is configured with listen_port = 8081 in app/service.conf. Changing listen_port to 8080 resolves the issue.",
		}},
	}

	f1 := newAgentExecutorFixture(t, turn1Parts, turn2Parts, turn3Parts, turn4Parts)
	transport := newLongitudinalInvestigationLaneClient(f1.client)
	// Seed file read and command responses
	transport.fsReads["app/service.conf"] = "# service configuration\nname = inventory-api\nlisten_port = 8081\nlog_file = app/logs/service.log\n"
	transport.fsReads["app/logs/service.log"] = "ERROR upstream health check expects port 8080, service is listening on 8081\n"
	transport.commands["check.sh"] = "port mismatch: app/service.conf does not listen on 8080\n"
	f1.executor.tools = NewCommandLane(transport, client.Persona{})

	f1.req.Assignment.RunId = "run-seq-1"
	f1.req.Assignment.ScenarioId = bp.ScenarioID
	f1.req.ScenarioInput = bp.Input
	f1.req.ScenarioGold = bp.Gold
	f1.req.ScenarioTools = ScenarioToolExpectations{
		AllowedTools:     bp.AllowedTools,
		TrajectoryPolicy: bp.TrajectoryPolicy,
	}
	f1.req.Investigation = &evalv1.EvaluationInvestigation{
		Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
		InvestigationId: persistentInvestigationID,
		DataOperatorId:  f1.ledger.operatorID,
	}

	res1, err := f1.execute()
	require.NoError(t, err)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res1.GetLifecycleStatus())

	scores1 := make(map[string]float64)
	for _, score := range res1.GetDecomposedScores() {
		scores1[score.GetDimension()] = score.GetValue()
	}
	steps1 := scores1[MetricStepsToDiagnosis]
	require.Equal(t, 7.0, steps1, "run 1 should take 7 steps (4 model turns + 3 tool calls)")
	require.Equal(t, 1.0, scores1[MetricDiagnosisCorrectness])
	require.Equal(t, 1.0, scores1[MetricEvidenceBackedConclusion])

	// --- RUN 2 (Sequential Run on Persistent Investigation) ---
	// Workspace is restored to planted fault.
	// Turn 1: Model calls audit_receipt_list, sees prior run's findings!
	run2Turn1 := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId:        "call-context-query",
			Name:          constants.MCPToolAuditReceiptList,
			ArgumentsJson: `{"limit":20}`,
		}}},
	}
	// Turn 2: Directly outputs diagnosis without needing extra file reads!
	run2Turn2 := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{
			Text: "Diagnosis: based on prior investigation history, inventory-api listens on 8081 instead of expected port 8080. Update app/service.conf to listen_port = 8080.",
		}},
	}

	f2 := newAgentExecutorFixture(t, run2Turn1, run2Turn2)
	f2.ledger = f1.ledger
	f2.req.Binding.Topology.Data = f1.req.Binding.Topology.Data
	// Connect to same transport holding persistent ledger records
	f2.executor.tools = NewCommandLane(transport, client.Persona{})

	f2.req.Assignment.RunId = "run-seq-2"
	f2.req.Assignment.ScenarioId = bp.ScenarioID
	f2.req.ScenarioInput = bp.Input
	f2.req.ScenarioGold = bp.Gold
	f2.req.ScenarioTools = ScenarioToolExpectations{
		AllowedTools:     bp.AllowedTools,
		TrajectoryPolicy: bp.TrajectoryPolicy,
	}
	f2.req.Investigation = &evalv1.EvaluationInvestigation{
		Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
		InvestigationId: persistentInvestigationID,
		DataOperatorId:  f1.ledger.operatorID,
	}

	res2, err := f2.execute()
	require.NoError(t, err)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res2.GetLifecycleStatus())

	scores2 := make(map[string]float64)
	for _, score := range res2.GetDecomposedScores() {
		scores2[score.GetDimension()] = score.GetValue()
	}
	steps2 := scores2[MetricStepsToDiagnosis]
	require.Equal(t, 3.0, steps2, "run 2 should take 3 steps (2 model turns + 1 context query call)")
	require.Equal(t, 1.0, scores2[MetricDiagnosisCorrectness])
	require.Equal(t, 1.0, scores2[MetricEvidenceBackedConclusion])
	require.Equal(t, 1.0, scores2[MetricRetrievalHitRate], "retrieval hit rate should be 100% from prior history")
	require.Equal(t, 0.0, scores2[MetricRedundantCommandRate], "no redundant commands executed")

	// Verify the longitudinal improvement: Run 2 took fewer steps than Run 1
	assert.Less(t, steps2, steps1, "sequential run on persistent investigation should achieve diagnosis in fewer steps than initial run")

	// --- CONTROL CONDITION (Fresh Investigation Runs) ---
	// In fresh investigation, audit_receipt_list returns nothing.
	controlTurn1 := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId:        "call-context-query-fresh",
			Name:          constants.MCPToolAuditReceiptList,
			ArgumentsJson: `{"limit":20}`,
		}}},
	}
	// Since history was empty, agent must perform file reads next
	controlTurn2 := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId:        "call-read-conf-control",
			Name:          "read_file",
			ArgumentsJson: `{"path":"app/service.conf"}`,
		}}},
	}
	controlTurn3 := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId:        "call-read-log-control",
			Name:          "read_file",
			ArgumentsJson: `{"path":"app/logs/service.log"}`,
		}}},
	}
	controlTurn4 := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId:        "call-run-check-control",
			Name:          agentToolRunCommands,
			ArgumentsJson: `{"command":"sh ./check.sh","request":"verify health check"}`,
		}}},
	}
	// Turn 5: Finally outputs diagnosis
	controlTurn5 := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{
			Text: "Diagnosis: upstream health check expects port 8080, but inventory-api is configured with listen_port = 8081 in app/service.conf.",
		}},
	}

	fCtrl := newAgentExecutorFixture(t, controlTurn1, controlTurn2, controlTurn3, controlTurn4, controlTurn5)
	fCtrl.ledger = f1.ledger
	fCtrl.req.Binding.Topology.Data = f1.req.Binding.Topology.Data
	fCtrl.executor.tools = NewCommandLane(transport, client.Persona{})

	fCtrl.req.Assignment.RunId = "run-control-2"
	fCtrl.req.Assignment.ScenarioId = bp.ScenarioID
	fCtrl.req.ScenarioInput = bp.Input
	fCtrl.req.ScenarioGold = bp.Gold
	fCtrl.req.ScenarioTools = ScenarioToolExpectations{
		AllowedTools:     bp.AllowedTools,
		TrajectoryPolicy: bp.TrajectoryPolicy,
	}
	fCtrl.req.Investigation = &evalv1.EvaluationInvestigation{
		Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH,
	}

	resCtrl, err := fCtrl.execute()
	require.NoError(t, err)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, resCtrl.GetLifecycleStatus())

	scoresCtrl := make(map[string]float64)
	for _, score := range resCtrl.GetDecomposedScores() {
		scoresCtrl[score.GetDimension()] = score.GetValue()
	}
	stepsCtrl := scoresCtrl[MetricStepsToDiagnosis]
	require.Equal(t, 9.0, stepsCtrl, "control run without persistent context must take extra steps")

	// Verify the comparison: Persistent condition achieves correct diagnosis in fewer steps than Control
	assert.Less(t, steps2, stepsCtrl, "persistent investigation with context retrieval must require fewer steps than fresh-investigation control")
}

func TestPlantedFaultFixtures_EvaluationAcrossAllThreeScenarios(t *testing.T) {
	blueprints, err := InvestigationScenarioBlueprints()
	require.NoError(t, err)
	require.Len(t, blueprints, 3)

	testCases := []struct {
		scenarioID    string
		readFile      string
		fileReads     map[string]string
		diagnosisText string
		expectedTerms []string
		expectedHits  []string
	}{
		{
			scenarioID: "wrong-port-config",
			readFile:   "app/logs/service.log",
			fileReads: map[string]string{
				"app/logs/service.log": "upstream health check expects port 8080, service is listening on 8081\n",
			},
			diagnosisText: "Diagnosis: port configuration mismatch. Upstream expects port 8080.",
			expectedTerms: []string{"8080"},
		},
		{
			scenarioID: "wrong-path-check-script",
			readFile:   "conf/settings.json",
			fileReads: map[string]string{
				"scripts/check.sh":   "grep config/settings.json\n",
				"conf/settings.json": "{\"enabled\": true}\n",
			},
			diagnosisText: "Diagnosis: check script references stale path; settings file is located at conf/settings.json.",
			expectedTerms: []string{"conf/settings.json"},
		},
		{
			scenarioID: "cron-disk-fill",
			readFile:   "cron/app.cron",
			fileReads: map[string]string{
				"logs/disk-report.txt": "14G /var/tmp/app-dump.log\n",
				"cron/app.cron":        "* * * * * /usr/local/bin/app-dump\n",
			},
			diagnosisText: "Diagnosis: disk filled by cron job app-dump every minute. Remove the app-dump entry from cron.",
			expectedTerms: []string{"app-dump", "cron"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.scenarioID, func(t *testing.T) {
			var bp ScenarioBlueprint
			for _, b := range blueprints {
				if b.ScenarioID == tc.scenarioID {
					bp = b
					break
				}
			}
			require.Equal(t, tc.scenarioID, bp.ScenarioID)

			readFile := tc.readFile
			if readFile == "" {
				readFile = bp.Input.WorkspaceFiles[0].RelPath
			}
			turns := [][]*operatorv1.InferenceResponsePart{
				{{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
					CallId:        "call-1",
					Name:          "read_file",
					ArgumentsJson: fmt.Sprintf(`{"path":%q}`, readFile),
				}}}},
				{{Part: &operatorv1.InferenceResponsePart_Text{Text: tc.diagnosisText}}},
			}

			f := newAgentExecutorFixture(t, turns...)
			transport := newLongitudinalInvestigationLaneClient(f.client)
			for p, c := range tc.fileReads {
				transport.fsReads[p] = c
			}
			f.executor.tools = NewCommandLane(transport, client.Persona{})

			f.req.Assignment.ScenarioId = bp.ScenarioID
			f.req.ScenarioInput = bp.Input
			f.req.ScenarioGold = bp.Gold
			f.req.ScenarioTools = ScenarioToolExpectations{
				AllowedTools:     bp.AllowedTools,
				TrajectoryPolicy: bp.TrajectoryPolicy,
			}

			res, err := f.execute()
			require.NoError(t, err)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus())

			scores := make(map[string]float64)
			for _, score := range res.GetDecomposedScores() {
				scores[score.GetDimension()] = score.GetValue()
			}
			assert.Equal(t, 1.0, scores[MetricDiagnosisCorrectness], "diagnosis correctness should pass")
			assert.Equal(t, 1.0, scores[MetricEvidenceBackedConclusion], "conclusion must be evidence backed")
			assert.GreaterOrEqual(t, scores[MetricRetrievalHitRate], 0.0)
			assert.Equal(t, 3.0, scores[MetricStepsToDiagnosis])
		})
	}
}
