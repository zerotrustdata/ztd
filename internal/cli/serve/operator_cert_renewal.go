// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package serve

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/internal/uuid"
)

const (
	// operatorRenewalAttemptTimeout bounds one renewal exchange: the
	// subscription, the request, and the Gateway's reply.
	operatorRenewalAttemptTimeout = 30 * time.Second
	// operatorRenewalMaxBackoff caps the wait between failed attempts.
	operatorRenewalMaxBackoff = time.Minute
)

// OperatorCertRenewer keeps an outbound Operator's in-memory identity current.
// Two thirds into the leaf's lifetime it asks the Gateway, over the
// Operator's own renewal: pub/sub channel, to sign a CSR for a fresh key. The
// renewed leaf replaces the identity every later handshake presents, and the
// L2 trust in the reply replaces the trust the Operator holds. Established
// connections keep their handshake; nothing is redialed for renewal.
//
// A failed attempt is retried with backoff until the leaf expires. A Gateway
// refusal is final: the identity cannot be renewed, and the Operator must
// re-enroll once the leaf expires.
type OperatorCertRenewer struct {
	newClient func() (pubsub.PubSubClient, error)
	identity  *certs.ClientIdentity
	l2Trust   *governance.L2TrustStore
	channel   string
	logger    *slog.Logger

	attemptTimeout time.Duration
}

// NewOperatorCertRenewer builds the renewer for one Operator session.
// newClient opens a pub/sub client over the Operator's mTLS identity.
func NewOperatorCertRenewer(newClient func() (pubsub.PubSubClient, error), identity *certs.ClientIdentity, l2Trust *governance.L2TrustStore, operatorID, operatorSessionID string, logger *slog.Logger) *OperatorCertRenewer {
	return &OperatorCertRenewer{
		newClient:      newClient,
		identity:       identity,
		l2Trust:        l2Trust,
		channel:        pubsub.RenewalChannel(operatorID, operatorSessionID),
		logger:         logger,
		attemptTimeout: operatorRenewalAttemptTimeout,
	}
}

// Run renews the identity each time its leaf reaches two thirds of its
// lifetime, until ctx is done or renewal fails for good.
func (r *OperatorCertRenewer) Run(ctx context.Context) {
	for {
		leaf, err := r.currentLeaf()
		if err != nil {
			r.logger.Error("operator renewal: no current certificate", "error", err)
			return
		}
		renewAt := leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3)
		if !sleepUntil(ctx, renewAt) {
			return
		}
		if err := r.renewBefore(ctx, leaf.NotAfter); err != nil {
			if ctx.Err() == nil {
				r.logger.Error("operator renewal: stopped; the Operator re-enrolls when its certificate expires",
					"not_after", leaf.NotAfter, "error", err)
			}
			return
		}
	}
}

// renewBefore retries renewal with backoff until it succeeds, the Gateway
// refuses it, or notAfter passes.
func (r *OperatorCertRenewer) renewBefore(ctx context.Context, notAfter time.Time) error {
	backoff := time.Second
	for {
		err := r.renewOnce(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || errors.Is(err, constants.ErrOperatorRenewalRefused) {
			return err
		}
		wait := min(backoff, time.Until(notAfter))
		if wait <= 0 {
			return fmt.Errorf("%w: %w", constants.ErrOperatorRenewalExpired, err)
		}
		r.logger.Warn("operator renewal: attempt failed; retrying", "retry_in", wait, "error", err)
		if !sleepUntil(ctx, time.Now().Add(wait)) {
			return ctx.Err()
		}
		backoff = min(backoff*2, operatorRenewalMaxBackoff)
	}
}

// renewOnce runs one renewal exchange and installs its result.
func (r *OperatorCertRenewer) renewOnce(ctx context.Context) error {
	current, ok := r.identity.GetCertificate()
	if !ok || current.Leaf == nil {
		return constants.ErrLoadCertKeyPair
	}

	attemptCtx, cancel := context.WithTimeout(ctx, r.attemptTimeout)
	defer cancel()
	client, err := r.newClient()
	if err != nil {
		return fmt.Errorf("operator renewal: pub/sub client: %w", err)
	}
	defer client.Close()

	// Subscribe returns once the Gateway acknowledges the subscription, so
	// the reply to the request below cannot be missed.
	replies, err := client.Subscribe(attemptCtx, r.channel)
	if err != nil {
		return fmt.Errorf("operator renewal: subscribe: %w", err)
	}
	csrPEM, key, err := GenerateCSR(current.Leaf.Subject.CommonName)
	if err != nil {
		return err
	}
	requestID, err := uuid.NewString()
	if err != nil {
		return fmt.Errorf("operator renewal: request id: %w", err)
	}
	request, err := json.Marshal(models.OperatorRenewalFrame{
		Type:      models.OperatorRenewalFrameRequest,
		RequestID: requestID,
		Request:   &models.OperatorRenewalRequest{CSR: csrPEM},
	})
	if err != nil {
		return fmt.Errorf("operator renewal: marshal request: %w", err)
	}
	if err := client.Publish(attemptCtx, r.channel, request); err != nil {
		return fmt.Errorf("operator renewal: publish request: %w", err)
	}

	for {
		select {
		case <-attemptCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return constants.ErrOperatorRenewalTimeout
		case data, ok := <-replies:
			if !ok {
				return fmt.Errorf("operator renewal: %w", constants.ErrPubSubConnectionError)
			}
			var frame models.OperatorRenewalFrame
			if err := json.Unmarshal(data, &frame); err != nil || frame.RequestID != requestID {
				continue
			}
			switch frame.Type {
			case models.OperatorRenewalFrameRenewed:
				return r.install(current, key, frame.Renewed)
			case models.OperatorRenewalFrameError:
				if frame.Error == nil {
					return constants.ErrOperatorRenewalInvalidFrame
				}
				if !frame.Error.Retryable {
					return fmt.Errorf("%w: %s", constants.ErrOperatorRenewalRefused, frame.Error.Message)
				}
				return fmt.Errorf("operator renewal: gateway: %s", frame.Error.Message)
			}
		}
	}
}

// install checks the renewed identity against the request and the identity
// it replaces, then swaps in the L2 trust and the certificate. Nothing is
// applied unless both are valid.
func (r *OperatorCertRenewer) install(current tls.Certificate, key *ecdsa.PrivateKey, renewed *models.OperatorRenewal) error {
	if renewed == nil {
		return constants.ErrOperatorRenewalInvalidFrame
	}
	next, _, err := operatorKeyPair(renewed.Certificate, renewed.CertificateChain, key)
	if err != nil {
		return err
	}
	if err := verifyRenewedLeaf(current, next.Leaf); err != nil {
		return err
	}
	if err := r.l2Trust.Replace(renewed.L2Trust); err != nil {
		return fmt.Errorf("operator renewal: l2 trust: %w", err)
	}
	r.identity.SetCertificate(next)
	r.logger.Info("operator renewal: certificate renewed",
		"previous_serial", current.Leaf.SerialNumber.String(),
		"serial", next.Leaf.SerialNumber.String(),
		"not_after", next.Leaf.NotAfter)
	return nil
}

// verifyRenewedLeaf requires the renewed leaf to name the same workload as
// the current one and to be issued by a CA in the current identity's chain:
// a renewal never moves the Operator to another identity or issuer.
// operatorKeyPair has already matched the leaf to the requested key.
func verifyRenewedLeaf(current tls.Certificate, leaf *x509.Certificate) error {
	if leaf == nil || len(leaf.URIs) != 1 || len(current.Leaf.URIs) != 1 ||
		leaf.URIs[0].String() != current.Leaf.URIs[0].String() {
		return fmt.Errorf("%w: workload changed", constants.ErrOperatorRenewalReplyInvalid)
	}
	issuers := x509.NewCertPool()
	for _, der := range current.Certificate[1:] {
		ca, err := x509.ParseCertificate(der)
		if err == nil && ca.IsCA {
			issuers.AddCert(ca)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: issuers, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrOperatorRenewalReplyInvalid, err)
	}
	return nil
}

// currentLeaf returns the leaf the identity presents.
func (r *OperatorCertRenewer) currentLeaf() (*x509.Certificate, error) {
	cert, ok := r.identity.GetCertificate()
	if !ok || cert.Leaf == nil {
		return nil, constants.ErrLoadCertKeyPair
	}
	return cert.Leaf, nil
}

// sleepUntil waits until t or ctx is done; it reports whether t was reached.
func sleepUntil(ctx context.Context, t time.Time) bool {
	timer := time.NewTimer(time.Until(t))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
