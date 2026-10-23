// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestGatewayModeService_RenewServiceCertWithIdentity_LeavesACurrentCert(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})
	require.NotEmpty(t, ls.pki.serviceCert.Certificate)

	err := ls.renewServiceCertWithIdentity(testutil.Context(t))
	require.NoError(t, err)
	assert.False(t, isExpiringSoon(ls.pki.serviceCert), "a successful renewal leaves a certificate outside the renewal window")
}

func TestGatewayModeService_RunServiceCertRenewalLoop(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan bool)
	go func() {
		ls.runServiceCertRenewalLoop(ctx)
		done <- true
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runServiceCertRenewalLoop did not return promptly with cancelled context")
	}
}
