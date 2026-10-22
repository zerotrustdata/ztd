// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type enrollmentTargetProcessor struct{}

func (*enrollmentTargetProcessor) ProcessEnvelope(context.Context, []byte) (*operatorv1.ActionReceipt, error) {
	return &operatorv1.ActionReceipt{}, nil
}

func TestPlatformEnrollment_EnvelopesBoundToEmbeddedOperator(t *testing.T) {
	actions := []constants.PlatformEnrollmentGovernanceAction{
		constants.PlatformEnrollmentActionCreate,
		constants.PlatformEnrollmentActionDecide,
		constants.PlatformEnrollmentActionIssue,
		constants.PlatformEnrollmentActionPersistPolicy,
		constants.PlatformEnrollmentActionCreateSession,
		constants.PlatformEnrollmentActionRevoke,
	}
	for _, action := range actions {
		t.Run(string(action), func(t *testing.T) {
			proc := &enrollmentTargetProcessor{}
			svc := NewPlatformEnrollmentService(nil, nil, proc, testutil.NewMockStateRootProvider("root-1"), constants.PostureDoctrine, nil, nil, nil, testutil.NewTestLogger())
			env, err := svc.submitEnvelope(context.Background(), action, &commonv1.PlatformEnrollmentGovernancePayload{Action: string(action), RequestId: "request-1"})
			require.NoError(t, err)
			require.Equal(t, string(constants.DocIDEmbeddedOperator), env.OperatorId)
		})
	}
}
