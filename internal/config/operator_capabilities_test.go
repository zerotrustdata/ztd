// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestStartupEnablesEveryRequestedOperatorCapabilityCombination(t *testing.T) {
	all := []constants.OperatorCapability{constants.OperatorCapabilityData, constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver}
	for _, operatorType := range []constants.OperatorType{constants.OperatorTypeEmbedded, constants.OperatorTypeRemote} {
		for mask := 1; mask < 1<<len(all); mask++ {
			capabilities := constants.OperatorCapabilities{}
			for i, capability := range all {
				if mask&(1<<i) != 0 {
					capabilities = append(capabilities, capability)
				}
			}
			t.Run(string(operatorType)+"/"+capabilities.String(), func(t *testing.T) {
				var cfg *config.Config
				var err error
				want := capabilities.Canonical()
				if operatorType == constants.OperatorTypeEmbedded {
					cfg, err = config.LoadGateway(config.GatewayOptions{OperatorCapabilities: capabilities, ProvenanceOperatorModelStorageRoot: testutil.TempDir(t)})
					want = capabilities.WithData()
				} else {
					cfg, err = config.Load(config.LoadOptions{OperatorCapabilities: capabilities, OperatorEndpoint: constants.DefaultEndpoint, WorkDir: testutil.TempDir(t), ProvenanceOperatorModelStorageRoot: testutil.TempDir(t)})
				}
				require.NoError(t, err)
				require.Equal(t, want, cfg.EffectiveOperatorCapabilities())
				require.Equal(t, capabilities.Has(constants.OperatorCapabilityInference), cfg.Inference.Enabled)
				require.Equal(t, capabilities.Has(constants.OperatorCapabilityProvenance), cfg.ProvenanceOperator.Enabled)
				require.Equal(t, capabilities.Has(constants.OperatorCapabilityObserver), cfg.ProviderBoundaryObserver.Enabled)
			})
		}
	}
}

func TestProvenanceCapabilityRequiresModelStorageRoot(t *testing.T) {
	provenance := constants.OperatorCapabilities{constants.OperatorCapabilityProvenance}
	_, err := config.LoadGateway(config.GatewayOptions{OperatorCapabilities: provenance})
	require.ErrorIs(t, err, constants.ErrProvenanceModelStorageRootRequired)
	_, err = config.Load(config.LoadOptions{OperatorCapabilities: provenance, OperatorEndpoint: constants.DefaultEndpoint, WorkDir: testutil.TempDir(t)})
	require.ErrorIs(t, err, constants.ErrProvenanceModelStorageRootRequired)
}
