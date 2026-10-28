// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"regexp"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

func validatePlatformEnrollmentRequest(request models.PlatformEnrollmentCreateRequest) (models.PlatformEnrollmentCSRFingerprints, error) {
	if err := request.ValidateShape(); err != nil {
		return models.PlatformEnrollmentCSRFingerprints{}, err
	}
	if !validPlatformInstanceID(request.InstanceID) {
		return models.PlatformEnrollmentCSRFingerprints{}, constants.ErrPlatformEnrollmentInvalidInstanceID
	}
	if !validPlatformHostname(request.Hostname) {
		return models.PlatformEnrollmentCSRFingerprints{}, constants.ErrPlatformEnrollmentInvalidHostname
	}
	fingerprints := models.PlatformEnrollmentCSRFingerprints{}
	if request.App != nil {
		fingerprint, _, err := parsePlatformEnrollmentCSR(request.App.CSRPEM)
		if err != nil {
			return fingerprints, err
		}
		fingerprints.App = fingerprint
		key, err := request.App.SigningKey()
		if err != nil {
			return fingerprints, err
		}
		if key != nil {
			if request.ComponentKind != models.PlatformComponentApplication {
				return fingerprints, constants.ErrPlatformEnrollmentInvalidComponent
			}
			digest := sha256.Sum256(key)
			fingerprints.SigningKey = hex.EncodeToString(digest[:])
		}
		return fingerprints, nil
	}

	operatorFingerprint, _, err := parsePlatformEnrollmentCSR(request.Operator.OperatorCSRPEM)
	if err != nil {
		return fingerprints, err
	}
	cliFingerprint, _, err := parsePlatformEnrollmentCSR(request.Operator.CLICSRPEM)
	if err != nil {
		return fingerprints, err
	}
	if subtle.ConstantTimeCompare([]byte(operatorFingerprint), []byte(cliFingerprint)) == 1 {
		return fingerprints, constants.ErrPlatformEnrollmentDuplicateKey
	}
	auditorKey, err := request.Operator.AuditorKey()
	if err != nil {
		return fingerprints, err
	}
	auditorDigest := sha256.Sum256(auditorKey)
	fingerprints.Operator = operatorFingerprint
	fingerprints.CLI = cliFingerprint
	fingerprints.AuditorKey = hex.EncodeToString(auditorDigest[:])
	return fingerprints, nil
}

func parsePlatformEnrollmentCSR(csrPEM string) (string, *ecdsa.PublicKey, error) {
	block, rest := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(rest) != 0 {
		return "", nil, constants.ErrPlatformEnrollmentInvalidCSR
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", nil, fmt.Errorf("platform enrollment: parse CSR: %w", constants.ErrPlatformEnrollmentInvalidCSR)
	}
	if err := csr.CheckSignature(); err != nil {
		return "", nil, fmt.Errorf("platform enrollment: verify CSR signature: %w", constants.ErrPlatformEnrollmentInvalidCSR)
	}
	publicKey, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve != elliptic.P256() {
		return "", nil, constants.ErrPlatformEnrollmentUnsupportedKey
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", nil, fmt.Errorf("platform enrollment: marshal CSR public key: %w", constants.ErrPlatformEnrollmentInvalidCSR)
	}
	digest := sha256.Sum256(publicDER)
	return hex.EncodeToString(digest[:]), publicKey, nil
}

func platformEnrollmentCompletionTranscript(request *models.PlatformEnrollmentRequest) ([]byte, error) {
	componentKind, err := platformComponentProto(request.ComponentKind)
	if err != nil {
		return nil, err
	}
	message := &commonv1.PlatformEnrollmentCompletionTranscript{
		ProtocolVersion: constants.PlatformEnrollmentProtocolVersion,
		RequestId:       request.ID,
		TokenHash:       request.TokenHash,
		ComponentKind:   componentKind,
		InstanceId:      request.InstanceID,
		Fingerprints:    payloadFingerprints(request.Fingerprints),
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("platform enrollment: marshal completion transcript: %w", err)
	}
	return encoded, nil
}

func verifyPlatformEnrollmentProofs(request *models.PlatformEnrollmentRequest, proofs models.PlatformEnrollmentProofs) error {
	transcript, err := platformEnrollmentCompletionTranscript(request)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(transcript)
	if request.App != nil {
		if err := verifyPlatformEnrollmentProof(request.App.CSRPEM, proofs.App, digest[:]); err != nil {
			return err
		}
		key, err := request.App.SigningKey()
		if err != nil || key == nil {
			return err
		}
		if proofs.SigningKey == "" {
			return constants.ErrPlatformEnrollmentProofRequired
		}
		signature, err := base64.RawURLEncoding.DecodeString(proofs.SigningKey)
		if err != nil || !ed25519.Verify(key, digest[:], signature) {
			return constants.ErrPlatformEnrollmentProofInvalid
		}
		return nil
	}
	if proofs.Operator == "" || proofs.CLI == "" {
		return constants.ErrPlatformEnrollmentProofRequired
	}
	if err := verifyPlatformEnrollmentProof(request.Operator.OperatorCSRPEM, proofs.Operator, digest[:]); err != nil {
		return err
	}
	return verifyPlatformEnrollmentProof(request.Operator.CLICSRPEM, proofs.CLI, digest[:])
}

func verifyPlatformEnrollmentProof(csrPEM, encodedSignature string, digest []byte) error {
	if encodedSignature == "" {
		return constants.ErrPlatformEnrollmentProofRequired
	}
	_, publicKey, err := parsePlatformEnrollmentCSR(csrPEM)
	if err != nil {
		return err
	}
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if err != nil || !ecdsa.VerifyASN1(publicKey, digest, signature) {
		return constants.ErrPlatformEnrollmentProofInvalid
	}
	return nil
}

func platformComponentProto(kind models.PlatformComponentKind) (commonv1.PlatformComponentKind, error) {
	switch kind {
	case models.PlatformComponentDashboard:
		return commonv1.PlatformComponentKind_PLATFORM_COMPONENT_KIND_DASHBOARD, nil
	case models.PlatformComponentOperator:
		return commonv1.PlatformComponentKind_PLATFORM_COMPONENT_KIND_OPERATOR, nil
	case models.PlatformComponentApplication:
		return commonv1.PlatformComponentKind_PLATFORM_COMPONENT_KIND_APPLICATION, nil
	default:
		return commonv1.PlatformComponentKind_PLATFORM_COMPONENT_KIND_UNSPECIFIED, constants.ErrPlatformEnrollmentInvalidComponent
	}
}

func validPlatformInstanceID(value string) bool {
	matched, err := regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9._-]*$`, value)
	return err == nil && matched
}

func validPlatformHostname(value string) bool {
	matched, err := regexp.MatchString(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`, value)
	return err == nil && matched
}
