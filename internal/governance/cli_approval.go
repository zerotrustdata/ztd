// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/url"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/protocol"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

const cliApprovalDomain = "ztd:l3:cli:v1|"

// SignCLIApproval signs a canonical SHA-256 transaction hash with an enrolled
// CLI certificate's P-256 private key. This produces a cryptographic proof;
// certificate trust and CLI session status must still be checked by the caller.
func SignCLIApproval(transactionHash string, cert *tls.Certificate) (*commonv1.L3Proof, error) {
	if cert == nil || len(cert.Certificate) == 0 || cert.PrivateKey == nil {
		return nil, constants.ErrCLIL3CertNil
	}
	if err := validateTransactionHash(transactionHash); err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse enrolled CLI leaf certificate: %w: %w", constants.ErrTxL3ProofInvalid, err)
	}
	if err := validateCLIApprovalCertificate(leaf, time.Now()); err != nil {
		return nil, err
	}
	signer, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("CLI approval private key must implement crypto.Signer: %w", constants.ErrCLIL3PublicKeyInvalid)
	}
	privatePublic, ok := signer.Public().(*ecdsa.PublicKey)
	if !ok || !isP256(privatePublic) {
		return nil, fmt.Errorf("CLI approval private key must be ECDSA P-256: %w", constants.ErrCLIL3PublicKeyInvalid)
	}
	certificatePublic, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || !isP256(certificatePublic) || !privatePublic.Equal(certificatePublic) {
		return nil, fmt.Errorf("CLI approval private key does not match an ECDSA P-256 certificate: %w", constants.ErrCLIL3PublicKeyInvalid)
	}
	digest := cliApprovalDigest(transactionHash)
	signature, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("sign CLI approval: %w", err)
	}
	certDER := cert.Certificate[0]
	fingerprint := sha256.Sum256(certDER)
	return &commonv1.L3Proof{
		CliSignature:        hex.EncodeToString(signature),
		MtlsCertFingerprint: hex.EncodeToString(fingerprint[:]),
		CliCertificateDer:   append([]byte(nil), certDER...),
	}, nil
}

// VerifyCLIApproval validates the cryptographic portion of an enrolled CLI
// approval and returns the session ID encoded by its canonical CLI SPIFFE SAN.
// It does not establish certificate trust, enrollment, or revocation status.
func VerifyCLIApproval(userID, transactionHash string, proof *commonv1.L3Proof) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("CLI approval user ID is required: %w", constants.ErrMissingRequiredField)
	}
	if err := validateTransactionHash(transactionHash); err != nil {
		return "", err
	}
	if proof == nil || proof.CliSignature == "" || proof.MtlsCertFingerprint == "" || len(proof.CliCertificateDer) == 0 ||
		proof.CredentialId != "" || proof.ClientDataJson != "" || proof.AuthenticatorData != "" || proof.Signature != "" || proof.PasskeyContext != nil {
		return "", constants.ErrTxL3ProofInvalid
	}
	cert, err := x509.ParseCertificate(proof.CliCertificateDer)
	if err != nil {
		return "", fmt.Errorf("parse CLI approval certificate: %w: %w", constants.ErrTxL3ProofInvalid, err)
	}
	if err := validateCLIApprovalCertificate(cert, time.Now()); err != nil {
		return "", err
	}
	identity, err := cliCertificateIdentity(cert)
	if err != nil {
		return "", err
	}
	if identity.userID != userID {
		return "", constants.ErrCLIL3NoUserIDInCert
	}
	fingerprint := sha256.Sum256(proof.CliCertificateDer)
	if proof.MtlsCertFingerprint != hex.EncodeToString(fingerprint[:]) {
		return "", constants.ErrTxL3ProofInvalid
	}
	sig, err := hex.DecodeString(proof.CliSignature)
	if err != nil {
		return "", fmt.Errorf("decode CLI approval signature: %w: %w", constants.ErrCLIL3SignatureEncodingFailed, err)
	}
	publicKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !isP256(publicKey) {
		return "", constants.ErrCLIL3PublicKeyInvalid
	}
	digest := cliApprovalDigest(transactionHash)
	if !ecdsa.VerifyASN1(publicKey, digest[:], sig) {
		return "", constants.ErrCLIL3SignatureVerificationFailed
	}
	return identity.sessionID, nil
}

// VerifyCLIApprovalWithTrust adds chain verification to the shared signature,
// certificate validity, fingerprint and canonical CLI SPIFFE identity checks.
// The supplied intermediates are hints; they never become trusted roots.
func VerifyCLIApprovalWithTrust(userID, transactionHash string, proof *commonv1.L3Proof, roots *x509.CertPool) (string, error) {
	if roots == nil {
		return "", constants.ErrEmptyTrustBundle
	}
	sessionID, err := VerifyCLIApproval(userID, transactionHash, proof)
	if err != nil {
		return "", err
	}
	leaf, err := x509.ParseCertificate(proof.CliCertificateDer)
	if err != nil {
		return "", fmt.Errorf("parse CLI leaf: %w: %w", constants.ErrTxL3ProofInvalid, err)
	}
	intermediates := x509.NewCertPool()
	for _, der := range proof.CliIntermediatesDer {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return "", fmt.Errorf("parse CLI intermediate: %w: %w", constants.ErrTxL3ProofInvalid, err)
		}
		intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return "", fmt.Errorf("verify CLI approval chain: %w: %w", constants.ErrTxL3ProofInvalid, err)
	}
	return sessionID, nil
}

func validateTransactionHash(transactionHash string) error {
	decoded, err := hex.DecodeString(transactionHash)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != transactionHash {
		return fmt.Errorf("transaction hash must be canonical lowercase SHA-256 hex: %w", constants.ErrCLIL3TransactionHashRequired)
	}
	return nil
}

func cliApprovalDigest(transactionHash string) [sha256.Size]byte {
	return sha256.Sum256([]byte(cliApprovalDomain + transactionHash))
}

func isP256(key *ecdsa.PublicKey) bool {
	if key == nil || key.Curve != elliptic.P256() {
		return false
	}
	_, err := key.Bytes()
	return err == nil
}

func validateCLIApprovalCertificate(cert *x509.Certificate, now time.Time) error {
	if cert == nil || now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return fmt.Errorf("CLI approval certificate is outside its validity period: %w", constants.ErrCLIL3SessionExpired)
	}
	if _, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || !isP256(cert.PublicKey.(*ecdsa.PublicKey)) {
		return fmt.Errorf("CLI approval certificate must use ECDSA P-256: %w", constants.ErrCLIL3PublicKeyInvalid)
	}
	_, err := cliCertificateIdentity(cert)
	return err
}

type cliApprovalIdentity struct {
	userID    string
	sessionID string
}

func cliCertificateIdentity(cert *x509.Certificate) (cliApprovalIdentity, error) {
	if cert == nil || len(cert.URIs) != 1 {
		return cliApprovalIdentity{}, constants.ErrCLIL3NoUserIDInCert
	}
	wid := protocol.NewWorkloadIdentity()
	uri := cert.URIs[0]
	if uri == nil || !validCanonicalCLIURI(uri) {
		return cliApprovalIdentity{}, constants.ErrCLIL3NoUserIDInCert
	}
	spiffeID := uri.String()
	userID, validUser := wid.ExtractUserID(spiffeID)
	sessionID, validSession := wid.ExtractCLISessionID(spiffeID)
	if !validUser || !validSession || wid.CLISPIFFEID(userID, sessionID) != spiffeID {
		return cliApprovalIdentity{}, constants.ErrCLIL3NoUserIDInCert
	}
	return cliApprovalIdentity{userID: userID, sessionID: sessionID}, nil
}

func validCanonicalCLIURI(uri *url.URL) bool {
	return uri.Scheme == "spiffe" && uri.Host == protocol.TrustDomain && uri.User == nil && uri.RawQuery == "" && uri.Fragment == "" && uri.Opaque == ""
}
