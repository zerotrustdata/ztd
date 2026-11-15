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
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/pkg/certutil"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// CLIRenewer is the gateway surface CLI certificate renewal uses.
// EnrollmentClient implements it.
type CLIRenewer interface {
	// Renew reissues a still-valid certificate over mTLS.
	Renew(ctx context.Context, fileSvc fs.RuntimeFileService, cliCSR string, cliKey *ecdsa.PrivateKey, caFingerprint string) (EnrollmentArtifacts, error)
	// Reinstate reissues an expired certificate by proof of possession of
	// its keystore-sealed key.
	Reinstate(ctx context.Context, fileSvc fs.RuntimeFileService, cliCSR string, cliKey *ecdsa.PrivateKey, caFingerprint string) (EnrollmentArtifacts, error)
}

// cliReissue is the session-preserving reissue a CLI certificate needs now.
type cliReissue int

const (
	cliReissueNone cliReissue = iota
	// cliReissueRenew: valid and past its renewal time; renews over mTLS.
	cliReissueRenew
	// cliReissueReinstate: expired, within constants.CLIReinstateWindow;
	// reinstates by proof of possession.
	cliReissueReinstate
)

// cliReissueDue classifies cert at now.
func cliReissueDue(cert *x509.Certificate, now time.Time) cliReissue {
	switch {
	case certRenewalDue(cert, now):
		return cliReissueRenew
	case !now.Before(cert.NotAfter) && now.Before(cert.NotAfter.Add(constants.CLIReinstateWindow)):
		return cliReissueReinstate
	default:
		return cliReissueNone
	}
}

// RenewCLIIdentityIfDue reissues the local CLI certificate for its existing
// CLI session when it needs it, and returns true when it committed the
// reissued identity. A certificate past its renewal time (certutil.RenewalTime,
// two thirds of its validity), the same point an Operator renews its leaf,
// renews over mTLS. An expired certificate cannot complete the mTLS handshake;
// within constants.CLIReinstateWindow of its expiry it is reinstated instead,
// by proof of possession of its keystore-sealed key, with no human approval.
// Either way the reissued certificate keeps the CLI session ID, user, and
// operator binding, so a process still holding the previous certificate keeps
// working until that certificate expires.
//
// It does nothing, without contacting the gateway, when there is no complete
// local identity or the certificate needs neither. A certificate expired for
// longer than the window is left for human-approved recovery
// (ztd auth enroll user). Reissue is serialized across ztd processes by an
// exclusive lock on the CLI identity lock file. For a renewal, a process that
// finds it held skips, since the holder is renewing and the current
// certificate is still valid. For a reinstatement it waits, bounded by the
// enrollment request timeout, since the command cannot run on an expired
// certificate. The local identity is inspected again under the lock, so a
// reissue another process just committed is not repeated.
// now controls the reissue decision; transport and artifact validation retain
// the real clock. Production callers pass time.Now.
func RenewCLIIdentityIfDue(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, gateway CLIRenewer, keys KeyProvider, now func() time.Time) (bool, error) {
	store := NewCredentialStore(fileSvc, cfg)
	// Most commands run with a certificate that is not due. Decide that from
	// the certificate alone: a full Inspect unseals the key.
	cert, present, corrupt := store.loadCLICert(ctx)
	if !present || corrupt {
		return false, nil
	}
	mode := cliReissueDue(cert, now())
	if mode == cliReissueNone {
		return false, nil
	}
	local, err := store.Inspect(ctx)
	if err != nil {
		return false, fmt.Errorf("cli renewal: inspect local identity: %w", err)
	}
	if local.State != LocalStateComplete || cliReissueDue(local.CLICert, now()) == cliReissueNone {
		return false, nil
	}

	// For a renewal a held lock means the holder is renewing and the current
	// certificate stays valid, so skip. A reinstatement waits: the command
	// cannot run on an expired certificate.
	lock, err := lockCLIIdentity(ctx, fileSvc, cfg, true, mode == cliReissueReinstate)
	if err != nil {
		return false, fmt.Errorf("cli renewal: %w", err)
	}
	if lock == nil {
		return false, nil
	}
	defer func() { _ = lock.Close() }()

	local, err = store.Inspect(ctx)
	if err != nil {
		return false, fmt.Errorf("cli renewal: inspect local identity: %w", err)
	}
	if local.State != LocalStateComplete {
		return false, nil
	}
	mode = cliReissueDue(local.CLICert, now())
	reissue := gateway.Renew
	switch mode {
	case cliReissueNone:
		return false, nil
	case cliReissueReinstate:
		reissue = gateway.Reinstate
	}

	csrPEM, key, err := generateCLICSR(ctx, keys)
	if err != nil {
		return false, fmt.Errorf("cli renewal: %w", err)
	}
	caFingerprint := ""
	if local.TrustBundle != nil {
		caFingerprint = local.TrustBundle.PrimaryRootFingerprint
	}
	artifacts, err := reissue(ctx, fileSvc, csrPEM, key, caFingerprint)
	if err != nil {
		return false, fmt.Errorf("cli renewal: %w", err)
	}
	if artifacts.CLISessionID != local.Credentials.CLISessionID || artifacts.UserID != local.Credentials.UserID {
		return false, constants.ErrCLIRenewalSessionMismatch
	}
	artifacts.OperatorSessionID = local.Credentials.OperatorSessionID
	artifacts.OperatorID = local.Credentials.OperatorID

	staged, err := store.Stage(ctx, artifacts)
	if err != nil {
		return false, fmt.Errorf("cli renewal: stage: %w", err)
	}
	if err := store.Commit(ctx, staged); err != nil {
		store.Rollback(staged)
		return false, fmt.Errorf("cli renewal: commit: %w", err)
	}
	return true, nil
}

// lockCLIIdentity takes the CLI identity lock, exclusive for a reissue and
// shared for signing. When another process holds a conflicting lock it returns
// (nil, nil) unless wait is set, in which case it polls for the lock, bounded
// by the enrollment request timeout and caller cancellation.
func lockCLIIdentity(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, exclusive, wait bool) (io.Closer, error) {
	rel, err := fileSvc.RelFromAbs(cfg.CLIIdentityLockFile())
	if err != nil {
		return nil, fmt.Errorf("resolve identity lock: %w", err)
	}
	tryLock := fileSvc.TryLockShared
	if exclusive {
		tryLock = fileSvc.TryLock
	}
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	for {
		lock, err := tryLock(ctx, rel, constants.PermFilePrivate)
		switch {
		case err == nil:
			return lock, nil
		case !errors.Is(err, constants.ErrRuntimeFileLocked):
			return nil, fmt.Errorf("identity lock: %w", err)
		case !wait:
			return nil, nil
		case !waitForCLIRenewal(ctx, 100*time.Millisecond):
			return nil, fmt.Errorf("wait for identity lock: %w", ctx.Err())
		}
	}
}

// certRenewalDue reports whether cert is past its renewal time
// (certutil.RenewalTime) and not yet expired at now.
func certRenewalDue(cert *x509.Certificate, now time.Time) bool {
	return now.Before(cert.NotAfter) && !now.Before(certutil.RenewalTime(cert))
}

// LockCLIIdentityForSigning keeps renewal from replacing a certificate between
// signing an approval and presenting that certificate over mTLS. Concurrent
// approvals share the lock. Acquisition waits for an in-flight renewal, bounded
// by the enrollment request timeout and caller cancellation.
func LockCLIIdentityForSigning(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config) (io.Closer, error) {
	lock, err := lockCLIIdentity(ctx, fileSvc, cfg, false, true)
	if err != nil {
		return nil, fmt.Errorf("CLI signing: %w", err)
	}
	return lock, nil
}
