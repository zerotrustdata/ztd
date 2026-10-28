// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package llamacpp

import (
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// ComposeModelDigest is the ztd model digest of a llama.cpp model from the
// SHA-256 hex digests of its shards, in shard order. A single-file model's
// digest is its file's SHA-256, so it matches a published GGUF checksum. A
// split model's digest is the SHA-256 of its shard digests joined by "\n".
// Projector and draft sidecars are not part of the model digest.
func ComposeModelDigest(shardDigests []string) (string, error) {
	if len(shardDigests) == 0 {
		return "", fmt.Errorf("llamacpp: compose model digest: %w", constants.ErrMissingRequiredField)
	}
	for index, digest := range shardDigests {
		if !models.IsSHA256Hex(digest) {
			return "", fmt.Errorf("llamacpp: compose model digest: shard %d: %w", index+1, constants.ErrInferenceEvidenceHashInvalid)
		}
	}
	if len(shardDigests) == 1 {
		return shardDigests[0], nil
	}
	return models.SHA256Hex([]byte(strings.Join(shardDigests, "\n"))), nil
}
