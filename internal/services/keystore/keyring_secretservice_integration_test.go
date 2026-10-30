// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux && integration

package keystore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

const privateBusMarker = "ztd-private-test-bus"

// runOnPrivateSecretService re-executes the named test inside dbus-run-session
// with HOME and XDG directories under a temp root, so gnome-keyring-daemon
// never sees the developer's keyrings or session bus.
func runOnPrivateSecretService(t *testing.T, name string) bool {
	t.Helper()
	if os.Args[len(os.Args)-1] == privateBusMarker {
		return true
	}
	for _, bin := range []string{"dbus-run-session", "gnome-keyring-daemon"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s unavailable", bin)
		}
	}
	root := testutil.TempDir(t)
	env := os.Environ()
	for _, dir := range []string{"home", "data", "config", "runtime"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o700))
	}
	env = append(env,
		"HOME="+filepath.Join(root, "home"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_RUNTIME_DIR="+filepath.Join(root, "runtime"),
		"DBUS_SESSION_BUS_ADDRESS=",
	)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "dbus-run-session", "--", os.Args[0], "-test.run=^"+name+"$", "-test.v", "--", privateBusMarker)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	if bytes.Contains(out, []byte("--- SKIP:")) {
		t.Skipf("private Secret Service check skipped: %s", out)
	}
	t.Logf("%s", out)
	return false
}

// startLoginKeyring runs gnome-keyring-daemon with an empty-password login
// keyring, which it registers as the default collection, and returns once the
// daemon owns the Secret Service bus name. Waiting on NameOwnerChanged keeps
// a call from D-Bus-activating a second daemon that has no login keyring.
func startLoginKeyring(t *testing.T) {
	t.Helper()
	conn, err := dbus.ConnectSessionBus()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	match := []dbus.MatchOption{
		dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, secretServiceBusName),
	}
	require.NoError(t, conn.AddMatchSignal(match...))
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)

	cmd := exec.Command("gnome-keyring-daemon", "--foreground", "--unlock", "--components=secrets",
		"--control-directory="+filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "keyring"))
	cmd.Stdin = strings.NewReader("\n")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	timeout := time.After(30 * time.Second)
	for {
		select {
		case sig := <-signals:
			if len(sig.Body) == 3 && sig.Body[0] == secretServiceBusName && sig.Body[2] != "" {
				return
			}
		case <-timeout:
			t.Fatal("gnome-keyring-daemon did not claim " + secretServiceBusName)
		}
	}
}

func TestSecretServiceKeyring_RealDaemonRoundTrip(t *testing.T) {
	if !runOnPrivateSecretService(t, "TestSecretServiceKeyring_RealDaemonRoundTrip") {
		return
	}
	startLoginKeyring(t)

	kr, err := newSecretServiceKeyring(dialSecretService)
	require.NoError(t, err)
	key := bytes.Repeat([]byte{7}, vault.KeySize)
	require.NoError(t, kr.StoreMasterKey(key))
	got, err := kr.RetrieveMasterKey()
	require.NoError(t, err)
	require.Equal(t, key, got)

	require.NoError(t, kr.DeleteMasterKey())
	_, err = kr.RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)
}

func TestSecretServiceKeyring_RealDaemonWithoutDefaultCollection(t *testing.T) {
	if !runOnPrivateSecretService(t, "TestSecretServiceKeyring_RealDaemonWithoutDefaultCollection") {
		return
	}
	// D-Bus activation starts gnome-keyring with only the in-memory session
	// collection, the state of a host whose login keyring was never created.
	_, err := newSecretServiceKeyring(dialSecretService)
	if secretServiceNotActivatable(err) {
		// gnome-keyring 48 (Debian 13) ships systemd user units and no D-Bus
		// activation file, so this host cannot start the daemon on demand.
		t.Skipf("org.freedesktop.secrets is not D-Bus activatable here: %v", err)
	}
	require.ErrorIs(t, err, constants.ErrKeyStoreNoDefaultCollection)
}

// secretServiceNotActivatable reports the D-Bus error a session bus returns
// when no .service file provides org.freedesktop.secrets. The check is the
// error name, not its rendered text.
func secretServiceNotActivatable(err error) bool {
	const serviceUnknown = "org.freedesktop.DBus.Error.ServiceUnknown"
	var asPtr *dbus.Error
	if errors.As(err, &asPtr) && asPtr != nil && asPtr.Name == serviceUnknown {
		return true
	}
	var asVal dbus.Error
	return errors.As(err, &asVal) && asVal.Name == serviceUnknown
}

func TestSecretServiceKeyring_StoreFailureHasGuidance(t *testing.T) {
	fake := newFakeSecretService()
	kr, err := newSecretServiceKeyring(fake.dial)
	require.NoError(t, err)
	fake.defaultAlias = noObject
	fileSvc, _ := setupTestFileService(t)
	ks, err := NewWithKeyringAndFS(testutil.NewTestLogger(), kr, fileSvc)
	require.NoError(t, err)
	err = ks.Initialize()
	require.ErrorIs(t, err, constants.ErrKeyStoreNoDefaultCollection)
	require.Contains(t, err.Error(), "ssh-agent")
	require.Empty(t, fake.items)
}

func TestKeystore_NoImplicitHomeKeyDiscovery(t *testing.T) {
	dir := testutil.TempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".ztd_master_key"), []byte("unused"), constants.PermFilePrivate))
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	// No reachable session bus, so the developer's Secret Service is never touched.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(dir, "no-bus"))
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv(string(constants.EnvVar.SSHAuthSock), "")
	fileSvc, _ := setupTestFileService(t)
	_, err := NewWithFS(fileSvc, testutil.NewTestLogger(), Options{})
	require.ErrorIs(t, err, constants.ErrKeyStoreOSKeyringRequired)
	require.ErrorIs(t, err, constants.ErrKeyStoreSecretServiceDown)
	require.Contains(t, err.Error(), "ssh-agent")
}
