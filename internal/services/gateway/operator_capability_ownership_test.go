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

func TestOperatorRuntimeConfigAllowsManyOperatorsWithAllCapabilities(t *testing.T) {
	allCapabilities := []string{
		string(constants.OperatorCapabilityData),
		string(constants.OperatorCapabilityInference),
		string(constants.OperatorCapabilityProvenance),
		string(constants.OperatorCapabilityObserver),
	}
	store := newOperatorOwnershipStore(t)
	reg := &RegistrationService{docStore: store}
	for _, id := range []string{string(constants.DocIDEmbeddedOperator), "remote-1", "remote-2", "remote-3"} {
		kind := constants.OperatorTypeRemote
		if id == string(constants.DocIDEmbeddedOperator) {
			kind = constants.OperatorTypeEmbedded
		}
		raw, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
			Id:                id,
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(kind),
			OperatorSessionId: id + "-session",
			Claimed:           true,
		})
		require.NoError(t, err)
		require.NoError(t, store.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), id, raw))
		require.NoError(t, reg.UpdateOperatorRuntimeConfig(t.Context(), id, &operatorv1.OperatorRuntimeConfig{Capabilities: allCapabilities}))
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

func TestRegisterOperatorManyOperatorsWithMixedAndFullCapabilities(t *testing.T) {
	store := newOperatorOwnershipStore(t)
	require.NoError(t, store.SetOperatorRuntimeConfig(t.Context(), string(constants.DocIDEmbeddedOperator), &operatorv1.OperatorRuntimeConfig{
		Capabilities: []string{"data", "inference", "provenance", "observer"},
	}))
	all := []constants.OperatorCapability{
		constants.OperatorCapabilityData,
		constants.OperatorCapabilityInference,
		constants.OperatorCapabilityProvenance,
		constants.OperatorCapabilityObserver,
	}
	key := 1
	for mask := 1; mask < 1<<len(all); mask++ {
		var caps []constants.OperatorCapability
		for i, capability := range all {
			if mask&(1<<i) != 0 {
				caps = append(caps, capability)
			}
		}
		op := ownershipOperator(fmt.Sprintf("op-mask-%d", mask), key, caps...)
		require.NoError(t, store.RegisterOperator(t.Context(), op))
		key++
	}
	for i := 0; i < 6; i++ {
		op := ownershipOperator(fmt.Sprintf("full-%d", i), key, all...)
		require.NoError(t, store.RegisterOperator(t.Context(), op))
		key++
	}
}

func TestRegisterOperatorConcurrentAllCapabilities(t *testing.T) {
	store := newOperatorOwnershipStore(t)
	const workers = 8
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results <- store.RegisterOperator(t.Context(), ownershipOperator(
				fmt.Sprintf("op-%d", i),
				i+1,
				constants.OperatorCapabilityData,
				constants.OperatorCapabilityInference,
				constants.OperatorCapabilityProvenance,
				constants.OperatorCapabilityObserver,
			))
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
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

func TestOperatorClaimsAllowCapabilityChangeReenrollmentAndCoexistingOperators(t *testing.T) {
	store := newOperatorOwnershipStore(t)
	first := ownershipOperator("witness", 1, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver)
	first.SystemFingerprint = "fingerprint-with-capabilities"
	first.Status = string(constants.OperatorStatusStale)
	require.NoError(t, store.RegisterOperator(t.Context(), first))
	blended := ownershipOperator("blended", 1, constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver)
	blended.SystemFingerprint = "fingerprint-by-location"
	require.NoError(t, store.RegisterOperator(t.Context(), blended))

	elsewhere := ownershipOperator("elsewhere", 2, constants.OperatorCapabilityData, constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver)
	require.NoError(t, store.RegisterOperator(t.Context(), elsewhere))
}

func TestOperatorActuatorKeyClaimReservesAcrossOwnersUntilTermination(t *testing.T) {
	store := newOperatorOwnershipStore(t)
	first := ownershipOperator("first", 1, constants.OperatorCapabilityInference)
	first.Status = string(constants.OperatorStatusStopped)
	require.NoError(t, store.RegisterOperator(t.Context(), first))
	next := ownershipOperator("next", 1, constants.OperatorCapabilityInference)
	next.UserId = "another-owner"
	require.ErrorIs(t, store.RegisterOperator(t.Context(), next), constants.ErrOperatorActuatorKeyInUse)
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
