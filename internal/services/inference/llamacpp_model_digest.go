// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"errors"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/llamacpp"
)

// llamaCppModelDigester resolves GGUF shards and composes their guarded file hashes.
type llamaCppModelDigester struct {
	modelRoot string
	*modelFileDigester
}

func newLlamaCppModelDigester(modelRoot string) *llamaCppModelDigester {
	return &llamaCppModelDigester{modelRoot: modelRoot, modelFileDigester: newModelFileDigester("llamacpp_backend")}
}

// modelDigest resolves router model id beneath the model root and returns the
// resolved model with its ztd model digest.
func (d *llamaCppModelDigester) modelDigest(ctx context.Context, id string) (llamacpp.Model, string, error) {
	model, err := llamacpp.ResolveModel(d.modelRoot, id)
	if err != nil {
		if errors.Is(err, constants.ErrLlamaCppModelNotFound) {
			return llamacpp.Model{}, "", fmt.Errorf("llamacpp_backend: model digest: %w: %w", constants.ErrInferenceModelNotFound, err)
		}
		return llamacpp.Model{}, "", fmt.Errorf("llamacpp_backend: model digest: %w", err)
	}
	shardDigests := make([]string, 0, len(model.Shards))
	for _, shard := range model.Shards {
		digest, err := d.fileDigest(ctx, shard)
		if err != nil {
			return llamacpp.Model{}, "", err
		}
		shardDigests = append(shardDigests, digest)
	}
	digest, err := llamacpp.ComposeModelDigest(shardDigests)
	if err != nil {
		return llamacpp.Model{}, "", fmt.Errorf("llamacpp_backend: model digest: %w", err)
	}
	return model, digest, nil
}
