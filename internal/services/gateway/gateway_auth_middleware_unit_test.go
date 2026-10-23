// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestAuthMiddleware_PublicHealthAndMissingClientCertificate(t *testing.T) {
	t.Parallel()

	logger := testutil.NewTestLogger()
	auth := NewAuthService(nil, nil, logger, nil, nil, response.NewWriter(logger), nil, "", "", "")
	for _, tc := range []struct {
		name, method, path string
		status             int
	}{
		{"public health reaches handler", http.MethodGet, constants.APIPaths.Health, http.StatusOK},
		{"websocket requires certificate", http.MethodGet, constants.APIPaths.PubSubWebSocket, http.StatusUnauthorized},
		{"settings mutation requires certificate", http.MethodPut, constants.APIPaths.DataSettings + "/" + string(constants.DocIDPlatformSettings), http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			called := false
			handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, nil))
			require.Equal(t, tc.status, rr.Code)
			assert.Equal(t, tc.status == http.StatusOK, called)
			if tc.status == http.StatusUnauthorized {
				assert.JSONEq(t, `{"error":"`+constants.ErrMTLSCertRequired.Error()+`"}`, rr.Body.String())
			}
		})
	}
}
