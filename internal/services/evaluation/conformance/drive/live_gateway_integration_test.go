// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package drive

import (
	"crypto/tls"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	agentconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// liveGateway is one live Gateway with its bootstrapped CLI identity and a harness
// client that submits as that identity to the embedded Operator.
type liveGateway struct {
	f       *fixtures.GatewayFixture
	ident   *fixtures.ClientIdentity
	client  *client.Client
	persona client.Persona
	target  operator.Target
}

// bootLiveGateway boots a Gateway under opts and returns a client for it. The CLI
// certificate is both the Operator and the CLI identity, so every request carries the
// CLI session header. The MCP, A2A, dispatch and envelope routes answer 401 without it.
// A consensus fixture's server certificate joins the trust bundle so L2 deliberation
// is reachable.
func bootLiveGateway(t *testing.T, opts fixtures.GatewayFixtureOptions) *liveGateway {
	t.Helper()
	f := fixtures.NewGatewayFixture(t, opts)
	f.WaitForReady(t)
	ident := fixtures.BootstrapCLIIdentity(t, f, opts.TestName+"-host")

	cliCert, err := tls.X509KeyPair([]byte(ident.CLICertificate), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ident.CLIPrivateKey}))
	require.NoError(t, err)
	caBundle := append(append([]byte{}, testutil.ReadRootCA(t, f.PKIDir)...), testutil.ReadOperatorCA(t, f.PKIDir)...)
	if f.ConsensusServer != nil {
		caBundle = append(caBundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.ConsensusServer.Certificate().Raw})...)
	}
	caPath := filepath.Join(testutil.TempDir(t), "ca.pem")
	require.NoError(t, os.WriteFile(caPath, caBundle, constants.PermFilePrivate))

	url := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())
	identity := agentconfig.Auth{Certificate: &cliCert, CABundle: caPath}
	c, err := client.New(agentconfig.Config{
		MTLSBaseURL:   url,
		PublicBaseURL: url,
		Auth:          identity,
		CLIAuth:       identity,
	})
	require.NoError(t, err)

	return &liveGateway{
		f:       f,
		ident:   ident,
		client:  c,
		persona: client.Persona{ID: opts.TestName, UserID: ident.UserID, CLISessionID: ident.CLISessionID},
		target:  operator.Target{OperatorID: ident.OperatorID, OperatorSessionID: ident.OperatorSessionID},
	}
}

// unreachableClient returns a client for a loopback address with nothing listening.
// The port is taken and released before the client is built, so every call through it
// fails at the transport and never receives an HTTP answer. No server is started.
func unreachableClient(t *testing.T) *client.Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	base := "http://" + listener.Addr().String()
	require.NoError(t, listener.Close())
	c, err := client.New(agentconfig.Config{MTLSBaseURL: base, PublicBaseURL: base})
	require.NoError(t, err)
	return c
}
