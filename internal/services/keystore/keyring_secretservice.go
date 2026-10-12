// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux

package keystore

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

// defaultCollectionAlias is the Secret Service alias new items are stored
// under. An unset alias resolves to noObject.
const defaultCollectionAlias = "default"

// masterKeyAttributes identify the master key item. Lookups match on this
// subset, so items stored by earlier secret-tool based releases still resolve.
func masterKeyAttributes() map[string]string {
	return map[string]string{keyStoreName: masterKeyName}
}

// secretService is the subset of the freedesktop Secret Service API the
// keyring needs. dbusSecretService implements it over a private session-bus
// connection; tests substitute an in-memory fake.
type secretService interface {
	// ReadAlias resolves a collection alias, returning noObject when unset.
	ReadAlias(name string) (dbus.ObjectPath, error)
	// SearchItems returns matching items across all collections.
	SearchItems(attributes map[string]string) (unlocked, locked []dbus.ObjectPath, err error)
	// CollectionLocked reports whether a collection must be unlocked before writing.
	CollectionLocked(collection dbus.ObjectPath) (bool, error)
	// Unlock unlocks objects, completing any prompt the service requires.
	Unlock(objects []dbus.ObjectPath) error
	// GetSecret returns an item's secret value.
	GetSecret(item dbus.ObjectPath) ([]byte, error)
	// CreateItem stores a new item without replacing an existing one.
	CreateItem(collection dbus.ObjectPath, label string, attributes map[string]string, secret []byte) error
	// DeleteItem removes an item, completing any prompt the service requires.
	DeleteItem(item dbus.ObjectPath) error
	// Close ends the transfer session and the bus connection.
	Close() error
}

// secretServiceDialer opens a secretService connection for one keyring operation.
type secretServiceDialer func() (secretService, error)

// secretServiceKeyring stores the master key as base64 text in the Secret
// Service (GNOME Keyring, KWallet), spoken to directly over D-Bus.
type secretServiceKeyring struct {
	dial secretServiceDialer
}

// newSecretServiceKeyring returns a Secret Service keyring only when the
// service answers and the master key is either readable or storable: an
// existing key must decode, and a missing key requires a default collection.
// Construction never writes.
func newSecretServiceKeyring(dial secretServiceDialer) (Keyring, error) {
	k := &secretServiceKeyring{dial: dial}
	svc, err := k.open()
	if err != nil {
		return nil, err
	}
	defer svc.Close()

	key, err := retrieveMasterKey(svc)
	vault.SecureZero(key)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, constants.ErrKeyStoreKeyNotFound) {
		return nil, err
	}
	if _, err := defaultCollection(svc); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *secretServiceKeyring) Name() string {
	return "secret-service"
}

func (k *secretServiceKeyring) open() (secretService, error) {
	svc, err := k.dial()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrKeyStoreSecretServiceDown, err)
	}
	return svc, nil
}

func (k *secretServiceKeyring) RetrieveMasterKey() ([]byte, error) {
	svc, err := k.open()
	if err != nil {
		return nil, err
	}
	defer svc.Close()
	return retrieveMasterKey(svc)
}

// StoreMasterKey writes a new item into the default collection. It never
// replaces an existing item; a racing duplicate makes retrieval fail closed.
func (k *secretServiceKeyring) StoreMasterKey(key []byte) error {
	svc, err := k.open()
	if err != nil {
		return err
	}
	defer svc.Close()

	collection, err := defaultCollection(svc)
	if err != nil {
		return err
	}
	locked, err := svc.CollectionLocked(collection)
	if err != nil {
		return fmt.Errorf("%w: secret service: collection %s: %w", constants.ErrKeyStoreStoreFailed, collection, err)
	}
	if locked {
		if err := svc.Unlock([]dbus.ObjectPath{collection}); err != nil {
			return fmt.Errorf("%w: secret service: unlock collection %s: %w", constants.ErrKeyStoreStoreFailed, collection, err)
		}
	}

	encoded := []byte(base64.StdEncoding.EncodeToString(key))
	defer vault.SecureZero(encoded)
	attributes := masterKeyAttributes()
	attributes[secretSchemaAttribute] = secretSchemaGeneric
	if err := svc.CreateItem(collection, keyStoreName, attributes, encoded); err != nil {
		return fmt.Errorf("%w: secret service: create item in %s: %w", constants.ErrKeyStoreStoreFailed, collection, err)
	}
	return nil
}

func (k *secretServiceKeyring) DeleteMasterKey() error {
	svc, err := k.open()
	if err != nil {
		return err
	}
	defer svc.Close()

	unlocked, locked, err := svc.SearchItems(masterKeyAttributes())
	if err != nil {
		return fmt.Errorf("%w: secret service: search: %w", constants.ErrKeyStoreDeleteFailed, err)
	}
	for _, item := range append(unlocked, locked...) {
		if err := svc.DeleteItem(item); err != nil {
			return fmt.Errorf("%w: secret service: delete %s: %w", constants.ErrKeyStoreDeleteFailed, item, err)
		}
	}
	return nil
}

// defaultCollection resolves the default alias. Hosts whose login keyring was
// never created (autologin, SSH-only sessions) have only the in-memory session
// collection and no default; storing there would lose the key at logout.
func defaultCollection(svc secretService) (dbus.ObjectPath, error) {
	collection, err := svc.ReadAlias(defaultCollectionAlias)
	if err != nil {
		return "", fmt.Errorf("%w: read alias %q: %w", constants.ErrKeyStoreSecretServiceDown, defaultCollectionAlias, err)
	}
	if collection == noObject {
		return "", constants.ErrKeyStoreNoDefaultCollection
	}
	return collection, nil
}

// retrieveMasterKey requires exactly one matching item; several items are
// ambiguous and fail closed rather than guessing which key encrypts the vault.
func retrieveMasterKey(svc secretService) ([]byte, error) {
	unlocked, locked, err := svc.SearchItems(masterKeyAttributes())
	if err != nil {
		return nil, fmt.Errorf("%w: search master key: %w", constants.ErrKeyStoreSecretServiceDown, err)
	}
	items := append(unlocked, locked...)
	switch len(items) {
	case 0:
		return nil, constants.ErrKeyStoreKeyNotFound
	case 1:
	default:
		return nil, fmt.Errorf("%w: %d items match %s=%s", constants.ErrKeyStoreDuplicateKey, len(items), keyStoreName, masterKeyName)
	}
	if len(locked) > 0 {
		if err := svc.Unlock(locked); err != nil {
			return nil, fmt.Errorf("%w: unlock master key: %w", constants.ErrKeyStoreLocked, err)
		}
	}

	secret, err := svc.GetSecret(items[0])
	if err != nil {
		return nil, fmt.Errorf("%w: get master key: %w", constants.ErrKeyStoreSecretServiceDown, err)
	}
	defer vault.SecureZero(secret)
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(secret)))
	if err != nil {
		vault.SecureZero(key)
		return nil, fmt.Errorf("%w: secret service: %w", constants.ErrKeyStoreDecodeFailed, err)
	}
	if len(key) != vault.KeySize {
		vault.SecureZero(key)
		return nil, constants.ErrKeyStoreInvalidKeyLength
	}
	return key, nil
}
