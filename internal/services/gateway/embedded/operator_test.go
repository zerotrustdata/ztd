// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package embedded

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
)

// memStore mirrors the document-store rules the substrate depends on:
// DocGet returns nil for a missing row, DocSet strips id and timestamp
// keys from the body, and DocUpdate merges fields while skipping those
// same keys. Gateway integration tests still run this path against
// DocumentStoreService.
type memStore struct {
	docs map[string]map[string]map[string]json.RawMessage
}

func TestService_ExecutionTarget(t *testing.T) {
	svc := New(newMemStore(), nil)
	require.NoError(t, svc.RegisterPending(context.Background()))
	require.True(t, svc.ExecutesFor(string(constants.DocIDEmbeddedOperator)), "bootstrap can execute before the embedded record is claimed")
	require.False(t, svc.ExecutesFor("outbound-operator"))
	require.False(t, svc.ExecutesFor(""))
	require.False(t, (*Service)(nil).ExecutesFor(string(constants.DocIDEmbeddedOperator)))
}

func newMemStore() *memStore {
	return &memStore{docs: map[string]map[string]map[string]json.RawMessage{}}
}

func (s *memStore) DocGet(_ context.Context, collection, id string) (*models.Document, error) {
	col := s.docs[collection]
	if col == nil {
		return nil, nil
	}
	data, ok := col[id]
	if !ok {
		return nil, nil
	}
	return &models.Document{ID: id, Collection: collection, Data: cloneRawMap(data)}, nil
}

func (s *memStore) DocSet(_ context.Context, collection, id string, data json.RawMessage) error {
	parsed, err := decodeRawObject(data)
	if err != nil {
		return err
	}
	delete(parsed, "id")
	delete(parsed, "created_at")
	delete(parsed, "updated_at")
	if s.docs[collection] == nil {
		s.docs[collection] = map[string]map[string]json.RawMessage{}
	}
	s.docs[collection][id] = parsed
	return nil
}

func (s *memStore) DocUpdate(_ context.Context, collection, id string, fields json.RawMessage) (*models.Document, error) {
	col := s.docs[collection]
	if col == nil || col[id] == nil {
		return nil, constants.ErrNotFound
	}
	incoming, err := decodeRawObject(fields)
	if err != nil {
		return nil, err
	}
	doc := col[id]
	for k, v := range incoming {
		if k == "id" || k == "created_at" || k == "updated_at" {
			continue
		}
		var nullCheck any
		if err := json.Unmarshal(v, &nullCheck); err == nil && nullCheck == nil {
			delete(doc, k)
			continue
		}
		doc[k] = v
	}
	return &models.Document{ID: id, Collection: collection, Data: cloneRawMap(doc)}, nil
}

func (s *memStore) count(collection string) int {
	return len(s.docs[collection])
}

type persistCall struct {
	operatorSessionID string
	userID            string
	orgID             string
	operatorID        string
	loginMethod       string
}

type memOperatorSessions struct {
	calls []persistCall
	err   error
}

func (m *memOperatorSessions) PersistOperatorSession(_ context.Context, operatorSessionID, userID, orgID, operatorID, loginMethod string) error {
	m.calls = append(m.calls, persistCall{
		operatorSessionID: operatorSessionID,
		userID:            userID,
		orgID:             orgID,
		operatorID:        operatorID,
		loginMethod:       loginMethod,
	})
	return m.err
}

func decodeRawObject(data json.RawMessage) (map[string]json.RawMessage, error) {
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	if parsed == nil {
		parsed = map[string]json.RawMessage{}
	}
	return parsed, nil
}

func cloneRawMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = append(json.RawMessage(nil), v...)
	}
	return out
}

func loadOperator(t *testing.T, store *memStore) *operatorv1.OperatorDocument {
	t.Helper()
	doc, err := store.DocGet(context.Background(), marshaler.CollectionName(constants.CollectionOperators), string(constants.DocIDEmbeddedOperator))
	require.NoError(t, err)
	require.NotNil(t, doc)
	op, err := models.OperatorDocumentFromStore(doc)
	require.NoError(t, err)
	return op
}

func TestRegisterPending_Idempotent(t *testing.T) {
	store := newMemStore()
	svc := New(store, &memOperatorSessions{})

	require.NoError(t, svc.RegisterPending(context.Background()))

	op := loadOperator(t, store)
	assert.Equal(t, string(constants.DocIDEmbeddedOperator), op.Id)
	assert.Equal(t, string(constants.OperatorTypeEmbedded), op.OperatorType)
	assert.Equal(t, string(constants.ComponentNameZTDO), op.Component)
	assert.Equal(t, string(constants.OperatorStatusAvailable), op.Status)
	assert.False(t, op.Claimed)
	assert.False(t, op.IsSlot)
	assert.Empty(t, op.UserId)
	assert.Empty(t, op.OperatorSessionId)
	assert.Equal(t, 1, store.count(marshaler.CollectionName(constants.CollectionOperators)))

	require.NoError(t, svc.RegisterPending(context.Background()))
	assert.Equal(t, 1, store.count(marshaler.CollectionName(constants.CollectionOperators)))

	now := time.Now().UTC()
	_, sessionID, err := svc.Claim(context.Background(), "user-claim", "fp", now)
	require.NoError(t, err)
	require.NoError(t, svc.RegisterPending(context.Background()))
	claimed := loadOperator(t, store)
	assert.True(t, claimed.Claimed)
	assert.Equal(t, "user-claim", claimed.UserId)
	assert.Equal(t, sessionID, claimed.OperatorSessionId)
	assert.Equal(t, "fp", claimed.SystemFingerprint)
}

func TestClaim_SameUserReclaimIsIdempotent(t *testing.T) {
	store := newMemStore()
	svc := New(store, &memOperatorSessions{})
	now := time.Now().UTC()

	operatorID, sessionID, err := svc.Claim(context.Background(), "user-same", "fp", now)
	require.NoError(t, err)
	assert.Equal(t, string(constants.DocIDEmbeddedOperator), operatorID)
	assert.NotEmpty(t, sessionID)

	operatorID2, sessionID2, err := svc.Claim(context.Background(), "user-same", "fp", now)
	require.NoError(t, err)
	assert.Equal(t, operatorID, operatorID2)
	assert.Equal(t, sessionID, sessionID2)
}

func TestClaim_DifferentUserRejected(t *testing.T) {
	store := newMemStore()
	svc := New(store, &memOperatorSessions{})
	now := time.Now().UTC()

	_, _, err := svc.Claim(context.Background(), "user-owner", "fp", now)
	require.NoError(t, err)

	_, _, err = svc.Claim(context.Background(), "user-other", "fp", now)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEmbeddedOperatorClaimed)
}

func TestClaim_MissingPendingDoc_ClaimsAnyway(t *testing.T) {
	store := newMemStore()
	svc := New(store, &memOperatorSessions{})

	operatorID, sessionID, err := svc.Claim(context.Background(), "user-heal", "fp-heal", time.Now().UTC())
	require.NoError(t, err)
	assert.Equal(t, string(constants.DocIDEmbeddedOperator), operatorID)
	assert.NotEmpty(t, sessionID)

	op := loadOperator(t, store)
	assert.True(t, op.Claimed)
	assert.Equal(t, "user-heal", op.UserId)
	assert.Equal(t, sessionID, op.OperatorSessionId)
	assert.Equal(t, "fp-heal", op.SystemFingerprint)
	assert.Equal(t, string(constants.OperatorTypeEmbedded), op.OperatorType)
	assert.Equal(t, string(constants.OperatorStatusActive), op.Status)
}

func TestClaimEmbeddedOperator_PersistsBootstrapSession(t *testing.T) {
	store := newMemStore()
	operatorSessions := &memOperatorSessions{}
	svc := New(store, operatorSessions)

	operatorID, sessionID, err := svc.ClaimEmbeddedOperator(context.Background(), "user-browser")
	require.NoError(t, err)
	assert.Equal(t, string(constants.DocIDEmbeddedOperator), operatorID)
	require.Len(t, operatorSessions.calls, 1)
	assert.Equal(t, persistCall{
		operatorSessionID: sessionID,
		userID:            "user-browser",
		orgID:             "user-browser",
		operatorID:        operatorID,
		loginMethod:       string(constants.HeartbeatTypeBootstrap),
	}, operatorSessions.calls[0])

	operatorID2, sessionID2, err := svc.ClaimEmbeddedOperator(context.Background(), "user-browser")
	require.NoError(t, err)
	assert.Equal(t, operatorID, operatorID2)
	assert.Equal(t, sessionID, sessionID2)
	require.Len(t, operatorSessions.calls, 2)
	assert.Equal(t, operatorSessions.calls[0], operatorSessions.calls[1])

	_, _, err = svc.ClaimEmbeddedOperator(context.Background(), "user-other")
	require.ErrorIs(t, err, constants.ErrEmbeddedOperatorClaimed)
	assert.Len(t, operatorSessions.calls, 2)
}

func TestService_OperatorSessionID(t *testing.T) {
	ctx := context.Background()
	opsCollection := marshaler.CollectionName(constants.CollectionOperators)

	t.Run("claimed operator returns its session", func(t *testing.T) {
		svc := New(newMemStore(), &memOperatorSessions{})
		require.NoError(t, svc.RegisterPending(ctx))
		_, want, err := svc.ClaimEmbeddedOperator(ctx, "user-1")
		require.NoError(t, err)

		got, err := svc.OperatorSessionID(ctx)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("pending operator is unclaimed", func(t *testing.T) {
		svc := New(newMemStore(), nil)
		require.NoError(t, svc.RegisterPending(ctx))

		got, err := svc.OperatorSessionID(ctx)
		require.ErrorIs(t, err, constants.ErrEmbeddedOperatorUnclaimed)
		assert.Empty(t, got)
	})

	t.Run("missing document is unclaimed and is not created", func(t *testing.T) {
		store := newMemStore()
		svc := New(store, nil)

		got, err := svc.OperatorSessionID(ctx)
		require.ErrorIs(t, err, constants.ErrEmbeddedOperatorUnclaimed)
		assert.Empty(t, got)
		assert.Zero(t, store.count(opsCollection), "OperatorSessionID must not write the pending document")
	})
}

func TestService_RuntimeIdentity(t *testing.T) {
	ctx := context.Background()
	svc := New(newMemStore(), nil)
	require.NoError(t, svc.RegisterPending(ctx))
	_, err := svc.OperatorSessionID(ctx)
	require.ErrorIs(t, err, constants.ErrEmbeddedOperatorUnclaimed, "an unclaimed Operator has no operator session")
	identity, err := svc.RuntimeIdentity(ctx)
	require.NoError(t, err)
	require.Equal(t, string(constants.DocIDEmbeddedOperator), identity.OperatorID)
	require.Equal(t, constants.EmbeddedOperatorBootstrapSessionID, identity.SessionID, "before the claim the Operator executes under the Gateway's bootstrap audit session")

	operatorID, sessionID, err := svc.Claim(ctx, "user-1", "", time.Now().UTC())
	require.NoError(t, err)
	identity, err = svc.RuntimeIdentity(ctx)
	require.NoError(t, err)
	require.Equal(t, operatorID, identity.OperatorID)
	require.Equal(t, sessionID, identity.SessionID)
	require.Equal(t, string(constants.DocIDEmbeddedOperator), identity.OperatorID)
}
