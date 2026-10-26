// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package config

import (
	"cmp"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/netutil"
	"github.com/zerotrustdata/ztd/internal/paths"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keyring"
)

// LoadOptions contains all configuration values passed explicitly from main
type LoadOptions struct {
	OperatorCapabilities constants.OperatorCapabilities

	// Required
	OperatorEndpoint string
	HTTPPort         int // HTTP port to dial on Operator for bootstrap and trust bundle fetch (default: from paths.json)
	HTTPSPort        int // HTTPS port to dial on Operator for auth proxy (default: from paths.json)

	// Cloud Operator mode
	CloudMode     bool
	CloudProvider string

	Keyring keyring.Keyring // Master key store; nil selects the OS key store, tests inject a memory keyring

	// Working directory
	WorkDir string // Absolute path of the directory the Operator was launched from (--working-dir or os.Getwd())

	// PKI and Secrets directories
	PKIDir     string
	SecretsDir string

	// Logging
	LogLevel string // Log level passed to --log flag (info, debug, error)

	// System / process context - sourced from Settings at startup
	Shell string // SHELL value
	Lang  string // LANG value
	Term  string // TERM value
	TZ    string // TZ value

	// Monitoring
	HeartbeatInterval time.Duration // --heartbeat-interval: overrides the 30s default when non-zero

	// Inference backend (ztdllama); enabled by the inference capability.
	InferenceOllamaEndpoint      string
	InferenceLlamaCppEndpoint    string
	InferenceLlamaCppModelRoot   string
	InferenceKeepAlive           string
	InferenceAllowHostedDecision bool
	InferenceDecisionModels      []string
	InferenceDecisionOnly        bool

	// ProviderBoundaryObserverID identifies the observer capability.
	ProviderBoundaryObserverID string

	// ProvenanceOperatorID identifies the provenance capability.
	ProvenanceOperatorID string
	// ProvenanceOperatorModelStorageRoot is the root directory of the model
	// store the provenance capability attests (for example ~/.ollama/models
	// or a llama-server --models-dir).
	ProvenanceOperatorModelStorageRoot string
	// ProvenanceOperatorModelStorageFormat is the store layout beneath the
	// root: constants.ModelStorageFormatOllama (default) or
	// constants.ModelStorageFormatLlamaCpp.
	ProvenanceOperatorModelStorageFormat string
}

// GatewayConfig holds configuration for gateway mode.
// In gateway mode, the Node binary becomes the persistence and messaging
// backbone for the entire ztd platform, replacing external databases.
// No outbound authentication is required - the Operator simply starts and listens.
type GatewayConfig struct {
	Enabled bool
	// Governance enforcement posture, immutable for the Gateway's lifetime
	Posture           constants.Posture
	HTTPPort          int             // Plain HTTP port for bootstrap, PKI discovery and enrollment (default: constants.Ports.OperatorHttp)
	HTTPSPort         int             // HTTPS port for mTLS API (default: constants.Ports.OperatorHttps)
	ListenHost        string          // Bind host for HTTP and HTTPS (default: constants.LocalhostIP; 0.0.0.0 must be requested explicitly)
	DataDir           string          // Root directory for SQLite database (default: .ztd/data in working directory)
	PKIDir            string          // Directory for TLS certificates (default: .ztd/pki)
	SecretsDir        string          // Directory for platform secrets (default: .ztd/secrets)
	VaultDir          string          // Directory for encryption vault (default: .ztd/vault)
	Keyring           keyring.Keyring // Master key store; nil selects the OS key store, tests inject a memory keyring
	PasskeyRpID       string          // RP ID for passkey operations (default: localhost)
	PasskeyRpName     string          // RP Name for passkey operations (default: ztd)
	PasskeyRpOrigins  []string        // Additional RP origins for passkey operations (e.g. demo remapped ports)
	MCPDownstreamURL  string          // URL of the downstream MCP server to proxy discovery and execution to
	MCPDownstreamCmd  string          // Command of the downstream MCP subprocess (mutually exclusive with MCPDownstreamURL)
	MCPDownstreamArgs []string        // Arguments for the downstream MCP subprocess
	A2ADownstreamURL  string          // URL of the downstream A2A server to proxy execution to; empty leaves A2A ingress unmounted
	PublicBaseURL     string          // Public base URL for L3 approval links (e.g., https://localhost:8443)
	JWKSURL           string          // URL to fetch JWKS for JWT validation
	JWTRoleClaim      string          // The claim in JWT that contains roles (default: "roles")
	JWTIssuer         string          // Expected issuer claim in JWT (optional, for multi-audience IdP deployments)
	JWTAudience       string          // Expected audience claim in JWT (optional, for multi-audience IdP deployments)

	// HTTP server limits
	MaxPayloadBytes   int64         // Maximum request payload size in bytes (default: 10MB)
	ReadHeaderTimeout time.Duration // Timeout for reading request headers (default: 10s)
	ReadTimeout       time.Duration // Timeout for reading entire request (default: 30s)
	WriteTimeout      time.Duration // Timeout for writing response (default: 30s)
	IdleTimeout       time.Duration // Timeout for idle connections (default: 120s)
	MaxHeaderBytes    int           // Maximum size of request headers in bytes (default: 1MB)

	// Rate limiting
	RateLimitRPS   float64 // Requests per second limit (default: 5)
	RateLimitBurst int     // Burst size for rate limiter (default: 10)

	// Certificate mode
	CertMode            string // "full" for all hostnames/IPs, "localhost" for minimal
	NetworkIdentityFile string // Path to JSON file containing pre-detected network identity

	// OperatorLeafCertTTL is the lifetime of every Operator identity
	// certificate the Gateway issues, at enrollment and at each pub/sub
	// renewal (default: 7 days).
	OperatorLeafCertTTL time.Duration

	// CORS allowed origins for cross-origin browser access (e.g. https://lovable.dev)
	AllowedOrigins []string

	// DoctrineDir is the directory containing doctrine JSON files for L1 threat detection.
	// Empty means hardcoded MITRE patterns only.
	DoctrineDir string

	// PublicSpectatorEnabled starts the in-process public mirror and evaluation
	// explorer static host when the gateway boots.
	PublicSpectatorEnabled bool
	// PublicSpectatorPrivateAddr is the authenticated mirror ingest listener.
	PublicSpectatorPrivateAddr string
	// PublicSpectatorPublicAddr is the anonymous mirror read/SSE listener.
	PublicSpectatorPublicAddr string
	// PublicSpectatorAllowContainerBind permits the mirror and explorer listeners
	// to bind 0.0.0.0 inside a container network.
	PublicSpectatorAllowContainerBind bool
	// EvalExplorerAddr is the loopback listener for the evaluation explorer SPA.
	EvalExplorerAddr string
	// EvalExplorerRoot overrides the built explorer dist directory.
	EvalExplorerRoot string
	// PublicSpectatorTrustedProxyCIDRs identifies proxies allowed to provide
	// exactly one validated CF-Connecting-IP visitor address.
	PublicSpectatorTrustedProxyCIDRs []string

	// Distributed lock retry configuration
	LockMaxRetries int           // Maximum retry attempts for distributed lock acquisition (default: 30)
	LockRetryDelay time.Duration // Base delay for lock retry backoff (default: 50ms)
}

// A2AIngressEnabled reports whether the Gateway exposes A2A ingress. A2A is
// opt-in: an A2A call's only effect is the downstream dispatch, so without a
// downstream the ingress is not mounted.
func (c GatewayConfig) A2AIngressEnabled() bool {
	return c.A2ADownstreamURL != ""
}

// InferenceConfig holds configuration for the inference backend
// (ztdllama). The provider (Ollama or a llama.cpp llama-server router) owns
// process lifecycle, VRAM management, model eviction, and multi-model
// residency; the Inference Operator only acts as its HTTP client.
// The Inference Operator is a worker: it holds no model configuration. Each
// governed request names the model it wants, chosen by the user in the Console.
type InferenceConfig struct {
	DecisionModels      []string
	DecisionOnly        bool
	AllowHostedDecision bool
	Enabled             bool

	// Provider is the provider class the Inference Operator calls:
	// constants.InferenceProviderOllama or constants.InferenceProviderLlamaCpp.
	Provider string

	// OllamaEndpoint is the HTTP endpoint of the configured Ollama provider
	// (default: http://127.0.0.1:<InferenceOllamaDefaultPort>). For this
	// release the provider is remote; nothing here manages a local daemon.
	OllamaEndpoint string

	// LlamaCppEndpoint is the HTTP endpoint of a llama-server running in
	// router mode. Setting it selects the llama.cpp provider.
	LlamaCppEndpoint string
	// LlamaCppModelRoot is the llama-server --models-dir, read to compute
	// the served-model digest from the GGUF files it serves.
	LlamaCppModelRoot string

	// KeepAlive is the provider keep-alive passed per request. Default "-1"
	// keeps each requested model resident on the provider. A llama.cpp
	// router accepts only "-1" (resident) or "0" (unload after the request).
	KeepAlive string
}

// Endpoint returns the HTTP endpoint of the selected provider, or "" when no
// provider is selected.
func (c InferenceConfig) Endpoint() string {
	switch c.Provider {
	case constants.InferenceProviderOllama:
		return c.OllamaEndpoint
	case constants.InferenceProviderLlamaCpp:
		return c.LlamaCppEndpoint
	default:
		return ""
	}
}

// ProviderBoundaryObserverConfig holds configuration for the remote
// provider-boundary hardware observer operator.
type ProviderBoundaryObserverConfig struct {
	Enabled    bool
	ObserverID string
}

// ProvenanceOperatorConfig holds configuration for the storage-side model
// provenance attestor operator.
type ProvenanceOperatorConfig struct {
	Enabled          bool
	OperatorID       string
	ModelStorageRoot string
	// ModelStorageFormat is the layout of the store beneath ModelStorageRoot.
	ModelStorageFormat string
}

// Config holds all configuration for ztdo
type Config struct {
	OperatorCapabilities constants.OperatorCapabilities

	// Basic configuration
	ProjectID     string
	ComponentName constants.ComponentName
	Version       string

	// Operator identification
	OperatorID        string
	OperatorSessionId string // Operator's unique Operator session ID for authorization
	SystemFingerprint string // Unique system fingerprint for Operator tracking

	// Cloud Operator mode
	CloudMode     bool   // True if running as cloud Operator (--cloud flag)
	CloudProvider string // Cloud provider: 'aws', 'gcp', 'azure' (empty unless --cloud is set)

	// Endpoint is the client host or IP used for all HTTP and WebSocket connections.
	Endpoint string

	// TLSServerName overrides the hostname used for TLS certificate verification.
	// Set automatically when Endpoint is a raw IP address so the embedded CA cert
	// (which carries a hostname SAN, not an IP SAN) still validates correctly.
	TLSServerName string

	// Operator connection ports (operator dials these on the remote host)
	PubSubURL string // WebSocket base URL for Operator pub/sub (e.g., wss://192.168.1.10:443) - no path; client appends /ws/pubsub
	HTTPPort  int    // HTTP port for bootstrap and trust bundle fetch (default: from paths.json)
	HTTPSPort int    // HTTPS port for auth/bootstrap requests via Operator proxy (default: from paths.json)

	// Logging
	LogLevel string // Active log level (info, debug, error)

	// Execution configuration
	MaxConcurrentTasks int
	MaxMemoryMB        int

	// Monitoring configuration
	HeartbeatInterval time.Duration

	// WorkDir is the absolute directory command execution and file operations
	// are anchored to: the launch directory for an outbound Operator, and
	// paths.EmbeddedOperatorWorkDir for the Gateway's embedded Operator.
	WorkDir string

	// PKI and Secrets directories
	PKIDir     string
	SecretsDir string

	// Vault configuration for encryption at rest
	VaultDir string // Directory for encryption vault (default: .ztd/vault)
	// Keyring holds the master key. Nil selects the OS key store; tests inject
	// an isolated in-memory keyring.
	Keyring keyring.Keyring

	// System / process context - injected from Settings at startup, never read again
	Shell string // SHELL env var value (e.g. /bin/bash)
	Lang  string // LANG env var value
	Term  string // TERM env var value
	TZ    string // TZ env var value (IANA timezone name)

	// Posture of the Gateway this Operator enrolled with. Informational only:
	// the Operator enforces the posture each GovernanceEnvelope carries.
	Posture constants.Posture

	// Gateway mode configuration
	Gateway GatewayConfig

	// Inference configuration for the inference backend (ztdllama).
	// Disabled by default; enabled when the operator runs as an Inference
	// Node calling the configured remote Ollama provider.
	Inference InferenceConfig

	// ProviderBoundaryObserver configuration for the remote read-only
	// hardware observer on the approved provider host.
	ProviderBoundaryObserver ProviderBoundaryObserverConfig

	// ProvenanceOperator configuration for the storage-side model provenance
	// attestor at the model file site.
	ProvenanceOperator ProvenanceOperatorConfig
}

// FindProjectRoot returns the current working directory.
func FindProjectRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// GatewayOptions contains configuration values for LoadGateway.
type GatewayOptions struct {
	WorkDir                              string
	InferenceOllamaEndpoint              string
	InferenceLlamaCppEndpoint            string
	InferenceLlamaCppModelRoot           string
	InferenceKeepAlive                   string
	InferenceAllowHostedDecision         bool
	InferenceDecisionModels              []string
	InferenceDecisionOnly                bool
	ProviderBoundaryObserverID           string
	ProvenanceOperatorID                 string
	ProvenanceOperatorModelStorageRoot   string
	ProvenanceOperatorModelStorageFormat string

	OperatorCapabilities constants.OperatorCapabilities

	Posture           constants.Posture
	HTTPPort          int
	HTTPSPort         int
	ListenHost        string
	DataDir           string
	PKIDir            string
	SecretsDir        string
	VaultDir          string
	Keyring           keyring.Keyring
	PasskeyRpID       string
	PasskeyRpName     string
	PasskeyRpOrigins  []string
	MCPDownstreamURL  string
	MCPDownstreamCmd  string
	MCPDownstreamArgs []string
	A2ADownstreamURL  string
	PublicBaseURL     string
	JWKSURL           string
	JWTRoleClaim      string
	JWTIssuer         string
	JWTAudience       string

	RateLimitRPS   float64
	RateLimitBurst int

	CertMode            string
	NetworkIdentityFile string

	AllowedOrigins []string

	DoctrineDir string

	// OperatorLeafCertTTL is the lifetime of every Operator identity certificate.
	// Zero selects constants.MaxWorkloadLeafCertTTL (7 days); any other value must
	// be positive and no longer than that maximum (see ValidateOperatorLeafCertTTL).
	OperatorLeafCertTTL time.Duration

	// AllowTestPortZero should be true only when called from Go tests; when false,
	// port 0 is rejected to prevent dynamic port assignment in production.
	AllowTestPortZero bool
}

// ResolveGatewayPorts finds two available ports incrementally, starting from the
// requested ports. This allows multiple operators to run on the same host.
func ResolveGatewayPorts(httpPort, httpsPort int) (int, int) {
	if httpPort <= 0 {
		httpPort = constants.Ports.OperatorHttp
	}
	if httpsPort <= 0 {
		httpsPort = constants.Ports.OperatorHttps
	}

	// Try up to 100 offsets
	for offset := 0; offset < 100; offset++ {
		h := httpPort + offset
		s := httpsPort + offset
		if _, r := constants.GatewayReservedLoopbackPorts[h]; r {
			continue
		}
		if _, r := constants.GatewayReservedLoopbackPorts[s]; r {
			continue
		}

		if netutil.CheckTCPPortAvailable(h) == nil && netutil.CheckTCPPortAvailable(s) == nil {
			return h, s
		}
	}

	// Fallback to original if we can't find a free block (let it fail during bind)
	return httpPort, httpsPort
}

// validateGatewayPorts validates the gateway port configuration. It rejects an
// explicit zero port in production unless both ports are zero, which selects the
// defaults. It never probes the host: the Gateway start path resolves free ports
// with ResolveGatewayPorts after loading.
func validateGatewayPorts(httpPort, httpsPort int, allowTestPortZero bool) (int, int, error) {
	if !allowTestPortZero {
		switch {
		case httpPort == 0 && httpsPort == 0:
			httpPort, httpsPort = constants.Ports.OperatorHttp, constants.Ports.OperatorHttps
		case httpPort == 0:
			return 0, 0, constants.ErrConfigHTTPPortZero
		case httpsPort == 0:
			return 0, 0, constants.ErrConfigHTTPSPortZero
		}
	}

	// Validate that all ports are unique to prevent conflicts.
	// Zero-valued ports are ignored so test/default configurations can leave
	// optional ports unset without tripping false conflicts.
	if httpPort > 0 && httpsPort > 0 && httpPort == httpsPort {
		return 0, 0, constants.ErrConfigPortsMustDiffer
	}

	return httpPort, httpsPort, nil
}

// ValidateOperatorLeafCertTTL fails closed unless ttl is positive and no longer
// than the platform workload leaf maximum (constants.MaxWorkloadLeafCertTTL).
// Gateway start and PKI construction both apply it, so an out-of-range lifetime
// cannot reach certificate issuance.
func ValidateOperatorLeafCertTTL(ttl time.Duration) error {
	if ttl <= 0 || ttl > constants.MaxWorkloadLeafCertTTL {
		return fmt.Errorf("%w: got %s, allowed range is (0, %s]", constants.ErrConfigLeafCertTTLInvalid, ttl, constants.MaxWorkloadLeafCertTTL)
	}
	return nil
}

// LoadGateway creates configuration for gateway mode.
// Gateway mode skips all operator-mode validation - no endpoint,
// no outbound connections. The Operator simply starts and listens locally.
func LoadGateway(opts GatewayOptions) (*Config, error) {
	if err := opts.OperatorCapabilities.Validate(); err != nil {
		return nil, err
	}
	// Embedded and outbound Operators use the same explicit capabilities and Data default.
	capabilities := opts.OperatorCapabilities.WithDefault()
	provenance, err := newProvenanceOperatorConfig(capabilities.Has(constants.OperatorCapabilityProvenance), LoadOptions{ProvenanceOperatorID: opts.ProvenanceOperatorID, ProvenanceOperatorModelStorageRoot: opts.ProvenanceOperatorModelStorageRoot, ProvenanceOperatorModelStorageFormat: opts.ProvenanceOperatorModelStorageFormat})
	if err != nil {
		return nil, err
	}
	inference, err := newInferenceConfig(capabilities.Has(constants.OperatorCapabilityInference), LoadOptions{InferenceOllamaEndpoint: opts.InferenceOllamaEndpoint, InferenceLlamaCppEndpoint: opts.InferenceLlamaCppEndpoint, InferenceLlamaCppModelRoot: opts.InferenceLlamaCppModelRoot, InferenceKeepAlive: opts.InferenceKeepAlive, InferenceAllowHostedDecision: opts.InferenceAllowHostedDecision, InferenceDecisionModels: opts.InferenceDecisionModels, InferenceDecisionOnly: opts.InferenceDecisionOnly})
	if err != nil {
		return nil, err
	}

	// Initialize paths relative to current working directory
	projectRoot := opts.WorkDir
	if projectRoot == "" {
		projectRoot = FindProjectRoot()
	}
	if projectRoot == "" {
		projectRoot = "."
	}
	if err := paths.InitWithBase(projectRoot); err != nil {
		return nil, fmt.Errorf("config: failed to initialize paths: %w", err)
	}

	// Resolve paths using canonical constants
	dataDir := opts.DataDir
	if dataDir == "" {
		dataDir = paths.Infra.DataDir
	}
	pkiDir := opts.PKIDir
	if pkiDir == "" {
		pkiDir = paths.Infra.PkiDir
	}

	mcpDownstreamURL := opts.MCPDownstreamURL
	mcpDownstreamCmd := opts.MCPDownstreamCmd
	mcpDownstreamArgs := opts.MCPDownstreamArgs
	a2aDownstreamURL := opts.A2ADownstreamURL
	secretsDir := opts.SecretsDir
	if secretsDir == "" {
		secretsDir = paths.Infra.SecretsDir
	}

	vaultDir := opts.VaultDir
	if vaultDir == "" {
		vaultDir = paths.Infra.VaultDir
	}

	httpPort, httpsPort, err := validateGatewayPorts(
		opts.HTTPPort,
		opts.HTTPSPort,
		opts.AllowTestPortZero,
	)
	if err != nil {
		return nil, err
	}

	passkeyRpID := opts.PasskeyRpID
	if passkeyRpID == "" {
		passkeyRpID = "localhost"
	}
	// Normalize 127.0.0.1 to localhost for passkey RP ID
	// WebAuthn requires RP ID to be a valid domain, not an IP address
	if passkeyRpID == "127.0.0.1" {
		passkeyRpID = "localhost"
	}
	passkeyRpName := opts.PasskeyRpName
	if passkeyRpName == "" {
		passkeyRpName = "ztd"
	}

	posture := opts.Posture
	if posture == "" {
		posture = constants.DefaultPosture
	}
	if !posture.Valid() {
		return nil, fmt.Errorf("load gateway config: %w: %q", constants.ErrInvalidPosture, posture)
	}

	jwksURL := opts.JWKSURL
	jwtRoleClaim := opts.JWTRoleClaim
	if jwtRoleClaim == "" {
		jwtRoleClaim = "roles"
	}
	jwtIssuer := opts.JWTIssuer
	jwtAudience := opts.JWTAudience

	operatorLeafCertTTL := opts.OperatorLeafCertTTL
	if operatorLeafCertTTL == 0 {
		operatorLeafCertTTL = constants.MaxWorkloadLeafCertTTL
	}
	if err := ValidateOperatorLeafCertTTL(operatorLeafCertTTL); err != nil {
		return nil, err
	}

	return &Config{
		OperatorCapabilities: capabilities,
		// The embedded Operator executes in its own runtime directory, never in
		// the project, so model-driven work cannot touch ztd source or state.
		WorkDir:                  paths.EmbeddedOperatorWorkDir,
		Inference:                inference,
		ProviderBoundaryObserver: newProviderBoundaryObserverConfig(capabilities.Has(constants.OperatorCapabilityObserver), LoadOptions{ProviderBoundaryObserverID: opts.ProviderBoundaryObserverID}),
		ProvenanceOperator:       provenance,

		ComponentName:      constants.ComponentNameZTDOGateway,
		PKIDir:             pkiDir,
		SecretsDir:         secretsDir,
		MaxConcurrentTasks: 25,
		MaxMemoryMB:        2048,

		Gateway: GatewayConfig{
			Enabled: true,
			Posture: posture,

			HTTPPort:          httpPort,
			HTTPSPort:         httpsPort,
			ListenHost:        listenHostOrDefault(opts.ListenHost),
			DataDir:           dataDir,
			PKIDir:            pkiDir,
			SecretsDir:        secretsDir,
			VaultDir:          vaultDir,
			Keyring:           opts.Keyring,
			PasskeyRpID:       passkeyRpID,
			PasskeyRpName:     passkeyRpName,
			PasskeyRpOrigins:  opts.PasskeyRpOrigins,
			MCPDownstreamURL:  mcpDownstreamURL,
			MCPDownstreamCmd:  mcpDownstreamCmd,
			MCPDownstreamArgs: mcpDownstreamArgs,
			A2ADownstreamURL:  a2aDownstreamURL,
			PublicBaseURL:     opts.PublicBaseURL,
			JWKSURL:           jwksURL,
			JWTRoleClaim:      jwtRoleClaim,
			JWTIssuer:         jwtIssuer,
			JWTAudience:       jwtAudience,

			// HTTP server limits with fail-closed defaults
			MaxPayloadBytes:   512 * 1024, // 512KB
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20, // 1MB

			RateLimitRPS:   opts.RateLimitRPS,
			RateLimitBurst: opts.RateLimitBurst,

			// Certificate mode
			CertMode:            opts.CertMode,
			NetworkIdentityFile: opts.NetworkIdentityFile,
			OperatorLeafCertTTL: operatorLeafCertTTL,

			AllowedOrigins: opts.AllowedOrigins,
			DoctrineDir:    opts.DoctrineDir,

			// Distributed lock retry defaults
			LockMaxRetries: 30,                    // 30 retry attempts
			LockRetryDelay: 50 * time.Millisecond, // 50ms base delay
		},
	}, nil
}

// Load creates configuration from explicit options passed by main
func Load(opts LoadOptions) (*Config, error) {
	if err := opts.OperatorCapabilities.Validate(); err != nil {
		return nil, err
	}
	provenance, err := newProvenanceOperatorConfig(opts.OperatorCapabilities.Has(constants.OperatorCapabilityProvenance), opts)
	if err != nil {
		return nil, err
	}
	inference, err := newInferenceConfig(opts.OperatorCapabilities.Has(constants.OperatorCapabilityInference), opts)
	if err != nil {
		return nil, err
	}

	// Resolve the worker root. An explicit --working-dir owns the Operator's
	// .ztd vault, ledger, PKI, and databases; the repository root is only the
	// default when it is absent. Runtime directories are derived from it here,
	// never from the process-wide paths.Infra, so Operators sharing a process
	// cannot overwrite each other's roots.
	projectRoot := FindProjectRoot()
	if projectRoot == "" {
		projectRoot = "."
	}
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = projectRoot
	} else {
		var err error
		workDir, err = filepath.Abs(workDir)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", constants.ErrConfigInvalidWorkingDir, opts.WorkDir)
		}
	}
	runtimeDir := filepath.Join(workDir, constants.RuntimeDirname)

	if opts.OperatorEndpoint == "" {
		return nil, constants.ErrEndpointRequired
	}

	// Build config from explicit options
	tlsServerName := TLSServerName(opts.OperatorEndpoint)
	cfg := &Config{
		OperatorCapabilities: opts.OperatorCapabilities.Canonical(),
		// From options
		CloudMode:     opts.CloudMode,
		CloudProvider: opts.CloudProvider,
		WorkDir:       workDir,
		PKIDir:        opts.PKIDir,
		SecretsDir:    opts.SecretsDir,

		// Derived values - ports default to values from paths.json
		Endpoint:  opts.OperatorEndpoint,
		PubSubURL: buildPubSubURL(opts.OperatorEndpoint, opts.HTTPSPort),

		HTTPPort:      httpPortOrDefault(opts.HTTPPort),
		HTTPSPort:     httpsPortOrDefault(opts.HTTPSPort),
		LogLevel:      opts.LogLevel,
		TLSServerName: tlsServerName,
		ProjectID:     "ztd",

		// Fixed defaults
		ComponentName:      constants.ComponentNameZTDO,
		MaxConcurrentTasks: 25,
		MaxMemoryMB:        2048,
		HeartbeatInterval:  heartbeatIntervalOrDefault(opts.HeartbeatInterval),

		Keyring: opts.Keyring,

		// System / process context
		Shell: opts.Shell,
		Lang:  opts.Lang,
		Term:  opts.Term,
		TZ:    opts.TZ,

		// Inference backend (ztdllama). Disabled by default; enabled when
		// the operator runs as an Inference Node calling the configured
		// remote Ollama provider.
		Inference:                inference,
		ProviderBoundaryObserver: newProviderBoundaryObserverConfig(opts.OperatorCapabilities.Has(constants.OperatorCapabilityObserver), opts),
		ProvenanceOperator:       provenance,
	}

	// Default PKIDir to .ztd/pki if not explicitly set
	if cfg.PKIDir == "" {
		cfg.PKIDir = filepath.Join(runtimeDir, constants.PkiDirname)
	}

	// Default SecretsDir to .ztd/secrets if not explicitly set
	if cfg.SecretsDir == "" {
		cfg.SecretsDir = filepath.Join(runtimeDir, constants.SecretsDirname)
	}

	// Default VaultDir to .ztd/vault if not explicitly set
	if cfg.VaultDir == "" {
		cfg.VaultDir = filepath.Join(runtimeDir, constants.VaultDirname)
	}

	return cfg, nil
}

// heartbeatIntervalOrDefault returns d if positive, otherwise
// constants.OperatorDefaultHeartbeatInterval.
func heartbeatIntervalOrDefault(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return constants.OperatorDefaultHeartbeatInterval
}

// newInferenceConfig builds an InferenceConfig from LoadOptions, applying
// defaults for unset fields. A llama.cpp endpoint selects the llama.cpp
// provider and requires its model root; otherwise the provider is Ollama and
// its endpoint defaults to loopback on the canonical Ollama port.
func newInferenceConfig(enabled bool, opts LoadOptions) (InferenceConfig, error) {
	cfg := InferenceConfig{
		Enabled:             enabled,
		AllowHostedDecision: opts.InferenceAllowHostedDecision,
		DecisionModels:      append([]string(nil), opts.InferenceDecisionModels...),
		DecisionOnly:        opts.InferenceDecisionOnly,
		Provider:            constants.InferenceProviderOllama,
		OllamaEndpoint:      strings.TrimSpace(opts.InferenceOllamaEndpoint),
		LlamaCppEndpoint:    strings.TrimSpace(opts.InferenceLlamaCppEndpoint),
		LlamaCppModelRoot:   strings.TrimSpace(opts.InferenceLlamaCppModelRoot),
		KeepAlive:           cmp.Or(opts.InferenceKeepAlive, constants.InferenceDefaultKeepAlive),
	}
	if cfg.LlamaCppEndpoint == "" {
		if cfg.OllamaEndpoint == "" {
			cfg.OllamaEndpoint = fmt.Sprintf("http://127.0.0.1:%d", constants.InferenceOllamaDefaultPort)
		}
		return cfg, nil
	}
	cfg.Provider = constants.InferenceProviderLlamaCpp
	if !enabled {
		return cfg, nil
	}
	if cfg.OllamaEndpoint != "" {
		return InferenceConfig{}, constants.ErrInferenceProviderConflict
	}
	if cfg.LlamaCppModelRoot == "" {
		return InferenceConfig{}, constants.ErrInferenceLlamaCppModelRootRequired
	}
	if cfg.KeepAlive != "-1" && cfg.KeepAlive != "0" {
		return InferenceConfig{}, fmt.Errorf("config: inference keep-alive %q: %w", cfg.KeepAlive, constants.ErrInferenceLlamaCppKeepAliveInvalid)
	}
	return cfg, nil
}

func newProviderBoundaryObserverConfig(enabled bool, opts LoadOptions) ProviderBoundaryObserverConfig {
	observerID := opts.ProviderBoundaryObserverID
	if observerID == "" {
		observerID = "ztd-provider-boundary-observer"
	}
	return ProviderBoundaryObserverConfig{
		Enabled:    enabled,
		ObserverID: observerID,
	}
}

func newProvenanceOperatorConfig(enabled bool, opts LoadOptions) (ProvenanceOperatorConfig, error) {
	modelStorageRoot := strings.TrimSpace(opts.ProvenanceOperatorModelStorageRoot)
	if enabled && modelStorageRoot == "" {
		return ProvenanceOperatorConfig{}, constants.ErrProvenanceModelStorageRootRequired
	}
	format := cmp.Or(strings.TrimSpace(opts.ProvenanceOperatorModelStorageFormat), constants.ModelStorageFormatOllama)
	if format != constants.ModelStorageFormatOllama && format != constants.ModelStorageFormatLlamaCpp {
		return ProvenanceOperatorConfig{}, fmt.Errorf("config: model storage format %q: %w", format, constants.ErrProvenanceModelStorageFormatInvalid)
	}
	operatorID := opts.ProvenanceOperatorID
	if operatorID == "" {
		operatorID = "ztd-model-provenance-operator"
	}
	return ProvenanceOperatorConfig{
		Enabled:            enabled,
		OperatorID:         operatorID,
		ModelStorageRoot:   modelStorageRoot,
		ModelStorageFormat: format,
	}, nil
}

// buildPubSubURL creates a WebSocket URL using the network endpoint and HTTPS
// port (WSS runs over TLS). TLS certificate identity is configured separately
// through Config.TLSServerName; it must not replace the TCP dial target.
func buildPubSubURL(endpoint string, httpsPort int) string {
	port := httpsPortOrDefault(httpsPort)
	return "wss://" + net.JoinHostPort(endpoint, strconv.Itoa(port))
}

// httpPortOrDefault returns p if non-zero, otherwise the default from paths.json.
func httpPortOrDefault(p int) int {
	if p > 0 {
		return p
	}
	return constants.Ports.OperatorHttp
}

// listenHostOrDefault returns host if non-empty, otherwise loopback. Binding
// all interfaces is opt-in via an explicit host (for example 0.0.0.0).
func listenHostOrDefault(host string) string {
	if host != "" {
		return host
	}
	return constants.LocalhostIP
}

// httpsPortOrDefault returns p if non-zero, otherwise the default from paths.json.
func httpsPortOrDefault(p int) int {
	if p > 0 {
		return p
	}
	return constants.Ports.OperatorHttps
}

// ReportedHTTPPort returns the HTTP port for heartbeat reporting and logging.
// Gateway mode uses Gateway.HTTPPort; remote operator mode uses the dial port
// on Config.HTTPPort (defaults applied at load time).
func (c *Config) ReportedHTTPPort() int {
	if c.Gateway.HTTPPort > 0 {
		return c.Gateway.HTTPPort
	}
	return c.HTTPPort
}

// ReportedHTTPSPort returns the HTTPS port for heartbeat reporting and logging.
// Gateway mode uses Gateway.HTTPSPort; remote operator mode uses the dial port
// on Config.HTTPSPort (defaults applied at load time).
func (c *Config) ReportedHTTPSPort() int {
	if c.Gateway.HTTPSPort > 0 {
		return c.Gateway.HTTPSPort
	}
	return c.HTTPSPort
}

// TLSServerName returns the TLS ServerName override to use when endpoint is a
// raw IP address. When connecting to a Gateway via IP, we use the internal
// Gateway hostname (ztd.local) for TLS verification since the Gateway's
// certificate is issued to this name.
// Returns an empty string when endpoint is already a hostname (no override needed).
func TLSServerName(endpoint string) string {
	if net.ParseIP(endpoint) != nil {
		return constants.GatewayInternalHostname
	}
	return ""
}

// GatewayDialHost returns the host to dial for a Gateway endpoint. The
// internal Gateway hostname (ztd.local) falls back to loopback when it does
// not resolve, since a local Gateway binds loopback unless started with
// --listen-host. TLS still verifies ztd.local via TLSServerName on the IP.
func GatewayDialHost(endpoint string) string {
	if endpoint != constants.GatewayInternalHostname {
		return endpoint
	}
	if _, err := net.LookupHost(endpoint); err == nil {
		return endpoint
	}
	return constants.LocalhostIP
}

// EffectiveOperatorCapabilities resolves all configured capabilities in stable order.
func (cfg *Config) EffectiveOperatorCapabilities() constants.OperatorCapabilities {
	capabilities := append(constants.OperatorCapabilities{}, cfg.OperatorCapabilities...)
	if cfg.Inference.Enabled {
		capabilities = append(capabilities, constants.OperatorCapabilityInference)
	}
	if cfg.ProvenanceOperator.Enabled {
		capabilities = append(capabilities, constants.OperatorCapabilityProvenance)
	}
	if cfg.ProviderBoundaryObserver.Enabled {
		capabilities = append(capabilities, constants.OperatorCapabilityObserver)
	}
	if len(capabilities) == 0 {
		capabilities = append(capabilities, constants.OperatorCapabilityData)
	}
	return capabilities.Canonical()
}
