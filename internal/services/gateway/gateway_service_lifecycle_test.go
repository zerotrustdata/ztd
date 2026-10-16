// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/storage"
)

type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestGatewayModeService_StartStop(t *testing.T) {
	var logBuf syncLogBuffer
	baseLogger, err := logging.NewLogger("info", &logBuf)
	require.NoError(t, err)
	ls := newTestGatewayService(t, testGatewayOpts{
		httpPort: 0,
		logger:   baseLogger.With(logging.ComponentKey, logging.ComponentGateway),
	})
	// Test configuration must keep real listeners on loopback so Windows does
	// not request firewall access for the test binary.
	for _, server := range []*http.Server{ls.server, ls.publicServer} {
		assert.NotNil(t, server.ErrorLog, "HTTP diagnostics must use the injected platform logger")
		host, _, err := net.SplitHostPort(server.Addr)
		require.NoError(t, err)
		assert.Equal(t, constants.LocalhostIP, host)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	select {
	case <-ls.Ready():
		t.Fatal("Ready must not be closed before Start")
	default:
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- ls.Start(ctx)
	}()

	select {
	case <-ls.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("service should become ready")
	}
	assert.True(t, ls.IsReady())

	httpPort := ls.GetHTTPPort()
	httpsPort := ls.GetHTTPSPort()
	assert.NotZero(t, httpPort, "HTTP port should be assigned")
	assert.NotZero(t, httpsPort, "HTTPS port should be assigned")
	assert.True(t, ls.running)

	// Exercise a real TLS handshake failure against the live HTTPS server and verify
	// that stdlib HTTP diagnostics route through the injected platform logger.
	_, tlsErr := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", fmt.Sprintf("127.0.0.1:%d", httpsPort), &tls.Config{
		RootCAs:    x509.NewCertPool(),
		MinVersion: tls.VersionTLS13,
	})
	require.Error(t, tlsErr)
	require.Eventually(t, func() bool {
		out := logBuf.String()
		return strings.Contains(out, "ERROR: http: TLS handshake error") &&
			strings.Contains(out, "component: gateway") &&
			strings.Contains(out, "server: HTTPS")
	}, 2*time.Second, 20*time.Millisecond, "TLS handshake failure must be formatted by the platform logger with server=HTTPS")

	err = ls.Start(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already running")

	cancel()
	err = <-errChan
	assert.NoError(t, err, "normal cancellation must not report http.ErrServerClosed")

	stopErr := ls.Stop(context.Background())
	assert.NoError(t, stopErr)

	assert.False(t, ls.running)
	assert.False(t, ls.IsReady())
	select {
	case <-ls.Ready():
	default:
		t.Fatal("Ready must stay closed across Stop")
	}
}

func TestGatewayModeService_StopWhenNotRunning(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})

	err := ls.Stop(context.Background())
	assert.NoError(t, err)
}

func TestGatewayModeService_ListenFailurePreservesCause(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	service := newTestGatewayService(t, testGatewayOpts{httpPort: 0})
	service.server.Addr = listener.Addr().String()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err = service.Start(ctx)
	require.Error(t, err)
	var networkErr *net.OpError
	require.ErrorAs(t, err, &networkErr)
	assert.Equal(t, "listen", networkErr.Op)
}

func TestGatewayModeService_SuspendedTxServiceSingleField(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})

	t.Run("suspendedTxService is non-nil after construction", func(t *testing.T) {
		assert.NotNil(t, ls.suspendedTxService)
	})

	t.Run("suspendedTxService satisfies SuspendedTransactionStore interface", func(t *testing.T) {
		var _ storage.SuspendedTransactionStore = ls.suspendedTxService
	})

	t.Run("Stop when not running closes suspendedTxService without error", func(t *testing.T) {
		err := ls.Stop(context.Background())
		assert.NoError(t, err)
	})

	t.Run("Stop when not running is idempotent for suspendedTxService", func(t *testing.T) {
		err := ls.Stop(context.Background())
		assert.NoError(t, err)
	})
}
