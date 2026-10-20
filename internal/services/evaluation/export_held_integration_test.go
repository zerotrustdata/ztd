// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation

import (
	"context"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// HeldAssignmentContextForTest exposes the controller's held-context lookup to
// the real-stack tests in package evaluation_test.
func HeldAssignmentContextForTest(ctx context.Context, c *CampaignController, campaignID string, assignment *evalv1.EvaluationAssignment) (*AgentTrace, error) {
	return c.heldAssignmentContext(ctx, campaignID, assignment)
}

// IsRecoverableAssignmentExecutionErrorForTest exposes the recoverable
// assignment execution classification to the real-stack tests.
func IsRecoverableAssignmentExecutionErrorForTest(err error) bool {
	return isRecoverableAssignmentExecutionError(err)
}
