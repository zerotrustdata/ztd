// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"log/slog"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// evidenceWindowAnnouncer tells the evidence Operator owner's consumers that
// the Gateway persisted one evidence window.
type evidenceWindowAnnouncer interface {
	WindowRecorded(ctx context.Context, eventType constants.EventType, userID, providerAttemptID string)
}

// EvidenceWindowPublisher pushes evidence-recorded events
// (ztd.v1.inference.model.provenance.attestation.recorded,
// ztd.v1.inference.provider.boundary.observation.recorded) to the owner's
// connected CLI sessions as each Operator completion is ingested, so campaign
// evidence capture waits on events instead of re-reading the read paths.
//
// The events are ephemeral and carry only the provider attempt ID. A failed
// push is logged and never fails ingest; a consumer that missed one reconciles
// by re-reading its pending attempts when its stream reconnects.
type EvidenceWindowPublisher struct {
	docStore  *DocumentStoreService
	publisher *SSEEventPublisher
	logger    *slog.Logger
}

// NewEvidenceWindowPublisher creates a publisher that resolves the owner's
// connected CLI sessions from docStore.
func NewEvidenceWindowPublisher(docStore *DocumentStoreService, publisher *SSEEventPublisher, logger *slog.Logger) *EvidenceWindowPublisher {
	return &EvidenceWindowPublisher{docStore: docStore, publisher: publisher, logger: logger}
}

// WindowRecorded announces the persisted window for providerAttemptID to every
// connected CLI session of userID.
func (p *EvidenceWindowPublisher) WindowRecorded(ctx context.Context, eventType constants.EventType, userID, providerAttemptID string) {
	if p == nil || userID == "" || providerAttemptID == "" {
		return
	}
	cliIDs, err := ownerConnectedCLISessionIDs(ctx, p.docStore, p.publisher, userID)
	if err != nil {
		p.logger.Warn("evidence window event not delivered: resolve cli sessions",
			"event_type", eventType, "provider_attempt_id", providerAttemptID, "user_id", userID, "error", err)
		return
	}
	payload := models.EvidenceWindowRecorded{ProviderAttemptID: providerAttemptID}
	for _, cliID := range cliIDs {
		route := SSERoute{UserID: userID, CLISessionID: cliID}
		if err := p.publisher.PublishEphemeral(route, string(eventType), payload); err != nil {
			p.logger.Warn("evidence window event not delivered",
				"event_type", eventType, "provider_attempt_id", providerAttemptID, "cli_session_id", cliID, "error", err)
		}
	}
}
