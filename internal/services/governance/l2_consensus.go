// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// L2ConsensusPolicy is the generic consensus policy consumed by the L4 Warden.
// It is not tied to any specific consensus implementation (e.g., Consensus).
type L2ConsensusPolicy struct {
	MemberKeyIDs    []string
	Quorum          int
	RequireDistinct bool
	Enabled         bool
}

// L2ConsensusPolicyStore defines the generic interface for loading an L2
// consensus policy by ID. The L4 Warden depends on this interface rather than
// ConsensusStore, allowing alternative consensus implementations to be plugged
// in without modifying the warden.
type L2ConsensusPolicyStore interface {
	GetConsensusPolicy(ctx context.Context, id string) (*L2ConsensusPolicy, error)
}

// NoopConsensusPolicyStore is a no-op implementation of L2ConsensusPolicyStore.
// It returns nil (no policy found) with no error. Used in tests and chaos
// engineering; production outbound mode uses nil with explicit nil-checks.
type NoopConsensusPolicyStore struct{}

func (NoopConsensusPolicyStore) GetConsensusPolicy(context.Context, string) (*L2ConsensusPolicy, error) {
	return nil, nil
}

// L2Enforced reports whether posture gates this envelope on L2 consensus votes.
// The Tribunal votes only on state changes: an action the registry classifies
// as changing state against the envelope's target resource
// (constants.ActionType.ChangesState). Inference, observations, typed provider
// reads and read-only tools get no vote round. Bootstrap actions (platform
// enrollment) are exempt: they bring the consensus tribunal into existence and
// therefore predate its ability to sign.
//
// This is the single owner of the L2 gate. L4 verification and every Gateway
// deliberation site call it, so the scope cannot drift per caller. The target
// resource is in the canonical hash, and L4 binds an MCP call's target to its
// tool name, so the classification cannot be steered after admission.
func L2Enforced(envelope *govtypes.GovernanceEnvelope, posture constants.Posture) bool {
	actionType := constants.ActionType(envelope.GetActionType())
	return posture.RequiresL2() && !actionType.IsBootstrapAction() &&
		actionType.ChangesState(envelope.GetTargetResource())
}

// l2StatusFor maps the L2 result of one envelope to the status its receipt
// records. A valid proof is REQUIRED_VALID even where L2 is not enforced, because
// its verified stage is recorded as audit evidence. A failed enforced proof is
// REQUIRED_FAILED. Everything else is NOT_REQUIRED.
func l2StatusFor(envelope *govtypes.GovernanceEnvelope, posture constants.Posture, valid bool) operatorv1.L2Status {
	switch {
	case valid:
		return operatorv1.L2Status_L2_STATUS_REQUIRED_VALID
	case L2Enforced(envelope, posture):
		return operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED
	default:
		return operatorv1.L2Status_L2_STATUS_NOT_REQUIRED
	}
}

// verifyL2Posture verifies L2 (machine consensus) votes against the consensus
// policy. Returns true if L2 consensus was achieved, false if no L2 votes were
// present (and not required by posture). Returns an error if L2 is required by
// posture but verification fails. The posture is read per-envelope from
// GovernanceEnvelope.Posture by the caller and passed in here.
//
// Where L2Enforced is false, L2 votes are still verified when present and
// recorded as audit evidence, but their absence or failure does not reject the
// envelope.
func (tv *L4Warden) verifyL2Posture(ctx context.Context, envelope *govtypes.GovernanceEnvelope, computedHash string, posture constants.Posture) (bool, error) {
	enforced := L2Enforced(envelope, posture)

	if envelope.Governance == nil || envelope.Governance.L2 == nil ||
		(len(envelope.Governance.L2.Votes) == 0 && envelope.Governance.L2.ConsensusSetId == "") {
		if enforced {
			tv.logger.Error("L2 votes missing but required by posture", "posture", posture)
			return false, constants.ErrTxL2SignatureMissing
		}
		return false, nil
	}

	l2 := envelope.Governance.L2
	// A named set with zero votes is a quorum failure, including the K=1
	// application binding. Keep it distinct from an absent L2 proof above.

	if tv.signerStore == nil {
		if enforced {
			tv.logger.Error("Signer store not configured but required by posture", "posture", posture)
			return false, constants.ErrTxL2SignerStoreNotConfigured
		}
		return false, nil
	}

	if tv.consensusPolicyStore == nil {
		if enforced {
			tv.logger.Error("Consensus policy store not configured but required by posture", "posture", posture)
			return false, constants.ErrTxL2ConsensusNotConfigured
		}
		return false, nil
	}

	policy, err := tv.consensusPolicyStore.GetConsensusPolicy(ctx, l2.ConsensusSetId)
	if err != nil {
		if enforced {
			tv.logger.Error("Failed to load L2 consensus policy", "consensus_set_id", l2.ConsensusSetId, string(constants.ConnectionStateError), err)
			return false, fmt.Errorf("l4 warden: verify L2 posture: %w", err)
		}
		return false, nil
	}
	if policy == nil || !policy.Enabled {
		if enforced {
			tv.logger.Error("L2 consensus policy not found or disabled", "consensus_set_id", l2.ConsensusSetId)
			return false, constants.ErrTxL2ConsensusSetUnknown
		}
		return false, nil
	}

	if err := verifyL2Quorum(ctx, tv.signerStore, policy, l2, computedHash); err != nil {
		tv.logger.Error("L2 votes rejected", "posture", posture, string(constants.ConnectionStateError), err)
		if enforced {
			return false, err
		}
		return false, nil
	}

	return true, nil
}

// VerifyL2Quorum verifies l2's votes over transactionHash against policy. Only
// member votes count; each must verify under its member's trusted key, a
// repeated member fails where policy requires distinct signers, and at least
// policy.Quorum must approve. It is the single owner of the L2 tally: L4 runs
// it on every envelope carrying votes.
func verifyL2Quorum(ctx context.Context, signers SignerStore, policy *L2ConsensusPolicy, l2 *commonv1.L2Metadata, transactionHash string) error {
	if signers == nil {
		return constants.ErrTxL2SignerStoreNotConfigured
	}
	if policy == nil || !policy.Enabled {
		return constants.ErrTxL2ConsensusSetUnknown
	}
	members := make(map[string]bool, len(policy.MemberKeyIDs))
	for _, m := range policy.MemberKeyIDs {
		members[m] = true
	}

	seen := make(map[string]bool)
	affirmative := 0

	for _, vote := range l2.GetVotes() {
		keyID := vote.GetSignerKeyId()
		if !members[keyID] {
			continue
		}
		if seen[keyID] {
			if policy.RequireDistinct {
				return fmt.Errorf("verify L2 vote %s: %w", keyID, constants.ErrTxL2DuplicateSigner)
			}
			continue
		}
		pubKey, err := signers.GetTrustedSigner(ctx, keyID)
		if err != nil {
			return fmt.Errorf("verify L2 vote %s: %w: %w", keyID, constants.ErrTxL2SignatureInvalid, err)
		}
		if pubKey == nil {
			return fmt.Errorf("verify L2 vote %s: signer is not trusted: %w", keyID, constants.ErrTxL2SignatureInvalid)
		}
		if !verifyL2Signature(pubKey, vote.GetConsensusSignature(), transactionHash, vote.GetDecision()) {
			return fmt.Errorf("verify L2 vote %s: %w", keyID, constants.ErrTxL2SignatureInvalid)
		}
		seen[keyID] = true
		if vote.GetDecision() {
			affirmative++
		}
	}

	if affirmative < policy.Quorum {
		return fmt.Errorf("%d of %d required approvals: %w", affirmative, policy.Quorum, constants.ErrTxL2QuorumNotMet)
	}
	return nil
}

// L2VotePayload returns the exact bytes L2 votes sign and L4 verifies: "<message_id>|<decision>".
func L2VotePayload(messageID string, decision bool) []byte {
	return []byte(fmt.Sprintf("%s|%v", messageID, decision))
}

// verifyL2Signature verifies an L2 ED25519 signature.
func verifyL2Signature(pubKey ed25519.PublicKey, signature, messageID string, decision bool) bool {
	if signature == "" || signature == "UNSIGNED" {
		return false
	}
	sigBytes, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pubKey, L2VotePayload(messageID, decision), sigBytes)
}
