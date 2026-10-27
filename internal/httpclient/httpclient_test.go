// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package httpclient

import (
	"crypto/tls"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestWithTimeoutPreservesClientPolicyAndSource(t *testing.T) {
	transport := NewIPv4Transport(&tls.Config{MinVersion: tls.VersionTLS13})
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	source := &http.Client{Transport: transport, Jar: jar, Timeout: DefaultTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return constants.ErrForbidden }}
	for _, timeout := range []time.Duration{0, time.Minute} {
		configured := WithTimeout(source, timeout)
		assert.NotSame(t, source, configured)
		assert.Equal(t, timeout, configured.Timeout)
		assert.Equal(t, DefaultTimeout, source.Timeout)
		assert.Same(t, source.Transport, configured.Transport)
		assert.Same(t, source.Jar, configured.Jar)
		require.NotNil(t, configured.CheckRedirect)
		require.ErrorIs(t, configured.CheckRedirect(nil, nil), constants.ErrForbidden)
	}
}

func TestConstants(t *testing.T) {
	assert.Equal(t, 30*time.Second, DefaultTimeout)
	assert.Equal(t, 10*time.Second, DefaultDialTimeout)
	assert.Equal(t, 10*time.Second, DefaultTLSTimeout)
	assert.Equal(t, 90*time.Second, DefaultIdleConnTimeout)
}

func TestWebSocketDialerWithTLS(t *testing.T) {
	customTLS := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}

	dialer := WebSocketDialerWithTLS(customTLS)
	require.NotNil(t, dialer)

	assert.Equal(t, customTLS, dialer.TLSClientConfig)
	assert.Equal(t, DefaultTLSTimeout, dialer.HandshakeTimeout)
	assert.NotNil(t, dialer.NetDialContext)
}
