// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/httpclient"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// BuildMTLSClient creates an *http.Client configured with CLI mTLS certificates
// and the gateway CA bundle. The timeout parameter controls the client-level
// timeout; pass 0 for context-controlled cancellation (e.g., SSE streaming).
func BuildMTLSClient(fileSvc fs.RuntimeFileService, cfg *config.Config, timeout time.Duration) (*http.Client, error) {
	cliCert, err := NewCLICertificateSource(fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	return buildGatewayTLSClient(fileSvc, cfg, timeout, cliCert.GetClientCertificate)
}

// BuildTrustedClient creates an *http.Client that verifies the Gateway against
// the local trust bundle and presents no client certificate. CLI reinstatement
// uses it: an expired CLI certificate cannot complete the mTLS handshake, so
// the request authenticates by proof of possession in its body instead.
func BuildTrustedClient(fileSvc fs.RuntimeFileService, cfg *config.Config, timeout time.Duration) (*http.Client, error) {
	return buildGatewayTLSClient(fileSvc, cfg, timeout, nil)
}

func buildGatewayTLSClient(fileSvc fs.RuntimeFileService, cfg *config.Config, timeout time.Duration, clientCert func(*tls.CertificateRequestInfo) (*tls.Certificate, error)) (*http.Client, error) {
	caBundleBytes, err := ReadTrustBundle(fileSvc, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrFailedToReadTrustBundle, err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caBundleBytes) {
		return nil, constants.ErrCAParseFailed
	}

	return &http.Client{
		Transport: httpclient.NewIPv4Transport(&tls.Config{
			GetClientCertificate: clientCert,
			RootCAs:              caPool,
			MinVersion:           tls.VersionTLS13,
			CurvePreferences:     certs.FIPSCurvePreferences(),
		}),
		Timeout: timeout,
	}, nil
}

// CLICertificateSource presents the CLI certificate currently on disk on every
// TLS handshake. A renewal committed by any ztd process (RenewCLIIdentityIfDue)
// reaches a long-running client on its next dial without rebuilding it. The
// key is unsealed again only when the certificate file changes. Renewal writes
// the key before the certificate, so a changed certificate always finds its
// key. If the changed identity does not load, the last loaded certificate is
// kept and the next handshake tries again. An expired certificate fails the
// handshake with constants.ErrCLICertificateExpired.
type CLICertificateSource struct {
	fileSvc fs.RuntimeFileService
	cfg     *config.Config

	mu      sync.Mutex
	certPEM []byte
	cert    tls.Certificate
}

// NewCLICertificateSource loads the current CLI certificate and returns a
// source that follows later renewals.
func NewCLICertificateSource(fileSvc fs.RuntimeFileService, cfg *config.Config) (*CLICertificateSource, error) {
	s := &CLICertificateSource{fileSvc: fileSvc, cfg: cfg}
	certPEM, err := s.readCertPEM()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrFailedToLoadClientCertificate, err)
	}
	cert, err := LoadCLICertificate(fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	s.certPEM, s.cert = certPEM, cert
	return s, nil
}

// GetClientCertificate implements tls.Config.GetClientCertificate.
func (s *CLICertificateSource) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if certPEM, err := s.readCertPEM(); err == nil && !bytes.Equal(certPEM, s.certPEM) {
		if cert, err := LoadCLICertificate(s.fileSvc, s.cfg); err == nil {
			s.certPEM, s.cert = certPEM, cert
		}
	}
	if err := checkCLICertificateUnexpired(s.cert.Leaf); err != nil {
		return nil, err
	}
	cert := s.cert
	return &cert, nil
}

func (s *CLICertificateSource) readCertPEM() ([]byte, error) {
	rel, err := s.fileSvc.RelFromAbs(s.cfg.CLICertFile())
	if err != nil {
		return nil, fmt.Errorf("auth: resolve CLI certificate path: %w", err)
	}
	certPEM, err := s.fileSvc.ReadFile(context.Background(), rel)
	if err != nil {
		return nil, fmt.Errorf("auth: read CLI certificate: %w", err)
	}
	return certPEM, nil
}
