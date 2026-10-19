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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// deliberateEnvelope is a hashed-looking envelope for the deliberation tests.
func deliberateEnvelope(txHash string) *commonv1.GovernanceEnvelope {
	return &commonv1.GovernanceEnvelope{Id: txHash, TransactionHash: txHash, ActionType: ActionMcpCall}
}

// deliberateProof is an L2 proof with one vote, as the bound app would attach.
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

// deliberateClient serves Gateway binding discovery and the app's protobuf endpoint.
func deliberateClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	var endpoint string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == constants.APIPaths.AuthPlatformEnrollmentConsensus {
			assert.Empty(t, r.Header.Get(constants.HeaderCLISessionID), "binding discovery runs on the Operator identity, which never carries the CLI session")
			_ = json.NewEncoder(w).Encode(models.ConsensusPolicy{Enabled: true, EndpointURL: endpoint})
			return
		}
		handler(w, r)
	}))
	endpoint = srv.URL + "/app/decide"
	t.Cleanup(srv.Close)
	c, err := New(config.Config{MTLSBaseURL: srv.URL, PublicBaseURL: srv.URL})
	require.NoError(t, err)
	c.http = srv.Client()
	return c
}

func TestDeliberate_Returns200ProofForTheSentTransaction(t *testing.T) {
	env := deliberateEnvelope("tx-1")
	env.Payload = []byte{0xff, 0x00, 0xfe}
	var mu sync.Mutex
	var gotPath, gotMethod, gotContentType, gotCLISession string
	var gotBody []byte
	c := deliberateClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotMethod, gotBody = r.URL.Path, r.Method, body
		gotContentType = r.Header.Get("Content-Type")
		gotCLISession = r.Header.Get(constants.HeaderCLISessionID)
		mu.Unlock()
		signed := deliberateEnvelope("tx-1")
		signed.Governance = &commonv1.GovernanceMetadata{L2: deliberateProof()}
		wire, err := proto.Marshal(signed)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wire)
	})
	var exchanges []Exchange
	c.Record(&exchanges)

	proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance", CLISessionID: "gateway-cli-session"}, env)
	require.NoError(t, err)
	assert.True(t, proto.Equal(deliberateProof(), proof), "proof: %v", proof)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/app/decide", gotPath)
	assert.Equal(t, "application/x-protobuf", gotContentType)
	assert.Empty(t, gotCLISession, "Gateway session headers must stay on the Gateway")
	var sent commonv1.GovernanceEnvelope
	require.NoError(t, proto.Unmarshal(gotBody, &sent))
	assert.Equal(t, "tx-1", sent.GetTransactionHash())
	assert.True(t, proto.Equal(env, &sent), "the full envelope must reach the application")
	require.Len(t, exchanges, 2, "capture binding discovery and application deliberation")
	assert.Equal(t, gotBody, []byte(exchanges[1].ReqRaw), "capture protobuf bytes without altering them")
}

func TestDeliberate_NonOKAnswersWrapDeliberationFailed(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"bad request on hash mismatch", http.StatusBadRequest},
		{"app endpoint not found", http.StatusNotFound},
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
		wire, _ := proto.Marshal(signed)
		_, _ = w.Write(wire)
	})
	proof, err := c.Deliberate(context.Background(), Persona{ID: "conformance"}, deliberateEnvelope("tx-1"))
	require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
	assert.Nil(t, proof)
}

func TestDeliberate_AnswerWithoutVotesFails(t *testing.T) {
	c := deliberateClient(t, func(w http.ResponseWriter, _ *http.Request) {
		wire, _ := proto.Marshal(deliberateEnvelope("tx-1"))
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

func TestDeliberate_InvalidBindingDoesNotContactApplication(t *testing.T) {
	for _, tt := range []struct {
		name    string
		binding models.ConsensusPolicy
	}{
		{"disabled", models.ConsensusPolicy{EndpointURL: "https://app.invalid/decide"}},
		{"missing endpoint", models.ConsensusPolicy{Enabled: true}},
		{"plaintext endpoint", models.ConsensusPolicy{Enabled: true, EndpointURL: "http://app.invalid/decide"}},
		{"missing host", models.ConsensusPolicy{Enabled: true, EndpointURL: "https:///decide"}},
		{"malformed URL", models.ConsensusPolicy{Enabled: true, EndpointURL: "://invalid"}},
		{"URL credentials", models.ConsensusPolicy{Enabled: true, EndpointURL: "https://user:password@app.invalid/decide"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, constants.APIPaths.AuthPlatformEnrollmentConsensus, r.URL.Path)
				_ = json.NewEncoder(w).Encode(tt.binding)
			}))
			t.Cleanup(srv.Close)
			c, err := New(config.Config{MTLSBaseURL: srv.URL})
			require.NoError(t, err)
			c.http = srv.Client()
			proof, err := c.Deliberate(context.Background(), Persona{}, deliberateEnvelope("tx-1"))
			require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
			require.ErrorIs(t, err, constants.ErrConsensusEndpointInvalid)
			assert.Nil(t, proof)
		})
	}
}

func TestDeliberate_ApplicationRedirectIsRejected(t *testing.T) {
	c := deliberateClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://app.invalid/plaintext", http.StatusTemporaryRedirect)
	})
	proof, err := c.Deliberate(context.Background(), Persona{}, deliberateEnvelope("tx-1"))
	require.ErrorIs(t, err, constants.ErrIngressL2DeliberationFailed)
	assert.Nil(t, proof)
}
