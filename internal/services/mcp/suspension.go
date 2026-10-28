// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package mcp provides Model Context Protocol (MCP) and Agent-to-Agent (A2A) services.
//
// This package contains GatewayService, which is a shared service used in both gateway mode
// and outbound mode for MCP/A2A protocol translation and downstream dispatch. The service is
// truly polymorphic - the same implementation is used in both modes.
//
// For more information on service modes, see docs/architecture/service_modes.md.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	serviceauth "github.com/zerotrustdata/ztd/internal/services/auth"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// EnvelopeSuspender shares the existing suspension persistence and audit owner
// with MCP while allowing dispatch construction before MCP routing is wired.
type EnvelopeSuspender struct {
	store         storage.SuspendedTransactionStore
	audit         AuditEventRecorder
	posture       constants.Posture
	publicBaseURL string
}

func NewEnvelopeSuspender(store storage.SuspendedTransactionStore, audit AuditEventRecorder, posture constants.Posture, publicBaseURL string) (*EnvelopeSuspender, error) {
	if store == nil {
		return nil, constants.ErrSuspendedStoreRequired
	}
	if audit == nil {
		return nil, constants.ErrLocalFirstEventsStoreRequired
	}
	if !posture.Valid() {
		return nil, constants.ErrInvalidPosture
	}
	return &EnvelopeSuspender{store: store, audit: audit, posture: posture, publicBaseURL: publicBaseURL}, nil
}

func (s *EnvelopeSuspender) SuspendGovernanceEnvelope(ctx context.Context, env *commonv1.GovernanceEnvelope) (models.PlatformEnrollmentL3SuspensionResponse, error) {
	return suspendGovernanceEnvelope(ctx, env, s.store, s.audit, s.posture, s.publicBaseURL)
}

func storeSuspension(ctx context.Context, suspendedStore storage.SuspendedTransactionStore, eventsStore AuditEventRecorder, tx *models.SuspendedTransaction) error {
	if suspendedStore == nil {
		return fmt.Errorf("gateway: store suspended transaction %s: %w", tx.TransactionHash, constants.ErrSuspendedStoreRequired)
	}
	if err := suspendedStore.StoreSuspendedTransaction(ctx, tx); err != nil {
		return fmt.Errorf("gateway: store suspended transaction %s: %w", tx.TransactionHash, err)
	}

	// Emit approval.requested to the targeted Operator session's audit chain.
	// This is the opening bookend for the approval lifecycle.
	event := &storage.Event{
		OperatorSessionID: tx.OperatorSessionID,
		Timestamp:         time.Now().UTC(),
		Type:              constants.Event.Operator.Notary.ApprovalRequested,
		ContentText:       fmt.Sprintf("Transaction %s approval requested (tool: %s, expires at: %s)", tx.TransactionHash, tx.ToolName, tx.ExpiresAt.Format(time.RFC3339)),
	}
	if _, err := eventsStore.RecordEvent(event); err != nil {
		auditErr := fmt.Errorf("gateway: record approval requested for transaction %s on session %s: %w", tx.TransactionHash, tx.OperatorSessionID, err)
		if delErr := suspendedStore.DeleteSuspendedTransaction(ctx, tx.TransactionHash); delErr != nil {
			return errors.Join(auditErr, fmt.Errorf("gateway: roll back suspended transaction %s: %w", tx.TransactionHash, delErr))
		}
		return auditErr
	}
	return nil
}

func suspendGovernanceEnvelope(ctx context.Context, envelope *commonv1.GovernanceEnvelope, suspendedStore storage.SuspendedTransactionStore, eventsStore AuditEventRecorder, posture constants.Posture, publicBaseURL string) (models.PlatformEnrollmentL3SuspensionResponse, error) {
	if envelope == nil || strings.TrimSpace(envelope.GetTransactionHash()) == "" || strings.TrimSpace(envelope.GetOperatorSessionId()) == "" {
		return models.PlatformEnrollmentL3SuspensionResponse{}, fmt.Errorf("gateway: suspend governance envelope: %w", constants.ErrMissingRequiredField)
	}
	rawEnvelope, err := (protojson.MarshalOptions{Multiline: false}).Marshal(envelope)
	if err != nil {
		return models.PlatformEnrollmentL3SuspensionResponse{}, fmt.Errorf("gateway: marshal suspended governance envelope %s: %w", envelope.GetTransactionHash(), err)
	}
	userID := strings.TrimSpace(envelope.GetRequestorUserId())
	if userID == "" {
		userID = serviceauth.RequestorUserID(ctx)
	}
	cliSessionID := strings.TrimSpace(envelope.GetCliSessionId())
	if cliSessionID == "" {
		cliSessionID, _ = ctx.Value(constants.ContextKeyCLISessionID).(string)
	}
	toolName := strings.TrimSpace(envelope.GetTargetResource())
	if toolName == "" {
		toolName = strings.TrimSpace(envelope.GetActionType())
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ApprovalRequestTTL)
	txHash := envelope.GetTransactionHash()
	tx := &models.SuspendedTransaction{
		TransactionHash:       txHash,
		Envelope:              json.RawMessage(rawEnvelope),
		CreatedAt:             now,
		ExpiresAt:             expiresAt,
		ToolName:              toolName,
		ToolArguments:         json.RawMessage(envelope.GetPayload()),
		UserID:                userID,
		OperatorID:            strings.TrimSpace(envelope.GetOperatorId()),
		OperatorSessionID:     strings.TrimSpace(envelope.GetOperatorSessionId()),
		SubmitterCLISessionID: cliSessionID,
	}
	if err := storeSuspension(ctx, suspendedStore, eventsStore, tx); err != nil {
		return models.PlatformEnrollmentL3SuspensionResponse{}, err
	}
	approvalURL := publicBaseURL + constants.APIPaths.ApprovePagePrefix + txHash
	return models.PlatformEnrollmentL3SuspensionResponse{
		Status:      models.PlatformEnrollmentStatusL3Required,
		Posture:     posture,
		TxHash:      txHash,
		ApprovalURL: approvalURL,
		ExpiresAt:   expiresAt,
		Message:     posture.HumanApprovalReason(),
	}, nil
}
