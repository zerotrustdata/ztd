// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestPlatformEnrollmentController_ContentionAndCancellationErrors(t *testing.T) {
	for _, cause := range []error{constants.ErrSQLiteBusy, context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			logger := testutil.NewTestLogger()
			controller := &PlatformEnrollmentController{logger: logger, responder: response.NewWriter(logger)}
			rr := httptest.NewRecorder()
			err := fmt.Errorf("submit enrollment: %w", errors.Join(constants.ErrPlatformEnrollmentGovernanceRejected, cause))
			controller.writeEnrollmentError(rr, err)
			require.Equal(t, http.StatusServiceUnavailable, rr.Code)
		})
	}
}

func TestPlatformEnrollmentPublicError_SendsOnlyTheSentinel(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		want       error
	}{
		{"wrapped sentinel drops its detail", fmt.Errorf("%w: approving user user-7 is not active", constants.ErrPlatformEnrollmentInvalidDecision), http.StatusForbidden, constants.ErrPlatformEnrollmentInvalidDecision},
		{"store contention", fmt.Errorf("decide: %w", constants.ErrSQLiteBusy), http.StatusServiceUnavailable, constants.ErrServiceUnavailable},
		{"heartbeat interval", fmt.Errorf("%w: 10m exceeds 1m", constants.ErrOperatorHeartbeatIntervalInvalid), http.StatusBadRequest, constants.ErrOperatorHeartbeatIntervalInvalid},
		{"implicit multi-member quorum", fmt.Errorf("platform enrollment: consensus quorum must be explicit for 3 members: %w", constants.ErrConsensusQuorumInvalid), http.StatusBadRequest, constants.ErrConsensusQuorumInvalid},
		{"duplicate consensus member", fmt.Errorf("platform enrollment: %w", constants.ErrConsensusMemberDuplicate), http.StatusBadRequest, constants.ErrConsensusMemberDuplicate},
		{"consensus primary not a member", fmt.Errorf("platform enrollment: %w", constants.ErrConsensusAppNotEnrolled), http.StatusBadRequest, constants.ErrConsensusAppNotEnrolled},
		{"unmatched failure", errors.New("open /var/lib/ztd/gateway.db: permission denied"), http.StatusInternalServerError, constants.ErrInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, public := platformEnrollmentPublicError(tc.err)
			require.Equal(t, tc.wantStatus, status)
			require.Same(t, tc.want, public)
		})
	}
}

func TestPlatformEnrollmentController_ResponseOmitsWrappedDetail(t *testing.T) {
	logger := testutil.NewTestLogger()
	controller := &PlatformEnrollmentController{logger: logger, responder: response.NewWriter(logger)}
	rr := httptest.NewRecorder()
	controller.writeEnrollmentError(rr, fmt.Errorf("%w: request req-9 owner user-7", constants.ErrPlatformEnrollmentRequestNotFound))
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), constants.ErrPlatformEnrollmentRequestNotFound.Error())
	require.NotContains(t, rr.Body.String(), "user-7")
}

func TestOperatorBootstrapErrorFrame_SendsOnlyTheSentinel(t *testing.T) {
	frame := operatorBootstrapErrorFrame(fmt.Errorf("%w: sign operator csr: pkcs11 slot 3 offline", constants.ErrPlatformEnrollmentIssuanceFailed))
	require.Equal(t, models.OperatorBootstrapFrameError, frame.Type)
	require.NotNil(t, frame.Error)
	require.True(t, frame.Error.Retryable)
	require.Equal(t, constants.ErrPlatformEnrollmentIssuanceFailed.Error(), frame.Error.Message)

	frame = operatorBootstrapErrorFrame(errors.New("open /var/lib/ztd/gateway.db: permission denied"))
	require.False(t, frame.Error.Retryable)
	require.Equal(t, constants.ErrInternal.Error(), frame.Error.Message)
}

func TestPlatformEnrollmentController_TokenConflictUsesHTTPConflict(t *testing.T) {
	logger := testutil.NewTestLogger()
	controller := &PlatformEnrollmentController{logger: logger, responder: response.NewWriter(logger)}
	rr := httptest.NewRecorder()
	controller.writeEnrollmentError(rr, constants.ErrPlatformEnrollmentTokenConflict)
	require.Equal(t, http.StatusConflict, rr.Code)
}
