// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// PlatformEnrollmentDecisionClient is the authenticated owner CLI client a
// platform enrollment decision is read and posted through.
type PlatformEnrollmentDecisionClient interface {
	Get(path string) ([]byte, error)
	Post(path string, body interface{}) ([]byte, error)
}

// PostPlatformEnrollmentBatchDecision sends one atomic, governed decision.
func PostPlatformEnrollmentBatchDecision(client PlatformEnrollmentDecisionClient, req models.PlatformEnrollmentBatchDecisionRequest) (*models.PlatformEnrollmentBatchDecisionResponse, error) {
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("validate batch decision: %w", err)
	}
	body, err := client.Post(constants.APIPaths.AuthPlatformEnrollmentBatchDecision, req)
	if err != nil {
		return nil, fmt.Errorf("post batch decision: %w", err)
	}
	var suspension models.PlatformEnrollmentL3SuspensionResponse
	if err := json.Unmarshal(body, &suspension); err == nil && suspension.Status == models.PlatformEnrollmentStatusL3Required {
		return nil, &models.PlatformEnrollmentL3RequiredError{Suspension: suspension}
	}
	var resp models.PlatformEnrollmentBatchDecisionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode batch decision: %w", err)
	}
	if len(resp.Requests) != len(req.Requests) || resp.ReceiptID == "" {
		return nil, constants.ErrPlatformEnrollmentInvalidDecision
	}
	for i, result := range resp.Requests {
		expected := models.PlatformEnrollmentStateApproved
		if req.Decision == models.PlatformEnrollmentDecisionDeny {
			expected = models.PlatformEnrollmentStateDenied
		}
		if result.RequestID != req.Requests[i].RequestID || result.State != expected {
			return nil, constants.ErrPlatformEnrollmentInvalidDecision
		}
	}
	return &resp, nil
}

// OwnerAppEnrollmentApprover approves an application's own pending platform
// enrollment request with the authenticated owner CLI identity behind client.
// It decides only the named request, and only when the pending snapshot is an
// application request whose app CSR fingerprint matches the key the requester
// generated. A request that is no longer pending is left alone; the
// requester's status check reports its outcome.
func OwnerAppEnrollmentApprover(client PlatformEnrollmentDecisionClient, reason string) AppEnrollmentApprover {
	return func(_ context.Context, target models.PlatformEnrollmentDecisionTarget) error {
		body, err := client.Get(constants.APIPaths.AuthPlatformEnrollmentPending)
		if err != nil {
			return fmt.Errorf("fetch pending list: %w", err)
		}
		var pending models.PlatformEnrollmentPendingResponse
		if err := json.Unmarshal(body, &pending); err != nil {
			return fmt.Errorf("parse pending list: %w", err)
		}
		for _, req := range pending.Requests {
			if req.RequestID != target.RequestID {
				continue
			}
			if req.ComponentKind != models.PlatformComponentApplication || req.Fingerprints != target.Fingerprints {
				return fmt.Errorf("%w: request %s", constants.ErrPlatformEnrollmentCSRMismatch, req.RequestID)
			}
			_, err := PostPlatformEnrollmentBatchDecision(client, models.PlatformEnrollmentBatchDecisionRequest{
				Requests: []models.PlatformEnrollmentDecisionTarget{target},
				Decision: models.PlatformEnrollmentDecisionApprove,
				Reason:   reason,
			})
			return err
		}
		return nil
	}
}

// WritePlatformEnrollmentSuspension describes a pending L3 decision without
// claiming the governed mutation has completed.
func WritePlatformEnrollmentSuspension(out io.Writer, suspension models.PlatformEnrollmentL3SuspensionResponse) error {
	reason := suspension.Posture.HumanApprovalReason()
	if reason == "" {
		reason = suspension.Message
	}
	_, err := fmt.Fprintf(out, "Submitted for L3 human approval (%s posture).\nReason:       %s\nApproval URL: %s\nTransaction:  %s\nExpires At:   %s\n", suspension.Posture, reason, suspension.ApprovalURL, suspension.TxHash, suspension.ExpiresAt.Format(time.RFC3339))
	return err
}

// EnrollmentFingerprint is one labeled CSR key fingerprint of a pending
// platform enrollment request, as the owner compares it with the workload.
type EnrollmentFingerprint struct {
	Label string
	Value string
}

// PlatformEnrollmentFingerprints returns the non-empty CSR fingerprints of a
// pending platform enrollment request as label/value pairs for display. It is
// shared by 'ztd auth enroll' and the TUI so both show the same keys.
func PlatformEnrollmentFingerprints(fps models.PlatformEnrollmentCSRFingerprints) []EnrollmentFingerprint {
	var out []EnrollmentFingerprint
	if fps.App != "" {
		out = append(out, EnrollmentFingerprint{Label: "App key", Value: fps.App})
	}
	if fps.SigningKey != "" {
		out = append(out, EnrollmentFingerprint{Label: "L2 signing key", Value: fps.SigningKey})
	}
	if fps.Operator != "" {
		out = append(out, EnrollmentFingerprint{Label: "Operator key", Value: fps.Operator})
	}
	if fps.CLI != "" {
		out = append(out, EnrollmentFingerprint{Label: "CLI key", Value: fps.CLI})
	}
	return out
}

// PlatformEnrollmentIssuedIdentity names the identity a completed platform
// enrollment issued: the Operator (or its session) for an Operator, and the
// app policy otherwise.
func PlatformEnrollmentIssuedIdentity(enrollment models.PlatformEnrollmentEnrolledRequest) string {
	switch enrollment.ComponentKind {
	case models.PlatformComponentOperator:
		if enrollment.OperatorID != "" {
			return enrollment.OperatorID
		}
		return enrollment.OperatorSessionID
	default:
		return enrollment.PolicyID
	}
}

// DecodePlatformEnrollmentDecision decodes the Gateway's response to an owner
// decision (AuthPlatformEnrollmentDecision).
func DecodePlatformEnrollmentDecision(body []byte) (*models.PlatformEnrollmentDecisionResponse, error) {
	var resp models.PlatformEnrollmentDecisionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse decision response: %w", err)
	}
	return &resp, nil
}

// DecodePlatformEnrollmentRevoke decodes the Gateway's response to a
// revocation (AuthPlatformEnrollmentRevoke).
func DecodePlatformEnrollmentRevoke(body []byte) (*models.PlatformEnrollmentRevokeResponse, error) {
	var resp models.PlatformEnrollmentRevokeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &resp, nil
}
