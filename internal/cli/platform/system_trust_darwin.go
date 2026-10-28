// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build darwin
// +build darwin

package platform

import (
	"context"
	"crypto/sha1" //nolint:gosec // SHA-1 is the keychain item handle security(1) deletes by, not a security decision.
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/zerotrustdata/ztd/internal/constants"
)

const (
	// darwinSystemKeychain is the machine-wide keychain. Browsers on every
	// account consult it. ztd reads it so an older install is still recognized,
	// and only removes from it after the caller has said so.
	darwinSystemKeychain = "/Library/Keychains/System.keychain"

	darwinScopeUser   = "user"
	darwinScopeSystem = "system"
)

// userLoginKeychain is the current account's login keychain. Trust written
// here is visible to this user and to nobody else, and security(1) can update
// it without sudo. The -d flag on add-trusted-cert is the admin domain; this
// path never passes it.
func userLoginKeychain() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: home directory: %w", constants.ErrSystemTrustInstallFailed, err)
	}
	db := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
	if _, err := os.Stat(db); err == nil {
		return db, nil
	}
	legacy := filepath.Join(home, "Library", "Keychains", "login.keychain")
	if _, err := os.Stat(legacy); err == nil {
		return legacy, nil
	}
	return "", fmt.Errorf("%w: login keychain not found", constants.ErrSystemTrustInstallFailed)
}

type darwinKeychain struct {
	path  string
	scope string
}

// darwinTrustKeychains lists the login keychain first, then the System
// keychain. A missing login keychain is skipped so a read still sees a
// legacy System anchor.
func darwinTrustKeychains() []darwinKeychain {
	var out []darwinKeychain
	if path, err := userLoginKeychain(); err == nil {
		out = append(out, darwinKeychain{path: path, scope: darwinScopeUser})
	}
	out = append(out, darwinKeychain{path: darwinSystemKeychain, scope: darwinScopeSystem})
	return out
}

func darwinScopeLabel(scope string) string {
	if scope == darwinScopeSystem {
		return "System keychain"
	}
	return "login keychain"
}

// isTrustedPlatform looks for the fingerprint in the login keychain and the
// System keychain. find-certificate reads both without elevation, so an
// already-trusted root does not prompt.
func (i *SystemTrustInstaller) isTrustedPlatform(ctx context.Context, root *x509.Certificate, fingerprint string) (bool, error) {
	for _, kc := range darwinTrustKeychains() {
		found, err := i.keychainHolds(ctx, kc.path, fingerprint)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

// keychainHolds reports whether keychain contains a certificate with the
// given SHA-256 fingerprint. An unreadable or empty keychain holds nothing.
func (i *SystemTrustInstaller) keychainHolds(ctx context.Context, keychain, fingerprint string) (bool, error) {
	out, err := i.runner.Run(ctx, nil, "security", "find-certificate", "-a", "-p", keychain)
	if err == nil {
		return bundleContainsFingerprint(out, fingerprint)
	}
	// find-certificate exits non-zero for an empty or unreadable keychain.
	return false, nil
}

// installPlatform adds the root to the login keychain in the user trust
// domain. There is no sudo and no -d: -d would write the admin domain, which
// is machine-wide.
func (i *SystemTrustInstaller) installPlatform(ctx context.Context, root *x509.Certificate, fingerprint string) error {
	login, err := userLoginKeychain()
	if err != nil {
		return err
	}
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw})
	certPath, cleanup, err := i.stageRootCert(ctx, rootPEM)
	if err != nil {
		return err
	}
	defer cleanup()

	held, err := i.keychainHolds(ctx, login, fingerprint)
	if err != nil {
		return err
	}
	// -p ssl limits the anchor to TLS. It is not a code-signing or package-signing root.
	out, err := i.runner.Run(ctx, nil, "security", "add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", login, certPath)
	if err != nil {
		if !held {
			i.removeUntrustedImport(ctx, root, login)
		}
		return trustCmdError(out, err)
	}
	return nil
}

// removeUntrustedImport deletes the copy add-trusted-cert imported before its
// trust write failed (a cancelled macOS dialog, or no GUI session to show it).
// isTrustedPlatform reads presence in the keychain, so an untrusted leftover
// would report the root as trusted and block every retry. The caller only
// calls this when the keychain did not hold the root beforehand, so it never
// removes an existing anchor. Deletion is best effort: when nothing was
// imported there is nothing to delete, and the install error is what matters.
func (i *SystemTrustInstaller) removeUntrustedImport(ctx context.Context, root *x509.Certificate, keychain string) {
	thumbprint := sha1.Sum(root.Raw) //nolint:gosec // keychain item handle, see import
	_, _ = i.runner.Run(ctx, nil, "security", "delete-certificate", "-Z", hex.EncodeToString(thumbprint[:]), keychain)
}

// listStaleAnchorsPlatform pairs each SHA-1 from `security -Z` with the PEM
// that follows it. One hash must not be reused for every certificate in the
// keychain: delete-certificate would then remove the wrong anchor.
func (i *SystemTrustInstaller) listStaleAnchorsPlatform(ctx context.Context, currentFingerprint string) ([]StaleAnchor, error) {
	stale := []StaleAnchor{}
	for _, kc := range darwinTrustKeychains() {
		stale = append(stale, i.listKeychainAnchors(ctx, kc, currentFingerprint)...)
	}
	return stale, nil
}

// listKeychainAnchors returns the stale anchors in one keychain.
// find-certificate exits non-zero when nothing matches or the keychain is
// unreadable; both mean that keychain holds no stale anchor.
func (i *SystemTrustInstaller) listKeychainAnchors(ctx context.Context, kc darwinKeychain, currentFingerprint string) []StaleAnchor {
	out, err := i.runner.Run(ctx, nil, "security", "find-certificate", "-c", constants.RootCACommonName, "-a", "-p", "-Z", kc.path)
	if err == nil {
		return staleAnchorsIn(out, kc, currentFingerprint)
	}
	return nil
}

func staleAnchorsIn(out []byte, kc darwinKeychain, currentFingerprint string) []StaleAnchor {
	var stale []StaleAnchor
	for _, item := range parseSecurityHashPEM(out) {
		if !validSHA1(item.sha1) {
			continue
		}
		certs, parseErr := parseBundleCerts(item.pem)
		if parseErr != nil || len(certs) != 1 {
			continue
		}
		cert := certs[0]
		if cert.Subject.CommonName != constants.RootCACommonName {
			continue
		}
		fp := certFingerprint(cert)
		if fp == currentFingerprint {
			continue
		}
		stale = append(stale, StaleAnchor{
			Fingerprint: fp,
			CommonName:  cert.Subject.CommonName,
			Handle:      kc.scope + ":" + item.sha1,
			Scope:       darwinScopeLabel(kc.scope),
			Elevated:    kc.scope == darwinScopeSystem,
		})
	}
	return stale
}

type securityHashPEM struct {
	sha1 string
	pem  []byte
}

// parseSecurityHashPEM reads `security find-certificate -Z -p` output: a
// "SHA-1 hash:" line, then the PEM it belongs to. The hash is consumed by
// that PEM so the next certificate cannot inherit it.
func parseSecurityHashPEM(out []byte) []securityHashPEM {
	var result []securityHashPEM
	var sha1 string
	var pemBuf []byte
	inPEM := false
	for _, line := range strings.Split(string(out), "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "SHA-1 hash:") {
			sha1 = strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, strings.TrimPrefix(trim, "SHA-1 hash:"))
			continue
		}
		if strings.HasPrefix(trim, "-----BEGIN CERTIFICATE-----") {
			inPEM = true
			pemBuf = []byte(trim + "\n")
			continue
		}
		if !inPEM {
			continue
		}
		pemBuf = append(pemBuf, line...)
		pemBuf = append(pemBuf, '\n')
		if strings.HasPrefix(trim, "-----END CERTIFICATE-----") {
			if sha1 != "" {
				result = append(result, securityHashPEM{sha1: strings.ToLower(sha1), pem: append([]byte(nil), pemBuf...)})
			}
			inPEM = false
			pemBuf = nil
			sha1 = ""
		}
	}
	return result
}

func validSHA1(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

func splitDarwinHandle(handle string) (scope, sha1, keychain string, err error) {
	scope, sha1, ok := strings.Cut(handle, ":")
	if !ok || !validSHA1(sha1) {
		return "", "", "", fmt.Errorf("%w: unrecognized keychain handle", constants.ErrSystemTrustInstallFailed)
	}
	switch scope {
	case darwinScopeUser:
		keychain, err = userLoginKeychain()
		if err != nil {
			return "", "", "", err
		}
		return scope, sha1, keychain, nil
	case darwinScopeSystem:
		return scope, sha1, darwinSystemKeychain, nil
	default:
		return "", "", "", fmt.Errorf("%w: unrecognized keychain handle", constants.ErrSystemTrustInstallFailed)
	}
}

// removeStaleAnchorsPlatform deletes each anchor from the keychain named in
// its handle. Login-keychain anchors do not use sudo. System-keychain anchors
// do, and only those.
func (i *SystemTrustInstaller) removeStaleAnchorsPlatform(ctx context.Context, anchors []StaleAnchor) error {
	for _, a := range anchors {
		scope, sha1, keychain, err := splitDarwinHandle(a.Handle)
		if err != nil {
			return err
		}
		args := []string{"delete-certificate", "-Z", sha1, keychain}
		var out []byte
		if scope == darwinScopeSystem {
			out, err = i.runner.Run(ctx, nil, "sudo", append([]string{"security"}, args...)...)
		} else {
			out, err = i.runner.Run(ctx, nil, "security", args...)
		}
		if err != nil {
			return trustCmdError(out, err)
		}
	}
	return nil
}
