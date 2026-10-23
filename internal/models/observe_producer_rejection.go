// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"errors"
	"fmt"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// ObserveProjectionKind names the observe projection a producer update targets.
type ObserveProjectionKind string

const (
	ObserveProjectionKindAgent ObserveProjectionKind = "agent"
	ObserveProjectionKindRun   ObserveProjectionKind = "run"
)

// ObserveTransitionError is a lifecycle transition the projection's state
// machine refused. From and To are the projection kind's lifecycle status wire
// values (AgentLifecycleStatus or RunLifecycleStatus). It unwraps to
// constants.ErrObserveInvalidTransition.
type ObserveTransitionError struct {
	Kind ObserveProjectionKind `json:"kind"`
	ID   string                `json:"id"`
	From string                `json:"from"`
	To   string                `json:"to"`
}

func (e *ObserveTransitionError) Error() string {
	return fmt.Sprintf("%s: %s %s: %s -> %s", constants.ErrObserveInvalidTransition, e.Kind, e.ID, e.From, e.To)
}

func (e *ObserveTransitionError) Unwrap() error { return constants.ErrObserveInvalidTransition }

// ObserveStaleUpdateError is a producer update observed before the state the
// projection already holds. It unwraps to constants.ErrObserveStaleUpdate.
type ObserveStaleUpdateError struct {
	Kind        ObserveProjectionKind `json:"kind"`
	ID          string                `json:"id"`
	ObservedAt  time.Time             `json:"observed_at"`
	ProjectedAt time.Time             `json:"projected_at"`
}

func (e *ObserveStaleUpdateError) Error() string {
	return fmt.Sprintf("%s: %s %s observed at %s, before projected %s", constants.ErrObserveStaleUpdate, e.Kind, e.ID,
		e.ObservedAt.Format(time.RFC3339Nano), e.ProjectedAt.Format(time.RFC3339Nano))
}

func (e *ObserveStaleUpdateError) Unwrap() error { return constants.ErrObserveStaleUpdate }

// ObserveProducerRejectionCode names why the Gateway refused a producer
// request. Each code stands for exactly one sentinel, so a producer recovers
// the sentinel from the wire with errors.Is intact.
type ObserveProducerRejectionCode string

const (
	ObserveProducerRejectionBodyUnreadable          ObserveProducerRejectionCode = "body_unreadable"
	ObserveProducerRejectionBodyTooLarge            ObserveProducerRejectionCode = "body_too_large"
	ObserveProducerRejectionInvalidJSON             ObserveProducerRejectionCode = "invalid_json"
	ObserveProducerRejectionUnsupportedSchema       ObserveProducerRejectionCode = "unsupported_schema_version"
	ObserveProducerRejectionAgentIDRequired         ObserveProducerRejectionCode = "agent_id_required"
	ObserveProducerRejectionRunIDRequired           ObserveProducerRejectionCode = "run_id_required"
	ObserveProducerRejectionCampaignIDRequired      ObserveProducerRejectionCode = "campaign_id_required"
	ObserveProducerRejectionObservedAtRequired      ObserveProducerRejectionCode = "observed_at_required"
	ObserveProducerRejectionAgentDisplayNameMissing ObserveProducerRejectionCode = "agent_display_name_required"
	ObserveProducerRejectionAgentRoleMissing        ObserveProducerRejectionCode = "agent_role_required"
	ObserveProducerRejectionRunDisplayNameMissing   ObserveProducerRejectionCode = "run_display_name_required"
	ObserveProducerRejectionRunKindInvalid          ObserveProducerRejectionCode = "run_kind_invalid"
	ObserveProducerRejectionStatusInvalid           ObserveProducerRejectionCode = "status_invalid"
	ObserveProducerRejectionNegativeTaskCount       ObserveProducerRejectionCode = "negative_task_count"
	ObserveProducerRejectionCompletedExceedsTotal   ObserveProducerRejectionCode = "completed_exceeds_total"
	ObserveProducerRejectionEndBeforeStart          ObserveProducerRejectionCode = "end_before_start"
	ObserveProducerRejectionStopReasonInvalid       ObserveProducerRejectionCode = "stop_reason_invalid"
	ObserveProducerRejectionStopScopeInvalid        ObserveProducerRejectionCode = "stop_scope_invalid"
	ObserveProducerRejectionRouteUserRequired       ObserveProducerRejectionCode = "route_user_required"
	ObserveProducerRejectionRouteSessionRequired    ObserveProducerRejectionCode = "route_session_required"
	ObserveProducerRejectionRouteSessionAmbiguous   ObserveProducerRejectionCode = "route_session_ambiguous"
	ObserveProducerRejectionInvalidTransition       ObserveProducerRejectionCode = "invalid_transition"
	ObserveProducerRejectionStaleUpdate             ObserveProducerRejectionCode = "stale_update"
	ObserveProducerRejectionForbidden               ObserveProducerRejectionCode = "forbidden"
	ObserveProducerRejectionInternal                ObserveProducerRejectionCode = "internal"
)

// observeProducerRejectionCodes is the one code <-> sentinel table for the
// producer boundary. The Gateway classifies with it and producers decode with
// it. Ownership misses are absent: the Gateway renders them as
// constants.ErrForbidden so the response does not disclose record existence.
var observeProducerRejectionCodes = []struct {
	code     ObserveProducerRejectionCode
	sentinel error
}{
	{ObserveProducerRejectionBodyUnreadable, constants.ErrRequestBodyRead},
	{ObserveProducerRejectionBodyTooLarge, constants.ErrPayloadExceedsLimit},
	{ObserveProducerRejectionInvalidJSON, constants.ErrInvalidJSONBody},
	{ObserveProducerRejectionUnsupportedSchema, constants.ErrObserveUnsupportedSchemaVersion},
	{ObserveProducerRejectionAgentIDRequired, constants.ErrObserveAgentIDRequired},
	{ObserveProducerRejectionRunIDRequired, constants.ErrObserveRunIDRequired},
	{ObserveProducerRejectionCampaignIDRequired, constants.ErrObserveCampaignIDRequired},
	{ObserveProducerRejectionObservedAtRequired, constants.ErrObserveObservedAtRequired},
	{ObserveProducerRejectionAgentDisplayNameMissing, constants.ErrObserveAgentDisplayNameRequired},
	{ObserveProducerRejectionAgentRoleMissing, constants.ErrObserveAgentRoleRequired},
	{ObserveProducerRejectionRunDisplayNameMissing, constants.ErrObserveRunDisplayNameRequired},
	{ObserveProducerRejectionRunKindInvalid, constants.ErrObserveRunKindRequired},
	{ObserveProducerRejectionStatusInvalid, constants.ErrObserveStatusInvalid},
	{ObserveProducerRejectionNegativeTaskCount, constants.ErrObserveNegativeTaskCount},
	{ObserveProducerRejectionCompletedExceedsTotal, constants.ErrObserveCompletedExceedsTotal},
	{ObserveProducerRejectionEndBeforeStart, constants.ErrObserveEndBeforeStart},
	{ObserveProducerRejectionStopReasonInvalid, constants.ErrObserveStopReasonInvalid},
	{ObserveProducerRejectionStopScopeInvalid, constants.ErrObserveStopScopeInvalid},
	{ObserveProducerRejectionRouteUserRequired, constants.ErrGatewaySSERouteUserIDRequired},
	{ObserveProducerRejectionRouteSessionRequired, constants.ErrGatewaySSERouteSessionRequired},
	{ObserveProducerRejectionRouteSessionAmbiguous, constants.ErrGatewaySSERouteSessionMutuallyExclusive},
	{ObserveProducerRejectionInvalidTransition, constants.ErrObserveInvalidTransition},
	{ObserveProducerRejectionStaleUpdate, constants.ErrObserveStaleUpdate},
	{ObserveProducerRejectionForbidden, constants.ErrForbidden},
	{ObserveProducerRejectionInternal, constants.ErrInternal},
}

// ObserveProducerRejection is the typed error body of the mTLS producer
// endpoints. Message is for people; Code is the contract. Transition or Stale
// carries the refused projection state for those two codes.
type ObserveProducerRejection struct {
	Message    string                       `json:"error"`
	Code       ObserveProducerRejectionCode `json:"code"`
	Transition *ObserveTransitionError      `json:"transition,omitempty"`
	Stale      *ObserveStaleUpdateError     `json:"stale,omitempty"`
}

// NewObserveProducerRejection classifies a producer failure. An error that
// wraps no rejection sentinel is ObserveProducerRejectionInternal, and its
// message is the bare sentinel so internal detail stays in the Gateway log.
func NewObserveProducerRejection(err error) ObserveProducerRejection {
	for _, entry := range observeProducerRejectionCodes {
		if entry.code == ObserveProducerRejectionInternal || !errors.Is(err, entry.sentinel) {
			continue
		}
		rejection := ObserveProducerRejection{Message: err.Error(), Code: entry.code}
		if entry.code == ObserveProducerRejectionForbidden {
			rejection.Message = constants.ErrForbidden.Error()
		}
		var transition *ObserveTransitionError
		if errors.As(err, &transition) {
			rejection.Transition = transition
		}
		var stale *ObserveStaleUpdateError
		if errors.As(err, &stale) {
			rejection.Stale = stale
		}
		return rejection
	}
	return ObserveProducerRejection{Message: constants.ErrInternal.Error(), Code: ObserveProducerRejectionInternal}
}

// Cause returns the typed error the rejection carries: the refused transition
// or stale update when present, else the code's sentinel. An unknown code is
// constants.ErrObserveRejectionUnknown.
func (r ObserveProducerRejection) Cause() error {
	if r.Transition != nil {
		return r.Transition
	}
	if r.Stale != nil {
		return r.Stale
	}
	for _, entry := range observeProducerRejectionCodes {
		if entry.code == r.Code {
			return entry.sentinel
		}
	}
	return fmt.Errorf("%w: %q", constants.ErrObserveRejectionUnknown, r.Code)
}
