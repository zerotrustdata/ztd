// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// executionIDFromMessage resolves the execution_id for a command from the
// inbound payload's execution_id field using strict Protobuf extraction.
// If the payload does not carry one it falls back to the envelope id (msg.ID).
func executionIDFromMessage(msg *PubSubCommandMessage) string {
	payloadMsg, err := unmarshalPayload(msg.EventType, msg.Payload)
	if err != nil {
		return msg.ID
	}

	reflectMsg := payloadMsg.ProtoReflect()
	descriptor := reflectMsg.Descriptor()

	// Try "execution_id" field by name first (Protocol-First reflection)
	fd := descriptor.Fields().ByName("execution_id")
	if fd != nil && fd.Kind() == protoreflect.StringKind {
		val := reflectMsg.Get(fd).String()
		if val != "" {
			return val
		}
	}

	return msg.ID
}

// setExecutionIDOnPayload sets the execution_id field on Protobuf payloads that support it via reflection.
func setExecutionIDOnPayload(payload proto.Message, executionID string) {
	if executionID == "" {
		return
	}
	reflectMsg := payload.ProtoReflect()
	fd := reflectMsg.Descriptor().Fields().ByName("execution_id")
	if fd != nil && fd.Kind() == protoreflect.StringKind {
		reflectMsg.Set(fd, protoreflect.ValueOfString(executionID))
	}
}

// AuditEventRecorder defines the interface for recording audit events. It is the dependency of
// NewAuditLogger; the publishing services hold the concrete *storage.SQLAuditStore.
type AuditEventRecorder interface {
	RecordEvent(event *storage.Event) (int64, error)
}

// requireActiveScrubber is the shared constructor check for every service that publishes through
// the egress choke point. A nil or disabled scrubber fails closed with
// constants.ErrEgressScrubberUnavailable; there is no unscrubbed publish path.
func requireActiveScrubber(service string, scrubbingSvc *scrubbing.ScrubbingService) error {
	if scrubbingSvc == nil || !scrubbingSvc.IsEnabled() {
		return fmt.Errorf("%s: %w", service, constants.ErrEgressScrubberUnavailable)
	}
	return nil
}

// requireAuditStore is the shared constructor check for every service that records execution or
// observed-state evidence. A nil store fails closed with constants.ErrAuditStoreRequired.
func requireAuditStore(service string, auditStore *storage.SQLAuditStore) error {
	if auditStore == nil {
		return fmt.Errorf("%s: %w", service, constants.ErrAuditStoreRequired)
	}
	return nil
}

// publishLFAATypedResponseTo builds a GovernanceEnvelope from a typed payload and publishes it to the
// results channel. Used by services that hold a PubSubClient directly.
func publishLFAATypedResponseTo(
	ctx context.Context,
	client PubSubClient,
	cfg *config.Config,
	logger *slog.Logger,
	msg *PubSubCommandMessage,
	eventType constants.EventType,
	payload proto.Message,
	auditStore *storage.SQLAuditStore, // records observed-state content evidence
	scrubbingService *scrubbing.ScrubbingService, // egress scrubber, also scrubs the recorded evidence
) {
	executionID := executionIDFromMessage(msg)
	setExecutionIDOnPayload(payload, executionID)

	originatingAction, err := constants.Registry.ActionFor(msg.EventType)
	if err != nil {
		logger.Error("Failed to resolve originating action for LFAA response", string(constants.ConnectionStateError), err)
		return
	}
	operatorID := cfg.OperatorID
	if msg.OperatorID != nil && *msg.OperatorID != "" {
		operatorID = *msg.OperatorID
	}
	// Egress choke point: BuildUniversalResultEnvelope scrubs the payload. A withheld payload
	// publishes the typed failure event instead.
	env, err := BuildUniversalResultEnvelope(ctx, cfg,msg.EventType, eventType, originatingAction, payload, scrubbingService, msg.ID, operatorID, msg.CaseID, msg.InvestigationID, msg.TaskID, msg.WebSessionID, msg.CLISessionID)
	if err != nil {
		if isEgressWithheld(err) {
			logger.Error("Withholding LFAA typed response: egress withheld", string(constants.ConnectionStateError), err)
			if failure := egressFailureEvent(eventType, payload); failure != "" {
				publishLFAAErrorTo(ctx, client, cfg, logger, msg, failure, egressWithheldText(err), auditStore, scrubbingService)
			}
			return
		}
		logger.Error("Failed to build LFAA typed response Governance Envelope", string(constants.ConnectionStateError), err)
		return
	}

	env.OperatorSessionId = msg.OperatorSessionID
	data, err := protojson.Marshal(env)
	if err != nil {
		logger.Error("Failed to marshal LFAA typed response Governance Envelope", string(constants.ConnectionStateError), err)
		return
	}

	channelName := ResultsChannel(operatorID, msg.OperatorSessionID)
	if err := client.Publish(ctx, channelName, data); err != nil {
		logger.Error("Failed to publish LFAA typed response Universal", string(constants.ConnectionStateError), err)
		return
	}

	logger.Info("LFAA typed response published (Universal)", "event_type", eventType)

	// §3: observed-state content evidence (best-effort, non-fatal)
	publishObservedStateEvidence(ctx, logger, msg, eventType, payload, auditStore, scrubbingService)
}

// publishLFAAErrorTo builds an error GovernanceEnvelope and publishes it to the results channel.
func publishLFAAErrorTo(
	ctx context.Context,
	client PubSubClient,
	cfg *config.Config,
	logger *slog.Logger,
	msg *PubSubCommandMessage,
	eventType constants.EventType,
	errorMsg string,
	auditStore *storage.SQLAuditStore, // records observed-state content evidence
	scrubbingService *scrubbing.ScrubbingService, // egress scrubber, also scrubs the recorded evidence
) {
	executionID := executionIDFromMessage(msg)

	// Error text can carry paths and command output, so it is scrubbed here. Without an active
	// scrubber the fixed sentinel text is published instead of the raw message. The text is
	// scrubbed or fixed before it reaches the envelope builder, so the builder runs without egress.
	safeMsg := constants.ErrEgressScrubberUnavailable.Error()
	if scrubbingService != nil && scrubbingService.IsEnabled() {
		safeMsg = scrubbingService.ScrubText(ctx, errorMsg)
	}

	// Use CommandResult as a generic error container
	payload := &operatorv1.CommandResult{
		ExecutionId: executionID,
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		Error:       safeMsg,
	}

	originatingAction, err := constants.Registry.ActionFor(msg.EventType)
	if err != nil {
		logger.Error("Failed to resolve originating action for LFAA response", string(constants.ConnectionStateError), err)
		return
	}
	operatorID := cfg.OperatorID
	if msg.OperatorID != nil && *msg.OperatorID != "" {
		operatorID = *msg.OperatorID
	}
	env, err := buildResultEnvelope(cfg, msg.EventType, eventType, originatingAction, payload, msg.ID, operatorID, msg.CaseID, msg.InvestigationID, msg.TaskID, msg.WebSessionID, msg.CLISessionID)
	if err != nil {
		logger.Error("Failed to build LFAA error Governance Envelope", string(constants.ConnectionStateError), err)
		return
	}

	env.OperatorSessionId = msg.OperatorSessionID
	data, err := protojson.Marshal(env)
	if err != nil {
		logger.Error("Failed to marshal LFAA error Governance Envelope", string(constants.ConnectionStateError), err)
		return
	}

	channelName := ResultsChannel(operatorID, msg.OperatorSessionID)
	if err := client.Publish(ctx, channelName, data); err != nil {
		logger.Error("Failed to publish LFAA error Universal", string(constants.ConnectionStateError), err)
	}

	// §3: observed-state content evidence (best-effort, non-fatal)
	publishObservedStateEvidence(ctx, logger, msg, eventType, payload, auditStore, scrubbingService)
}

// publishObservedStateEvidence persists observed-state content evidence to the audit store.
// A failed write is logged and non-fatal, and the evidence applies only to observe handlers
// (not commands). Commands already capture stdout/stderr via their own RecordEvent calls.
// The audit store and an active scrubber are required: the publishing services take both at
// construction, so neither is checked here.
func publishObservedStateEvidence(
	ctx context.Context,
	logger *slog.Logger,
	msg *PubSubCommandMessage,
	eventType constants.EventType,
	payload proto.Message,
	auditStore *storage.SQLAuditStore,
	scrubbingService *scrubbing.ScrubbingService,
) {
	// Handlers that record their own raw execution event are not recorded twice.
	if recordsOwnExecutionEvent(eventType) {
		return
	}

	// Extract content text from the payload
	contentText := extractContentText(payload)
	if contentText == "" {
		return
	}

	// Scrub content before persisting (per position paper §10: raw data belongs only in raw vault)
	contentText = scrubbingService.ScrubText(ctx, contentText)

	// Create event record
	event := &storage.Event{
		OperatorSessionID: msg.OperatorSessionID,
		Timestamp:         time.Now().UTC(),
		Type:              eventType,
		ContentText:       contentText,
		StoredLocally:     true,
	}

	// Best-effort: log warning but don't fail the publish
	if _, err := auditStore.RecordEvent(event); err != nil {
		logger.Warn("Failed to record observed-state content evidence (non-fatal)",
			"event_type", eventType,
			"error", err)
	}
}

// recordsOwnExecutionEvent reports whether the handler behind eventType records its raw
// execution event itself, so the observed-state evidence path must not record it again.
func recordsOwnExecutionEvent(eventType constants.EventType) bool {
	switch eventType {
	case constants.Event.Operator.Command.Completed, constants.Event.Operator.Command.Failed,
		constants.Event.Operator.FileEdit.Completed, constants.Event.Operator.FileEdit.Failed,
		constants.Event.Operator.FsRead.Completed, constants.Event.Operator.FsRead.Failed,
		constants.Event.Operator.RestoreFile.Completed, constants.Event.Operator.RestoreFile.Failed:
		return true
	}
	return false
}

// extractContentText extracts human-readable content from observe handler payloads
func extractContentText(payload proto.Message) string {
	if payload == nil {
		return ""
	}

	// Handle different payload types
	switch p := payload.(type) {
	case *operatorv1.FsListResult:
		if len(p.Entries) > 0 {
			entriesJSON, _ := json.Marshal(p.Entries)
			return string(entriesJSON)
		}
		return ""
	case *operatorv1.FsReadResult:
		return p.Content
	case *operatorv1.FsGrepResult:
		if len(p.Matches) > 0 {
			matchesJSON, _ := json.Marshal(p.Matches)
			return string(matchesJSON)
		}
		return ""
	case *operatorv1.PortCheckResult:
		if len(p.Results) > 0 {
			resultsJSON, _ := json.Marshal(p.Results)
			return string(resultsJSON)
		}
		return ""
	case *operatorv1.FetchLogsResult:
		return fmt.Sprintf("command: %s, exit_code: %d, stdout_size: %d, stderr_size: %d",
			p.Command, p.ReturnCode, p.StdoutSize, p.StderrSize)
	case *operatorv1.FetchHistoryResult:
		return fmt.Sprintf("fetch_history: %d events", p.Total)
	case *operatorv1.FetchFileHistoryResult:
		return fmt.Sprintf("fetch_file_history: %s, %d entries", p.FilePath, len(p.History))
	case *operatorv1.FetchFileDiffResult:
		if p.Diff != nil {
			return fmt.Sprintf("fetch_file_diff: %s, operation: %s", p.Diff.FilePath, p.Diff.Operation)
		}
		return fmt.Sprintf("fetch_file_diff: %d diffs", p.Total)
	case *operatorv1.RestoreFileResult:
		return fmt.Sprintf("restore_file: %s, commit: %s", p.FilePath, p.CommitHash)
	case *operatorv1.HeartbeatResult:
		return fmt.Sprintf("heartbeat: %s, status: %s", p.OperatorId, p.Status)
	case *operatorv1.EvalAnswerRequested:
		return fmt.Sprintf("eval_answer: benchmark=%s, prompt_id=%s", p.Benchmark, p.PromptId)
	default:
		// Generic fallback: marshal to JSON
		if jsonBytes, err := json.Marshal(payload); err == nil {
			return string(jsonBytes)
		}
		return ""
	}
}
