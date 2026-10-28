// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux

package gw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
)

func TestGatewayKeystoreRecovery(t *testing.T) {
	const fingerprint = "SHA256:original-sealing-key"
	const keyPath = "/home/example user/.ssh/key'$(touch should-not-run)"
	for _, tc := range []struct {
		name        string
		answer      string
		interactive bool
		socket      string
		keyPath     string
		cause       error
		loadErr     error
		verifyErr   error
		wantLoads   int
		wantChecks  int
		wantSuccess bool
	}{
		{name: "approved key is loaded and verified", answer: "yes\n", interactive: true, socket: "/agent", keyPath: keyPath, cause: constants.ErrKeyStoreSSHAgentKeyMissing, wantLoads: 1, wantChecks: 2, wantSuccess: true},
		{name: "declined", answer: "no\n", interactive: true, socket: "/agent", keyPath: keyPath, cause: constants.ErrKeyStoreSSHAgentKeyMissing, wantChecks: 1},
		{name: "empty answer declines", answer: "\n", interactive: true, socket: "/agent", keyPath: keyPath, cause: constants.ErrKeyStoreSSHAgentKeyMissing, wantChecks: 1},
		{name: "EOF declines", interactive: true, socket: "/agent", keyPath: keyPath, cause: constants.ErrKeyStoreSSHAgentKeyMissing, wantChecks: 1},
		{name: "pipe cannot approve", answer: "yes\n", socket: "/agent", keyPath: keyPath, cause: constants.ErrKeyStoreSSHAgentKeyMissing, wantChecks: 1},
		{name: "stale socket gives shell instructions", answer: "yes\n", interactive: true, socket: "/stale", keyPath: keyPath, cause: constants.ErrKeyStoreSSHAgentUnavailable, wantChecks: 1},
		{name: "unset socket needs shell export", answer: "yes\n", interactive: true, keyPath: keyPath, cause: constants.ErrKeyStoreSSHAgentKeyMissing, wantChecks: 1},
		{name: "unmatched key needs original", answer: "yes\n", interactive: true, socket: "/agent", cause: constants.ErrKeyStoreSSHAgentKeyMissing, wantChecks: 1},
		{name: "ssh-add failure stops startup", answer: "y\n", interactive: true, socket: "/agent", keyPath: keyPath, cause: constants.ErrKeyStoreSSHAgentKeyMissing, loadErr: constants.ErrKeyStoreLocked, wantLoads: 1, wantChecks: 1},
		{name: "wrong loaded key stops startup", answer: "y\n", interactive: true, socket: "/agent", keyPath: keyPath, cause: constants.ErrKeyStoreSSHAgentKeyMissing, verifyErr: constants.ErrKeyStoreSSHAgentKeyMissing, wantLoads: 1, wantChecks: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cmd := &cobra.Command{Use: "start"}
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetIn(strings.NewReader(tc.answer))
			initial := &keystore.SSHAgentUnlockError{Fingerprint: fingerprint, Err: tc.cause}
			checks, loads := 0, 0
			err := recoverGatewayKeystore(cmd, gatewayKeystoreRecovery{
				check: func() error {
					checks++
					if checks == 1 {
						return fmt.Errorf("master key: %w", initial)
					}
					return tc.verifyErr
				},
				findKey: func(got string) (string, error) {
					assert.Equal(t, fingerprint, got)
					return tc.keyPath, nil
				},
				loadKey: func(_ context.Context, got string) error {
					loads++
					assert.Equal(t, keyPath, got)
					return tc.loadErr
				},
				interactive: tc.interactive,
				agentSocket: tc.socket,
			})
			if tc.wantSuccess {
				require.NoError(t, err)
				assert.Contains(t, stderr.String(), "continuing Gateway startup")
			} else {
				require.ErrorIs(t, err, tc.cause)
			}
			assert.Equal(t, tc.wantLoads, loads)
			assert.Equal(t, tc.wantChecks, checks)
			assert.Empty(t, stdout.String(), "recovery must stay visible even when --quiet discards normal output")
			assert.Contains(t, stderr.String(), fingerprint)
			assert.Contains(t, stderr.String(), "./ztd gw start")
			if tc.keyPath != "" {
				assert.Contains(t, stderr.String(), `ssh-add -t 8h '/home/example user/.ssh/key'"'"'$(touch should-not-run)'`)
				assert.Contains(t, stderr.String(), "SSH key's passphrase")
			} else {
				assert.Contains(t, stderr.String(), "ssh-keygen -lf")
				assert.Contains(t, stderr.String(), "A new SSH key cannot unlock this vault")
			}
			if tc.socket == "" || errors.Is(tc.cause, constants.ErrKeyStoreSSHAgentUnavailable) {
				assert.Contains(t, stderr.String(), `eval "$(ssh-agent -s)"`)
			}
		})
	}
}

func TestGatewayKeystoreRecovery_HealthyAndUntrustedDoNotPrompt(t *testing.T) {
	for _, cause := range []error{nil, constants.ErrKeyStoreSSHAgentUntrusted, constants.ErrKeyStoreDecodeFailed} {
		cmd := &cobra.Command{Use: "start"}
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		err := recoverGatewayKeystore(cmd, gatewayKeystoreRecovery{
			check: func() error { return cause },
			findKey: func(string) (string, error) {
				t.Fatal("must not search or prompt for a healthy, corrupt, or untrusted keystore")
				return "", nil
			},
		})
		if cause == nil {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, cause)
		}
		assert.Empty(t, out.String())
	}
}

func TestGatewayKeystoreRetryCommand_PreservesFlags(t *testing.T) {
	cmd := gatewayStartCmdWithConfig(nil, nil, nil)
	require.NoError(t, cmd.ParseFlags([]string{
		"--listen-host=0.0.0.0", "--public-spectator", "--capabilities=inference,data", "--posture=consensus",
		"--cors-origin=https://one.example", "--cors-origin=https://two.example", "--follow",
		"--model-storage-root=/model path/with'$(injection)",
	}))
	got := gatewayKeystoreRetryCommand(cmd)
	assert.Contains(t, got, "./ztd gw start")
	assert.Contains(t, got, "--listen-host='0.0.0.0'")
	assert.Contains(t, got, "--public-spectator='true'")
	assert.Contains(t, got, "--capabilities='data,inference'")
	assert.Contains(t, got, "--posture='consensus'")
	assert.Contains(t, got, "--follow='true'")
	assert.Contains(t, got, "--cors-origin='https://one.example' --cors-origin='https://two.example'")
	assert.Contains(t, got, `--model-storage-root='/model path/with'"'"'$(injection)'`)
}
