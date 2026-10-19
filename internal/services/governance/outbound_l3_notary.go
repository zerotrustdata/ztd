// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"crypto/x509"

	"github.com/zerotrustdata/ztd/internal/constants"
	approvals "github.com/zerotrustdata/ztd/internal/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

type outboundNotary struct{ roots *x509.CertPool }

// NewOutboundL3Notary pins the Operator's existing Gateway CA trust. No local
// suspended row or Gateway-signed authorization substitutes for the human proof.
func NewOutboundL3Notary(roots *x509.CertPool) (L3Notary, error) {
	if roots == nil {
		return nil, constants.ErrEmptyTrustBundle
	}
	return &outboundNotary{roots: roots.Clone()}, nil
}

func (n *outboundNotary) VerifyL3Proof(_ context.Context, userID, transactionHash, _ string, proof *commonv1.L3Proof) (bool, error) {
	if proof == nil {
		return false, constants.ErrGatewayL3ProofRequired
	}
	if proof.CliSignature != "" || len(proof.CliCertificateDer) != 0 || len(proof.CliIntermediatesDer) != 0 {
		_, err := approvals.VerifyCLIApprovalWithTrust(userID, transactionHash, proof, n.roots)
		return err == nil, err
	}
	err := approvals.VerifyPasskeyApproval(userID, transactionHash, proof)
	return err == nil, err
}
