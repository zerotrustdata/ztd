// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// --- handleSwaggerUI ---

func TestHandleSwaggerUI_ReturnsHTMLContentType(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/swagger/", nil)

	handleSwaggerUI(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "text/html; charset=utf-8", rr.Header().Get("Content-Type"))
}

func TestHandleSwaggerUI_BodyContainsSwaggerUIElements(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)

	handleSwaggerUI(rr, req)

	body := rr.Body.String()
	assert.Contains(t, body, "swagger-ui")
	assert.Contains(t, body, "SwaggerUIBundle")
	assert.Contains(t, body, "/swagger/doc.json")
}

func TestHandleSwaggerUI_IgnoresMethod(t *testing.T) {
	t.Parallel()

	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(method, "/swagger/", nil)

			handleSwaggerUI(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, "text/html; charset=utf-8", rr.Header().Get("Content-Type"))
		})
	}
}

// --- registerMCPRoutes ---

func TestRegisterMCPRoutes_RegistersMCPAndA2APaths(t *testing.T) {
	t.Parallel()

	paths := []string{constants.APIPaths.MCPEndpoint, constants.APIPaths.A2ACall}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			handlerCalled := 0
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handlerCalled++
				w.WriteHeader(http.StatusOK)
			})
			registerMCPRoutes(mux, handler, true)

			req := httptest.NewRequest(http.MethodPost, path, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code, "path %s should be registered and handled", path)
			assert.Equal(t, 1, handlerCalled, "handler should be called once for %s", path)
		})
	}
}

func TestRegisterMCPRoutes_A2ANotMountedWithoutDownstream(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	registerMCPRoutes(mux, handler, false)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, constants.APIPaths.MCPEndpoint, nil))
	assert.Equal(t, http.StatusOK, rr.Code, "MCP ingress stays mounted")

	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, constants.APIPaths.A2ACall, nil))
	assert.Equal(t, http.StatusNotFound, rr.Code, "A2A ingress is not mounted without a downstream")
}

func TestRegisterMCPRoutes_UnregisteredPathsReturn404(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	registerMCPRoutes(mux, handler, true)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nonexistent", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code, "unregistered path should return 404")
}

func TestRegisterMCPRoutes_BothPathsDeferToSameHandler(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Handler", "called")
		w.WriteHeader(http.StatusOK)
	})

	registerMCPRoutes(mux, handler, true)

	for _, path := range []string{constants.APIPaths.MCPEndpoint, constants.APIPaths.A2ACall} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, path, nil)
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			require.Equal(t, http.StatusOK, rr.Code)
			assert.Equal(t, "called", rr.Header().Get("X-Test-Handler"), "handler should be the same for %s", path)
		})
	}
}

// --- handleSwaggerDoc ---

func TestHandleSwaggerDoc_ServesEmbeddedSpec(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	handleSwaggerDoc(rr, httptest.NewRequest(http.MethodGet, constants.SwaggerDocPath, nil))

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	var spec struct {
		Swagger string                     `json:"swagger"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &spec))
	assert.Equal(t, "2.0", spec.Swagger)
	assert.NotEmpty(t, spec.Paths)
}

func TestHandleSwaggerDoc_RejectsWrites(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	handleSwaggerDoc(rr, httptest.NewRequest(http.MethodPost, constants.SwaggerDocPath, nil))

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	assert.Equal(t, "GET, HEAD", rr.Header().Get("Allow"))
}
