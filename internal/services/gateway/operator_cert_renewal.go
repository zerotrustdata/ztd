// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/protocol"
)

// operatorLeafAuthority is the PKI surface renewal uses: the revocation check
// on the presenting certificate and the signing of the next one.
// *PKIAuthority implements it.
type operatorLeafAuthority interface {
	VerifyCertificate(ctx context.Context, cert *x509.Certificate) error
	SignCSR(csrPEM string, leafType string, organizationID, operatorID, userID, sessionID, gatewayID string) (certPEM, chainPEM string, err error)
}

// operatorL2TrustSource resolves the L2 trust an Operator holds.
// *ConsensusStoreService implements it.
type operatorL2TrustSource interface {
	OperatorL2Trust(ctx context.Context, id ...string) (*models.OperatorL2Trust, error)
}

// OperatorCertRenewalService renews an outbound Operator's leaf over its mTLS
// pub/sub connection (the renewal: channel). The identity comes from the
// certificate that authenticated the connection, never from the request: the
// renewed leaf names the same organization, Operator, and session. The reply
// also carries the Gateway's current L2 trust, which replaces the trust the
// Operator holds.
//
// Errors that wrap constants.ErrOperatorRenewalRefused are final for this
// identity; any other error is a Gateway-side failure a later attempt may
// clear.
type OperatorCertRenewalService struct {
	pki      operatorLeafAuthority
	sessions operatorSessionValidator
	trust    operatorL2TrustSource
	logger   *slog.Logger
}

// NewOperatorCertRenewalService builds the renewal service.
func NewOperatorCertRenewalService(pki operatorLeafAuthority, sessions operatorSessionValidator, trust operatorL2TrustSource, logger *slog.Logger) *OperatorCertRenewalService {
	return &OperatorCertRenewalService{pki: pki, sessions: sessions, trust: trust, logger: logger}
}

// Renew signs req.CSR for the Operator workload peer names. channel is the
// renewal channel the request arrived on and must be that workload's own.
func (s *OperatorCertRenewalService) Renew(ctx context.Context, peer *x509.Certificate, channel string, req *models.OperatorRenewalRequest) (*models.OperatorRenewal, error) {
	workload, err := operatorWorkloadOf(peer)
	if err != nil {
		return nil, err
	}
	if channel != pubsub.RenewalChannel(workload.OperatorID, workload.OperatorSessionID) {
		return nil, fmt.Errorf("%w: %w: %s", constants.ErrOperatorRenewalRefused, constants.ErrOperatorRenewalChannelMismatch, channel)
	}
	if err := s.pki.VerifyCertificate(ctx, peer); err != nil {
		if errors.Is(err, constants.ErrPKICertificateRevoked) {
			return nil, fmt.Errorf("%w: %w", constants.ErrOperatorRenewalRefused, err)
		}
		return nil, fmt.Errorf("operator renewal: verify certificate: %w", err)
	}
	op, err := s.sessions.ValidateOperatorSession(ctx, workload.OperatorSessionID)
	if err != nil {
		var authErr *AuthError
		if errors.As(err, &authErr) {
			return nil, fmt.Errorf("%w: %s", constants.ErrOperatorRenewalRefused, authErr.Message)
		}
		return nil, fmt.Errorf("operator renewal: validate session: %w", err)
	}
	if op.GetId() != workload.OperatorID || op.GetOrganizationId() != workload.OrganizationID {
		return nil, fmt.Errorf("%w: %w", constants.ErrOperatorRenewalRefused, constants.ErrOperatorRenewalSessionMismatch)
	}
	if req == nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrOperatorRenewalRefused, constants.ErrOperatorRenewalInvalidFrame)
	}
	if err := checkRenewalCSR(req.CSR); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrOperatorRenewalRefused, err)
	}

	certPEM, chainPEM, err := s.pki.SignCSR(req.CSR, constants.LeafTypeOperator, workload.OrganizationID, workload.OperatorID, "", workload.OperatorSessionID, "")
	if err != nil {
		return nil, fmt.Errorf("operator renewal: sign: %w", err)
	}
	l2Trust, err := s.trust.OperatorL2Trust(ctx)
	if err != nil {
		return nil, fmt.Errorf("operator renewal: l2 trust: %w", err)
	}
	s.logger.Info("operator renewal: certificate renewed",
		"operator_id", workload.OperatorID,
		"operator_session_id", workload.OperatorSessionID,
		"previous_serial", peer.SerialNumber.String())
	return &models.OperatorRenewal{Certificate: certPEM, CertificateChain: chainPEM, L2Trust: l2Trust}, nil
}

// operatorWorkloadOf reads the Operator workload from the certificate that
// authenticated the connection.
func operatorWorkloadOf(peer *x509.Certificate) (protocol.OperatorWorkload, error) {
	if peer == nil || len(peer.URIs) != 1 {
		return protocol.OperatorWorkload{}, fmt.Errorf("%w: %w", constants.ErrOperatorRenewalRefused, constants.ErrOperatorRenewalPeerNotOperator)
	}
	workload, ok := protocol.NewWorkloadIdentity().ExtractOperatorIdentity(peer.URIs[0].String())
	if !ok {
		return protocol.OperatorWorkload{}, fmt.Errorf("%w: %w", constants.ErrOperatorRenewalRefused, constants.ErrOperatorRenewalPeerNotOperator)
	}
	return workload, nil
}

// checkRenewalCSR rejects a CSR the Operator could not have produced for its
// own identity. SignCSR copies DNS and IP names from the CSR, so a renewal
// CSR naming any subject alternative name is refused before signing.
func checkRenewalCSR(csrPEM string) error {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return constants.ErrPKIInvalidCSR
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrPKIParseCSR, err)
	}
	if err := csr.CheckSignature(); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrPKICSRSignatureCheck, err)
	}
	if !isCurveP256(csr.PublicKey) {
		return constants.ErrPKIInvalidCurve
	}
	if len(csr.DNSNames) > 0 || len(csr.IPAddresses) > 0 || len(csr.URIs) > 0 || len(csr.EmailAddresses) > 0 {
		return constants.ErrOperatorRenewalCSRSubjectAltName
	}
	return nil
}
