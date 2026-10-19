// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	approvals "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	govsvc "github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/governance/consensustest"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestSuspendedOutboundDeny_ReachesExecutingOperatorAndRetiresSuspension(t *testing.T) {
	const userID, operatorID, sessionID = "resume-user", "resume-operator", "resume-session"
	infra := setupTestInfrastructure(t, false)
	seedActiveUser(t, infra, userID)
	seedInferenceOperator(t, infra, userID, operatorID, sessionID, false)
	workDir := testutil.OperatorDir(t)
	targetPath := filepath.Join(workDir, constants.TestEvaluationTargetFilename)
	require.NoError(t, os.WriteFile(targetPath, []byte("original"), constants.PermFilePrivate))
	publicKey := startFileEditOperator(t, infra, operatorID, sessionID, workDir)
	dispatcher := NewDispatchService(infra.Logger, infra.Pubsub, infra.StateRootSvc, infra.Auth, constants.PostureRatify, govsvc.NewL1Doctrine(), nil, infra.SignerStore, nil, nil)
	gateway, err := mcp.NewGatewayService(mcp.Dependencies{
		Logger: infra.Logger, Responder: infra.Responder, SuspendedStore: infra.SuspendedStore,
		ScrubbingService: infra.Scrubber, ThreatScanner: govsvc.NewL1Doctrine(),
		Posture: constants.PostureRatify, AuditStore: infra.AuditStore,
		EmbeddedSession: infra.Embedded, OutboundEnvProc: dispatcher,
	})
	require.NoError(t, err)
	payload, err := proto.Marshal(&operatorv1.FileEditRequested{
		FilePath: targetPath, Operation: string(constants.FileOperationReplace),
		ExecutionId: "resume-execution", OldContent: "original", NewContent: "must-never-execute",
	})
	require.NoError(t, err)
	root, err := infra.StateRootSvc.GetCurrentStateRoot(t.Context())
	require.NoError(t, err)
	env, err := BuildGovernanceEnvelope(BuildEnvelopeParams{
		OperatorID: operatorID, OperatorSessionID: sessionID, RequestorUserID: userID,
		EventType: string(constants.Event.Operator.FileEdit.Requested), Payload: payload,
		TargetResource: targetPath, StateMerkleRoot: root,
		Posture: constants.PostureDoctrine, Doctrine: govsvc.NewL1Doctrine(),
	})
	require.NoError(t, err)
	// The dispatch admission gate stays closed to new ratify mutations. This
	// regression exercises routing of a prebuilt suspended envelope only.
	env.Posture = string(constants.PostureRatify)
	_, err = gateway.SuspendGovernanceEnvelope(t.Context(), env)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	receipt, err := gateway.DenySuspendedTransaction(ctx, env.Id, userID)
	require.NoError(t, err)
	require.Equal(t, env.Id, receipt.GetTransactionId())
	require.Equal(t, env.TransactionHash, receipt.GetTransactionHash())
	require.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED, receipt.GetRejectionCode())
	require.NoError(t, govsvc.VerifyActionReceiptSignature(receipt, publicKey))
	require.NoError(t, govsvc.VerifyReceiptPersistenceAttestation(receipt, publicKey))
	persisted, err := infra.AuditStore.GetActionReceipt(env.Id)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	require.Equal(t, sessionID, persisted.OperatorSessionID)
	require.Equal(t, receipt.Signature, persisted.ActionReceipt.Signature)
	_, pending, err := gateway.GetSuspendedTransaction(t.Context(), env.Id)
	require.NoError(t, err)
	require.False(t, pending)
	body, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	require.Equal(t, "original", string(body))
	_, err = gateway.DenySuspendedTransaction(ctx, env.Id, userID)
	require.ErrorIs(t, err, constants.ErrTransactionExpired, "a settled suspension cannot be submitted twice")
}

func TestDispatchRatify_OutboundHumanDecisionReturnsOriginalVerifiedResult(t *testing.T) {
	runOutboundHumanDecisions(t, constants.PostureRatify, []string{"cli", "passkey", "deny", "inference-passkey"})
}

// TestDispatchNotary_OutboundHumanDecisionWithDeliberatedL2ReturnsOriginalVerifiedResult
// proves the blocking outbound approval path under notary with real L2: a 2-of-3
// Ed25519 consensus set deliberates through the bound HTTPS consensus application
// before the dispatch suspends, the executing Operator's L4 verifies both L2 and
// L3, and the human decision resolves the original Dispatch call.
func TestDispatchNotary_OutboundHumanDecisionWithDeliberatedL2ReturnsOriginalVerifiedResult(t *testing.T) {
	runOutboundHumanDecisions(t, constants.PostureNotary, []string{"cli", "passkey", "deny"})
}

// recordingPendingDispatchResumer forwards to the real coordinator and keeps the
// last envelope the Gateway handed back for the waiting dispatch.
type recordingPendingDispatchResumer struct {
	inner mcp.PendingDispatchResumer
	mu    sync.Mutex
	last  *commonv1.GovernanceEnvelope
}

func (r *recordingPendingDispatchResumer) ResumePendingDispatch(ctx context.Context, env *commonv1.GovernanceEnvelope) (*operatorv1.ActionReceipt, bool, error) {
	r.mu.Lock()
	r.last = proto.Clone(env).(*commonv1.GovernanceEnvelope)
	r.mu.Unlock()
	return r.inner.ResumePendingDispatch(ctx, env)
}

func (r *recordingPendingDispatchResumer) resumed() *commonv1.GovernanceEnvelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// runOutboundHumanDecisions drives each human decision mechanism against a
// blocking outbound dispatch under posture. Under notary the Gateway
// deliberates through a real 2-of-3 consensus set and the Operator enforces L2
// next to L3; under ratify L2 is not enforced.
func runOutboundHumanDecisions(t *testing.T, posture constants.Posture, mechanisms []string) {
	for _, mechanism := range mechanisms {
		t.Run(mechanism, func(t *testing.T) {
			const userID, operatorID, sessionID, cliSessionID = "approval-user", "approval-operator", "approval-session", "human-cli-session"
			infra := setupTestInfrastructure(t, false)
			seedActiveUser(t, infra, userID)
			isInference := mechanism == "inference-passkey"
			seedInferenceOperator(t, infra, userID, operatorID, sessionID, isInference)
			rootPEM, err := infra.PKI.RootCAPEM()
			require.NoError(t, err)
			roots := x509.NewCertPool()
			require.True(t, roots.AppendCertsFromPEM(rootPEM))
			notary, err := govsvc.NewOutboundL3Notary(roots)
			require.NoError(t, err)
			enforcesL2 := posture.RequiresL2()
			var deliberator L2ConsensusDeliberator
			var consensus *consensustest.Setup
			var operatorPolicies govsvc.L2ConsensusPolicyStore
			if enforcesL2 {
				consensus = consensustest.SetupConsensus(t, infra.ConsensusStore, infra.SignerStore, "notary-consensus", 3, 2, 2)
				deliberator = infra.ConsensusStore.NewConsensusAppDeliberator(consensus.Server.Client())
				operatorPolicies = infra.ConsensusStore
			}
			workDir := testutil.OperatorDir(t)
			target := filepath.Join(workDir, constants.TestEvaluationTargetFilename)
			require.NoError(t, os.WriteFile(target, []byte("original"), constants.PermFilePrivate))
			var publicKey ed25519.PublicKey
			var backend *boundaryInferenceBackend
			if isInference {
				var keyID string
				backend, keyID = startInferenceOperator(t, infra, infra.StateRootSvc, operatorID, sessionID)
				publicKey, err = hex.DecodeString(keyID)
				require.NoError(t, err)
			} else {
				publicKey = startFileEditOperatorWithGovernance(t, infra, operatorID, sessionID, workDir, notary, operatorPolicies)
			}
			passkey := testutil.NewSoftwarePasskey(t)
			user := &models.User{ID: userID, Status: constants.UserStatusActive, PasskeyCredentials: []models.PasskeyCredential{passkey.Credential(t)}}
			userBytes, err := json.Marshal(user)
			require.NoError(t, err)
			require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionUsers), userID, userBytes))
			passkeyService, err := NewPasskeyService(infra.DocStore, infra.Logger, &PasskeyConfig{RpID: "localhost"})
			require.NoError(t, err)
			preparer, err := NewOutboundApprovalService(govsvc.NewGatewayL3Notary(NewCLISessionVerifier(infra.DocStore, infra.PKI, infra.Logger, infra.UserSvc, infra.CLISessionSvc), passkeyService, infra.Logger), passkeyService)
			require.NoError(t, err)
			suspender, err := mcp.NewEnvelopeSuspender(infra.SuspendedStore, infra.AuditStore, posture, "")
			require.NoError(t, err)
			coordinator, err := NewDispatchApprovalCoordinator(suspender)
			require.NoError(t, err)
			resumer := &recordingPendingDispatchResumer{inner: coordinator}
			dispatcher := NewDispatchService(infra.Logger, infra.Pubsub, infra.StateRootSvc, infra.Auth, posture, govsvc.NewL1Doctrine(), deliberator, infra.SignerStore, nil, coordinator)
			gateway, err := mcp.NewGatewayService(mcp.Dependencies{
				Logger: infra.Logger, Responder: infra.Responder, SuspendedStore: infra.SuspendedStore, ScrubbingService: infra.Scrubber,
				ThreatScanner: govsvc.NewL1Doctrine(), Posture: posture, AuditStore: infra.AuditStore, EmbeddedSession: infra.Embedded,
				OutboundEnvProc: dispatcher, OutboundProofPreparer: preparer, PendingDispatchResumer: resumer,
			})
			require.NoError(t, err)
			payload, err := proto.Marshal(&operatorv1.FileEditRequested{FilePath: target, Operation: string(constants.FileOperationReplace), ExecutionId: "human-approved-mutation", OldContent: "original", NewContent: "approved"})
			require.NoError(t, err)
			eventType := constants.Event.Operator.FileEdit.Requested
			if isInference {
				eventType = constants.Event.Operator.Inference.Requested
				payload, err = proto.Marshal(&operatorv1.InferenceRequested{RequestSchemaVersion: constants.InferenceRequestSchemaVersion, ProviderAttemptId: "approved-provider-attempt", Role: operatorv1.ModelRole_MODEL_ROLE_PRIMARY, Model: "primary-boundary", Messages: []*operatorv1.InferenceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "boundary prompt"}}}}}})
				require.NoError(t, err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			type outcome struct {
				result *DispatchResult
				err    error
			}
			resultCh := make(chan outcome, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				result, err := dispatcher.Dispatch(ctx, DispatchRequest{TargetOperatorSessionID: sessionID, RequestorUserID: userID, EventType: string(eventType), Payload: payload, TargetResource: target, Timeout: 5 * time.Second})
				resultCh <- outcome{result, err}
			}()
			defer func() { cancel(); <-finished }()
			var suspended *models.SuspendedTransaction
			require.Eventually(t, func() bool {
				select {
				case early := <-resultCh:
					t.Errorf("dispatch ended before suspending for a human decision: %v", early.err)
					return true
				default:
				}
				rows, err := infra.SuspendedStore.ListSuspendedTransactions(ctx, userID)
				if err != nil || len(rows) != 1 {
					return false
				}
				suspended = rows[0]
				return true
			}, 5*time.Second, 10*time.Millisecond)
			require.NotNil(t, suspended)
			select {
			case <-resultCh:
				t.Fatal("dispatch returned before a human decision")
			default:
			}
			body, err := os.ReadFile(target)
			require.NoError(t, err)
			require.Equal(t, "original", string(body))
			suspendedEnvelope := &commonv1.GovernanceEnvelope{}
			require.NoError(t, protojson.Unmarshal(suspended.Envelope, suspendedEnvelope))
			require.Equal(t, string(posture), suspendedEnvelope.GetPosture())
			if enforcesL2 {
				// Deliberation ran before the dispatch suspended: the parked
				// envelope already carries a quorum of independent, valid votes.
				l2 := suspendedEnvelope.GetGovernance().GetL2()
				require.NotNil(t, l2, "suspended envelope must carry the deliberated L2 proof")
				require.Equal(t, consensus.ConsensusID, l2.GetConsensusSetId())
				require.Len(t, l2.GetVotes(), len(consensus.Members))
				voted := map[string]struct{}{}
				for _, vote := range l2.GetVotes() {
					var member *consensustest.Member
					for i := range consensus.Members {
						if consensus.Members[i].AppID == vote.GetSignerKeyId() {
							member = &consensus.Members[i]
						}
					}
					require.NotNil(t, member, "vote from unknown signer %s", vote.GetSignerKeyId())
					require.True(t, vote.GetDecision())
					signature, err := hex.DecodeString(vote.GetConsensusSignature())
					require.NoError(t, err)
					require.True(t, ed25519.Verify(member.PrivateKey.Public().(ed25519.PublicKey), []byte(suspended.TransactionHash+"|true"), signature))
					voted[vote.GetSignerKeyId()] = struct{}{}
				}
				require.Len(t, voted, 2, "a 2-of-3 quorum needs two distinct signers")
			} else {
				require.Nil(t, suspendedEnvelope.GetGovernance().GetL2(), "ratify does not deliberate L2")
			}
			pendingL3 := suspendedEnvelope.GetGovernance().GetL3()
			require.True(t, pendingL3.GetPendingDispatch())
			require.Nil(t, pendingL3.GetProof(), "no L3 proof exists before the human acts")
			require.False(t, pendingL3.GetDenied())
			var receipt *operatorv1.ActionReceipt
			if mechanism == "deny" {
				receipt, err = gateway.DenySuspendedTransaction(ctx, suspended.TransactionHash, userID)
			} else {
				var proof *commonv1.L3Proof
				if mechanism == "passkey" || isInference {
					assertion := passkey.Assert(t, "localhost", []byte(suspended.TransactionHash))
					proof = &commonv1.L3Proof{CredentialId: assertion.ID, ClientDataJson: assertion.ClientDataJSON, AuthenticatorData: assertion.AuthenticatorData, Signature: assertion.Signature}
				} else {
					key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
					require.NoError(t, err)
					csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
					require.NoError(t, err)
					certPEM, chainPEM, err := infra.PKI.SignCSR(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})), constants.LeafTypeCLI, "org", operatorID, userID, cliSessionID, "")
					require.NoError(t, err)
					keyDER, err := x509.MarshalECPrivateKey(key)
					require.NoError(t, err)
					certificate, err := tls.X509KeyPair([]byte(certPEM+chainPEM), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
					require.NoError(t, err)
					leaf, err := x509.ParseCertificate(certificate.Certificate[0])
					require.NoError(t, err)
					session := &models.CLISession{ID: cliSessionID, UserID: userID, IsActive: true, ExpiresAt: time.Now().Add(time.Hour), CertFingerprint: models.SHA256Hex(leaf.Raw), CertSerial: leaf.SerialNumber.String()}
					sessionBytes, err := json.Marshal(session)
					require.NoError(t, err)
					require.NoError(t, infra.DocStore.DocSet(ctx, marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, sessionBytes))
					proof, err = approvals.SignCLIApproval(suspended.TransactionHash, &certificate)
					require.NoError(t, err)
					proof.CliIntermediatesDer = certificate.Certificate[1:]
				}
				bad := proto.Clone(proof).(*commonv1.L3Proof)
				if mechanism == "passkey" || isInference {
					bad.Signature = "AA"
				} else {
					bad.CliSignature = "00"
				}
				_, err = gateway.ResumeWithL3Proof(ctx, suspended.TransactionHash, userID, bad)
				require.Error(t, err, "invalid human proof must not dispatch")
				receipt, err = gateway.ResumeWithL3Proof(ctx, suspended.TransactionHash, userID, proof)
			}
			require.NoError(t, err)
			require.NoError(t, govsvc.VerifyActionReceiptSignature(receipt, publicKey))
			require.NoError(t, govsvc.VerifyReceiptPersistenceAttestation(receipt, publicKey))
			var original outcome
			select {
			case original = <-resultCh:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// Resume changes only the L3 decision: every other admitted fact,
			// including the deliberated L2 proof, reaches the Operator unchanged.
			resumedEnvelope := resumer.resumed()
			require.NotNil(t, resumedEnvelope)
			expectedResume := proto.Clone(suspendedEnvelope).(*commonv1.GovernanceEnvelope)
			expectedResume.Governance.L3 = resumedEnvelope.GetGovernance().GetL3()
			require.True(t, proto.Equal(expectedResume, resumedEnvelope), "resume must change only L3 metadata")
			if enforcesL2 {
				require.True(t, proto.Equal(suspendedEnvelope.GetGovernance().GetL2(), resumedEnvelope.GetGovernance().GetL2()))
			}
			if mechanism == "deny" {
				require.ErrorIs(t, original.err, constants.ErrDispatchGovernanceRejected)
				require.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED, receipt.GetRejectionCode())
				require.True(t, resumedEnvelope.GetGovernance().GetL3().GetDenied())
			} else {
				require.NoError(t, original.err)
				require.NotNil(t, original.result.ResultEnvelope)
				require.Equal(t, receipt.Signature, original.result.Receipt.Signature)
				require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus())
				if enforcesL2 {
					require.Equal(t, operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, receipt.GetL2Status())
					require.Equal(t, operatorv1.L2Status_L2_STATUS_REQUIRED_VALID, original.result.Receipt.GetL2Status())
				} else {
					require.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, receipt.GetL2Status())
				}
				require.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, receipt.GetL3Status())
				require.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, original.result.Receipt.GetL3Status())
				if isInference {
					require.NotNil(t, original.result.InferenceResult)
					require.Equal(t, original.result.InferenceResult.ResultDigest, receipt.ResultSummary)
					calls, _ := backend.snapshot()
					require.Equal(t, 1, calls)
				}
			}
			persisted, err := infra.AuditStore.GetActionReceipt(suspended.TransactionHash)
			require.NoError(t, err)
			require.NotNil(t, persisted)
			require.Equal(t, sessionID, persisted.OperatorSessionID)
			_, pending, err := gateway.GetSuspendedTransaction(ctx, suspended.TransactionHash)
			require.NoError(t, err)
			require.False(t, pending)
			body, err = os.ReadFile(target)
			require.NoError(t, err)
			if mechanism == "deny" || isInference {
				require.Equal(t, "original", string(body))
			} else {
				require.Equal(t, "approved", string(body))
			}
			_, err = gateway.ResumeWithL3Proof(ctx, suspended.TransactionHash, userID, &commonv1.L3Proof{})
			require.ErrorIs(t, err, constants.ErrTransactionExpired)
		})
	}
}
