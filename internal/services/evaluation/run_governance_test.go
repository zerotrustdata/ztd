// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const consensusPosture = evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS

func testTribunalBinding() *models.ConsensusPolicy {
	return &models.ConsensusPolicy{
		ID:           "tribunal",
		Provider:     string(constants.ConsensusProviderTribunal),
		VoteKind:     "decision",
		ModelTag:     "sam860/LFM2:700m",
		ModelDigest:  repeatHex('d'),
		MemberAppIDs: []string{"m1", "m2", "m3", "m4", "m5"},
		Quorum:       3,
		Enabled:      true,
		CreatedAt:    time.Unix(1_700_000_000, 0).UTC(),
		UpdatedAt:    time.Unix(1_700_000_000, 0).UTC(),
	}
}

func frozenRun(t *testing.T, governance RunGovernance) *evalv1.EvaluationRun {
	t.Helper()
	return &evalv1.EvaluationRun{RunId: "run-g", ActivePosture: governance.Posture, Consensus: governance.Consensus}
}

func TestNewRunGovernance_ProjectsTheBindingIdentity(t *testing.T) {
	unbound, err := NewRunGovernance(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE, nil)
	require.NoError(t, err)
	assert.False(t, unbound.Consensus.GetBound())

	disabled := testTribunalBinding()
	disabled.Enabled = false
	governance, err := NewRunGovernance(consensusPosture, disabled)
	require.NoError(t, err)
	assert.False(t, governance.Consensus.GetBound(), "a disabled binding governs nothing")

	governance, err = NewRunGovernance(consensusPosture, testTribunalBinding())
	require.NoError(t, err)
	identity := governance.Consensus
	assert.True(t, identity.GetBound())
	assert.Equal(t, "tribunal", identity.GetProvider())
	assert.Equal(t, "sam860/LFM2:700m", identity.GetModelTag())
	assert.Equal(t, uint32(3), identity.GetQuorum())
	assert.Len(t, identity.GetMemberAppIds(), 5)
	assert.NotEmpty(t, identity.GetBindingDigest())
	assert.Equal(t, "consensus | consensus tribunal 3-of-5 sam860/LFM2:700m decision", DescribeRunGovernance(frozenRun(t, governance)))
}

func TestNewRunGovernance_BindingDigestIgnoresTimestampsOnly(t *testing.T) {
	base, err := NewRunGovernance(consensusPosture, testTribunalBinding())
	require.NoError(t, err)

	touched := testTribunalBinding()
	touched.UpdatedAt = touched.UpdatedAt.Add(time.Hour)
	same, err := NewRunGovernance(consensusPosture, touched)
	require.NoError(t, err)
	assert.Equal(t, base.Consensus.GetBindingDigest(), same.Consensus.GetBindingDigest())

	retuned := testTribunalBinding()
	retuned.VoteThresholds = []models.ConsensusDecisionThreshold{{}}
	changed, err := NewRunGovernance(consensusPosture, retuned)
	require.NoError(t, err)
	assert.NotEqual(t, base.Consensus.GetBindingDigest(), changed.Consensus.GetBindingDigest(), "every governing field is in the digest")
}

func TestNewRunGovernance_RejectsAnUnpinnedOrUnderQuorumBinding(t *testing.T) {
	unpinned := testTribunalBinding()
	unpinned.ModelDigest = ""
	_, err := NewRunGovernance(consensusPosture, unpinned)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)

	overQuorum := testTribunalBinding()
	overQuorum.Quorum = 6
	_, err = NewRunGovernance(consensusPosture, overQuorum)
	require.ErrorIs(t, err, constants.ErrTxL2QuorumNotMet)

	_, err = NewRunGovernance(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_UNSPECIFIED, nil)
	require.ErrorIs(t, err, constants.ErrEvaluationPostureUnsupported)
	assert.Equal(t, ExecutionFailurePosture, ClassifyExecutionFailure(err))
}

func TestValidateRunGovernance_RequiresTheFrozenPostureAndBinding(t *testing.T) {
	frozen, err := NewRunGovernance(consensusPosture, testTribunalBinding())
	require.NoError(t, err)
	run := frozenRun(t, frozen)

	live, err := NewRunGovernance(consensusPosture, testTribunalBinding())
	require.NoError(t, err)
	require.NoError(t, ValidateRunGovernance(run, live))

	ratify, err := NewRunGovernance(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY, testTribunalBinding())
	require.NoError(t, err)
	err = ValidateRunGovernance(run, ratify)
	require.ErrorIs(t, err, constants.ErrEvaluationRunGovernanceChanged)
	require.ErrorIs(t, err, constants.ErrGatewayPostureMismatch)

	rebound := testTribunalBinding()
	rebound.ModelTag, rebound.ModelDigest = "gemma4:e2b", repeatHex('e')
	rebind, err := NewRunGovernance(consensusPosture, rebound)
	require.NoError(t, err)
	err = ValidateRunGovernance(run, rebind)
	require.ErrorIs(t, err, constants.ErrEvaluationRunGovernanceChanged)
	assert.Equal(t, ExecutionFailurePosture, ClassifyExecutionFailure(err))

	unbound, err := NewRunGovernance(consensusPosture, nil)
	require.NoError(t, err)
	require.ErrorIs(t, ValidateRunGovernance(run, unbound), constants.ErrEvaluationRunGovernanceChanged)
}

func TestValidateRunGovernance_RejectsARunThatFrozeNoBinding(t *testing.T) {
	legacy := &evalv1.EvaluationRun{RunId: "legacy", ActivePosture: consensusPosture}
	live, err := NewRunGovernance(consensusPosture, nil)
	require.NoError(t, err)
	require.ErrorIs(t, ValidateRunGovernance(legacy, live), constants.ErrEvaluationRunGovernanceChanged)
	assert.Equal(t, "consensus | consensus not frozen", DescribeRunGovernance(legacy))
}

func TestValidateFormationTribunal_RequiresTheLiteSlotToBeTheBoundVoteModel(t *testing.T) {
	variants := []*evalv1.ModelVariant{
		{VariantId: "v-primary", ServedModelTag: "qwen3:8b", ModelDigest: repeatHex('a')},
		{VariantId: "v-assistant", ServedModelTag: "gemma4:e2b", ModelDigest: repeatHex('b')},
		{VariantId: "v-lite", ServedModelTag: "sam860/LFM2:700m", ModelDigest: repeatHex('d')},
		{VariantId: "v-other", ServedModelTag: "gemma4:e4b", ModelDigest: repeatHex('e')},
	}
	stacks := func(lite string) *HeterogeneousStackSet {
		return &HeterogeneousStackSet{Stacks: []*evalv1.HeterogeneousStackDefinition{{
			StackId:       "s1",
			PrimarySlot:   &evalv1.RoleAssignment{VariantId: "v-primary"},
			AssistantSlot: &evalv1.RoleAssignment{VariantId: "v-assistant"},
			LiteSlot:      &evalv1.RoleAssignment{VariantId: lite},
		}}}
	}
	generative := testTribunalBinding()
	generative.VoteKind = models.ConsensusVoteKindGenerative
	governance, err := NewRunGovernance(consensusPosture, generative)
	require.NoError(t, err)

	require.NoError(t, governance.ValidateFormationTribunal(stacks("v-lite"), variants))
	require.ErrorIs(t, governance.ValidateFormationTribunal(stacks("v-other"), variants), constants.ErrEvaluationTribunalNotLite)
	require.ErrorIs(t, governance.ValidateFormationTribunal(stacks("v-missing"), variants), constants.ErrFormationRegistryBinding)

	decision, err := NewRunGovernance(consensusPosture, testTribunalBinding())
	require.NoError(t, err)
	require.ErrorIs(t, decision.ValidateFormationTribunal(stacks("v-lite"), variants), constants.ErrEvaluationTribunalNotLite,
		"decision votes run on a separate provider, never on the Lite slot")

	ratify, err := NewRunGovernance(evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_RATIFY, generative)
	require.NoError(t, err)
	require.NoError(t, ratify.ValidateFormationTribunal(stacks("v-other"), variants), "unenforced L2 casts no gating vote")

	unbound, err := NewRunGovernance(consensusPosture, nil)
	require.NoError(t, err)
	require.NoError(t, unbound.ValidateFormationTribunal(stacks("v-other"), variants))
}
