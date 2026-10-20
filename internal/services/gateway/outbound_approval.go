// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// OutboundApprovalService checks Gateway-owned revocation/session and passkey
// registration state before transferring the original human proof to an Operator.
type OutboundApprovalService struct {
	notary  governance.L3Notary
	passkey *PasskeyService
}

func NewOutboundApprovalService(notary governance.L3Notary, passkey *PasskeyService) (*OutboundApprovalService, error) {
	if notary == nil || passkey == nil || passkey.userStore == nil {
		return nil, constants.ErrTxL3NotaryNotConfigured
	}
	return &OutboundApprovalService{notary: notary, passkey: passkey}, nil
}

// PrepareOutboundL3Proof sources context only from Gateway registration state.
// It verifies the human proof, active user/session, revocation and clone counter
// before forwarding; the Operator must still independently verify the signature.
func (s *OutboundApprovalService) PrepareOutboundL3Proof(ctx context.Context, userID, txHash, cliSessionID string, proof *commonv1.L3Proof) (*commonv1.L3Proof, error) {
	if proof == nil {
		return nil, constants.ErrGatewayL3ProofRequired
	}
	if proof.PasskeyContext != nil {
		return nil, constants.ErrTxL3ProofInvalid
	}
	prepared := proto.Clone(proof).(*commonv1.L3Proof)
	user, err := s.passkey.userStore.GetUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("outbound approval: load user: %w", err)
	}
	if user == nil || !user.IsActive() {
		return nil, constants.ErrCLIL3UserInactive
	}
	valid, err := s.notary.VerifyL3Proof(ctx, userID, txHash, cliSessionID, prepared)
	if err != nil {
		return nil, fmt.Errorf("outbound approval: verify proof: %w", err)
	}
	if !valid {
		return nil, constants.ErrTxL3ProofInvalid
	}
	if prepared.CliSignature != "" {
		return prepared, nil
	}
	for _, credential := range user.PasskeyCredentials {
		if encodeCredID(credential.ID) == prepared.CredentialId {
			prepared.PasskeyContext = &commonv1.PasskeyVerificationContext{
				UserId: userID, CredentialId: prepared.CredentialId,
				PublicKeyCose: append([]byte(nil), credential.PublicKey...), RpId: s.passkey.rpID,
				AllowedOrigins: append([]string(nil), s.passkey.rpOrigins...),
			}
			return prepared, nil
		}
	}
	return nil, constants.ErrTxL3ProofInvalid
}
