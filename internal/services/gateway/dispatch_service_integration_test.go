// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/execution"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	govsvc "github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/governance/governancetest"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	inferdispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/protocol"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// seedOperatorForDispatch registers an active user and an active operator
// document in the doc store so that AuthService.ValidateOperatorSession
// resolves the session. Returns the operator ID, session ID, and user ID.
func seedOperatorForDispatch(t *testing.T, infra *TestInfrastructure) (operatorID, operatorSessionID, userID string) {
	t.Helper()
	operatorID = "op-dispatch-int"
	operatorSessionID = "sess-dispatch-int"
	userID = "user-dispatch-int"

	userDoc := &models.User{
		ID:     userID,
		Status: constants.UserStatusActive,
	}
	userBytes, err := json.Marshal(userDoc)
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionUsers), userID, userBytes))

	now := time.Now().UTC()
	opDoc := &operatorv1.OperatorDocument{
		Id:                operatorID,
		UserId:            userID,
		OrganizationId:    "org-dispatch-int",
		Status:            string(constants.OperatorStatusActive),
		OperatorSessionId: operatorSessionID,
		CreatedAt:         timestamppb.New(now),
		UpdatedAt:         timestamppb.New(now),
	}
	opBytes, err := models.MarshalOperatorDocument(opDoc)
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), operatorID, opBytes))

	return operatorID, operatorSessionID, userID
}

// seedCLISessionForDispatch registers a CLI session for the given user so the
// auth middleware's handleCLIAuth path accepts the mTLS cert. Returns the CLI
// session ID and a self-signed cert with a matching CLI SPIFFE URI SAN.
func seedCLISessionForDispatch(t *testing.T, infra *TestInfrastructure, userID string) (cliSessionID string, cert *x509.Certificate) {
	t.Helper()
	cliSessionID = "cli-dispatch-int"

	cliDoc := &models.CLISession{
		ID:        cliSessionID,
		UserID:    userID,
		ExpiresAt: time.Now().Add(1 * time.Hour),
		IsActive:  true,
	}
	cliBytes, err := json.Marshal(cliDoc)
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, cliBytes))

	wid := protocol.NewWorkloadIdentity()
	cliURI, err := wid.CLISPIFFEURL(userID, cliSessionID)
	require.NoError(t, err)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "test-cli-dispatch"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{cliURI},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)
	cert, err = x509.ParseCertificate(certDER)
	require.NoError(t, err)

	return cliSessionID, cert
}

// TestDispatchController_HandleDispatch_RoundTrip verifies the full HTTP
// round-trip through the built public router: POST /api/v1/operators/commands
// publishes a signed GovernanceEnvelope to the target operator's cmd channel,
// a simulated operator handler receives it, publishes a result on the results
// channel, and the correlated result is returned in the HTTP response. This
// exercises the real in-process WS broker, the real AuthService
// ValidateOperatorSession path, the real StateRootService, and the real auth
// middleware (mTLS CLI session).
func TestDispatchController_HandleDispatch_RoundTrip(t *testing.T) {
	h, _, infra := setupTestHTTPHandler(t)

	operatorID, operatorSessionID, requestorUserID := seedOperatorForDispatch(t, infra)
	cliSessionID, cliCert := seedCLISessionForDispatch(t, infra, requestorUserID)

	// The simulated operator's Actuator key is bound on its document, so the
	// final receipt it signs verifies against the operator's bound key.
	actuatorPub, actuatorPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	actuatorKeyID := hex.EncodeToString(actuatorPub)
	bindOperatorActuatorKey(t, infra, operatorID, actuatorKeyID)

	// Register a simulated operator on the cmd channel. The operator
	// unmarshals the command, asserts the envelope carries the gateway's
	// state root and targets the right session, then publishes a result
	// envelope on the results channel with Id == command TransactionHash,
	// and its signed final receipt on the receipts channel.
	broker := infra.Pubsub
	cmdChannel := pubsub.CmdChannel(operatorID, operatorSessionID)
	resultsChannel := pubsub.ResultsChannel(operatorID, operatorSessionID)
	simulatedOperator := &operatorv1.OperatorDocument{Id: operatorID, OperatorSessionId: operatorSessionID}

	operatorDone := make(chan struct{})
	var receivedCmd *commonv1.GovernanceEnvelope
	unregisterOperator := broker.RegisterHandler(cmdChannel, func(channel string, data []byte) {
		defer close(operatorDone)
		cmdEnv := &commonv1.GovernanceEnvelope{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, cmdEnv); err != nil {
			t.Errorf("operator: unmarshal command: %v", err)
			return
		}
		receivedCmd = cmdEnv
		// The envelope must carry the gateway's current state root.
		assert.NotEmpty(t, cmdEnv.StateMerkleRoot, "dispatched envelope must carry the gateway state root")
		assert.Equal(t, operatorID, cmdEnv.OperatorId, "envelope must target the correct operator")
		assert.Equal(t, operatorSessionID, cmdEnv.OperatorSessionId, "envelope must target the correct session")
		assert.NotEmpty(t, cmdEnv.Nonce, "envelope must carry a nonce")
		assert.Equal(t, cmdEnv.Id, cmdEnv.TransactionHash, "Id must equal TransactionHash")
		assert.Equal(t, string(constants.ActionTypeFsRead), cmdEnv.ActionType, "action type must be FS_READ")
		assert.Equal(t, requestorUserID, cmdEnv.RequestorUserId, "envelope must carry the requestor user ID from mTLS context")

		// Publish a correlated result envelope on the results channel, then
		// the signed final receipt on the receipts channel.
		resultEnv := dispatchTestResultEnvelope(cmdEnv, nil)
		resultWire, err := protojson.Marshal(resultEnv)
		require.NoError(t, err)
		broker.Publish(resultsChannel, resultWire)
		publishDispatchTestReceipt(t, broker, simulatedOperator, cmdEnv, actuatorPriv, actuatorKeyID)
	})
	t.Cleanup(unregisterOperator)

	// Build the FS_READ payload (proto-marshaled FsReadRequested).
	fsReadReq := &operatorv1.FsReadRequested{Path: "/etc/hostname"}
	payload, err := proto.Marshal(fsReadReq)
	require.NoError(t, err)

	reqBody := OperatorCommandRequest{
		TargetOperatorSessionID: operatorSessionID,
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 payload,
		TargetResource:          "/etc/hostname",
	}
	bodyBytes, err := json.Marshal(reqBody)
	require.NoError(t, err)

	// The dispatch route is mTLS-protected. Stamp the request with a CLI
	// mTLS cert + CLI session header so the auth middleware's handleCLIAuth
	// path validates the session and stamps ContextKeyUserID.
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.OperatorsCommands, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cliCert},
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "dispatch HTTP response body: %s", rr.Body.String())

	resp := &operatorv1.GovernedDispatchResult{}
	require.NoError(t, protojson.Unmarshal(rr.Body.Bytes(), resp), "response body: %s", rr.Body.String())
	assert.NotEmpty(t, resp.GetTransactionId(), "response must carry the transaction ID")
	assert.Equal(t, string(constants.ActionTypeFsRead), resp.GetActionType(), "response action type must echo FS_READ")
	require.NotNil(t, resp.GetReceipt(), "the 200 body must carry the executing Operator's final receipt")
	assert.NoError(t, govsvc.VerifyActionReceiptSignature(resp.GetReceipt(), actuatorPub), "the receipt must verify against the operator's actuator key")
	assert.NoError(t, govsvc.VerifyReceiptPersistenceAttestation(resp.GetReceipt(), actuatorPub))

	// The simulated operator must have received the command.
	select {
	case <-operatorDone:
	case <-time.After(5 * time.Second):
		t.Fatal("simulated operator did not receive the command on the cmd channel")
	}
	require.NotNil(t, receivedCmd)
	assert.Equal(t, resp.GetTransactionId(), receivedCmd.Id, "response transaction ID must match the dispatched envelope Id")
}

// TestDispatchController_HandleDispatch_UnknownSession verifies that
// dispatching to an unregistered operator session fails closed with a 500
// error (the dispatch service wraps the auth error) and does not publish to
// any cmd channel.
func TestDispatchService_ShutdownPublishesReceiptThenAcknowledgementBeforeCancellation(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	_, _, userID := seedOperatorForDispatch(t, infra)
	operatorID := "shutdown-lifecycle-operator"
	sessionID := "shutdown-lifecycle-session"
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, false)

	remoteCfg := *infra.Cfg
	remoteCfg.OperatorID = operatorID
	remoteCfg.OperatorSessionId = sessionID
	remoteCfg.HeartbeatInterval = 0
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := hex.EncodeToString(publicKey)
	require.NoError(t, infra.SignerStore.AddTrustedSigner(t.Context(), models.TrustedSigner{ID: keyID, PublicKey: keyID, AddedAt: time.Now().UTC(), Enabled: true}))
	// The final receipt is accepted only under the Operator's bound Actuator key.
	bindOperatorActuatorKey(t, infra, operatorID, keyID)
	client := pubsub.NewInProcessPubSubClient(infra.Pubsub, infra.Logger)
	scrubbingSvc, err := scrubbing.NewScrubbingService(context.Background(), scrubbing.DefaultConfig(), infra.Logger, nil)
	require.NoError(t, err)
	results, err := pubsub.NewPubSubResultsService(&remoteCfg, infra.Logger, client, scrubbingSvc)
	require.NoError(t, err)
	operatorSvc, err := pubsub.NewOperatorPubSubService(withRequiredOperatorDeps(t, pubsub.CommandServiceConfig{
		Ledger:             storagetest.NewTestLedger(t),
		RuntimeVault:       governancetest.UnlockedVault{},
		Config:             &remoteCfg,
		Logger:             infra.Logger,
		PubSubClient:       client,
		ResultsService:     results,
		ActuatorSigningKey: privateKey,
		ActuatorKeyID:      keyID,
		AuditorSigningKey:  privateKey,
		AuditorKeyID:       keyID,
		Scrubbing:          scrubbingSvc,
		AuditStore:         infra.AuditStore,
	}), pubsub.OutboundModeDeps{GovernanceCoreDeps: pubsub.GovernanceCoreDeps{
		ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{operatorID}, SessionID: sessionID},
		ReplayStore:       infra.ReplayStore,
		StateRootProvider: infra.StateRootSvc,
		TransactionAudit:  infra.AuditStore,
		SignerStore:       infra.SignerStore,
		Doctrine:          govsvc.NewL1Doctrine(),
	}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, operatorSvc.Start(ctx))
	t.Cleanup(func() {
		cancel()
		require.NoError(t, operatorSvc.Stop())
	})

	var orderMu sync.Mutex
	order := make([]string, 0, 3)
	record := func(step string) {
		orderMu.Lock()
		order = append(order, step)
		orderMu.Unlock()
	}
	unregisterReceipt := infra.Pubsub.RegisterHandler(pubsub.ReceiptsChannel(operatorID, sessionID), func(_ string, _ []byte) { record("receipt") })
	unregisterResult := infra.Pubsub.RegisterHandler(pubsub.ResultsChannel(operatorID, sessionID), func(_ string, _ []byte) { record("result") })
	t.Cleanup(unregisterReceipt)
	t.Cleanup(unregisterResult)
	shutdownObserved := make(chan string, 1)
	go func() {
		reason := <-operatorSvc.ShutdownChan
		record("shutdown")
		shutdownObserved <- reason
		cancel()
	}()

	cmdChannel := pubsub.CmdChannel(operatorID, sessionID)
	require.Eventually(t, func() bool {
		return infra.Pubsub.ChannelSubscriberCount(cmdChannel) > 0 || handlerCount(infra.Pubsub, cmdChannel) > 0
	}, time.Second, 10*time.Millisecond)
	payload, err := proto.Marshal(&operatorv1.ShutdownRequested{Reason: "planned maintenance"})
	require.NoError(t, err)
	dispatch := NewDispatchService(infra.Logger, infra.Pubsub, infra.StateRootSvc, infra.Auth, string(config.PostureDoctrine), govsvc.NewL1Doctrine(), nil, infra.SignerStore, nil)
	result, err := dispatch.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: sessionID,
		EventType:               string(constants.Event.Operator.ShutdownRequested),
		Payload:                 payload,
		TargetResource:          operatorID,
		RequestorUserID:         userID,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, result.TransactionID)
	assert.Equal(t, "planned maintenance", <-shutdownObserved)
	require.Eventually(t, func() bool { return handlerCount(infra.Pubsub, cmdChannel) == 0 }, time.Second, 10*time.Millisecond)
	orderMu.Lock()
	assert.Equal(t, []string{"receipt", "result", "shutdown"}, order)
	orderMu.Unlock()
}

func handlerCount(broker *GatewayWebSocketHandler, channel string) int {
	broker.handlersMu.RLock()
	defer broker.handlersMu.RUnlock()
	return len(broker.handlers[channel])
}

// TestDispatch_UnknownSession_ReturnsIngressRefusal proves a dispatch that
// names no operator session is refused as OPERATOR_SESSION_NOT_FOUND through
// the real router, before any envelope exists.
func TestDispatch_UnknownSession_ReturnsIngressRefusal(t *testing.T) {
	h, _, infra := setupTestHTTPHandler(t)

	// Seed a CLI session for mTLS auth, but target a nonexistent operator.
	_, _, requestorUserID := seedOperatorForDispatch(t, infra)
	cliSessionID, cliCert := seedCLISessionForDispatch(t, infra, requestorUserID)

	fsReadReq := &operatorv1.FsReadRequested{Path: "/etc/hostname"}
	payload, err := proto.Marshal(fsReadReq)
	require.NoError(t, err)

	reqBody := OperatorCommandRequest{
		TargetOperatorSessionID: "nonexistent-session-int",
		EventType:               string(constants.Event.Operator.FsRead.Requested),
		Payload:                 payload,
	}
	bodyBytes, err := json.Marshal(reqBody)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.OperatorsCommands, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cliCert},
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code, "unknown operator session must fail closed")
	refusal := &operatorv1.IngressRefusal{}
	require.NoError(t, protojson.Unmarshal(rr.Body.Bytes(), refusal), "response body: %s", rr.Body.String())
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_OPERATOR_SESSION_NOT_FOUND, refusal.GetCode())
	assert.Contains(t, refusal.GetError(), constants.ErrGatewayOperatorSessionInvalid.Error())
}

// TestDispatch_DoctrineViolation_ReturnsIngressRefusal proves an EXECUTE_BASH
// whose command matches a doctrine prohibition is refused L1_DOCTRINE with 403
// at build time: no receipt is returned and nothing reaches the operator's
// command channel.
func TestDispatch_DoctrineViolation_ReturnsIngressRefusal(t *testing.T) {
	h, _, infra := setupTestHTTPHandler(t)
	h.dispatchController = newDispatchController(DispatchControllerDeps{
		DispatchSvc: NewDispatchService(infra.Logger, infra.Pubsub, infra.StateRootSvc, infra.Auth, string(config.PostureDoctrine), govsvc.NewL1Doctrine(), nil, infra.SignerStore, nil),
		Responder:   infra.Responder,
		Logger:      infra.Logger,
	})
	h.router = h.buildPublicRouter()
	operatorID, operatorSessionID, requestorUserID := seedOperatorForDispatch(t, infra)
	cliSessionID, cliCert := seedCLISessionForDispatch(t, infra, requestorUserID)
	published := make(chan struct{}, 1)
	unregister := infra.Pubsub.RegisterHandler(pubsub.CmdChannel(operatorID, operatorSessionID), func(_ string, _ []byte) {
		published <- struct{}{}
	})
	t.Cleanup(unregister)

	payload, err := proto.Marshal(&operatorv1.CommandRequested{
		Command:     "rm -rf /",
		ExecutionId: "exec-doctrine-bash",
	})
	require.NoError(t, err)
	body, err := json.Marshal(OperatorCommandRequest{
		TargetOperatorSessionID: operatorSessionID,
		EventType:               string(constants.Event.Operator.Command.Requested),
		Payload:                 payload,
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.OperatorsCommands, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cliCert}}
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "response body: %s", rr.Body.String())
	refusal := &operatorv1.IngressRefusal{}
	require.NoError(t, protojson.Unmarshal(rr.Body.Bytes(), refusal), "response body: %s", rr.Body.String())
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE, refusal.GetCode())
	assert.Equal(t, constants.ErrTxL1ValidationFailed.Error(), refusal.GetError())
	select {
	case <-published:
		t.Fatal("doctrine-prohibited command reached the operator command channel")
	default:
	}
}

type boundaryInferenceBackend struct {
	mu           sync.Mutex
	calls        int
	lastReq      models.GenerateRequest
	started      chan struct{}
	release      chan struct{}
	progressText string
}

func (b *boundaryInferenceBackend) Generate(ctx context.Context, req models.GenerateRequest) (*models.GenerateResponse, error) {
	b.mu.Lock()
	b.calls++
	b.lastReq = req
	started := b.started
	release := b.release
	progressText := b.progressText
	b.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	parts := []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: "governed boundary response"}}}
	if req.Stream {
		progressParts := parts
		if progressText != "" {
			progressParts = []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: progressText}}}
		}
		if reporter := inference.ProgressReporterFromContext(ctx); reporter != nil {
			if err := reporter(&operatorv1.InferenceProgressEvent{
				ProviderAttemptId: req.ProviderAttemptID,
				Sequence:          1,
				Parts:             progressParts,
				ServedModel:       req.Model,
			}); err != nil {
				return nil, err
			}
		}
	}
	outputHash, err := models.ComputeInferenceOutputHash(parts, "stop")
	if err != nil {
		return nil, err
	}
	return &models.GenerateResponse{
		Parts:                 parts,
		PromptTokens:          4,
		CompletionTokens:      3,
		TotalTokens:           7,
		UsageReported:         true,
		FinishReason:          "stop",
		Model:                 req.Model,
		NormalizedRequestHash: models.SHA256Hex([]byte("boundary request")),
		OutputHash:            outputHash,
	}, nil
}

func (b *boundaryInferenceBackend) Status(_ context.Context) (*models.BackendStatus, error) {
	return &models.BackendStatus{Available: true, Models: []string{"primary-boundary", "assistant-boundary", "lite-boundary"}}, nil
}

func (b *boundaryInferenceBackend) snapshot() (int, models.GenerateRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls, b.lastReq
}

func boundaryInferenceMessages(text string) []*operatorv1.InferenceMessage {
	return []*operatorv1.InferenceMessage{{
		Role:  operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
		Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: text}}},
	}}
}

func boundaryInferenceText(t *testing.T, messages []*operatorv1.InferenceMessage) string {
	t.Helper()
	require.Len(t, messages, 1)
	require.Len(t, messages[0].GetParts(), 1)
	return messages[0].GetParts()[0].GetText()
}

func boundaryInferenceResultText(t *testing.T, result *operatorv1.InferenceResult) string {
	t.Helper()
	require.NotNil(t, result)
	require.Len(t, result.GetParts(), 1)
	return result.GetParts()[0].GetText()
}

func seedInferenceOperator(t *testing.T, infra *TestInfrastructure, userID, operatorID, sessionID string, capable bool) {
	t.Helper()
	var capabilities []string
	if capable {
		capabilities = []string{string(constants.OperatorCapabilityInference)}
	}
	now := time.Now().UTC()
	op := &operatorv1.OperatorDocument{
		Id:                operatorID,
		UserId:            userID,
		OrganizationId:    "org-inference-boundary",
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		OperatorSessionId: sessionID,
		RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: capabilities},
		CreatedAt:         timestamppb.New(now),
		UpdatedAt:         timestamppb.New(now),
	}
	body, err := models.MarshalOperatorDocument(op)
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), operatorID, body))
	require.NoError(t, infra.OperatorSessionSvc.PersistOperatorSession(t.Context(), sessionID, userID, op.OrganizationId, operatorID, "mTLS"))
}

// bindOperatorActuatorKey binds keyID as the Actuator key on the seeded
// OperatorDocument, as enrollment does (INV-GW-GOV-06).
func bindOperatorActuatorKey(t *testing.T, infra *TestInfrastructure, operatorID, keyID string) {
	t.Helper()
	collection := marshaler.CollectionName(constants.CollectionOperators)
	doc, err := infra.DocStore.DocGet(t.Context(), collection, operatorID)
	require.NoError(t, err)
	require.NotNil(t, doc, "seed the Operator before binding its Actuator key")
	op, err := models.OperatorDocumentFromStore(doc)
	require.NoError(t, err)
	op.ActuatorPublicKey = keyID
	op.ActuatorKeyId = keyID
	body, err := models.MarshalOperatorDocument(op)
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), collection, operatorID, body))
}

type boundaryTamperingResultsService struct {
	*pubsub.PubSubResultsService
}

func (s *boundaryTamperingResultsService) PublishInferenceCompletion(ctx context.Context, envelope *commonv1.GovernanceEnvelope, completion *operatorv1.InferenceCompletion) error {
	tampered := proto.Clone(completion).(*operatorv1.InferenceCompletion)
	if tampered.Result != nil && len(tampered.Result.GetParts()) > 0 {
		tampered.Result.GetParts()[0].Part = &operatorv1.InferenceResponsePart_Text{Text: "tampered"}
	}
	return s.PubSubResultsService.PublishInferenceCompletion(ctx, envelope, tampered)
}

func startInferenceOperator(t *testing.T, infra *TestInfrastructure, stateRoots govsvc.StateRootProvider, operatorID, sessionID string) (*boundaryInferenceBackend, string) {
	t.Helper()
	return startInferenceOperatorWithResultTampering(t, infra, stateRoots, operatorID, sessionID, false)
}

func startInferenceOperatorWithResultTampering(t *testing.T, infra *TestInfrastructure, stateRoots govsvc.StateRootProvider, operatorID, sessionID string, tamperResult bool) (*boundaryInferenceBackend, string) {
	t.Helper()
	remoteCfg := *infra.Cfg
	remoteCfg.OperatorID = operatorID
	remoteCfg.OperatorSessionId = sessionID
	remoteCfg.HeartbeatInterval = 0
	remoteCfg.Inference.Enabled = true
	remoteCfg.Inference.KeepAlive = "-1"

	backend := &boundaryInferenceBackend{}
	scrubbingSvc, err := scrubbing.NewScrubbingService(context.Background(), scrubbing.DefaultConfig(), infra.Logger, nil)
	require.NoError(t, err)
	inferenceHandler, err := inference.NewInferenceExecutionHandler(backend, &remoteCfg, scrubbingSvc, infra.Logger)
	require.NoError(t, err)

	pubKey, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := hex.EncodeToString(pubKey)
	bindOperatorActuatorKey(t, infra, operatorID, keyID)

	client := pubsub.NewInProcessPubSubClient(infra.Pubsub, infra.Logger)
	resultsSvc, err := pubsub.NewPubSubResultsService(&remoteCfg, infra.Logger, client, scrubbingSvc)
	require.NoError(t, err)
	var resultsPublisher pubsub.ResultsPublisher = resultsSvc
	if tamperResult {
		resultsPublisher = &boundaryTamperingResultsService{PubSubResultsService: resultsSvc}
	}
	cliVerifier := NewCLISessionVerifier(infra.DocStore, infra.PKI, infra.Logger, infra.UserSvc, infra.CLISessionSvc)
	operatorSvc, err := pubsub.NewOperatorPubSubService(withRequiredOperatorDeps(t, pubsub.CommandServiceConfig{
		Ledger:             storagetest.NewTestLedger(t),
		RuntimeVault:       governancetest.UnlockedVault{},
		Config:             &remoteCfg,
		Logger:             infra.Logger,
		PubSubClient:       client,
		ResultsService:     resultsPublisher,
		ActuatorSigningKey: privKey,
		ActuatorKeyID:      keyID,
		AuditorSigningKey:  privKey,
		AuditorKeyID:       keyID,
		Scrubbing:          scrubbingSvc,
		Inference:          inferenceHandler,
		AuditStore:         infra.AuditStore,
	}), pubsub.OutboundModeDeps{GovernanceCoreDeps: pubsub.GovernanceCoreDeps{
		ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{operatorID}, SessionID: sessionID},
		ReplayStore:       infra.ReplayStore,
		StateRootProvider: stateRoots,
		TransactionAudit:  infra.AuditStore,
		L3Notary:          govsvc.NewGatewayL3Notary(cliVerifier, infra.Passkey.PasskeyService, infra.Logger),
		SignerStore:       infra.SignerStore,
		Doctrine:          govsvc.NewL1Doctrine(),
	}})
	require.NoError(t, err)
	require.NoError(t, operatorSvc.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, operatorSvc.Stop()) })

	cmdChannel := pubsub.CmdChannel(operatorID, sessionID)
	require.Eventually(t, func() bool {
		infra.Pubsub.handlersMu.RLock()
		defer infra.Pubsub.handlersMu.RUnlock()
		return len(infra.Pubsub.handlers[cmdChannel]) == 1
	}, time.Second, 10*time.Millisecond)
	return backend, keyID
}

func startFileEditOperator(t *testing.T, infra *TestInfrastructure, operatorID, sessionID, workDir string) ed25519.PublicKey {
	t.Helper()
	remoteCfg := *infra.Cfg
	remoteCfg.OperatorID = operatorID
	remoteCfg.OperatorSessionId = sessionID
	remoteCfg.WorkDir = workDir
	remoteCfg.HeartbeatInterval = 0

	pubKey, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := hex.EncodeToString(pubKey)
	bindOperatorActuatorKey(t, infra, operatorID, keyID)

	client := pubsub.NewInProcessPubSubClient(infra.Pubsub, infra.Logger)
	scrubbingSvc, err := scrubbing.NewScrubbingService(context.Background(), scrubbing.DefaultConfig(), infra.Logger, nil)
	require.NoError(t, err)
	resultsSvc, err := pubsub.NewPubSubResultsService(&remoteCfg, infra.Logger, client, scrubbingSvc)
	require.NoError(t, err)
	fileSvc, err := fs.NewRuntimeFileService(workDir, infra.Logger)
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(t.Context()))
	fileEditSvc, err := execution.NewFileEditService(&remoteCfg, infra.Logger, fileSvc)
	require.NoError(t, err)
	operatorSvc, err := pubsub.NewOperatorPubSubService(withRequiredOperatorDeps(t, pubsub.CommandServiceConfig{
		Ledger:             storagetest.NewTestLedger(t),
		RuntimeVault:       governancetest.UnlockedVault{},
		Config:             &remoteCfg,
		Logger:             infra.Logger,
		FileEdit:           fileEditSvc,
		PubSubClient:       client,
		ResultsService:     resultsSvc,
		ActuatorSigningKey: privKey,
		ActuatorKeyID:      keyID,
		AuditorSigningKey:  privKey,
		AuditorKeyID:       keyID,
		Scrubbing:          scrubbingSvc,
		AuditStore:         infra.AuditStore,
	}), pubsub.OutboundModeDeps{GovernanceCoreDeps: pubsub.GovernanceCoreDeps{
		ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{operatorID}, SessionID: sessionID},
		ReplayStore:       infra.ReplayStore,
		StateRootProvider: infra.StateRootSvc,
		TransactionAudit:  infra.AuditStore,
		SignerStore:       infra.SignerStore,
		Doctrine:          govsvc.NewL1Doctrine(),
	}})
	require.NoError(t, err)
	require.NoError(t, operatorSvc.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, operatorSvc.Stop()) })

	cmdChannel := pubsub.CmdChannel(operatorID, sessionID)
	require.Eventually(t, func() bool {
		infra.Pubsub.handlersMu.RLock()
		defer infra.Pubsub.handlersMu.RUnlock()
		return len(infra.Pubsub.handlers[cmdChannel]) == 1
	}, time.Second, 10*time.Millisecond)
	return pubKey
}

func newInferenceBoundaryDispatchService(infra *TestInfrastructure, posture config.GatewayPosture) *inferdispatch.DispatchService {
	commandSvc := NewDispatchService(infra.Logger, infra.Pubsub, infra.StateRootSvc, infra.Auth, string(posture), govsvc.NewL1Doctrine(), nil, infra.SignerStore, nil)
	return inferdispatch.NewDispatchService(
		&gatewayDispatcherAdapter{svc: commandSvc},
		&gatewayOperatorListerAdapter{svc: infra.Reg},
		infra.Logger,
	)
}

func TestDispatch_FileMutationExecutesOnceAndReplayProducesSignedRejection(t *testing.T) {
	const (
		userID      = "user-evaluation-boundary"
		operatorID  = "operator-evaluation-boundary"
		sessionID   = "session-evaluation-boundary"
		runID       = "run-evaluation-boundary"
		scenarioID  = "scenario-allowed-execution"
		attemptID   = "attempt-allowed-execution"
		executionID = "execution-allowed-execution"
		seed        = "seed"
		marker      = "evaluation-run-marker"
	)

	infra := setupTestInfrastructure(t, false)
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, false)
	targetDir := testutil.OperatorDir(t)
	targetPath := filepath.Join(targetDir, constants.TestEvaluationTargetFilename)
	require.NoError(t, os.WriteFile(targetPath, []byte(seed), constants.PermFilePrivate))
	pubKey := startFileEditOperator(t, infra, operatorID, sessionID, targetDir)

	commandWire := make(chan []byte, 1)
	unregisterCommandCapture := infra.Pubsub.RegisterHandler(pubsub.CmdChannel(operatorID, sessionID), func(_ string, data []byte) {
		envelope := &commonv1.GovernanceEnvelope{}
		if err := protojson.Unmarshal(data, envelope); err == nil && envelope.ActionType == string(constants.ActionTypeFileEdit) {
			select {
			case commandWire <- append([]byte(nil), data...):
			default:
			}
		}
	})
	t.Cleanup(unregisterCommandCapture)

	receipts := make(chan *operatorv1.ActionReceipt, 8)
	unregisterReceiptCapture := infra.Pubsub.RegisterHandler(pubsub.ReceiptsChannel(operatorID, sessionID), func(_ string, data []byte) {
		envelope := &commonv1.GovernanceEnvelope{}
		if err := protojson.Unmarshal(data, envelope); err != nil {
			return
		}
		receipt := &operatorv1.ActionReceipt{}
		if err := proto.Unmarshal(envelope.Payload, receipt); err == nil {
			receipts <- receipt
		}
	})
	t.Cleanup(unregisterReceiptCapture)

	payload, err := proto.Marshal(&operatorv1.FileEditRequested{
		FilePath:    targetPath,
		Operation:   string(constants.FileOperationReplace),
		ExecutionId: executionID,
		OldContent:  seed,
		NewContent:  seed + "\n" + marker,
	})
	require.NoError(t, err)
	dispatchSvc := NewDispatchService(infra.Logger, infra.Pubsub, infra.StateRootSvc, infra.Auth, string(config.PostureDoctrine), govsvc.NewL1Doctrine(), nil, infra.SignerStore, nil)
	result, err := dispatchSvc.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: sessionID,
		EventType:               string(constants.Event.Operator.FileEdit.Requested),
		Payload:                 payload,
		TargetResource:          targetPath,
		RequestorUserID:         userID,
		CaseID:                  runID,
		InvestigationID:         scenarioID,
		TaskID:                  attemptID,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	fileResult := &operatorv1.FileEditResult{}
	require.NoError(t, proto.Unmarshal(result.ResultEnvelope.Payload, fileResult))
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, fileResult.Status)
	assert.Equal(t, executionID, fileResult.ExecutionId)

	body, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(body), marker))

	var wire []byte
	select {
	case wire = <-commandWire:
	case <-time.After(time.Second):
		t.Fatal("governed file mutation was not captured")
	}

	waitForReceipt := func(transactionID string) *operatorv1.ActionReceipt {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case receipt := <-receipts:
				if receipt.TransactionId == transactionID {
					return receipt
				}
			case <-timer.C:
				t.Fatalf("receipt for transaction %s was not captured", transactionID)
				return nil
			}
		}
	}

	completedReceipt := waitForReceipt(result.TransactionID)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, completedReceipt.Status)
	require.NoError(t, govsvc.VerifyActionReceiptSignature(completedReceipt, pubKey))
	require.NoError(t, govsvc.VerifyReceiptPersistenceAttestation(completedReceipt, pubKey))
	_, err = govsvc.ValidateDeterministicProtocolChain(completedReceipt)
	require.NoError(t, err)
	for _, stage := range completedReceipt.DeterministicStageEvidence {
		assert.Equal(t, operatorID, stage.OperatorId)
		assert.Equal(t, sessionID, stage.OperatorSessionId)
		assert.Equal(t, runID, stage.CaseId)
		assert.Equal(t, scenarioID, stage.InvestigationId)
		assert.Equal(t, attemptID, stage.TaskId)
		assert.Equal(t, string(constants.ActionTypeFileEdit), stage.ActionType)
	}

	persisted, err := infra.AuditStore.GetActionReceipt(result.TransactionID)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	require.NotNil(t, persisted.ActionReceipt)
	assert.Equal(t, operatorID, persisted.OperatorID)
	assert.Equal(t, sessionID, persisted.OperatorSessionID)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, persisted.ActionReceipt.Status)
	commitments, err := infra.AuditStore.CommitmentLedger().ListCommitments()
	require.NoError(t, err)
	matchingCommitments := 0
	for _, commitment := range commitments {
		if commitment.TransactionID == result.TransactionID {
			matchingCommitments++
			assert.Equal(t, string(constants.ActionTypeFileEdit), commitment.ActionType)
		}
	}
	assert.Equal(t, 1, matchingCommitments)

	deliveries := infra.Pubsub.Publish(pubsub.CmdChannel(operatorID, sessionID), wire)
	require.Positive(t, deliveries)
	rejectedReceipt := waitForReceipt(result.TransactionID)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, rejectedReceipt.Status)
	assert.Equal(t, operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, rejectedReceipt.FailureCode)
	require.NoError(t, govsvc.VerifyActionReceiptSignature(rejectedReceipt, pubKey))
	require.NoError(t, govsvc.VerifyReceiptPersistenceAttestation(rejectedReceipt, pubKey))

	body, err = os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(body), marker))
}

// TestDispatch_FileEditIntoProtectedRuntimeStateProducesSignedFailure covers
// the executing-runtime half of the FILE_EDIT runtime-state boundary: a path
// that passes Gateway L1 lexically but resolves into the Operator's runtime
// control/evidence tree must be refused by the Operator, recorded as a signed
// FAILED receipt, and leave the protected file untouched.
func TestDispatch_FileEditIntoProtectedRuntimeStateProducesSignedFailure(t *testing.T) {
	const (
		userID      = "user-protected-runtime"
		operatorID  = "operator-protected-runtime"
		sessionID   = "session-protected-runtime"
		executionID = "execution-protected-runtime"
		protected   = "protected-runtime-state"
	)

	infra := setupTestInfrastructure(t, false)
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, false)
	workDir := testutil.OperatorDir(t)
	pubKey := startFileEditOperator(t, infra, operatorID, sessionID, workDir)

	protectedPath := filepath.Join(workDir, constants.RuntimeDirname, constants.DataDirname, "protected.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(protectedPath), constants.PermDirStandard))
	require.NoError(t, os.WriteFile(protectedPath, []byte(protected), constants.PermFilePrivate))
	aliasPath := filepath.Join(workDir, "runtime-alias.json")
	testutil.Symlink(t, protectedPath, aliasPath)

	receipts := make(chan *operatorv1.ActionReceipt, 8)
	unregisterReceiptCapture := infra.Pubsub.RegisterHandler(pubsub.ReceiptsChannel(operatorID, sessionID), func(_ string, data []byte) {
		envelope := &commonv1.GovernanceEnvelope{}
		if err := protojson.Unmarshal(data, envelope); err != nil {
			return
		}
		receipt := &operatorv1.ActionReceipt{}
		if err := proto.Unmarshal(envelope.Payload, receipt); err == nil {
			receipts <- receipt
		}
	})
	t.Cleanup(unregisterReceiptCapture)

	payload, err := proto.Marshal(&operatorv1.FileEditRequested{
		FilePath:        aliasPath,
		Operation:       string(constants.FileOperationWrite),
		ExecutionId:     executionID,
		Content:         "overwritten",
		CreateIfMissing: true,
	})
	require.NoError(t, err)
	dispatchSvc := NewDispatchService(infra.Logger, infra.Pubsub, infra.StateRootSvc, infra.Auth, string(config.PostureDoctrine), govsvc.NewL1Doctrine(), nil, infra.SignerStore, nil)
	result, dispatchErr := dispatchSvc.Dispatch(context.Background(), DispatchRequest{
		TargetOperatorSessionID: sessionID,
		EventType:               string(constants.Event.Operator.FileEdit.Requested),
		Payload:                 payload,
		TargetResource:          aliasPath,
		RequestorUserID:         userID,
	})
	if dispatchErr == nil {
		require.NotNil(t, result)
		require.NotNil(t, result.ResultEnvelope)
		fileResult := &operatorv1.FileEditResult{}
		require.NoError(t, proto.Unmarshal(result.ResultEnvelope.Payload, fileResult))
		assert.NotEqual(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, fileResult.Status)
	}

	var failed *operatorv1.ActionReceipt
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for failed == nil {
		select {
		case receipt := <-receipts:
			require.NotEqual(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.Status, "protected runtime edit must not complete")
			if receipt.Status == operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED {
				failed = receipt
			}
		case <-timer.C:
			t.Fatal("signed FAILED receipt was not published")
		}
	}
	require.NoError(t, govsvc.VerifyActionReceiptSignature(failed, pubKey))
	require.NoError(t, govsvc.VerifyReceiptPersistenceAttestation(failed, pubKey))

	body, err := os.ReadFile(protectedPath)
	require.NoError(t, err)
	assert.Equal(t, protected, string(body))
}

// TestDispatch_EmbeddedFileEditRecordsLocalEvidence covers the Gateway's own
// embedded Operator. A FILE_EDIT create dispatched in-process must complete
// and record its file mutation in the Gateway audit store. That requires the
// embedded command service to be wired to the Gateway's audit store and
// git file ledger, which the Gateway owns in its own runtime root.
func TestDispatch_EmbeddedFileEditRecordsLocalEvidence(t *testing.T) {
	const (
		targetName  = "embedded-created.txt"
		executionID = "execution-embedded-file-create"
		content     = "created by the embedded operator"
	)
	ctx := t.Context()
	logger := testutil.NewTestLogger()
	fileSvc := newTestFileSvc(t)
	workDir := testutil.OperatorDir(t)

	cfg := testutil.NewTestConfig(t)
	cfg.Gateway.DataDir = testutil.OperatorDir(t)
	cfg.Gateway.PKIDir = testutil.OperatorDir(t)
	cfg.Gateway.SecretsDir = fileSvc.Resolve(constants.SecretsDirname)
	cfg.Gateway.VaultDir = fileSvc.Resolve(constants.VaultDirname)
	cfg.Gateway.Posture = config.PostureDoctrine
	cfg.OperatorCapabilities = constants.OperatorCapabilities{constants.OperatorCapabilityData}
	cfg.WorkDir = workDir
	// LoadGateway sets no runtime Operator session; the embedded Operator serves
	// whichever session claims it, so evidence must key off the command session.
	cfg.OperatorSessionId = ""
	db, err := openTestDB(t, fileSvc, logger)
	require.NoError(t, err)
	svc, err := NewGatewayModeServiceWithDB(cfg, fileSvc, logger, db, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Stop(context.Background()) })

	_, sessionID, err := svc.embeddedOperator.ClaimEmbeddedOperator(ctx, "owner")
	require.NoError(t, err)

	targetPath := filepath.Join(workDir, targetName)
	payload, err := proto.Marshal(&operatorv1.FileEditRequested{
		FilePath:        targetPath,
		Operation:       string(constants.FileOperationWrite),
		ExecutionId:     executionID,
		Content:         content,
		CreateIfMissing: true,
	})
	require.NoError(t, err)
	dispatchSvc := NewDispatchService(logger, svc.pubsub, svc.stateRootSvc, svc.auth, string(config.PostureDoctrine), svc.doctrine, nil, svc.signerStore, svc.envProc)
	result, err := dispatchSvc.Dispatch(ctx, DispatchRequest{
		TargetOperatorSessionID: sessionID,
		EventType:               string(constants.Event.Operator.FileEdit.Requested),
		Payload:                 payload,
		TargetResource:          targetPath,
		RequestorUserID:         "owner",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	fileResult := &operatorv1.FileEditResult{}
	require.NoError(t, proto.Unmarshal(result.ResultEnvelope.Payload, fileResult))
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, fileResult.Status)

	var mutations []*storage.FileMutationLog
	require.Eventually(t, func() bool {
		var listErr error
		mutations, listErr = svc.auditStore.ListFileMutations(10, 0)
		return listErr == nil && len(mutations) > 0
	}, 5*time.Second, 20*time.Millisecond, "embedded FILE_EDIT must record a file_mutations row in the Gateway audit store")
	assert.Contains(t, mutations[0].Filepath, targetName)

	events, err := svc.auditStore.ListEvents(sessionID, 50, 0)
	require.NoError(t, err)
	var fileEditEvent *storage.Event
	for _, event := range events {
		if event.ID == mutations[0].EventID {
			fileEditEvent = event
		}
	}
	require.NotNil(t, fileEditEvent, "file mutation event must be recorded under the claimed embedded session")
	assert.Equal(t, constants.Event.Operator.FileEdit.Completed, fileEditEvent.Type)
}

func TestDispatchController_HandleDispatch_DoctrineProhibitedRequestRejectedBeforeRemoteExecution(t *testing.T) {
	const (
		runID      = "run-prohibited-boundary"
		scenarioID = "scenario-prohibited-boundary"
		attemptID  = "attempt-prohibited-boundary"
	)

	h, _, infra := setupTestHTTPHandler(t)
	h.dispatchController = newDispatchController(DispatchControllerDeps{
		DispatchSvc: NewDispatchService(infra.Logger, infra.Pubsub, infra.StateRootSvc, infra.Auth, string(config.PostureDoctrine), govsvc.NewL1Doctrine(), nil, infra.SignerStore, nil),
		Responder:   infra.Responder,
		Logger:      infra.Logger,
	})
	h.router = h.buildPublicRouter()
	operatorID, operatorSessionID, requestorUserID := seedOperatorForDispatch(t, infra)
	cliSessionID, cliCert := seedCLISessionForDispatch(t, infra, requestorUserID)
	published := make(chan struct{}, 1)
	unregister := infra.Pubsub.RegisterHandler(pubsub.CmdChannel(operatorID, operatorSessionID), func(_ string, _ []byte) {
		published <- struct{}{}
	})
	t.Cleanup(unregister)

	targetPath := filepath.Join(testutil.OperatorDir(t), constants.TestEvaluationTargetFilename)
	payload, err := proto.Marshal(&operatorv1.FileEditRequested{
		FilePath:    targetPath,
		Operation:   string(constants.FileOperationWrite),
		ExecutionId: attemptID,
		Content:     "rm -rf /",
	})
	require.NoError(t, err)
	body, err := json.Marshal(OperatorCommandRequest{
		TargetOperatorSessionID: operatorSessionID,
		EventType:               string(constants.Event.Operator.FileEdit.Requested),
		Payload:                 payload,
		TargetResource:          targetPath,
		CaseID:                  runID,
		InvestigationID:         scenarioID,
		TaskID:                  attemptID,
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.OperatorsCommands, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cliCert}}
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), constants.ErrTxL1ValidationFailed.Error())
	select {
	case <-published:
		t.Fatal("doctrine-prohibited request reached the remote execution channel")
	default:
	}
	_, err = os.Stat(targetPath)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestInferenceDispatch_RealBrokerAndOutboundOperator_VerifiesReceiptAuditAndCommitment(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	userID := "user-inference-boundary"
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, "operator-inference-boundary", "session-inference-boundary", true)
	backend, _ := startInferenceOperator(t, infra, infra.StateRootSvc, "operator-inference-boundary", "session-inference-boundary")

	dispatchSvc := newInferenceBoundaryDispatchService(infra, config.PostureDoctrine)
	result, err := dispatchSvc.DispatchInference(context.Background(), inferdispatch.DispatchInferenceRequest{
		RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
		ProviderAttemptID:       "provider-attempt-integration",
		Role:                    models.InferenceModelRolePrimary,
		Model:                   "primary-boundary",
		Messages:                boundaryInferenceMessages("boundary prompt"),
		TargetOperatorSessionID: "session-inference-boundary",
		RequestorUserID:         userID,
		ActingAppID:             testAppID,
		CaseID:                  "case-inference-boundary",
		InvestigationID:         "investigation-inference-boundary",
		TaskID:                  "task-inference-boundary",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Result)
	require.NotNil(t, result.Receipt)
	assert.Equal(t, "governed boundary response", boundaryInferenceResultText(t, result.Result))
	assert.Equal(t, result.TransactionID, result.Receipt.TransactionId)
	assert.Equal(t, result.Result.ResultDigest, result.Receipt.ResultSummary)
	assert.NotEmpty(t, result.Receipt.Signature)
	assert.NotNil(t, result.Receipt.FinalPersistenceAttestation)

	calls, request := backend.snapshot()
	assert.Equal(t, 1, calls)
	assert.Equal(t, "primary-boundary", request.Model)
	assert.Equal(t, "boundary prompt", boundaryInferenceText(t, request.Messages))

	receipt, err := infra.AuditStore.GetActionReceipt(result.TransactionID)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, constants.ActionTypeInference, receipt.ActionType)
	assert.Equal(t, result.Receipt.ResultSummary, receipt.ResultSummary)

	commitments, err := infra.AuditStore.CommitmentLedger().ListCommitments()
	require.NoError(t, err)
	var inferenceCommitmentAction string
	for _, commitment := range commitments {
		if commitment.TransactionID == result.TransactionID {
			inferenceCommitmentAction = commitment.ActionType
			break
		}
	}
	require.NotEmpty(t, inferenceCommitmentAction)
	assert.Equal(t, string(constants.ActionTypeInference), inferenceCommitmentAction)
}

func TestInferenceDispatch_HTTPRouterAppMTLSIdentityTraversesRealBrokerAndOutboundOperator(t *testing.T) {
	h, cfg, infra := setupTestHTTPHandler(t)
	userID := "user-inference-http"
	operatorID := "operator-inference-http"
	sessionID := "session-inference-http"
	seedActiveUser(t, infra, userID)
	seedAppPolicy(t, infra)
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, true)
	backend, _ := startInferenceOperator(t, infra, infra.StateRootSvc, operatorID, sessionID)
	h.inferenceDispatchController = newInferenceDispatchController(InferenceDispatchControllerDeps{
		DispatchSvc: newInferenceBoundaryDispatchService(infra, config.PostureDoctrine),
		Responder:   infra.Responder,
		Logger:      infra.Logger,
		MaxPayload:  cfg.Gateway.MaxPayloadBytes,
	})
	h.router = h.buildPublicRouter()

	body, err := protojson.Marshal(&operatorv1.InferenceDispatchRequest{
		RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
		Role:                    operatorv1.ModelRole_MODEL_ROLE_ASSISTANT,
		Model:                   "assistant-boundary",
		Messages:                boundaryInferenceMessages("HTTP mTLS boundary"),
		ProviderAttemptId:       "provider-attempt-http",
		TargetOperatorSessionId: sessionID,
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.InferenceDispatch, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{appUserMTLSCert(t, userID)}}
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "response body: %s", rr.Body.String())
	response := &operatorv1.InferenceDispatchResponse{}
	require.NoError(t, protojson.Unmarshal(rr.Body.Bytes(), response))
	assert.Equal(t, "governed boundary response", boundaryInferenceResultText(t, response.Result))
	assert.Equal(t, response.Result.ResultDigest, response.Receipt.ResultSummary)
	calls, backendRequest := backend.snapshot()
	assert.Equal(t, 1, calls)
	assert.Equal(t, "assistant-boundary", backendRequest.Model)
}

func TestInferenceDispatch_RealBrokerAndOutboundOperator_RoutesAllModelRoles(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	userID := "user-inference-roles"
	operatorID := "operator-inference-roles"
	sessionID := "session-inference-roles"
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, true)
	backend, _ := startInferenceOperator(t, infra, infra.StateRootSvc, operatorID, sessionID)
	dispatchSvc := newInferenceBoundaryDispatchService(infra, config.PostureDoctrine)

	tests := []struct {
		name      string
		role      models.InferenceModelRole
		wantModel string
	}{
		{name: "primary role", role: models.InferenceModelRolePrimary, wantModel: "primary-boundary"},
		{name: "assistant role", role: models.InferenceModelRoleAssistant, wantModel: "assistant-boundary"},
		{name: "lite role", role: models.InferenceModelRoleLite, wantModel: "lite-boundary"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := dispatchSvc.DispatchInference(context.Background(), inferdispatch.DispatchInferenceRequest{
				RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
				ProviderAttemptID:       "provider-attempt-integration",
				Role:                    test.role,
				Model:                   test.wantModel,
				Messages:                boundaryInferenceMessages(test.name),
				TargetOperatorSessionID: sessionID,
				RequestorUserID:         userID,
				ActingAppID:             testAppID,
			})
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, test.wantModel, result.Result.Model)
			calls, request := backend.snapshot()
			assert.Equal(t, index+1, calls)
			assert.Equal(t, test.wantModel, request.Model)
			assert.Equal(t, test.name, boundaryInferenceText(t, request.Messages))
		})
	}
}

func TestInferenceDispatch_TwoActiveSessions_ExplicitTargetReceivesOnlySelectedRequest(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	userID := "user-inference-selection"
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, "operator-inference-a", "session-inference-a", true)
	seedInferenceOperator(t, infra, userID, "operator-inference-b", "session-inference-b", true)
	backendA, _ := startInferenceOperator(t, infra, infra.StateRootSvc, "operator-inference-a", "session-inference-a")
	backendB, _ := startInferenceOperator(t, infra, infra.StateRootSvc, "operator-inference-b", "session-inference-b")
	dispatchSvc := newInferenceBoundaryDispatchService(infra, config.PostureDoctrine)

	result, err := dispatchSvc.DispatchInference(context.Background(), inferdispatch.DispatchInferenceRequest{
		RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
		ProviderAttemptID:       "provider-attempt-integration",
		Role:                    models.InferenceModelRolePrimary,
		Model:                   "primary-boundary",
		Messages:                boundaryInferenceMessages("selected operator only"),
		TargetOperatorSessionID: "session-inference-b",
		RequestorUserID:         userID,
		ActingAppID:             testAppID,
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	callsA, _ := backendA.snapshot()
	callsB, requestB := backendB.snapshot()
	assert.Zero(t, callsA)
	assert.Equal(t, 1, callsB)
	assert.Equal(t, "selected operator only", boundaryInferenceText(t, requestB.Messages))
}

func TestInferenceDispatch_RealStoresRejectInvalidSessionSelectionBeforeBackendInvocation(t *testing.T) {
	tests := []struct {
		name    string
		seed    func(*testing.T, *TestInfrastructure, string)
		target  string
		wantErr error
	}{
		{
			name: "ambiguous capable sessions without target",
			seed: func(t *testing.T, infra *TestInfrastructure, userID string) {
				seedInferenceOperator(t, infra, userID, "operator-ambiguous-a", "session-ambiguous-a", true)
				seedInferenceOperator(t, infra, userID, "operator-ambiguous-b", "session-ambiguous-b", true)
			},
			wantErr: constants.ErrInferenceOperatorAmbiguous,
		},
		{
			name: "explicit incapable session",
			seed: func(t *testing.T, infra *TestInfrastructure, userID string) {
				seedInferenceOperator(t, infra, userID, "operator-incapable", "session-incapable", false)
			},
			target:  "session-incapable",
			wantErr: constants.ErrInferenceOperatorNotCapable,
		},
		{
			name: "cross-user target is nondisclosed",
			seed: func(t *testing.T, infra *TestInfrastructure, _ string) {
				otherUserID := "user-inference-other"
				seedActiveUser(t, infra, otherUserID)
				seedInferenceOperator(t, infra, otherUserID, "operator-other-user", "session-other-user", true)
			},
			target:  "session-other-user",
			wantErr: constants.ErrInferenceOperatorNotFound,
		},
		{
			name: "capable session with zero delivery",
			seed: func(t *testing.T, infra *TestInfrastructure, userID string) {
				seedInferenceOperator(t, infra, userID, "operator-no-delivery", "session-no-delivery", true)
			},
			target:  "session-no-delivery",
			wantErr: constants.ErrDispatchNoDelivery,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			infra := setupTestInfrastructure(t, false)
			userID := "user-inference-routing"
			seedActiveUser(t, infra, userID)
			test.seed(t, infra, userID)
			dispatchSvc := newInferenceBoundaryDispatchService(infra, config.PostureDoctrine)
			_, err := dispatchSvc.DispatchInference(context.Background(), inferdispatch.DispatchInferenceRequest{
				RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
				ProviderAttemptID:       "provider-attempt-integration",
				Role:                    models.InferenceModelRolePrimary,
				Model:                   "primary-boundary",
				Messages:                boundaryInferenceMessages("must not reach backend"),
				TargetOperatorSessionID: test.target,
				RequestorUserID:         userID,
				ActingAppID:             testAppID,
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, test.wantErr)
		})
	}
}

func TestInferenceDispatch_CallerCancellationRemovesHandlerWhileRemoteExecutionCompletes(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	userID := "user-inference-cancel"
	operatorID := "operator-inference-cancel"
	sessionID := "session-inference-cancel"
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, true)
	backend, _ := startInferenceOperator(t, infra, infra.StateRootSvc, operatorID, sessionID)
	backend.mu.Lock()
	backend.started = make(chan struct{})
	backend.release = make(chan struct{})
	started := backend.started
	release := backend.release
	backend.mu.Unlock()
	dispatchSvc := newInferenceBoundaryDispatchService(infra, config.PostureDoctrine)

	transactionIDs := make(chan string, 1)
	unregisterCommandCapture := infra.Pubsub.RegisterHandler(pubsub.CmdChannel(operatorID, sessionID), func(_ string, data []byte) {
		envelope := &commonv1.GovernanceEnvelope{}
		if err := protojson.Unmarshal(data, envelope); err == nil {
			select {
			case transactionIDs <- envelope.Id:
			default:
			}
		}
	})
	t.Cleanup(unregisterCommandCapture)

	ctx, cancel := context.WithCancel(context.Background())
	dispatchErr := make(chan error, 1)
	go func() {
		_, err := dispatchSvc.DispatchInference(ctx, inferdispatch.DispatchInferenceRequest{
			RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
			ProviderAttemptID:       "provider-attempt-integration",
			Role:                    models.InferenceModelRolePrimary,
			Model:                   "primary-boundary",
			Messages:                boundaryInferenceMessages("complete after caller cancellation"),
			TargetOperatorSessionID: sessionID,
			RequestorUserID:         userID,
			ActingAppID:             testAppID,
		})
		dispatchErr <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("backend invocation did not start")
	}
	var transactionID string
	select {
	case transactionID = <-transactionIDs:
	case <-time.After(time.Second):
		t.Fatal("governed transaction ID was not captured")
	}
	cancel()
	select {
	case err := <-dispatchErr:
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrInferenceCanceled)
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("dispatch did not return after caller cancellation")
	}
	assert.Zero(t, resultHandlerCount(infra.Pubsub, &operatorv1.OperatorDocument{Id: operatorID, OperatorSessionId: sessionID}))

	close(release)
	require.Eventually(t, func() bool {
		receipt, err := infra.AuditStore.GetActionReceipt(transactionID)
		return err == nil && receipt != nil && receipt.Status == operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	}, time.Second, 10*time.Millisecond)
	assert.Zero(t, resultHandlerCount(infra.Pubsub, &operatorv1.OperatorDocument{Id: operatorID, OperatorSessionId: sessionID}))
	calls, _ := backend.snapshot()
	assert.Equal(t, 1, calls)
}

func TestInferenceDispatch_StreamingProgressReconcilesToTerminalOutputHash(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	userID := "user-inference-progress"
	operatorID := "operator-inference-progress"
	sessionID := "session-inference-progress"
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, true)
	startInferenceOperator(t, infra, infra.StateRootSvc, operatorID, sessionID)
	dispatchSvc := newInferenceBoundaryDispatchService(infra, config.PostureDoctrine)

	var progressEvents []*operatorv1.InferenceProgressEvent
	result, err := dispatchSvc.DispatchInference(context.Background(), inferdispatch.DispatchInferenceRequest{
		RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
		ProviderAttemptID:       "provider-attempt-integration",
		Role:                    models.InferenceModelRolePrimary,
		Model:                   "primary-boundary",
		Messages:                boundaryInferenceMessages("streaming progress reconciliation"),
		TargetOperatorSessionID: sessionID,
		RequestorUserID:         userID,
		ActingAppID:             testAppID,
		Stream:                  true,
		OnProgress: func(event *operatorv1.InferenceProgressEvent) error {
			progressEvents = append(progressEvents, proto.Clone(event).(*operatorv1.InferenceProgressEvent))
			return nil
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Result)
	require.Len(t, progressEvents, 1)
	assert.Equal(t, uint32(1), progressEvents[0].GetSequence())
	assert.NoError(t, models.ReconcileInferenceProgress(progressEvents, result.Result))
}

func TestInferenceDispatch_StreamingProgressHashMismatchFailsClosed(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	userID := "user-inference-progress-mismatch"
	operatorID := "operator-inference-progress-mismatch"
	sessionID := "session-inference-progress-mismatch"
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, true)
	backend, _ := startInferenceOperator(t, infra, infra.StateRootSvc, operatorID, sessionID)
	backend.mu.Lock()
	backend.progressText = "progress that does not match terminal output"
	backend.mu.Unlock()
	dispatchSvc := newInferenceBoundaryDispatchService(infra, config.PostureDoctrine)

	_, err := dispatchSvc.DispatchInference(context.Background(), inferdispatch.DispatchInferenceRequest{
		RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
		ProviderAttemptID:       "provider-attempt-integration",
		Role:                    models.InferenceModelRolePrimary,
		Model:                   "primary-boundary",
		Messages:                boundaryInferenceMessages("streaming progress mismatch"),
		TargetOperatorSessionID: sessionID,
		RequestorUserID:         userID,
		ActingAppID:             testAppID,
		Stream:                  true,
		OnProgress:              func(*operatorv1.InferenceProgressEvent) error { return nil },
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInferenceProgressHashMismatch)
}

func TestInferenceDispatch_ResultMutatedAfterDigestComputationFailsClosed(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	userID := "user-inference-digest"
	operatorID := "operator-inference-digest"
	sessionID := "session-inference-digest"
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, true)
	backend, _ := startInferenceOperatorWithResultTampering(t, infra, infra.StateRootSvc, operatorID, sessionID, true)
	dispatchSvc := newInferenceBoundaryDispatchService(infra, config.PostureDoctrine)

	_, err := dispatchSvc.DispatchInference(context.Background(), inferdispatch.DispatchInferenceRequest{
		RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
		ProviderAttemptID:       "provider-attempt-integration",
		Role:                    models.InferenceModelRolePrimary,
		Model:                   "primary-boundary",
		Messages:                boundaryInferenceMessages("digest substitution boundary"),
		TargetOperatorSessionID: sessionID,
		RequestorUserID:         userID,
		ActingAppID:             testAppID,
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInferenceResultDigestMismatch)
	calls, _ := backend.snapshot()
	assert.Equal(t, 1, calls)
}

func TestInferenceDispatch_ReplayedEnvelopeIsRejectedWithoutSecondBackendInvocation(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	userID := "user-inference-replay"
	operatorID := "operator-inference-replay"
	sessionID := "session-inference-replay"
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, true)
	backend, _ := startInferenceOperator(t, infra, infra.StateRootSvc, operatorID, sessionID)
	dispatchSvc := newInferenceBoundaryDispatchService(infra, config.PostureDoctrine)

	commandWire := make(chan []byte, 1)
	unregisterCommandCapture := infra.Pubsub.RegisterHandler(pubsub.CmdChannel(operatorID, sessionID), func(_ string, data []byte) {
		select {
		case commandWire <- append([]byte(nil), data...):
		default:
		}
	})
	t.Cleanup(unregisterCommandCapture)
	completions := make(chan *operatorv1.InferenceCompletion, 2)
	unregisterCompletionCapture := infra.Pubsub.RegisterHandler(pubsub.ResultsChannel(operatorID, sessionID), func(_ string, data []byte) {
		envelope := &commonv1.GovernanceEnvelope{}
		if err := protojson.Unmarshal(data, envelope); err != nil {
			return
		}
		completion := &operatorv1.InferenceCompletion{}
		if err := proto.Unmarshal(envelope.Payload, completion); err != nil {
			return
		}
		completions <- completion
	})
	t.Cleanup(unregisterCompletionCapture)

	result, err := dispatchSvc.DispatchInference(context.Background(), inferdispatch.DispatchInferenceRequest{
		RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
		ProviderAttemptID:       "provider-attempt-integration",
		Role:                    models.InferenceModelRolePrimary,
		Model:                   "primary-boundary",
		Messages:                boundaryInferenceMessages("invoke exactly once"),
		TargetOperatorSessionID: sessionID,
		RequestorUserID:         userID,
		ActingAppID:             testAppID,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	var wire []byte
	select {
	case wire = <-commandWire:
	case <-time.After(time.Second):
		t.Fatal("governed command was not captured")
	}
	select {
	case first := <-completions:
		require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, first.Receipt.Status)
	case <-time.After(time.Second):
		t.Fatal("successful completion was not captured")
	}

	deliveries := infra.Pubsub.Publish(pubsub.CmdChannel(operatorID, sessionID), wire)
	require.Positive(t, deliveries)
	select {
	case replay := <-completions:
		require.NotNil(t, replay.Receipt)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, replay.Receipt.Status)
		assert.Equal(t, operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, replay.Receipt.FailureCode)
		assert.NotEmpty(t, replay.Receipt.Signature)
	case <-time.After(time.Second):
		t.Fatal("signed replay rejection was not captured")
	}
	calls, _ := backend.snapshot()
	assert.Equal(t, 1, calls)
}

func TestInferenceDispatch_NotaryPostureRejectsMutationBeforeBackendInvocation(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	userID := "user-inference-notary"
	operatorID := "operator-inference-notary"
	sessionID := "session-inference-notary"
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, true)
	backend, _ := startInferenceOperator(t, infra, infra.StateRootSvc, operatorID, sessionID)
	dispatchSvc := newInferenceBoundaryDispatchService(infra, config.PostureNotary)

	_, err := dispatchSvc.DispatchInference(context.Background(), inferdispatch.DispatchInferenceRequest{
		RequestSchemaVersion:    constants.InferenceRequestSchemaVersion,
		ProviderAttemptID:       "provider-attempt-integration",
		Role:                    models.InferenceModelRolePrimary,
		Model:                   "primary-boundary",
		Messages:                boundaryInferenceMessages("notary must reject"),
		TargetOperatorSessionID: sessionID,
		RequestorUserID:         userID,
		ActingAppID:             testAppID,
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrTxL3ProofUnmintable)
	calls, _ := backend.snapshot()
	assert.Zero(t, calls)
}

// withRequiredOperatorDeps fills the collaborators every Operator pub/sub service requires and the
// test does not exercise: the execution and file edit services over cfg.WorkDir, and a history
// handler over the supplied audit store and ledger. A field the test sets is kept.
func withRequiredOperatorDeps(t *testing.T, c pubsub.CommandServiceConfig) pubsub.CommandServiceConfig {
	t.Helper()
	if c.Execution == nil {
		c.Execution = execution.NewExecutionService(c.Config, c.Logger)
	}
	if c.FileEdit == nil {
		fileSvc, err := fs.NewRuntimeFileService(c.Config.WorkDir, c.Logger)
		require.NoError(t, err)
		require.NoError(t, fileSvc.CreateRuntimeTree(t.Context()))
		c.FileEdit, err = execution.NewFileEditService(c.Config, c.Logger, fileSvc)
		require.NoError(t, err)
	}
	if c.HistoryHandler == nil {
		var err error
		c.HistoryHandler, err = storage.NewHistoryHandler(c.AuditStore, c.Ledger, c.Logger, nil, "")
		require.NoError(t, err)
	}
	return c
}
