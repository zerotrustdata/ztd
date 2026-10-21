// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build e2e

package testcmd

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	"google.golang.org/protobuf/proto"
)

const evalLoopConsensusPrimaryApp = "eval-loop-consensus-1"

// evalLoopConsensusApp is the real HTTPS protobuf application bound to the
// Gateway. It uses three independently enrolled, sealed L2 identities and
// returns two distinct signatures for each transaction.
type evalLoopConsensusApp struct {
	server *http.Server
	count  atomic.Int64
	keys   []evalLoopConsensusSigner
}

type evalLoopConsensusSigner struct {
	id  string
	key ed25519.PrivateKey
}

func (a *evalLoopConsensusApp) DeliberationCount() int { return int(a.count.Load()) }

func (a *evalLoopConsensusApp) close(ctx context.Context) error {
	var errs []error
	if a.server != nil {
		if err := a.server.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	for _, signer := range a.keys {
		vault.SecureZero(signer.key)
	}
	a.keys = nil
	return errors.Join(errs...)
}

// provisionEvalLoopConsensus enrolls three real application identities,
// starts their primary app HTTPS endpoint, then binds the explicit 2-of-3
// policy through the governed CLI endpoint. The CLI approval responder must
// already be running for this notary-posture flow.
func provisionEvalLoopConsensus(t *testing.T, r *releaseSmokeRun) *evalLoopConsensusApp {
	t.Helper()
	app := &evalLoopConsensusApp{}
	for _, appID := range []string{evalLoopConsensusPrimaryApp, "eval-loop-consensus-2", "eval-loop-consensus-3"} {
		if _, err := r.ztdGateway(t.Context(), "enroll-"+appID, "auth", "enroll", "app", appID, "--signing-key"); err != nil {
			t.Fatalf("enroll consensus application %s: %v", appID, err)
		}
	}

	cfg, err := cliconfig.Load(r.gwRoot)
	if err != nil {
		t.Fatalf("load Gateway runtime config for consensus apps: %v", err)
	}
	fileSvc, err := shared.NewFileSvc(r.gwRoot, nil)
	if err != nil {
		t.Fatalf("open Gateway runtime for consensus apps: %v", err)
	}
	cfg.Keyring = r.agent.Keyring(fileSvc)
	for _, appID := range []string{evalLoopConsensusPrimaryApp, "eval-loop-consensus-2", "eval-loop-consensus-3"} {
		key, err := auth.LoadAppSigningKey(t.Context(), fileSvc, cfg, appID)
		if err != nil {
			for _, signer := range app.keys {
				vault.SecureZero(signer.key)
			}
			t.Fatalf("load sealed consensus signing identity %s: %v", appID, err)
		}
		app.keys = append(app.keys, evalLoopConsensusSigner{id: appID, key: key})
	}
	primaryIdentity, err := auth.LoadAppIdentity(fileSvc, cfg, evalLoopConsensusPrimaryApp)
	if err != nil {
		t.Fatalf("load enrolled primary app TLS identity: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for consensus app: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	endpointURL := fmt.Sprintf("https://localhost:%d/deliberate", port)
	app.server = &http.Server{
		Handler:           http.HandlerFunc(app.handleDeliberate),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{*primaryIdentity}},
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- app.server.ServeTLS(listener, "", "") }()
	r.addCleanup("consensus application", func(ctx context.Context) error {
		err := app.close(ctx)
		serveErr := <-serveResult
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			err = errors.Join(err, serveErr)
		}
		return err
	})
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = app.close(cleanupCtx)
	})

	args := []string{"consensus", "enroll", "app", evalLoopConsensusPrimaryApp, endpointURL,
		"--member-app", evalLoopConsensusPrimaryApp, "--member-app", "eval-loop-consensus-2", "--member-app", "eval-loop-consensus-3", "--quorum", "2"}
	if _, err := r.ztdGateway(t.Context(), "bind-eval-loop-consensus", args...); err != nil {
		t.Fatalf("governed CLI bind of 2-of-3 consensus app: %v", err)
	}
	return app
}

func (a *evalLoopConsensusApp) handleDeliberate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/deliberate" || r.Header.Get("Content-Type") != "application/x-protobuf" {
		http.Error(w, "expected protobuf deliberation POST", http.StatusBadRequest)
		return
	}
	wire, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read protobuf envelope", http.StatusBadRequest)
		return
	}
	var envelope commonv1.GovernanceEnvelope
	if err := proto.Unmarshal(wire, &envelope); err != nil || strings.TrimSpace(envelope.GetTransactionHash()) == "" {
		http.Error(w, "decode protobuf envelope", http.StatusBadRequest)
		return
	}
	votes := make([]*commonv1.L2Vote, 0, 2)
	for _, signer := range a.keys[:2] {
		signature := ed25519.Sign(signer.key, governance.L2VotePayload(envelope.GetTransactionHash(), true))
		votes = append(votes, &commonv1.L2Vote{
			SignerKeyId: signer.id, Decision: true, ConsensusSignature: fmt.Sprintf("%x", signature),
		})
	}
	// Add L2 votes only; the Gateway rejects a response that changes any other
	// governance field, such as the L3 proof carried under notary.
	if envelope.Governance == nil {
		envelope.Governance = &commonv1.GovernanceMetadata{}
	}
	envelope.Governance.L2 = &commonv1.L2Metadata{
		ConsensusSetId: string(constants.DocIDConsensusBinding), Votes: votes,
	}
	response, err := proto.Marshal(&envelope)
	if err != nil {
		http.Error(w, "encode protobuf envelope", http.StatusInternalServerError)
		return
	}
	a.count.Add(1)
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response)
}
