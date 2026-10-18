// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type auditorResponseJSON struct {
	Verdict   string `json:"verdict"`
	Command   string `json:"command"`
	Rationale string `json:"rationale"`
}

// AuditCommand performs technical scrutiny on the selected command before execution.
func AuditCommand(ctx context.Context, cfg PipelineConfig, req PipelineRequest, vote *VoteResult) (*AuditResult, error) {
	if cfg.Dispatcher == nil {
		return nil, fmt.Errorf("tribunal: auditor: %w", constants.ErrMissingRequiredField)
	}

	attemptID := cfg.NewID(fmt.Sprintf("tribunal-audit-%d", req.Turn))
	systemPrompt := "You are the Auditor, the final technical verification gate.\n" +
		"Inspect the proposed shell command for technical correctness, safe quoting, error handling (pipefail/&&), and alignment with intent.\n" +
		"You may:\n" +
		"- APPROVE: The command is correct and safe to run as-is.\n" +
		"- REVISE: The command has minor quoting or flag flaws that you fix in the command field.\n" +
		"- SWAP: Swap to an alternative candidate from the panel if it is superior.\n\n" +
		`Respond ONLY with JSON: {"verdict": "APPROVE"|"REVISE"|"SWAP", "command": "<final command>", "rationale": "<short rationale>"}`

	var userPrompt strings.Builder
	userPrompt.WriteString(fmt.Sprintf("<request>\n%s\n</request>\n\n", req.Request))
	userPrompt.WriteString(fmt.Sprintf("<selected_command>\n%s\n</selected_command>\n\n", vote.WinnerCommand))

	if len(vote.Clusters) > 1 {
		userPrompt.WriteString("<alternative_candidates>\n")
		for cmd, members := range vote.Clusters {
			if cmd != vote.WinnerCommand {
				userPrompt.WriteString(fmt.Sprintf("- %s (supported by %d members)\n", cmd, len(members)))
			}
		}
		userPrompt.WriteString("</alternative_candidates>\n\n")
	}

	userPrompt.WriteString("Provide your verdict.")

	dispatchReq := buildDispatchRequest(cfg, operatorv1.ModelRole_MODEL_ROLE_PRIMARY, attemptID, systemPrompt, userPrompt.String())

	resp, err := cfg.Dispatcher.DispatchInference(ctx, dispatchReq)
	if err != nil {
		// Fallback to APPROVE with winner command if auditor inference fails
		return &AuditResult{
			Verdict:      evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE,
			FinalCommand: vote.WinnerCommand,
			Rationale:    fmt.Sprintf("Auditor inference fallback: %v", err),
		}, nil
	}

	text := collectTextParts(resp.GetResult())
	parsed := parseAuditorResponse(text)

	verdict := evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE
	finalCmd := vote.WinnerCommand

	switch strings.ToUpper(strings.TrimSpace(parsed.Verdict)) {
	case "REVISE":
		verdict = evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_REVISE
		if parsedCmd := NormaliseCommand(parsed.Command); parsedCmd != "" {
			finalCmd = parsedCmd
		}
	case "SWAP":
		verdict = evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_SWAP
		if parsedCmd := NormaliseCommand(parsed.Command); parsedCmd != "" {
			finalCmd = parsedCmd
		}
	default:
		verdict = evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE
	}

	return &AuditResult{
		Verdict:       verdict,
		FinalCommand:  finalCmd,
		Rationale:     parsed.Rationale,
		TransactionID: resp.GetTransactionId(),
		Receipt:       resp.GetReceipt(),
	}, nil
}

func parseAuditorResponse(text string) auditorResponseJSON {
	cleaned := strings.TrimSpace(text)
	if idx := strings.Index(cleaned, "{"); idx != -1 {
		cleaned = cleaned[idx:]
	}
	if idx := strings.LastIndex(cleaned, "}"); idx != -1 {
		cleaned = cleaned[:idx+1]
	}

	var res auditorResponseJSON
	if err := json.Unmarshal([]byte(cleaned), &res); err == nil && res.Verdict != "" {
		return res
	}

	// Heuristic fallback
	lower := strings.ToLower(text)
	if strings.Contains(lower, "revise") {
		return auditorResponseJSON{Verdict: "REVISE"}
	}
	if strings.Contains(lower, "swap") {
		return auditorResponseJSON{Verdict: "SWAP"}
	}
	return auditorResponseJSON{Verdict: "APPROVE"}
}
