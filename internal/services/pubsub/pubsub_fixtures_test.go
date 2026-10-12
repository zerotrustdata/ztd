// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"testing"

	"crypto/ed25519"
	"crypto/rand"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/governance/governancetest"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// signL2Vote creates an L2Vote with a signature derived from the Decision field,
// preventing decision/signature mismatch bugs where the signed string and the
// Decision field diverge.
func signL2Vote(privKey ed25519.PrivateKey, keyID, hash string, decision bool) *commonv1.L2Vote {
	sig := ed25519.Sign(privKey, []byte(fmt.Sprintf("%s|%v", hash, decision)))
	return &commonv1.L2Vote{
		SignerKeyId:        keyID,
		ConsensusSignature: hex.EncodeToString(sig),
		Decision:           decision,
	}
}

// pubsubTestConsensusStoreAdapter wraps a governancetest.SimpleConsensusStore and
// adapts it to satisfy governance.L2ConsensusPolicyStore for pubsub test code.
// This is the test-only replacement for the removed production ConsensusStoreAdapter.
type pubsubTestConsensusStoreAdapter struct {
	Inner *governancetest.SimpleConsensusStore
}

func (a *pubsubTestConsensusStoreAdapter) GetConsensusPolicy(ctx context.Context, id string) (*governance.L2ConsensusPolicy, error) {
	policy, err := a.Inner.GetConsensus(ctx, id)
	if err != nil {
		return nil, err
	}
	if policy == nil {
		return nil, nil
	}
	return &governance.L2ConsensusPolicy{
		MemberKeyIDs:    policy.MemberAppIDs,
		Quorum:          policy.Quorum,
		RequireDistinct: policy.RequireDistinct,
		Enabled:         policy.Enabled,
	}, nil
}

// testConsensusStore returns an L2ConsensusPolicyStore with a single 1-of-1
// "test-consensus" policy whose sole member is "test-key". This mirrors the
// single trusted signer registered by the pubsub test fixtures so L4 quorum
// verification can pass.
func testConsensusStore() governance.L2ConsensusPolicyStore {
	return &pubsubTestConsensusStoreAdapter{Inner: &governancetest.SimpleConsensusStore{
		Consensus: map[string]*models.ConsensusPolicy{
			"test-consensus": {
				ID:              "test-consensus",
				MemberAppIDs:    []string{"test-key"},
				Quorum:          1,
				RequireDistinct: true,
				Enabled:         true,
			},
		},
	}}
}

type pubsubFixture struct {
	Cfg        *config.Config
	Logger     *slog.Logger
	DB         *pubsubtest.MockOperatorPubSubClient
	Svc        *OperatorPubSubService
	SignerPriv ed25519.PrivateKey
	SignerPub  ed25519.PublicKey
}

// testAuditBackend is the real SQLAuditStore and auditor identity an L5Actuator commits receipts
// through. The actuator fails closed without it, so every test that executes through the actuator
// attaches one over a fresh runtime root.
type testAuditBackend struct {
	Store        *storage.SQLAuditStore
	AuditorKey   ed25519.PrivateKey
	AuditorKeyID string
	// SessionID is the first pre-created session: the runtime's own audit session.
	SessionID string
}

// newTestAuditBackend opens the audit store and pre-creates the operator sessions the executed
// envelopes reference; the store rejects receipts for unknown sessions.
func newTestAuditBackend(t *testing.T, sessionIDs ...string) testAuditBackend {
	t.Helper()
	store := storagetest.NewTestAuditStore(t, sessionIDs...)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate auditor key: %v", err)
	}
	backend := testAuditBackend{Store: store, AuditorKey: priv, AuditorKeyID: hex.EncodeToString(pub)}
	if len(sessionIDs) > 0 {
		backend.SessionID = sessionIDs[0]
	}
	return backend
}

// Attach wires the backend into an actuator.
func (b testAuditBackend) Attach(a *governance.L5Actuator) {
	a.SQLAuditStore = b.Store
	a.AuditorSigningKey = b.AuditorKey
	a.AuditorKeyID = b.AuditorKeyID
	a.Identity = &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: b.SessionID}
}

func newPubsubFixture(t *testing.T) *pubsubFixture {
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	db := pubsubtest.NewMockOperatorPubSubClient()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signerStore := &governance.FailClosedSignerStore{
		Signers: map[string]ed25519.PublicKey{
			"test-key": pub,
		},
	}

	svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
		Scrubbing:          mustNewScrubbingSvc(t, testutil.NewTestLogger()),
		Ledger:             storagetest.NewTestLedger(t),
		RuntimeVault:       governancetest.UnlockedVault{},
		Config:             cfg,
		Logger:             logger,
		PubSubClient:       db,
		ActuatorSigningKey: priv,
		ActuatorKeyID:      "Actuator-key",
	}), OutboundModeDeps{
		GovernanceCoreDeps: GovernanceCoreDeps{
			ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{cfg.OperatorID, "operator-1"}, SessionID: "session-1"},
			ReplayStore:       &testutil.MockReplayStore{},
			StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
			TransactionAudit:  &testutil.MockTransactionAudit{},
			L3Notary:          &testutil.MockL3Notary{},
			SignerStore:       signerStore,
			Doctrine:          governance.NewL1Doctrine(),
		},
	})
	if err != nil {
		t.Fatalf("failed to create OperatorPubSubService: %v", err)
	}

	// Set up a mock actuator for tests that require execution
	mockHandler := &mockExecutionHandler{
		ExecuteVerifiedTransactionFunc: func(ctx context.Context, eventType constants.EventType, cmdMsg governance.CommandMessage) (string, error) {
			return "test-receipt-id", nil
		},
	}
	mockActuator := &governance.L5Actuator{
		Logger:           logger,
		Scrubbing:        mustNewScrubbingSvc(t, logger),
		ExecutionHandler: mockHandler,
		SigningKey:       priv,
		KeyID:            "Actuator-key",
	}
	newTestAuditBackend(t, "session-1").Attach(mockActuator)
	svc.SetActuator(mockActuator)

	// Default governed document store is a no-op mock; tests that exercise
	// document handlers override f.Svc.governedDocStore with a configurable
	// mock or real document store.
	svc.governedDocStore = &testutil.ConfigurableMockGovernedDocStore{}

	return &pubsubFixture{
		Cfg:        cfg,
		Logger:     logger,
		DB:         db,
		Svc:        svc,
		SignerPriv: priv,
		SignerPub:  pub,
	}
}
