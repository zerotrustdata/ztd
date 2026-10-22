// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// unprovenAppID is the app workload that submits the unproven mutation. Its
// mTLS certificate and app policy are seeded by seedUnprovenAppWorkload so the
// transport admits the envelope and the rejection comes from L4, not from
// authentication.
const unprovenAppID = "test-app"

// unprovenAppSPIFFEID is the app SPIFFE identity that the seeded certificate carries.
const unprovenAppSPIFFEID = "spiffe://ztd.local/app/" + unprovenAppID

// seedUnprovenAppWorkload registers an app policy for unprovenAppID and returns
// an mTLS client presenting an app certificate signed by the fixture PKI.
func seedUnprovenAppWorkload(t *testing.T, f *fixtures.GatewayFixture) *http.Client {
	t.Helper()
	ctx := t.Context()

	policy, err := json.Marshal(&models.AppPolicy{AppID: unprovenAppSPIFFEID, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	require.NoError(t, err)
	require.NoError(t, f.Service.GetDocStore().DocSet(ctx, string(constants.CollectionAppPolicies), unprovenAppSPIFFEID, policy))

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: unprovenAppID}}, key)
	require.NoError(t, err)
	certPEM, _, err := f.Service.GetPKI().SignCSR(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})), constants.LeafTypeApp, "", unprovenAppID, "", "", "")
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair([]byte(certPEM), keyPEM)
	require.NoError(t, err)

	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(testutil.ReadRootCA(t, f.PKIDir)))
	require.True(t, roots.AppendCertsFromPEM(testutil.ReadOperatorCA(t, f.PKIDir)))
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

// unprovenDocumentUpdateEnvelope builds a well-formed, hash-consistent
// DOCUMENT_UPDATE envelope that carries no L2 votes and no L3 proof. It is
// otherwise valid: current state root, protocol v2, unexpired, app-bound.
func unprovenDocumentUpdateEnvelope(t *testing.T, f *fixtures.GatewayFixture, documentID, nonce string) *commonv1.GovernanceEnvelope {
	t.Helper()
	ctx := t.Context()

	updates := &structpb.Struct{Fields: map[string]*structpb.Value{"case_title": structpb.NewStringValue("unproven")}}
	payload, err := proto.Marshal(&operatorv1.DocumentUpdateRequested{
		Collection: string(constants.CollectionInvestigations),
		DocumentId: documentID,
		Updates:    updates,
	})
	require.NoError(t, err)

	root, err := f.Service.GetStateRootSvc().GetCurrentStateRoot(ctx)
	require.NoError(t, err)

	env := &commonv1.GovernanceEnvelope{
		ProtocolVersion: govtypes.GovernanceProtocolVersionV2,
		Timestamp:       timestamppb.Now(),
		ExpiresAt:       timestamppb.New(time.Now().Add(time.Minute)),
		SourceComponent: commonv1.Component_COMPONENT_AGENT,
		ActingAppId:     unprovenAppID,
		EventType:       string(constants.EventAppDocumentUpdateRequested),
		ActionType:      string(constants.ActionTypeDocumentUpdate),
		TargetResource:  string(constants.CollectionInvestigations) + "/" + documentID,
		Payload:         payload,
		StateMerkleRoot: root,
		Nonce:           nonce,
	}
	env.Id, err = govtypes.GenerateMessageID(env)
	require.NoError(t, err)
	env.TransactionHash = env.Id
	require.Nil(t, env.Governance, "fixture envelope must carry no L2/L3 proof")
	return env
}

// assertUnprovenEnvelopeRejectedBeforeExecution submits an envelope lacking a
// required proof through POST /api/v1/governance/envelopes and asserts that the
// real Gateway rejects it at L4 with the expected sentinel. The 403 also shows
// the Gateway did not mint the missing proof.
func assertUnprovenEnvelopeRejectedBeforeExecution(t *testing.T, f *fixtures.GatewayFixture, client *http.Client, sentinel error) {
	t.Helper()
	const documentID = "l4-unproven-document"
	env := unprovenDocumentUpdateEnvelope(t, f, documentID, "l4-unproven-http-nonce")
	assertEnvelopeRejectedAtIngress(t, f, client, env, documentID, sentinel)
}

// assertEnvelopeRejectedAtIngress posts a prepared envelope through the direct
// envelope endpoint and asserts HTTP 403 carrying the sentinel, no EXECUTING or
// COMPLETED event receipt, and no written target document.
func assertEnvelopeRejectedAtIngress(t *testing.T, f *fixtures.GatewayFixture, client *http.Client, env *commonv1.GovernanceEnvelope, documentID string, sentinel error) {
	t.Helper()
	ctx := t.Context()

	wire, err := protojson.Marshal(env)
	require.NoError(t, err)
	url := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort()) + constants.APIPaths.GovernanceEnvelopes
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(wire))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)

	require.Equal(t, http.StatusForbidden, resp.StatusCode, "rejected envelope must map to HTTP 403: %s", body)
	var errBody struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &errBody))
	require.Contains(t, errBody.Error, sentinel.Error(), "HTTP rejection must carry the sentinel")

	persisted, err := f.Service.GetAuditStore().GetEventReceipt(env.Id)
	require.NoError(t, err)
	if persisted != nil {
		require.NotEqual(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING, persisted.EventReceipt.GetStatus(), "L5 must not start execution for rejected transaction")
		require.NotEqual(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, persisted.EventReceipt.GetStatus(), "L5 must not complete execution for rejected transaction")
	}
	doc, err := f.Service.GetDocStore().DocGet(ctx, string(constants.CollectionInvestigations), documentID)
	require.NoError(t, err)
	require.Nil(t, doc, "rejected mutation must not write the target document")
}

// TestGovernanceEnvelopeIntegration_ConsensusPostureRejectsMissingL2AtWarden
// runs the real Gateway under consensus posture, where L2 quorum gates
// execution. A DOCUMENT_UPDATE with no L2 votes must fail closed at L4 with
// ErrTxL2SignatureMissing (HTTP 403) and never reach L5.
func TestGovernanceEnvelopeIntegration_ConsensusPostureRejectsMissingL2AtWarden(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "governance-envelope-consensus-missing-l2",
		Posture:  constants.PostureConsensus,
	})
	f.WaitForReady(t)
	client := seedUnprovenAppWorkload(t, f)

	assertUnprovenEnvelopeRejectedBeforeExecution(t, f, client, constants.ErrTxL2SignatureMissing)
}

// TestGovernanceEnvelopeIntegration_RatifyPostureRejectsMissingL3ForMutationAtWarden
// runs the real Gateway under ratify posture, where L3 notary proof gates
// mutations (L2 is audit-only). A DOCUMENT_UPDATE with no L3 proof must fail
// closed at L4 with ErrTxL3ProofMissing (HTTP 403), the gateway must not mint a
// proof or suspend it for approval on direct submission, and L5 must not run.
func TestGovernanceEnvelopeIntegration_RatifyPostureRejectsMissingL3ForMutationAtWarden(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "governance-envelope-ratify-missing-l3",
		Posture:  constants.PostureRatify,
	})
	f.WaitForReady(t)
	client := seedUnprovenAppWorkload(t, f)

	assertUnprovenEnvelopeRejectedBeforeExecution(t, f, client, constants.ErrTxL3ProofMissing)
}
