// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tribunal

import (
	"context"
	"time"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Dispatcher dispatches inference through the bound Inference Operator.
type Dispatcher interface {
	DispatchInference(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error)
}

// Member identifies one of the five panel members in the sealed Tribunal generation.
type Member string

const (
	MemberAxiom    Member = "axiom"
	MemberConcord  Member = "concord"
	MemberVariance Member = "variance"
	MemberPragma   Member = "pragma"
	MemberNemesis  Member = "nemesis"
)

// AllMembers defines the 5 panel members in canonical order.
var AllMembers = []Member{
	MemberAxiom,
	MemberConcord,
	MemberVariance,
	MemberPragma,
	MemberNemesis,
}

// RiskLevel represents Marshal's assessment of command risk.
type RiskLevel string

const (
	RiskLow    RiskLevel = "LOW"
	RiskMedium RiskLevel = "MEDIUM"
	RiskHigh   RiskLevel = "HIGH"
)

// ToProtoRisk converts RiskLevel to proto AgentRiskLevel.
func (r RiskLevel) ToProtoRisk() evalv1.AgentRiskLevel {
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

// Candidate records one member's generated command proposal.
type Candidate struct {
	Member         Member
	Command        string
	RawText        string
	Reasoning      string
	TransactionID  string
	Receipt        *operatorv1.ActionReceipt
	ProviderCallID string
}

// CandidateVote represents one voter's choice of command label.
type CandidateVote struct {
	VoterMember    string
	CandidateLabel string
}

// VoteResult records the outcome of the clustering and weighted voting stage.
type VoteResult struct {
	WinnerCommand    string
	Votes            []CandidateVote
	Rounds           uint32
	ConsensusReached bool
	Clusters         map[string][]Member // command -> members supporting it
	TransactionIDs   []string
	Receipts         []*operatorv1.ActionReceipt
}

// RiskAnalysis records Marshal's evaluation of the selected command.
type RiskAnalysis struct {
	SubjectKind   string
	Subject       string
	Risk          RiskLevel
	Blocked       bool
	Rationale     string
	Feedback      string
	TransactionID string
	Receipt       *operatorv1.ActionReceipt
}

// AuditResult records the Auditor's review of the selected command.
type AuditResult struct {
	Verdict       evalv1.AgentReviewVerdict
	FinalCommand  string
	Rationale     string
	TransactionID string
	Receipt       *operatorv1.ActionReceipt
}

// PipelineConfig configures the Tribunal generation, voting, risk, and audit stages.
type PipelineConfig struct {
	Dispatcher          Dispatcher
	InferenceSessionID  string
	Variant             *evalv1.ModelVariant
	CampaignID          string
	ModelRegistryDigest string
	ModelRegistry       []*operatorv1.InferenceModelVariant
	NewID               func(prefix string) string
	Now                 func() time.Time
}

// PipelineRequest contains the agent's intent-only request and execution context.
type PipelineRequest struct {
	Request                   string // intent: natural language description of what to accomplish
	Guidelines                string // plain language constraints/guidelines
	WorkingDirectory          string
	OS                        string
	Shell                     string
	Turn                      int
	AttemptID                 string
	CircuitBreakerStrikeCount int // previous consecutive high-risk blocks
}

// PipelineResult records the full outcome and all discrete persona steps of the intent pipeline.
type PipelineResult struct {
	FinalCommand          string
	Candidates            []Candidate
	Vote                  *VoteResult
	Risk                  *RiskAnalysis
	Audit                 *AuditResult
	Steps                 []*evalv1.AgentStep
	Receipts              []*operatorv1.ActionReceipt
	TransactionIDs        []string
	HighRiskBlocked       bool
	CircuitBreakerTripped bool
	Feedback              string
}
