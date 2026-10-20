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

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// Run deliberates one intent-only command request: a sealed generation round
// and vote, one anonymized regeneration round and revote when the first has
// no winner, Marshal's risk assessment of the winner, and the Auditor's
// review. When the Auditor changes the command, Marshal assesses the changed
// command before it is approved, so an approved command is always one Marshal
// has classified. Steps are indexed from startIndex+1. A deliberation that
// ends without an approved command is a Result with its Outcome, not an
// error; only an inference failure returns an error.
func Run(ctx context.Context, cfg Config, req Request, startIndex uint32) (*Result, error) {
	if cfg.Inferer == nil {
		return nil, fmt.Errorf("tribunal: inferer: %w", constants.ErrMissingRequiredField)
	}
	steps := stepRecorder{cfg: cfg, index: startIndex}
	result := &Result{}

	candidates, err := generate(ctx, cfg, req, 1, nil)
	if err != nil {
		return nil, err
	}
	result.Candidates = candidates
	steps.candidates(candidates)
	result.Vote = vote(1, candidates)
	if result.Vote.Winner == "" {
		candidates, err = generate(ctx, cfg, req, 2, result.Vote.Clusters)
		if err != nil {
			return nil, err
		}
		result.Candidates = append(result.Candidates, candidates...)
		steps.candidates(candidates)
		result.Vote = vote(2, candidates)
	}
	steps.vote(result.Vote, candidates)
	if result.Vote.Winner == "" {
		result.Outcome = OutcomeNoConsensus
		result.Steps = steps.steps
		return result, nil
	}

	result.Risk, err = assessRisk(ctx, cfg, req, result.Vote.Winner)
	if err != nil {
		return nil, err
	}
	steps.risk(result.Vote.Winner, result.Risk)
	if result.Risk.Blocked() {
		result.Outcome = OutcomeBlocked
		result.Steps = steps.steps
		return result, nil
	}

	result.Audit, err = audit(ctx, cfg, req, result.Vote)
	if err != nil {
		return nil, err
	}
	steps.review(result.Audit)
	if result.Audit.Failure != "" {
		result.Outcome = OutcomeAuditFailed
		result.Steps = steps.steps
		return result, nil
	}
	if result.Audit.Command != result.Vote.Winner {
		result.Risk, err = assessRisk(ctx, cfg, req, result.Audit.Command)
		if err != nil {
			return nil, err
		}
		steps.risk(result.Audit.Command, result.Risk)
		if result.Risk.Blocked() {
			result.Outcome = OutcomeBlocked
			result.Steps = steps.steps
			return result, nil
		}
	}
	result.Steps = steps.steps
	result.Outcome = OutcomeApproved
	result.Command = result.Audit.Command
	return result, nil
}

// stepRecorder appends the deliberation's persona steps in order, each on the
// frozen variant and tier the deliberation runs under.
type stepRecorder struct {
	cfg   Config
	index uint32
	steps []*evalv1.AgentStep
}

func (s *stepRecorder) add(role evalv1.AgentRole, persona string, output func(*evalv1.AgentStep)) {
	s.index++
	step := &evalv1.AgentStep{
		Index:        s.index,
		Role:         role,
		PersonaLabel: persona,
		ResolvedTier: s.cfg.Tier,
		ModelTag:     s.cfg.ModelTag,
		ModelDigest:  s.cfg.ModelDigest,
		CallClass:    evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
	}
	output(step)
	s.steps = append(s.steps, step)
}

func (s *stepRecorder) candidates(candidates []Candidate) {
	for _, candidate := range candidates {
		s.add(evalv1.AgentRole_AGENT_ROLE_CANDIDATE_GENERATOR, string(candidate.Member), func(step *evalv1.AgentStep) {
			step.Output = &evalv1.AgentStep_Candidate{Candidate: &evalv1.CandidateOutput{Command: candidate.Command}}
		})
	}
}

// vote records the deciding round: every member's vote is its own proposal
// in that round.
func (s *stepRecorder) vote(v Vote, candidates []Candidate) {
	votes := make([]*evalv1.CandidateVote, 0, len(candidates))
	for _, candidate := range candidates {
		votes = append(votes, &evalv1.CandidateVote{VoterLabel: string(candidate.Member), CandidateLabel: candidate.Command})
	}
	s.add(evalv1.AgentRole_AGENT_ROLE_CANDIDATE_VOTER, PersonaVoter, func(step *evalv1.AgentStep) {
		step.Output = &evalv1.AgentStep_Vote{Vote: &evalv1.VoteOutput{
			Votes:            votes,
			Rounds:           v.Round,
			ConsensusReached: v.Winner != "",
			SelectedCommand:  v.Winner,
		}}
	})
}

func (s *stepRecorder) risk(command string, risk *Risk) {
	s.add(evalv1.AgentRole_AGENT_ROLE_RISK_ANALYST, PersonaMarshal, func(step *evalv1.AgentStep) {
		step.Output = &evalv1.AgentStep_Risk{Risk: &evalv1.RiskOutput{
			SubjectKind: "command",
			Subject:     command,
			Risk:        risk.Level.Proto(),
			Blocked:     risk.Blocked(),
		}}
	})
}

func (s *stepRecorder) review(audit *Audit) {
	s.add(evalv1.AgentRole_AGENT_ROLE_REVIEWER, PersonaAuditor, func(step *evalv1.AgentStep) {
		step.Output = &evalv1.AgentStep_Review{Review: &evalv1.ReviewOutput{Verdict: audit.Verdict, Command: audit.Command}}
	})
}
