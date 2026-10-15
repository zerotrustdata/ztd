// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package shared

import (
	"fmt"
	"log/slog"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
)

// NewFileSvc is the canonical file service factory for command RunE functions.
// It is a package-level var so tests can swap it for a mock factory.
var NewFileSvc = fs.NewRuntimeFileService

// NewEvidenceFileSvc is the file service factory for commands that read or
// write evaluation evidence. Every record under .ztd/data/eval/ is encrypted at
// rest with the runtime vault, which is opened from the runtime key store the
// first time a record is read or written; a missing or locked vault fails the
// read or write and there is no plaintext fallback. Commands that never touch
// evaluation evidence never open the vault. A restore opens a backup
// snapshot's vault the same way, from the key chain the snapshot carries.
var NewEvidenceFileSvc = EvidenceFileSvcFactory(keystore.Options{})

// EvidenceFileSvcFactory returns an evidence file service factory whose vault
// is opened from the key store opts selects; production passes zero Options
// for the OS key store.
func EvidenceFileSvcFactory(opts keystore.Options) func(baseDir string, logger *slog.Logger) (fs.RuntimeFileService, error) {
	return func(baseDir string, logger *slog.Logger) (fs.RuntimeFileService, error) {
		files, err := NewFileSvc(baseDir, logger)
		if err != nil {
			return nil, err
		}
		return evaluation.NewEvidenceFileService(files, func(keys fs.RuntimeFileService) (evaluation.EvidenceVault, error) {
			ks, err := keystore.NewWithFS(keys, logger, opts)
			if err != nil {
				return nil, fmt.Errorf("%w: open key store: %w", constants.ErrVaultKeyReadFailed, err)
			}
			return ks.OpenVault()
		})
	}
}
