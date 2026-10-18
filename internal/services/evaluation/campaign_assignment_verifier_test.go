// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// The system lane has no executor, so a heterogeneous assignment can never
// verify: an otherwise well-bound result fails closed on the lane alone.
func TestCampaignAssignmentVerifier_FailsClosedOnHeterogeneousAssignment(t *testing.T) {
	t.Parallel()
	req := heterogeneousAssignmentExecutionRequest(t, mustHeterogeneousStack(t), testHeterogeneousVariants())
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    req.Assignment.GetAssignmentId(),
		RunId:           req.Assignment.GetRunId(),
		CampaignId:      req.Assignment.GetCampaignId(),
		Lane:            req.Assignment.GetLane(),
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
	}
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest

	verifier := NewCampaignAssignmentVerifier(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() })
	report, err := verifier.Verify(context.Background(), CampaignAssignmentVerificationRequest{
		Assignment: req.Assignment,
		Result:     result,
	})
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
	assert.Equal(t, []string{"heterogeneous assignment: " + constants.ErrEvaluationSystemLaneUnavailable.Error()}, report.GetFailureReasons())
}

func TestCampaignAssignmentVerifier_FailsClosedOnModelRoleAssignment(t *testing.T) {
	t.Parallel()
	variants := testHeterogeneousVariants()
	stack := mustHeterogeneousStack(t)
	req := heterogeneousAssignmentExecutionRequest(t, stack, variants)
	modelRole := proto.Clone(req.Assignment).(*evalv1.EvaluationAssignment)
	modelRole.Target = nil
	verifier := NewCampaignAssignmentVerifier(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() })
	report, err := verifier.Verify(context.Background(), CampaignAssignmentVerificationRequest{
		Assignment: modelRole,
		Result:     &evalv1.EvaluationAssignmentResult{AssignmentId: modelRole.GetAssignmentId()},
	})
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
	// A model-role assignment is verified against its persisted agent trace, so
	// a result with no trace is rejected rather than accepted as formation evidence.
	assert.Contains(t, report.GetFailureReasons(), "agent trace is required for homogeneous verification")
}
