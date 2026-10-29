// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestOperatorRuntimeConfigRejectsDuplicateSingletonCapability(t *testing.T) {
	for _, capability := range []constants.OperatorCapability{constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver} {
		t.Run(string(capability), func(t *testing.T) {
			store := newOperatorOwnershipStore(t)
			reg := &RegistrationService{docStore: store}
			for _, id := range []string{string(constants.DocIDEmbeddedOperator), "remote"} {
				kind := constants.OperatorTypeRemote
				if id == string(constants.DocIDEmbeddedOperator) {
					kind = constants.OperatorTypeEmbedded
				}
				raw, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{Id: id, Status: string(constants.OperatorStatusActive), OperatorType: string(kind), OperatorSessionId: id + "-session", Claimed: true})
				require.NoError(t, err)
				require.NoError(t, store.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), id, raw))
			}
			cfg := &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(capability)}}
			require.NoError(t, reg.UpdateOperatorRuntimeConfig(t.Context(), string(constants.DocIDEmbeddedOperator), cfg))
			err := reg.UpdateOperatorRuntimeConfig(t.Context(), "remote", cfg)
			require.ErrorIs(t, err, constants.ErrOperatorCapabilityInUse)
		})
	}
}

func newOperatorOwnershipStore(t *testing.T) *DocumentStoreService {
	t.Helper()
	store := newDocumentStoreService(t)
	raw, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
		Id: string(constants.DocIDEmbeddedOperator), OperatorType: string(constants.OperatorTypeEmbedded),
		Status: string(constants.OperatorStatusAvailable), RuntimeConfig: &operatorv1.OperatorRuntimeConfig{},
	})
	require.NoError(t, err)
	require.NoError(t, store.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), string(constants.DocIDEmbeddedOperator), raw))
	return store
}

func ownershipOperator(id string, key int, capabilities ...constants.OperatorCapability) *operatorv1.OperatorDocument {
	return &operatorv1.OperatorDocument{
		Id: id, UserId: "owner", OperatorType: string(constants.OperatorTypeRemote),
		OperatorSessionId: id + "-session", Status: string(constants.OperatorStatusOffline), Claimed: true,
		ActuatorKeyId: fmt.Sprintf("%064x", key), ActuatorPublicKey: fmt.Sprintf("%064x", key),
		OperatorCapabilities: models.OperatorCapabilitiesToProto(constants.OperatorCapabilities(capabilities).WithDefault()),
	}
}

func TestRegisterOperatorCombinedWitnessAndManyDataOperators(t *testing.T) {
	store := newOperatorOwnershipStore(t)
	witness := ownershipOperator("beepboop", 1, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver)
	require.NoError(t, store.RegisterOperator(t.Context(), witness))
	for i := 0; i < 4; i++ {
		require.NoError(t, store.RegisterOperator(t.Context(), ownershipOperator(fmt.Sprintf("data-%d", i), i+2)))
	}
	raw, err := store.DocGet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), witness.GetId())
	require.NoError(t, err)
	saved, err := models.OperatorDocumentFromStore(raw)
	require.NoError(t, err)
	require.Equal(t, []string{"provenance", "observer"}, saved.GetOperatorCapabilities())
}

func TestRegisterOperatorConcurrentSingletonOwnership(t *testing.T) {
	for _, capability := range []constants.OperatorCapability{constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver} {
		t.Run(string(capability), func(t *testing.T) {
			store := newOperatorOwnershipStore(t)
			const workers = 6
			start := make(chan struct{})
			results := make(chan error, workers)
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					results <- store.RegisterOperator(t.Context(), ownershipOperator(fmt.Sprintf("op-%d", i), i+1, capability))
				}(i)
			}
			close(start)
			wg.Wait()
			close(results)
			successes := 0
			for err := range results {
				if err == nil {
					successes++
				} else {
					require.ErrorIs(t, err, constants.ErrOperatorCapabilityInUse)
				}
			}
			require.Equal(t, 1, successes)
		})
	}
}

func TestRegisterOperatorRejectsSharedActuatorAcrossOwners(t *testing.T) {
	store := newOperatorOwnershipStore(t)
	require.NoError(t, store.RegisterOperator(t.Context(), ownershipOperator("provenance", 1, constants.OperatorCapabilityProvenance)))
	other := ownershipOperator("observer", 1, constants.OperatorCapabilityObserver)
	other.UserId = "another-owner"
	err := store.RegisterOperator(t.Context(), other)
	require.ErrorIs(t, err, constants.ErrOperatorActuatorKeyInUse)
	require.ErrorContains(t, err, "provenance")
	require.ErrorContains(t, err, "observer")
}

// A runtime that re-enrolls with an added capability keeps its working
// directory and Actuator key. Its prior lease, even one whose fingerprint was
// computed by an older build that hashed capabilities, is the same Operator:
// the replacement takes over its claims, and other runtimes still cannot.
func TestOperatorClaimsAllowCapabilityChangeReenrollment(t *testing.T) {
	store := newOperatorOwnershipStore(t)
	first := ownershipOperator("witness", 1, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver)
	first.SystemFingerprint = "fingerprint-with-capabilities"
	first.Status = string(constants.OperatorStatusStale)
	require.NoError(t, store.RegisterOperator(t.Context(), first))
	blended := ownershipOperator("blended", 1, constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver)
	blended.SystemFingerprint = "fingerprint-by-location"
	require.NoError(t, store.RegisterOperator(t.Context(), blended))

	elsewhere := ownershipOperator("elsewhere", 2, constants.OperatorCapabilityProvenance)
	require.ErrorIs(t, store.RegisterOperator(t.Context(), elsewhere), constants.ErrOperatorCapabilityInUse)
}

func TestOperatorClaimsReserveAcrossOwnersUntilTermination(t *testing.T) {
	store := newOperatorOwnershipStore(t)
	first := ownershipOperator("first", 1, constants.OperatorCapabilityInference)
	first.Status = string(constants.OperatorStatusStopped)
	require.NoError(t, store.RegisterOperator(t.Context(), first))
	next := ownershipOperator("next", 2, constants.OperatorCapabilityInference)
	next.UserId = "another-owner"
	require.ErrorIs(t, store.RegisterOperator(t.Context(), next), constants.ErrOperatorCapabilityInUse)
	_, err := store.DocUpdate(t.Context(), marshaler.CollectionName(constants.CollectionOperators), first.GetId(), json.RawMessage(`{"status":"terminated"}`))
	require.NoError(t, err)
	require.NoError(t, store.RegisterOperator(t.Context(), next))
}

func TestOperatorClaimsAllowSameFingerprintReenrollment(t *testing.T) {
	store := newOperatorOwnershipStore(t)
	first := ownershipOperator("old", 1, constants.OperatorCapabilityInference)
	first.SystemFingerprint = "same-runtime"
	require.NoError(t, store.RegisterOperator(t.Context(), first))
	replacement := ownershipOperator("replacement", 1, constants.OperatorCapabilityInference)
	replacement.SystemFingerprint = first.SystemFingerprint
	require.NoError(t, store.RegisterOperator(t.Context(), replacement))
}

func TestRegisterOperatorRequiresEmbeddedStartupRegistration(t *testing.T) {
	store := newDocumentStoreService(t)
	require.ErrorIs(t, store.RegisterOperator(t.Context(), ownershipOperator("remote", 1)), constants.ErrEmbeddedOperatorNotReady)
	raw, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{Id: string(constants.DocIDEmbeddedOperator), OperatorType: string(constants.OperatorTypeEmbedded)})
	require.NoError(t, err)
	require.NoError(t, store.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), string(constants.DocIDEmbeddedOperator), raw))
	require.ErrorIs(t, store.RegisterOperator(t.Context(), ownershipOperator("remote", 1)), constants.ErrEmbeddedOperatorNotReady)
}

func TestPendingEmbeddedOperatorReservesCapabilitiesBeforeFirstUser(t *testing.T) {
	store := newOperatorOwnershipStore(t)
	require.NoError(t, store.SetOperatorRuntimeConfig(t.Context(), string(constants.DocIDEmbeddedOperator), &operatorv1.OperatorRuntimeConfig{Capabilities: []string{"inference", "observer", "provenance"}}))
	for i, capability := range []constants.OperatorCapability{constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver} {
		require.ErrorIs(t, store.RegisterOperator(t.Context(), ownershipOperator(fmt.Sprintf("remote-%d", i), i+1, capability)), constants.ErrOperatorCapabilityInUse)
	}
	require.NoError(t, store.RegisterOperator(t.Context(), ownershipOperator("data", 5)))
}
