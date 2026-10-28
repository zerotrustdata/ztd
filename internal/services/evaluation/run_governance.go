// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// RunGovernance is the live Gateway governance one run executes under: the
// immutable Gateway posture and the L2 consensus binding. A run freezes it at
// start and refuses to execute or resume under any other.
type RunGovernance struct {
	Posture   evalv1.EvaluationGovernancePosture
	Consensus *evalv1.EvaluationConsensusIdentity
}

// NewRunGovernance projects the live posture and binding. A nil binding is
// unbound. Under the tribunal provider the binding must pin its vote model.
func NewRunGovernance(posture evalv1.EvaluationGovernancePosture, binding *models.ConsensusPolicy) (RunGovernance, error) {
	identity, err := consensusIdentity(binding)
	if err != nil {
		return RunGovernance{}, err
	}
	governance := RunGovernance{Posture: posture, Consensus: identity}
	if err := governance.validate(); err != nil {
		return RunGovernance{}, err
	}
	return governance, nil
}

// validate admits only a known posture with a frozen consensus identity.
// Ratify and notary dispatches wait for real human proof; eval mints none.
func (g RunGovernance) validate() error {
	if _, err := cases.GovernancePosture(g.Posture); err != nil {
		return fmt.Errorf("evaluation: run governance posture: %w: %w", constants.ErrEvaluationPostureUnsupported, err)
	}
	if g.Consensus == nil {
		return fmt.Errorf("evaluation: run governance froze no consensus binding: %w", constants.ErrEvaluationRunGovernanceChanged)
	}
	return nil
}

func consensusIdentity(binding *models.ConsensusPolicy) (*evalv1.EvaluationConsensusIdentity, error) {
	if binding == nil || !binding.Enabled {
		return &evalv1.EvaluationConsensusIdentity{}, nil
	}
	provider, err := binding.ConsensusProvider()
	if err != nil {
		return nil, fmt.Errorf("evaluation: run governance consensus provider: %w", err)
	}
	if provider == constants.ConsensusProviderTribunal && (binding.ModelTag == "" || binding.ModelDigest == "") {
		return nil, fmt.Errorf("evaluation: run governance: tribunal binding pins no vote model: %w", constants.ErrMissingRequiredField)
	}
	if binding.Quorum < 1 || len(binding.MemberAppIDs) < binding.Quorum {
		return nil, fmt.Errorf("evaluation: run governance: binding quorum %d of %d members: %w", binding.Quorum, len(binding.MemberAppIDs), constants.ErrTxL2QuorumNotMet)
	}
	governing := *binding
	governing.CreatedAt, governing.UpdatedAt = time.Time{}, time.Time{}
	encoded, err := json.Marshal(governing)
	if err != nil {
		return nil, fmt.Errorf("evaluation: run governance: encode binding: %w", err)
	}
	return &evalv1.EvaluationConsensusIdentity{
		Bound:         true,
		BindingId:     binding.ID,
		Provider:      string(provider),
		VoteKind:      binding.VoteKind,
		ModelTag:      binding.ModelTag,
		ModelDigest:   binding.ModelDigest,
		Quorum:        uint32(binding.Quorum),
		MemberAppIds:  slices.Clone(binding.MemberAppIDs),
		BindingDigest: models.SHA256Hex(encoded),
	}, nil
}

// ValidateRunGovernance requires the live Gateway governance to equal what the
// run froze at start. A run started before the consensus freeze existed has no
// frozen binding and never executes again.
func ValidateRunGovernance(run *evalv1.EvaluationRun, live RunGovernance) error {
	if run.GetConsensus() == nil {
		return fmt.Errorf("evaluation: run %q froze no consensus binding: %w", run.GetRunId(), constants.ErrEvaluationRunGovernanceChanged)
	}
	if run.GetActivePosture() != live.Posture {
		return fmt.Errorf("evaluation: run %q froze posture %s, Gateway runs %s: %w: %w", run.GetRunId(), run.GetActivePosture(), live.Posture, constants.ErrEvaluationRunGovernanceChanged, constants.ErrGatewayPostureMismatch)
	}
	if !proto.Equal(run.GetConsensus(), live.Consensus) {
		return fmt.Errorf("evaluation: run %q froze consensus %s, Gateway binds %s: %w", run.GetRunId(), describeConsensusIdentity(run.GetConsensus()), describeConsensusIdentity(live.Consensus), constants.ErrEvaluationRunGovernanceChanged)
	}
	return nil
}

// DescribeRunGovernance renders the frozen governance for run output.
func DescribeRunGovernance(run *evalv1.EvaluationRun) string {
	posture := run.GetActivePosture().String()
	if p, err := cases.GovernancePosture(run.GetActivePosture()); err == nil {
		posture = string(p)
	}
	return posture + " | consensus " + describeConsensusIdentity(run.GetConsensus())
}

func describeConsensusIdentity(identity *evalv1.EvaluationConsensusIdentity) string {
	switch {
	case identity == nil:
		return "not frozen"
	case !identity.GetBound():
		return "not bound"
	}
	line := fmt.Sprintf("%s %d-of-%d", identity.GetProvider(), identity.GetQuorum(), len(identity.GetMemberAppIds()))
	if identity.GetModelTag() != "" {
		line += " " + identity.GetModelTag()
	}
	if identity.GetVoteKind() != "" {
		line += " " + identity.GetVoteKind()
	}
	return line
}
