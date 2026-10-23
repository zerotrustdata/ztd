// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package serve

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/auth"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

const mockBootstrapRequestID = "req-bootstrap-1"

// mockBootstrapSocket scripts one accepted bootstrap socket.
type mockBootstrapSocket struct {
	// dropAfterCreated closes the socket once the created frame is sent.
	dropAfterCreated bool
	// errorFrame, when set, is sent instead of the created frame.
	errorFrame *models.OperatorBootstrapError
}

// mockBootstrapGateway serves the Gateway side of the Operator bootstrap
// websocket: it records each request, issues the Operator certificate from a
// test CA, and verifies both proofs against the submitted CSRs.
type mockBootstrapGateway struct {
	t        *testing.T
	server   *httptest.Server
	caPEM    string
	caCert   *x509.Certificate
	caKey    *ecdsa.PrivateKey
	decision models.PlatformEnrollmentState
	approve  chan struct{}
	created  chan struct{}
	// onComplete runs once the complete frame's proofs verify, before the
	// owner decides: the client has recorded the request and not yet the
	// enrollment.
	onComplete func()
	// onRequest runs once the request frame is read, before the Gateway
	// creates the request: nothing has been announced yet.
	onRequest func(models.OperatorBootstrapRequest)

	mu       sync.Mutex
	sockets  []mockBootstrapSocket
	requests []models.OperatorBootstrapRequest
	proofsOK bool
}

func newMockBootstrapGateway(t *testing.T, sockets ...mockBootstrapSocket) *mockBootstrapGateway {
	t.Helper()
	mg := newUnstartedMockBootstrapGateway(t, sockets...)
	mg.server.Start()
	return mg
}

// newMockBootstrapGatewayTLS serves the bootstrap websocket over TLS, as the
// Gateway's HTTPS listener does for a wss:// bootstrap.
func newMockBootstrapGatewayTLS(t *testing.T) *mockBootstrapGateway {
	t.Helper()
	mg := newUnstartedMockBootstrapGateway(t)
	mg.server.StartTLS()
	return mg
}

func newUnstartedMockBootstrapGateway(t *testing.T, sockets ...mockBootstrapSocket) *mockBootstrapGateway {
	t.Helper()
	caPEM, caCert, caKey := generateTestCA(t)
	mg := &mockBootstrapGateway{
		t:        t,
		caPEM:    caPEM,
		caCert:   caCert,
		caKey:    caKey,
		decision: models.PlatformEnrollmentStateApproved,
		approve:  make(chan struct{}),
		created:  make(chan struct{}, 8),
		sockets:  sockets,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(constants.APIPaths.AuthOperatorBootstrapWebSocket, mg.serve)
	mg.server = httptest.NewUnstartedServer(mux)
	t.Cleanup(mg.server.Close)
	return mg
}

func (mg *mockBootstrapGateway) url() string {
	return OperatorBootstrapURL(mg.server.URL, 0)
}

func (mg *mockBootstrapGateway) dials() int {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	return len(mg.requests)
}

func (mg *mockBootstrapGateway) received() []models.OperatorBootstrapRequest {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	return append([]models.OperatorBootstrapRequest(nil), mg.requests...)
}

func (mg *mockBootstrapGateway) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var frame models.OperatorBootstrapFrame
	if err := conn.ReadJSON(&frame); err != nil || frame.Type != models.OperatorBootstrapFrameRequest || frame.Request == nil {
		return
	}
	req := *frame.Request
	mg.mu.Lock()
	mg.requests = append(mg.requests, req)
	var script mockBootstrapSocket
	if n := len(mg.requests); n <= len(mg.sockets) {
		script = mg.sockets[n-1]
	}
	mg.mu.Unlock()

	if script.errorFrame != nil {
		_ = conn.WriteJSON(models.OperatorBootstrapFrame{Type: models.OperatorBootstrapFrameError, Error: script.errorFrame})
		return
	}
	if models.PlatformEnrollmentTokenHash(req.Token) != req.Enrollment.TokenHash || req.Enrollment.Operator == nil {
		return
	}
	if mg.onRequest != nil {
		mg.onRequest(req)
	}
	operatorFP, err := auth.CSRFingerprint(req.Enrollment.Operator.OperatorCSRPEM)
	if err != nil {
		return
	}
	cliFP, err := auth.CSRFingerprint(req.Enrollment.Operator.CLICSRPEM)
	if err != nil {
		return
	}
	fingerprints := models.PlatformEnrollmentCSRFingerprints{Operator: operatorFP, CLI: cliFP}
	if err := conn.WriteJSON(models.OperatorBootstrapFrame{Type: models.OperatorBootstrapFrameCreated, Created: &models.PlatformEnrollmentCreateResponse{
		RequestID:     mockBootstrapRequestID,
		ComponentKind: models.PlatformComponentOperator,
		Fingerprints:  fingerprints,
		ExpiresAt:     time.Now().Add(30 * time.Minute).UTC(),
	}}); err != nil {
		return
	}
	mg.created <- struct{}{}
	if script.dropAfterCreated {
		return
	}

	// The proofs arrive with the request still pending; the owner has not
	// decided when the Gateway holds them.
	if err := conn.ReadJSON(&frame); err != nil || frame.Type != models.OperatorBootstrapFrameComplete || frame.Complete == nil {
		return
	}
	transcript, err := buildOperatorCompletionTranscript(mockBootstrapRequestID, req.Enrollment.TokenHash, req.Enrollment.InstanceID, operatorFP, cliFP)
	if err != nil {
		return
	}
	verified := mg.verifyProof(req.Enrollment.Operator.OperatorCSRPEM, frame.Complete.Operator, transcript) &&
		mg.verifyProof(req.Enrollment.Operator.CLICSRPEM, frame.Complete.CLI, transcript)
	mg.mu.Lock()
	mg.proofsOK = verified
	mg.mu.Unlock()
	if !verified {
		return
	}
	if mg.onComplete != nil {
		mg.onComplete()
	}

	select {
	case <-mg.approve:
	case <-r.Context().Done():
		return
	}
	if err := conn.WriteJSON(models.OperatorBootstrapFrame{Type: models.OperatorBootstrapFrameDecision, Decision: &models.PlatformEnrollmentStatusResponse{
		RequestID:     mockBootstrapRequestID,
		ComponentKind: models.PlatformComponentOperator,
		State:         mg.decision,
	}}); err != nil || mg.decision != models.PlatformEnrollmentStateApproved {
		return
	}
	_ = conn.WriteJSON(models.OperatorBootstrapFrame{Type: models.OperatorBootstrapFrameBundle, Bundle: &models.OperatorBootstrapBundle{
		Credentials: models.PlatformEnrollmentOperatorCredentials{
			OperatorCert:      signTestCSR(mg.t, req.Enrollment.Operator.OperatorCSRPEM, mg.caCert, mg.caKey),
			OperatorCertChain: mg.caPEM,
			HubTrustBundle:    mg.caPEM,
			OperatorID:        "op-1",
			OperatorSessionID: "op-session-1",
			CLISessionID:      "cli-session-1",
			CLICert:           signTestCSR(mg.t, req.Enrollment.Operator.CLICSRPEM, mg.caCert, mg.caKey),
			Posture:           "doctrine",
		},
		MaxConcurrentTasks: 25,
		MaxMemoryMB:        2048,
	}})
}

func (mg *mockBootstrapGateway) verifyProof(csrPEM, proof string, transcript []byte) bool {
	signature, err := base64.RawURLEncoding.DecodeString(proof)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(transcript)
	return ecdsa.VerifyASN1(csrPublicKey(mg.t, csrPEM), digest[:], signature)
}

func newTestBootstrapClient(t *testing.T, mg *mockBootstrapGateway, launchID string) (*OperatorBootstrapClient, fs.RuntimeFileService) {
	t.Helper()
	fileSvc := newTestFileSvc(t)
	deployment, err := NewOperatorDeploymentRecorder(fileSvc, launchID)
	require.NoError(t, err)
	client, err := NewOperatorBootstrapClient(OperatorBootstrapClientConfig{
		URL:               mg.url(),
		InstanceID:        "op-test-instance",
		Hostname:          "op-test-host",
		RuntimeConfig:     json.RawMessage(`{"platform":"linux"}`),
		Deployment:        deployment,
		Logger:            testLogger(),
		ActuatorPublicKey: strings.Repeat("ab", 32),
	})
	require.NoError(t, err)
	return client, fileSvc
}

func readTestDeploymentState(t *testing.T, fileSvc fs.RuntimeFileService) models.OperatorDeploymentState {
	t.Helper()
	data, err := fileSvc.ReadFile(context.Background(), operatorDeploymentStateRelPath())
	require.NoError(t, err)
	var state models.OperatorDeploymentState
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

func enrollContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestOperatorBootstrap_HeldSocketDeliversInMemoryIdentity proves the whole
// exchange runs on one socket held through the owner's decision, the proofs
// arrive before the decision and verify against the submitted keys, and the
// identity never touches disk.
func TestOperatorBootstrap_HeldSocketDeliversInMemoryIdentity(t *testing.T) {
	mg := newMockBootstrapGateway(t)
	client, fileSvc := newTestBootstrapClient(t, mg, "")

	// Runs on the server goroutine, so it asserts rather than requires. The
	// owner approves only once the proofs are held.
	mg.onComplete = func() {
		defer close(mg.approve)
		data, err := fileSvc.ReadFile(context.Background(), operatorDeploymentStateRelPath())
		var state models.OperatorDeploymentState
		if assert.NoError(t, err) && assert.NoError(t, json.Unmarshal(data, &state)) {
			assert.Equal(t, models.OperatorDeploymentPhasePendingApproval, state.Phase)
		}
	}
	identity, err := client.Enroll(enrollContext(t))
	require.NoError(t, err)

	assert.Equal(t, 1, mg.dials(), "one socket carries the whole exchange")
	mg.mu.Lock()
	assert.True(t, mg.proofsOK, "both proofs verify against the submitted CSRs")
	mg.mu.Unlock()

	assert.Equal(t, "op-1", identity.OperatorID)
	assert.Equal(t, "op-session-1", identity.OperatorSessionID)
	assert.Equal(t, constants.PostureDoctrine, identity.Posture)
	assert.Equal(t, mg.received()[0].Enrollment.SystemFingerprint, identity.SystemFingerprint)
	assert.Equal(t, []byte(mg.caPEM), identity.TrustBundlePEM)
	leaf, err := x509.ParseCertificate(identity.Certificate.Certificate[0])
	require.NoError(t, err)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: certPoolOf(mg.caCert), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	assert.NoError(t, err)

	state := readTestDeploymentState(t, fileSvc)
	assert.Equal(t, models.OperatorDeploymentPhaseEnrolled, state.Phase)
	assert.Equal(t, mockBootstrapRequestID, state.RequestID)
	assert.Equal(t, "op-session-1", state.OperatorSessionID)

	for _, rel := range []string{
		filepath.Join(constants.PkiDirname, constants.PkiFileOperatorCert),
		filepath.Join(constants.PkiDirname, constants.PkiFileOperatorKey),
		filepath.Join(constants.PkiDirname, constants.CliCertFilename),
		filepath.Join(constants.PkiDirname, constants.CliKeyFilename),
		filepath.Join(constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle),
		filepath.Join(constants.PkiDirname, constants.PkiSubdirPendingEnroll),
	} {
		exists, err := fileSvc.FileExists(context.Background(), rel)
		require.NoError(t, err)
		assert.False(t, exists, "%s must not be written", rel)
	}
}

// TestOperatorBootstrap_PresentsTheDeploymentLaunchID verifies that a worker
// launched by `operator deploy` puts its launch ID in the request, which keys
// the Gateway's pending-request announcement to the deploying CLI.
func TestOperatorBootstrap_PresentsTheDeploymentLaunchID(t *testing.T) {
	for name, launchID := range map[string]string{
		"deploy-launched": "35fe96f6-cb3c-4e7e-a392-ed72e84ac9ad",
		"started by hand": "",
	} {
		t.Run(name, func(t *testing.T) {
			mg := newMockBootstrapGateway(t)
			close(mg.approve)
			client, _ := newTestBootstrapClient(t, mg, launchID)

			_, err := client.Enroll(enrollContext(t))
			require.NoError(t, err)
			assert.Equal(t, launchID, mg.received()[0].Enrollment.DeploymentID)
		})
	}
}

// TestOperatorBootstrap_RecordsItsKeysBeforeTheRequestIsAnnounced proves the
// worker's CSR fingerprints are on disk before the Gateway can announce its
// launch ID, so a deploying CLI always has them to bind approval to, and that
// later progress keeps them.
func TestOperatorBootstrap_RecordsItsKeysBeforeTheRequestIsAnnounced(t *testing.T) {
	mg := newMockBootstrapGateway(t)
	close(mg.approve)
	client, fileSvc := newTestBootstrapClient(t, mg, "launch-1")
	type observation struct {
		recorded  models.OperatorDeploymentState
		requested models.PlatformEnrollmentCSRFingerprints
	}
	seen := make(chan observation, 1)
	// Runs on the server goroutine, so it asserts rather than requires.
	mg.onRequest = func(req models.OperatorBootstrapRequest) {
		var obs observation
		data, err := fileSvc.ReadFile(context.Background(), operatorDeploymentStateRelPath())
		if assert.NoError(t, err, "the state must exist before the request is created") {
			assert.NoError(t, json.Unmarshal(data, &obs.recorded))
		}
		var opErr, cliErr error
		obs.requested.Operator, opErr = auth.CSRFingerprint(req.Enrollment.Operator.OperatorCSRPEM)
		obs.requested.CLI, cliErr = auth.CSRFingerprint(req.Enrollment.Operator.CLICSRPEM)
		assert.NoError(t, opErr)
		assert.NoError(t, cliErr)
		seen <- obs
	}

	_, err := client.Enroll(enrollContext(t))
	require.NoError(t, err)
	obs := <-seen
	assert.Equal(t, models.OperatorDeploymentPhaseRequesting, obs.recorded.Phase)
	assert.Equal(t, "launch-1", obs.recorded.LaunchID)
	assert.NotEmpty(t, obs.requested.Operator)
	assert.NotEmpty(t, obs.requested.CLI)
	assert.Equal(t, obs.requested, obs.recorded.Fingerprints, "the recorded keys are the ones the request carries")

	final := readTestDeploymentState(t, fileSvc)
	assert.Equal(t, models.OperatorDeploymentPhaseEnrolled, final.Phase)
	assert.Equal(t, obs.requested, final.Fingerprints, "later progress keeps the recorded keys")
}

// TestOperatorBootstrap_WSSVerifiesTheGatewayAgainstThePinnedBundle proves a
// wss:// bootstrap trusts only the owner-pinned bundle and verifies the name
// the Gateway certificate carries, not the dialed IP.
func TestOperatorBootstrap_WSSVerifiesTheGatewayAgainstThePinnedBundle(t *testing.T) {
	newClient := func(t *testing.T, mg *mockBootstrapGateway, serverName string) *OperatorBootstrapClient {
		t.Helper()
		deployment, err := NewOperatorDeploymentRecorder(newTestFileSvc(t), "")
		require.NoError(t, err)
		client, err := NewOperatorBootstrapClient(OperatorBootstrapClientConfig{
			URL:               mg.url(),
			InstanceID:        "op-test-instance",
			Hostname:          "op-test-host",
			RuntimeConfig:     json.RawMessage(`{"platform":"linux"}`),
			TrustBundlePEM:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: mg.server.Certificate().Raw}),
			TLSServerName:     serverName,
			Deployment:        deployment,
			Logger:            testLogger(),
			ActuatorPublicKey: strings.Repeat("ab", 32),
		})
		require.NoError(t, err)
		return client
	}

	t.Run("the certificate's name verifies", func(t *testing.T) {
		mg := newMockBootstrapGatewayTLS(t)
		close(mg.approve)
		require.True(t, strings.HasPrefix(mg.url(), "wss://"))
		// httptest's certificate names example.com, as the Gateway's names ztd.local.
		identity, err := newClient(t, mg, "example.com").Enroll(enrollContext(t))
		require.NoError(t, err)
		assert.Equal(t, "op-1", identity.OperatorID)
	})

	t.Run("a name the certificate does not carry is refused before any frame", func(t *testing.T) {
		mg := newMockBootstrapGatewayTLS(t)
		close(mg.approve)
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		_, err := newClient(t, mg, constants.GatewayInternalHostname).Enroll(ctx)
		require.Error(t, err)
		assert.Zero(t, mg.dials(), "no request may reach a Gateway the pinned bundle does not verify")
	})
}

// TestOperatorBootstrap_DroppedSocketRedialsTheSameRequest proves a dropped
// socket is redialed with the same token, keys, and CSRs, so the Gateway
// resumes the one request instead of creating another.
func TestOperatorBootstrap_DroppedSocketRedialsTheSameRequest(t *testing.T) {
	mg := newMockBootstrapGateway(t, mockBootstrapSocket{dropAfterCreated: true})
	close(mg.approve)
	client, fileSvc := newTestBootstrapClient(t, mg, "")

	identity, err := client.Enroll(enrollContext(t))
	require.NoError(t, err)
	assert.Equal(t, "op-session-1", identity.OperatorSessionID)

	requests := mg.received()
	require.Len(t, requests, 2)
	assert.Equal(t, requests[0], requests[1], "the redial carries the identical in-memory request")
	assert.Equal(t, models.OperatorDeploymentPhaseEnrolled, readTestDeploymentState(t, fileSvc).Phase)
}

// TestOperatorBootstrap_GatewayErrorFrames proves a retryable error frame is
// redialed and a permanent one ends enrollment without another socket.
func TestOperatorBootstrap_GatewayErrorFrames(t *testing.T) {
	t.Run("retryable", func(t *testing.T) {
		mg := newMockBootstrapGateway(t, mockBootstrapSocket{errorFrame: &models.OperatorBootstrapError{Retryable: true, Message: "issuance in progress"}})
		close(mg.approve)
		client, _ := newTestBootstrapClient(t, mg, "")

		_, err := client.Enroll(enrollContext(t))
		require.NoError(t, err)
		assert.Equal(t, 2, mg.dials())
	})
	t.Run("permanent", func(t *testing.T) {
		mg := newMockBootstrapGateway(t, mockBootstrapSocket{errorFrame: &models.OperatorBootstrapError{Message: "invalid component"}})
		client, _ := newTestBootstrapClient(t, mg, "")

		_, err := client.Enroll(enrollContext(t))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid component")
		assert.Equal(t, 1, mg.dials())
	})
}

// TestOperatorBootstrap_TerminalDecisionsFailClosed proves a denial or expiry
// ends enrollment on the first socket.
func TestOperatorBootstrap_TerminalDecisionsFailClosed(t *testing.T) {
	for state, want := range map[models.PlatformEnrollmentState]string{
		models.PlatformEnrollmentStateDenied:  "denied",
		models.PlatformEnrollmentStateExpired: "expired",
	} {
		t.Run(string(state), func(t *testing.T) {
			mg := newMockBootstrapGateway(t)
			mg.decision = state
			close(mg.approve)
			client, _ := newTestBootstrapClient(t, mg, "")

			_, err := client.Enroll(enrollContext(t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
			assert.Equal(t, 1, mg.dials())
		})
	}
}

// TestOperatorBootstrap_CancellationEndsTheHeldSocket proves the held socket
// closes when the Operator is stopped before the owner decides.
func TestOperatorBootstrap_CancellationEndsTheHeldSocket(t *testing.T) {
	mg := newMockBootstrapGateway(t)
	client, _ := newTestBootstrapClient(t, mg, "")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-mg.created
		cancel()
	}()
	_, err := client.Enroll(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
	assert.Equal(t, 1, mg.dials())
}

// TestOperatorBootstrap_UnreachableGatewayIsRetriedUntilCancelled proves an
// Operator started before its Gateway keeps redialing rather than exiting.
func TestOperatorBootstrap_UnreachableGatewayIsRetriedUntilCancelled(t *testing.T) {
	mg := newMockBootstrapGateway(t)
	url := mg.url()
	mg.server.Close()
	client, _ := newTestBootstrapClient(t, mg, "")
	client.cfg.URL = url

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, err := client.Enroll(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, strings.Contains(err.Error(), "refused the bootstrap websocket"))
}

func certPoolOf(certs ...*x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, cert := range certs {
		pool.AddCert(cert)
	}
	return pool
}
