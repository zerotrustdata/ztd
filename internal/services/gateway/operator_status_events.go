// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// operatorStatusProducerID is the producer_id stamped into SSE event rows the
// Gateway emits for Operator status transitions.
const operatorStatusProducerID = "ztd-gateway-operator-status"

// operatorStatusEvents maps each Operator status the Gateway pushes to the
// status.updated event that announces the transition into it. Bound is a view of
// an active Operator whose bound_web_session_id is set, not a status the Gateway
// stores or announces, so it, available and unavailable have no event and
// Publish rejects them. Offline is announced when an enrollment claims or
// creates the Operator: it is enrolled but has not yet
// subscribed to its command channel, so it is not ready for commands. Active is
// announced only when that subscription is established
// (OperatorCommandSubscribed), so active means ready to receive commands.
// Stale is never stored or announced as a transition (it is derived at read
// time); it is here because OperatorCommandSubscribed reports an Operator's
// current, derived status.
var operatorStatusEvents = map[constants.OperatorStatus]constants.EventType{
	constants.OperatorStatusOffline:    constants.EventOperatorStatusUpdatedOffline,
	constants.OperatorStatusActive:     constants.EventOperatorStatusUpdatedActive,
	constants.OperatorStatusStale:      constants.EventOperatorStatusUpdatedStale,
	constants.OperatorStatusStopped:    constants.EventOperatorStatusUpdatedStopped,
	constants.OperatorStatusTerminated: constants.EventOperatorStatusUpdatedTerminated,
}

// OperatorStatusTransition is an Operator status change the Gateway has
// already persisted. Status is the new status; UserID is the owner whose
// dashboard sessions receive the event.
//
// DeploymentID and OperatorSessionID are set only for the readiness
// announcement of a worker launched by `operator deploy` (see
// OperatorCommandSubscribed); the CLI that launched the worker matches on them.
type OperatorStatusTransition struct {
	OperatorID        string
	UserID            string
	Name              string
	Status            constants.OperatorStatus
	DeploymentID      string
	OperatorSessionID string
}

// operatorDocument loads the persisted Operator document, or returns
// constants.ErrNotFound when no such Operator exists.
func (s *DocumentStoreService) operatorDocument(ctx context.Context, operatorID string) (*operatorv1.OperatorDocument, error) {
	doc, err := s.DocGet(ctx, marshaler.CollectionName(constants.CollectionOperators), operatorID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("%w: operator %s", constants.ErrNotFound, operatorID)
	}
	op, err := models.OperatorDocumentFromStore(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrDocumentStoreUnmarshalDocument, err)
	}
	return op, nil
}

// OperatorStatusTransition builds the transition of operatorID into status from
// the persisted Operator document, for a caller that knows the Operator only by
// id. It returns constants.ErrNotFound when no such Operator exists.
func (s *DocumentStoreService) OperatorStatusTransition(ctx context.Context, operatorID string, status constants.OperatorStatus) (OperatorStatusTransition, error) {
	op, err := s.operatorDocument(ctx, operatorID)
	if err != nil {
		return OperatorStatusTransition{}, err
	}
	return OperatorStatusTransition{OperatorID: operatorID, UserID: op.GetUserId(), Name: op.GetName(), Status: status}, nil
}

// OperatorCommandSubscribed records that the Operator's command channel for
// sessionID is established: the Gateway broker has registered the Operator's
// own cmd:<operator_id>:<session_id> subscription, so a command dispatched from
// now on is delivered to it. deploymentID is the launch the worker presented on
// its pub/sub connection, empty for an Operator not launched by `operator deploy`.
//
// An enrolled Operator is stored offline until this point; this is the one
// transition into active, so active means ready to receive commands. The
// transition is announced once, carrying deploymentID and sessionID for the CLI
// that launched the worker. An Operator in any other status keeps it: a stopped
// or stale Operator is not revived by subscribing, and an already active one has
// no transition. A deploy-launched worker is still told its current status, so a
// stopped or stale Operator is reported as such rather than as ready. Nothing is
// announced when sessionID is no longer the Operator's session.
func (s *DocumentStoreService) OperatorCommandSubscribed(ctx context.Context, operatorID, sessionID, deploymentID string) error {
	op, err := s.operatorDocument(ctx, operatorID)
	if err != nil {
		return err
	}
	if op.GetOperatorSessionId() != sessionID {
		s.logger.Info("operator command subscription not announced: session superseded",
			"operator_id", operatorID, "subscribed_session_id", sessionID,
			"current_session_id", op.GetOperatorSessionId(), "deployment_id", deploymentID)
		return nil
	}
	status := constants.OperatorStatus(op.GetStatus())
	if status == constants.OperatorStatusOffline {
		activated, err := s.activateSubscribedOperator(ctx, operatorID)
		if err != nil {
			return err
		}
		if activated {
			status = constants.OperatorStatusActive
		} else {
			// The Operator left offline between the read and the write; report
			// what it is now, to the launching CLI only.
			if op, err = s.operatorDocument(ctx, operatorID); err != nil {
				return err
			}
			status = constants.OperatorStatus(op.GetStatus())
			if deploymentID == "" {
				return nil
			}
		}
	} else if deploymentID == "" {
		return nil
	}
	s.NotifyOperatorStatusChanged(ctx, OperatorStatusTransition{
		OperatorID:        operatorID,
		UserID:            op.GetUserId(),
		Name:              op.GetName(),
		Status:            status,
		DeploymentID:      deploymentID,
		OperatorSessionID: sessionID,
	})
	return nil
}

// activateSubscribedOperator moves operatorID from offline to active in one
// conditional write, so a concurrent stop or termination is never overwritten.
// It reports whether this call made the transition.
func (s *DocumentStoreService) activateSubscribedOperator(ctx context.Context, operatorID string) (bool, error) {
	update, err := json.Marshal(struct {
		Status string `json:"status"`
	}{Status: string(constants.OperatorStatusActive)})
	if err != nil {
		return false, fmt.Errorf("%w: %w", constants.ErrDocumentStoreMarshalDocument, err)
	}
	applied, err := s.DocConditionalUpdate(ctx, marshaler.CollectionName(constants.CollectionOperators), operatorID, update, "status", string(constants.OperatorStatusOffline))
	if err != nil {
		return false, fmt.Errorf("activate operator %s: %w", operatorID, err)
	}
	return applied, nil
}

// OperatorStatusObserver is told about each persisted Operator status
// transition and each recorded heartbeat, the liveness the derived stale
// status is read from. The operator document stays the source of truth: an
// observer reports the change and must not fail the write that produced it.
type OperatorStatusObserver interface {
	OperatorStatusChanged(context.Context, OperatorStatusTransition)
	OperatorHeartbeatRecorded(ctx context.Context, operatorID string)
}

// OperatorStatusPublisher pushes Operator status transitions to the owning
// user's dashboard and CLI sessions as ztd.v1.operator.status.updated.<state> SSE events,
// and each recorded heartbeat to the owner's connected dashboard and CLI
// sessions as an ephemeral ztd.v1.operator.heartbeat.received event.
//
// The events are telemetry, not records: a failed push is logged and never
// fails or rolls back the transition, and a dashboard that missed one
// reconciles against the operator list.
type OperatorStatusPublisher struct {
	docStore  *DocumentStoreService
	publisher *SSEEventPublisher
	logger    *slog.Logger
}

// NewOperatorStatusPublisher creates a publisher that resolves the owner's web
// sessions from docStore and emits through publisher.
func NewOperatorStatusPublisher(docStore *DocumentStoreService, publisher *SSEEventPublisher, logger *slog.Logger) *OperatorStatusPublisher {
	return &OperatorStatusPublisher{docStore: docStore, publisher: publisher, logger: logger}
}

// OperatorStatusChanged implements OperatorStatusObserver. Delivery failures
// are logged because the transition is already persisted.
func (p *OperatorStatusPublisher) OperatorStatusChanged(ctx context.Context, t OperatorStatusTransition) {
	if err := p.Publish(ctx, t); err != nil {
		p.logger.Warn("operator status event not delivered",
			"operator_id", t.OperatorID,
			"status", t.Status,
			"error", err)
	}
}

// Publish emits the status.updated event for t to every unexpired web and CLI
// session of the Operator's owner. It returns constants.ErrOperatorStatusEventUnsupported
// when t.Status has no Gateway-published event. Sessions are delivered
// independently, so one failing session does not hide the event from the rest;
// the returned error joins every per-session failure.
func (p *OperatorStatusPublisher) Publish(ctx context.Context, t OperatorStatusTransition) error {
	eventType, ok := operatorStatusEvents[t.Status]
	if !ok {
		return fmt.Errorf("%w: %q", constants.ErrOperatorStatusEventUnsupported, t.Status)
	}

	sessionIDs, err := ownerWebSessionIDs(ctx, p.docStore, t.UserID)
	if err != nil {
		return fmt.Errorf("operator status event: resolve web sessions of %s: %w", t.UserID, err)
	}

	payload := models.OperatorStatusUpdatedPayload{
		OperatorID:        t.OperatorID,
		Status:            t.Status,
		Name:              t.Name,
		DeploymentID:      t.DeploymentID,
		OperatorSessionID: t.OperatorSessionID,
		Timestamp:         time.Now().UTC(),
	}
	var errs []error
	for _, sessionID := range sessionIDs {
		route := SSERoute{UserID: t.UserID, WebSessionID: sessionID}
		if err := p.publisher.Publish(route, string(eventType), payload, operatorStatusProducerID); err != nil {
			errs = append(errs, fmt.Errorf("web session %s: %w", sessionID, err))
		}
	}

	// The owner's connected CLI sessions hear every transition too: a
	// launch-keyed readiness event (DeploymentID set) is for the CLI that
	// launched the worker, and the TUI re-lists Operators on any of them. Only
	// CLIs with an open stream are addressed; each worker has a CLI session that
	// never connects, and a row per such session would multiply with the fleet.
	// A CLI that connects later reconciles against the Operator list.
	cliIDs, err := ownerConnectedCLISessionIDs(ctx, p.docStore, p.publisher, t.UserID)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("operator status event: resolve cli sessions of %s: %w", t.UserID, err))...)
	}
	for _, cliID := range cliIDs {
		route := SSERoute{UserID: t.UserID, CLISessionID: cliID}
		if err := p.publisher.Publish(route, string(eventType), payload, operatorStatusProducerID); err != nil {
			errs = append(errs, fmt.Errorf("cli session %s: %w", cliID, err))
		}
	}
	return errors.Join(errs...)
}

// OperatorHeartbeatRecorded implements OperatorStatusObserver. Delivery
// failures are logged because the heartbeat is already recorded.
func (p *OperatorStatusPublisher) OperatorHeartbeatRecorded(ctx context.Context, operatorID string) {
	if err := p.PublishHeartbeat(ctx, operatorID); err != nil {
		p.logger.Warn("operator heartbeat event not delivered", "operator_id", operatorID, "error", err)
	}
}

// PublishHeartbeat emits ztd.v1.operator.heartbeat.received for the heartbeat
// just recorded for operatorID to every connected web and CLI session of its
// owner, carrying the last_heartbeat_at and derived stale_at a re-list would
// return. The event is ephemeral, so only a dashboard or CLI with an open
// stream can consume it, and with none open nothing is read: a fleet
// heartbeating to a Gateway nobody is watching costs only the check for open
// streams. A session that connects later gets the same fields from its list.
func (p *OperatorStatusPublisher) PublishHeartbeat(ctx context.Context, operatorID string) error {
	connectedWeb := p.publisher.ConnectedWebSessionIDs()
	connectedCLI := p.publisher.ConnectedCLISessionIDs()
	if len(connectedWeb) == 0 && len(connectedCLI) == 0 {
		return nil
	}
	op, err := p.docStore.operatorDocument(ctx, operatorID)
	if err != nil {
		return fmt.Errorf("operator heartbeat event: %w", err)
	}
	webIDs, err := ownerConnectedWebSessionIDs(ctx, p.docStore, connectedWeb, op.GetUserId())
	if err != nil {
		return fmt.Errorf("operator heartbeat event: resolve web sessions of %s: %w", op.GetUserId(), err)
	}

	payload := models.OperatorHeartbeatReceivedPayload{
		OperatorID:      operatorID,
		LastHeartbeatAt: op.GetLastHeartbeatAt().AsTime(),
	}
	if staleAt := op.GetStaleAt(); staleAt != nil {
		t := staleAt.AsTime()
		payload.StaleAt = &t
	}
	var errs []error
	for _, sessionID := range webIDs {
		route := SSERoute{UserID: op.GetUserId(), WebSessionID: sessionID}
		if err := p.publisher.PublishEphemeral(route, string(constants.EventOperatorHeartbeatReceived), payload); err != nil {
			errs = append(errs, fmt.Errorf("web session %s: %w", sessionID, err))
		}
	}
	// The owner's connected CLI sessions run the TUI, which keeps the Operator
	// list in place from this payload. Only CLIs with an open stream are
	// addressed, as for Publish.
	cliIDs, err := ownerConnectedCLISessionIDs(ctx, p.docStore, p.publisher, op.GetUserId())
	if err != nil {
		errs = append(errs, fmt.Errorf("operator heartbeat event: resolve cli sessions of %s: %w", op.GetUserId(), err))
	}
	for _, cliID := range cliIDs {
		route := SSERoute{UserID: op.GetUserId(), CLISessionID: cliID}
		if err := p.publisher.PublishEphemeral(route, string(constants.EventOperatorHeartbeatReceived), payload); err != nil {
			errs = append(errs, fmt.Errorf("cli session %s: %w", cliID, err))
		}
	}
	return errors.Join(errs...)
}

// ownerWebSessionIDs returns the ids of userID's web sessions that have not
// expired.
func ownerWebSessionIDs(ctx context.Context, docStore *DocumentStoreService, userID string) ([]string, error) {
	docs, err := docStore.DocQuery(ctx, marshaler.CollectionName(constants.CollectionWebSessions), []models.DocFilter{
		{Field: "user_id", Op: "==", Value: json.RawMessage(fmt.Sprintf("%q", userID))},
	}, "", 0)
	if err != nil {
		return nil, err
	}

	nowMs := time.Now().UnixMilli()
	ids := make([]string, 0, len(docs))
	for _, doc := range docs {
		session, err := decodeWebSession(doc)
		if err != nil {
			return nil, err
		}
		if nowMs > session.ExpiresAtUnixMs {
			continue
		}
		ids = append(ids, doc.ID)
	}
	return ids, nil
}

// ownerConnectedWebSessionIDs returns the ids among connected (web sessions
// with an SSE stream open) that belong to userID and have not expired.
func ownerConnectedWebSessionIDs(ctx context.Context, docStore *DocumentStoreService, connected []string, userID string) ([]string, error) {
	nowMs := time.Now().UnixMilli()
	var ids []string
	for _, id := range connected {
		doc, err := docStore.DocGet(ctx, marshaler.CollectionName(constants.CollectionWebSessions), id)
		if err != nil {
			return nil, err
		}
		if doc == nil {
			continue
		}
		session, err := decodeWebSession(doc)
		if err != nil {
			return nil, err
		}
		if session.UserID != userID || nowMs > session.ExpiresAtUnixMs {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// decodeWebSession decodes a stored web session document.
func decodeWebSession(doc *models.Document) (models.WebSession, error) {
	wire, err := json.Marshal(doc.ForWire())
	if err != nil {
		return models.WebSession{}, fmt.Errorf("%w: %w", constants.ErrDocumentStoreMarshalDocument, err)
	}
	var session models.WebSession
	if err := json.Unmarshal(wire, &session); err != nil {
		return models.WebSession{}, fmt.Errorf("%w: %w", constants.ErrDocumentStoreUnmarshalDocument, err)
	}
	return session, nil
}
