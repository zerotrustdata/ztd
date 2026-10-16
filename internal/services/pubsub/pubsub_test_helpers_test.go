// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/config"
	execution "github.com/zerotrustdata/ztd/internal/services/execution"
	"github.com/zerotrustdata/ztd/internal/services/governance/governancetest"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// mustMarshalProto marshals a protobuf message to bytes, fatally failing the test on error.
func mustMarshalProto(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	data, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal proto: %v", err)
	}
	return data
}

func mustNewStruct(t *testing.T, documentJSON string) *structpb.Struct {
	t.Helper()
	var document structpb.Struct
	if err := protojson.Unmarshal([]byte(documentJSON), &document); err != nil {
		t.Fatalf("failed to unmarshal document JSON: %v", err)
	}
	return &document
}

// mustNewScrubbingSvc creates a scrubbing service for tests, fatally failing on error.
func mustNewScrubbingSvc(t *testing.T, logger *slog.Logger) *scrubbing.ScrubbingService {
	t.Helper()
	svc, err := scrubbing.NewScrubbingService(context.Background(), scrubbing.DefaultConfig(), logger, storagetest.NewTestTokenStore())
	require.NoError(t, err)
	return svc
}

// newTestCommandService builds a CommandService over a real audit store. results is the
// publisher the service reports to.
func newTestCommandService(t *testing.T, cfg *config.Config, logger *slog.Logger, results ResultsPublisher) *CommandService {
	t.Helper()
	svc, err := NewCommandService(cfg, logger, execution.NewExecutionService(cfg, logger), results, storagetest.NewTestAuditStore(t))
	require.NoError(t, err)
	return svc
}

// newTestHistoryHandler builds a HistoryHandler over a real file ledger and the given real audit store.
func newTestHistoryHandler(t *testing.T, logger *slog.Logger, auditStore *storage.SQLAuditStore) *storage.HistoryHandler {
	t.Helper()
	handler, err := storage.NewHistoryHandler(auditStore, storagetest.NewTestLedger(t), logger, nil, "")
	require.NoError(t, err)
	return handler
}

// newTestHistoryService builds a HistoryService over a real history handler, an active scrubber and a
// real audit store.
// testOperatorSessionID is the audit session the default file-ops and history fixtures create, so
// handlers that record their own execution event can write it.
const testOperatorSessionID = "session-pubsub-test"

func newTestHistoryService(t *testing.T, cfg *config.Config, logger *slog.Logger, client PubSubClient) *HistoryService {
	t.Helper()
	auditStore := storagetest.NewTestAuditStore(t, testOperatorSessionID)
	svc, err := NewHistoryService(cfg, logger, client, newTestHistoryHandler(t, logger, auditStore), mustNewScrubbingSvc(t, logger), auditStore)
	require.NoError(t, err)
	return svc
}

// newTestPortService builds a PortService over an active scrubber and a real audit store.
func newTestPortService(t *testing.T, cfg *config.Config, logger *slog.Logger, client PubSubClient) *PortService {
	t.Helper()
	svc, err := NewPortService(cfg, logger, client, mustNewScrubbingSvc(t, logger), storagetest.NewTestAuditStore(t))
	require.NoError(t, err)
	return svc
}

// withTestDeps fills every required collaborator c leaves unset with a real one: the execution and
// file edit services, a real audit store with the history handler over it, a real file ledger, an
// active scrubber, an unlocked runtime vault and a mock results publisher. A field the test sets
// explicitly is kept, so a test overrides only the collaborator it exercises.
func withTestDeps(t *testing.T, c CommandServiceConfig) CommandServiceConfig {
	t.Helper()
	if c.Execution == nil {
		c.Execution = execution.NewExecutionService(c.Config, c.Logger)
	}
	if c.FileEdit == nil {
		c.FileEdit = newFileEditTestService(t, c.Config, c.Logger)
	}
	if c.ResultsService == nil {
		c.ResultsService = &mockResultsPublisher{}
	}
	if c.AuditStore == nil {
		c.AuditStore = storagetest.NewTestAuditStore(t)
	}
	if c.Ledger == nil {
		c.Ledger = storagetest.NewTestLedger(t)
	}
	if c.HistoryHandler == nil {
		c.HistoryHandler = newTestHistoryHandler(t, c.Logger, c.AuditStore)
	}
	if c.Scrubbing == nil {
		c.Scrubbing = mustNewScrubbingSvc(t, c.Logger)
	}
	if c.RuntimeVault == nil {
		c.RuntimeVault = governancetest.UnlockedVault{}
	}
	return c
}
