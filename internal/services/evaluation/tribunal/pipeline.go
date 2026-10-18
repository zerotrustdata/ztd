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

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// RunPipeline orchestrates intent-only command generation:
// 1. Sealed 5-member candidate generation (Axiom, Concord, Variance, Pragma, Nemesis).
// 2. Weighted vote with clustering, minimum consensus of 2, and Round 2 peer review.
// 3. Marshal risk analysis and circuit breaker enforcement.
// 4. Auditor technical scrutiny and potential revision/swap.
// All stages record discrete AgentSteps and inference receipts.
func RunPipeline(ctx context.Context, cfg PipelineConfig, req PipelineRequest, startStepIndex uint32) (*PipelineResult, error) {
	result := &PipelineResult{}
	stepIndex := startStepIndex

	// Stage 1: Generate candidates
	candidates, err := GenerateCandidates(ctx, cfg, req)
	if err != nil {
		return nil, fmt.Errorf("tribunal pipeline: %w", err)
	}
	result.Candidates = candidates

	for _, c := range candidates {
		stepIndex++
		result.Steps = append(result.Steps, &evalv1.AgentStep{
			Index:        stepIndex,
			Role:         evalv1.AgentRole_AGENT_ROLE_CANDIDATE_GENERATOR,
			PersonaLabel: string(c.Member),
			ResolvedTier: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE,
			ModelTag:     cfg.Variant.GetServedModelTag(),
			ModelDigest:  cfg.Variant.GetModelDigest(),
			CallClass:    evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
			Output: &evalv1.AgentStep_Candidate{
				Candidate: &evalv1.CandidateOutput{
					Command: c.Command,
				},
			},
		})
		if c.Receipt != nil {
			result.Receipts = append(result.Receipts, c.Receipt)
		}
		if c.TransactionID != "" {
			result.TransactionIDs = append(result.TransactionIDs, c.TransactionID)
		}
	}

	// Stage 2: Voting & Consensus
	vote, err := RunVoting(ctx, cfg, req, candidates)
	if err != nil {
		return nil, fmt.Errorf("tribunal pipeline voting: %w", err)
	}
	result.Vote = vote

	var protoVotes []*evalv1.CandidateVote
	for _, v := range vote.Votes {
		protoVotes = append(protoVotes, &evalv1.CandidateVote{
			VoterLabel:     v.VoterMember,
			CandidateLabel: v.CandidateLabel,
		})
	}

	stepIndex++
	result.Steps = append(result.Steps, &evalv1.AgentStep{
		Index:        stepIndex,
		Role:         evalv1.AgentRole_AGENT_ROLE_CANDIDATE_VOTER,
		PersonaLabel: "tribunal-voter",
		ResolvedTier: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT,
		ModelTag:     cfg.Variant.GetServedModelTag(),
		ModelDigest:  cfg.Variant.GetModelDigest(),
		CallClass:    evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
		Output: &evalv1.AgentStep_Vote{
			Vote: &evalv1.VoteOutput{
				Votes:            protoVotes,
				Rounds:           vote.Rounds,
				ConsensusReached: vote.ConsensusReached,
				SelectedCommand:  vote.WinnerCommand,
			},
		},
	})
	result.Receipts = append(result.Receipts, vote.Receipts...)
	result.TransactionIDs = append(result.TransactionIDs, vote.TransactionIDs...)

	if !vote.ConsensusReached || vote.WinnerCommand == "" {
		return result, fmt.Errorf("tribunal pipeline: consensus could not be reached after %d round(s)", vote.Rounds)
	}

	// Stage 3: Marshal Risk Analysis
	risk, err := AnalyzeRisk(ctx, cfg, req, vote.WinnerCommand)
	if err != nil {
		return nil, fmt.Errorf("tribunal pipeline marshal: %w", err)
	}
	result.Risk = risk
	if risk.Receipt != nil {
		result.Receipts = append(result.Receipts, risk.Receipt)
	}
	if risk.TransactionID != "" {
		result.TransactionIDs = append(result.TransactionIDs, risk.TransactionID)
	}

	stepIndex++
	result.Steps = append(result.Steps, &evalv1.AgentStep{
		Index:        stepIndex,
		Role:         evalv1.AgentRole_AGENT_ROLE_RISK_ANALYST,
		PersonaLabel: "marshal",
		ResolvedTier: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE,
		ModelTag:     cfg.Variant.GetServedModelTag(),
		ModelDigest:  cfg.Variant.GetModelDigest(),
		CallClass:    evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
		Output: &evalv1.AgentStep_Risk{
			Risk: &evalv1.RiskOutput{
				SubjectKind: risk.SubjectKind,
				Subject:     risk.Subject,
				Risk:        risk.Risk.ToProtoRisk(),
				Blocked:     risk.Blocked,
			},
		},
	})

	if risk.Blocked {
		result.HighRiskBlocked = true
		result.Feedback = risk.Feedback
		if req.CircuitBreakerStrikeCount >= 1 {
			result.CircuitBreakerTripped = true
			return result, fmt.Errorf("tribunal pipeline: circuit breaker tripped (agent conflict: high risk blocked repeatedly)")
		}
		return result, fmt.Errorf("tribunal pipeline: marshal blocked high-risk command: %s (feedback: %s)", risk.Rationale, risk.Feedback)
	}

	// Stage 4: Auditor Technical Scrutiny
	audit, err := AuditCommand(ctx, cfg, req, vote)
	if err != nil {
		return nil, fmt.Errorf("tribunal pipeline auditor: %w", err)
	}
	result.Audit = audit
	if audit.Receipt != nil {
		result.Receipts = append(result.Receipts, audit.Receipt)
	}
	if audit.TransactionID != "" {
		result.TransactionIDs = append(result.TransactionIDs, audit.TransactionID)
	}

	stepIndex++
	result.Steps = append(result.Steps, &evalv1.AgentStep{
		Index:        stepIndex,
		Role:         evalv1.AgentRole_AGENT_ROLE_REVIEWER,
		PersonaLabel: "auditor",
		ResolvedTier: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
		ModelTag:     cfg.Variant.GetServedModelTag(),
		ModelDigest:  cfg.Variant.GetModelDigest(),
		CallClass:    evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
		Output: &evalv1.AgentStep_Review{
			Review: &evalv1.ReviewOutput{
				Verdict: audit.Verdict,
				Command: audit.FinalCommand,
			},
		},
	})

	result.FinalCommand = audit.FinalCommand
	return result, nil
}
