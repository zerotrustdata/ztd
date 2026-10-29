// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux && integration

package gw

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestFindGatewaySealingKey_UsesMatchingPublicSidecar(t *testing.T) {
	keyDir := testutil.TempDir(t)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPublic, err := ssh.NewPublicKey(public)
	require.NoError(t, err)
	fingerprint := ssh.FingerprintSHA256(sshPublic)
	keyPath := filepath.Join(keyDir, "custom key'$(literal)")
	require.NoError(t, os.WriteFile(keyPath+".pub", ssh.MarshalAuthorizedKey(sshPublic), constants.PermFilePublic))

	got, err := findGatewaySealingKey(keyDir, fingerprint)
	require.NoError(t, err)
	assert.Empty(t, got, "a public key without a local private-key file cannot be offered for ssh-add")
	// Deliberately invalid private material proves discovery never reads it.
	require.NoError(t, os.WriteFile(keyPath, []byte("not parsed by ztd"), constants.PermFilePrivate))
	got, err = findGatewaySealingKey(keyDir, fingerprint)
	require.NoError(t, err)
	assert.Equal(t, keyPath, got)
	got, err = findGatewaySealingKey(keyDir, "SHA256:another-key")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestGatewayKeystoreRecovery_RealSSHAddPreservesSeal(t *testing.T) {
	sshAdd, err := exec.LookPath("ssh-add")
	if err != nil {
		t.Skip("OpenSSH ssh-add is required")
	}
	ctx := testutil.Context(t)
	files, _ := cmdtest.NewCmdTestEnv(t)
	keyDir := testutil.TempDir(t)
	keyPath := filepath.Join(keyDir, "seal key")
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	privatePEM, err := ssh.MarshalPrivateKey(privateKey, "test-only recovery key")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(privatePEM), constants.PermFilePrivate))
	publicKey, err := ssh.NewPublicKey(privateKey.Public())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyPath+".pub", ssh.MarshalAuthorizedKey(publicKey), constants.PermFilePublic))

	socket := filepath.Join(keyDir, "agent.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	require.NoError(t, err)
	keys := agent.NewKeyring()
	require.NoError(t, keys.Add(agent.AddedKey{PrivateKey: privateKey}))
	var serving sync.WaitGroup
	serving.Add(1)
	go func() {
		defer serving.Done()
		for {
			connection, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			serving.Add(1)
			go func() {
				defer serving.Done()
				defer connection.Close()
				_ = agent.ServeAgent(keys, connection)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		serving.Wait()
	})
	t.Setenv(string(constants.EnvVar.SSHAuthSock), socket)
	t.Setenv(string(constants.EnvVar.DBusSessionBusAddress), "unix:path="+filepath.Join(keyDir, "no-session-bus"))
	store, err := keystore.Open(files, testutil.NewTestLogger(), keystore.Options{})
	require.NoError(t, err)
	require.NoError(t, store.EncryptSecret("recovery_probe", "still decrypts"))
	store.Close()
	originalSeal, err := files.ReadFile(ctx, constants.MasterKeySSHAgentRelPath)
	require.NoError(t, err)
	require.NoError(t, keys.RemoveAll())
	require.ErrorIs(t, keystore.CheckSSHAgentMasterKey(ctx, files), constants.ErrKeyStoreSSHAgentKeyMissing)

	cmd := &cobra.Command{Use: "start"}
	cmd.SetContext(ctx)
	cmd.SetIn(strings.NewReader("yes\n"))
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	require.NoError(t, recoverGatewayKeystore(cmd, gatewayKeystoreRecovery{
		check: func() error { return keystore.CheckSSHAgentMasterKey(ctx, files) },
		findKey: func(fingerprint string) (string, error) {
			return findGatewaySealingKey(keyDir, fingerprint)
		},
		loadKey: func(ctx context.Context, path string) error {
			add := exec.CommandContext(ctx, sshAdd, "-t", constants.KeyStoreSSHAgentKeyLifetime, path)
			add.Stdout, add.Stderr = &output, &output
			return add.Run()
		},
		interactive: true,
		agentSocket: socket,
	}))
	unchangedSeal, err := files.ReadFile(ctx, constants.MasterKeySSHAgentRelPath)
	require.NoError(t, err)
	assert.Equal(t, originalSeal, unchangedSeal)
	reopened, err := keystore.Open(files, testutil.NewTestLogger(), keystore.Options{})
	require.NoError(t, err)
	defer reopened.Close()
	plaintext, err := reopened.DecryptSecret("recovery_probe")
	require.NoError(t, err)
	assert.Equal(t, "still decrypts", plaintext)
}

func TestGatewayStart_LockedSealStopsBeforeNetworkDetection(t *testing.T) {
	for _, follow := range []bool{false, true} {
		t.Run(fmt.Sprintf("follow=%t", follow), func(t *testing.T) {
			files, cfg := cmdtest.NewCmdTestEnv(t)
			require.NoError(t, files.WriteFile(testutil.Context(t), constants.MasterKeySSHAgentRelPath, []byte("corrupt seal"), constants.PermFilePrivate))
			cmd := gatewayStartCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(files), nil)
			cmd.SetContext(testutil.Context(t))
			require.NoError(t, cmd.Flags().Set("follow", strconv.FormatBool(follow)))
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			err := cmd.RunE(cmd, nil)
			require.ErrorIs(t, err, constants.ErrKeyStoreDecodeFailed)
			assert.NotContains(t, out.String(), "Detected network identity")
			assert.NotContains(t, out.String(), "Starting ztd Gateway")
		})
	}
}

func TestGatewayRestart_LockedSealLeavesRunningProcessAlone(t *testing.T) {
	ctx := testutil.Context(t)
	files, cfg := cmdtest.NewCmdTestEnv(t)
	require.NoError(t, serve.WriteLaunchProfile(files, serve.GatewayConfig{
		Posture:    constants.PostureConsensus,
		LogLevel:   constants.LogLevelInfo,
		ListenHost: constants.LocalhostIP,
		HTTPPort:   constants.Ports.OperatorHttp,
		HTTPSPort:  constants.Ports.OperatorHttps,
	}))
	require.NoError(t, files.WriteFile(ctx, constants.MasterKeySSHAgentRelPath, []byte("corrupt seal"), constants.PermFilePrivate))
	process := exec.CommandContext(ctx, "sleep", "60")
	require.NoError(t, process.Start())
	t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	pidPath := filepath.Join(constants.PidDirname, constants.OperatorPIDFilename)
	require.NoError(t, files.WriteFile(ctx, pidPath, []byte(strconv.Itoa(process.Process.Pid)), constants.PermFilePrivate))
	cmd := gatewayRestartCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(files))
	cmd.SetContext(ctx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.RunE(cmd, nil)
	require.ErrorIs(t, err, constants.ErrKeyStoreDecodeFailed)
	require.NoError(t, process.Process.Signal(syscall.Signal(0)), "unlock must succeed before a running process is stopped")
	assert.NotContains(t, out.String(), "Stopping ztd Gateway")
}
