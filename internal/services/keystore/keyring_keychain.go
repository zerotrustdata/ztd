// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build darwin

package keystore

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

const (
	coreFoundationPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	securityPath       = "/System/Library/Frameworks/Security.framework/Security"

	kCFStringEncodingUTF8 = 0x08000100

	errSecSuccess               = 0
	errSecItemNotFound          = -25300
	errSecDuplicateItem         = -25299
	errSecInteractionNotAllowed = -25308
	errSecAuthFailed            = -25293
	errSecUserCanceled          = -128
)

// keychainAPI is the CoreFoundation and Security.framework surface the
// keyring calls, bound at runtime without cgo.
type keychainAPI struct {
	cfRelease           func(ref uintptr)
	cfStringCreate      func(alloc uintptr, s string, encoding uint32) uintptr
	cfDataCreate        func(alloc uintptr, data *byte, length int) uintptr
	cfDataGetLength     func(data uintptr) int
	cfDataGetBytePtr    func(data uintptr) *byte
	cfDictionaryCreate  func(alloc uintptr, keys, values *uintptr, count int, keyCallbacks, valueCallbacks uintptr) uintptr
	secItemAdd          func(attributes uintptr, result *uintptr) int32
	secItemCopyMatching func(query uintptr, result *uintptr) int32
	secItemDelete       func(query uintptr) int32

	keyCallbacks, valueCallbacks                         uintptr
	booleanTrue                                          uintptr
	class, classGenericPassword, service, account, label uintptr
	valueData, returnData, matchLimit, matchLimitOne     uintptr
}

var loadKeychainAPI = sync.OnceValues(func() (*keychainAPI, error) {
	cf, err := purego.Dlopen(coreFoundationPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("%w: load CoreFoundation: %w", constants.ErrKeyStoreKeychainFailed, err)
	}
	sec, err := purego.Dlopen(securityPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("%w: load Security: %w", constants.ErrKeyStoreKeychainFailed, err)
	}
	api := &keychainAPI{}
	funcs := []struct {
		lib  uintptr
		name string
		fptr any
	}{
		{cf, "CFRelease", &api.cfRelease},
		{cf, "CFStringCreateWithCString", &api.cfStringCreate},
		{cf, "CFDataCreate", &api.cfDataCreate},
		{cf, "CFDataGetLength", &api.cfDataGetLength},
		{cf, "CFDataGetBytePtr", &api.cfDataGetBytePtr},
		{cf, "CFDictionaryCreate", &api.cfDictionaryCreate},
		{sec, "SecItemAdd", &api.secItemAdd},
		{sec, "SecItemCopyMatching", &api.secItemCopyMatching},
		{sec, "SecItemDelete", &api.secItemDelete},
	}
	for _, f := range funcs {
		sym, err := purego.Dlsym(f.lib, f.name)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", constants.ErrKeyStoreKeychainFailed, f.name, err)
		}
		purego.RegisterFunc(f.fptr, sym)
	}
	// The callback structs are used by address; every other symbol is a
	// global CFTypeRef variable whose value is the constant.
	addrs := []struct {
		lib   uintptr
		name  string
		dst   *uintptr
		deref bool
	}{
		{cf, "kCFTypeDictionaryKeyCallBacks", &api.keyCallbacks, false},
		{cf, "kCFTypeDictionaryValueCallBacks", &api.valueCallbacks, false},
		{cf, "kCFBooleanTrue", &api.booleanTrue, true},
		{sec, "kSecClass", &api.class, true},
		{sec, "kSecClassGenericPassword", &api.classGenericPassword, true},
		{sec, "kSecAttrService", &api.service, true},
		{sec, "kSecAttrAccount", &api.account, true},
		{sec, "kSecAttrLabel", &api.label, true},
		{sec, "kSecValueData", &api.valueData, true},
		{sec, "kSecReturnData", &api.returnData, true},
		{sec, "kSecMatchLimit", &api.matchLimit, true},
		{sec, "kSecMatchLimitOne", &api.matchLimitOne, true},
	}
	for _, a := range addrs {
		sym, err := purego.Dlsym(a.lib, a.name)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", constants.ErrKeyStoreKeychainFailed, a.name, err)
		}
		*a.dst = sym
		if a.deref {
			*a.dst = **(**uintptr)(unsafe.Pointer(&sym))
		}
	}
	return api, nil
})

// keychainKeyring stores the master key as a generic password in the user's
// default Keychain through Security.framework. The item is created by the ztd
// process itself, so its access list trusts only ztd: other programs,
// including the security command, must pass a Keychain prompt to read it.
type keychainKeyring struct {
	api     *keychainAPI
	service string
	account string
}

func newKeychainKeyring() (Keyring, error) {
	return newKeychainKeyringFor(keyStoreName, masterKeyName)
}

func newKeychainKeyringFor(service, account string) (Keyring, error) {
	api, err := loadKeychainAPI()
	if err != nil {
		return nil, err
	}
	return &keychainKeyring{api: api, service: service, account: account}, nil
}

func (c *keychainKeyring) Name() string {
	return "keychain"
}

// cfString returns an owned CFString; the caller releases it.
func (c *keychainKeyring) cfString(s string) (uintptr, error) {
	ref := c.api.cfStringCreate(0, s, kCFStringEncodingUTF8)
	if ref == 0 {
		return 0, fmt.Errorf("%w: create CFString", constants.ErrKeyStoreKeychainFailed)
	}
	return ref, nil
}

// query builds an owned dictionary identifying the master key item plus
// extra key/value pairs; the caller releases it.
func (c *keychainKeyring) query(extra ...uintptr) (uintptr, error) {
	service, err := c.cfString(c.service)
	if err != nil {
		return 0, err
	}
	defer c.api.cfRelease(service)
	account, err := c.cfString(c.account)
	if err != nil {
		return 0, err
	}
	defer c.api.cfRelease(account)

	keys := []uintptr{c.api.class, c.api.service, c.api.account}
	values := []uintptr{c.api.classGenericPassword, service, account}
	for i := 0; i+1 < len(extra); i += 2 {
		keys = append(keys, extra[i])
		values = append(values, extra[i+1])
	}
	dict := c.api.cfDictionaryCreate(0, &keys[0], &values[0], len(keys), c.api.keyCallbacks, c.api.valueCallbacks)
	if dict == 0 {
		return 0, fmt.Errorf("%w: create query", constants.ErrKeyStoreKeychainFailed)
	}
	return dict, nil
}

func keychainStatusError(op string, status int32) error {
	switch status {
	case errSecInteractionNotAllowed, errSecAuthFailed, errSecUserCanceled:
		return fmt.Errorf("%w: keychain %s: OSStatus %d", constants.ErrKeyStoreLocked, op, status)
	default:
		return fmt.Errorf("%w: %s: OSStatus %d", constants.ErrKeyStoreKeychainFailed, op, status)
	}
}

func (c *keychainKeyring) RetrieveMasterKey() ([]byte, error) {
	query, err := c.query(c.api.returnData, c.api.booleanTrue, c.api.matchLimit, c.api.matchLimitOne)
	if err != nil {
		return nil, err
	}
	defer c.api.cfRelease(query)

	var data uintptr
	switch status := c.api.secItemCopyMatching(query, &data); status {
	case errSecSuccess:
	case errSecItemNotFound:
		return nil, constants.ErrKeyStoreKeyNotFound
	default:
		return nil, keychainStatusError("read", status)
	}
	if data == 0 {
		return nil, fmt.Errorf("%w: read returned no data", constants.ErrKeyStoreKeychainFailed)
	}
	defer c.api.cfRelease(data)

	n := c.api.cfDataGetLength(data)
	if n != vault.KeySize {
		return nil, fmt.Errorf("%w: got %d, expected %d", constants.ErrKeyStoreInvalidKeyLength, n, vault.KeySize)
	}
	key := make([]byte, n)
	copy(key, unsafe.Slice(c.api.cfDataGetBytePtr(data), n))
	return key, nil
}

// StoreMasterKey adds the raw key as a new item. It never replaces an
// existing item: a duplicate fails rather than overwrite a key that may
// already seal secrets.
func (c *keychainKeyring) StoreMasterKey(key []byte) error {
	if len(key) != vault.KeySize {
		return fmt.Errorf("%w: got %d, expected %d", constants.ErrKeyStoreInvalidKeyLength, len(key), vault.KeySize)
	}
	data := c.api.cfDataCreate(0, &key[0], len(key))
	if data == 0 {
		return fmt.Errorf("%w: create CFData", constants.ErrKeyStoreKeychainFailed)
	}
	defer c.api.cfRelease(data)
	label, err := c.cfString(keyStoreName)
	if err != nil {
		return err
	}
	defer c.api.cfRelease(label)
	attributes, err := c.query(c.api.label, label, c.api.valueData, data)
	if err != nil {
		return err
	}
	defer c.api.cfRelease(attributes)

	switch status := c.api.secItemAdd(attributes, nil); status {
	case errSecSuccess:
		return nil
	case errSecDuplicateItem:
		return fmt.Errorf("%w: keychain item %s/%s already exists", constants.ErrKeyStoreDuplicateKey, c.service, c.account)
	default:
		return keychainStatusError("add", status)
	}
}

func (c *keychainKeyring) DeleteMasterKey() error {
	query, err := c.query()
	if err != nil {
		return err
	}
	defer c.api.cfRelease(query)
	switch status := c.api.secItemDelete(query); status {
	case errSecSuccess, errSecItemNotFound:
		return nil
	default:
		return keychainStatusError("delete", status)
	}
}
