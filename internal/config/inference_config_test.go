// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestNewInferenceConfig_AppliesTypedDefaults(t *testing.T) {
	got, err := newInferenceConfig(true, LoadOptions{})
	require.NoError(t, err)

	assert.True(t, got.Enabled)
	assert.Equal(t, constants.InferenceProviderOllama, got.Provider)
	assert.NotEmpty(t, got.OllamaEndpoint)
	assert.Equal(t, constants.InferenceDefaultKeepAlive, got.KeepAlive)
}

func TestNewInferenceConfig_ExplicitFlagsOverrideDefaults(t *testing.T) {
	got, err := newInferenceConfig(true, LoadOptions{InferenceKeepAlive: "5m"})
	require.NoError(t, err)

	assert.Equal(t, "5m", got.KeepAlive)
}

func TestNewInferenceConfig_LlamaCppEndpointSelectsProvider(t *testing.T) {
	got, err := newInferenceConfig(true, LoadOptions{InferenceLlamaCppEndpoint: "http://127.0.0.1:8081", InferenceLlamaCppModelRoot: "/models"})
	require.NoError(t, err)

	assert.Equal(t, constants.InferenceProviderLlamaCpp, got.Provider)
	assert.Equal(t, "http://127.0.0.1:8081", got.LlamaCppEndpoint)
	assert.Equal(t, "/models", got.LlamaCppModelRoot)
	assert.Empty(t, got.OllamaEndpoint)
}

func TestNewInferenceConfig_LlamaCppFailsClosed(t *testing.T) {
	llamacpp := LoadOptions{InferenceLlamaCppEndpoint: "http://127.0.0.1:8081", InferenceLlamaCppModelRoot: "/models"}

	conflict := llamacpp
	conflict.InferenceOllamaEndpoint = "http://127.0.0.1:11434"
	_, err := newInferenceConfig(true, conflict)
	require.ErrorIs(t, err, constants.ErrInferenceProviderConflict)

	noRoot := llamacpp
	noRoot.InferenceLlamaCppModelRoot = ""
	_, err = newInferenceConfig(true, noRoot)
	require.ErrorIs(t, err, constants.ErrInferenceLlamaCppModelRootRequired)

	duration := llamacpp
	duration.InferenceKeepAlive = "5m"
	_, err = newInferenceConfig(true, duration)
	require.ErrorIs(t, err, constants.ErrInferenceLlamaCppKeepAliveInvalid)

	unload := llamacpp
	unload.InferenceKeepAlive = "0"
	_, err = newInferenceConfig(true, unload)
	require.NoError(t, err)
}

func TestNewProvenanceOperatorConfig_ModelStorageFormat(t *testing.T) {
	got, err := newProvenanceOperatorConfig(true, LoadOptions{ProvenanceOperatorModelStorageRoot: "/models"})
	require.NoError(t, err)
	assert.Equal(t, constants.ModelStorageFormatOllama, got.ModelStorageFormat)

	got, err = newProvenanceOperatorConfig(true, LoadOptions{ProvenanceOperatorModelStorageRoot: "/models", ProvenanceOperatorModelStorageFormat: constants.ModelStorageFormatLlamaCpp})
	require.NoError(t, err)
	assert.Equal(t, constants.ModelStorageFormatLlamaCpp, got.ModelStorageFormat)

	_, err = newProvenanceOperatorConfig(true, LoadOptions{ProvenanceOperatorModelStorageRoot: "/models", ProvenanceOperatorModelStorageFormat: "safetensors"})
	require.ErrorIs(t, err, constants.ErrProvenanceModelStorageFormatInvalid)
}

func TestNewInferenceConfig_HostedProvidersComeFromKeys(t *testing.T) {
	setKeys := func(t *testing.T, gemini, anthropic, openai string) {
		t.Helper()
		t.Setenv(string(constants.EnvVar.GeminiAPIKey), gemini)
		t.Setenv(string(constants.EnvVar.AnthropicAPIKey), anthropic)
		t.Setenv(string(constants.EnvVar.OpenAIAPIKey), openai)
	}

	t.Run("each class with a key is served, in canonical order", func(t *testing.T) {
		setKeys(t, "g-key", "", " o-key ")
		got, err := newInferenceConfig(true, LoadOptions{})
		require.NoError(t, err)
		assert.Equal(t, []string{constants.InferenceProviderGemini, constants.InferenceProviderOpenAI}, got.HostedProviders)
	})
	t.Run("no key serves no hosted class", func(t *testing.T) {
		setKeys(t, "", "", "")
		got, err := newInferenceConfig(true, LoadOptions{})
		require.NoError(t, err)
		assert.Empty(t, got.HostedProviders)
	})
	t.Run("without the inference capability keys are ignored", func(t *testing.T) {
		setKeys(t, "g-key", "a-key", "\n")
		got, err := newInferenceConfig(false, LoadOptions{})
		require.NoError(t, err)
		assert.Empty(t, got.HostedProviders)
	})
	for name, key := range map[string]string{
		"a whitespace-only key": "   ",
		"a key split by LF":     "abc\ndef",
		"a key ending in CR":    "abc\r",
	} {
		t.Run(name+" fails startup", func(t *testing.T) {
			setKeys(t, "", key, "")
			_, err := newInferenceConfig(true, LoadOptions{})
			require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyInvalid)
			assert.Contains(t, err.Error(), string(constants.EnvVar.AnthropicAPIKey), "the error names the variable")
			assert.NotContains(t, err.Error(), "abc", "the error never carries the value")
		})
	}
}

func TestHostedProviderAPIKey(t *testing.T) {
	t.Setenv(string(constants.EnvVar.GeminiAPIKey), "  g-key  ")
	key, err := HostedProviderAPIKey(constants.InferenceProviderGemini)
	require.NoError(t, err)
	assert.Equal(t, "g-key", key, "the key is trimmed")

	t.Setenv(string(constants.EnvVar.GeminiAPIKey), "")
	_, err = HostedProviderAPIKey(constants.InferenceProviderGemini)
	require.ErrorIs(t, err, constants.ErrInferenceHostedProviderKeyMissing)

	for _, class := range []string{constants.InferenceProviderOllama, constants.InferenceProviderLlamaCpp, "acme", ""} {
		_, err = HostedProviderAPIKey(class)
		require.ErrorIs(t, err, constants.ErrInferenceHostedProviderInvalid, class)
	}
}
