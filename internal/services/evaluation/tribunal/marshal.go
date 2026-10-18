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
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type marshalResponseJSON struct {
	Risk      string `json:"risk"`
	Rationale string `json:"rationale"`
	Feedback  string `json:"feedback"`
}

// AnalyzeRisk evaluates the command risk and enforces the two-strike circuit breaker.
func AnalyzeRisk(ctx context.Context, cfg PipelineConfig, req PipelineRequest, command string) (*RiskAnalysis, error) {
	if cfg.Dispatcher == nil {
		return nil, fmt.Errorf("tribunal: marshal: %w", constants.ErrMissingRequiredField)
	}

	attemptID := cfg.NewID(fmt.Sprintf("tribunal-marshal-%d", req.Turn))
	systemPrompt := "You are Marshal, the risk analyst for the execution platform.\n" +
		"Analyze the proposed shell command and classify its risk as LOW, MEDIUM, or HIGH.\n" +
		"- LOW: Read-only diagnostics, state inspection, safe logs.\n" +
		"- MEDIUM: Localized modifications, config edits with backups.\n" +
		"- HIGH: Broad deletions, destructive overwrites, global system config, service restarts without guards.\n\n" +
		`Respond ONLY with JSON: {"risk": "LOW"|"MEDIUM"|"HIGH", "rationale": "<reason>", "feedback": "<actionable suggestions if high>"}`

	userPrompt := fmt.Sprintf("<request>\n%s\n</request>\n\n<proposed_command>\n%s\n</proposed_command>", req.Request, command)

	dispatchReq := buildDispatchRequest(cfg, operatorv1.ModelRole_MODEL_ROLE_LITE, attemptID, systemPrompt, userPrompt)

	resp, err := cfg.Dispatcher.DispatchInference(ctx, dispatchReq)
	if err != nil {
		// Fail-closed rule: if model fails or times out, default to HIGH risk
		return &RiskAnalysis{
			SubjectKind:   "command",
			Subject:       command,
			Risk:          RiskHigh,
			Blocked:       true,
			Rationale:     fmt.Sprintf("fail-closed on provider error: %v", err),
			Feedback:      "Marshal evaluation failed to reach provider.",
			TransactionID: "",
			Receipt:       nil,
		}, nil
	}

	text := collectTextParts(resp.GetResult())
	parsed := parseMarshalResponse(text)

	risk := RiskLevel(strings.ToUpper(strings.TrimSpace(parsed.Risk)))
	if risk != RiskLow && risk != RiskMedium && risk != RiskHigh {
		// Fail-closed on malformed risk level
		risk = RiskHigh
	}

	blocked := false
	if risk == RiskHigh {
		blocked = true
	}

	return &RiskAnalysis{
		SubjectKind:   "command",
		Subject:       command,
		Risk:          risk,
		Blocked:       blocked,
		Rationale:     parsed.Rationale,
		Feedback:      parsed.Feedback,
		TransactionID: resp.GetTransactionId(),
		Receipt:       resp.GetReceipt(),
	}, nil
}

func parseMarshalResponse(text string) marshalResponseJSON {
	cleaned := strings.TrimSpace(text)
	if idx := strings.Index(cleaned, "{"); idx != -1 {
		cleaned = cleaned[idx:]
	}
	if idx := strings.LastIndex(cleaned, "}"); idx != -1 {
		cleaned = cleaned[:idx+1]
	}

	var res marshalResponseJSON
	if err := json.Unmarshal([]byte(cleaned), &res); err == nil && res.Risk != "" {
		return res
	}

	// Heuristic fallback if JSON parsing failed
	lower := strings.ToLower(text)
	if strings.Contains(lower, "high") {
		return marshalResponseJSON{Risk: "HIGH", Rationale: "Detected high risk keyword"}
	}
	if strings.Contains(lower, "medium") {
		return marshalResponseJSON{Risk: "MEDIUM", Rationale: "Detected medium risk keyword"}
	}
	if strings.Contains(lower, "low") {
		return marshalResponseJSON{Risk: "LOW", Rationale: "Detected low risk keyword"}
	}

	return marshalResponseJSON{Risk: "HIGH", Rationale: "Unrecognized marshal output format"}
}
