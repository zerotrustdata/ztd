// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package gateway provides services for gateway mode (operator platform mode).
//
// This package contains mode-specific services that are only used in gateway mode,
// including GatewayModeService (the top-level orchestrator) and CanonicalDBService
// (shared with outbound mode for state root calculation).
//
// For more information on service modes, see docs/architecture/service_modes.md.
package gateway

import (
	"context"
	"crypto/ed25519"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

// gatewaySchema is the canonical Operator SQLite schema, embedded at compile time
// from `schema.sql`. That file is the single source of truth - do not inline
// CREATE TABLE statements in Go code.
//
//go:embed db/schema.sql
var gatewaySchema string

// stores holds the extracted single-responsibility store services.
// CanonicalDBService privately owns this aggregate for lifecycle management
// (maintenance, close). Consumers retrieve only the narrow typed services they
// use through the Get*Store accessors on CanonicalDBService; the aggregate
// itself never crosses the package boundary.
type stores struct {
	DocStore       *DocumentStoreService
	AppPolicyStore *AppPolicyStoreService
	SignerStore    *SignerStoreService
	ConsensusStore *ConsensusStoreService
	StateRootSvc   *StateRootService
	ReplayStore    *storage.ReplayStore
	KVStore        *KVStoreService
	SSEStore       *SSEEventService
	BlobStore      *BlobStoreService
	EventsStore    *storage.LocalFirstEventsStore
	SuspendedStore *storage.SuspendedTransactionService
}

// CanonicalDBService manages the lifecycle of the unified SQLite persistence
// layer for gateway mode. It owns the database connection, vault, secret
// manager, and background maintenance. Domain logic is delegated to the
// extracted store services held in the private stores aggregate, which
// consumers reach only through narrow typed accessors.
//
// This service is used in both gateway mode (full database service) and
// outbound mode (state root calculation only).
type CanonicalDBService struct {
	db     *sqliteutil.DB
	logger *slog.Logger
	vault  *vault.Vault
	sm     *SecretManager

	// stores holds the extracted services for lifecycle management (maintenance, close).
	stores *stores

	// Shutdown tracking
	mu      sync.Mutex
	running bool
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
}

// OpenCanonicalDBService opens (or creates) the unified SQLite database.
// ks is the initialized keystore holding the master key (see keystore.Open);
// it encrypts the vault key and every platform secret at rest. Close zeroes
// the master key ks holds in memory.
func OpenCanonicalDBService(logger *slog.Logger, ks *keystore.Keystore, fileSvc fs.RuntimeFileService) (*CanonicalDBService, error) {
	if fileSvc == nil {
		return nil, fmt.Errorf("%w: runtime file service", constants.ErrMissingRequiredField)
	}
	if ks == nil {
		return nil, fmt.Errorf("%w: keystore", constants.ErrMissingRequiredField)
	}

	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	cfg := sqliteutil.DefaultDBConfig(dbPath)

	db, err := sqliteutil.OpenDB(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrDatabaseLocked, err)
	}

	vaultConfig := &vault.VaultConfig{
		FileSvc: fileSvc,
		Logger:  logger,
	}
	encryptionVault, err := vault.NewVault(vaultConfig)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: %w", constants.ErrVaultCreateFailed, err)
	}

	vaultDirAbs := fileSvc.Resolve(constants.VaultDirname)

	// Auto-initialize vault on first run. If no vault header exists, generate
	// a random key, store it encrypted under the keystore master key, then
	// create the vault header. The key is stored before the header so a crash
	// in between leaves a vault that re-initializes, never one with no key.
	headerExists, err := vault.VaultHeaderExists(fileSvc)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("gateway: check vault header: %w", err)
	}
	if !headerExists {
		if err := ks.InitVault(); err != nil {
			db.Close()
			return nil, err
		}
		logger.Info("Vault auto-initialized on first run", "vault_dir", vaultDirAbs, "key_store", ks.KeyringName())
	}

	// Unlock vault. Encryption is required for secure data storage at rest —
	// the vault must always be unlocked at startup. If the key cannot be read
	// or the vault cannot be unlocked, the gateway fails to start.
	privateKey, err := ks.LoadVaultKey()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: %w", constants.ErrVaultKeyReadFailed, err)
	}
	defer vault.SecureZero(privateKey)

	if err := encryptionVault.Unlock(privateKey); err != nil {
		db.Close()
		if errors.Is(err, constants.ErrVaultNotInitialized) {
			return nil, fmt.Errorf("%w: %s", constants.ErrVaultNotInitialized, vaultDirAbs)
		}
		if errors.Is(err, constants.ErrVaultInvalidPrivateKey) {
			return nil, fmt.Errorf("%w: %s", constants.ErrVaultKeyDecodeFailed, constants.SecretsFileVaultKey)
		}
		return nil, fmt.Errorf("%w: %w", constants.ErrVaultUnlockFailed, err)
	}
	logger.Info("Vault unlocked successfully", "vault_dir", vaultDirAbs)

	// Initialize LocalFirstEventsStore for transaction-native audit recording. It
	// shares this pool, so every ztd.db writer queues on one writer gate.
	eventsStoreConfig := storage.DefaultLocalFirstEventsStoreConfig()
	eventsStoreConfig.EncryptionVault = encryptionVault
	eventsStore, err := storage.NewLocalFirstEventsStore(eventsStoreConfig, db, logger, fileSvc)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("%w: %w", constants.ErrGatewayDBEventsStoreInit, err)
	}

	// From here the local-first events store owns a pruner goroutine, so every error path
	// stops it before the pool it prunes is closed.
	closeAfterEventsStore := func() {
		if closeErr := eventsStore.Close(); closeErr != nil {
			logger.Error("EventsStore close error during failed initialization", "error", closeErr)
		}
		db.Close()
	}

	// Replay protection shares this pool and owns the nonces table.
	replayStore, err := storage.NewReplayStore(db, logger)
	if err != nil {
		closeAfterEventsStore()
		return nil, fmt.Errorf("%w: %w", constants.ErrGatewayDBReplayStoreInit, err)
	}

	// The suspended transaction store shares this pool and encrypts its content
	// columns with the unlocked runtime vault.
	suspendedStore, err := storage.NewSuspendedTransactionService(db, encryptionVault, logger)
	if err != nil {
		closeAfterEventsStore()
		return nil, fmt.Errorf("%w: %w", constants.ErrGatewayDBSuspendedStoreInit, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	svc := &CanonicalDBService{
		db:      db,
		logger:  logger,
		vault:   encryptionVault,
		ctx:     ctx,
		cancel:  cancel,
		running: true,
	}

	// The schema and Actuator key come first: the SignerStore binds the
	// Gateway's own Actuator key as the embedded Operator's receipt key, and
	// that key is created by the secret manager's app settings initialization.
	if err := svc.initSchema(fileSvc, ks); err != nil {
		cancel()
		closeAfterEventsStore()
		return nil, fmt.Errorf("%w: %w", constants.ErrGatewayDBSchemaInit, err)
	}
	actuatorPriv, actuatorKeyID, err := svc.sm.GetActuatorKey()
	if err != nil {
		cancel()
		closeAfterEventsStore()
		return nil, fmt.Errorf("%w: %w", constants.ErrKeyReadFailed, err)
	}
	actuatorPub := actuatorPriv.Public().(ed25519.PublicKey)

	auditorPriv, auditorKeyID, err := svc.sm.GetAuditorKey()
	if err != nil {
		cancel()
		closeAfterEventsStore()
		return nil, fmt.Errorf("%w: %w", constants.ErrKeyReadFailed, err)
	}
	auditorPub := auditorPriv.Public().(ed25519.PublicKey)
	eventsStore.SetCheckpointSigner(storage.NewEd25519CheckpointSigner(auditorPriv, auditorKeyID))
	eventsStore.SetPinnedAuditorPublicKey(auditorPub)

	// Initialize extracted services with the same db connection
	s := &stores{
		DocStore:     NewDocumentStoreService(db, logger),
		StateRootSvc: NewStateRootService(db, logger),
		ReplayStore:  replayStore,
		KVStore:      NewKVStoreService(db, logger),
		SSEStore:     NewSSEEventService(db, logger),
		BlobStore:    NewBlobStoreService(db, logger),
		EventsStore:  eventsStore,
	}
	s.SuspendedStore = suspendedStore
	s.AppPolicyStore = NewAppPolicyStoreService(db, logger, s.DocStore)
	s.SignerStore = NewSignerStoreService(db, logger, s.DocStore, actuatorPub, actuatorKeyID)
	s.ConsensusStore = NewConsensusStoreService(db, logger, s.DocStore, s.SignerStore)
	svc.stores = s

	// Initialize state root if missing
	if err := svc.initStateRoot(); err != nil {
		cancel()
		closeAfterEventsStore()
		return nil, fmt.Errorf("%w: %w", constants.ErrGatewayDBStateRootInit, err)
	}

	// Start background maintenance
	svc.wg.Add(1)
	go svc.RunMaintenance(svc.ctx)

	logger.Info("Gateway database initialized", "path", dbPath)
	return svc, nil
}

// initStateRoot builds the incremental state commitment once for a new database.
// A database recorded under any other commitment algorithm is not readable by
// this build and fails closed rather than being silently rebuilt.
func (s *CanonicalDBService) initStateRoot() error {
	algorithm, err := s.stores.StateRootSvc.CommitmentAlgorithm()
	if err != nil {
		return fmt.Errorf("canonicalDB: init state root: %w", err)
	}
	switch algorithm {
	case stateCommitmentAlgorithm:
		return nil
	case 0:
	default:
		return fmt.Errorf("canonicalDB: init state root: %w: recorded %d, supported %d",
			constants.ErrStateRootUnsupportedAlgorithm, algorithm, stateCommitmentAlgorithm)
	}
	start := time.Now()
	if err := s.stores.StateRootSvc.RebuildCommitment(s.ctx); err != nil {
		return fmt.Errorf("canonicalDB: init state root: rebuild: %w", err)
	}
	s.logger.Info("State commitment initialized", "algorithm", stateCommitmentAlgorithm, "elapsed", time.Since(start))
	return nil
}

// RunMaintenance periodically removes expired entries by delegating to extracted services.
func (s *CanonicalDBService) RunMaintenance(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.stores.KVStore.RunMaintenance(ctx); err != nil {
				s.logger.Warn("KV store maintenance error", "error", err)
			}
			if err := s.stores.BlobStore.RunMaintenance(); err != nil {
				s.logger.Warn("Blob store maintenance error", "error", err)
			}
			if err := s.stores.ReplayStore.CleanupExpiredNonces(); err != nil {
				s.logger.Warn("Replay store maintenance error", "error", err)
			}
			if _, err := s.stores.SSEStore.SSEEventsCleanup(time.Hour); err != nil {
				s.logger.Warn("SSE event cleanup error", "error", err)
			}
		}
	}
}

func (s *CanonicalDBService) initSchema(fileSvc fs.RuntimeFileService, ks *keystore.Keystore) error {
	_, err := s.db.ExecWithRetry(context.Background(), gatewaySchema)
	if err != nil {
		return fmt.Errorf("canonicalDB: init schema: %w", err)
	}

	sm := &SecretManager{
		db:       s.db,
		logger:   s.logger,
		fileSvc:  fileSvc,
		keystore: ks,
	}
	s.sm = sm
	if err := sm.InitAppSettings(); err != nil {
		return fmt.Errorf("canonicalDB: init schema: app settings: %w", err)
	}

	return nil
}

// GetSecretManager returns the SecretManager initialized during schema init.
func (s *CanonicalDBService) GetSecretManager() *SecretManager {
	return s.sm
}

func (s *CanonicalDBService) GetVault() *vault.Vault {
	return s.vault
}

// GetDocStore returns the document store service.
func (s *CanonicalDBService) GetDocStore() *DocumentStoreService {
	return s.stores.DocStore
}

// GetAppPolicyStore returns the app policy store service.
func (s *CanonicalDBService) GetAppPolicyStore() *AppPolicyStoreService {
	return s.stores.AppPolicyStore
}

// GetSignerStore returns the signer store service.
func (s *CanonicalDBService) GetSignerStore() *SignerStoreService {
	return s.stores.SignerStore
}

// GetConsensusStore returns the consensus store service.
func (s *CanonicalDBService) GetConsensusStore() *ConsensusStoreService {
	return s.stores.ConsensusStore
}

// GetStateRootSvc returns the state root service.
func (s *CanonicalDBService) GetStateRootSvc() *StateRootService {
	return s.stores.StateRootSvc
}

// GetReplayStore returns the replay store service.
func (s *CanonicalDBService) GetReplayStore() *storage.ReplayStore {
	return s.stores.ReplayStore
}

// GetKVStore returns the key-value store service.
func (s *CanonicalDBService) GetKVStore() *KVStoreService {
	return s.stores.KVStore
}

// GetSSEStore returns the SSE event store service.
func (s *CanonicalDBService) GetSSEStore() *SSEEventService {
	return s.stores.SSEStore
}

// GetBlobStore returns the blob store service.
func (s *CanonicalDBService) GetBlobStore() *BlobStoreService {
	return s.stores.BlobStore
}

// GetLocalFirstEventsStore returns the local-first events store service.
func (s *CanonicalDBService) GetLocalFirstEventsStore() *storage.LocalFirstEventsStore {
	return s.stores.EventsStore
}

// GetSuspendedStore returns the suspended transaction store service.
func (s *CanonicalDBService) GetSuspendedStore() *storage.SuspendedTransactionService {
	return s.stores.SuspendedStore
}

// Close closes the database connection and waits for background workers.
func (s *CanonicalDBService) Close() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	s.cancel()
	s.mu.Unlock()

	// Wait for background workers with a timeout
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All workers finished cleanly
	case <-time.After(30 * time.Second):
		s.logger.Warn("CanonicalDBService shutdown timeout, forcing close")
	}

	if s.stores != nil && s.stores.EventsStore != nil {
		if err := s.stores.EventsStore.Close(); err != nil {
			s.logger.Error("EventsStore close error", "error", err)
		}
	}

	if s.sm != nil && s.sm.keystore != nil {
		s.sm.keystore.Close()
	}

	if err := s.db.Close(); err != nil {
		s.logger.Error("Database close error", "error", err)
		return err
	}
	return nil
}
