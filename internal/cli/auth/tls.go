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

// cliIdentityPaths names one CLI identity: its certificate and sealed key.
type cliIdentityPaths struct {
	cert string
	key  string
}

// heldCLICertificateSources holds the certificate source each CLI identity
// loaded in this process, keyed by cliIdentityPaths. A process unseals a CLI
// key once and keeps it in memory until it exits, the way a running Gateway
// keeps its unlocked master key, so a long-running command (an eval rollout
// that starts a run every few minutes for hours) does not need the master key
// store again after it started. A changed certificate on disk is a changed
// identity, and its key is unsealed again.
var heldCLICertificateSources sync.Map

// NewCLICertificateSource returns a source for the current CLI certificate
// that follows later renewals. When this process already loaded the identity
// that is on disk now, it returns that source without unsealing the key again.
func NewCLICertificateSource(fileSvc fs.RuntimeFileService, cfg *config.Config) (*CLICertificateSource, error) {
	s := &CLICertificateSource{fileSvc: fileSvc, cfg: cfg}
	certPEM, err := s.readCertPEM()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrFailedToLoadClientCertificate, err)
	}
	paths := cliIdentityPaths{cert: cfg.CLICertFile(), key: cfg.CLIKeyFile()}
	if held, ok := heldCLICertificateSources.Load(paths); ok {
		source := held.(*CLICertificateSource)
		if leaf, holds := source.holds(certPEM); holds {
			if err := checkCLICertificateUnexpired(leaf); err != nil {
				return nil, err
			}
			return source, nil
		}
	}
	cert, err := LoadCLICertificate(fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	s.certPEM, s.cert = certPEM, cert
	heldCLICertificateSources.Store(paths, s)
	return s, nil
}

// holds reports whether the source presents the certificate certPEM, and
// returns its leaf when it does.
func (s *CLICertificateSource) holds(certPEM []byte) (*x509.Certificate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cert.Leaf, bytes.Equal(s.certPEM, certPEM)
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
