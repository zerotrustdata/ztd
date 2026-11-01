// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcapability

import (
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// InferenceProvider returns the provider class and approved endpoint an
// Inference Operator reported in its runtime config. An Operator serves exactly
// one provider: a reported llama.cpp endpoint selects llama.cpp, otherwise the
// provider is Ollama. The endpoint is blank when none was reported.
func InferenceProvider(cfg *operatorv1.OperatorRuntimeConfig) (provider, endpoint string) {
	if llamaCpp := strings.TrimSpace(cfg.GetInferenceLlamacppEndpoint()); llamaCpp != "" {
		return constants.InferenceProviderLlamaCpp, llamaCpp
	}
	return constants.InferenceProviderOllama, strings.TrimSpace(cfg.GetInferenceOllamaEndpoint())
}
