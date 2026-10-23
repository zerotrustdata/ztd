// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// actuatorBindingDocStore serves stored Operator documents by id. Every other
// PlatformEnrollmentDocStore method is unused by refuseActuatorKeyChange.
type actuatorBindingDocStore struct {
	PlatformEnrollmentDocStore
	docs map[string]*models.Document
}

func (s *actuatorBindingDocStore) DocGet(_ context.Context, _, id string) (*models.Document, error) {
	return s.docs[id], nil
}

// storedOperatorDocument encodes an OperatorDocument the way the document
// store holds it.
func storedOperatorDocument(t *testing.T, id, actuatorKeyHex string) *models.Document {
	t.Helper()
	raw, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
		Id:                id,
		ActuatorPublicKey: actuatorKeyHex,
		ActuatorKeyId:     actuatorKeyHex,
	})
	require.NoError(t, err)
	var data map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &data))
	return &models.Document{ID: id, Collection: "operators", Data: data}
}

func TestRefuseActuatorKeyChange(t *testing.T) {
	t.Parallel()
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	other, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	bound := hex.EncodeToString(pub)
	changed := hex.EncodeToString(other)

	t.Run("unknown operator id binds freely", func(t *testing.T) {
		t.Parallel()
		h := &PlatformEnrollmentHandler{deps: PlatformEnrollmentDeps{DocStore: &actuatorBindingDocStore{docs: map[string]*models.Document{}}}}
		assert.NoError(t, h.refuseActuatorKeyChange(t.Context(), "op-new", bound))
	})

	t.Run("same key for a bound operator is accepted", func(t *testing.T) {
		t.Parallel()
		h := &PlatformEnrollmentHandler{deps: PlatformEnrollmentDeps{DocStore: &actuatorBindingDocStore{docs: map[string]*models.Document{
			"op-1": storedOperatorDocument(t, "op-1", bound),
		}}}}
		assert.NoError(t, h.refuseActuatorKeyChange(t.Context(), "op-1", bound))
	})

	t.Run("changed key for a bound operator is refused", func(t *testing.T) {
		t.Parallel()
		h := &PlatformEnrollmentHandler{deps: PlatformEnrollmentDeps{DocStore: &actuatorBindingDocStore{docs: map[string]*models.Document{
			"op-1": storedOperatorDocument(t, "op-1", bound),
		}}}}
		err := h.refuseActuatorKeyChange(t.Context(), "op-1", changed)
		assert.ErrorIs(t, err, constants.ErrOperatorActuatorKeyChanged)
	})

	t.Run("stored document without a bound key binds freely", func(t *testing.T) {
		t.Parallel()
		h := &PlatformEnrollmentHandler{deps: PlatformEnrollmentDeps{DocStore: &actuatorBindingDocStore{docs: map[string]*models.Document{
			"op-1": storedOperatorDocument(t, "op-1", ""),
		}}}}
		assert.NoError(t, h.refuseActuatorKeyChange(t.Context(), "op-1", changed))
	})
}
