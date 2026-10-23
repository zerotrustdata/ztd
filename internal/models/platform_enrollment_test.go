// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestPlatformEnrollmentTransitionValidateAllowsOnlyLifecycleEdges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		from PlatformEnrollmentState
		to   PlatformEnrollmentState
		err  error
	}{
		{name: "pending to approved", from: PlatformEnrollmentStatePending, to: PlatformEnrollmentStateApproved},
		{name: "pending to denied", from: PlatformEnrollmentStatePending, to: PlatformEnrollmentStateDenied},
		{name: "pending to expired", from: PlatformEnrollmentStatePending, to: PlatformEnrollmentStateExpired},
		{name: "approved to issuing", from: PlatformEnrollmentStateApproved, to: PlatformEnrollmentStateIssuing},
		{name: "approved to expired", from: PlatformEnrollmentStateApproved, to: PlatformEnrollmentStateExpired},
		{name: "issuing to completed", from: PlatformEnrollmentStateIssuing, to: PlatformEnrollmentStateCompleted},
		{name: "issuing lease rollback", from: PlatformEnrollmentStateIssuing, to: PlatformEnrollmentStateApproved},
		{name: "completed to revoked", from: PlatformEnrollmentStateCompleted, to: PlatformEnrollmentStateRevoked},
		{name: "issuing to expired", from: PlatformEnrollmentStateIssuing, to: PlatformEnrollmentStateExpired},
		{name: "completed cannot reopen", from: PlatformEnrollmentStateCompleted, to: PlatformEnrollmentStateApproved, err: constants.ErrPlatformEnrollmentInvalidState},
		{name: "denied is terminal", from: PlatformEnrollmentStateDenied, to: PlatformEnrollmentStatePending, err: constants.ErrPlatformEnrollmentInvalidState},
		{name: "revoked is terminal", from: PlatformEnrollmentStateRevoked, to: PlatformEnrollmentStateCompleted, err: constants.ErrPlatformEnrollmentInvalidState},
		{name: "approved cannot deny", from: PlatformEnrollmentStateApproved, to: PlatformEnrollmentStateDenied, err: constants.ErrPlatformEnrollmentInvalidState},
		{name: "pending cannot skip approval", from: PlatformEnrollmentStatePending, to: PlatformEnrollmentStateIssuing, err: constants.ErrPlatformEnrollmentInvalidState},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			transition := PlatformEnrollmentTransition{RequestID: "req-1", From: tt.from, To: tt.to}
			if tt.from == PlatformEnrollmentStateIssuing || tt.to == PlatformEnrollmentStateIssuing {
				transition.LeaseOwner = "lease-1"
			}
			assert.ErrorIs(t, transition.Validate(), tt.err)
		})
	}
}

func TestPlatformEnrollmentTransitionValidateRequiresTheIssuanceLease(t *testing.T) {
	t.Parallel()
	acquire := PlatformEnrollmentTransition{RequestID: "req-1", From: PlatformEnrollmentStateApproved, To: PlatformEnrollmentStateIssuing}
	require.ErrorIs(t, acquire.Validate(), constants.ErrPlatformEnrollmentInvalidState, "acquiring names the new holder")

	release := PlatformEnrollmentTransition{RequestID: "req-1", From: PlatformEnrollmentStateIssuing, To: PlatformEnrollmentStateCompleted}
	require.ErrorIs(t, release.Validate(), constants.ErrPlatformEnrollmentInvalidState, "leaving issuing is fenced on the holder")

	release.LeaseOwner = "lease-1"
	require.NoError(t, release.Validate())

	outsideLease := PlatformEnrollmentTransition{RequestID: "req-1", From: PlatformEnrollmentStateCompleted, To: PlatformEnrollmentStateRevoked, LeaseOwner: "lease-1"}
	require.ErrorIs(t, outsideLease.Validate(), constants.ErrPlatformEnrollmentInvalidState, "a lease fences only issuance")

	assert.ErrorIs(t, PlatformEnrollmentTransition{From: PlatformEnrollmentStatePending, To: PlatformEnrollmentStateApproved}.Validate(),
		constants.ErrPlatformEnrollmentRequestIDRequired)
}

func TestPlatformEnrollmentStateExpiresWithRequest(t *testing.T) {
	t.Parallel()
	assert.True(t, PlatformEnrollmentStatePending.ExpiresWithRequest())
	assert.True(t, PlatformEnrollmentStateApproved.ExpiresWithRequest())
	assert.False(t, PlatformEnrollmentStateIssuing.ExpiresWithRequest(), "issuance is bounded by its lease")
	assert.False(t, PlatformEnrollmentStateCompleted.ExpiresWithRequest())
}

func TestPlatformEnrollmentCreateRequestValidateShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		request PlatformEnrollmentCreateRequest
		wantErr error
	}{
		{
			name: "dashboard accepts app payload",
			request: PlatformEnrollmentCreateRequest{
				ComponentKind: PlatformComponentDashboard,
				InstanceID:    "dashboard-1",
				Hostname:      "dashboard-host",
				App:           &PlatformAppCSRPayload{CSRPEM: "csr"},
			},
		},
		{
			name: "operator accepts operator payload and metadata",
			request: PlatformEnrollmentCreateRequest{
				ComponentKind:     PlatformComponentOperator,
				InstanceID:        "operator-1",
				Hostname:          "operator-host",
				SystemFingerprint: "fingerprint",
				Operator: &PlatformOperatorCSRPayload{
					OperatorCSRPEM: "operator-csr",
					CLICSRPEM:      "cli-csr",
				},
			},
		},
		{
			name: "operator accepts a deployment launch UUID",
			request: PlatformEnrollmentCreateRequest{
				ComponentKind:     PlatformComponentOperator,
				InstanceID:        "operator-1",
				Hostname:          "operator-host",
				SystemFingerprint: "fingerprint",
				DeploymentID:      "35fe96f6-cb3c-4e7e-a392-ed72e84ac9ad",
				Operator: &PlatformOperatorCSRPayload{
					OperatorCSRPEM: "operator-csr",
					CLICSRPEM:      "cli-csr",
				},
			},
		},
		{
			name: "rejects a deployment ID that is not a UUID",
			request: PlatformEnrollmentCreateRequest{
				ComponentKind:     PlatformComponentOperator,
				InstanceID:        "operator-1",
				Hostname:          "operator-host",
				SystemFingerprint: "fingerprint",
				DeploymentID:      "not-a-uuid",
				Operator: &PlatformOperatorCSRPayload{
					OperatorCSRPEM: "operator-csr",
					CLICSRPEM:      "cli-csr",
				},
			},
			wantErr: constants.ErrPlatformEnrollmentInvalidDeploymentID,
		},
		{
			name: "rejects unknown component",
			request: PlatformEnrollmentCreateRequest{
				ComponentKind: "unknown",
				InstanceID:    "unknown-1",
				Hostname:      "unknown-host",
				App:           &PlatformAppCSRPayload{CSRPEM: "csr"},
			},
			wantErr: constants.ErrPlatformEnrollmentInvalidComponent,
		},
		{
			name: "rejects missing instance ID",
			request: PlatformEnrollmentCreateRequest{
				ComponentKind: PlatformComponentDashboard,
				Hostname:      "dashboard-host",
				App:           &PlatformAppCSRPayload{CSRPEM: "csr"},
			},
			wantErr: constants.ErrPlatformEnrollmentInstanceIDRequired,
		},
		{
			name: "rejects mismatched app payload",
			request: PlatformEnrollmentCreateRequest{
				ComponentKind: PlatformComponentOperator,
				InstanceID:    "operator-1",
				Hostname:      "operator-host",
				App:           &PlatformAppCSRPayload{CSRPEM: "csr"},
			},
			wantErr: constants.ErrPlatformEnrollmentInvalidPayload,
		},
		{
			name: "rejects both payloads",
			request: PlatformEnrollmentCreateRequest{
				ComponentKind: PlatformComponentDashboard,
				InstanceID:    "dashboard-1",
				Hostname:      "dashboard-host",
				App:           &PlatformAppCSRPayload{CSRPEM: "csr"},
				Operator:      &PlatformOperatorCSRPayload{OperatorCSRPEM: "operator-csr", CLICSRPEM: "cli-csr"},
			},
			wantErr: constants.ErrPlatformEnrollmentInvalidPayload,
		},
		{
			name: "rejects operator without fingerprint",
			request: PlatformEnrollmentCreateRequest{
				ComponentKind: PlatformComponentOperator,
				InstanceID:    "operator-1",
				Hostname:      "operator-host",
				Operator:      &PlatformOperatorCSRPayload{OperatorCSRPEM: "operator-csr", CLICSRPEM: "cli-csr"},
			},
			wantErr: constants.ErrPlatformEnrollmentFingerprintRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.request.TokenHash = strings.Repeat("a", 64)
			err := tt.request.ValidateShape()
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestPlatformEnrollmentCreateRequestRequiresLowercaseSHA256TokenHash(t *testing.T) {
	t.Parallel()
	base := PlatformEnrollmentCreateRequest{
		ComponentKind: PlatformComponentDashboard,
		InstanceID:    "dashboard-1",
		Hostname:      "dashboard-host",
		App:           &PlatformAppCSRPayload{CSRPEM: "csr"},
	}
	tests := []struct {
		name    string
		hash    string
		wantErr error
	}{
		{name: "missing", wantErr: constants.ErrPlatformEnrollmentTokenRequired},
		{name: "uppercase", hash: strings.Repeat("A", 64), wantErr: constants.ErrPlatformEnrollmentInvalidToken},
		{name: "short", hash: strings.Repeat("a", 62), wantErr: constants.ErrPlatformEnrollmentInvalidToken},
		{name: "non hexadecimal", hash: strings.Repeat("g", 64), wantErr: constants.ErrPlatformEnrollmentInvalidToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := base
			req.TokenHash = tt.hash
			assert.ErrorIs(t, req.ValidateShape(), tt.wantErr)
		})
	}
}

func TestPlatformEnrollmentComponentCanonicalName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind PlatformComponentKind
		name string
		err  error
	}{
		{kind: PlatformComponentDashboard, name: PlatformDashboardName},
		{kind: PlatformComponentOperator, name: PlatformOperatorName},
		{kind: "unknown", err: constants.ErrPlatformEnrollmentInvalidComponent},
	}

	for _, tt := range tests {
		name, err := tt.kind.CanonicalName()
		assert.Equal(t, tt.name, name)
		assert.ErrorIs(t, err, tt.err)
	}
}

func TestValidateAppNameRejectsReservedPlatformNames(t *testing.T) {
	t.Parallel()
	reserved := []string{
		"ztd", "ztdg", "ztdo", "ztde", "ztdd",
		"ztd-gateway", "ztd-operator", "ztd-ensemble", "ztd-dashboard", "ztd-cli",
		"operator", "ensemble", "dashboard", "gateway", "cli", "admin", "system",
	}
	for _, name := range reserved {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAppName(name)
			require.ErrorIs(t, err, constants.ErrPlatformEnrollmentInvalidAppName)
			require.ErrorIs(t, err, constants.ErrPlatformEnrollmentReservedIdentity)

			request := PlatformEnrollmentCreateRequest{
				TokenHash:     PlatformEnrollmentTokenHash("token"),
				ComponentKind: PlatformComponentApplication,
				AppName:       name,
				InstanceID:    "app-1",
				Hostname:      "app.local",
				App:           &PlatformAppCSRPayload{CSRPEM: "csr"},
			}
			assert.ErrorIs(t, request.ValidateShape(), constants.ErrPlatformEnrollmentReservedIdentity)
		})
	}
}

func TestValidateAppNameEnforcesFormatAndLength(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		app  string
		err  error
	}{
		{name: "empty", app: "", err: constants.ErrPlatformEnrollmentAppNameRequired},
		{name: "lowercase with dash", app: "my-agent-1"},
		{name: "dotted", app: "custom.app"},
		{name: "eval identity", app: constants.EvaluationAppName},
		{name: "max length", app: strings.Repeat("a", constants.PlatformEnrollmentMaxAppNameBytes)},
		{name: "over max length", app: strings.Repeat("a", constants.PlatformEnrollmentMaxAppNameBytes+1), err: constants.ErrPlatformEnrollmentInvalidAppName},
		{name: "uppercase", app: "INVALID-UPPER", err: constants.ErrPlatformEnrollmentInvalidAppName},
		{name: "slash", app: "app/slash", err: constants.ErrPlatformEnrollmentInvalidAppName},
		{name: "leading dash", app: "-leading", err: constants.ErrPlatformEnrollmentInvalidAppName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.err == nil {
				assert.NoError(t, ValidateAppName(tt.app))
				return
			}
			assert.ErrorIs(t, ValidateAppName(tt.app), tt.err)
		})
	}
}
