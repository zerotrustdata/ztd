// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import "time"

// OperatorDeploymentPhase is the externally observable enrollment/startup
// progress of one Operator working directory.
type OperatorDeploymentPhase string

const (
	// OperatorDeploymentPhaseRequesting: the enrollment keys exist and the
	// request is being submitted. Fingerprints is set; no request ID yet.
	OperatorDeploymentPhaseRequesting OperatorDeploymentPhase = "requesting"
	// OperatorDeploymentPhasePendingApproval: the enrollment request exists and
	// waits for an owner decision. RequestID is set.
	OperatorDeploymentPhasePendingApproval OperatorDeploymentPhase = "pending_approval"
	// OperatorDeploymentPhaseEnrolled: credentials and the Operator session are
	// issued. OperatorSessionID is set.
	OperatorDeploymentPhaseEnrolled OperatorDeploymentPhase = "enrolled"
	// OperatorDeploymentPhaseReady: this process established its command
	// subscription. OperatorSessionID is set.
	OperatorDeploymentPhaseReady OperatorDeploymentPhase = "ready"
	// OperatorDeploymentPhaseFailed: enrollment or startup failed. Error is set.
	OperatorDeploymentPhaseFailed OperatorDeploymentPhase = "failed"
)

// OperatorDeploymentState is the non-secret deployment state an Operator
// persists for the CLI that launched it. It never carries the requester token
// or private keys. LaunchID binds progress to the deploying invocation without
// depending on synchronized clocks between hosts. Fingerprints are the public
// CSR fingerprints of the worker's enrollment request: the launch ID is not a
// secret, so a deploying CLI approves only a request carrying these keys.
type OperatorDeploymentState struct {
	LaunchID          string                            `json:"launch_id,omitempty"`
	Phase             OperatorDeploymentPhase           `json:"phase"`
	RequestID         string                            `json:"request_id,omitempty"`
	Fingerprints      PlatformEnrollmentCSRFingerprints `json:"fingerprints,omitzero"`
	OperatorSessionID string                            `json:"operator_session_id,omitempty"`
	Error             string                            `json:"error,omitempty"`
	UpdatedAt         time.Time                         `json:"updated_at"`
}
