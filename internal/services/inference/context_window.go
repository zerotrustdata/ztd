// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// loadedModel is a served model as the provider holds it once loaded. Its
// context window is the one bound on how much a generation may produce: the
// provider reports it for the loaded runner (Ollama /api/ps context_length,
// llama-server /props n_ctx), so it is whatever window the provider loaded the
// model with, never a platform constant and never the model's advertised
// training length.
type loadedModel struct {
	// ContextWindow is the loaded runner's context length in tokens.
	ContextWindow int32
	// LoadDurationNS is the wall time of the load the provider performed for
	// this request, zero when the model was already resident with this window,
	// and nil when the provider cannot say.
	LoadDurationNS *int64
}

// newLoadedModel validates a provider-reported context window.
func newLoadedModel(component, model string, contextWindow int64, loadDurationNS *int64) (loadedModel, error) {
	if contextWindow <= 0 || contextWindow > maxInferenceContextLimit {
		return loadedModel{}, fmt.Errorf("%s: load model %q: %w: %w", component, model, constants.ErrInferenceContextWindowUnavailable, constants.ErrInferenceProviderResponseInvalid)
	}
	return loadedModel{ContextWindow: int32(contextWindow), LoadDurationNS: loadDurationNS}, nil
}

// outputTokenLimit is the output budget a generation runs under: the caller's
// stated limit when it is smaller than the loaded context window, otherwise
// the window itself. A request that states no limit (0) runs until the model
// stops or fills its window.
func outputTokenLimit(requested int32, model loadedModel) int32 {
	if requested > 0 && requested < model.ContextWindow {
		return requested
	}
	return model.ContextWindow
}

// sumLoadDurations adds the load the backend performed before generation to
// the load the provider reported for the generation itself. Either part
// unknown leaves the total unknown.
func sumLoadDurations(model loadedModel, generation *int64) *int64 {
	if model.LoadDurationNS == nil || generation == nil {
		return nil
	}
	total := *model.LoadDurationNS + *generation
	return &total
}
