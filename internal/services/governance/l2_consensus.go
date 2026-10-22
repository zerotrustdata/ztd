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
	// Provider is the binding's vote producer. Only a tribunal policy can
	// prove a deliberation attestation.
	Provider constants.ConsensusProvider
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

// L2Enforced reports whether posture gates this action on L2 consensus votes.
// Bootstrap actions (platform enrollment) are exempt: they bring the consensus
// tribunal into existence and therefore predate its ability to sign.
//
// This is the single owner of the L2 gate. L4 verification and every Gateway
// deliberation site call it, so the bootstrap exemption cannot drift per caller.
func L2Enforced(actionType constants.ActionType, posture constants.Posture) bool {
	return posture.RequiresL2() && !actionType.IsBootstrapAction()
}

// l2StatusFor maps the L2 result of one envelope to the status its receipt
// records. A valid proof is REQUIRED_VALID even where L2 is not enforced, because
// its verified stage is recorded as audit evidence. A failed enforced proof is
// REQUIRED_FAILED. Everything else is NOT_REQUIRED.
//
// A deliberation-exempt envelope reaching here has had its attestation proven
// by verifyL2Posture, so it is NOT_REQUIRED even under an L2 posture.
func l2StatusFor(envelope *govtypes.GovernanceEnvelope, posture constants.Posture, valid bool) operatorv1.L2Status {
	actionType := constants.ActionType(envelope.GetActionType())
	switch {
	case valid:
		return operatorv1.L2Status_L2_STATUS_REQUIRED_VALID
	case L2Enforced(actionType, posture) && !L2DeliberationExempt(envelope):
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
	actionType := constants.ActionType(envelope.ActionType)
	enforced := L2Enforced(actionType, posture)

	// A deliberation attestation is verified first and in every posture. A
	// proven one exempts the envelope from L2 (NOT_REQUIRED); any other
	// envelope carrying one is rejected.
	if envelope.GetGovernance().GetL2().GetDeliberation() != nil {
		if err := tv.verifyL2Deliberation(ctx, envelope, computedHash); err != nil {
			tv.logger.Error("L2 deliberation attestation rejected", string(constants.ConnectionStateError), err)
			return false, err
		}
		return false, nil
	}

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

	// A witness observation's votes are its parent inference's quorum. Once
	// the payload is proven to name that parent, they are verified against the
	// parent hash exactly as the parent's own L4 verifies them.
	if parent := l2.GetParentTransactionHash(); parent != "" {
		if err := VerifyL2WitnessBinding(envelope); err != nil {
			tv.logger.Error("L2 witness binding rejected", string(constants.ConnectionStateError), err)
			return false, err
		}
		computedHash = parent
	}

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

	if err := VerifyL2Quorum(ctx, tv.signerStore, policy, l2, computedHash); err != nil {
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
// it on every envelope carrying votes, and Gateway dispatch runs it before it
// admits a deliberated inference's witnesses.
func VerifyL2Quorum(ctx context.Context, signers SignerStore, policy *L2ConsensusPolicy, l2 *commonv1.L2Metadata, transactionHash string) error {
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

// L2DeliberationPayload returns the exact bytes a Tribunal member's
// deliberation attestation signs and L4 verifies:
// "<message_id>|consensus-deliberation". It never equals an L2VotePayload, so a
// vote can never be replayed as an attestation, nor an attestation as a vote.
func L2DeliberationPayload(messageID string) []byte {
	return []byte(messageID + "|consensus-deliberation")
}

// L2DeliberationExempt reports whether an envelope claims the deliberation
// exemption from L2: a deliberation action carrying a deliberation attestation
// and no votes. It is the single owner of the claim. Gateway deliberation sites
// skip deliberation when it holds; L4 accepts the claim only after
// verifyL2Deliberation proves it, and rejects any other envelope carrying an
// attestation.
func L2DeliberationExempt(envelope *govtypes.GovernanceEnvelope) bool {
	l2 := envelope.GetGovernance().GetL2()
	return isDeliberationAction(constants.ActionType(envelope.GetActionType())) &&
		l2.GetDeliberation() != nil && len(l2.GetVotes()) == 0 && l2.GetParentTransactionHash() == ""
}

// L2WitnessBound reports whether an envelope claims its parent inference's L2
// quorum: a witness observation command naming a parent transaction hash and
// carrying no deliberation attestation. It is the single owner of the claim.
// Gateway deliberation sites skip deliberation when it holds; L4 accepts it
// only after VerifyL2WitnessBinding proves the payload names that parent and
// the votes prove the parent's quorum.
func L2WitnessBound(envelope *govtypes.GovernanceEnvelope) bool {
	l2 := envelope.GetGovernance().GetL2()
	return isWitnessAction(constants.ActionType(envelope.GetActionType())) &&
		l2.GetParentTransactionHash() != "" && l2.GetDeliberation() == nil
}

// isWitnessAction reports whether an action witnesses an inference attempt: the
// provider-boundary and model-provenance observation commands.
func isWitnessAction(actionType constants.ActionType) bool {
	return actionType == constants.ActionTypeProviderBoundaryObservation ||
		actionType == constants.ActionTypeModelProvenanceObservation
}

// witnessCommand is the observation command payload naming the inference
// transaction it witnesses.
type witnessCommand interface {
	GetInferenceTransactionId() string
}

// VerifyL2WitnessBinding proves a witness binding before its votes are checked
// against the parent hash: the envelope is L2WitnessBound and its own payload,
// covered by its own transaction hash, names that parent. Every failure, in
// every posture, is ErrTxL2SignatureInvalid: a binding is never audit-only. The
// Gateway runs it when it attaches a binding; L4 runs it before the votes.
func VerifyL2WitnessBinding(envelope *govtypes.GovernanceEnvelope) error {
	if !L2WitnessBound(envelope) {
		return fmt.Errorf("l4 warden: witness binding outside an observation command: %w", constants.ErrTxL2SignatureInvalid)
	}
	decoded, err := DecodePayloadForAction(constants.ActionType(envelope.GetActionType()), envelope.GetPayload())
	if err != nil {
		return fmt.Errorf("l4 warden: witness binding: %w: %w", constants.ErrTxL2SignatureInvalid, err)
	}
	command, ok := decoded.(witnessCommand)
	if !ok || command.GetInferenceTransactionId() != envelope.GetGovernance().GetL2().GetParentTransactionHash() {
		return fmt.Errorf("l4 warden: witness binding names another inference: %w", constants.ErrTxL2SignatureInvalid)
	}
	return nil
}

// isDeliberationAction reports whether a Tribunal member's vote may produce an
// envelope of this action: the member's INFERENCE, and the provider-boundary
// and model-provenance observation commands that same inference attempt emits.
// Without the observations, each vote's observation would itself be voted on,
// recursing without bound.
func isDeliberationAction(actionType constants.ActionType) bool {
	switch actionType {
	case constants.ActionTypeInference,
		constants.ActionTypeProviderBoundaryObservation,
		constants.ActionTypeModelProvenanceObservation:
		return true
	default:
		return false
	}
}

// verifyL2Deliberation verifies a deliberation attestation. It holds only for
// an L2DeliberationExempt envelope naming a bound, enabled Tribunal-provider
// consensus set whose member, an enabled trusted signer, signed
// L2DeliberationPayload over the recomputed hash. Every failure, in every
// posture, is ErrTxL2SignatureInvalid: an attestation is never audit-only.
func (tv *L4Warden) verifyL2Deliberation(ctx context.Context, envelope *govtypes.GovernanceEnvelope, computedHash string) error {
	if !L2DeliberationExempt(envelope) {
		return fmt.Errorf("l4 warden: deliberation attestation outside a vote-free inference: %w", constants.ErrTxL2SignatureInvalid)
	}
	if tv.signerStore == nil || tv.consensusPolicyStore == nil {
		return fmt.Errorf("l4 warden: deliberation attestation: %w", constants.ErrTxL2SignatureInvalid)
	}
	l2 := envelope.GetGovernance().GetL2()
	attestation := l2.GetDeliberation()
	policy, err := tv.consensusPolicyStore.GetConsensusPolicy(ctx, l2.GetConsensusSetId())
	if err != nil {
		return fmt.Errorf("l4 warden: deliberation attestation policy: %w: %w", constants.ErrTxL2SignatureInvalid, err)
	}
	if policy == nil || !policy.Enabled || policy.Provider != constants.ConsensusProviderTribunal {
		return fmt.Errorf("l4 warden: deliberation attestation needs a bound tribunal consensus: %w", constants.ErrTxL2SignatureInvalid)
	}
	member := false
	for _, id := range policy.MemberKeyIDs {
		if id == attestation.GetSignerKeyId() {
			member = true
			break
		}
	}
	if !member {
		return fmt.Errorf("l4 warden: deliberation attestation signer %q is not a member: %w", attestation.GetSignerKeyId(), constants.ErrTxL2SignatureInvalid)
	}
	pubKey, err := tv.signerStore.GetTrustedSigner(ctx, attestation.GetSignerKeyId())
	if err != nil {
		return fmt.Errorf("l4 warden: deliberation attestation signer %q: %w: %w", attestation.GetSignerKeyId(), constants.ErrTxL2SignatureInvalid, err)
	}
	sig, err := hex.DecodeString(attestation.GetSignature())
	if pubKey == nil || err != nil || !ed25519.Verify(pubKey, L2DeliberationPayload(computedHash), sig) {
		return fmt.Errorf("l4 warden: deliberation attestation signer %q: %w", attestation.GetSignerKeyId(), constants.ErrTxL2SignatureInvalid)
	}
	return nil
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
