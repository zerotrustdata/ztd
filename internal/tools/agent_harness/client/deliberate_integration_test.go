// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// deliberateEnvelope is a hashed-looking envelope for the deliberation tests.
func deliberateEnvelope(txHash string) *commonv1.GovernanceEnvelope {
	return &commonv1.GovernanceEnvelope{Id: txHash, TransactionHash: txHash, ActionType: ActionMcpCall}
}

// deliberateProof is an L2 proof with one vote, as the Gateway would attach.
func deliberateProof() *commonv1.L2Metadata {
	return &commonv1.L2Metadata{
		ConsensusSetId: "set-1",
		Votes: []*commonv1.L2Vote{{
			SignerKeyId:        "set-1-member-0",
			ConsensusSignature: "ab12",
			Decision:           true,
		}},
	}
}

// deliberateClient builds a client whose MTLSBaseURL is the test server.
func deliberateClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(config.Config{MTLSBaseURL: srv.URL, PublicBaseURL: srv.URL})
	require.NoError(t, err)
	return c
}

func TestDeliberate_Returns200ProofForTheSentTransaction(t *testing.T) {
	env := deliberateEnvelope("tx-1")
	var mu sync.Mutex
	var gotPath, gotMethod string
	var gotBody []byte
	c := deliberateClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotMethod, gotBody = r.URL.Path, r.Method, body
		mu.Unlock()
		signed := deliberateEnvelope("tx-1")
		signed.Governance = &commonv1.GovernanceMetadata{L2: deliberateProof()}
		wire, err := protojson.Marshal(signed)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wire)
	})

	proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance"}, env)
	require.NoError(t, err)
	assert.True(t, proto.Equal(deliberateProof(), proof), "proof: %v", proof)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, constants.APIPaths.ConsensusDeliberate, gotPath)
	var sent commonv1.GovernanceEnvelope
	require.NoError(t, protojson.Unmarshal(gotBody, &sent))
	assert.Equal(t, "tx-1", sent.GetTransactionHash())
}

func TestDeliberate_NonOKAnswersWrapDeliberationFailed(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"bad request on hash mismatch", http.StatusBadRequest},
		{"not found when the posture does not register the route", http.StatusNotFound},
		{"internal error", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := deliberateClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("refused"))
			})
			proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance"}, deliberateEnvelope("tx-1"))
			require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
			assert.Nil(t, proof)
		})
	}
}

func TestDeliberate_ProofForAnotherTransactionFails(t *testing.T) {
	c := deliberateClient(t, func(w http.ResponseWriter, _ *http.Request) {
		signed := deliberateEnvelope("tx-other")
		signed.Governance = &commonv1.GovernanceMetadata{L2: deliberateProof()}
		wire, _ := protojson.Marshal(signed)
		_, _ = w.Write(wire)
	})
	proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance"}, deliberateEnvelope("tx-1"))
	require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
	assert.Nil(t, proof)
}

func TestDeliberate_AnswerWithoutVotesFails(t *testing.T) {
	c := deliberateClient(t, func(w http.ResponseWriter, _ *http.Request) {
		wire, _ := protojson.Marshal(deliberateEnvelope("tx-1"))
		_, _ = w.Write(wire)
	})
	proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance"}, deliberateEnvelope("tx-1"))
	require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
	assert.Nil(t, proof)
}

func TestDeliberate_UndecodableAnswerFails(t *testing.T) {
	c := deliberateClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"unknown_field": 1}`))
	})
	proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance"}, deliberateEnvelope("tx-1"))
	require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
	assert.Nil(t, proof)
}
