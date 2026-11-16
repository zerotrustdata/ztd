// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// HostedBackend is a hosted inference provider client on an Inference
// Operator. It serves governed generation for the models its provider class
// lists and reports them as provider inventory. A hosted provider owns its own
// residency, so a HostedBackend has no load, release, pull, or copy
// operations. Every generation it returns carries an
// InferenceHostedEgressAttestation binding the provider's reported model
// version to the signed result.
type HostedBackend interface {
	Backend
	// ProviderClass is the hosted provider class it serves: one of
	// constants.HostedInferenceProviders.
	ProviderClass() string
	ListProviderModelInventory(ctx context.Context) ([]ProviderModelInventoryEntry, error)
}

// NewHostedBackends builds one client per hosted provider class the Operator
// was started with (--inference-hosted-providers). Each reads its API key from
// the process environment once, here; the key is held only by the client and
// sent only in a request header.
func NewHostedBackends(classes []string, logger *slog.Logger) (map[string]HostedBackend, error) {
	backends := make(map[string]HostedBackend, len(classes))
	for _, class := range classes {
		backend, err := NewHostedBackend(class, logger)
		if err != nil {
			return nil, err
		}
		backends[class] = backend
	}
	return backends, nil
}

// NewHostedBackend builds the client for one hosted provider class. A class
// without its API key in the environment fails closed, as does a class this
// build has no client for.
func NewHostedBackend(class string, logger *slog.Logger) (HostedBackend, error) {
	origin, ok := constants.HostedInferenceProviderOrigin(class)
	if !ok {
		return nil, fmt.Errorf("inference: hosted provider %q: %w", class, constants.ErrInferenceHostedProviderInvalid)
	}
	keyEnv, ok := constants.HostedInferenceProviderAPIKeyEnv(class)
	if !ok {
		return nil, fmt.Errorf("inference: hosted provider %q: %w", class, constants.ErrInferenceHostedProviderInvalid)
	}
	key := strings.TrimSpace(os.Getenv(string(keyEnv)))
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return nil, fmt.Errorf("inference: hosted provider %q: %s: %w", class, keyEnv, constants.ErrInferenceHostedProviderKeyMissing)
	}
	switch class {
	case constants.InferenceProviderGemini:
		return NewGeminiBackend(origin, key, logger)
	default:
		return nil, fmt.Errorf("inference: hosted provider %q: %w", class, constants.ErrInferenceHostedProviderNoClient)
	}
}
