// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func securityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "security",
		Short: "Security validation checks",
		Long:  `Run security validation and PKI verification checks.`,
	}

	cmd.AddCommand(
		securityValidateCmd(),
	)

	return cmd
}

func securityValidateCmd() *cobra.Command {
	return securityValidateCmdWithConfig(shared.NewFileSvc)
}

func securityValidateCmdWithConfig(fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error)) *cobra.Command {
	var pkiDir string
	var secretsDir string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Run security validation checks",
		Long: `Run security validation checks against the local ztd installation. Verifies
PKI certificate existence and validity, checks that the HTTPS port is active,
and confirms that the CA bundle is properly configured for mTLS.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Println("Running platform security validation...")
			failed := false

			fileSvc, err := fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			// Helper to check file existence through fileSvc
			checkFileExists := func(displayPath, relPath string) (bool, error) {
				if relPath != "" {
					return fileSvc.FileExists(ctx, relPath)
				}
				rPath, inRuntime, rErr := fs.RuntimeRelativePath(fileSvc, displayPath)
				if rErr != nil {
					return false, rErr
				}
				if inRuntime && rPath != "" {
					return fileSvc.FileExists(ctx, rPath)
				}
				_, statErr := os.Stat(displayPath)
				return statErr == nil, nil
			}

			// Helper to read file bytes through fileSvc
			readFileBytes := func(displayPath, relPath string) ([]byte, error) {
				if relPath != "" {
					return fileSvc.ReadFile(ctx, relPath)
				}
				rPath, inRuntime, rErr := fs.RuntimeRelativePath(fileSvc, displayPath)
				if rErr != nil {
					return nil, rErr
				}
				if inRuntime && rPath != "" {
					return fileSvc.ReadFile(ctx, rPath)
				}
				return os.ReadFile(displayPath)
			}

			// ─── 1. PKI Directory Structure ─────────────────────────────
			cmd.Println("\n=== PKI Directory Structure ===")
			type pkiCheckEntry struct {
				displayPath  string
				relPath      string
				mustNotExist bool // e.g. unsealed private keys that violate INV-AUTH-ID-04
				isOptional   bool
			}

			var pkiEntries []pkiCheckEntry
			var rootCARelPath, rootCADisplayPath string
			var rootKeyRelPath, rootKeyDisplayPath string
			var trustBundleRelPath, trustBundleDisplayPath string
			var actuatorPubRelPath, actuatorPubDisplayPath string
			var hubCARelPath, hubCADisplayPath string
			var opCARelPath, opCADisplayPath string
			var gpCARelPath, gpCADisplayPath string
			var svcCertRelPath, svcCertDisplayPath string
			var svcChainRelPath, svcChainDisplayPath string

			if pkiDir == "" {
				rootCARelPath = filepath.Join(constants.PkiDirname, constants.PkiSubdirRoot, constants.PkiFileRootCA)
				rootCADisplayPath = fileSvc.Resolve(rootCARelPath)
				rootKeyRelPath = filepath.Join(constants.PkiDirname, constants.PkiSubdirRoot, constants.PkiFileRootCAKey)
				rootKeyDisplayPath = fileSvc.Resolve(rootKeyRelPath)
				trustBundleRelPath = filepath.Join(constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle)
				trustBundleDisplayPath = fileSvc.Resolve(trustBundleRelPath)
				actuatorPubRelPath = filepath.Join(constants.PkiDirname, constants.ActuatorPubJSONFilename)
				actuatorPubDisplayPath = fileSvc.Resolve(actuatorPubRelPath)

				hubCARelPath = filepath.Join(constants.PkiDirname, constants.PkiSubdirAuthorities, constants.PkiFileHubCA)
				hubCADisplayPath = fileSvc.Resolve(hubCARelPath)
				opCARelPath = filepath.Join(constants.PkiDirname, constants.PkiSubdirAuthorities, constants.PkiFileOperatorCA)
				opCADisplayPath = fileSvc.Resolve(opCARelPath)
				gpCARelPath = filepath.Join(constants.PkiDirname, constants.PkiSubdirAuthorities, constants.PkiFileGatewayPeerCA)
				gpCADisplayPath = fileSvc.Resolve(gpCARelPath)

				svcCertRelPath = filepath.Join(constants.PkiDirname, constants.PkiSubdirIssued, constants.PkiSubdirHub, constants.PkiFileGatewayCert)
				svcCertDisplayPath = fileSvc.Resolve(svcCertRelPath)
				svcChainRelPath = filepath.Join(constants.PkiDirname, constants.PkiSubdirIssued, constants.PkiSubdirHub, constants.PkiFileGatewayChain)
				svcChainDisplayPath = fileSvc.Resolve(svcChainRelPath)

				pkiEntries = []pkiCheckEntry{
					{displayPath: rootCADisplayPath, relPath: rootCARelPath},
					{displayPath: rootKeyDisplayPath, relPath: rootKeyRelPath, mustNotExist: true},
					{displayPath: trustBundleDisplayPath, relPath: trustBundleRelPath},
					{displayPath: actuatorPubDisplayPath, relPath: actuatorPubRelPath},
					{displayPath: hubCADisplayPath, relPath: hubCARelPath},
					{displayPath: opCADisplayPath, relPath: opCARelPath},
					{displayPath: gpCADisplayPath, relPath: gpCARelPath, isOptional: true},
					{displayPath: svcCertDisplayPath, relPath: svcCertRelPath, isOptional: true},
					{displayPath: svcChainDisplayPath, relPath: svcChainRelPath, isOptional: true},
				}
			} else {
				rootCADisplayPath = filepath.Join(pkiDir, constants.PkiSubdirRoot, constants.PkiFileRootCA)
				rootKeyDisplayPath = filepath.Join(pkiDir, constants.PkiSubdirRoot, constants.PkiFileRootCAKey)
				trustBundleDisplayPath = filepath.Join(pkiDir, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle)
				actuatorPubDisplayPath = filepath.Join(pkiDir, constants.ActuatorPubJSONFilename)

				var errRoot, errKey, errTrust, errAct error
				rootCARelPath, _, errRoot = fs.RuntimeRelativePath(fileSvc, rootCADisplayPath)
				rootKeyRelPath, _, errKey = fs.RuntimeRelativePath(fileSvc, rootKeyDisplayPath)
				trustBundleRelPath, _, errTrust = fs.RuntimeRelativePath(fileSvc, trustBundleDisplayPath)
				actuatorPubRelPath, _, errAct = fs.RuntimeRelativePath(fileSvc, actuatorPubDisplayPath)
				if errRoot != nil || errKey != nil || errTrust != nil || errAct != nil {
					return fmt.Errorf("security: pki path validation: %w: %w", constants.ErrValidationFailed, constants.ErrPathValidation)
				}

				pkiEntries = []pkiCheckEntry{
					{displayPath: rootCADisplayPath, relPath: rootCARelPath},
					{displayPath: rootKeyDisplayPath, relPath: rootKeyRelPath, mustNotExist: true},
					{displayPath: trustBundleDisplayPath, relPath: trustBundleRelPath},
					{displayPath: actuatorPubDisplayPath, relPath: actuatorPubRelPath},
				}
			}

			for _, entry := range pkiEntries {
				exists, chkErr := checkFileExists(entry.displayPath, entry.relPath)
				if chkErr != nil {
					cmd.Printf("  [FAIL] %s: %v\n", entry.displayPath, chkErr)
					failed = true
					continue
				}

				if entry.mustNotExist {
					// Invariant check: unsealed CA private keys must never touch disk in the clear
					if exists {
						cmd.Printf("  [FAIL] %s exists unsealed on disk (violates INV-AUTH-ID-04 / INV-STOR-ENC-01)\n", entry.displayPath)
						failed = true
					} else {
						cmd.Printf("  [OK]   %s is not exposed (sealed in secrets store)\n", filepath.Base(entry.displayPath))
					}
					continue
				}

				if exists {
					cmd.Printf("  [OK]   %s exists\n", entry.displayPath)
				} else {
					// Check for legacy actuator key (warden_pub.pem) fallback
					if entry.displayPath == actuatorPubDisplayPath {
						legacyDisplay := filepath.Join(filepath.Dir(actuatorPubDisplayPath), constants.PkiFileWardenPub)
						var legacyRel string
						if pkiDir == "" {
							legacyRel = filepath.Join(constants.PkiDirname, constants.PkiFileWardenPub)
						} else {
							legacyRel, _, _ = fs.RuntimeRelativePath(fileSvc, legacyDisplay)
						}
						legacyExists, _ := checkFileExists(legacyDisplay, legacyRel)
						if legacyExists {
							cmd.Printf("  [OK]   %s exists (legacy actuator key)\n", legacyDisplay)
							continue
						}
					}

					if entry.isOptional {
						continue
					}
					cmd.Printf("  [FAIL] %s missing\n", entry.displayPath)
					failed = true
				}
			}

			// ─── 2. Secrets Directory ───────────────────────────────────
			cmd.Println("\n=== Secrets Directory ===")
			type secretCheckEntry struct {
				displayPath string
				relPath     string
				description string
				isOptional  bool
			}

			var secretEntries []secretCheckEntry
			if secretsDir == "" {
				actKeyRel := filepath.Join(constants.SecretsDirname, constants.SecretsFileActuatorSigningKey)
				bootDigestRel := filepath.Join(constants.SecretsDirname, constants.SecretsFileBootstrapDigest)
				caRootKeyRel := filepath.Join(constants.SecretsDirname, "ca_root_key")

				secretEntries = []secretCheckEntry{
					{displayPath: fileSvc.Resolve(actKeyRel), relPath: actKeyRel, description: "actuator signing key"},
					{displayPath: fileSvc.Resolve(bootDigestRel), relPath: bootDigestRel, description: "bootstrap digest"},
					{displayPath: fileSvc.Resolve(caRootKeyRel), relPath: caRootKeyRel, description: "sealed CA root key", isOptional: true},
				}
			} else {
				actKeyDisplay := filepath.Join(secretsDir, constants.SecretsFileActuatorSigningKey)
				bootDigestDisplay := filepath.Join(secretsDir, constants.SecretsFileBootstrapDigest)
				actKeyRel, _, errActKey := fs.RuntimeRelativePath(fileSvc, actKeyDisplay)
				bootDigestRel, _, errBoot := fs.RuntimeRelativePath(fileSvc, bootDigestDisplay)
				if errActKey != nil || errBoot != nil {
					return fmt.Errorf("security: secrets path validation: %w: %w", constants.ErrValidationFailed, constants.ErrPathValidation)
				}
				secretEntries = []secretCheckEntry{
					{displayPath: actKeyDisplay, relPath: actKeyRel, description: "actuator signing key"},
					{displayPath: bootDigestDisplay, relPath: bootDigestRel, description: "bootstrap digest"},
				}
			}

			for _, entry := range secretEntries {
				exists, chkErr := checkFileExists(entry.displayPath, entry.relPath)
				if chkErr != nil {
					cmd.Printf("  [FAIL] %s: %v\n", entry.displayPath, chkErr)
					failed = true
					continue
				}
				if exists {
					if entry.description != "" && entry.isOptional {
						cmd.Printf("  [OK]   %s exists (%s)\n", entry.displayPath, entry.description)
					} else {
						cmd.Printf("  [OK]   %s exists\n", entry.displayPath)
					}
				} else if !entry.isOptional {
					cmd.Printf("  [FAIL] %s missing\n", entry.displayPath)
					failed = true
				}
			}

			// Check secrets directory permissions on POSIX
			if runtime.GOOS != "windows" {
				var secInfo os.FileInfo
				var statErr error
				if secretsDir == "" {
					secInfo, statErr = fileSvc.Stat(ctx, constants.SecretsDirname)
				} else {
					rSecDir, inRuntime, _ := fs.RuntimeRelativePath(fileSvc, secretsDir)
					if inRuntime && rSecDir != "" {
						secInfo, statErr = fileSvc.Stat(ctx, rSecDir)
					}
				}
				if statErr == nil && secInfo != nil {
					perm := secInfo.Mode().Perm()
					if perm&0077 != 0 {
						cmd.Printf("  [WARN] Secrets directory permissions (%04o) are overly permissive (expected 0700)\n", perm)
					} else {
						cmd.Printf("  [OK]   Secrets directory permissions are private (%04o)\n", perm)
					}
				}
			}

			// ─── 3. Certificate Validation ──────────────────────────────
			cmd.Println("\n=== Certificate Validation ===")
			certData, err := readFileBytes(rootCADisplayPath, rootCARelPath)
			if err != nil && !errors.Is(err, constants.ErrNotFound) && !os.IsNotExist(err) {
				cmd.Printf("  [FAIL] failed to read root_ca.crt: %v\n", err)
				failed = true
			}

			var rootCert *x509.Certificate
			if certData != nil {
				block, _ := pem.Decode(certData)
				if block == nil || block.Type != "CERTIFICATE" {
					cmd.Printf("  [FAIL] root_ca.crt is not a valid PEM certificate\n")
					failed = true
				} else {
					cmd.Printf("  [OK]   root_ca.crt is valid PEM\n")
					var parseErr error
					rootCert, parseErr = x509.ParseCertificate(block.Bytes)
					if parseErr != nil {
						cmd.Printf("  [FAIL] root_ca.crt X.509 parse failed: %v\n", parseErr)
						failed = true
					} else {
						if !rootCert.IsCA {
							cmd.Printf("  [FAIL] root_ca.crt is not a Certificate Authority (CA: false)\n")
							failed = true
						} else {
							cmd.Printf("  [OK]   root_ca.crt is a Certificate Authority (CA: true)\n")
						}
						now := time.Now()
						if now.Before(rootCert.NotBefore) {
							cmd.Printf("  [FAIL] root_ca.crt is not yet valid (valid from %s)\n", rootCert.NotBefore.Format(time.RFC3339))
							failed = true
						} else if now.After(rootCert.NotAfter) {
							cmd.Printf("  [FAIL] root_ca.crt expired on %s\n", rootCert.NotAfter.Format(time.RFC3339))
							failed = true
						} else {
							daysLeft := int(time.Until(rootCert.NotAfter).Hours() / 24)
							cmd.Printf("  [OK]   root_ca.crt is active (expires in %d days)\n", daysLeft)
						}
					}
				}
			}

			// In default runtime mode, validate intermediate CAs against root CA
			if pkiDir == "" && rootCert != nil {
				validateIntermediate := func(name, displayPath, relPath string) *x509.Certificate {
					exists, _ := checkFileExists(displayPath, relPath)
					if !exists {
						return nil
					}
					data, readErr := readFileBytes(displayPath, relPath)
					if readErr != nil {
						return nil
					}
					blk, _ := pem.Decode(data)
					if blk == nil || blk.Type != "CERTIFICATE" {
						cmd.Printf("  [FAIL] %s is not a valid PEM certificate\n", name)
						failed = true
						return nil
					}
					c, pErr := x509.ParseCertificate(blk.Bytes)
					if pErr != nil {
						cmd.Printf("  [FAIL] %s X.509 parse failed: %v\n", name, pErr)
						failed = true
						return nil
					}
					if sigErr := c.CheckSignatureFrom(rootCert); sigErr != nil {
						cmd.Printf("  [FAIL] %s signature check against root CA failed: %v\n", name, sigErr)
						failed = true
					} else {
						cmd.Printf("  [OK]   %s is valid CA signed by root CA\n", name)
					}
					return c
				}

				hubCert := validateIntermediate("hub_ca.crt", hubCADisplayPath, hubCARelPath)
				validateIntermediate("operator_ca.crt", opCADisplayPath, opCARelPath)
				validateIntermediate("gateway_peer_ca.crt", gpCADisplayPath, gpCARelPath)

				// Validate service certificate against hub CA if present
				if svcExists, _ := checkFileExists(svcCertDisplayPath, svcCertRelPath); svcExists {
					svcData, readErr := readFileBytes(svcCertDisplayPath, svcCertRelPath)
					if readErr == nil {
						blk, _ := pem.Decode(svcData)
						if blk != nil && blk.Type == "CERTIFICATE" {
							svcCert, pErr := x509.ParseCertificate(blk.Bytes)
							if pErr == nil && hubCert != nil {
								if sigErr := svcCert.CheckSignatureFrom(hubCert); sigErr != nil {
									cmd.Printf("  [FAIL] operator-gateway.crt signature check against hub CA failed: %v\n", sigErr)
									failed = true
								} else {
									cmd.Printf("  [OK]   operator-gateway.crt is valid and signed by hub CA\n")
								}
							}
						}
					}
				}
			}

			// ─── 4. Port Availability & Listener Status ─────────────────
			cmd.Println("\n=== Port Availability ===")
			ports := []struct {
				port int
				name string
			}{
				{constants.Ports.OperatorHttp, "HTTP"},
				{constants.Ports.OperatorHttps, "HTTPS/mTLS"},
			}

			var gwRunning bool
			var gwPID int
			if pkiDir == "" {
				pm, pmErr := platform.NewProcessManager(fileSvc)
				if pmErr == nil {
					gwRunning, gwPID, _ = pm.OperatorStatus()
				}
			}

			for _, p := range ports {
				addr := fmt.Sprintf("127.0.0.1:%d", p.port)
				conn, dialErr := net.DialTimeout(string(constants.NetworkProtocolTCP), addr, 300*time.Millisecond)
				if dialErr == nil {
					conn.Close()
					if gwRunning && gwPID > 0 {
						cmd.Printf("  [OK]   Port %d is active (Gateway %s listener, PID %d)\n", p.port, p.name, gwPID)
					} else {
						cmd.Printf("  [OK]   Port %d is active (%s listener connected)\n", p.port, p.name)
					}
				} else {
					if gwRunning {
						cmd.Printf("  [FAIL] Port %d is not responding (Gateway online with PID %d)\n", p.port, gwPID)
						failed = true
					} else {
						listener, listenErr := net.Listen(string(constants.NetworkProtocolTCP), addr)
						if listenErr == nil {
							listener.Close()
							cmd.Printf("  [OK]   Port %d is available\n", p.port)
						} else {
							cmd.Printf("  [WARN] Port %d is in use by another process\n", p.port)
						}
					}
				}
			}

			// ─── 5. TLS Configuration ───────────────────────────────────
			cmd.Println("\n=== TLS Configuration ===")
			trustData, err := readFileBytes(trustBundleDisplayPath, trustBundleRelPath)
			if err != nil && !errors.Is(err, constants.ErrNotFound) && !os.IsNotExist(err) {
				cmd.Printf("  [FAIL] failed to read trust bundle: %v\n", err)
				failed = true
			}

			if trustData != nil {
				certPool := x509.NewCertPool()
				if !certPool.AppendCertsFromPEM(trustData) {
					cmd.Printf("  [FAIL] Trust bundle is not valid PEM\n")
					failed = true
				} else {
					cmd.Printf("  [OK]   Trust bundle is valid PEM\n")

					var bundleCerts []*x509.Certificate
					rest := trustData
					for len(rest) > 0 {
						var block *pem.Block
						block, rest = pem.Decode(rest)
						if block == nil {
							break
						}
						if block.Type == "CERTIFICATE" {
							c, cErr := x509.ParseCertificate(block.Bytes)
							if cErr == nil {
								bundleCerts = append(bundleCerts, c)
							}
						}
					}
					if len(bundleCerts) > 0 {
						hasExpired := false
						now := time.Now()
						for _, c := range bundleCerts {
							if now.After(c.NotAfter) {
								hasExpired = true
								cmd.Printf("  [FAIL] Trust bundle contains expired certificate: %s (expired %s)\n", c.Subject.CommonName, c.NotAfter.Format(time.RFC3339))
								failed = true
							}
						}
						if !hasExpired {
							cmd.Printf("  [OK]   Trust bundle contains %d valid CA certificate(s)\n", len(bundleCerts))
						}
					}
				}
			}

			// ─── 6. Storage & Database Permissions ──────────────────────
			if pkiDir == "" {
				cmd.Println("\n=== Storage & Permissions ===")
				dbRelPath := filepath.Join(constants.DataDirname, constants.DbFilename)
				dbExists, _ := fileSvc.FileExists(ctx, dbRelPath)
				if dbExists {
					cmd.Printf("  [OK]   Canonical database %s exists\n", constants.DbFilename)
					if runtime.GOOS != "windows" {
						dbInfo, statErr := fileSvc.Stat(ctx, dbRelPath)
						if statErr == nil {
							perm := dbInfo.Mode().Perm()
							if perm&0077 != 0 {
								cmd.Printf("  [WARN] Database permissions (%04o) are overly permissive (expected 0600)\n", perm)
							} else {
								cmd.Printf("  [OK]   Database permissions are private (%04o)\n", perm)
							}
						}
					}
				}
			}

			// ─── 7. Summary ─────────────────────────────────────────────
			cmd.Println("\n=== Summary ===")
			if failed {
				cmd.Println("[FAIL] Security validation failed")
				return fmt.Errorf("security: validate: %w", constants.ErrValidationFailed)
			}
			cmd.Println("[OK]   Security validation passed")
			return nil
		},
	}

	cmd.Flags().StringVar(&pkiDir, "pki-dir", "", "PKI directory (default: "+constants.RuntimeDirname+"/"+constants.PkiDirname+")")
	cmd.Flags().StringVar(&secretsDir, "secrets-dir", "", "Secrets directory (default: "+constants.RuntimeDirname+"/"+constants.SecretsDirname+")")

	return cmd
}
