// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package operatorcmd

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// preflightGateway serves the Operator bootstrap websocket and listens on an
// mTLS stand-in port. bootstrapFrames receives, for each bootstrap socket, the
// number of data frames the peer sent before closing it.
type preflightGateway struct {
	httpPort        int
	httpsPort       int
	bootstrapFrames chan int
}

func startPreflightGateway(t *testing.T) *preflightGateway {
	t.Helper()
	gw := &preflightGateway{bootstrapFrames: make(chan int, 1)}
	var upgrader websocket.Upgrader
	bootstrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != constants.APIPaths.AuthOperatorBootstrapWebSocket {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		frames := 0
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				gw.bootstrapFrames <- frames
				return
			}
			frames++
		}
	}))
	t.Cleanup(bootstrap.Close)
	gw.httpPort = listenerPort(t, bootstrap.Listener)

	mtls, err := net.Listen("tcp", net.JoinHostPort(constants.LocalhostIP, "0"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mtls.Close() })
	go func() {
		for {
			conn, err := mtls.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	gw.httpsPort = listenerPort(t, mtls)
	return gw
}

func listenerPort(t *testing.T, l net.Listener) int {
	t.Helper()
	_, port, err := net.SplitHostPort(l.Addr().String())
	require.NoError(t, err)
	n, err := strconv.Atoi(port)
	require.NoError(t, err)
	return n
}

func unusedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(constants.LocalhostIP, "0"))
	require.NoError(t, err)
	port := listenerPort(t, l)
	require.NoError(t, l.Close())
	return port
}

func TestGatewayPreflightOpensTheBootstrapWebsocketWithoutEnrolling(t *testing.T) {
	gw := startPreflightGateway(t)
	require.NoError(t, runGatewayPreflight(context.Background(), constants.LocalhostIP, gw.httpPort, gw.httpsPort))
	assert.Zero(t, <-gw.bootstrapFrames, "preflight must not send a bootstrap request frame")
}

func TestGatewayPreflightFailsWhenTheBootstrapWebsocketIsUnreachable(t *testing.T) {
	gw := startPreflightGateway(t)
	err := runGatewayPreflight(context.Background(), constants.LocalhostIP, unusedPort(t), gw.httpsPort)
	require.ErrorIs(t, err, constants.ErrGatewayNotReachable)
	assert.Contains(t, err.Error(), "bootstrap websocket")
}

func TestGatewayPreflightFailsWhenTheHTTPPortServesNoBootstrapWebsocket(t *testing.T) {
	gw := startPreflightGateway(t)
	plain := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(plain.Close)
	err := runGatewayPreflight(context.Background(), constants.LocalhostIP, listenerPort(t, plain.Listener), gw.httpsPort)
	require.ErrorIs(t, err, constants.ErrGatewayNotReachable)
	assert.ErrorIs(t, err, websocket.ErrBadHandshake)
}

func TestGatewayPreflightFailsWhenTheMTLSPortIsUnreachable(t *testing.T) {
	gw := startPreflightGateway(t)
	err := runGatewayPreflight(context.Background(), constants.LocalhostIP, gw.httpPort, unusedPort(t))
	require.ErrorIs(t, err, constants.ErrGatewayNotReachable)
	assert.Contains(t, err.Error(), "mTLS port")
}
