// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"crypto/x509"
	"errors"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/pkg/certutil"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// CLIRenewalLoop keeps the CLI identity current for the lifetime of a command.
// It uses the same locked renewal and commit path as command-start renewal.
// Missing and incomplete identities remain the enrollment flow's
// responsibility, and an expired one is reinstated at the next command start. A stale certificate requires explicit rotation.
func CLIRenewalLoop(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config, gateway CLIRenewer, keys KeyProvider, logger *slog.Logger) {
	store := NewCredentialStore(fileSvc, cfg)
	loop := cliRenewalLoop{
		current: func() *x509.Certificate {
			cert, present, corrupt := store.loadCLICert(ctx)
			if !present || corrupt {
				return nil
			}
			return cert
		},
		renew: func(ctx context.Context) (bool, error) {
			return RenewCLIIdentityIfDue(ctx, fileSvc, cfg, gateway, keys, time.Now)
		},
		now: time.Now, wait: waitForCLIRenewal, logger: logger,
	}
	loop.run(ctx)
}

type cliRenewalLoop struct {
	current func() *x509.Certificate
	renew   func(context.Context) (bool, error)
	now     func() time.Time
	wait    func(context.Context, time.Duration) bool
	logger  *slog.Logger
}

func (l cliRenewalLoop) run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		cert := l.current()
		if cert == nil || !l.now().Before(cert.NotAfter) {
			return
		}
		if delay := certutil.RenewalTime(cert).Sub(l.now()); delay > 0 {
			if !l.wait(ctx, delay) {
				return
			}
			continue // Another process may have renewed while we waited.
		}
		attemptCtx, cancel := context.WithTimeout(ctx, httpTimeout)
		renewed, err := l.renew(attemptCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if renewed {
			l.logger.Info("Renewed the CLI certificate (CLI session unchanged).")
			backoff = time.Second
			continue
		}
		if err != nil {
			l.logger.Warn("CLI certificate renewal failed", "error", err)
			if errors.Is(err, constants.ErrCLIRenewalStaleCertificate) || errors.Is(err, constants.ErrCLIRenewalUnavailable) {
				return
			}
		}
		// A held cross-process lock is a skipped attempt. Poll with the same
		// bounded backoff, never spin or wait beyond the leaf's expiry.
		delay := min(backoff, cert.NotAfter.Sub(l.now()))
		if delay <= 0 || !l.wait(ctx, delay) {
			return
		}
		backoff = min(2*backoff, time.Minute)
	}
}

func waitForCLIRenewal(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
