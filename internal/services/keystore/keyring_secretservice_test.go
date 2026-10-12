// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux

package keystore

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

const fakeLoginCollection = dbus.ObjectPath("/org/freedesktop/secrets/collection/login")

type fakeItem struct {
	collection dbus.ObjectPath
	label      string
	attributes map[string]string
	secret     []byte
	locked     bool
}

// fakeSecretService is an in-memory Secret Service that records every call.
type fakeSecretService struct {
	defaultAlias     dbus.ObjectPath
	collectionLocked bool
	items            map[dbus.ObjectPath]*fakeItem
	nextItem         int
	calls            []string
	unlocked         []dbus.ObjectPath
	searchErr        error
	closed           int
}

func newFakeSecretService() *fakeSecretService {
	return &fakeSecretService{defaultAlias: fakeLoginCollection, items: map[dbus.ObjectPath]*fakeItem{}}
}

func (f *fakeSecretService) dial() (secretService, error) { return f, nil }

func (f *fakeSecretService) addItem(secret string, locked bool) dbus.ObjectPath {
	f.nextItem++
	path := dbus.ObjectPath(string(fakeLoginCollection) + "/" + string(rune('0'+f.nextItem)))
	f.items[path] = &fakeItem{collection: fakeLoginCollection, attributes: masterKeyAttributes(), secret: []byte(secret), locked: locked}
	return path
}

func (f *fakeSecretService) ReadAlias(string) (dbus.ObjectPath, error) {
	f.calls = append(f.calls, "ReadAlias")
	return f.defaultAlias, nil
}

func (f *fakeSecretService) SearchItems(attributes map[string]string) ([]dbus.ObjectPath, []dbus.ObjectPath, error) {
	f.calls = append(f.calls, "SearchItems")
	if f.searchErr != nil {
		return nil, nil, f.searchErr
	}
	var unlocked, locked []dbus.ObjectPath
	for path, item := range f.items {
		match := true
		for k, v := range attributes {
			if item.attributes[k] != v {
				match = false
			}
		}
		if !match {
			continue
		}
		if item.locked {
			locked = append(locked, path)
		} else {
			unlocked = append(unlocked, path)
		}
	}
	return unlocked, locked, nil
}

func (f *fakeSecretService) CollectionLocked(dbus.ObjectPath) (bool, error) {
	f.calls = append(f.calls, "CollectionLocked")
	return f.collectionLocked, nil
}

func (f *fakeSecretService) Unlock(objects []dbus.ObjectPath) error {
	f.calls = append(f.calls, "Unlock")
	f.unlocked = append(f.unlocked, objects...)
	for _, path := range objects {
		if item, ok := f.items[path]; ok {
			item.locked = false
		}
		if path == fakeLoginCollection {
			f.collectionLocked = false
		}
	}
	return nil
}

func (f *fakeSecretService) GetSecret(item dbus.ObjectPath) ([]byte, error) {
	f.calls = append(f.calls, "GetSecret")
	return bytes.Clone(f.items[item].secret), nil
}

func (f *fakeSecretService) CreateItem(collection dbus.ObjectPath, label string, attributes map[string]string, secret []byte) error {
	f.calls = append(f.calls, "CreateItem")
	if f.collectionLocked {
		return errors.New("collection is locked")
	}
	f.nextItem++
	path := dbus.ObjectPath(string(collection) + "/" + string(rune('0'+f.nextItem)))
	f.items[path] = &fakeItem{collection: collection, label: label, attributes: attributes, secret: bytes.Clone(secret)}
	return nil
}

func (f *fakeSecretService) DeleteItem(item dbus.ObjectPath) error {
	f.calls = append(f.calls, "DeleteItem")
	delete(f.items, item)
	return nil
}

func (f *fakeSecretService) Close() error {
	f.closed++
	return nil
}

func TestSecretServiceKeyring_StoreAndRetrieveRoundTrip(t *testing.T) {
	t.Parallel()

	fake := newFakeSecretService()
	kr, err := newSecretServiceKeyring(fake.dial)
	require.NoError(t, err)
	assert.Equal(t, "secret-service", kr.Name())

	key := testMasterKey(1)
	require.NoError(t, kr.StoreMasterKey(key))
	require.Len(t, fake.items, 1)
	for _, item := range fake.items {
		assert.Equal(t, fakeLoginCollection, item.collection)
		assert.Equal(t, keyStoreName, item.label)
		assert.Equal(t, masterKeyName, item.attributes[keyStoreName])
		assert.Equal(t, secretSchemaGeneric, item.attributes[secretSchemaAttribute])
		assert.Equal(t, base64.StdEncoding.EncodeToString(key), string(item.secret))
	}

	got, err := kr.RetrieveMasterKey()
	require.NoError(t, err)
	assert.Equal(t, key, got)
	assert.Equal(t, 3, fake.closed, "every operation closes its connection")
}

func TestSecretServiceKeyring_ConstructionNeverWrites(t *testing.T) {
	t.Parallel()

	fake := newFakeSecretService()
	_, err := newSecretServiceKeyring(fake.dial)
	require.NoError(t, err)
	assert.Equal(t, []string{"SearchItems", "ReadAlias"}, fake.calls)
	assert.Empty(t, fake.items)
}

func TestSecretServiceKeyring_NoDefaultCollectionFailsAtConstruction(t *testing.T) {
	t.Parallel()

	fake := newFakeSecretService()
	fake.defaultAlias = noObject
	_, err := newSecretServiceKeyring(fake.dial)
	require.ErrorIs(t, err, constants.ErrKeyStoreNoDefaultCollection)
	assert.NotContains(t, fake.calls, "CreateItem")
}

func TestSecretServiceKeyring_ExistingKeyNeedsNoDefaultCollection(t *testing.T) {
	t.Parallel()

	fake := newFakeSecretService()
	fake.defaultAlias = noObject
	key := testMasterKey(2)
	fake.addItem(base64.StdEncoding.EncodeToString(key)+"\n", false)

	kr, err := newSecretServiceKeyring(fake.dial)
	require.NoError(t, err)
	got, err := kr.RetrieveMasterKey()
	require.NoError(t, err)
	assert.Equal(t, key, got)
}

func TestSecretServiceKeyring_RetrieveUnlocksLockedItem(t *testing.T) {
	t.Parallel()

	fake := newFakeSecretService()
	key := testMasterKey(3)
	item := fake.addItem(base64.StdEncoding.EncodeToString(key), true)

	got, err := (&secretServiceKeyring{dial: fake.dial}).RetrieveMasterKey()
	require.NoError(t, err)
	assert.Equal(t, key, got)
	assert.Equal(t, []dbus.ObjectPath{item}, fake.unlocked)
}

func TestSecretServiceKeyring_StoreUnlocksLockedCollection(t *testing.T) {
	t.Parallel()

	fake := newFakeSecretService()
	fake.collectionLocked = true
	require.NoError(t, (&secretServiceKeyring{dial: fake.dial}).StoreMasterKey(testMasterKey(4)))
	assert.Equal(t, []dbus.ObjectPath{fakeLoginCollection}, fake.unlocked)
	assert.Len(t, fake.items, 1)
}

func TestSecretServiceKeyring_StoreWithoutDefaultCollection(t *testing.T) {
	t.Parallel()

	fake := newFakeSecretService()
	fake.defaultAlias = noObject
	err := (&secretServiceKeyring{dial: fake.dial}).StoreMasterKey(testMasterKey(5))
	require.ErrorIs(t, err, constants.ErrKeyStoreNoDefaultCollection)
	assert.Empty(t, fake.items)
}

func TestSecretServiceKeyring_RetrieveNotFound(t *testing.T) {
	t.Parallel()

	fake := newFakeSecretService()
	_, err := (&secretServiceKeyring{dial: fake.dial}).RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)
}

func TestSecretServiceKeyring_DuplicateItemsFailClosed(t *testing.T) {
	t.Parallel()

	fake := newFakeSecretService()
	fake.addItem(base64.StdEncoding.EncodeToString(testMasterKey(6)), false)
	fake.addItem(base64.StdEncoding.EncodeToString(testMasterKey(7)), false)
	_, err := (&secretServiceKeyring{dial: fake.dial}).RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreDuplicateKey)
	require.NotErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)
	assert.NotContains(t, fake.calls, "GetSecret")
}

func TestSecretServiceKeyring_ServiceErrorsAreNotMissing(t *testing.T) {
	t.Parallel()

	fake := newFakeSecretService()
	fake.searchErr = errors.New("org.freedesktop.DBus.Error.ServiceUnknown")
	_, err := newSecretServiceKeyring(fake.dial)
	require.ErrorIs(t, err, constants.ErrKeyStoreSecretServiceDown)
	require.NotErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)

	dialErr := errors.New("no session bus")
	_, err = newSecretServiceKeyring(func() (secretService, error) { return nil, dialErr })
	require.ErrorIs(t, err, constants.ErrKeyStoreSecretServiceDown)
	require.ErrorIs(t, err, dialErr)
}

func TestSecretServiceKeyring_CorruptSecretDoesNotMeanMissing(t *testing.T) {
	t.Parallel()

	for _, data := range []string{"", "bad!", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		fake := newFakeSecretService()
		fake.addItem(data, false)
		_, err := (&secretServiceKeyring{dial: fake.dial}).RetrieveMasterKey()
		require.Error(t, err)
		require.NotErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)
	}
}

func TestSecretServiceKeyring_DeleteRemovesEveryMatch(t *testing.T) {
	t.Parallel()

	fake := newFakeSecretService()
	kr := &secretServiceKeyring{dial: fake.dial}
	require.NoError(t, kr.DeleteMasterKey(), "deleting a missing key is not an error")

	fake.addItem("a", false)
	fake.addItem("b", true)
	require.NoError(t, kr.DeleteMasterKey())
	assert.Empty(t, fake.items)
}
