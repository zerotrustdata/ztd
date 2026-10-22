// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// mapProtoToPayloadType maps a protobuf message to its canonical ztd payload type string.
// The mapping is the single source of that discriminator for the typed protobuf payloads.
func mapProtoToPayloadType(msg proto.Message) string {
	switch m := msg.(type) {
	case *operatorv1.CommandResult:
		return "execution_result"
	case *operatorv1.ExecutionStatusUpdate:
		return "execution_status"
	case *operatorv1.FileEditResult:
		return "file_edit_result"
	case *operatorv1.FsListResult:
		return "fs_list_result"
	case *operatorv1.FsGrepResult:
		return "fs_grep_result"
	case *operatorv1.FsReadResult:
		return "fs_read_result"
	case *operatorv1.PortCheckResult:
		return "port_check_result"
	case *operatorv1.OllamaModelInventoryResult:
		return "ollama_model_inventory_result"
	case *operatorv1.OllamaModelResidencyResult:
		return "ollama_model_residency_result"
	case *operatorv1.OllamaModelReleaseResult:
		return "ollama_model_release_result"
	case *operatorv1.FetchLogsResult:
		if m.Error != "" {
			return "fetch_logs_error"
		}
		return "fetch_logs_result"
	case *operatorv1.FetchHistoryResult:
		if !m.Success {
			return "fetch_history_error"
		}
		return "fetch_history_success"
	case *operatorv1.FetchFileHistoryResult:
		if !m.Success {
			return "fetch_file_history_error"
		}
		return "fetch_file_history_success"
	case *operatorv1.RestoreFileResult:
		if !m.Success {
			return "restore_file_error"
		}
		return "restore_file_success"
	case *operatorv1.FetchFileDiffResult:
		if !m.Success {
			return "fetch_file_diff_error"
		}
		if m.Diff != nil {
			return "fetch_file_diff_by_id_success"
		}
		return "fetch_file_diff_by_session_success"
	case *operatorv1.HeartbeatResult:
		return "heartbeat"
	default:
		return string(constants.SystemHealthUnknown)
	}
}

// BuildUniversalResultEnvelope constructs a GovernanceEnvelope for result publishing.
// It preserves the original command's MessageID for correlation.
//
// This is the egress choke point for every Operator result payload: the payload passes
// egressScrubbed before it is marshalled. A withheld payload (nil or disabled scrubber with
// content, or an unclassified type) returns an error wrapping the egress sentinel; nothing is
// published.
func BuildUniversalResultEnvelope(
	ctx context.Context,
	cfg *config.Config,
	requestEvent constants.EventType,
	outcomeEvent constants.EventType,
	originatingActionType constants.ActionType,
	payload proto.Message,
	scrubber *scrubbing.ScrubbingService,
	originalMessageID string,
	senderID string,
	caseID string,
	investigationID string,
	taskID *string,
	webSessionID string,
	cliSessionID string,
) (*commonv1.GovernanceEnvelope, error) {
	egressPayload, err := egressScrubbed(ctx, payload, scrubber)
	if err != nil {
		return nil, fmt.Errorf("build result envelope: %w", err)
	}
	return buildResultEnvelope(cfg, requestEvent, outcomeEvent, originatingActionType, egressPayload, originalMessageID, senderID, caseID, investigationID, taskID, webSessionID, cliSessionID)
}

// buildResultEnvelope marshals an already-egress-approved payload into a GovernanceEnvelope.
// Callers outside this package must use BuildUniversalResultEnvelope. The only other caller is
// the withheld-failure path, which passes a payload whose text is fixed by this package or has
// already been scrubbed explicitly.
func buildResultEnvelope(
	cfg *config.Config,
	requestEvent constants.EventType,
	outcomeEvent constants.EventType,
	originatingActionType constants.ActionType,
	payload proto.Message,
	originalMessageID string,
	senderID string,
	caseID string,
	investigationID string,
	taskID *string,
	webSessionID string,
	cliSessionID string,
) (*commonv1.GovernanceEnvelope, error) {
	if requestEvent != "" {
		if err := constants.ValidateGovernedResultEnvelope(requestEvent, outcomeEvent, originatingActionType); err != nil {
			return nil, fmt.Errorf("validate governed result envelope: %w", err)
		}
	} else if originatingActionType == "" {
		return nil, fmt.Errorf("originating action type required for outcome %q", outcomeEvent)
	}
	payloadBytes, err := proto.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Build the intent data directly from canonical protojson so the envelope
	// carries the same field names and values as the public protocol surface.
	intentDataStruct := &structpb.Struct{}
	intentJSON, err := protojson.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload intent data: %w", err)
	}
	if err := protojson.Unmarshal(intentJSON, intentDataStruct); err != nil {
		return nil, fmt.Errorf("failed to unmarshal payload intent data: %w", err)
	}

	// Inject canonical payload_type for consumer discriminator-based parsing of the typed payload.
	if _, ok := intentDataStruct.Fields["payload_type"]; !ok {
		intentDataStruct.Fields["payload_type"] = structpb.NewStringValue(mapProtoToPayloadType(payload))
	}

	env := &commonv1.GovernanceEnvelope{
		Id:                originalMessageID, // Will be regenerated if empty
		Timestamp:         timestamppb.Now(),
		ExpiresAt:         timestamppb.New(time.Now().Add(5 * time.Minute)),
		SourceComponent:   commonv1.Component_COMPONENT_ZTDO,
		OperatorId:        senderID,
		OperatorSessionId: cfg.OperatorSessionId,
		EventType:         string(outcomeEvent),
		ActionType:        string(originatingActionType),
		Payload:           payloadBytes,
		IntentData:        intentDataStruct,
		CaseId:            caseID,
		InvestigationId:   investigationID,
		WebSessionId:      webSessionID,
		CliSessionId:      cliSessionID,
	}

	if taskID != nil {
		env.TaskId = *taskID
	}

	// Generate ID if not provided (for correlation with command)
	if env.Id == "" {
		// For now using simple UUID during transition, but eventually should be hash-based
		env.Id = fmt.Sprintf("res_%d", time.Now().UnixNano())
	}

	return env, nil
}

// MaxPayloadSize is the maximum allowed size for a protobuf payload (5MB).
const MaxPayloadSize = 5 * 1024 * 1024

// unmarshalPayload converts the raw bytes into a typed proto.Message for the given event type.
func unmarshalPayload(eventType constants.EventType, payload []byte) (proto.Message, error) {
	if len(payload) > MaxPayloadSize {
		return nil, fmt.Errorf("payload exceeds maximum size limit (%d bytes): %w", MaxPayloadSize, constants.ErrPayloadExceedsLimit)
	}
	var m proto.Message
	switch eventType {
	case constants.Event.Operator.HeartbeatRequested:
		m = &operatorv1.HeartbeatRequested{}
	case constants.Event.Operator.Command.Requested:
		m = &operatorv1.CommandRequested{}
	case constants.Event.Operator.Command.CancelRequested:
		m = &operatorv1.CommandCancelRequested{}
	case constants.Event.Operator.FileEdit.Requested:
		m = &operatorv1.FileEditRequested{}
	case constants.Event.Operator.FsList.Requested:
		m = &operatorv1.FsListRequested{}
	case constants.Event.Operator.FsRead.Requested:
		m = &operatorv1.FsReadRequested{}
	case constants.Event.Operator.FsGrep.Requested:
		m = &operatorv1.FsGrepRequested{}
	case constants.Event.Operator.PortCheck.Requested:
		m = &operatorv1.CheckPortRequested{}
	case constants.Event.Operator.OllamaModelInventory.Requested:
		m = &operatorv1.OllamaModelInventoryRequested{}
	case constants.Event.Operator.OllamaModelResidency.Requested:
		m = &operatorv1.OllamaModelResidencyRequested{}
	case constants.Event.Operator.OllamaModelRelease.Requested:
		m = &operatorv1.OllamaModelReleaseRequested{}
	case constants.Event.Operator.FetchLogs.Requested:
		m = &operatorv1.FetchLogsRequested{}
	case constants.Event.Operator.FetchHistory.Requested:
		m = &operatorv1.FetchHistoryRequested{}
	case constants.Event.Operator.FetchFileHistory.Requested:
		m = &operatorv1.FetchFileHistoryRequested{}
	case constants.Event.Operator.RestoreFile.Requested:
		m = &operatorv1.RestoreFileRequested{}
	case constants.Event.Operator.ShutdownRequested:
		m = &operatorv1.ShutdownRequested{}
	case constants.Event.Operator.Audit.UserMsg, constants.Event.Operator.Audit.AIMsg:
		m = &operatorv1.AuditMsgRequested{}
	case constants.Event.Operator.Audit.DirectCmd:
		m = &operatorv1.DirectCommandAuditRequested{}
	case constants.Event.Operator.Audit.DirectCmdResult:
		m = &operatorv1.DirectCommandResultAuditRequested{}
	case constants.Event.Operator.FetchFileDiff.Requested:
		m = &operatorv1.FetchFileDiffRequested{}
	case constants.Event.Operator.Mcp.CallRequested:
		m = &operatorv1.McpCallRequested{}
	case constants.Event.Operator.A2a.CallRequested:
		m = &operatorv1.A2ACallRequested{}
	case constants.Event.Operator.Eval.AnswerRequested:
		m = &operatorv1.EvalAnswerRequested{}
	case constants.Event.Operator.Inference.Requested:
		m = &operatorv1.InferenceRequested{}
	case constants.Event.Operator.ProviderBoundaryObservation.Requested:
		m = &evalv1.ProviderBoundaryObservationCommand{}
	case constants.EventPlatformEnrollmentCreateRequested,
		constants.EventPlatformEnrollmentDecideRequested,
		constants.EventPlatformEnrollmentIssueRequested,
		constants.EventPlatformEnrollmentPersistPolicyRequested,
		constants.EventPlatformEnrollmentCreateSessionRequested,
		constants.EventPlatformEnrollmentRevokeRequested,
		constants.EventPlatformEnrollmentBindConsensusRequested:
		m = &commonv1.PlatformEnrollmentGovernancePayload{}
	default:
		return nil, fmt.Errorf("unknown event type for unmarshaling: %s", eventType)
	}

	if err := proto.Unmarshal(payload, m); err != nil {
		return nil, err
	}
	return m, nil
}
