// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"encoding/json"
	"fmt"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
)

// staleStatusJSON is the derived status a silent Operator reads as.
var staleStatusJSON = json.RawMessage(fmt.Sprintf("%q", constants.OperatorStatusStale))

// operatorStaleAtField is the derived stale_at field of an Operator document.
const operatorStaleAtField = "stale_at"

// deriveOperatorStaleness sets stale_at on each of docs that is an active
// remote Operator to the instant its constants.OperatorStaleAfter window
// (derived from the heartbeat interval it declared at session start) runs out,
// and sets its status to stale once now is past it, in the returned document
// only. Any other Operator has stale_at removed, so the field only ever exists
// as a reading. Nothing is written and nothing is announced: staleness is a
// reading of the last heartbeat at now, so the stored status stays active and
// the Operator reads as active again with its next heartbeat. A reader that
// keeps a document (the console) derives the same status from stale_at
// without asking again. It is a no-op for every collection except operators,
// and docs must already carry their heartbeat overlay.
//
// Every reader (registration, auth, SSE, the data API, enrollment, selection)
// goes through DocGet, DocList or DocQuery, so none can observe a silent
// Operator as active.
//
// The embedded Operator is exempt: it is the Gateway's own in-process
// substrate and is live exactly when the Gateway is.
func deriveOperatorStaleness(now time.Time, docs ...*models.Document) error {
	if len(docs) == 0 || docs[0].Collection != marshaler.CollectionName(constants.CollectionOperators) {
		return nil
	}
	for _, doc := range docs {
		deadline, err := operatorStaleDeadline(doc)
		if err != nil {
			return fmt.Errorf("%w: operator %s: %w", constants.ErrOperatorStalenessDerive, doc.ID, err)
		}
		if deadline.IsZero() {
			delete(doc.Data, operatorStaleAtField)
			continue
		}
		staleAt, err := json.Marshal(deadline.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("%w: operator %s: %w", constants.ErrOperatorStalenessDerive, doc.ID, err)
		}
		doc.Data[operatorStaleAtField] = staleAt
		if now.After(deadline) {
			doc.Data["status"] = staleStatusJSON
		}
	}
	return nil
}

// storedActiveRemoteOperator reports from doc's stored status and type fields
// whether it can be an active remote Operator, so only those documents pay for
// a full decode. A field that does not decode as a string is left to the full
// decode to reject.
func storedActiveRemoteOperator(doc *models.Document) bool {
	var status, operatorType string
	if raw, ok := doc.Data["status"]; ok && json.Unmarshal(raw, &status) != nil {
		return true
	}
	if raw, ok := doc.Data["operator_type"]; ok && json.Unmarshal(raw, &operatorType) != nil {
		return true
	}
	return constants.OperatorStatus(status) == constants.OperatorStatusActive && constants.OperatorType(operatorType) == constants.OperatorTypeRemote
}

// operatorStaleDeadline returns the instant after which doc, an active remote
// Operator, reads as stale if it stays silent: its last sign of life plus
// constants.OperatorStaleAfter for the heartbeat interval it declared at
// session start. The last sign of life is the Gateway-stamped
// last_heartbeat_at, falling back to claimed_at and then to the document's
// created_at for an Operator that has not heartbeated yet. The deadline is zero
// for any other Operator, which never reads as stale.
func operatorStaleDeadline(doc *models.Document) (time.Time, error) {
	if !storedActiveRemoteOperator(doc) {
		return time.Time{}, nil
	}
	op, err := models.OperatorDocumentFromStore(doc)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %w", constants.ErrDocumentStoreUnmarshalDocument, err)
	}
	if constants.OperatorStatus(op.GetStatus()) != constants.OperatorStatusActive || constants.OperatorType(op.GetOperatorType()) != constants.OperatorTypeRemote {
		return time.Time{}, nil
	}

	lastSeen := doc.CreatedAt
	if claimedAt := op.GetClaimedAt(); claimedAt != nil {
		lastSeen = claimedAt.AsTime()
	}
	if lastHeartbeatAt := op.GetLastHeartbeatAt(); lastHeartbeatAt != nil {
		lastSeen = lastHeartbeatAt.AsTime()
	}
	return lastSeen.Add(operatorStaleAfter(op)), nil
}

// operatorStaleAfter is op's constants.OperatorStaleAfter window for the
// heartbeat interval it declared at session start.
func operatorStaleAfter(op *operatorv1.OperatorDocument) time.Duration {
	declared := time.Duration(op.GetRuntimeConfig().GetHeartbeatIntervalMs()) * time.Millisecond
	return constants.OperatorStaleAfter(declared)
}

// operatorStatusFilters splits filters for an operators query into the ones
// SQL can apply to the stored document and the status filters that must be
// matched against the derived status. A derived status differs from the
// stored one only where a stored active reads as stale, so every == and !=
// status filter is rechecked after derivation, and the two that a stored
// active can satisfy only once derived (== stale, != active) are not sent to
// SQL at all. Other comparison operators on status stay in SQL.
func operatorStatusFilters(filters []models.DocFilter) (sqlFilters, statusFilters []models.DocFilter) {
	for _, f := range filters {
		if f.Field != "status" || (f.Op != "==" && f.Op != "=" && f.Op != "!=") {
			sqlFilters = append(sqlFilters, f)
			continue
		}
		var status constants.OperatorStatus
		if json.Unmarshal(f.Value, &status) != nil {
			sqlFilters = append(sqlFilters, f)
			continue
		}
		statusFilters = append(statusFilters, f)
		equal := f.Op != "!="
		if (equal && status == constants.OperatorStatusStale) || (!equal && status == constants.OperatorStatusActive) {
			continue
		}
		sqlFilters = append(sqlFilters, f)
	}
	return sqlFilters, statusFilters
}

// matchesOperatorStatus reports whether doc's derived status satisfies every
// one of statusFilters (== and != on status, from operatorStatusFilters).
func matchesOperatorStatus(doc *models.Document, statusFilters []models.DocFilter) bool {
	var status string
	if raw, ok := doc.Data["status"]; ok {
		if json.Unmarshal(raw, &status) != nil {
			return false
		}
	}
	for _, f := range statusFilters {
		var want string
		_ = json.Unmarshal(f.Value, &want) // decodable: operatorStatusFilters checked it
		if (status == want) == (f.Op == "!=") {
			return false
		}
	}
	return true
}
