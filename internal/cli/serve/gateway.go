// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package serve

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/paths"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	gateway "github.com/zerotrustdata/ztd/internal/services/gateway"
	govsvc "github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keyring"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/profiling"
)

// GatewayConfig holds configuration for starting the gateway in gateway mode.
type GatewayConfig struct {
	WorkingDir                         string                         `json:"working_dir,omitempty"`
	OperatorCapabilities               constants.OperatorCapabilities `json:"operator_capabilities,omitempty"`
	InferenceOllamaEndpoint            string                         `json:"inference_ollama_endpoint,omitempty"`
	InferenceKeepAlive                 string                         `json:"inference_keep_alive,omitempty"`
	ProviderBoundaryObserverID         string                         `json:"provider_boundary_observer_id,omitempty"`
	ProvenanceOperatorID               string                         `json:"provenance_operator_id,omitempty"`
	ProvenanceOperatorModelStorageRoot string                         `json:"provenance_operator_model_storage_root,omitempty"`
	Posture                            constants.Posture              `json:"posture"`
	HTTPPort                           int                            `json:"http_port"`
	HTTPSPort                          int                            `json:"https_port"`
	ListenHost                         string                         `json:"listen_host,omitempty"`
	DataDir                            string                         `json:"data_dir,omitempty"`
	PKIDir                             string                         `json:"pki_dir,omitempty"`
	SecretsDir                         string                         `json:"secrets_dir,omitempty"`
	VaultDir                           string                         `json:"vault_dir,omitempty"`
	PasskeyRpID                        string                         `json:"passkey_rp_id,omitempty"`
	PasskeyRpName                      string                         `json:"passkey_rp_name,omitempty"`
	PasskeyRpOrigins                   []string                       `json:"passkey_rp_origins,omitempty"`
	RateLimitRPS                       float64                        `json:"rate_limit_rps"`
	RateLimitBurst                     int                            `json:"rate_limit_burst"`
	LogLevel                           string                         `json:"log_level"`
	CertIdentityMode                   string                         `json:"cert_identity_mode,omitempty"`
	NetworkIdentityFile                string                         `json:"network_identity_file,omitempty"`
	MCPDownstreamURL                   string                         `json:"mcp_downstream_url,omitempty"`
	MCPDownstreamCmd                   string                         `json:"mcp_downstream_cmd,omitempty"`
	MCPDownstreamArgs                  []string                       `json:"mcp_downstream_args,omitempty"`
	A2ADownstreamURL                   string                         `json:"a2a_downstream_url,omitempty"`
	PublicBaseURL                      string                         `json:"public_base_url,omitempty"`
	AllowedOrigins                     []string                       `json:"allowed_origins,omitempty"`
	DoctrineDir                        string                         `json:"doctrine_dir,omitempty"`
	PublicSpectatorEnabled             bool                           `json:"public_spectator_enabled"`
	PublicSpectatorPrivateAddr         string                         `json:"public_spectator_private_addr,omitempty"`
	PublicSpectatorPublicAddr          string                         `json:"public_spectator_public_addr,omitempty"`
	PublicSpectatorAllowContainerBind  bool                           `json:"public_spectator_allow_container_bind,omitempty"`
	EvalExplorerAddr                   string                         `json:"eval_explorer_addr,omitempty"`
	EvalExplorerRoot                   string                         `json:"eval_explorer_root,omitempty"`
	PublicSpectatorTrustedProxyCIDRs   []string                       `json:"public_spectator_trusted_proxy_cidrs,omitempty"`
	PprofListenAddr                    string                         `json:"pprof_listen_addr,omitempty"`
	// ConsensusMemberSignerFactory builds the signer for the Gateway-hosted
	// Tribunal consensus members. It is a process dependency, never part of
	// the launch profile; nil leaves the Tribunal unable to vote.
	ConsensusMemberSignerFactory func(ctx context.Context, fileSvc fs.RuntimeFileService, kr keyring.Keyring) (gateway.ConsensusMemberSigner, error) `json:"-"`
}

// RunGateway starts the Operator in gateway mode - the platform's central
// persistence (operator) and pub/sub broker. In this mode, the Operator also
// runs an in-process command service to act as the sovereign execution Gateway.
func RunGateway(cfg GatewayConfig, vi VersionInfo) error {
	// Initialize paths relative to current working directory
	if err := paths.Init(); err != nil {
		return fmt.Errorf("gateway: initialize paths: %w", err)
	}

	// Construct RuntimeFileService early so all .ztd/ I/O goes through it
	initLogger, err := logging.NewLogger(cfg.LogLevel, os.Stderr)
	if err != nil {
		return fmt.Errorf("gateway: initialize logger: %w", err)
	}
	fileSvc, err := fs.NewRuntimeFileService(cfg.WorkingDir, initLogger)
	if err != nil {
		return fmt.Errorf("gateway: create file service: %w", err)
	}
	if err := fileSvc.CreateRuntimeTree(context.Background()); err != nil {
		return fmt.Errorf("gateway: create runtime tree: %w", err)
	}

	logSvc := logging.NewLogService(fileSvc)
	logger, logHandle, err := logSvc.ConfigureFileLogger(context.Background(), cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("gateway: configure logger: %w", err)
	}
	logger = logger.With(logging.ComponentKey, logging.ComponentGateway)
	defer func() {
		if closeErr := logHandle.Close(); closeErr != nil {
			logger.Error("gateway: close log handle", "error", closeErr)
		}
	}()

	// Profiling starts before service construction so startup cost is
	// visible in CPU profiles and execution traces.
	if cfg.PprofListenAddr != "" {
		profiler, err := profiling.Start(cfg.PprofListenAddr, logger)
		if err != nil {
			return fmt.Errorf("gateway: start profiling: %w", err)
		}
		defer func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopCancel()
			if stopErr := profiler.Stop(stopCtx); stopErr != nil {
				logger.Error("gateway: stop profiling", "error", stopErr)
			}
		}()
	}

	// Apply defaults for empty directory flags
	if cfg.DataDir == "" {
		cfg.DataDir = fileSvc.Resolve(constants.DataDirname)
	}
	if cfg.PKIDir == "" {
		cfg.PKIDir = fileSvc.Resolve(constants.PkiDirname)
	}
	if cfg.SecretsDir == "" {
		cfg.SecretsDir = fileSvc.Resolve(constants.SecretsDirname)
	}

	logger.Info("Gateway paths configured", "data_dir", cfg.DataDir, "pki_dir", cfg.PKIDir, "secrets_dir", cfg.SecretsDir)

	logger.Info("ztd - Gateway Mode",
		"posture", cfg.Posture,
		"version", vi.Version,
		"build", vi.BuildID)

	gatewayCfg, err := config.LoadGateway(config.GatewayOptions{
		WorkDir:                            cfg.WorkingDir,
		OperatorCapabilities:               cfg.OperatorCapabilities,
		InferenceOllamaEndpoint:            cfg.InferenceOllamaEndpoint,
		InferenceKeepAlive:                 cfg.InferenceKeepAlive,
		ProviderBoundaryObserverID:         cfg.ProviderBoundaryObserverID,
		ProvenanceOperatorID:               cfg.ProvenanceOperatorID,
		ProvenanceOperatorModelStorageRoot: cfg.ProvenanceOperatorModelStorageRoot,
		Posture:                            cfg.Posture,
		HTTPPort:                           cfg.HTTPPort,
		HTTPSPort:                          cfg.HTTPSPort,
		ListenHost:                         cfg.ListenHost,
		DataDir:                            cfg.DataDir,
		PKIDir:                             cfg.PKIDir,
		SecretsDir:                         cfg.SecretsDir,
		VaultDir:                           cfg.VaultDir,
		PasskeyRpID:                        cfg.PasskeyRpID,
		PasskeyRpName:                      cfg.PasskeyRpName,
		PasskeyRpOrigins:                   cfg.PasskeyRpOrigins,
		RateLimitRPS:                       cfg.RateLimitRPS,
		RateLimitBurst:                     cfg.RateLimitBurst,
		CertMode:                           cfg.CertIdentityMode,
		NetworkIdentityFile:                cfg.NetworkIdentityFile,
		MCPDownstreamURL:                   cfg.MCPDownstreamURL, // empty by default — no downstream proxy
		MCPDownstreamCmd:                   cfg.MCPDownstreamCmd,
		MCPDownstreamArgs:                  cfg.MCPDownstreamArgs,
		A2ADownstreamURL:                   cfg.A2ADownstreamURL, // empty by default — no downstream proxy
		PublicBaseURL:                      cfg.PublicBaseURL,
		AllowedOrigins:                     cfg.AllowedOrigins,
		DoctrineDir:                        cfg.DoctrineDir,
		AllowTestPortZero:                  false,
	})
	if err != nil {
		return fmt.Errorf("gateway: load configuration: %w", err)
	}
	gatewayCfg.Gateway.HTTPPort, gatewayCfg.Gateway.HTTPSPort = config.ResolveGatewayPorts(gatewayCfg.Gateway.HTTPPort, gatewayCfg.Gateway.HTTPSPort)
	gatewayCfg.Gateway.PublicSpectatorEnabled = cfg.PublicSpectatorEnabled
	gatewayCfg.Gateway.PublicSpectatorPrivateAddr = cfg.PublicSpectatorPrivateAddr
	gatewayCfg.Gateway.PublicSpectatorPublicAddr = cfg.PublicSpectatorPublicAddr
	gatewayCfg.Gateway.PublicSpectatorAllowContainerBind = cfg.PublicSpectatorAllowContainerBind
	gatewayCfg.Gateway.EvalExplorerAddr = cfg.EvalExplorerAddr
	gatewayCfg.Gateway.EvalExplorerRoot = cfg.EvalExplorerRoot
	gatewayCfg.Gateway.PublicSpectatorTrustedProxyCIDRs = cfg.PublicSpectatorTrustedProxyCIDRs
	gatewayCfg.Version = vi.Version

	svc, err := gateway.NewGatewayModeService(gatewayCfg, fileSvc, logger)
	if err != nil {
		return fmt.Errorf("gateway: create service: %w", err)
	}
	if cfg.ConsensusMemberSignerFactory != nil {
		signer, err := cfg.ConsensusMemberSignerFactory(context.Background(), fileSvc, gatewayCfg.Gateway.Keyring)
		if err != nil {
			return fmt.Errorf("gateway: create consensus member signer: %w", err)
		}
		svc.SetConsensusMemberSigner(signer)
	}

	// L2 posture advisory check:
	// The gateway starts regardless of consensus configuration — L2 enforcement
	// happens at transaction time via L4Warden. If no consensus application is bound yet,
	// log a warning so the operator knows L2-gated transactions will be rejected
	// until a consensus application is bound.
	if cfg.Posture.RequiresL2() {
		consensus := svc.ConsensusHealth(context.Background())
		if !consensus.Ready {
			logger.Warn("L2 posture requires a ready consensus binding; L2-gated transactions will be rejected until one is bound",
				"posture", cfg.Posture,
				"bound", consensus.Bound,
				"usable", consensus.Usable,
				"quorum", consensus.Quorum,
				"action", "ztd consensus enroll tribunal --model <tag>")
		} else {
			logger.Info("Consensus binding ready",
				"provider", consensus.Provider,
				"quorum", consensus.Quorum,
				"usable", consensus.Usable)
		}
	}

	// Export Actuator public key for event receipt verification by evals harness
	sm, err := svc.GetSecretManager()
	if err != nil {
		return fmt.Errorf("gateway: get secret manager: %w", err)
	}

	actuatorPriv, actuatorKeyID, err := sm.GetActuatorKey()
	if err != nil {
		return fmt.Errorf("gateway: load actuator signing key: %w", err)
	}

	actuatorPub := actuatorPriv.Public().(ed25519.PublicKey)
	logger.Info("Exporting Actuator public key", "pki_dir", cfg.PKIDir, "key_id", actuatorKeyID)
	if err := govsvc.ExportActuatorPublicKey(fileSvc, actuatorPub, actuatorKeyID, logger); err != nil {
		return fmt.Errorf("gateway: export actuator public key: %w", err)
	}

	cmdSvc := svc.GetCommandService()
	if cmdSvc == nil {
		return fmt.Errorf("gateway: command service not initialized")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	errChan := make(chan error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := svc.Start(ctx); err != nil {
			logger.Error("Gateway service failed", string(constants.ConnectionStateError), err)
			errChan <- err
		}
	}()

	// Start the command service once the gateway service is ready
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-svc.Ready():
		case <-ctx.Done():
			return
		}
		logger.Info("Gateway service ready, starting in-process command service")
		if err := cmdSvc.Start(ctx); err != nil {
			logger.Error("In-process command service failed to start", string(constants.ConnectionStateError), err)
			errChan <- err
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		logger.Info("Received signal, shutting down", "signal", sig.String())
	case err := <-errChan:
		logger.Error("Gateway service failed during operation", string(constants.ConnectionStateError), err)
	}
	cancel()
	wg.Wait()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	var shutdownErr error
	if cmdSvc.Actuator() != nil {
		logger.Info("Waiting for in-flight transactions to drain...")
		cmdSvc.Actuator().Wait()
	}
	if err := cmdSvc.Stop(); err != nil {
		logger.Error("Command service stop error", string(constants.ConnectionStateError), err)
		shutdownErr = fmt.Errorf("gateway: command service stop: %w", err)
	}

	if err := svc.Stop(shutdownCtx); err != nil {
		logger.Error("Gateway shutdown error", string(constants.ConnectionStateError), err)
		shutdownErr = fmt.Errorf("gateway: service stop: %w", err)
	}
	logger.Info("Gateway mode stopped")

	select {
	case err := <-errChan:
		return err
	default:
		return shutdownErr
	}
}
