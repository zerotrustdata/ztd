// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/publicdisclosure"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// BuildPublicAssignmentTranscripts fills the role_transcripts summary of the
// feed record from digest-checked evidence: final response, finish state, and
// each tool call verbatim. The complete trace (exact input, thinking, every
// turn, event receipts, anchors) is published separately as the content-addressed
// assignment_agent_trace proof artifact. The shared disclosure validator
// checks the closed schema and byte bounds; content is never screened. A
// transcript past those bounds is omitted rather than truncated: the agent
// trace proof artifact carries it in full.
func BuildPublicAssignmentTranscripts(ctx context.Context, store CampaignStore, assignment *evalv1.EvaluationAssignment, result *evalv1.EvaluationAssignmentResult) ([]publicdisclosure.RoleTranscript, error) {
	if ctx == nil || store == nil || assignment == nil || result == nil {
		return nil, fmt.Errorf("evaluation: build public transcript: %w", constants.ErrMissingRequiredField)
	}
	if assignment.GetRunId() != result.GetRunId() || assignment.GetAssignmentId() != result.GetAssignmentId() {
		return nil, fmt.Errorf("evaluation: build public transcript: %w", constants.ErrEvidenceScopeMismatch)
	}
	trace, err := store.LoadAssignmentAgentTrace(ctx, assignment.GetRunId(), assignment.GetAssignmentId())
	if errors.Is(err, constants.ErrNotFound) {
		for _, ref := range result.GetEvidenceRefs() {
			if ref.GetArtifactType() == string(complianceevidence.ArtifactTypeEvaluationAssignmentTrace) {
				return nil, fmt.Errorf("evaluation: publish referenced assignment transcript: %w", err)
			}
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("evaluation: publish assignment transcript: %w", err)
	}
	if trace == nil {
		return nil, fmt.Errorf("evaluation: publish assignment transcript: %w", constants.ErrEvidenceArtifactMalformed)
	}
	transcript := publicdisclosure.RoleTranscript{Role: trace.DesignatedRole, Response: trace.FinalOutput, TraceDigest: trace.EvidenceDigest}
	for _, call := range trace.ModelCalls {
		if call.IsTurn() {
			transcript.FinishReason = call.FinishReason
		}
	}
	for _, call := range trace.ToolCalls {
		transcript.ToolCalls = append(transcript.ToolCalls, publicdisclosure.RoleTranscriptToolCall{
			ToolName:              call.ToolName,
			ArgumentsJSON:         call.ArgumentsJSON,
			ArgumentsHash:         models.SHA256Hex([]byte(call.ArgumentsJSON)),
			ApprovedArgumentsJSON: call.ApprovedArgumentsJSON,
			Command:               call.Command,
			ResultJSON:            call.ResultJSON,
			Success:               call.Success,
			ErrorType:             call.ErrorType,
		})
	}
	transcripts := []publicdisclosure.RoleTranscript{transcript}
	body, err := json.Marshal(transcripts)
	if err != nil {
		return nil, fmt.Errorf("evaluation: encode public transcript: %w", err)
	}
	if err := publicdisclosure.ValidateRoleTranscripts(body); errors.Is(err, constants.ErrEvidenceArtifactTooLarge) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("evaluation: validate public transcript: %w", err)
	}
	return transcripts, nil
}
