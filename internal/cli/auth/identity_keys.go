// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/logging"
)

// Client identity secrets (the CLI and app private keys, and the pending app
// enrollment state that carries a key and a bearer token) never touch disk in
// the clear. They are sealed under the runtime tree's keystore master key,
// bound to their runtime-relative path, and written at the path the plaintext
// used to occupy. A plaintext file at one of those paths is refused with
// constants.ErrIdentityFilePlaintext; there is no migration or fallback.

// openIdentityKeystore returns the keystore that seals client identity files.
// Sealing may create the master key in the OS key store on first use; opening
// never does, so a read with a missing or locked master key fails closed.
func openIdentityKeystore(fileSvc fs.RuntimeFileService, cfg *config.Config, create bool) (*keystore.Keystore, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: config is required to open the identity keystore", constants.ErrInternal)
	}
	opts := keystore.Options{Keyring: cfg.Keyring}
	// Every CLI request unseals the identity key, so keystore progress logging
	// stays below warn/error level instead of printing on each command.
	logger, err := logging.NewLogger(constants.LogLevelError, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("auth: configure logger: %w", err)
	}
	if create {
		return keystore.Open(fileSvc, logger, opts)
	}
	return keystore.NewWithFS(fileSvc, logger, opts)
}

// identityRelPath resolves an absolute runtime path to the slash-separated
// runtime-relative path that binds its sealed contents.
func identityRelPath(fileSvc fs.RuntimeFileService, absPath string) (string, error) {
	rel, err := fileSvc.RelFromAbs(absPath)
	if err != nil {
		return "", fmt.Errorf("auth: resolve identity path: %w", err)
	}
	return filepath.ToSlash(rel), nil
}

// sealIdentityFile seals plaintext under the keystore master key and writes it
// to relPath with private permissions.
func sealIdentityFile(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, relPath string, plaintext []byte) error {
	ks, err := openIdentityKeystore(fileSvc, cfg, true)
	if err != nil {
		return fmt.Errorf("auth: seal %s: %w", relPath, err)
	}
	sealed, err := ks.Encrypt(filepath.ToSlash(relPath), string(plaintext))
	if err != nil {
		return fmt.Errorf("auth: seal %s: %w", relPath, err)
	}
	if err := fileSvc.WriteFile(ctx, relPath, []byte(sealed), constants.PermFilePrivate); err != nil {
		return fmt.Errorf("auth: write sealed %s: %w", relPath, err)
	}
	return nil
}

// unsealIdentityFile reads relPath and unseals it. A missing file keeps its
// not-found error; a plaintext file returns ErrIdentityFilePlaintext; any other
// failure (including a missing or locked master key) wraps ErrIdentityFileUnseal.
func unsealIdentityFile(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, relPath string) ([]byte, error) {
	data, err := fileSvc.ReadFile(ctx, relPath)
	if err != nil {
		return nil, fmt.Errorf("auth: read %s: %w", relPath, err)
	}
	trimmed := bytes.TrimSpace(data)
	if block, _ := pem.Decode(trimmed); block != nil || bytes.HasPrefix(trimmed, []byte("{")) {
		return nil, fmt.Errorf("%w: %s", constants.ErrIdentityFilePlaintext, relPath)
	}
	ks, err := openIdentityKeystore(fileSvc, cfg, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", constants.ErrIdentityFileUnseal, relPath, err)
	}
	plaintext, err := ks.Decrypt(filepath.ToSlash(relPath), string(trimmed))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", constants.ErrIdentityFileUnseal, relPath, err)
	}
	return []byte(plaintext), nil
}

// SealIdentityKey seals keyPEM under the keystore master key and writes it to
// the absolute runtime path keyPath.
func SealIdentityKey(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, keyPath string, keyPEM []byte) error {
	rel, err := identityRelPath(fileSvc, keyPath)
	if err != nil {
		return err
	}
	return sealIdentityFile(ctx, fileSvc, cfg, rel, keyPEM)
}

// UnsealIdentityKey reads the sealed private key at the absolute runtime path
// keyPath and returns its PEM. The caller must not persist it.
func UnsealIdentityKey(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, keyPath string) ([]byte, error) {
	rel, err := identityRelPath(fileSvc, keyPath)
	if err != nil {
		return nil, err
	}
	return unsealIdentityFile(ctx, fileSvc, cfg, rel)
}

// LoadIdentityCertificate builds a tls.Certificate from the certificate PEM at
// certPath and the sealed private key at keyPath.
func LoadIdentityCertificate(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, certPath, keyPath string) (tls.Certificate, error) {
	certRel, err := identityRelPath(fileSvc, certPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM, err := fileSvc.ReadFile(ctx, certRel)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("auth: read %s: %w", certRel, err)
	}
	keyPEM, err := UnsealIdentityKey(ctx, fileSvc, cfg, keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("auth: invalid certificate and key pair: %w", err)
	}
	return cert, nil
}

// LoadCLICertificate returns the enrolled CLI mTLS client certificate with its
// private key unsealed in memory only.
func LoadCLICertificate(fileSvc fs.RuntimeFileService, cfg *config.Config) (tls.Certificate, error) {
	cert, err := LoadIdentityCertificate(context.Background(), fileSvc, cfg, cfg.CLICertFile(), cfg.CLIKeyFile())
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("%w: %w", constants.ErrFailedToLoadClientCertificate, err)
	}
	return cert, nil
}
