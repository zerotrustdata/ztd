// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux

package keystore

import (
	"context"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Secret Service D-Bus API names (freedesktop Secret Service API 0.2).
const (
	secretServiceBusName     = "org.freedesktop.secrets"
	secretServicePath        = dbus.ObjectPath("/org/freedesktop/secrets")
	secretServiceIface       = "org.freedesktop.Secret.Service"
	secretCollectionIface    = "org.freedesktop.Secret.Collection"
	secretItemIface          = "org.freedesktop.Secret.Item"
	secretSessionIface       = "org.freedesktop.Secret.Session"
	secretPromptIface        = "org.freedesktop.Secret.Prompt"
	dbusPropertiesGet        = "org.freedesktop.DBus.Properties.Get"
	secretItemLabelProp      = secretItemIface + ".Label"
	secretItemAttrsProp      = secretItemIface + ".Attributes"
	secretSchemaAttribute    = "xdg:schema"
	secretSchemaGeneric      = "org.freedesktop.Secret.Generic"
	secretContentType        = "text/plain"
	secretAlgorithmPlain     = "plain"
	noObject                 = dbus.ObjectPath("/")
	secretServiceCallTimeout = 15 * time.Second
	// secretServicePromptTimeout bounds an unlock or delete prompt, which waits
	// on a human at the desktop.
	secretServicePromptTimeout = 2 * time.Minute
)

// secretValue is the Secret Service (oayays) secret struct.
type secretValue struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

// dbusSecretService speaks the Secret Service API over a private session-bus
// connection with a "plain" transfer session. Plain transfer adds no exposure:
// any same-user process able to monitor the session bus can already call
// GetSecret on an unlocked collection itself.
type dbusSecretService struct {
	conn    *dbus.Conn
	session dbus.ObjectPath
}

// dialSecretService connects to the existing session bus and opens a transfer
// session. It never autolaunches a bus: a host without one has no Secret
// Service to reach.
func dialSecretService() (secretService, error) {
	conn, err := dbus.SessionBusPrivateNoAutoStartup()
	if err != nil {
		return nil, fmt.Errorf("connect session bus: %w", err)
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("authenticate session bus: %w", err)
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("register on session bus: %w", err)
	}
	s := &dbusSecretService{conn: conn}
	var output dbus.Variant
	if err := s.call(secretServicePath, secretServiceIface+".OpenSession", secretAlgorithmPlain, dbus.MakeVariant("")).Store(&output, &s.session); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open session: %w", err)
	}
	return s, nil
}

func (s *dbusSecretService) call(path dbus.ObjectPath, method string, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(context.Background(), secretServiceCallTimeout)
	defer cancel()
	return s.conn.Object(secretServiceBusName, path).CallWithContext(ctx, method, 0, args...)
}

func (s *dbusSecretService) ReadAlias(name string) (dbus.ObjectPath, error) {
	var collection dbus.ObjectPath
	err := s.call(secretServicePath, secretServiceIface+".ReadAlias", name).Store(&collection)
	return collection, err
}

func (s *dbusSecretService) SearchItems(attributes map[string]string) ([]dbus.ObjectPath, []dbus.ObjectPath, error) {
	var unlocked, locked []dbus.ObjectPath
	err := s.call(secretServicePath, secretServiceIface+".SearchItems", attributes).Store(&unlocked, &locked)
	return unlocked, locked, err
}

func (s *dbusSecretService) CollectionLocked(collection dbus.ObjectPath) (bool, error) {
	var locked dbus.Variant
	if err := s.call(collection, dbusPropertiesGet, secretCollectionIface, "Locked").Store(&locked); err != nil {
		return false, err
	}
	value, ok := locked.Value().(bool)
	if !ok {
		return false, fmt.Errorf("collection Locked property has type %s", locked.Signature())
	}
	return value, nil
}

func (s *dbusSecretService) Unlock(objects []dbus.ObjectPath) error {
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	if err := s.call(secretServicePath, secretServiceIface+".Unlock", objects).Store(&unlocked, &prompt); err != nil {
		return err
	}
	return s.completePrompt(prompt)
}

func (s *dbusSecretService) GetSecret(item dbus.ObjectPath) ([]byte, error) {
	var secret secretValue
	if err := s.call(item, secretItemIface+".GetSecret", s.session).Store(&secret); err != nil {
		return nil, err
	}
	return secret.Value, nil
}

func (s *dbusSecretService) CreateItem(collection dbus.ObjectPath, label string, attributes map[string]string, secret []byte) error {
	properties := map[string]dbus.Variant{
		secretItemLabelProp: dbus.MakeVariant(label),
		secretItemAttrsProp: dbus.MakeVariant(attributes),
	}
	value := secretValue{Session: s.session, Parameters: []byte{}, Value: secret, ContentType: secretContentType}
	var item, prompt dbus.ObjectPath
	if err := s.call(collection, secretCollectionIface+".CreateItem", properties, value, false).Store(&item, &prompt); err != nil {
		return err
	}
	return s.completePrompt(prompt)
}

func (s *dbusSecretService) DeleteItem(item dbus.ObjectPath) error {
	var prompt dbus.ObjectPath
	if err := s.call(item, secretItemIface+".Delete").Store(&prompt); err != nil {
		return err
	}
	return s.completePrompt(prompt)
}

// completePrompt shows a service prompt and waits for its Completed signal.
// A dismissed or timed-out prompt leaves the object locked.
func (s *dbusSecretService) completePrompt(prompt dbus.ObjectPath) error {
	if prompt == noObject || prompt == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretServicePromptTimeout)
	defer cancel()

	match := []dbus.MatchOption{
		dbus.WithMatchObjectPath(prompt),
		dbus.WithMatchInterface(secretPromptIface),
		dbus.WithMatchMember("Completed"),
	}
	if err := s.conn.AddMatchSignalContext(ctx, match...); err != nil {
		return fmt.Errorf("subscribe to prompt %s: %w", prompt, err)
	}
	defer func() { _ = s.conn.RemoveMatchSignal(match...) }()
	signals := make(chan *dbus.Signal, 4)
	s.conn.Signal(signals)
	defer s.conn.RemoveSignal(signals)

	if err := s.call(prompt, secretPromptIface+".Prompt", "").Err; err != nil {
		return fmt.Errorf("show prompt %s: %w", prompt, err)
	}
	for {
		select {
		case sig := <-signals:
			if sig.Path != prompt || sig.Name != secretPromptIface+".Completed" || len(sig.Body) == 0 {
				continue
			}
			if dismissed, _ := sig.Body[0].(bool); dismissed {
				return fmt.Errorf("%w: prompt %s dismissed", constants.ErrKeyStoreLocked, prompt)
			}
			return nil
		case <-ctx.Done():
			_ = s.call(prompt, secretPromptIface+".Dismiss").Err
			return fmt.Errorf("%w: prompt %s: %w", constants.ErrKeyStoreLocked, prompt, ctx.Err())
		}
	}
}

func (s *dbusSecretService) Close() error {
	if s.session != "" && s.session != noObject {
		_ = s.call(s.session, secretSessionIface+".Close").Err
	}
	return s.conn.Close()
}
