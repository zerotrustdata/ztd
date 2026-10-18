// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestConsensusDeliberation_RejectsPlaintextAndRedirects(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, status)
			}))
			t.Cleanup(redirect.Close)
			client := redirect.Client()
			originalRedirect := client.CheckRedirect
			deliberator := infra.ConsensusStore.NewConsensusAppDeliberator(client)
			for _, endpoint := range []string{target.URL, redirect.URL} {
				data, err := json.Marshal(models.ConsensusPolicy{Enabled: true, EndpointURL: endpoint})
				require.NoError(t, err)
				require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionConsensus), string(constants.DocIDConsensusBinding), data))
				_, err = deliberator.Deliberate(t.Context(), []byte("private envelope"))
				require.ErrorIs(t, err, constants.ErrConsensusEndpointInvalid)
			}
			require.Equal(t, int32(0), received.Load())
			require.Equal(t, originalRedirect == nil, client.CheckRedirect == nil)
		})
	}
}
