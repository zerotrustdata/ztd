// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// CreateRuntimeTree creates the full .ztd/ directory tree with correct
// permissions. Called once at startup. Idempotent.
//
// Every directory that holds data or private keys (data, ledger, logs, pids,
// bin, secrets, vault, and the PKI directories that hold or parent private keys) is
// PermDirPrivate (0700): the runtime root is private, but --data-dir can place
// a data directory outside it. Only the PKI directories that hold nothing but
// public certificates and revocation lists (trust, revocation, binaries)
// use PermDirStandard (0755).
func (fs *localFS) CreateRuntimeTree(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	pkiDir := filepath.Join(fs.runtimeDir, constants.PkiDirname)

	dirs := []struct {
		path string
		mode os.FileMode
	}{
		{fs.runtimeDir, constants.PermDirPrivate},
		{filepath.Join(fs.runtimeDir, constants.DataDirname), constants.PermDirPrivate},
		{pkiDir, constants.PermDirPrivate},
		{filepath.Join(pkiDir, constants.PkiSubdirRoot), constants.PermDirPrivate},
		{filepath.Join(pkiDir, constants.PkiSubdirAuthorities), constants.PermDirPrivate},
		{filepath.Join(pkiDir, constants.PkiSubdirIssued), constants.PermDirPrivate},
		{filepath.Join(pkiDir, constants.PkiSubdirIssued, constants.PkiSubdirHub), constants.PermDirPrivate},
		{filepath.Join(pkiDir, constants.PkiSubdirIssued, constants.PkiSubdirGatewayPeer), constants.PermDirPrivate},
		{filepath.Join(pkiDir, constants.PkiSubdirIssued, constants.PkiSubdirApps), constants.PermDirPrivate},
		{filepath.Join(pkiDir, constants.PkiSubdirTrust), constants.PermDirStandard},
		{filepath.Join(pkiDir, constants.PkiSubdirRevocation), constants.PermDirStandard},
		{filepath.Join(pkiDir, constants.PkiSubdirBinaries), constants.PermDirStandard},
		{filepath.Join(pkiDir, constants.PkiSubdirClient), constants.PermDirPrivate},
		{filepath.Join(fs.runtimeDir, constants.SecretsDirname), constants.PermDirPrivate},
		{filepath.Join(fs.runtimeDir, constants.VaultDirname), constants.PermDirPrivate},
		{filepath.Join(fs.runtimeDir, constants.DataDirname, constants.LedgerDirname), constants.PermDirPrivate},
		{filepath.Join(fs.runtimeDir, constants.DataDirname, constants.LedgerDirname, constants.FilesDirname), constants.PermDirPrivate},
		{filepath.Join(fs.runtimeDir, constants.LogDirname), constants.PermDirPrivate},
		{filepath.Join(fs.runtimeDir, constants.PidDirname), constants.PermDirPrivate},
		{filepath.Join(fs.runtimeDir, constants.BinDirname), constants.PermDirPrivate},
		{filepath.Join(fs.runtimeDir, constants.PublicFeedDirname), constants.PermDirPrivate},
		{filepath.Join(fs.runtimeDir, constants.PublicMirrorDirname), constants.PermDirPrivate},
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return fmt.Errorf("%w: %s: %w", constants.ErrDirCreateFailed, dir.path, err)
		}
	}

	return nil
}
