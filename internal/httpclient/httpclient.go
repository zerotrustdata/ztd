// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package httpclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/zerotrustdata/ztd/internal/constants"
)

const (
	DefaultTimeout         = 30 * time.Second
	DefaultDialTimeout     = 10 * time.Second
	DefaultTLSTimeout      = 10 * time.Second
	DefaultIdleConnTimeout = 90 * time.Second
)

// IPv4DialContext is an http.Transport.DialContext function that resolves
// the host using IPv4-only lookup (no AAAA records) and dials over "tcp4".
//
// This forces `localhost` to resolve to 127.0.0.1 on Windows, where the OS
// resolver returns ::1 first and the IDE's port-forward only listens on
// IPv4 127.0.0.1. Even if an IPv6 address somehow slips through the
// lookup, the "tcp4" network prevents the kernel from creating an IPv6
// socket. Literal IPv6 addresses (e.g. "[::1]") fail with
// ErrNetworkIPv4Unavailable — IPv6 is excluded entirely, not merely deprioritized.
//
// Use this as the DialContext of any CLI HTTP transport that dials the
// gateway via `localhost`. The URL retains `localhost` (for TLS SAN
// verification against the gateway cert) while the dialer connects to
// 127.0.0.1 under the hood.
func IPv4DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ipv4 dial: split host/port %q: %w", addr, err)
	}
	d := &net.Dialer{Timeout: DefaultDialTimeout}
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return d.DialContext(ctx, "tcp4", net.JoinHostPort(ip.String(), port))
		}
		return nil, fmt.Errorf("ipv4 dial: literal IPv6 host %s: %w", host, constants.ErrNetworkIPv4Unavailable)
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, fmt.Errorf("ipv4 dial: resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("ipv4 dial: resolve %s: %w", host, constants.ErrNetworkIPv4Unavailable)
	}
	return d.DialContext(ctx, "tcp4", net.JoinHostPort(ips[0].String(), port))
}

// NewIPv4Transport returns an *http.Transport that dials IPv4 only (see
// IPv4DialContext) with the same timeout/conn-pool tuning as the base
// transport. The optional tlsCfg is set as TLSClientConfig. This is the
// opt-in IPv4 transport for CLI HTTP clients that reach the gateway via
// `localhost`; gateway-side and operator-side transports keep their own
// dialer unless they explicitly need IPv4 restriction.
func NewIPv4Transport(tlsCfg *tls.Config) *http.Transport {
	return &http.Transport{
		TLSClientConfig:     tlsCfg,
		DialContext:         IPv4DialContext,
		TLSHandshakeTimeout: DefaultTLSTimeout,
		IdleConnTimeout:     DefaultIdleConnTimeout,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
	}
}

// WithTimeout returns a copy of client with a different overall request timeout.
// Zero leaves cancellation to the request context. The transport, identity,
// redirect policy and cookie jar are retained; the source client is not mutated.
func WithTimeout(client *http.Client, timeout time.Duration) *http.Client {
	configured := *client
	configured.Timeout = timeout
	return &configured
}

// WithoutRedirects returns a copy of client that fails every redirect with
// refusal. Use it for a client whose destination is a bound identity, where a
// Location header would send the request to a host nobody bound or downgrade
// its TLS. The caller's client is not mutated.
func WithoutRedirects(client *http.Client, refusal error) *http.Client {
	pinned := *client
	pinned.CheckRedirect = func(*http.Request, []*http.Request) error { return refusal }
	return &pinned
}

// SPIFFEServerTLSConfig returns a TLS 1.3 client config that authenticates the
// server by its SPIFFE URI SAN rather than its DNS name. The peer chain must
// verify against roots for server auth and its leaf must carry exactly
// spiffeID; a chain that verifies but names another workload fails with
// refusal. Gateway-issued workload certificates carry no DNS SANs, so hostname
// verification can never authenticate them. roots is required: a nil pool
// would fall back to the system roots, which never issue workload identities.
func SPIFFEServerTLSConfig(roots *x509.CertPool, spiffeID string, refusal error) (*tls.Config, error) {
	if roots == nil {
		return nil, constants.ErrEmptyTrustBundle
	}
	if spiffeID == "" {
		return nil, fmt.Errorf("spiffe server identity: %w", constants.ErrMissingRequiredField)
	}
	return &tls.Config{
		// Hostname matching is skipped only because VerifyConnection performs
		// the chain and SPIFFE identity validation below.
		InsecureSkipVerify: true, // #nosec G402 -- VerifyConnection performs chain and identity validation.
		MinVersion:         tls.VersionTLS13,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return refusal
			}
			var intermediates *x509.CertPool
			if len(state.PeerCertificates) > 1 {
				intermediates = x509.NewCertPool()
				for _, cert := range state.PeerCertificates[1:] {
					intermediates.AddCert(cert)
				}
			}
			if _, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots: roots, Intermediates: intermediates,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}); err != nil {
				return fmt.Errorf("verify %s certificate chain: %w", spiffeID, err)
			}
			for _, uri := range state.PeerCertificates[0].URIs {
				if uri.String() == spiffeID {
					return nil
				}
			}
			return refusal
		},
	}, nil
}

func WebSocketDialerWithTLS(tlsCfg *tls.Config) *websocket.Dialer {
	return &websocket.Dialer{
		NetDialContext:   IPv4DialContext,
		TLSClientConfig:  tlsCfg,
		HandshakeTimeout: DefaultTLSTimeout,
	}
}
