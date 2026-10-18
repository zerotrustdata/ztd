//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package profiling

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_ServesHeapProfileUntilStopped(t *testing.T) {
	srv, err := Start("127.0.0.1:0", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	url := "http://" + srv.Addr() + "/debug/pprof/heap"

	resp, err := http.Get(url)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, body)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, srv.Stop(ctx))

	resp2, err := http.Get(url)
	if err == nil {
		_ = resp2.Body.Close()
	}
	assert.Error(t, err)
}
