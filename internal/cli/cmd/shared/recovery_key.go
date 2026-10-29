// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package shared

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

const (
	// FlagAcceptUnrecoverable creates a master key without a recovery key.
	FlagAcceptUnrecoverable = "accept-unrecoverable"
	// AcceptUnrecoverableHelp is the flag text for gateway and operator start.
	AcceptUnrecoverableHelp = "Create the master key without showing a recovery key. If the OS key store is lost, this runtime cannot be restored"
)

// recoveryMu keeps two local operator launches from printing recovery keys at once.
var recoveryMu sync.Mutex

// EnsureRecoveryKey stores a new master key only after the operator saves it.
// A non-nil kr is an injected store (tests); the caller owns that key and no
// ceremony runs. An existing OS master key is left untouched and is not read,
// so this check does not add a keychain prompt.
func EnsureRecoveryKey(in io.Reader, out io.Writer, fileSvc fs.RuntimeFileService, kr keystore.Keyring, role string, acceptUnrecoverable bool) error {
	if kr != nil {
		return nil
	}
	recoveryMu.Lock()
	defer recoveryMu.Unlock()
	return provisionRecoveryKey(in, out, fileSvc, nil, role, acceptUnrecoverable)
}

// provisionRecoveryKey owns the master-key ceremony, independent of runtime
// directory creation and permissions, which belong to service startup.
func provisionRecoveryKey(in io.Reader, out io.Writer, fileSvc fs.RuntimeFileService, kr keystore.Keyring, role string, acceptUnrecoverable bool) error {
	logger := slog.New(slog.DiscardHandler)
	var store *keystore.Keystore
	store, err := keystore.NewWithFS(fileSvc, logger, keystore.Options{
		Keyring: kr,
		Ceremony: func(key []byte) error {
			return keystore.ConfirmRecoveryKey(out, in, role, store.KeyringName(), key, keystore.ReaderIsTerminal(in), acceptUnrecoverable)
		},
	})
	if err != nil {
		return fmt.Errorf("recovery key: %w", err)
	}
	defer store.Close()
	present, err := store.MasterKeyPresent()
	if err != nil {
		return fmt.Errorf("recovery key: %w", err)
	}
	if present {
		return nil
	}
	if err := store.Initialize(); err != nil {
		return fmt.Errorf("recovery key: %w", err)
	}
	return nil
}

// RecoverRuntimeKey installs a recovery key the operator saved at creation.
// It does not generate a key, replace a different key, or log the one it reads.
func RecoverRuntimeKey(in io.Reader, out io.Writer, fileSvc fs.RuntimeFileService, kr keystore.Keyring, role string) error {
	if role == "" {
		role = keystore.RecoveryRoleRuntime
	}
	_, _ = io.WriteString(out, "[ztd] Paste the recovery key for this "+role+". It is not logged.\n")
	line, err := keystore.ReadRecoveryLine(in)
	if err != nil {
		if errors.Is(err, constants.ErrKeyStoreRecoveryDeclined) {
			_, _ = io.WriteString(out, "[ztd] No recovery key entered. Nothing was changed.\n")
		}
		return err
	}
	defer vault.SecureZero(line)
	key, err := keystore.ParseRecoveryKey(line)
	if err != nil {
		_, _ = io.WriteString(out, "[ztd] That key is not valid. Nothing was changed.\n")
		return err
	}
	defer vault.SecureZero(key)
	fingerprint := keystore.RecoveryFingerprint(key)
	ks, err := keystore.NewWithFS(fileSvc, slog.New(slog.DiscardHandler), keystore.Options{Keyring: kr})
	if err != nil {
		return fmt.Errorf("recovery key: %w", err)
	}
	stored, err := ks.InstallRecoveryKey(key)
	if err != nil {
		if errors.Is(err, constants.ErrKeyStoreRecoveryConflict) {
			_, _ = io.WriteString(out, "[ztd] The OS key store already holds a different master key. It was not replaced.\n")
		}
		if errors.Is(err, constants.ErrKeyStoreRecoveryMismatch) {
			_, _ = io.WriteString(out, "[ztd] That key does not open this runtime. Nothing was changed.\n")
		}
		return fmt.Errorf("recovery key: %w", err)
	}
	if stored {
		_, _ = fmt.Fprintf(out, "[ztd] Recovery key installed. Fingerprint: %s\n", fingerprint)
		_, _ = io.WriteString(out, "[ztd] Start the "+role+" again. Keep the offline copy. This command will not show the key.\n")
		return nil
	}
	_, _ = fmt.Fprintf(out, "[ztd] The OS key store already holds this recovery key. Fingerprint: %s\n", fingerprint)
	return nil
}
