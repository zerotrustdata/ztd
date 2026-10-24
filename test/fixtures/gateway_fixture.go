// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration || e2e

package fixtures

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/governance/consensustest"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/uuid"
)

// GatewayFixture provides a reusable gateway setup for integration tests.
// It encapsulates the common pattern of creating a gateway, downstream server,
// and all required dependencies.
type GatewayFixture struct {
	FileService              fs.RuntimeFileService
	Config                   *config.Config
	Service                  *gateway.GatewayModeService
	DownstreamServer         *httptest.Server
	DataDir                  string
	SecretsDir               string
	PKIDir                   string
	MCPGateway               *mcp.GatewayService
	ActuatorPriv             ed25519.PrivateKey
	ActuatorKeyID            string
	DownstreamURL            string // URL of the downstream MCP/A2A server
	DownstreamCmd            string // Command of downstream subprocess MCP server
	DownstreamArgs           []string
	Keystore                 *keystore.Keystore
	A2ADownstreamURL         string           // URL used for A2A (same as DownstreamURL if not overridden)
	ConsensusServer          *httptest.Server // External HTTPS protobuf deliberation endpoint, when configured
	PublicMirrorOrigin       string           // Anonymous public mirror origin, when enabled
	PublicMirrorBootstrapURL string           // Anonymous public mirror bootstrap endpoint, when enabled
}

// GatewayFixtureOptions configures the gateway fixture.
type GatewayFixtureOptions struct {
	TestName         string
	Posture          constants.Posture
	DownstreamURL    string   // MCP downstream HTTP URL; creates mock server if empty and DownstreamCmd is empty
	DownstreamCmd    string   // MCP downstream stdio subprocess command
	DownstreamArgs   []string // MCP downstream stdio subprocess arguments
	A2ADownstreamURL string   // A2A downstream; if empty, reuses MCP downstream server
	NoDownstream     bool     // If true, do not create a mock downstream server when DownstreamURL/Cmd are empty
	PublicBaseURL    string   // Public base URL for approval links; defaults to localhost:HTTPS_port

	// Consensus configuration for consensus/notary postures. When zero-valued,
	// defaults to a single-member consensus (nMembers=1, quorum=1, nServiceMembers=1)
	// with consensusID "test-consensus".
	ConsensusID              string
	ConsensusNMembers        int
	ConsensusQuorum          int
	ConsensusNServiceMembers int

	// ProductionConsensus uses HTTPS deliberation and gives enrolled Operators
	// the same complete bound consensus policy that signs their commands.
	ProductionConsensus bool
	// EnablePublicSpectator starts the real Gateway-owned public mirror on
	// isolated loopback listeners.
	EnablePublicSpectator bool

	// OperatorLeafCertTTL, when nonzero, shortens the lifetime of issued
	// Operator leaves so a test observes renewal.
	OperatorLeafCertTTL time.Duration
}

// repoTestResultsDir returns <repo>/test-results, computed from this source
// file's location so it is independent of the test's working directory. This
// file lives at <repo>/test/fixtures/gateway_fixture.go.
func repoTestResultsDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(repoRoot, constants.TestResultsDirname)
}

// NewGatewayFixture creates a fully configured gateway fixture for testing.
// It handles:
// - Path initialization and test data directory creation
// - Mock downstream MCP server (if no URL provided)
// - Gateway configuration and service initialization
// - Execution and file edit services
// - Governance dependencies and actuator key setup
// - MCP gateway dependency wiring
//
// Cleanup is registered internally via t.Cleanup() — callers do not need to
// defer anything.
func NewGatewayFixture(t *testing.T, opts GatewayFixtureOptions) *GatewayFixture {
	t.Helper()

	// Create test paths without mutating global constants.Paths. The Gateway is
	// rooted in a throwaway Operator directory, not testutil.TempDir(t): its
	// embedded Operator executes and edits files under this root, and .ztd/tmp
	// is protected runtime state that L1 Doctrine rejects edits to. Only the
	// gateway data/vault is relocated to a persistent results directory below.
	testPaths := testutil.NewTestPaths(testutil.OperatorDir(t))

	// Integration runs leave their artifacts behind for inspection: each run
	// writes a fresh, uniquely-named directory under <repo>/test-results/ and
	// nothing is deleted between or within runs. os.MkdirTemp gives a unique
	// suffix so concurrent fixtures in the same test/second never collide, and
	// (unlike t.TempDir) the directory is NOT auto-removed when the test ends.
	resultsRoot := repoTestResultsDir()
	if err := os.MkdirAll(resultsRoot, 0755); err != nil {
		t.Fatalf("gateway_fixture: create test-results root: %v", err)
	}
	dataDir, err := os.MkdirTemp(resultsRoot, fmt.Sprintf("%s-%s-", time.Now().Format("20060102-150405"), opts.TestName))
	if err != nil {
		t.Fatalf("gateway_fixture: create test run directory: %v", err)
	}
	t.Logf("Test vault created at: %s", dataDir)

	secretsDir := testPaths.SecretsDir
	pkiDir := testPaths.PKIDir

	var downstreamServer *httptest.Server
	var downstreamURL string

	if !opts.NoDownstream && opts.DownstreamCmd == "" {
		if opts.DownstreamURL == "" {
			// Create default mock downstream MCP server
			downstreamServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Method string `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				switch req.Method {
				case "tools/list":
					resp := mcp.JSONRPCResponse{
						JSONRPC: "2.0",
						ID:      1,
						Result:  mustMarshal(t, mcp.ToolsListResult{Tools: []mcp.Tool{{Name: "echo", Description: "echoes input"}}}),
					}
					if err := json.NewEncoder(w).Encode(resp); err != nil {
						t.Logf("Failed to encode response: %v", err)
					}
				case "tools/call":
					resp := mcp.JSONRPCResponse{
						JSONRPC: "2.0",
						ID:      1,
						Result:  mustMarshal(t, mcp.CallToolResult{Content: []mcp.TextContent{{Type: "text", Text: "mcp says hello"}}}),
					}
					if err := json.NewEncoder(w).Encode(resp); err != nil {
						t.Logf("Failed to encode response: %v", err)
					}
				case "resources/list":
					resp := mcp.JSONRPCResponse{
						JSONRPC: "2.0",
						ID:      1,
						Result:  mustMarshal(t, mcp.ResourcesListResult{Resources: []mcp.Resource{{URI: "file:///test.txt", Name: "test.txt"}}}),
					}
					if err := json.NewEncoder(w).Encode(resp); err != nil {
						t.Logf("Failed to encode response: %v", err)
					}
				case "prompts/list":
					resp := mcp.JSONRPCResponse{
						JSONRPC: "2.0",
						ID:      1,
						Result:  mustMarshal(t, mcp.PromptsListResult{Prompts: []mcp.Prompt{{Name: "test-prompt", Description: "A test prompt"}}}),
					}
					if err := json.NewEncoder(w).Encode(resp); err != nil {
						t.Logf("Failed to encode response: %v", err)
					}
				}
			}))
			downstreamURL = downstreamServer.URL
		} else {
			downstreamURL = opts.DownstreamURL
		}
	}

	// Setup gateway configuration
	posture := opts.Posture
	if posture == "" {
		posture = constants.PostureNotary
	}
	// WorkDir roots the Gateway (and its embedded Operator's working directory)
	// in this test's runtime base, exactly as `gw start` roots it in the project.
	cfg, err := config.LoadGateway(config.GatewayOptions{
		WorkDir:       testPaths.BaseDir,
		DataDir:       dataDir,
		PKIDir:        pkiDir,
		SecretsDir:    secretsDir,
		VaultDir:      testPaths.VaultDir,
		PasskeyRpID:   "localhost",
		PasskeyRpName: "ztd",
		// Both listeners bind port 0, so the kernel assigns each port atomically
		// at listen time. Probing for a free port first races the other test
		// binaries that go test runs concurrently.
		AllowTestPortZero: true,
		Posture:           posture,
	})
	require.NoError(t, err)
	// Integration fixtures serve only local test clients. Keep both Gateway
	// listeners on loopback so Windows test binaries never request firewall access.
	cfg.Gateway.ListenHost = "127.0.0.1"
	cfg.Gateway.MCPDownstreamURL = downstreamURL
	cfg.Gateway.MCPDownstreamCmd = opts.DownstreamCmd
	cfg.Gateway.MCPDownstreamArgs = opts.DownstreamArgs
	if opts.EnablePublicSpectator {
		// Bind port 0 on both mirror listeners so the kernel assigns each port
		// atomically at listen time, matching the Gateway HTTP/HTTPS listeners.
		cfg.Gateway.PublicSpectatorEnabled = true
		cfg.Gateway.PublicSpectatorPrivateAddr = "127.0.0.1:0"
		cfg.Gateway.PublicSpectatorPublicAddr = "127.0.0.1:0"
		// The public handler serves both the mirror API and explorer assets;
		// pointing the explorer at that address suppresses a second listener.
		cfg.Gateway.EvalExplorerAddr = "127.0.0.1:0"
	}

	if opts.PublicBaseURL != "" {
		cfg.Gateway.PublicBaseURL = opts.PublicBaseURL
	}
	if opts.OperatorLeafCertTTL > 0 {
		cfg.Gateway.OperatorLeafCertTTL = opts.OperatorLeafCertTTL
	}

	a2aURL := opts.A2ADownstreamURL
	if a2aURL == "" {
		a2aURL = downstreamURL
	}
	cfg.Gateway.A2ADownstreamURL = a2aURL

	fileSvc, err := fs.NewRuntimeFileService(testPaths.BaseDir, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))

	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	// Open DB and seed consensus policy and binding before constructing the
	// Gateway. The fixture injects its test deliberator for Gateway ingress;
	// direct conformance clients use the bound HTTPS protobuf endpoint.
	db, err := gateway.OpenCanonicalDBService(testutil.NewTestLogger(), ks, fileSvc)
	require.NoError(t, err)

	var l2Deliberator mcp.L2ConsensusDeliberator
	var consensusServer *httptest.Server
	if posture.RequiresL2() {
		consensusID := opts.ConsensusID
		if consensusID == "" {
			consensusID = "test-consensus"
		}
		nMembers := opts.ConsensusNMembers
		if nMembers == 0 {
			nMembers = 1
		}
		quorum := opts.ConsensusQuorum
		if quorum == 0 {
			quorum = 1
		}
		nServiceMembers := opts.ConsensusNServiceMembers
		if nServiceMembers == 0 {
			nServiceMembers = 1
		}
		cs := consensustest.SetupConsensus(t, db.GetConsensusStore(), db.GetSignerStore(), consensusID, nMembers, quorum, nServiceMembers)
		l2Deliberator = cs.Deliberator
		if opts.ProductionConsensus {
			policy, err := db.GetConsensusStore().GetConsensus(t.Context(), consensusID)
			require.NoError(t, err)
			require.NotNil(t, policy)
			policy.ID = string(constants.DocIDConsensusBinding)
			policy.EndpointURL = cs.Server.URL
			require.NoError(t, db.GetConsensusStore().ClearConsensusBinding(t.Context(), ""))
			require.NoError(t, db.GetConsensusStore().AddConsensus(t.Context(), *policy))
			cs.Deliberator.ConsensusID = policy.ID
			l2Deliberator = db.GetConsensusStore().NewConsensusAppDeliberator(cs.Server.Client())
		}
		consensusServer = cs.Server
	}

	ls, err := gateway.NewGatewayModeServiceWithDB(cfg, fileSvc, testutil.NewTestLogger(), db, l2Deliberator)
	require.NoError(t, err)

	// Obtain the actuator signing key and MCP gateway from the constructed
	// service.
	sm, err := ls.GetSecretManager()
	require.NoError(t, err)
	ActuatorPriv, ActuatorKeyID, err := sm.GetActuatorKey()
	require.NoError(t, err)

	// Add Actuator key to SignerStore so Implicit L2 signatures from the gateway are trusted in fixture tests
	ActuatorPub := ActuatorPriv.Public().(ed25519.PublicKey)
	err = ls.GetSignerStore().AddTrustedSigner(t.Context(), models.TrustedSigner{
		ID:        ActuatorKeyID,
		PublicKey: hex.EncodeToString(ActuatorPub),
		AddedAt:   time.Now().UTC(),
		Enabled:   true,
	})
	require.NoError(t, err)

	mcpGateway := ls.GetMCPGateway()
	require.NotNil(t, mcpGateway)

	// Seed platform_settings required for health check
	err = ls.GetDocStore().DocSet(t.Context(), string(constants.CollectionSettings), "platform_settings", json.RawMessage(`{}`))
	require.NoError(t, err)

	// Start the gateway service. The Start error is delivered on a buffered
	// channel and drained once so the goroutine never logs after the test has
	// completed (which would panic the test runtime under -race).
	ctx, cancel := context.WithCancel(context.Background())
	serverErr := make(chan error, 1)
	var startOnce sync.Once
	var startErr error
	waitStart := func() error {
		startOnce.Do(func() {
			startErr = <-serverErr
		})
		return startErr
	}
	go func() {
		serverErr <- ls.Start(ctx)
	}()

	// Register cleanup via t.Cleanup() — fires when the test completes
	t.Cleanup(func() {
		cancel()
		// Join the Start goroutine before the test ends. A graceful shutdown
		// surfaces http.ErrServerClosed, which is expected and not an error.
		if err := waitStart(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Logf("gateway start error: %v", err)
		}
		if downstreamServer != nil {
			downstreamServer.Close()
		}
		// Stop the gateway service to close all databases and release file locks.
		// The data directory itself is intentionally left on disk: integration
		// runs accumulate results under <repo>/test-results/ for later inspection.
		if err := ls.Stop(context.Background()); err != nil {
			t.Logf("gateway stop error: %v", err)
		}
	})

	select {
	case <-ls.Ready():
	case err := <-serverErr:
		startOnce.Do(func() { startErr = err })
		t.Fatalf("gateway service failed to start: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("gateway service did not become ready")
	}

	mirrorOrigin, mirrorBootstrapURL := publicMirrorOrigin(cfg), ""
	if mirrorOrigin != "" {
		mirrorBootstrapURL = mirrorOrigin + "/bootstrap"
	}
	return &GatewayFixture{
		FileService:              fileSvc,
		Config:                   cfg,
		Service:                  ls,
		DownstreamServer:         downstreamServer,
		DataDir:                  dataDir,
		SecretsDir:               secretsDir,
		PKIDir:                   pkiDir,
		MCPGateway:               mcpGateway,
		ActuatorPriv:             ActuatorPriv,
		ActuatorKeyID:            ActuatorKeyID,
		DownstreamURL:            downstreamURL,
		DownstreamCmd:            opts.DownstreamCmd,
		DownstreamArgs:           opts.DownstreamArgs,
		Keystore:                 ks,
		A2ADownstreamURL:         a2aURL,
		ConsensusServer:          consensusServer,
		PublicMirrorOrigin:       mirrorOrigin,
		PublicMirrorBootstrapURL: mirrorBootstrapURL,
	}
}

// publicMirrorOrigin returns the Gateway's anonymous mirror origin, or "" when
// the public spectator is disabled.
func publicMirrorOrigin(cfg *config.Config) string {
	if cfg == nil || !cfg.Gateway.PublicSpectatorEnabled || cfg.Gateway.PublicSpectatorPublicAddr == "" {
		return ""
	}
	return "http://" + cfg.Gateway.PublicSpectatorPublicAddr
}

// WaitForReady waits for the Gateway's listener signal, then verifies the
// public health endpoint once. No timer-driven health polling is needed.
func (f *GatewayFixture) WaitForReady(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	select {
	case <-f.Service.Ready():
	case <-ctx.Done():
		t.Fatal("Gateway listeners did not become ready: ", ctx.Err())
	}
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		network.LocalhostHTTPURL(f.Service.GetHTTPPort())+constants.APIPaths.Health, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "Gateway health after listener readiness")
}

func mustMarshal(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal fixture value: %v", err)
	}
	return b
}

// ClientIdentity represents an enrolled client with certificates.
type ClientIdentity struct {
	UserID            string
	OrganizationID    string
	PrivateKey        []byte
	Certificate       []byte
	CLIPrivateKey     []byte
	CLICertificate    string
	CLISessionID      string
	OperatorID        string
	OperatorSessionID string
	Passkey           *testutil.SoftwarePasskey
}

// EnrollClientIdentity performs CSR enrollment for a test client.
// It generates CSRs, creates a temporary certificate for enrollment,
// calls the enrollment endpoint, and returns the enrolled identity.
func EnrollClientIdentity(t *testing.T, f *GatewayFixture, userID, organizationID, systemFingerprint, hostname string) *ClientIdentity {
	t.Helper()

	// The user holds a real software passkey so L3 approvals verify end to end.
	passkey := testutil.NewSoftwarePasskey(t)
	webAuthnUserID, err := uuid.NewString()
	require.NoError(t, err)
	user := models.User{
		ID:                 userID,
		WebAuthnUserID:     webAuthnUserID,
		Status:             constants.UserStatusActive,
		PasskeyCredentials: []models.PasskeyCredential{passkey.Credential(t)},
	}
	userBytes, err := json.Marshal(user)
	require.NoError(t, err)
	err = f.Service.GetDocStore().DocSet(t.Context(), string(constants.CollectionUsers), userID, userBytes)
	require.NoError(t, err)

	// Generate CSR for client certificate using P-256 (required by PKI curve enforcement)
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrTmpl := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   userID,
			Organization: []string{organizationID},
		},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTmpl, priv)
	require.NoError(t, err)
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	// Generate CLI CSR for distinct SPIFFE identity (required by PKI cleanup Phase 3)
	cliPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cliCSRTmpl := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   userID + "-cli",
			Organization: []string{organizationID},
		},
	}
	cliCSRDER, err := x509.CreateCertificateRequest(rand.Reader, cliCSRTmpl, cliPriv)
	require.NoError(t, err)
	cliCSRPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: cliCSRDER})

	// Create a temporary client cert for initial enrollment (using Operator CA)
	operatorCAPEM := testutil.ReadOperatorCA(t, f.PKIDir)
	operatorBlock, _ := pem.Decode(operatorCAPEM)
	operatorCert, err := x509.ParseCertificate(operatorBlock.Bytes)
	require.NoError(t, err)
	sm, err := f.Service.GetSecretManager()
	require.NoError(t, err)
	operatorKeyDER, err := sm.GetCAPrivateKey("operator")
	require.NoError(t, err)
	operatorKey, err := x509.ParseECPrivateKey(operatorKeyDER)
	require.NoError(t, err)

	spiffeURI, err := url.Parse(fmt.Sprintf("spiffe://ztd.local/cli/%s/cli-session-123", userID))
	require.NoError(t, err)
	tempCertTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               csrTmpl.Subject,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{spiffeURI},
	}
	tempCertDER, err := x509.CreateCertificate(rand.Reader, tempCertTemplate, operatorCert, priv.Public(), operatorKey)
	require.NoError(t, err)
	tempCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tempCertDER})

	// Create mTLS client with temporary cert
	privBytes, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	tempCert, err := tls.X509KeyPair(tempCertPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes}))
	require.NoError(t, err)

	rootPEM := testutil.ReadRootCA(t, f.PKIDir)
	operatorPEM := testutil.ReadOperatorCA(t, f.PKIDir)
	rootPool := x509.NewCertPool()
	require.True(t, rootPool.AppendCertsFromPEM(rootPEM), "failed to parse root CA PEM")
	require.True(t, rootPool.AppendCertsFromPEM(operatorPEM), "failed to parse operator CA PEM")

	enrollClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      rootPool,
				Certificates: []tls.Certificate{tempCert},
			},
		},
	}

	// Enroll via CSR endpoint
	mtlsURL := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())
	regReq := models.OperatorRegistrationRequest{
		CSR:               string(csrPEM),
		CLICSR:            string(cliCSRPEM),
		SystemFingerprint: systemFingerprint,
		Hostname:          hostname,
	}
	regBody, _ := json.Marshal(regReq)
	hReq, _ := http.NewRequest(http.MethodPost, mtlsURL+constants.APIPaths.PKIDevicesEnroll, bytes.NewReader(regBody))
	hResp, err := enrollClient.Do(hReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, hResp.StatusCode)
	var regResp models.OperatorRegistrationResponse
	if err := json.NewDecoder(hResp.Body).Decode(&regResp); err != nil {
		t.Fatalf("gateway_fixture: decode registration response: %v", err)
	}
	hResp.Body.Close()

	// Enrollment persists the identity synchronously. A remote Operator stays
	// offline until it subscribes to its command channel; this fixture enrolls
	// client certificates without starting an outbound Operator.
	op, err := f.Service.GetDocStore().DocGet(t.Context(), string(constants.CollectionOperators), regResp.OperatorID)
	require.NoError(t, err)
	require.NotNil(t, op)
	opDoc, err := models.OperatorDocumentFromStore(op)
	require.NoError(t, err)
	require.Equal(t, regResp.OperatorSessionID, opDoc.GetOperatorSessionId())
	require.Equal(t, string(constants.OperatorStatusOffline), opDoc.GetStatus())
	session, err := f.Service.GetDocStore().DocGet(t.Context(), string(constants.CollectionOperatorSessions), regResp.OperatorSessionID)
	require.NoError(t, err)
	require.NotNil(t, session, "Operator session must be persisted before enrollment returns")

	cliPrivBytes, err := x509.MarshalECPrivateKey(cliPriv)
	require.NoError(t, err)

	return &ClientIdentity{
		UserID:            userID,
		OrganizationID:    organizationID,
		PrivateKey:        privBytes,
		Certificate:       []byte(regResp.OperatorCert),
		CLIPrivateKey:     cliPrivBytes,
		CLICertificate:    regResp.CLICert,
		CLISessionID:      regResp.CLISessionID,
		OperatorID:        regResp.OperatorID,
		OperatorSessionID: regResp.OperatorSessionID,
		Passkey:           passkey,
	}
}

// CreateNoCertClient creates an HTTP client that trusts the gateway's CA bundle
// but does not present a client certificate. This is useful for testing endpoints
// that should reject unauthenticated connections (e.g., API key rejection).
func CreateNoCertClient(t *testing.T, f *GatewayFixture) *http.Client {
	t.Helper()

	rootPEM := testutil.ReadRootCA(t, f.PKIDir)
	operatorPEM := testutil.ReadOperatorCA(t, f.PKIDir)
	rootPool := x509.NewCertPool()
	require.True(t, rootPool.AppendCertsFromPEM(rootPEM), "failed to parse root CA PEM")
	require.True(t, rootPool.AppendCertsFromPEM(operatorPEM), "failed to parse operator CA PEM")

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: rootPool,
			},
		},
	}
}

// CreateMTLSClient creates an HTTP client configured for mTLS using the enrolled identity.
func CreateMTLSClient(t *testing.T, f *GatewayFixture, identity *ClientIdentity) *http.Client {
	t.Helper()

	rootPEM := testutil.ReadRootCA(t, f.PKIDir)
	operatorPEM := testutil.ReadOperatorCA(t, f.PKIDir)
	rootPool := x509.NewCertPool()
	require.True(t, rootPool.AppendCertsFromPEM(rootPEM), "failed to parse root CA PEM")
	require.True(t, rootPool.AppendCertsFromPEM(operatorPEM), "failed to parse operator CA PEM")

	enrolledCert, err := tls.X509KeyPair(identity.Certificate, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: identity.PrivateKey}))
	require.NoError(t, err)

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      rootPool,
				Certificates: []tls.Certificate{enrolledCert},
			},
		},
	}
}

// CreateCLIMTLSClient creates an HTTP client configured for mTLS using the
// enrolled CLI identity. The client presents the CLI certificate (which carries
// the CLI SPIFFE URI SAN) and sets the X-ZTD-CLI-Session-ID header on every
// outbound request via a wrapping RoundTripper.
func CreateCLIMTLSClient(t *testing.T, f *GatewayFixture, identity *ClientIdentity) *http.Client {
	t.Helper()

	rootPEM := testutil.ReadRootCA(t, f.PKIDir)
	operatorPEM := testutil.ReadOperatorCA(t, f.PKIDir)
	rootPool := x509.NewCertPool()
	require.True(t, rootPool.AppendCertsFromPEM(rootPEM), "failed to parse root CA PEM")
	require.True(t, rootPool.AppendCertsFromPEM(operatorPEM), "failed to parse operator CA PEM")

	cliCert, err := tls.X509KeyPair([]byte(identity.CLICertificate), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: identity.CLIPrivateKey}))
	require.NoError(t, err)

	base := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      rootPool,
				Certificates: []tls.Certificate{cliCert},
			},
		},
	}

	return &http.Client{
		Transport: &cliSessionRoundTripper{
			base:      base.Transport,
			sessionID: identity.CLISessionID,
		},
	}
}

// cliSessionRoundTripper wraps an http.RoundTripper and injects the
// X-ZTD-CLI-Session-ID header on every outbound request.
type cliSessionRoundTripper struct {
	base      http.RoundTripper
	sessionID string
}

func (rt *cliSessionRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set(constants.HeaderCLISessionID, rt.sessionID)
	return rt.base.RoundTrip(clone)
}

// CloseIdleConnections forwards the HTTP client's cleanup to its base transport.
func (rt *cliSessionRoundTripper) CloseIdleConnections() {
	if closer, ok := rt.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
