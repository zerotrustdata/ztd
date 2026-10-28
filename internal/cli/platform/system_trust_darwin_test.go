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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func newDarwinTrustInstaller(t *testing.T, r commandRunner) *SystemTrustInstaller {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	return &SystemTrustInstaller{runner: r, now: time.Now, files: files}
}

type darwinCall struct {
	name string
	args []string
}

type darwinRunner struct {
	mu        sync.Mutex
	calls     []darwinCall
	responses map[string]struct {
		out []byte
		err error
	}
}

func newDarwinRunner() *darwinRunner {
	return &darwinRunner{responses: map[string]struct {
		out []byte
		err error
	}{}}
}

func (r *darwinRunner) set(key string, out []byte, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses[key] = struct {
		out []byte
		err error
	}{out: out, err: err}
}

func (r *darwinRunner) Run(_ context.Context, _ map[string]string, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, darwinCall{name: name, args: append([]string(nil), args...)})
	key := name + " " + strings.Join(args, " ")
	resp, ok := r.responses[key]
	if !ok {
		return nil, errors.New("no scripted response for " + key)
	}
	return resp.out, resp.err
}

func (r *darwinRunner) snapshot() []darwinCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]darwinCall(nil), r.calls...)
}

func mustRootPEM(t *testing.T, cn string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), certFingerprint(cert)
}

func securityPEMBlock(sha1, certPEM string) string {
	return "SHA-1 hash: " + sha1 + "\n" + certPEM
}

func TestDarwin_InstallRoot_UserKeychainWithoutSudo(t *testing.T) {
	t.Parallel()
	login, err := userLoginKeychain()
	require.NoError(t, err)
	certPEM, fp := mustRootPEM(t, constants.RootCACommonName)
	block, _ := pem.Decode([]byte(certPEM))
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	r := newDarwinRunner()
	installer := newDarwinTrustInstaller(t, r)
	// The staged path is not known until InstallRoot runs, so accept any
	// add-trusted-cert invocation by filling the response inside Run.
	r.mu.Lock()
	r.responses = nil
	r.mu.Unlock()
	installer.runner = darwinAcceptRunner{inner: r}

	err = installer.InstallRoot(context.Background(), cert, fp)
	require.NoError(t, err)
	calls := r.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "security", calls[0].name)
	assert.Equal(t, []string{"add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", login, calls[0].args[len(calls[0].args)-1]}, calls[0].args)
	assert.NotContains(t, calls[0].args, "-d")
	assert.NotContains(t, calls[0].args, darwinSystemKeychain)
}

// darwinAcceptRunner records the call and succeeds for add-trusted-cert.
type darwinAcceptRunner struct {
	inner *darwinRunner
}

func (d darwinAcceptRunner) Run(ctx context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
	d.inner.mu.Lock()
	d.inner.calls = append(d.inner.calls, darwinCall{name: name, args: append([]string(nil), args...)})
	d.inner.mu.Unlock()
	if name != "security" || len(args) == 0 || args[0] != "add-trusted-cert" {
		return nil, errors.New("unexpected command")
	}
	return nil, nil
}

func TestDarwin_IsTrusted_LoginKeychainThenSystem(t *testing.T) {
	t.Parallel()
	login, err := userLoginKeychain()
	require.NoError(t, err)
	pemText, fp := mustRootPEM(t, constants.RootCACommonName)

	r := newDarwinRunner()
	r.set("security find-certificate -a -p "+login, []byte(pemText), nil)
	installer := newDarwinTrustInstaller(t, r)
	trusted, err := installer.IsTrusted(context.Background(), fp)
	require.NoError(t, err)
	assert.True(t, trusted)
	assert.Len(t, r.snapshot(), 1, "a login-keychain hit must not read the System keychain")

	r = newDarwinRunner()
	r.set("security find-certificate -a -p "+login, []byte("no certs\n"), nil)
	r.set("security find-certificate -a -p "+darwinSystemKeychain, []byte(pemText), nil)
	installer = newDarwinTrustInstaller(t, r)
	trusted, err = installer.IsTrusted(context.Background(), fp)
	require.NoError(t, err)
	assert.True(t, trusted, "a legacy System keychain anchor still counts as trusted")
}

func TestDarwin_ListStaleAnchors_PairsEachHashWithItsCert(t *testing.T) {
	t.Parallel()
	login, err := userLoginKeychain()
	require.NoError(t, err)
	firstPEM, firstFP := mustRootPEM(t, constants.RootCACommonName)
	secondPEM, secondFP := mustRootPEM(t, constants.RootCACommonName)
	currentPEM, currentFP := mustRootPEM(t, constants.RootCACommonName)
	hash1 := strings.Repeat("ab", 20)
	hashCurrent := strings.Repeat("ef", 20)

	body := securityPEMBlock(hash1, firstPEM) + securityPEMBlock("CD CD CD CD CD CD CD CD CD CD CD CD CD CD CD CD CD CD CD CD", secondPEM) + securityPEMBlock(hashCurrent, currentPEM)
	r := newDarwinRunner()
	r.set("security find-certificate -c "+constants.RootCACommonName+" -a -p -Z "+login, []byte(body), nil)
	r.set("security find-certificate -c "+constants.RootCACommonName+" -a -p -Z "+darwinSystemKeychain, nil, errors.New("no matches"))
	installer := newDarwinTrustInstaller(t, r)

	stale, err := installer.ListStaleAnchors(context.Background(), currentFP)
	require.NoError(t, err)
	require.Len(t, stale, 2)
	assert.Equal(t, firstFP, stale[0].Fingerprint)
	assert.Equal(t, "user:"+hash1, stale[0].Handle)
	assert.Equal(t, "login keychain", stale[0].Scope)
	assert.False(t, stale[0].Elevated)
	assert.Equal(t, secondFP, stale[1].Fingerprint)
	assert.Equal(t, "user:"+strings.Repeat("cd", 20), stale[1].Handle, "the second certificate must keep its own SHA-1")
	assert.NotEqual(t, stale[0].Handle, stale[1].Handle)
}

func TestDarwin_Remove_SudoOnlyForSystemKeychain(t *testing.T) {
	t.Parallel()
	login, err := userLoginKeychain()
	require.NoError(t, err)
	userHash := strings.Repeat("11", 20)
	systemHash := strings.Repeat("22", 20)
	r := newDarwinRunner()
	r.set("security delete-certificate -Z "+userHash+" "+login, nil, nil)
	r.set("sudo security delete-certificate -Z "+systemHash+" "+darwinSystemKeychain, nil, nil)
	installer := newDarwinTrustInstaller(t, r)

	err = installer.RemoveStaleAnchors(context.Background(), []StaleAnchor{
		{Handle: "user:" + userHash, Scope: "login keychain"},
		{Handle: "system:" + systemHash, Scope: "System keychain", Elevated: true},
	})
	require.NoError(t, err)
	calls := r.snapshot()
	require.Len(t, calls, 2)
	assert.Equal(t, "security", calls[0].name)
	assert.Equal(t, []string{"delete-certificate", "-Z", userHash, login}, calls[0].args)
	assert.Equal(t, "sudo", calls[1].name)
	assert.Equal(t, []string{"security", "delete-certificate", "-Z", systemHash, darwinSystemKeychain}, calls[1].args)
}

func TestDarwin_Remove_RejectsABareHash(t *testing.T) {
	t.Parallel()
	r := newDarwinRunner()
	installer := newDarwinTrustInstaller(t, r)
	err := installer.RemoveStaleAnchors(context.Background(), []StaleAnchor{{Handle: strings.Repeat("ab", 20)}})
	require.Error(t, err)
	assert.Empty(t, r.snapshot(), "a handle without a keychain scope must not run security")
}
