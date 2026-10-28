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

// securityPath is a path shown to the operator and, when it lives inside the
// runtime root, its service-relative form.
type securityPath struct {
	display string
	rel     string
}

// securityPKIPaths holds every PKI path the validation reads. The authority,
// service certificate, and chain entries are populated only for the default
// runtime layout; an explicit --pki-dir checks the four core files.
type securityPKIPaths struct {
	rootCA        securityPath
	rootKey       securityPath
	trustBundle   securityPath
	actuatorPub   securityPath
	hubCA         securityPath
	operatorCA    securityPath
	gatewayPeerCA securityPath
	serviceCert   securityPath
	serviceChain  securityPath
}

type securityPKICheck struct {
	path         securityPath
	mustNotExist bool // e.g. unsealed private keys that violate INV-AUTH-ID-04
	isOptional   bool
}

type securitySecretCheck struct {
	path        securityPath
	description string
	isOptional  bool
}

// securityValidator runs the validation sections in order and accumulates the
// pass/fail state. Every file read goes through fileSvc.
type securityValidator struct {
	cmd        *cobra.Command
	fileSvc    fs.RuntimeFileService
	ctx        context.Context
	pkiDir     string
	secretsDir string
	failed     bool
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

			fileSvc, err := fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			v := &securityValidator{cmd: cmd, fileSvc: fileSvc, ctx: ctx, pkiDir: pkiDir, secretsDir: secretsDir}
			return v.run()
		},
	}

	cmd.Flags().StringVar(&pkiDir, "pki-dir", "", "PKI directory (default: "+constants.RuntimeDirname+"/"+constants.PkiDirname+")")
	cmd.Flags().StringVar(&secretsDir, "secrets-dir", "", "Secrets directory (default: "+constants.RuntimeDirname+"/"+constants.SecretsDirname+")")

	return cmd
}

func (v *securityValidator) run() error {
	v.cmd.Println("\n=== PKI Directory Structure ===")
	paths, err := v.pkiPaths()
	if err != nil {
		return err
	}
	v.checkPKIDirectory(paths)

	v.cmd.Println("\n=== Secrets Directory ===")
	if err := v.checkSecretsDirectory(); err != nil {
		return err
	}

	v.cmd.Println("\n=== Certificate Validation ===")
	rootCert := v.checkRootCA(paths.rootCA)
	if v.pkiDir == "" && rootCert != nil {
		v.checkIntermediateChain(paths, rootCert)
	}

	v.cmd.Println("\n=== Port Availability ===")
	v.checkPorts()

	v.cmd.Println("\n=== TLS Configuration ===")
	v.checkTrustBundle(paths.trustBundle)

	if v.pkiDir == "" {
		v.cmd.Println("\n=== Storage & Permissions ===")
		v.checkDatabase()
	}

	v.cmd.Println("\n=== Summary ===")
	if v.failed {
		v.cmd.Println("[FAIL] Security validation failed")
		return fmt.Errorf("security: validate: %w", constants.ErrValidationFailed)
	}
	v.cmd.Println("[OK]   Security validation passed")
	return nil
}

// runtimePath resolves a service-relative path to its operator-facing form.
func (v *securityValidator) runtimePath(rel string) securityPath {
	return securityPath{display: v.fileSvc.Resolve(rel), rel: rel}
}

// explicitPath maps a path under an explicit directory flag to its
// service-relative form.
func (v *securityValidator) explicitPath(display string) (securityPath, error) {
	rel, _, err := fs.RuntimeRelativePath(v.fileSvc, display)
	return securityPath{display: display, rel: rel}, err
}

func (v *securityValidator) pkiPaths() (securityPKIPaths, error) {
	if v.pkiDir == "" {
		authorities := filepath.Join(constants.PkiDirname, constants.PkiSubdirAuthorities)
		hub := filepath.Join(constants.PkiDirname, constants.PkiSubdirIssued, constants.PkiSubdirHub)
		return securityPKIPaths{
			rootCA:        v.runtimePath(filepath.Join(constants.PkiDirname, constants.PkiSubdirRoot, constants.PkiFileRootCA)),
			rootKey:       v.runtimePath(filepath.Join(constants.PkiDirname, constants.PkiSubdirRoot, constants.PkiFileRootCAKey)),
			trustBundle:   v.runtimePath(filepath.Join(constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle)),
			actuatorPub:   v.runtimePath(filepath.Join(constants.PkiDirname, constants.ActuatorPubJSONFilename)),
			hubCA:         v.runtimePath(filepath.Join(authorities, constants.PkiFileHubCA)),
			operatorCA:    v.runtimePath(filepath.Join(authorities, constants.PkiFileOperatorCA)),
			gatewayPeerCA: v.runtimePath(filepath.Join(authorities, constants.PkiFileGatewayPeerCA)),
			serviceCert:   v.runtimePath(filepath.Join(hub, constants.PkiFileGatewayCert)),
			serviceChain:  v.runtimePath(filepath.Join(hub, constants.PkiFileGatewayChain)),
		}, nil
	}

	rootCA, errRoot := v.explicitPath(filepath.Join(v.pkiDir, constants.PkiSubdirRoot, constants.PkiFileRootCA))
	rootKey, errKey := v.explicitPath(filepath.Join(v.pkiDir, constants.PkiSubdirRoot, constants.PkiFileRootCAKey))
	trustBundle, errTrust := v.explicitPath(filepath.Join(v.pkiDir, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle))
	actuatorPub, errAct := v.explicitPath(filepath.Join(v.pkiDir, constants.ActuatorPubJSONFilename))
	if errRoot != nil || errKey != nil || errTrust != nil || errAct != nil {
		return securityPKIPaths{}, fmt.Errorf("security: pki path validation: %w: %w", constants.ErrValidationFailed, constants.ErrPathValidation)
	}
	return securityPKIPaths{rootCA: rootCA, rootKey: rootKey, trustBundle: trustBundle, actuatorPub: actuatorPub}, nil
}

func (v *securityValidator) pkiChecks(paths securityPKIPaths) []securityPKICheck {
	checks := []securityPKICheck{
		{path: paths.rootCA},
		{path: paths.rootKey, mustNotExist: true},
		{path: paths.trustBundle},
		{path: paths.actuatorPub},
	}
	if v.pkiDir != "" {
		return checks
	}
	return append(checks,
		securityPKICheck{path: paths.hubCA},
		securityPKICheck{path: paths.operatorCA},
		securityPKICheck{path: paths.gatewayPeerCA, isOptional: true},
		securityPKICheck{path: paths.serviceCert, isOptional: true},
		securityPKICheck{path: paths.serviceChain, isOptional: true},
	)
}

func (v *securityValidator) checkPKIDirectory(paths securityPKIPaths) {
	for _, check := range v.pkiChecks(paths) {
		exists, err := v.exists(check.path)
		if err != nil {
			v.fail("  [FAIL] %s: %v\n", check.path.display, err)
			continue
		}

		if check.mustNotExist {
			// Invariant check: unsealed CA private keys must never touch disk in the clear
			if exists {
				v.fail("  [FAIL] %s exists unsealed on disk (violates INV-AUTH-ID-04 / INV-STOR-ENC-01)\n", check.path.display)
			} else {
				v.cmd.Printf("  [OK]   %s is not exposed (sealed in secrets store)\n", filepath.Base(check.path.display))
			}
			continue
		}

		if exists {
			v.cmd.Printf("  [OK]   %s exists\n", check.path.display)
			continue
		}
		if check.path == paths.actuatorPub && v.legacyActuatorKeyExists(paths.actuatorPub) {
			continue
		}
		if check.isOptional {
			continue
		}
		v.fail("  [FAIL] %s missing\n", check.path.display)
	}
}

// legacyActuatorKeyExists reports the warden_pub.pem fallback for the
// actuator public key.
func (v *securityValidator) legacyActuatorKeyExists(actuatorPub securityPath) bool {
	legacy := securityPath{display: filepath.Join(filepath.Dir(actuatorPub.display), constants.PkiFileWardenPub)}
	if v.pkiDir == "" {
		legacy.rel = filepath.Join(constants.PkiDirname, constants.PkiFileWardenPub)
	} else {
		legacy.rel, _, _ = fs.RuntimeRelativePath(v.fileSvc, legacy.display)
	}
	exists, _ := v.exists(legacy)
	if exists {
		v.cmd.Printf("  [OK]   %s exists (legacy actuator key)\n", legacy.display)
	}
	return exists
}

func (v *securityValidator) secretChecks() ([]securitySecretCheck, error) {
	if v.secretsDir == "" {
		return []securitySecretCheck{
			{path: v.runtimePath(filepath.Join(constants.SecretsDirname, constants.SecretsFileActuatorSigningKey)), description: "actuator signing key"},
			{path: v.runtimePath(filepath.Join(constants.SecretsDirname, constants.SecretsFileBootstrapDigest)), description: "bootstrap digest"},
			{path: v.runtimePath(filepath.Join(constants.SecretsDirname, "ca_root_key")), description: "sealed CA root key", isOptional: true},
		}, nil
	}

	actKey, errActKey := v.explicitPath(filepath.Join(v.secretsDir, constants.SecretsFileActuatorSigningKey))
	bootDigest, errBoot := v.explicitPath(filepath.Join(v.secretsDir, constants.SecretsFileBootstrapDigest))
	if errActKey != nil || errBoot != nil {
		return nil, fmt.Errorf("security: secrets path validation: %w: %w", constants.ErrValidationFailed, constants.ErrPathValidation)
	}
	return []securitySecretCheck{
		{path: actKey, description: "actuator signing key"},
		{path: bootDigest, description: "bootstrap digest"},
	}, nil
}

func (v *securityValidator) checkSecretsDirectory() error {
	checks, err := v.secretChecks()
	if err != nil {
		return err
	}

	for _, check := range checks {
		exists, chkErr := v.exists(check.path)
		if chkErr != nil {
			v.fail("  [FAIL] %s: %v\n", check.path.display, chkErr)
			continue
		}
		switch {
		case exists && check.description != "" && check.isOptional:
			v.cmd.Printf("  [OK]   %s exists (%s)\n", check.path.display, check.description)
		case exists:
			v.cmd.Printf("  [OK]   %s exists\n", check.path.display)
		case !check.isOptional:
			v.fail("  [FAIL] %s missing\n", check.path.display)
		}
	}

	v.checkSecretsPermissions()
	return nil
}

// checkSecretsPermissions warns when the secrets directory is group or world
// accessible on POSIX.
func (v *securityValidator) checkSecretsPermissions() {
	if runtime.GOOS == "windows" {
		return
	}
	var info os.FileInfo
	var statErr error
	if v.secretsDir == "" {
		info, statErr = v.fileSvc.Stat(v.ctx, constants.SecretsDirname)
	} else {
		rel, inRuntime, _ := fs.RuntimeRelativePath(v.fileSvc, v.secretsDir)
		if inRuntime && rel != "" {
			info, statErr = v.fileSvc.Stat(v.ctx, rel)
		}
	}
	if statErr != nil || info == nil {
		return
	}
	perm := info.Mode().Perm()
	if perm&0077 != 0 {
		v.cmd.Printf("  [WARN] Secrets directory permissions (%04o) are overly permissive (expected 0700)\n", perm)
	} else {
		v.cmd.Printf("  [OK]   Secrets directory permissions are private (%04o)\n", perm)
	}
}

func (v *securityValidator) checkRootCA(rootCA securityPath) *x509.Certificate {
	certData, err := v.read(rootCA)
	if err != nil && !errors.Is(err, constants.ErrNotFound) && !os.IsNotExist(err) {
		v.fail("  [FAIL] failed to read root_ca.crt: %v\n", err)
	}
	if certData == nil {
		return nil
	}

	block, _ := pem.Decode(certData)
	if block == nil || block.Type != "CERTIFICATE" {
		v.fail("  [FAIL] root_ca.crt is not a valid PEM certificate\n")
		return nil
	}
	v.cmd.Printf("  [OK]   root_ca.crt is valid PEM\n")
	rootCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		v.fail("  [FAIL] root_ca.crt X.509 parse failed: %v\n", err)
		return nil
	}

	if rootCert.IsCA {
		v.cmd.Printf("  [OK]   root_ca.crt is a Certificate Authority (CA: true)\n")
	} else {
		v.fail("  [FAIL] root_ca.crt is not a Certificate Authority (CA: false)\n")
	}
	now := time.Now()
	switch {
	case now.Before(rootCert.NotBefore):
		v.fail("  [FAIL] root_ca.crt is not yet valid (valid from %s)\n", rootCert.NotBefore.Format(time.RFC3339))
	case now.After(rootCert.NotAfter):
		v.fail("  [FAIL] root_ca.crt expired on %s\n", rootCert.NotAfter.Format(time.RFC3339))
	default:
		daysLeft := int(time.Until(rootCert.NotAfter).Hours() / 24)
		v.cmd.Printf("  [OK]   root_ca.crt is active (expires in %d days)\n", daysLeft)
	}
	return rootCert
}

// checkIntermediateChain validates the intermediate CAs against the root CA
// and the service certificate against the hub CA.
func (v *securityValidator) checkIntermediateChain(paths securityPKIPaths, rootCert *x509.Certificate) {
	hubCert := v.checkIntermediateCA("hub_ca.crt", paths.hubCA, rootCert)
	v.checkIntermediateCA("operator_ca.crt", paths.operatorCA, rootCert)
	v.checkIntermediateCA("gateway_peer_ca.crt", paths.gatewayPeerCA, rootCert)
	v.checkServiceCertificate(paths.serviceCert, hubCert)
}

func (v *securityValidator) checkIntermediateCA(name string, path securityPath, rootCert *x509.Certificate) *x509.Certificate {
	exists, _ := v.exists(path)
	if !exists {
		return nil
	}
	data, err := v.read(path)
	if err != nil {
		return nil
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		v.fail("  [FAIL] %s is not a valid PEM certificate\n", name)
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		v.fail("  [FAIL] %s X.509 parse failed: %v\n", name, err)
		return nil
	}
	if err := cert.CheckSignatureFrom(rootCert); err != nil {
		v.fail("  [FAIL] %s signature check against root CA failed: %v\n", name, err)
	} else {
		v.cmd.Printf("  [OK]   %s is valid CA signed by root CA\n", name)
	}
	return cert
}

func (v *securityValidator) checkServiceCertificate(path securityPath, hubCert *x509.Certificate) {
	if exists, _ := v.exists(path); !exists {
		return
	}
	data, err := v.read(path)
	if err != nil {
		return
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return
	}
	svcCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || hubCert == nil {
		return
	}
	if err := svcCert.CheckSignatureFrom(hubCert); err != nil {
		v.fail("  [FAIL] operator-gateway.crt signature check against hub CA failed: %v\n", err)
	} else {
		v.cmd.Printf("  [OK]   operator-gateway.crt is valid and signed by hub CA\n")
	}
}

func (v *securityValidator) checkPorts() {
	ports := []struct {
		port int
		name string
	}{
		{constants.Ports.OperatorHttp, "HTTP"},
		{constants.Ports.OperatorHttps, "HTTPS/mTLS"},
	}

	var gwRunning bool
	var gwPID int
	if v.pkiDir == "" {
		pm, err := platform.NewProcessManager(v.fileSvc)
		if err == nil {
			gwRunning, gwPID, _ = pm.OperatorStatus()
		}
	}

	for _, p := range ports {
		addr := fmt.Sprintf("127.0.0.1:%d", p.port)
		conn, err := net.DialTimeout(string(constants.NetworkProtocolTCP), addr, 300*time.Millisecond)
		switch {
		case err == nil:
			conn.Close()
			if gwRunning && gwPID > 0 {
				v.cmd.Printf("  [OK]   Port %d is active (Gateway %s listener, PID %d)\n", p.port, p.name, gwPID)
			} else {
				v.cmd.Printf("  [OK]   Port %d is active (%s listener connected)\n", p.port, p.name)
			}
		case gwRunning:
			v.fail("  [FAIL] Port %d is not responding (Gateway online with PID %d)\n", p.port, gwPID)
		default:
			listener, listenErr := net.Listen(string(constants.NetworkProtocolTCP), addr)
			if listenErr == nil {
				listener.Close()
				v.cmd.Printf("  [OK]   Port %d is available\n", p.port)
			} else {
				v.cmd.Printf("  [WARN] Port %d is in use by another process\n", p.port)
			}
		}
	}
}

func (v *securityValidator) checkTrustBundle(trustBundle securityPath) {
	trustData, err := v.read(trustBundle)
	if err != nil && !errors.Is(err, constants.ErrNotFound) && !os.IsNotExist(err) {
		v.fail("  [FAIL] failed to read trust bundle: %v\n", err)
	}
	if trustData == nil {
		return
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(trustData) {
		v.fail("  [FAIL] Trust bundle is not valid PEM\n")
		return
	}
	v.cmd.Printf("  [OK]   Trust bundle is valid PEM\n")

	bundleCerts := parseBundleCertificates(trustData)
	if len(bundleCerts) == 0 {
		return
	}
	hasExpired := false
	now := time.Now()
	for _, c := range bundleCerts {
		if now.After(c.NotAfter) {
			hasExpired = true
			v.fail("  [FAIL] Trust bundle contains expired certificate: %s (expired %s)\n", c.Subject.CommonName, c.NotAfter.Format(time.RFC3339))
		}
	}
	if !hasExpired {
		v.cmd.Printf("  [OK]   Trust bundle contains %d valid CA certificate(s)\n", len(bundleCerts))
	}
}

// parseBundleCertificates returns every certificate in a PEM bundle that
// parses; entries that are not certificates or do not parse are skipped.
func parseBundleCertificates(bundle []byte) []*x509.Certificate {
	var certs []*x509.Certificate
	rest := bundle
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if c, err := x509.ParseCertificate(block.Bytes); err == nil {
			certs = append(certs, c)
		}
	}
	return certs
}

func (v *securityValidator) checkDatabase() {
	dbRelPath := filepath.Join(constants.DataDirname, constants.DbFilename)
	dbExists, _ := v.fileSvc.FileExists(v.ctx, dbRelPath)
	if !dbExists {
		return
	}
	v.cmd.Printf("  [OK]   Canonical database %s exists\n", constants.DbFilename)
	if runtime.GOOS == "windows" {
		return
	}
	dbInfo, err := v.fileSvc.Stat(v.ctx, dbRelPath)
	if err != nil {
		return
	}
	perm := dbInfo.Mode().Perm()
	if perm&0077 != 0 {
		v.cmd.Printf("  [WARN] Database permissions (%04o) are overly permissive (expected 0600)\n", perm)
	} else {
		v.cmd.Printf("  [OK]   Database permissions are private (%04o)\n", perm)
	}
}

// fail prints a failed check and marks the validation as failed.
func (v *securityValidator) fail(format string, args ...any) {
	v.cmd.Printf(format, args...)
	v.failed = true
}

// exists checks a path through the file service when it lives inside the
// runtime root and falls back to the host filesystem otherwise.
func (v *securityValidator) exists(p securityPath) (bool, error) {
	if p.rel != "" {
		return v.fileSvc.FileExists(v.ctx, p.rel)
	}
	rel, inRuntime, err := fs.RuntimeRelativePath(v.fileSvc, p.display)
	if err != nil {
		return false, err
	}
	if inRuntime && rel != "" {
		return v.fileSvc.FileExists(v.ctx, rel)
	}
	_, statErr := os.Stat(p.display)
	return statErr == nil, nil
}

// read reads a path with the same runtime-root routing as exists.
func (v *securityValidator) read(p securityPath) ([]byte, error) {
	if p.rel != "" {
		return v.fileSvc.ReadFile(v.ctx, p.rel)
	}
	rel, inRuntime, err := fs.RuntimeRelativePath(v.fileSvc, p.display)
	if err != nil {
		return nil, err
	}
	if inRuntime && rel != "" {
		return v.fileSvc.ReadFile(v.ctx, rel)
	}
	return os.ReadFile(p.display)
}
