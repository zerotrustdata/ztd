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

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// EvidenceSignerSource supplies public keys from an explicit verifier trust boundary.
type EvidenceSignerSource interface {
	GetTrustedSignerPublicKey(context.Context, string) (ed25519.PublicKey, error)
}

// PinnedSignerSource resolves only the public key supplied by the verifier's
// trust boundary. A key ID in evidence cannot introduce a new trusted key.
type PinnedSignerSource struct {
	publicKey ed25519.PublicKey
}

func NewPinnedSignerSource(publicKey ed25519.PublicKey) *PinnedSignerSource {
	return &PinnedSignerSource{publicKey: append(ed25519.PublicKey(nil), publicKey...)}
}

func (s *PinnedSignerSource) GetTrustedSignerPublicKey(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || len(s.publicKey) != ed25519.PublicKeySize || keyID != hex.EncodeToString(s.publicKey) {
		return nil, constants.ErrTrustedSignerKeyNotFound
	}
	return append(ed25519.PublicKey(nil), s.publicKey...), nil
}

// CommitmentRowMatchesAttestation checks the SQL projection against the signed
// fields; callers separately verify the canonical hash and pinned signature.
func CommitmentRowMatchesAttestation(row *storage.CommitmentRow, attestation *operatorv1.CommitmentAttestation) bool {
	return row != nil && attestation != nil &&
		row.TransactionID == attestation.TransactionId &&
		row.TransactionHash == attestation.TransactionHash &&
		row.PriorCommitmentHash == attestation.PriorCommitmentHash &&
		row.Hash == attestation.Hash &&
		row.StateRootAtCommit == attestation.StateRootAtCommit &&
		row.L2SignatureDigest == attestation.L2SignatureDigest &&
		row.WardenIntentSignatureDigest == attestation.WardenIntentSignatureDigest &&
		row.HumanSignatureDigest == attestation.HumanSignatureDigest &&
		row.ActionType == attestation.ActionType &&
		row.TargetResource == attestation.TargetResource &&
		row.CommittedAt.UnixMilli() == attestation.CommittedAtUnixMs &&
		row.AuditorKeyID == attestation.AuditorKeyId &&
		row.Signature == attestation.Signature
}

// ReceiptMatchesCommitment binds the signed receipt's commitment stage to the
// local ledger. Receipt signature verification also authenticates all stages.
func ReceiptMatchesCommitment(receipt *operatorv1.EventReceipt, row *storage.CommitmentRow) bool {
	if receipt == nil || row == nil || receipt.GetTransactionId() != row.TransactionID || receipt.GetTransactionHash() != row.TransactionHash {
		return false
	}
	matched := false
	for _, stage := range receipt.GetDeterministicStageEvidence() {
		if stage.GetKind() != operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND {
			continue
		}
		if matched || stage.GetTransactionId() != row.TransactionID || stage.GetTransactionHash() != row.TransactionHash ||
			stage.GetCommitmentHash() != row.Hash || stage.GetPriorCommitmentHash() != row.PriorCommitmentHash ||
			stage.GetSignerKeyId() != row.AuditorKeyID || stage.GetL2SignatureDigest() != row.L2SignatureDigest || stage.GetL3SignatureDigest() != row.HumanSignatureDigest {
			return false
		}
		matched = true
	}
	return matched
}
