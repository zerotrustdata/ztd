// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func ownerApproverPending(t *testing.T, requests ...models.PlatformEnrollmentPendingRequest) []byte {
	t.Helper()
	body, err := json.Marshal(models.PlatformEnrollmentPendingResponse{Requests: requests})
	require.NoError(t, err)
	return body
}

func ownerApproverTarget() models.PlatformEnrollmentDecisionTarget {
	return models.PlatformEnrollmentDecisionTarget{
		RequestID:    "req-eval",
		Fingerprints: models.PlatformEnrollmentCSRFingerprints{App: "sha256:app"},
	}
}

func TestOwnerAppEnrollmentApprover_ApprovesMatchingRequest(t *testing.T) {
	t.Parallel()
	target := ownerApproverTarget()
	decision, err := json.Marshal(models.PlatformEnrollmentBatchDecisionResponse{
		ReceiptID: "receipt-1",
		Requests:  []models.PlatformEnrollmentDecisionResponse{{RequestID: target.RequestID, State: models.PlatformEnrollmentStateApproved}},
	})
	require.NoError(t, err)
	client := &cmdtest.MockAPIClient{
		GetResp: ownerApproverPending(t, models.PlatformEnrollmentPendingRequest{
			RequestID:     target.RequestID,
			ComponentKind: models.PlatformComponentApplication,
			Fingerprints:  target.Fingerprints,
		}),
		PostResp: decision,
	}

	require.NoError(t, auth.OwnerAppEnrollmentApprover(client, "note")(context.Background(), target))

	require.Len(t, client.PostCalls, 1)
	assert.Equal(t, constants.APIPaths.AuthPlatformEnrollmentBatchDecision, client.PostCalls[0].Path)
	posted, ok := client.PostCalls[0].Body.(models.PlatformEnrollmentBatchDecisionRequest)
	require.True(t, ok)
	assert.Equal(t, models.PlatformEnrollmentDecisionApprove, posted.Decision)
	assert.Equal(t, []models.PlatformEnrollmentDecisionTarget{target}, posted.Requests)
	assert.Equal(t, "note", posted.Reason)
}

func TestOwnerAppEnrollmentApprover_RejectsFingerprintMismatch(t *testing.T) {
	t.Parallel()
	target := ownerApproverTarget()
	client := &cmdtest.MockAPIClient{GetResp: ownerApproverPending(t, models.PlatformEnrollmentPendingRequest{
		RequestID:     target.RequestID,
		ComponentKind: models.PlatformComponentApplication,
		Fingerprints:  models.PlatformEnrollmentCSRFingerprints{App: "sha256:other"},
	})}

	err := auth.OwnerAppEnrollmentApprover(client, "")(context.Background(), target)

	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentCSRMismatch)
	assert.Empty(t, client.PostCalls)
}

func TestOwnerAppEnrollmentApprover_RejectsSigningFingerprintMismatch(t *testing.T) {
	t.Parallel()
	target := ownerApproverTarget()
	target.Fingerprints.SigningKey = "expected-signing-key"
	client := &cmdtest.MockAPIClient{GetResp: ownerApproverPending(t, models.PlatformEnrollmentPendingRequest{
		RequestID: target.RequestID, ComponentKind: models.PlatformComponentApplication,
		Fingerprints: models.PlatformEnrollmentCSRFingerprints{App: target.Fingerprints.App, SigningKey: "substituted-signing-key"},
	})}
	require.ErrorIs(t, auth.OwnerAppEnrollmentApprover(client, "")(t.Context(), target), constants.ErrPlatformEnrollmentCSRMismatch)
	require.Empty(t, client.PostCalls)
}

func TestOwnerAppEnrollmentApprover_RejectsNonApplicationRequest(t *testing.T) {
	t.Parallel()
	target := ownerApproverTarget()
	client := &cmdtest.MockAPIClient{GetResp: ownerApproverPending(t, models.PlatformEnrollmentPendingRequest{
		RequestID:     target.RequestID,
		ComponentKind: models.PlatformComponentOperator,
		Fingerprints:  target.Fingerprints,
	})}

	err := auth.OwnerAppEnrollmentApprover(client, "")(context.Background(), target)

	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentCSRMismatch)
	assert.Empty(t, client.PostCalls)
}

func TestOwnerAppEnrollmentApprover_LeavesDecidedRequestAlone(t *testing.T) {
	t.Parallel()
	client := &cmdtest.MockAPIClient{GetResp: ownerApproverPending(t)}

	require.NoError(t, auth.OwnerAppEnrollmentApprover(client, "")(context.Background(), ownerApproverTarget()))
	assert.Empty(t, client.PostCalls)
}
