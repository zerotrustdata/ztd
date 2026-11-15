// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

type fakeCLIRenewer struct {
	calls      int
	reinstates int
	artifacts  EnrollmentArtifacts
	err        error
}

func (f *fakeCLIRenewer) Renew(context.Context, fs.RuntimeFileService, string, *ecdsa.PrivateKey, string) (EnrollmentArtifacts, error) {
	f.calls++
	return f.artifacts, f.err
}

func (f *fakeCLIRenewer) Reinstate(context.Context, fs.RuntimeFileService, string, *ecdsa.PrivateKey, string) (EnrollmentArtifacts, error) {
	f.reinstates++
	return f.artifacts, f.err
}

// writeBoundIdentity writes a complete local CLI identity whose certificate
// expires at notAfter (NotBefore is an hour ago) and binds it to an operator.
func writeBoundIdentity(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config, notAfter time.Time) (cliSessionID string, bundlePEM string) {
	t.Helper()
	userID, cliSessionID, _, bundlePEM := writeCompleteIdentityWithBundleFP(t, fileSvc, cfg, notAfter)
	require.NoError(t, SaveCredentials(fileSvc, cfg, &Credentials{
		UserID: userID, CLISessionID: cliSessionID, OperatorSessionID: "op-session-1", OperatorID: "op-1",
	}))
	return cliSessionID, bundlePEM
}

// renewedArtifacts returns renewal artifacts for cliSessionID with a fresh
// year-long certificate and the key that matches it.
func renewedArtifacts(t *testing.T, cliSessionID, bundlePEM string) EnrollmentArtifacts {
	t.Helper()
	caKey, caCert := generateTestCAWithKey(t, "renewal-ca")
	certPEM, key := testutil.GenerateTestSignedCertWithExpiry(t, "ztd-cli-test", caCert, caKey, time.Now().Add(365*24*time.Hour))
	return EnrollmentArtifacts{
		Source:         EnrollmentSourceRenewal,
		CLISessionID:   cliSessionID,
		UserID:         "user-existing",
		CLICertPEM:     certPEM,
		CLIKey:         key,
		TrustBundlePEM: bundlePEM,
	}
}

func readLocalCLICert(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config) *x509.Certificate {
	t.Helper()
	cert, present, corrupt := NewCredentialStore(fileSvc, cfg).loadCLICert(testutil.Context(t))
	require.True(t, present)
	require.False(t, corrupt)
	return cert
}

func TestCertRenewalDue_StartsAtTwoThirdsOfTheLifetime(t *testing.T) {
	notBefore := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	cert := &x509.Certificate{NotBefore: notBefore, NotAfter: notBefore.Add(9 * 24 * time.Hour)}
	renewAt := notBefore.Add(6 * 24 * time.Hour)

	assert.False(t, certRenewalDue(cert, renewAt.Add(-time.Second)), "before two thirds")
	assert.True(t, certRenewalDue(cert, renewAt), "at two thirds")
	assert.True(t, certRenewalDue(cert, cert.NotAfter.Add(-time.Second)), "just before expiry")
	assert.False(t, certRenewalDue(cert, cert.NotAfter), "an expired certificate is not renewable")
}

func TestRenewCLIIdentityIfDue_NotDueDoesNotContactGateway(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(365*24*time.Hour))
	gw := &fakeCLIRenewer{}

	renewed, err := RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, gw, &mockKeyProvider{}, time.Now)
	require.NoError(t, err)
	assert.False(t, renewed)
	assert.Equal(t, 0, gw.calls)
}

func TestRenewCLIIdentityIfDue_NoIdentityDoesNotContactGateway(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	gw := &fakeCLIRenewer{}

	renewed, err := RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, gw, &mockKeyProvider{}, time.Now)
	require.NoError(t, err)
	assert.False(t, renewed)
	assert.Equal(t, 0, gw.calls)
}

func TestCLIReissueDue_ReinstatesOnlyWithinTheWindowAfterExpiry(t *testing.T) {
	notBefore := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	cert := &x509.Certificate{NotBefore: notBefore, NotAfter: notBefore.Add(9 * 24 * time.Hour)}

	assert.Equal(t, cliReissueNone, cliReissueDue(cert, notBefore.Add(time.Hour)))
	assert.Equal(t, cliReissueRenew, cliReissueDue(cert, cert.NotAfter.Add(-time.Second)))
	assert.Equal(t, cliReissueReinstate, cliReissueDue(cert, cert.NotAfter))
	assert.Equal(t, cliReissueReinstate, cliReissueDue(cert, cert.NotAfter.Add(constants.CLIReinstateWindow-time.Second)))
	assert.Equal(t, cliReissueNone, cliReissueDue(cert, cert.NotAfter.Add(constants.CLIReinstateWindow)), "past the window is left for recovery")
}

func TestRenewCLIIdentityIfDue_ReinstatesAnExpiredCertificateKeepingSessionAndBinding(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	cliSessionID, bundlePEM := writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(-time.Minute))
	artifacts := renewedArtifacts(t, cliSessionID, bundlePEM)
	artifacts.Source = EnrollmentSourceReinstatement
	gw := &fakeCLIRenewer{artifacts: artifacts}

	reinstated, err := RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, gw, &mockKeyProvider{csr: "csr", key: artifacts.CLIKey}, time.Now)
	require.NoError(t, err)
	assert.True(t, reinstated)
	assert.Equal(t, 0, gw.calls, "an expired certificate cannot renew over mTLS")
	assert.Equal(t, 1, gw.reinstates)

	creds, err := LoadCredentials(fileSvc, cfg)
	require.NoError(t, err)
	assert.Equal(t, cliSessionID, creds.CLISessionID)
	assert.Equal(t, "op-session-1", creds.OperatorSessionID, "reinstatement keeps the operator binding")
	assert.Equal(t, "op-1", creds.OperatorID)
	_, err = LoadCLICertificate(fileSvc, cfg)
	require.NoError(t, err, "the reinstated certificate is valid and matches the committed key")
}

func TestRenewCLIIdentityIfDue_LeavesACertificatePastTheWindowForRecovery(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(-constants.CLIReinstateWindow-time.Minute))
	gw := &fakeCLIRenewer{}

	renewed, err := RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, gw, &mockKeyProvider{}, time.Now)
	require.NoError(t, err)
	assert.False(t, renewed)
	assert.Equal(t, 0, gw.calls)
	assert.Equal(t, 0, gw.reinstates)
}

func TestRenewCLIIdentityIfDue_RenewsInPlaceKeepingSessionAndBinding(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	cliSessionID, bundlePEM := writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(time.Minute))
	before := readLocalCLICert(t, fileSvc, cfg)
	artifacts := renewedArtifacts(t, cliSessionID, bundlePEM)
	gw := &fakeCLIRenewer{artifacts: artifacts}

	renewed, err := RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, gw, &mockKeyProvider{csr: "csr", key: artifacts.CLIKey}, time.Now)
	require.NoError(t, err)
	assert.True(t, renewed)
	assert.Equal(t, 1, gw.calls)

	after := readLocalCLICert(t, fileSvc, cfg)
	assert.NotEqual(t, before.SerialNumber, after.SerialNumber, "the renewed certificate is committed")
	creds, err := LoadCredentials(fileSvc, cfg)
	require.NoError(t, err)
	assert.Equal(t, cliSessionID, creds.CLISessionID)
	assert.Equal(t, "op-session-1", creds.OperatorSessionID, "renewal keeps the operator binding")
	assert.Equal(t, "op-1", creds.OperatorID)

	// The committed key matches the committed certificate.
	_, err = LoadCLICertificate(fileSvc, cfg)
	require.NoError(t, err)

	// Renewed: the next command finds nothing due.
	renewed, err = RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, gw, &mockKeyProvider{}, time.Now)
	require.NoError(t, err)
	assert.False(t, renewed)
	assert.Equal(t, 1, gw.calls)
}

func TestRenewCLIIdentityIfDue_RefusesARenewalForAnotherSession(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	_, bundlePEM := writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(time.Minute))
	before := readLocalCLICert(t, fileSvc, cfg)
	artifacts := renewedArtifacts(t, "some-other-session", bundlePEM)
	gw := &fakeCLIRenewer{artifacts: artifacts}

	renewed, err := RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, gw, &mockKeyProvider{csr: "csr", key: artifacts.CLIKey}, time.Now)
	require.ErrorIs(t, err, constants.ErrCLIRenewalSessionMismatch)
	assert.False(t, renewed)
	assert.Equal(t, before.SerialNumber, readLocalCLICert(t, fileSvc, cfg).SerialNumber, "nothing is committed")
}

func TestRenewCLIIdentityIfDue_SkipsWhileAnotherProcessHoldsTheLock(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(time.Minute))
	lockRel, err := fileSvc.RelFromAbs(cfg.CLIIdentityLockFile())
	require.NoError(t, err)
	lock, err := fileSvc.TryLock(testutil.Context(t), lockRel, constants.PermFilePrivate)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })
	gw := &fakeCLIRenewer{}

	renewed, err := RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, gw, &mockKeyProvider{}, time.Now)
	require.NoError(t, err)
	assert.False(t, renewed)
	assert.Equal(t, 0, gw.calls)
}

func TestCLISigningLock_ExcludesRenewalAndHonorsCancellation(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(time.Minute))
	lock, err := LockCLIIdentityForSigning(testutil.Context(t), fileSvc, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })
	other, err := LockCLIIdentityForSigning(testutil.Context(t), fileSvc, cfg)
	require.NoError(t, err, "concurrent signing operations share the identity")
	require.NoError(t, other.Close())
	gateway := &fakeCLIRenewer{}
	renewed, err := RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, gateway, &mockKeyProvider{}, time.Now)
	require.NoError(t, err)
	require.False(t, renewed)
	require.Zero(t, gateway.calls)
	require.NoError(t, lock.Close())
	ctx, cancel := context.WithCancel(testutil.Context(t))
	cancel()
	_, err = LockCLIIdentityForSigning(ctx, fileSvc, cfg)
	require.ErrorIs(t, err, context.Canceled)
}

// holdCLIIdentityLock takes the exclusive CLI identity lock, as a renewing
// process does, and returns its release.
func holdCLIIdentityLock(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config) func() {
	t.Helper()
	lockRel, err := fileSvc.RelFromAbs(cfg.CLIIdentityLockFile())
	require.NoError(t, err)
	lock, err := fileSvc.TryLock(testutil.Context(t), lockRel, constants.PermFilePrivate)
	require.NoError(t, err)
	var once sync.Once
	release := func() { once.Do(func() { require.NoError(t, lock.Close()) }) }
	t.Cleanup(release)
	return release
}

// requireBlockedUntilReleased asserts that done stays open while the lock is
// held and closes once release runs.
func requireBlockedUntilReleased(t *testing.T, done <-chan struct{}, release func()) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("returned while another process held the CLI identity lock")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("did not acquire the CLI identity lock after it was released")
	}
}

func TestLockCLIIdentityForSigning_WaitsForAnInFlightRenewal(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(365*24*time.Hour))
	release := holdCLIIdentityLock(t, fileSvc, cfg)

	ctx := testutil.Context(t)
	done := make(chan struct{})
	var lock io.Closer
	var lockErr error
	go func() {
		defer close(done)
		lock, lockErr = LockCLIIdentityForSigning(ctx, fileSvc, cfg)
	}()
	requireBlockedUntilReleased(t, done, release)
	require.NoError(t, lockErr)
	require.NoError(t, lock.Close())
}

func TestRenewCLIIdentityIfDue_ReinstatementWaitsForAHeldLock(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	cliSessionID, bundlePEM := writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(-time.Minute))
	artifacts := renewedArtifacts(t, cliSessionID, bundlePEM)
	artifacts.Source = EnrollmentSourceReinstatement
	gw := &fakeCLIRenewer{artifacts: artifacts}
	release := holdCLIIdentityLock(t, fileSvc, cfg)

	ctx := testutil.Context(t)
	done := make(chan struct{})
	var reinstated bool
	var reinstateErr error
	go func() {
		defer close(done)
		reinstated, reinstateErr = RenewCLIIdentityIfDue(ctx, fileSvc, cfg, gw, &mockKeyProvider{csr: "csr", key: artifacts.CLIKey}, time.Now)
	}()
	requireBlockedUntilReleased(t, done, release)
	require.NoError(t, reinstateErr)
	assert.True(t, reinstated, "an expired certificate is reinstated after the holder releases, not skipped")
	assert.Equal(t, 1, gw.reinstates)
}

func TestLoadCLICertificate_RefusesAnExpiredCertificate(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(-time.Minute))

	_, err := LoadCLICertificate(fileSvc, cfg)
	require.ErrorIs(t, err, constants.ErrCLICertificateExpired)
	assert.Contains(t, err.Error(), "./ztd auth enroll user")
}

func TestCLICertificateSource_FollowsARenewalOnDisk(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	cliSessionID, bundlePEM := writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(time.Minute))
	source, err := NewCLICertificateSource(fileSvc, cfg)
	require.NoError(t, err)
	first, err := source.GetClientCertificate(&tls.CertificateRequestInfo{})
	require.NoError(t, err)

	artifacts := renewedArtifacts(t, cliSessionID, bundlePEM)
	_, err = RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, &fakeCLIRenewer{artifacts: artifacts}, &mockKeyProvider{csr: "csr", key: artifacts.CLIKey}, time.Now)
	require.NoError(t, err)

	second, err := source.GetClientCertificate(&tls.CertificateRequestInfo{})
	require.NoError(t, err)
	assert.NotEqual(t, first.Leaf.SerialNumber, second.Leaf.SerialNumber, "the next handshake presents the renewed certificate")
	assert.Equal(t, readLocalCLICert(t, fileSvc, cfg).SerialNumber, second.Leaf.SerialNumber)
}

func TestCLICertificateSource_KeepsTheLoadedCertificateWhenTheFileDoesNotLoad(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(365*24*time.Hour))
	source, err := NewCLICertificateSource(fileSvc, cfg)
	require.NoError(t, err)
	first, err := source.GetClientCertificate(&tls.CertificateRequestInfo{})
	require.NoError(t, err)

	// A certificate without its key, as a concurrent writer could leave
	// briefly.
	caKey, caCert := generateTestCAWithKey(t, "other-ca")
	otherPEM, _ := testutil.GenerateTestSignedCert(t, "ztd-cli-test", caCert, caKey)
	certRel, err := fileSvc.RelFromAbs(cfg.CLICertFile())
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(testutil.Context(t), certRel, []byte(otherPEM), constants.PermFilePrivate))

	second, err := source.GetClientCertificate(&tls.CertificateRequestInfo{})
	require.NoError(t, err)
	assert.Equal(t, first.Leaf.SerialNumber, second.Leaf.SerialNumber)
}

func TestCLICertificateSource_FailsTheHandshakeOnceExpired(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	writeBoundIdentity(t, fileSvc, cfg, time.Now().Add(365*24*time.Hour))
	source, err := NewCLICertificateSource(fileSvc, cfg)
	require.NoError(t, err)
	expired := *source.cert.Leaf
	expired.NotAfter = time.Now().Add(-time.Second)
	source.cert.Leaf = &expired

	_, err = source.GetClientCertificate(&tls.CertificateRequestInfo{})
	require.ErrorIs(t, err, constants.ErrCLICertificateExpired)
}
