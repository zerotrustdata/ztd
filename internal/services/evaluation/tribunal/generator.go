// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func textMessage(role operatorv1.InferenceMessageRole, text string) *operatorv1.InferenceMessage {
	return &operatorv1.InferenceMessage{
		Role:  role,
		Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: text}}},
	}
}

func buildDispatchRequest(cfg PipelineConfig, role operatorv1.ModelRole, attemptID, systemPrompt, userPrompt string) *operatorv1.InferenceDispatchRequest {
	var model, digest string
	if cfg.Variant != nil {
		model = cfg.Variant.GetServedModelTag()
		digest = cfg.Variant.GetModelDigest()
	}
	return &operatorv1.InferenceDispatchRequest{
		RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
		Role:                    role,
		Model:                   model,
		ModelDigest:             digest,
		ProviderAttemptId:       attemptID,
		TargetOperatorSessionId: cfg.InferenceSessionID,
		CampaignId:              cfg.CampaignID,
		ModelRegistryDigest:     cfg.ModelRegistryDigest,
		ModelRegistry:           cfg.ModelRegistry,
		Messages: []*operatorv1.InferenceMessage{
			textMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, systemPrompt),
			textMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, userPrompt),
		},
	}
}

// memberSystemPrompt returns the system prompt for a specific Tribunal panel member.
func memberSystemPrompt(member Member) string {
	base := "You are a member of the Tribunal, an ensemble of models that synthesizes safe, effective shell commands from intent.\n" +
		"Every Tribunal member emits EXACTLY a shell command string - nothing else. No explanation, no markdown fences, no prose.\n\n"

	switch member {
	case MemberAxiom:
		return base + "Role: Axiom (The Composer).\n" +
			"Style: Clean, multi-stage pipelines with coherent composition and canonical flag order.\n" +
			"Prefer idiomatic pipe composition and concise, readable structure."
	case MemberConcord:
		return base + "Role: Concord (The Guardian).\n" +
			"Style: Defensive discipline. Prefer read-only queries, safe quoting across pipes, -r on xargs, and && chaining."
	case MemberVariance:
		return base + "Role: Variance (The Exhaustive).\n" +
			"Style: Edge-case resilience. Handle spaces and null delimiters (-print0, xargs -0), explicit sort locales, and robust matching."
	case MemberPragma:
		return base + "Role: Pragma (The Conventional).\n" +
			"Style: Practical conventions. Prefer modern, idiomatic OS utilities (ss over netstat, journalctl on systemd) and standard CLI flags."
	case MemberNemesis:
		return base + "Role: Nemesis (The Calibrated Adversary).\n" +
			"Style: Calibrated inspection. If there are subtle edge cases or ambiguities, reflect them, or honestly emit the standard command when safe."
	default:
		return base
	}
}

// formatUserPrompt formats the intent request into the generation prompt.
func formatUserPrompt(req PipelineRequest) string {
	var sb strings.Builder
	sb.WriteString("<system_context>\n")
	if req.OS != "" {
		sb.WriteString("OS: " + req.OS + "\n")
	} else {
		sb.WriteString("OS: linux\n")
	}
	if req.Shell != "" {
		sb.WriteString("Shell: " + req.Shell + "\n")
	} else {
		sb.WriteString("Shell: bash\n")
	}
	if req.WorkingDirectory != "" {
		sb.WriteString("Working directory: " + req.WorkingDirectory + "\n")
	}
	sb.WriteString("</system_context>\n\n")

	if req.Guidelines != "" {
		sb.WriteString("<guidelines>\n" + req.Guidelines + "\n</guidelines>\n\n")
	}

	sb.WriteString("<request>\n" + req.Request + "\n</request>\n\n")
	sb.WriteString("Respond now with the exact shell command string and nothing else.")
	return sb.String()
}

// GenerateCandidates invokes the sealed 5-member panel to propose candidate commands.
func GenerateCandidates(ctx context.Context, cfg PipelineConfig, req PipelineRequest) ([]Candidate, error) {
	if cfg.Dispatcher == nil {
		return nil, fmt.Errorf("tribunal: generate candidates: %w", constants.ErrMissingRequiredField)
	}

	userPrompt := formatUserPrompt(req)
	candidates := make([]Candidate, 0, len(AllMembers))

	for _, member := range AllMembers {
		sysPrompt := memberSystemPrompt(member)
		attemptID := cfg.NewID(fmt.Sprintf("tribunal-gen-%s-%d", member, req.Turn))
		dispatchReq := buildDispatchRequest(cfg, operatorv1.ModelRole_MODEL_ROLE_LITE, attemptID, sysPrompt, userPrompt)

		resp, err := cfg.Dispatcher.DispatchInference(ctx, dispatchReq)
		if err != nil {
			return nil, fmt.Errorf("tribunal: generate candidate for %s: %w", member, err)
		}

		rawText := collectTextParts(resp.GetResult())
		normCmd := NormaliseCommand(rawText)

		candidates = append(candidates, Candidate{
			Member:         member,
			Command:        normCmd,
			RawText:        rawText,
			TransactionID:  resp.GetTransactionId(),
			Receipt:        resp.GetReceipt(),
			ProviderCallID: attemptID,
		})
	}

	return candidates, nil
}

func collectTextParts(res *operatorv1.InferenceResult) string {
	if res == nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range res.GetParts() {
		if text := p.GetText(); text != "" {
			sb.WriteString(text)
		}
	}
	return strings.TrimSpace(sb.String())
}
