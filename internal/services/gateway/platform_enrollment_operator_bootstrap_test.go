// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
)

func TestOperatorBootstrapRuntimeConfig_KeepsDeclaredHeartbeatInterval(t *testing.T) {
	cfg, err := operatorBootstrapRuntimeConfig(&models.OperatorBootstrapRequest{
		RuntimeConfig: json.RawMessage(`{"heartbeat_interval_ms":120000,"capabilities":["inference"]}`),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 120000, cfg.GetHeartbeatIntervalMs())
	assert.True(t, operatorcapability.ResolveOperatorCapabilities(cfg).Has(constants.OperatorCapabilityInference))
}

func TestOperatorBootstrapRuntimeConfig_AcceptsTheMaximumInterval(t *testing.T) {
	cfg, err := operatorBootstrapRuntimeConfig(&models.OperatorBootstrapRequest{
		RuntimeConfig: json.RawMessage(`{"heartbeat_interval_ms":300000}`),
	})
	require.NoError(t, err)
	assert.EqualValues(t, constants.OperatorHeartbeatMaxInterval.Milliseconds(), cfg.GetHeartbeatIntervalMs())
}

func TestOperatorBootstrapRuntimeConfig_RejectsIntervalPastMaximum(t *testing.T) {
	_, err := operatorBootstrapRuntimeConfig(&models.OperatorBootstrapRequest{
		RuntimeConfig: json.RawMessage(`{"heartbeat_interval_ms":300001}`),
	})
	require.ErrorIs(t, err, constants.ErrOperatorHeartbeatIntervalInvalid)
}

func TestOperatorBootstrapRuntimeConfig_RejectsUndecodableConfig(t *testing.T) {
	_, err := operatorBootstrapRuntimeConfig(&models.OperatorBootstrapRequest{
		RuntimeConfig: json.RawMessage(`{"heartbeat_interval_ms":"soon"}`),
	})
	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentInvalidPayload)
}

func TestOperatorBootstrapRuntimeConfig_KeepsDiscoveredHostedProviders(t *testing.T) {
	cfg, err := operatorBootstrapRuntimeConfig(&models.OperatorBootstrapRequest{
		RuntimeConfig: json.RawMessage(`{"capabilities":["inference"],"inference_hosted_providers":["gemini","openai"]}`),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{constants.InferenceProviderGemini, constants.InferenceProviderOpenAI}, cfg.GetInferenceHostedProviders())
}

func TestOperatorBootstrapRuntimeConfig_RejectsHostedProvidersTheOperatorCannotServe(t *testing.T) {
	for name, tc := range map[string]struct {
		config string
		want   error
	}{
		"no inference capability": {`{"capabilities":["data"],"inference_hosted_providers":["gemini"]}`, constants.ErrInferenceHostedRequiresInference},
		"default data capability": {`{"inference_hosted_providers":["gemini"]}`, constants.ErrInferenceHostedRequiresInference},
		"a local class":           {`{"capabilities":["inference"],"inference_hosted_providers":["ollama"]}`, constants.ErrInferenceHostedProviderInvalid},
		"an unknown class":        {`{"capabilities":["inference"],"inference_hosted_providers":["acme"]}`, constants.ErrInferenceHostedProviderInvalid},
		"a repeated class":        {`{"capabilities":["inference"],"inference_hosted_providers":["gemini","gemini"]}`, constants.ErrInferenceHostedProviderInvalid},
		"out of canonical order":  {`{"capabilities":["inference"],"inference_hosted_providers":["openai","gemini"]}`, constants.ErrInferenceHostedProviderInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := operatorBootstrapRuntimeConfig(&models.OperatorBootstrapRequest{RuntimeConfig: json.RawMessage(tc.config)})
			require.ErrorIs(t, err, constants.ErrPlatformEnrollmentInvalidPayload)
			require.ErrorIs(t, err, tc.want)
		})
	}
}
