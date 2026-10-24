// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway/embedded"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestInfrastructure holds common test setup components shared across gateway tests.
type TestInfrastructure struct {
	Cfg                *config.Config
	Logger             *slog.Logger
	DB                 *CanonicalDBService
	DocStore           *DocumentStoreService
	AppPolicyStore     *AppPolicyStoreService
	SignerStore        *SignerStoreService
	ConsensusStore     *ConsensusStoreService
	StateRootSvc       *StateRootService
	ReplayStore        *storage.ReplayStore
	KVStore            *KVStoreService
	SSEStore           *SSEEventService
	BlobStore          *BlobStoreService
	EventsStore        *storage.LocalFirstEventsStore
	Pubsub             *GatewayWebSocketHandler
	SecretMgr          *SecretManager
	PKI                *PKIAuthority
	UserSvc            *UserService
	PersonaSvc         *PersonaService
	Responder          *response.Writer
	Scrubber           *scrubbing.ScrubbingService
	Auth               *AuthService
	CLISessionSvc      *CLISessionService
	OperatorSessionSvc *OperatorSessionService
	Embedded           *embedded.Service
	WebSessionSvc      *WebSessionService
	Reg                *RegistrationService
	Passkey            *PasskeyHandler
	SuspendedStore     storage.SuspendedTransactionStore
	DBDir              string
	PKIDir             string
	SecretsDir         string
}

// newTestFileSvc creates a RuntimeFileService backed by a temp directory
// with the full .ztd runtime tree created. All gateway integration tests
// should use this helper to obtain a fileSvc.
func newTestFileSvc(tb testing.TB) fs.RuntimeFileService {
	tb.Helper()
	baseDir := testutil.TempDir(tb)
	svc, err := fs.NewRuntimeFileService(baseDir, testutil.NewTestLogger())
	require.NoError(tb, err)
	require.NoError(tb, svc.CreateRuntimeTree(context.Background()))
	return svc
}

// newTestOperatorSessionService builds an OperatorSessionService over a real
// local-first events store; the constructor rejects a nil one.
func newTestOperatorSessionService(t *testing.T, docStore *DocumentStoreService, eventsStore *storage.LocalFirstEventsStore, logger *slog.Logger) *OperatorSessionService {
	t.Helper()
	svc, err := NewOperatorSessionService(docStore, eventsStore, logger)
	require.NoError(t, err)
	return svc
}

// newTestScrubber builds an enabled egress scrubber with the default configuration.
func newTestScrubber(t *testing.T) *scrubbing.ScrubbingService {
	t.Helper()
	scrubber, err := scrubbing.NewScrubbingService(context.Background(), scrubbing.DefaultConfig(), testutil.NewTestLogger(), storagetest.NewTestTokenStore())
	require.NoError(t, err)
	return scrubber
}

// testActuatorPublicKeyHex is a well-formed hex Ed25519 Actuator public key for
// Operator enrollment payloads; issuance binds it and rejects a missing key.
const testActuatorPublicKeyHex = "abababababababababababababababababababababababababababababababab"

// openTestDB wraps OpenCanonicalDBService for tests, creating a keystore
// with an in-memory keyring so callers don't need to manage a keystore.
// Vault auto-initializes on first open; the keystore is for secret operations.
func openTestDB(t *testing.T, fileSvc fs.RuntimeFileService, logger *slog.Logger) (*CanonicalDBService, error) {
	t.Helper()
	ks := newTestKeystore(t, fileSvc, logger)
	return OpenCanonicalDBService(logger, ks, fileSvc)
}

// setupTestInfrastructure creates common test infrastructure for gateway tests.
// It initializes DB, PKI, auth services, and other shared components.
func setupTestInfrastructure(t *testing.T, _ bool) *TestInfrastructure {
	t.Helper()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()

	fileSvc := newTestFileSvc(t)
	secretsDir := fileSvc.Resolve(constants.SecretsDirname)

	ks := newTestKeystore(t, fileSvc, logger)

	db, err := OpenCanonicalDBService(logger, ks, fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	pubsub := NewGatewayWebSocketHandler(logger)
	t.Cleanup(func() { pubsub.Close() })

	sm := db.GetSecretManager()

	docStore := db.GetDocStore()
	kvStore := db.GetKVStore()
	sseStore := db.GetSSEStore()

	pki := newPKIAuthority(fileSvc, docStore, sm, logger)
	err = pki.InitializePKI(nil)
	require.NoError(t, err)

	userSvc := NewUserService(docStore, logger)
	personaSvc := NewPersonaService(docStore, logger)
	resp := response.NewWriter(logger)
	scrubber := newTestScrubber(t)
	auth := NewAuthService(docStore, pki, logger, userSvc, personaSvc, resp, nil, "", "", "")
	// Wire up auth service to user service for cache invalidation
	userSvc.SetAuthService(auth)
	cliSessionSvc := NewCLISessionService(docStore, logger)
	operatorSessionSvc := newTestOperatorSessionService(t, docStore, db.GetLocalFirstEventsStore(), logger)
	embeddedOperator := embedded.New(docStore, operatorSessionSvc)
	webSessionSvc := NewWebSessionService(docStore, logger)
	reg := NewRegistrationService(docStore, kvStore, pki, logger, userSvc, cliSessionSvc, operatorSessionSvc, &cfg.Gateway)
	passkey, _ := NewPasskeyService(docStore, logger, &PasskeyConfig{RpID: "localhost", RpName: "ztd"})

	// The suspended transaction store is owned by the canonical DB.
	suspendedTxService := db.GetSuspendedStore()

	passkeyOrchestrator, err := NewPasskeyOrchestrator(nil, suspendedTxService, sseStore, pubsub, logger)
	require.NoError(t, err)
	passkeyHandler := NewPasskeyHandler(PasskeyHandlerDeps{
		Service:         passkey,
		WebSessionSvc:   webSessionSvc,
		OperatorBinder:  reg,
		OperatorClaimer: embeddedOperator,
		Responder:       resp,
		MaxPayload:      cfg.Gateway.MaxPayloadBytes,
		Orchestrator:    passkeyOrchestrator,
	})

	return &TestInfrastructure{
		Cfg:                cfg,
		Logger:             logger,
		DB:                 db,
		DocStore:           db.GetDocStore(),
		AppPolicyStore:     db.GetAppPolicyStore(),
		SignerStore:        db.GetSignerStore(),
		ConsensusStore:     db.GetConsensusStore(),
		StateRootSvc:       db.GetStateRootSvc(),
		ReplayStore:        db.GetReplayStore(),
		KVStore:            db.GetKVStore(),
		SSEStore:           db.GetSSEStore(),
		BlobStore:          db.GetBlobStore(),
		EventsStore:        db.GetLocalFirstEventsStore(),
		Pubsub:             pubsub,
		SecretMgr:          sm,
		PKI:                pki,
		UserSvc:            userSvc,
		PersonaSvc:         personaSvc,
		Responder:          resp,
		Scrubber:           scrubber,
		Auth:               auth,
		CLISessionSvc:      cliSessionSvc,
		OperatorSessionSvc: operatorSessionSvc,
		Embedded:           embeddedOperator,
		WebSessionSvc:      webSessionSvc,
		Reg:                reg,
		Passkey:            passkeyHandler,
		SuspendedStore:     suspendedTxService,
		DBDir:              fileSvc.Resolve(constants.DataDirname),
		PKIDir:             fileSvc.Resolve(constants.PkiDirname),
		SecretsDir:         secretsDir,
	}
}
