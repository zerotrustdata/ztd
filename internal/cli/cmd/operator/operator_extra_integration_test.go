// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package operatorcmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOperatorModelReleaseCmd_BatchesAndVerifiesModels(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/ps" {
			_, _ = w.Write([]byte(`{"models":[]}`))
			return
		}
		require.Equal(t, "/api/generate", r.URL.Path)
		var request struct {
			Model     string          `json:"model"`
			KeepAlive json.RawMessage `json:"keep_alive"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		requests = append(requests, request.Model)
		assert.JSONEq(t, "0", string(request.KeepAlive))
		require.NoError(t, json.NewEncoder(w).Encode(struct {
			Model      string `json:"model"`
			Done       bool   `json:"done"`
			DoneReason string `json:"done_reason"`
		}{
			Model:      request.Model,
			Done:       true,
			DoneReason: "unload",
		}))
	}))
	t.Cleanup(server.Close)

	cmd := operatorModelCmd()
	cmd.SetArgs([]string{"release", "qwen3:0.6b", "gemma3:4b", "--inference-ollama-endpoint", server.URL})
	require.NoError(t, cmd.ExecuteContext(t.Context()))
	assert.Equal(t, []string{"qwen3:0.6b", "gemma3:4b"}, requests)
}
