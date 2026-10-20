// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package tribunal turns one intent-only command request into one concrete
// shell command by deliberation: a sealed five-member generation, a
// deterministic vote with one anonymized regeneration round, Marshal's risk
// block, and the Auditor's review. It owns no dispatch: every model call goes
// through the caller's Inferer, which runs it as governed inference and
// records its receipt. The chosen command then runs through real L1-L5
// governance like any other; the deliberation never stands in for an L2
// quorum proof.
package tribunal

import (
	"context"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// Stage names the deliberation stage a model call serves.
type Stage string

const (
	StageGenerate Stage = "generate"
	StageMarshal  Stage = "marshal"
	StageAudit    Stage = "audit"
)

// Persona labels for the non-member stages.
const (
	PersonaMarshal = "marshal"
	PersonaAuditor = "auditor"
	PersonaVoter   = "tribunal"
)

// Call is one model call the deliberation needs answered.
type Call struct {
	Stage   Stage
	Persona string
	// Round is the generation round (1 or 2) of a generate call, and the
	// attempt (1 or 2) of an audit call.
	Round  uint32
	System string
	User   string
}

// Inferer answers one deliberation call through governed inference and
// returns the model's answer text. An error is an inference failure: the
// deliberation stops and returns it unchanged.
type Inferer interface {
	Infer(ctx context.Context, call Call) (string, error)
}

// Member is one of the five sealed generation panel members.
type Member string

const (
	MemberAxiom    Member = "axiom"
	MemberConcord  Member = "concord"
	MemberVariance Member = "variance"
	MemberPragma   Member = "pragma"
	MemberNemesis  Member = "nemesis"
)

// AllMembers is the panel in canonical generation order.
var AllMembers = []Member{MemberAxiom, MemberConcord, MemberVariance, MemberPragma, MemberNemesis}

// generationRounds is the sealed round plus the one anonymized regeneration.
const generationRounds = 2

// MaxMemberCalls bounds the member inferences of one deliberation: every
// member in both generation rounds, Marshal on the vote winner and again on a
// command the Auditor changed, and each Auditor attempt.
var MaxMemberCalls = generationRounds*len(AllMembers) + 2 + auditorMaxAttempts

// RiskLevel is Marshal's classification of a command.
type RiskLevel string

const (
	RiskLow    RiskLevel = "LOW"
	RiskMedium RiskLevel = "MEDIUM"
	RiskHigh   RiskLevel = "HIGH"
)

// Proto converts the level to its trace contract form.
func (r RiskLevel) Proto() evalv1.AgentRiskLevel {
	switch r {
	case RiskLow:
		return evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_LOW
	case RiskMedium:
		return evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_MEDIUM
	case RiskHigh:
		return evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_HIGH
	default:
		return evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_UNSPECIFIED
	}
}

// Outcome is how one deliberation ended.
type Outcome string

const (
	// OutcomeApproved: the Auditor approved, revised or swapped to a command.
	OutcomeApproved Outcome = "approved"
	// OutcomeNoConsensus: no command reached the minimum consensus after the
	// regeneration round.
	OutcomeNoConsensus Outcome = "no_consensus"
	// OutcomeBlocked: Marshal classified the command that would run HIGH risk:
	// the vote winner, or the Auditor's command when the Auditor changed it.
	OutcomeBlocked Outcome = "blocked"
	// OutcomeAuditFailed: the Auditor gave no valid verdict; the deliberation
	// fails closed and nothing runs.
	OutcomeAuditFailed Outcome = "audit_failed"
)

// Config binds one deliberation to the frozen model it runs on. Tier is the
// campaign role every Tribunal call dispatches under: Service.Deliberate
// requires the member template's role to be Tier's inference role. ModelTag
// and ModelDigest identify the variant on each recorded step.
type Config struct {
	Inferer     Inferer
	Tier        evalv1.ModelCampaignRole
	ModelTag    string
	ModelDigest string
}

// Request is the agent's intent-only command request and the context the
// command runs in.
type Request struct {
	Request          string
	Guidelines       string
	WorkingDirectory string
	OS               string
	Shell            string
}

// Candidate is one member's proposal in one generation round.
type Candidate struct {
	Member  Member
	Round   uint32
	Command string
}

// Cluster is every member that proposed one normalized command, in member
// order. ID is the anonymized label the cluster is shown under.
type Cluster struct {
	ID      string
	Command string
	Members []Member
}

// Vote is the deterministic vote over one round's candidates.
type Vote struct {
	Round    uint32
	Clusters []Cluster
	// Winner is the selected command; empty when no cluster reached the
	// minimum consensus or the tie-break ladder left a tie.
	Winner string
}

// Unanimous reports whether every panel member proposed the winner.
func (v Vote) Unanimous() bool {
	for _, cluster := range v.Clusters {
		if cluster.Command == v.Winner {
			return len(cluster.Members) == len(AllMembers)
		}
	}
	return false
}

// Risk is Marshal's assessment of the vote winner.
type Risk struct {
	Level     RiskLevel
	Rationale string
	Feedback  string
	// Invalid reports that Marshal's answer was not a valid assessment and
	// the command was classified HIGH by fail-closed rule.
	Invalid bool
}

// Blocked reports whether the assessment blocks the command.
func (r Risk) Blocked() bool { return r.Level == RiskHigh }

// Audit is the Auditor's review of the vote winner.
type Audit struct {
	Verdict   evalv1.AgentReviewVerdict
	Command   string
	Rationale string
	// Failure explains why no valid verdict was given.
	Failure string
}

// Result is the full record of one deliberation.
type Result struct {
	Outcome Outcome
	// Command is the command to dispatch; set only when Outcome is approved.
	Command    string
	Candidates []Candidate
	Vote       Vote
	// Risk is Marshal's latest assessment: of the vote winner, or of the
	// Auditor's command when the Auditor changed it. Nil when no command was
	// assessed.
	Risk  *Risk
	Audit *Audit
	// Steps are the deliberation's persona steps in order.
	Steps []*evalv1.AgentStep
}
