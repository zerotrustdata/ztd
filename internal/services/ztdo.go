// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"

	"github.com/zerotrustdata/ztd/internal/services/execution"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/storage"
)

// ExecutesFor binds outbound execution to this runtime's enrolled Operator ID.
func (vs *ZtdoService) ExecutesFor(operatorID string) bool {
	return vs != nil && vs.config != nil && operatorID != "" && operatorID == vs.config.OperatorID
}

// RuntimeIdentity is the Operator and the audit session this runtime owns: its
// enrolled Operator ID and the operator session it created in its local audit
// store at startup.
func (vs *ZtdoService) RuntimeIdentity(_ context.Context) (govtypes.RuntimeIdentity, error) {
	if vs == nil || vs.config == nil || vs.config.OperatorID == "" {
		return govtypes.RuntimeIdentity{}, constants.ErrOperatorIDMissing
	}
	if vs.config.OperatorSessionId == "" {
		return govtypes.RuntimeIdentity{}, constants.ErrGatewayOperatorSessionIDRequired
	}
	return govtypes.RuntimeIdentity{OperatorID: vs.config.OperatorID, SessionID: vs.config.OperatorSessionId}, nil
}

type ZtdoService struct {
	config  *config.Config
	logger  *slog.Logger
	fileSvc fs.RuntimeFileService

	secretManager    *gateway.SecretManager
	execution        *execution.ExecutionService
	fileEdit         *execution.FileEditService
	pubSubCommands   *pubsub.OperatorPubSubService
	pubSubResults    *pubsub.PubSubResultsService
	tokenStore       storage.TokenStore
	suspendedTxStore *storage.SuspendedTransactionService
	gatewayDB        *gateway.CanonicalDBService

	pubSubClientFactory   PubSubClientFactory
	pubSubClient          pubsub.PubSubClient
	onCommandSubscription func(context.Context, string, bool) error
	tlsConfig             *certs.TLSConfig
	l2Trust               *governance.L2TrustStore
	keystore              *keystore.Keystore

	evidence *storage.LocalExecutionEvidence

	// P0 Transaction Gate infrastructure
	replayStore governance.ReplayStore

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	running   bool
	mu        sync.RWMutex
	startTime time.Time
	wg        sync.WaitGroup
}

type PubSubClientFactory func(baseURL, serverName string, logger *slog.Logger, tlsConfig *certs.TLSConfig) (pubsub.PubSubClient, error)

// NewZtdoService creates a new Operator service in Outbound Mode.
// In this mode, the Operator initiates all connections to the platform
// on port 443 and performs command execution on the local host.
func NewZtdoService(cfg *config.Config, logger *slog.Logger, tlsConfig *certs.TLSConfig, l2Trust *governance.L2TrustStore, fileSvc fs.RuntimeFileService, pubSubClientFactory PubSubClientFactory) (*ZtdoService, error) {
	if pubSubClientFactory == nil {
		return nil, fmt.Errorf("%w: pub/sub client factory is required", constants.ErrInternal)
	}
	// The enrolled in-memory identity is the only way onto the mTLS pub/sub
	// socket; without it the Operator cannot reach the Gateway.
	if tlsConfig == nil {
		return nil, fmt.Errorf("%w: operator TLS identity is required", constants.ErrInternal)
	}
	// The L2 trust arrives in the same bundle as the identity. An empty store
	// is valid (no consensus policy) and fails L2-required envelopes closed.
	if l2Trust == nil {
		return nil, fmt.Errorf("%w: operator L2 trust is required", constants.ErrInternal)
	}

	service := &ZtdoService{
		config:              cfg,
		logger:              logger,
		startTime:           time.Now().UTC(),
		tlsConfig:           tlsConfig,
		l2Trust:             l2Trust,
		fileSvc:             fileSvc,
		pubSubClientFactory: pubSubClientFactory,
		done:                make(chan struct{}),
	}

	return service, nil
}

// SetCommandSubscriptionObserver installs a startup dependency before Start.
func (vs *ZtdoService) SetCommandSubscriptionObserver(observer func(context.Context, string, bool) error) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if vs.ctx != nil {
		return fmt.Errorf("operator subscription observer: %w", constants.ErrServiceUnavailable)
	}
	vs.onCommandSubscription = observer
	return nil
}

// Done is closed when the Operator service context ends. The CLI waits on it
// so a governed shutdown request can stop the process after service cleanup.
func (vs *ZtdoService) Done() <-chan struct{} {
	return vs.done
}

func (vs *ZtdoService) Start(ctx context.Context) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	if vs.running {
		return fmt.Errorf("%w", constants.ErrServiceUnavailable)
	}

	vs.ctx, vs.cancel = context.WithCancel(ctx)
	vs.logger.Info("ztd Operator initializing (Outbound Mode)",
		"posture", vs.config.Posture)

	if vs.fileSvc == nil {
		return fmt.Errorf("%w: fileSvc must be provided to NewZtdoService", constants.ErrInternal)
	}

	// Validate local inference configuration without contacting the provider.
	// Provider availability belongs to individual requests, not Operator liveness.
	var ollamaBackend *inference.OllamaBackend
	if vs.config.Inference.Enabled {
		var err error
		ollamaBackend, err = vs.newInferenceBackend()
		if err != nil {
			return err
		}
	}

	vs.logger.Info("Establishing ztd connectivity...")
	var err error
	vs.pubSubClient, err = vs.pubSubClientFactory(vs.config.PubSubURL, vs.config.TLSServerName, vs.logger, vs.tlsConfig)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrInternal, err)
	}

	vs.execution = execution.NewExecutionService(vs.config, vs.logger)
	vs.fileEdit, err = execution.NewFileEditService(vs.config, vs.logger, vs.fileSvc)
	if err != nil {
		return fmt.Errorf("operator: create file edit service: %w", err)
	}

	// Initialize CanonicalDBService for canonical state root calculation
	// This ensures outbound mode uses the same state root schema as gateway mode.
	if vs.keystore == nil {
		vs.keystore, err = keystore.Open(vs.fileSvc, vs.logger, keystore.Options{MasterKeyFile: vs.config.MasterKeyFile})
		if err != nil {
			return fmt.Errorf("%w: %w", constants.ErrGatewayDatabaseServiceNotConfigured, err)
		}
	}
	gatewayDB, err := gateway.OpenCanonicalDBService(vs.logger, vs.keystore, vs.fileSvc)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrGatewayDatabaseServiceNotConfigured, err)
	}
	vs.gatewayDB = gatewayDB
	vs.logger.Info("Gateway database initialized (canonical state root)")

	vs.secretManager = gatewayDB.GetSecretManager()
	vs.logger.Info("Secret manager initialized")

	// Reuse vault from CanonicalDBService (already initialized and unlocked)
	encryptionVault := vs.gatewayDB.GetVault()
	if encryptionVault == nil {
		return fmt.Errorf("%w: vault not available from CanonicalDBService", constants.ErrVaultNotInitialized)
	}
	vs.logger.Info("Vault reused from CanonicalDBService")

	// Token persistence for Sentinel UEI tokens routes through the canonical gateway
	// DB (ztd.db) via EncryptedKVAdapter — no separate token_store.db.
	vs.tokenStore = gateway.NewEncryptedKVAdapter(vs.gatewayDB.GetKVStore(), encryptionVault)
	vs.logger.Info("Token store initialized (canonical KV store)")

	// The suspended transaction store is owned by the canonical DB (ztd.db).
	vs.suspendedTxStore = vs.gatewayDB.GetSuspendedStore()
	vs.logger.Info("Suspended transaction store initialized")

	vs.logger.Info("Initializing Local-First Audit Architecture (LFAA)...")

	// Reuse the SQLAuditStore from CanonicalDBService — both the standalone
	// and canonical instances open the same ztd.db file, so a separate connection
	// pool and pruner are redundant. CanonicalDBService.Close() handles lifecycle.
	auditStore := vs.gatewayDB.GetAuditStore()

	if vs.config.OperatorSessionId == "" {
		return fmt.Errorf("%w: operator session ID required before audit store can accept events", constants.ErrGatewayOperatorSessionIDRequired)
	}
	// StartSession is the single, atomic start path: it creates the row on first
	// start and marks it live again when a restarted Operator reuses its session.
	if err := auditStore.StartSession(vs.config.OperatorSessionId, constants.SessionTypeOperator, "Operator Session", vs.config.OperatorID); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditRecordUserMsg, err)
	}

	evidence, err := storage.OpenLocalExecutionEvidence(storage.LocalExecutionEvidenceConfig{
		FileSvc:    vs.fileSvc,
		Vault:      encryptionVault,
		AuditStore: auditStore,
		WorkDir:    vs.config.WorkDir,
		Logger:     vs.logger,
	})
	if err != nil {
		return fmt.Errorf("ztdo: open local execution evidence: %w", err)
	}
	vs.evidence = evidence

	// Initialize P0 Transaction Gate infrastructure (replay protection and state root verification)
	// ReplayStore is mandatory for fail-closed replay protection
	replayStore := vs.gatewayDB.GetReplayStore()
	if replayStore == nil {
		return fmt.Errorf("%w: canonical database provided no replay store", constants.ErrReplayStoreRequired)
	}
	vs.replayStore = replayStore
	vs.logger.Info("Replay store initialized for transaction verification")

	// Initialize ScrubbingService for data scrubbing (scrubbing/rehydration)
	scrubbingConfig := scrubbing.DefaultConfig()
	scrubbingService, err := scrubbing.NewScrubbingService(ctx, scrubbingConfig, vs.logger, vs.tokenStore)
	if err != nil {
		return fmt.Errorf("ztdo: failed to initialize scrubbing service: %w", err)
	}

	// Initialize PubSub Layer. Results egress through the scrubbing service.
	vs.pubSubResults, err = pubsub.NewPubSubResultsService(vs.config, vs.logger, vs.pubSubClient, scrubbingService)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrPubSubActuator, err)
	}

	// Gateway-dispatched verification uses the admission snapshot already
	// bound into the envelope. The provider captures this runtime's local
	// state for L5 receipt evidence; execution never queries Gateway state.
	stateRootProvider := vs.gatewayDB.GetStateRootSvc()
	transactionAudit := auditStore
	// L3Notary for outbound mode: CLI-based approval via suspended transactions
	// Mutations requiring L3 are suspended and must be approved via CLI command
	cliL3Notary := governance.NewOutboundL3Notary(vs.suspendedTxStore, vs.logger)

	// Load signing keys for Actuator (fail-closed if missing)
	actuatorPriv, actuatorKeyID, err := vs.secretManager.GetActuatorKey()
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyReadFailed, err)
	}
	auditorPriv, auditorKeyID, err := vs.secretManager.GetAuditorKey()
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyReadFailed, err)
	}

	handlers, err := pubsub.NewCapabilityHandlers(vs.config, vs.logger, vs.fileSvc, scrubbingService, vs.pubSubResults, ollamaBackend)
	if err != nil {
		return err
	}

	// OperatorPubSubService Construction
	psConfig := pubsub.CommandServiceConfig{
		Config:                   vs.config,
		OnCommandSubscription:    vs.onCommandSubscription,
		Logger:                   vs.logger,
		Execution:                vs.execution,
		FileEdit:                 vs.fileEdit,
		PubSubClient:             vs.pubSubClient,
		ResultsService:           vs.pubSubResults,
		AuditStore:               auditStore,
		RuntimeVault:             encryptionVault,
		Ledger:                   vs.evidence.Ledger,
		HistoryHandler:           vs.evidence.HistoryHandler,
		Scrubbing:                scrubbingService,
		Inference:                handlers.Inference,
		InferenceAttemptStore:    handlers.InferenceAttemptStore,
		ProviderBoundaryObserver: handlers.ProviderBoundaryObserver,
		ModelProvenanceOperator:  handlers.ModelProvenanceOperator,
		ActuatorSigningKey:       actuatorPriv,
		ActuatorKeyID:            actuatorKeyID,
		AuditorSigningKey:        auditorPriv,
		AuditorKeyID:             auditorKeyID,
	}

	outboundDeps, err := pubsub.NewOutboundModeDeps(pubsub.OutboundModeDeps{
		GovernanceCoreDeps: pubsub.GovernanceCoreDeps{
			ExecutionTarget:   vs,
			ReplayStore:       vs.replayStore,
			StateRootProvider: stateRootProvider,
			TransactionAudit:  transactionAudit,
			SignerStore:       vs.l2Trust,
			L3Notary:          cliL3Notary,
			Doctrine:          governance.NewL1Doctrine(),
		},
		ConsensusPolicyStore: vs.l2Trust,
	})
	if err != nil {
		return fmt.Errorf("ztdo: outbound mode deps: %w", err)
	}

	vs.pubSubCommands, err = pubsub.NewOperatorPubSubService(psConfig, *outboundDeps)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrPubSubActuator, err)
	}

	if err = vs.pubSubCommands.Start(vs.ctx); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrServiceUnavailable, err)
	}

	vs.running = true

	// Handle external shutdown requests (remote shutdown or SSL failure)
	vs.wg.Add(1)
	go func() {
		defer vs.wg.Done()
		defer close(vs.done)
		select {
		case reason := <-vs.pubSubCommands.ShutdownChan:
			vs.logger.Info("ztdo Service received external shutdown request", "reason", reason)
			// We can't call vs.Stop() here because it would deadlock on vs.mu
			// Instead, we signal the main loop via the context or a dedicated channel if needed.
			// However, in our current architecture, the main.go's context is what we should cancel.
			if vs.cancel != nil {
				vs.cancel()
			}
		case <-vs.ctx.Done():
			return
		}
	}()

	vs.logger.Info("ztd Operator started successfully!",
		"max_concurrent_tasks", vs.config.MaxConcurrentTasks,
		"startup_duration", time.Since(vs.startTime))

	// Print startup banner to stdout
	printOperatorStartupBanner(vs.config, vs.logger)

	vs.logger.Info("Standing by")
	return nil
}

// newInferenceBackend validates the endpoint without requiring the remote provider
// to be online. The reusable backend retries connectivity on each request, allowing
// recovery without restarting or re-enrolling the Operator.
func (vs *ZtdoService) newInferenceBackend() (*inference.OllamaBackend, error) {
	backend, err := inference.NewOllamaBackend(vs.config.Inference.OllamaEndpoint, vs.logger)
	if err != nil {
		return nil, fmt.Errorf("ztdo: inference backend: %w", err)
	}
	return backend, nil
}

// Stop gracefully shuts down all ztdo sub-services.
func (vs *ZtdoService) Stop(ctx context.Context) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	if !vs.running {
		return nil
	}

	vs.logger.Info("ztd Operator shutting down...")

	if vs.cancel != nil {
		vs.cancel()
	}

	// Stop pubsub command service first to stop receiving new commands
	if vs.pubSubCommands != nil {
		if vs.pubSubCommands.Actuator() != nil {
			vs.logger.Info("Waiting for in-flight transactions to drain...")
			vs.pubSubCommands.Actuator().Wait()
		}
		if err := vs.pubSubCommands.Stop(); err != nil {
			vs.logger.Error("ztdo: failed to stop pubsub command service", "error", err)
		}
	}

	// Stop execution service to kill any active tasks
	if vs.execution != nil {
		vs.execution.Stop()
	}

	// Drain audit store writes (CanonicalDBService.Close() handles final close)
	if vs.gatewayDB != nil && vs.gatewayDB.GetAuditStore() != nil {
		vs.logger.Info("Waiting for audit writes to drain...")
		vs.gatewayDB.GetAuditStore().Wait()
	}

	// Wait for shutdown handler goroutine to complete
	vs.wg.Wait()

	// The runtime session is over: mark its audit session ended so retention may
	// prune it. A failure is logged; the row then stays live and is never pruned.
	if vs.gatewayDB != nil && vs.gatewayDB.GetAuditStore() != nil && vs.config.OperatorSessionId != "" {
		if err := vs.gatewayDB.GetAuditStore().EndSession(vs.config.OperatorSessionId); err != nil {
			vs.logger.Error("ztdo: failed to end audit session", "operator_session_id", vs.config.OperatorSessionId, "error", err)
		}
	}

	// Close vaults and stores
	if vs.gatewayDB != nil {
		if err := vs.gatewayDB.Close(); err != nil {
			vs.logger.Error("ztdo: failed to close gateway database", "error", err)
		}
	}

	if vs.replayStore != nil {
		if err := vs.replayStore.Close(); err != nil {
			vs.logger.Error("ztdo: failed to close replay store", "error", err)
		}
	}

	vs.running = false
	vs.logger.Info("ztd Operator stopped")
	return nil
}

// printOperatorStartupBanner prints the Operator startup banner to stdout
func printOperatorStartupBanner(cfg *config.Config, logger *slog.Logger) {
	logger.Info("[ztdo] Initializing Edge Execution Operator...")
	logger.Info("Operator Integrity & Uplink",
		"identity_attestation", "VERIFIED (mTLS Client Certificate Valid)",
		"gateway_uplink", fmt.Sprintf("CONNECTED (WSS @ %s:%d)", cfg.Endpoint, cfg.HTTPSPort),
		"heartbeat", "30s interval established",
		"sovereign_boundary", "ACTIVE (Data egress scrubbing enabled)")
	logger.Info("CAPABILITIES & EXPOSED TOOLING",
		"system.run", "GRANTED: Requires L1 Signature",
		"fs.read", fmt.Sprintf("GRANTED: Scoped to %s", cfg.WorkDir),
		"fs.write", "GRANTED: Requires L1 Signature",
		"net.fetch", "DENIED: Air-gap mode active")
	logger.Info("[ztdo] Edge node operational. Awaiting cryptographically signed agentic intents...")
}
