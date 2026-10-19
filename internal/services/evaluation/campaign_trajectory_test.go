// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestTrajectoryPolicyDenialRequiresGovernanceRejectionReceipt(t *testing.T) {
	grader := homogeneousGrader{req: AssignmentExecutionRequest{
		ScenarioTools: ScenarioToolExpectations{ForbiddenTools: []string{agentToolRunCommands}},
	}}
	grader.view.Calls = []AgentToolCall{{ToolName: agentToolRunCommands, ErrorType: agentToolErrorSecurity}}
	assert.Equal(t, policyOutcomeNone, grader.derivePolicyOutcome(false), "a security error label alone is not governance evidence")

	signer := oracletest.NewSigner(t)
	receipt := signer.Rejected(t, oracletest.ReceiptOpts{TransactionID: "tx-denied", OperatorID: "operator-1", SessionID: "session-1", ActionType: "EXECUTE_BASH"}, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE)
	grader.view.Calls[0].Receipt = &AgentTraceReceipt{ActionReceipt: receipt}
	assert.Equal(t, policyOutcomeDenied, grader.derivePolicyOutcome(false), "a typed signed rejection receipt is governance evidence")
}

func TestGovernedTrajectoryRejectsDenialLabelsWithoutSignedRejection(t *testing.T) {
	trace := &AgentTrace{ToolCalls: []AgentToolCall{{ToolName: agentToolRunCommands, ErrorType: agentToolErrorSecurity}}}
	view, err := newAgentGradingView(trace, nil)
	assert.NoError(t, err)
	tools := ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_GOVERNED, ForbiddenTools: []string{agentToolRunCommands}}
	result := readTrajectory(tools, ScenarioGoldCriteria{}, view)
	assert.Equal(t, evalv1.EvaluationTrajectoryOutcome_EVALUATION_TRAJECTORY_OUTCOME_DIRECT, result.Outcome)
}
