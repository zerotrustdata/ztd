// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux

package gw

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/constants"
	sshutil "github.com/zerotrustdata/ztd/internal/pkg/ssh"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
)

type gatewayKeystoreRecovery struct {
	check       func() error
	findKey     func(string) (string, error)
	loadKey     func(context.Context, string) error
	interactive bool
	agentSocket string
}

// prepareGatewayKeystore runs while the invoking terminal is still available,
// before network detection, detaching a child, or stopping a healthy Gateway.
func prepareGatewayKeystore(cmd *cobra.Command, files fs.RuntimeFileService) error {
	interactive := false
	if input, ok := cmd.InOrStdin().(*os.File); ok {
		_, err := unix.IoctlGetTermios(int(input.Fd()), unix.TCGETS)
		interactive = err == nil
	}
	return recoverGatewayKeystore(cmd, gatewayKeystoreRecovery{
		check: func() error { return keystore.CheckSSHAgentMasterKey(shared.CommandContext(cmd), files) },
		findKey: func(fingerprint string) (string, error) {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("locate SSH public keys: %w", err)
			}
			return findGatewaySealingKey(filepath.Join(homeDir, constants.SshDirname), fingerprint)
		},
		loadKey: func(ctx context.Context, keyPath string) error {
			// No shell, no private-key parsing, and no passphrase in ztd's
			// arguments, environment, or logs. OpenSSH owns the passphrase prompt.
			add := exec.CommandContext(ctx, "ssh-add", "-t", constants.KeyStoreSSHAgentKeyLifetime, keyPath)
			add.Stdin, add.Stdout, add.Stderr = cmd.InOrStdin(), cmd.ErrOrStderr(), cmd.ErrOrStderr()
			return add.Run()
		},
		interactive: interactive,
		agentSocket: os.Getenv(string(constants.EnvVar.SSHAuthSock)),
	})
}

func recoverGatewayKeystore(cmd *cobra.Command, recovery gatewayKeystoreRecovery) error {
	if err := shared.CommandContext(cmd).Err(); err != nil {
		return err
	}
	unlockErr := recovery.check()
	if unlockErr == nil {
		return nil
	}
	var required *keystore.SSHAgentUnlockError
	if !errors.As(unlockErr, &required) {
		return fmt.Errorf("gateway: check master key: %w", unlockErr)
	}
	cmd.PrintErrf("[ztd] The vault requires SSH key %s.\n", required.Fingerprint)
	keyPath, err := recovery.findKey(required.Fingerprint)
	if err != nil {
		return fmt.Errorf("gateway: recover master key: %w; %w", unlockErr, err)
	}
	if keyPath == "" {
		cmd.PrintErrln("[ztd] No matching local key pair was found under ~/.ssh. Compare these public fingerprints:")
		cmd.PrintErrln("  for ztd_pub in \"$HOME\"/.ssh/*.pub; do")
		cmd.PrintErrln("    [ -f \"$ztd_pub\" ] && ssh-keygen -lf \"$ztd_pub\"")
		cmd.PrintErrln("  done")
		cmd.PrintErrln("  ssh-add -l")
		cmd.PrintErrln("[ztd] Restore the original key pair, or reconnect with agent forwarding from the machine holding that key, then retry:")
		cmd.PrintErrln("  " + gatewayKeystoreRetryCommand(cmd))
		cmd.PrintErrln("[ztd] A new SSH key cannot unlock this vault. Keep the existing runtime and master-key seal.")
		return fmt.Errorf("gateway: unlock master key: %w", unlockErr)
	}

	cmd.PrintErrf("[ztd] Matching key: %q\n", keyPath)
	cmd.PrintErrln("[ztd] Copy and paste in this same terminal:")
	if recovery.agentSocket == "" || errors.Is(unlockErr, constants.ErrKeyStoreSSHAgentUnavailable) {
		cmd.PrintErrln("  eval \"$(ssh-agent -s)\"")
	}
	cmd.PrintErrf("  ssh-add -t %s %s\n", constants.KeyStoreSSHAgentKeyLifetime, sshutil.QuoteArg(keyPath))
	cmd.PrintErrln("  ssh-add -l")
	cmd.PrintErrln("  " + gatewayKeystoreRetryCommand(cmd))
	cmd.PrintErrln("[ztd] Enter the SSH key's passphrase if asked. The key expires from the agent after 8 hours; the running Gateway keeps its unlocked key until it stops.")

	// An unavailable agent requires a shell export, which a child cannot apply
	// to its parent. Never silently create another agent or change SSH_AUTH_SOCK.
	if !recovery.interactive || recovery.agentSocket == "" || !errors.Is(unlockErr, constants.ErrKeyStoreSSHAgentKeyMissing) {
		return fmt.Errorf("gateway: unlock master key: %w", unlockErr)
	}
	previousOut := cmd.OutOrStdout()
	cmd.SetOut(cmd.ErrOrStderr())
	approved := shared.AskYesNo(cmd, bufio.NewReader(cmd.InOrStdin()), "Load this key into the current ssh-agent for 8 hours and continue?", false)
	cmd.SetOut(previousOut)
	if !approved {
		return fmt.Errorf("gateway: unlock master key: %w", unlockErr)
	}
	if err := shared.CommandContext(cmd).Err(); err != nil {
		return err
	}
	if err := recovery.loadKey(shared.CommandContext(cmd), keyPath); err != nil {
		return fmt.Errorf("gateway: ssh-add: %w; %w", unlockErr, err)
	}
	if err := recovery.check(); err != nil {
		return fmt.Errorf("gateway: verify master key after ssh-add: %w", err)
	}
	cmd.PrintErrln("[ztd] Master key unlocked; continuing Gateway startup.")
	return nil
}

// findGatewaySealingKey reads public sidecars only. OpenSSH reads the private
// key after consent; the master key is checked again before startup continues.
func findGatewaySealingKey(keyDir, fingerprint string) (string, error) {
	entries, err := os.ReadDir(keyDir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("list SSH public keys: %w", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".pub") {
			continue
		}
		publicPath := filepath.Join(keyDir, entry.Name())
		data, err := os.ReadFile(publicPath)
		if err != nil {
			return "", fmt.Errorf("read SSH public key %q: %w", publicPath, err)
		}
		publicKey, _, _, _, err := ssh.ParseAuthorizedKey(data)
		if err != nil || ssh.FingerprintSHA256(publicKey) != fingerprint {
			continue
		}
		privatePath := strings.TrimSuffix(publicPath, ".pub")
		info, err := os.Stat(privatePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("stat SSH key %q: %w", privatePath, err)
		}
		if info.Mode().IsRegular() {
			return privatePath, nil
		}
	}
	return "", nil
}

func gatewayKeystoreRetryCommand(cmd *cobra.Command) string {
	parts := []string{getBinaryName(), "gw", cmd.Name()}
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		values := []string{flag.Value.String()}
		if slice, ok := flag.Value.(pflag.SliceValue); ok {
			values = slice.GetSlice()
		}
		for _, value := range values {
			parts = append(parts, "--"+flag.Name+"="+sshutil.QuoteArg(value))
		}
	})
	return strings.Join(parts, " ")
}
