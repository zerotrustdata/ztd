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

// TestNewRunGovernance_FreezesTheHostedVoteProvider: a run freezes which
// provider class serves the vote model, so moving the same tag between a
// local and a hosted provider is a governance change the run refuses.
func TestNewRunGovernance_FreezesTheHostedVoteProvider(t *testing.T) {
	hosted := testTribunalBinding()
	hosted.VoteKind, hosted.ModelTag, hosted.ModelProviderClass = "", "gemini-3.6-flash", constants.InferenceProviderGemini
	governance, err := NewRunGovernance(consensusPosture, hosted)
	require.NoError(t, err)
	assert.Equal(t, constants.InferenceProviderGemini, governance.Consensus.GetModelProviderClass())
	assert.Equal(t, "consensus | consensus tribunal 3-of-5 gemini-3.6-flash (hosted: gemini)", DescribeRunGovernance(frozenRun(t, governance)))

	local := testTribunalBinding()
	local.VoteKind, local.ModelTag = "", "gemini-3.6-flash"
	live, err := NewRunGovernance(consensusPosture, local)
	require.NoError(t, err)
	assert.Empty(t, live.Consensus.GetModelProviderClass())
	require.ErrorIs(t, ValidateRunGovernance(frozenRun(t, governance), live), constants.ErrEvaluationRunGovernanceChanged)
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
