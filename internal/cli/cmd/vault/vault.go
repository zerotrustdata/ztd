// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package vaultcmd

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Manage the encryption vault",
		Long: `Initialize, unlock, re-key, and manage the ztd encryption vault.

The vault key is stored only encrypted under the master key held by the OS key
store. It is never written or printed in the clear.`,
	}

	cmd.AddCommand(
		vaultInitCmd(),
		vaultUnlockCmd(),
		vaultRekeyCmd(),
		vaultStatusCmd(),
		vaultResetCmd(),
	)

	return cmd
}

type fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error)

// keystoreFactory opens the keystore that holds the vault key. create reports
// whether a missing master key may be generated; only init may create one.
type keystoreFactory func(fileSvc fs.RuntimeFileService, create bool) (*keystore.Keystore, error)

func openKeystore(fileSvc fs.RuntimeFileService, create bool) (*keystore.Keystore, error) {
	if create {
		return keystore.Open(fileSvc, slog.Default(), keystore.Options{})
	}
	return keystore.NewWithFS(fileSvc, slog.Default(), keystore.Options{})
}

func vaultInitCmd() *cobra.Command {
	return vaultInitCmdWithConfig(shared.NewFileSvc, openKeystore)
}

func vaultInitCmdWithConfig(newFileSvc fileSvcFactory, newKeystore keystoreFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a new encryption vault",
		Long:  `Generate a new encryption vault with a random key, stored encrypted under the master key.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fileSvc, err := newFileSvc("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			ks, err := newKeystore(fileSvc, true)
			if err != nil {
				return fmt.Errorf("vault init: %w", err)
			}
			if err := ks.InitVault(); err != nil {
				return fmt.Errorf("vault init: %w", err)
			}

			cmd.Printf("Vault initialized at %s\n", fileSvc.Resolve(constants.VaultDirname))
			cmd.Printf("Vault key sealed under the %s master key\n", ks.KeyringName())
			return nil
		},
	}
	return cmd
}

func vaultUnlockCmd() *cobra.Command {
	return vaultUnlockCmdWithConfig(shared.NewFileSvc, openKeystore)
}

func vaultUnlockCmdWithConfig(newFileSvc fileSvcFactory, newKeystore keystoreFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unlock",
		Short: "Verify the vault unlocks with the stored key",
		Long:  `Unlock the vault with the vault key held in the keystore to confirm the key and header match.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fileSvc, err := newFileSvc("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}

			vaultDirAbs := fileSvc.Resolve(constants.VaultDirname)
			headerExists, err := vault.VaultHeaderExists(fileSvc)
			if err != nil {
				return fmt.Errorf("vault unlock: check header: %w", err)
			}
			if !headerExists {
				return fmt.Errorf("%w: %s\nInitialize the vault first with:\nztd vault init", constants.ErrVaultNotInitialized, vaultDirAbs)
			}

			ks, err := newKeystore(fileSvc, false)
			if err != nil {
				return fmt.Errorf("vault unlock: %w", err)
			}
			privateKey, err := ks.LoadVaultKey()
			if err != nil {
				return fmt.Errorf("vault unlock: %w: %w", constants.ErrVaultKeyReadFailed, err)
			}
			defer vault.SecureZero(privateKey)

			v, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc})
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrVaultCreateFailed, err)
			}
			if err := v.Unlock(privateKey); err != nil {
				return fmt.Errorf("%w: %w", constants.ErrVaultUnlockFailed, err)
			}
			v.Lock()

			cmd.Println("Vault unlocked successfully")
			return nil
		},
	}
	return cmd
}

func vaultRekeyCmd() *cobra.Command {
	return vaultRekeyCmdWithConfig(shared.NewFileSvc, openKeystore)
}

func vaultRekeyCmdWithConfig(newFileSvc fileSvcFactory, newKeystore keystoreFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rekey",
		Short: "Re-key the vault with a new vault key",
		Long:  `Re-wrap the vault's DEK under a newly generated vault key and replace the stored key. Stop the runtime first.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fileSvc, err := newFileSvc("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}

			vaultDirAbs := fileSvc.Resolve(constants.VaultDirname)
			headerExists, err := vault.VaultHeaderExists(fileSvc)
			if err != nil {
				return fmt.Errorf("vault: check header: %w", err)
			}
			if !headerExists {
				return fmt.Errorf("%w: %s", constants.ErrVaultNotInitialized, vaultDirAbs)
			}

			ks, err := newKeystore(fileSvc, false)
			if err != nil {
				return fmt.Errorf("vault rekey: %w", err)
			}
			if err := ks.RekeyVault(); err != nil {
				return fmt.Errorf("vault rekey: %w", err)
			}

			cmd.Println("Vault rekeyed successfully")
			return nil
		},
	}
	return cmd
}

func vaultStatusCmd() *cobra.Command {
	return vaultStatusCmdWithConfig(shared.NewFileSvc)
}

func vaultStatusCmdWithConfig(newFileSvc fileSvcFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show vault status",
		Long:  `Display whether the vault is initialized and unlocked.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fileSvc, err := newFileSvc("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}

			vaultDirAbs := fileSvc.Resolve(constants.VaultDirname)
			v, err := vault.NewVault(&vault.VaultConfig{
				FileSvc: fileSvc,
				Logger:  nil,
			})
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrVaultCreateFailed, err)
			}

			initialized := v.IsInitialized()
			unlocked := v.IsUnlocked()

			cmd.Printf("Vault directory: %s\n", vaultDirAbs)
			if initialized {
				cmd.Println("Status: initialized")
			} else {
				cmd.Println("Status: not initialized")
			}
			if unlocked {
				cmd.Println("Lock state: unlocked")
			} else {
				cmd.Println("Lock state: locked")
			}

			return nil
		},
	}

	return cmd
}

func vaultResetCmd() *cobra.Command {
	return vaultResetCmdWithConfig(shared.NewFileSvc)
}

func vaultResetCmdWithConfig(newFileSvc fileSvcFactory) *cobra.Command {
	var confirm bool

	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Destroy the vault and all encrypted data",
		Long:  `Reset the vault completely. This is a destructive operation that makes all encrypted data unrecoverable.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fileSvc, err := newFileSvc("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}

			vaultDirAbs := fileSvc.Resolve(constants.VaultDirname)
			headerExists, err := vault.VaultHeaderExists(fileSvc)
			if err != nil {
				return fmt.Errorf("vault: check header: %w", err)
			}
			if !headerExists {
				return fmt.Errorf("%w: %s", constants.ErrVaultNotInitialized, vaultDirAbs)
			}

			if !confirm {
				reader := bufio.NewReader(cmd.InOrStdin())
				cmd.Printf("WARNING: This will destroy the vault at %s and all encrypted data will be unrecoverable.\n", vaultDirAbs)
				cmd.Print("Type 'destroy' to confirm: ")
				input, err := reader.ReadString('\n')
				if err != nil {
					return fmt.Errorf("%w: %w", constants.ErrVaultStdinReadFailed, err)
				}
				if strings.TrimSpace(input) != "destroy" {
					cmd.Println("Reset cancelled.")
					return nil
				}
			}

			v, err := vault.NewVault(&vault.VaultConfig{
				FileSvc: fileSvc,
				Logger:  nil,
			})
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrVaultCreateFailed, err)
			}

			if err := v.Reset(true); err != nil {
				return fmt.Errorf("%w: %w", constants.ErrVaultResetFailed, err)
			}
			if err := fileSvc.Remove(context.Background(), filepath.Join(constants.SecretsDirname, constants.SecretsFileVaultKey)); err != nil {
				return fmt.Errorf("%w: remove sealed vault key: %w", constants.ErrVaultResetFailed, err)
			}

			cmd.Println("Vault reset complete. All encrypted data has been destroyed.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&confirm, "confirm", false, "Skip interactive confirmation (dangerous)")

	return cmd
}
