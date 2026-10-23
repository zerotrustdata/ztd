// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestPublicMirrorIngestAuthFailsClosedWithoutConfiguredToken verifies that an
// unconfigured ingest token rejects every request, including one that presents
// a bearer header, and that a configured token still requires an exact match.
func TestPublicMirrorIngestAuthFailsClosedWithoutConfiguredToken(t *testing.T) {
	t.Parallel()

	mirror := &PublicMirrorServer{logger: testutil.NewTestLogger()}
	for _, header := range []string{"", "Bearer ", "Bearer anything"} {
		request := httptest.NewRequest(http.MethodPost, "/ingest", nil)
		if header != "" {
			request.Header.Set("Authorization", header)
		}
		require.ErrorIs(t, mirror.ingestAuthorized(request), constants.ErrPublicFeedIngestTokenRequired, "header %q", header)
	}

	mirror.SetIngestAuthToken("configured-token")
	request := httptest.NewRequest(http.MethodPost, "/ingest", nil)
	require.ErrorIs(t, mirror.ingestAuthorized(request), constants.ErrPublicFeedIngestUnauthorized)
	request.Header.Set("Authorization", "Bearer wrong-token")
	require.ErrorIs(t, mirror.ingestAuthorized(request), constants.ErrPublicFeedIngestUnauthorized)
	request.Header.Set("Authorization", "Bearer configured-token")
	require.NoError(t, mirror.ingestAuthorized(request))
}

type mirrorFailedResponseWriter struct {
	http.ResponseWriter
	err error
}

func (w mirrorFailedResponseWriter) Write([]byte) (int, error) { return 0, w.err }

func TestPublicMirrorHTTPServer_DiagnosticsUseInjectedPlatformLogger(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger, err := logging.NewLogger("error", &output)
	require.NoError(t, err)
	logger = logger.With(logging.ComponentKey, logging.ComponentGateway, "server", "mirror")
	server := newPublicMirrorHTTPServer("127.0.0.1:0", http.NotFoundHandler(), logger)
	require.NotNil(t, server.ErrorLog)
	server.ErrorLog.Print("HTTP diagnostic")
	assert.Contains(t, output.String(), "ERROR: HTTP diagnostic")
	assert.Contains(t, output.String(), "component: gateway")
	assert.Contains(t, output.String(), "server: mirror")
}

func TestPublicMirror_ResponseWriteFailuresUsePlatformLogger(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		err   error
		level string
	}{
		{"broken pipe", fmt.Errorf("write: %w", syscall.EPIPE), "DEBUG"},
		{"connection reset", fmt.Errorf("write: %w", syscall.ECONNRESET), "DEBUG"},
		{"unexpected failure", constants.ErrInternal, "ERROR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			for _, compressed := range []bool{false, true} {
				var output bytes.Buffer
				logger, err := logging.NewLogger("debug", &output)
				require.NoError(t, err)
				mirror := &PublicMirrorServer{logger: logger.With(logging.ComponentKey, logging.ComponentGateway)}
				writer := mirrorFailedResponseWriter{ResponseWriter: httptest.NewRecorder(), err: test.err}
				handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					mirror.writeJSON(w, http.StatusOK, struct{ Value string }{Value: "public data"})
				})
				request := httptest.NewRequest(http.MethodGet, "/history", nil)
				if compressed {
					request.Header.Set("Accept-Encoding", "gzip")
					mirror.withGzip(handler).ServeHTTP(writer, request)
				} else {
					handler.ServeHTTP(writer, request)
				}
				assert.Contains(t, output.String(), test.level+": mirror:")
				assert.Contains(t, output.String(), "component: gateway")
				assert.NotContains(t, output.String(), "public data")
			}
		})
	}
}
