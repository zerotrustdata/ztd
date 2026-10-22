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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

// AuditController handles audit event receipt, event, summary, report, and ingest endpoints.
//
// The audit store holds raw, decrypted content (command lines, file paths, tool
// arguments). Everything this controller returns over HTTP leaves the runtime, so
// every content-bearing field passes through the scrubber first; a disabled
// scrubber withholds the response (constants.ErrEgressScrubberUnavailable).
type AuditController struct {
	cfg            *config.Config
	logger         *slog.Logger
	auditStore     *storage.SQLAuditStore
	auditIngestSvc *AuditIngestService
	responder      *response.Writer
	scrubber       *scrubbing.ScrubbingService
}

// AuditControllerDeps groups all dependencies for AuditController.
type AuditControllerDeps struct {
	Cfg            *config.Config
	Logger         *slog.Logger
	AuditStore     *storage.SQLAuditStore
	AuditIngestSvc *AuditIngestService
	Responder      *response.Writer
	// Scrubber is the egress scrubber for audit content. Required: a nil scrubber
	// fails construction with constants.ErrScrubbingServiceRequired.
	Scrubber *scrubbing.ScrubbingService
}

func newAuditController(d AuditControllerDeps) (*AuditController, error) {
	if d.Scrubber == nil {
		return nil, fmt.Errorf("audit controller: %w", constants.ErrScrubbingServiceRequired)
	}
	return &AuditController{
		cfg:            d.Cfg,
		logger:         d.Logger,
		auditStore:     d.AuditStore,
		auditIngestSvc: d.AuditIngestSvc,
		responder:      d.Responder,
		scrubber:       d.Scrubber,
	}, nil
}

// egressReady reports whether the scrubber can scrub. When it cannot, it writes the
// withheld response and returns false: raw audit content is never returned.
func (c *AuditController) egressReady(w http.ResponseWriter) bool {
	if c.scrubber.IsEnabled() {
		return true
	}
	c.responder.Error(w, http.StatusServiceUnavailable, constants.ErrEgressScrubberUnavailable.Error())
	return false
}

// scrubEventReceiptRecord returns a copy of an event receipt projection with its free-text fields
// (target resource and result summary) scrubbed. The nested signed EventReceipt is
// returned unchanged: its content was scrubbed before it was signed and altering it
// would break the signature.
func (c *AuditController) scrubEventReceiptRecord(ctx context.Context, r *models.EventReceiptRecord) *models.EventReceiptRecord {
	if r == nil {
		return nil
	}
	out := *r
	out.TargetResource = c.scrubber.ScrubText(ctx, r.TargetResource)
	out.ResultSummary = c.scrubber.ScrubText(ctx, r.ResultSummary)
	return &out
}

func (c *AuditController) scrubEventReceiptRecords(ctx context.Context, records []*models.EventReceiptRecord) []*models.EventReceiptRecord {
	out := make([]*models.EventReceiptRecord, len(records))
	for i, r := range records {
		out[i] = c.scrubEventReceiptRecord(ctx, r)
	}
	return out
}

// handleAuditRecords ingests an LFAA audit record and returns operator chain metadata.
func (c *AuditController) handleAuditRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}
	if c.auditIngestSvc == nil {
		c.responder.Error(w, http.StatusServiceUnavailable, constants.ErrInternal.Error())
		return
	}

	var req models.AuditRecordIngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrInvalidJSONBody.Error())
		return
	}

	requestorUserID, _ := r.Context().Value(constants.ContextKeyUserID).(string)
	result, err := c.auditIngestSvc.Ingest(r.Context(), req, requestorUserID)
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, constants.ErrAuditIngestTimeout),
			errors.Is(err, constants.ErrAuditIngestNoDelivery):
			status = http.StatusGatewayTimeout
		case errors.Is(err, constants.ErrRegistrationOperatorNotBelongToUser):
			status = http.StatusForbidden
		}
		c.logger.Error("audit ingest failed", "error", err)
		c.responder.Error(w, status, err.Error())
		return
	}

	c.responder.JSON(w, http.StatusOK, result)
}

// @Summary		Get or list event receipts
// @Description	Returns one canonical protojson EventReceipt when tx_id is set or when investigation_id and action_type uniquely correlate an event receipt. Without those selectors, returns searchable event receipt records with the complete EventReceipt nested under event_receipt.
// @Tags			audit
// @Produce		json
// @Param			tx_id			query		string	false	"Transaction ID for a single canonical EventReceipt"
// @Param			investigation_id	query		string	false	"Investigation ID for a unique correlation lookup"
// @Param			action_type		query		string	false	"Action type required with investigation_id"
// @Param acting_app_id query string false "Acting app identity filter"
// @Param			operator_session_id	query	string	false	"Operator session filter for list results"
// @Param			limit			query		int		false	"Maximum list results"
// @Param			offset			query		int		false	"List offset"
// @Success		200				{object}	models.AuditEventReceiptsResponse	"List wrapper; unique selectors instead return a bare operatorv1.EventReceipt"
// @Failure		400				{string}	string	"Bad Request — conflicting or incomplete selectors"
// @Failure		404				{string}	string	"Event receipt not found"
// @Failure		405				{string}	string	"Method Not Allowed"
// @Failure		409				{string}	string	"Conflict — correlation matched multiple event receipts"
// @Failure		500				{string}	string	"Internal Server Error"
// @Failure		503				{string}	string	"Service Unavailable — egress scrubber unavailable, content withheld"
// @Router			/api/v1/audit/event-receipts [get]
func (c *AuditController) handleAuditEventReceipts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}

	txID := r.URL.Query().Get("tx_id")
	investigationID := r.URL.Query().Get("investigation_id")
	actionType := constants.ActionType(r.URL.Query().Get("action_type"))
	if txID != "" && (investigationID != "" || actionType != "") {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrAuditStoreEventReceiptLookupConflict.Error())
		return
	}
	if investigationID != "" && actionType == "" {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrMissingRequiredField.Error())
		return
	}
	if investigationID == "" && actionType != "" {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrAuditStoreEventReceiptLookupConflict.Error())
		return
	}
	if txID != "" {
		eventReceipt, err := c.auditStore.GetEventReceipt(txID)
		if err != nil {
			c.responder.Error(w, http.StatusInternalServerError, fmt.Errorf("audit_controller: handleAuditEventReceipts: %w", err).Error())
			return
		}
		if eventReceipt == nil {
			c.responder.Error(w, http.StatusNotFound, constants.ErrNotFound.Error())
			return
		}
		if eventReceipt.EventReceipt == nil {
			c.responder.Error(w, http.StatusInternalServerError, constants.ErrMissingRequiredField.Error())
			return
		}
		c.responder.ProtoJSON(w, http.StatusOK, eventReceipt.EventReceipt)
		return
	}
	if investigationID != "" {
		eventReceipt, err := c.auditStore.GetEventReceiptByInvestigationID(investigationID, actionType)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, constants.ErrAuditStoreEventReceiptCorrelationDuplicate) {
				status = http.StatusConflict
			}
			c.responder.Error(w, status, fmt.Errorf("audit_controller: handleAuditEventReceipts: %w", err).Error())
			return
		}
		if eventReceipt == nil {
			c.responder.Error(w, http.StatusNotFound, constants.ErrNotFound.Error())
			return
		}
		if eventReceipt.EventReceipt == nil {
			c.responder.Error(w, http.StatusInternalServerError, constants.ErrMissingRequiredField.Error())
			return
		}
		c.responder.ProtoJSON(w, http.StatusOK, eventReceipt.EventReceipt)
		return
	}

	operatorSessionID := r.URL.Query().Get("operator_session_id")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}
	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil {
			offset = o
		}
	}

	if !c.egressReady(w) {
		return
	}
	eventReceipts, err := c.auditStore.ListEventReceipts(models.AuditScope{OperatorSessionID: operatorSessionID, ActingAppID: r.URL.Query().Get("acting_app_id")}, limit, offset)
	if err != nil {
		c.responder.Error(w, http.StatusInternalServerError, fmt.Errorf("audit_controller: handleAuditEventReceipts: %w", err).Error())
		return
	}

	c.responder.JSON(w, http.StatusOK, models.AuditEventReceiptsResponse{
		Success:       true,
		EventReceipts: c.scrubEventReceiptRecords(r.Context(), eventReceipts),
	})
}

// @Summary		Export event receipts
// @Description	Returns an AuditEventReceiptsResponse list wrapper. Every searchable record embeds the complete canonical protojson event receipt under event_receipt.
// @Tags			audit
// @Produce		json
// @Param			since			query		string	false	"Inclusive event receipt timestamp in RFC3339 format"
// @Param acting_app_id query string false "Acting app identity filter"
// @Param			operator_session_id	query	string	false	"Operator session filter"
// @Param			limit			query		int		false	"Maximum results"
// @Success		200				{object}	models.AuditEventReceiptsResponse
// @Failure		405				{string}	string	"Method Not Allowed"
// @Failure		500				{string}	string	"Internal Server Error"
// @Failure		503				{string}	string	"Service Unavailable — egress scrubber unavailable, content withheld"
// @Router			/api/v1/audit/event-receipts/export [get]
func (c *AuditController) handleAuditEventReceiptsExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}

	sinceStr := r.URL.Query().Get("since")
	limitStr := r.URL.Query().Get("limit")

	since := time.Time{}
	if sinceStr != "" {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = t
		} else if t, err := timesvc.ParseTimestamp(sinceStr); err == nil {
			since = t
		}
	}

	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	if !c.egressReady(w) {
		return
	}
	operatorSessionID := r.URL.Query().Get("operator_session_id")
	var eventReceipts []*models.EventReceiptRecord
	var err error
	if operatorSessionID != "" || r.URL.Query().Get("acting_app_id") != "" {
		eventReceipts, err = c.auditStore.ListEventReceipts(models.AuditScope{OperatorSessionID: operatorSessionID, ActingAppID: r.URL.Query().Get("acting_app_id")}, limit, 0)
	} else {
		eventReceipts, err = c.auditStore.ListEventReceiptsSince(since, limit)
	}
	if err != nil {
		c.responder.Error(w, http.StatusInternalServerError, fmt.Errorf("audit_controller: handleAuditEventReceiptsExport: %w", err).Error())
		return
	}

	c.responder.JSON(w, http.StatusOK, models.AuditEventReceiptsResponse{
		Success:       true,
		EventReceipts: c.scrubEventReceiptRecords(r.Context(), eventReceipts),
	})
}

func (c *AuditController) handleAuditEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}

	if !c.egressReady(w) {
		return
	}

	operatorSessionID := r.URL.Query().Get("operator_session_id")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}
	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil {
			offset = o
		}
	}

	events, err := c.auditStore.GetEvents(models.AuditScope{OperatorSessionID: operatorSessionID, ActingAppID: r.URL.Query().Get("acting_app_id")}, limit, offset)
	if err != nil {
		c.responder.Error(w, http.StatusInternalServerError, fmt.Errorf("audit_controller: handleAuditEvents: %w", err).Error())
		return
	}

	eventRows := make([]models.AuditEventRow, len(events))
	for i, e := range events {
		eventRows[i] = c.toAuditEventRow(r.Context(), e)
	}

	c.responder.JSON(w, http.StatusOK, models.AuditEventsResponse{
		Success: true,
		Events:  eventRows,
		Count:   len(eventRows),
	})
}

// @Summary		List consensus votes for a transaction
// @Description	Returns every recorded L2 consensus member vote (member, outcome, reason, failure) for one governed transaction, oldest first. Reason and failure text is scrubbed.
// @Tags			audit
// @Produce		json
// @Param			tx_id	query		string	true	"Governed transaction ID (the envelope ID event receipts carry)"
// @Success		200		{object}	models.ConsensusVotesResponse
// @Failure		400		{string}	string	"Bad Request — tx_id missing"
// @Failure		405		{string}	string	"Method Not Allowed"
// @Failure		500		{string}	string	"Internal Server Error"
// @Failure		503		{string}	string	"Service Unavailable — egress scrubber unavailable, content withheld"
// @Router			/api/v1/audit/consensus-votes [get]
func (c *AuditController) handleAuditConsensusVotes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}
	if !c.egressReady(w) {
		return
	}
	txID := r.URL.Query().Get("tx_id")
	if txID == "" {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrMissingRequiredField.Error())
		return
	}
	events, err := c.auditStore.GetTransactionEvents(txID, constants.EventConsensusVoteRecorded)
	if err != nil {
		c.responder.Error(w, http.StatusInternalServerError, fmt.Errorf("audit_controller: handleAuditConsensusVotes: %w", err).Error())
		return
	}
	votes := make([]models.ConsensusVoteRecord, 0, len(events))
	for _, e := range events {
		var vote models.ConsensusVoteRecord
		if err := json.Unmarshal([]byte(e.ContentText), &vote); err != nil {
			c.responder.Error(w, http.StatusInternalServerError, fmt.Errorf("audit_controller: handleAuditConsensusVotes: event %d: %w", e.ID, err).Error())
			return
		}
		vote.Reason = c.scrubber.ScrubText(r.Context(), vote.Reason)
		vote.Failure = c.scrubber.ScrubText(r.Context(), vote.Failure)
		votes = append(votes, vote)
	}
	c.responder.JSON(w, http.StatusOK, models.ConsensusVotesResponse{
		Success:       true,
		TransactionID: txID,
		Votes:         votes,
	})
}

// toAuditEventRow converts a storage.Event to the API-facing AuditEventRow. CommandRaw is
// the only content-bearing field (decrypted command lines, file paths, tool arguments) and
// is scrubbed; the rest are identifiers, the event type, a timestamp and an exit code.
// Callers must have checked egressReady first.
func (c *AuditController) toAuditEventRow(ctx context.Context, e *storage.Event) models.AuditEventRow {
	if e == nil {
		return models.AuditEventRow{}
	}
	return models.AuditEventRow{
		ID:                e.ID,
		TransactionID:     e.TransactionID,
		OperatorSessionID: e.OperatorSessionID,
		Timestamp:         e.Timestamp.Format(time.RFC3339),
		Type:              string(e.Type),
		CommandRaw:        c.scrubber.ScrubText(ctx, e.CommandRaw),
		CommandExitCode:   e.CommandExitCode,
	}
}

const maxAuditQueryLimit = 10000

func (c *AuditController) handleAuditSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}

	operatorSessionID := r.URL.Query().Get("operator_session_id")

	// Query events summary
	events, err := c.auditStore.GetEvents(models.AuditScope{OperatorSessionID: operatorSessionID, ActingAppID: r.URL.Query().Get("acting_app_id")}, maxAuditQueryLimit, 0)
	if err != nil {
		c.responder.Error(w, http.StatusInternalServerError, fmt.Errorf("audit_controller: handleAuditSummary: %w", err).Error())
		return
	}

	eventSummary := make(map[string]int)
	for _, event := range events {
		eventSummary[string(event.Type)]++
	}

	// Query event receipts summary
	eventReceipts, err := c.auditStore.ListEventReceipts(models.AuditScope{OperatorSessionID: operatorSessionID, ActingAppID: r.URL.Query().Get("acting_app_id")}, maxAuditQueryLimit, 0)
	if err != nil {
		c.responder.Error(w, http.StatusInternalServerError, fmt.Errorf("audit_controller: handleAuditSummary: %w", err).Error())
		return
	}

	eventReceiptSummary := make(map[string]int)
	for _, eventReceipt := range eventReceipts {
		key := string(eventReceipt.ActionType) + ":" + eventReceipt.Status.String()
		eventReceiptSummary[key]++
	}

	c.responder.JSON(w, http.StatusOK, models.AuditSummaryResponse{
		Success:              true,
		EventsSummary:        eventSummary,
		EventsTotal:          len(events),
		EventReceiptsSummary: eventReceiptSummary,
		EventReceiptsTotal:   len(eventReceipts),
		TotalRecords:         len(events) + len(eventReceipts),
	})
}

func (c *AuditController) handleAuditReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}

	if !c.egressReady(w) {
		return
	}

	operatorSessionID := r.URL.Query().Get("operator_session_id")

	// Fetch events
	events, err := c.auditStore.GetEvents(models.AuditScope{OperatorSessionID: operatorSessionID, ActingAppID: r.URL.Query().Get("acting_app_id")}, maxAuditQueryLimit, 0)
	if err != nil {
		c.responder.Error(w, http.StatusInternalServerError, fmt.Errorf("audit_controller: handleAuditReport: %w", err).Error())
		return
	}

	// Fetch event_receipts
	eventReceipts, err := c.auditStore.ListEventReceipts(models.AuditScope{OperatorSessionID: operatorSessionID, ActingAppID: r.URL.Query().Get("acting_app_id")}, maxAuditQueryLimit, 0)
	if err != nil {
		c.responder.Error(w, http.StatusInternalServerError, fmt.Errorf("audit_controller: handleAuditReport: %w", err).Error())
		return
	}

	// Build comprehensive report
	ctx := r.Context()
	eventRows := make([]json.RawMessage, len(events))
	for i, e := range events {
		data, err := json.Marshal(c.toAuditEventRow(ctx, e))
		if err != nil {
			c.logger.Error("audit_controller: handleAuditReport: marshal event", string(constants.ConnectionStateError), err)
			eventRows[i] = json.RawMessage("null")
			continue
		}
		eventRows[i] = data
	}

	eventReceiptRows := make([]json.RawMessage, len(eventReceipts))
	for i, r := range eventReceipts {
		data, err := json.Marshal(c.scrubEventReceiptRecord(ctx, r))
		if err != nil {
			c.logger.Error("audit_controller: handleAuditReport: marshal event receipt", string(constants.ConnectionStateError), err)
			eventReceiptRows[i] = json.RawMessage("null")
			continue
		}
		eventReceiptRows[i] = data
	}

	report := models.AuditReportData{
		GeneratedAt:        time.Now().Format(time.RFC3339),
		OperatorSessionID:  operatorSessionID,
		ActingAppID:        r.URL.Query().Get("acting_app_id"),
		Events:             eventRows,
		EventsCount:        len(events),
		EventReceipts:      eventReceiptRows,
		EventReceiptsCount: len(eventReceipts),
		TotalRecords:       len(events) + len(eventReceipts),
	}

	c.responder.JSON(w, http.StatusOK, models.AuditReportResponse{
		Success: true,
		Report:  report,
	})
}

func (c *AuditController) handleAuditVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}
	if c.auditStore == nil {
		c.responder.Error(w, http.StatusServiceUnavailable, constants.ErrAuditStoreDisabled.Error())
		return
	}

	fromSeq := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("from_seq")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			c.responder.Error(w, http.StatusBadRequest, "from_seq must be a non-negative integer")
			return
		}
		fromSeq = parsed
	}

	headSeq, headHash, err := c.auditStore.GetAuditChainHead(r.Context())
	if err != nil {
		c.responder.Error(w, http.StatusInternalServerError, fmt.Errorf("audit_controller: handleAuditVerify: %w", err).Error())
		return
	}

	// Verify checks the content behind the chain, not only the hash linkage: it
	// decrypts each event and recomputes its keyed content digest. A locked vault
	// cannot verify content and fails the request closed.
	verifyErr := c.auditStore.VerifyChainContent(r.Context(), fromSeq)
	if errors.Is(verifyErr, constants.ErrAuditStoreVaultLocked) {
		c.responder.Error(w, http.StatusServiceUnavailable, constants.ErrAuditStoreVaultLocked.Error())
		return
	}
	resp := models.AuditVerifyResponse{
		Success:         true,
		OK:              verifyErr == nil,
		VerifiedFromSeq: fromSeq,
		HeadSeq:         headSeq,
		HeadHash:        headHash,
	}
	if verifyErr != nil {
		resp.Error = verifyErr.Error()
		c.responder.JSON(w, http.StatusOK, resp)
		return
	}

	c.responder.JSON(w, http.StatusOK, resp)
}
